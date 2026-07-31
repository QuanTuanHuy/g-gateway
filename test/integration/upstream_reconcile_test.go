package integration_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuanTuanHuy/g-gateway/internal/config"
	"github.com/QuanTuanHuy/g-gateway/internal/model"
	"github.com/QuanTuanHuy/g-gateway/internal/tlsmaterial"
	"github.com/QuanTuanHuy/g-gateway/internal/upstream"
)

func TestUpstreamReconcileWeightOnlyUpdateReusesWarmConnections(t *testing.T) {
	upstreamA := newCountedUpstream(t, "A")
	upstreamB := newCountedUpstream(t, "B")
	resources := balancedResources(upstreamA.URL(), upstreamB.URL(), 1, 1)
	instance, addresses := startSnapshotGateway(t, resources)
	target := "http://" + loopback(t, addresses.HTTP) + "/hello"
	client := newReconcileClient(t)

	assertEndpointSet(t, requestEndpoints(t, client, target, 2), "A", "B")
	beforeA, beforeB := upstreamA.AcceptedConnections(), upstreamB.AcceptedConnections()

	reweighted := balancedResources(upstreamA.URL(), upstreamB.URL(), 7, 1)
	if err := instance.Apply(2, reweighted); err != nil {
		t.Fatalf("Apply(2) error = %v", err)
	}
	assertEndpointSet(t, requestEndpoints(t, client, target, 8), "A", "B")
	if got := upstreamA.AcceptedConnections(); got != beforeA {
		t.Fatalf("upstream A connections after weight update = %d, want %d", got, beforeA)
	}
	if got := upstreamB.AcceptedConnections(); got != beforeB {
		t.Fatalf("upstream B connections after weight update = %d, want %d", got, beforeB)
	}
}

func TestUpstreamReconcileUnrelatedChangePreservesWarmPool(t *testing.T) {
	upstreamA := newCountedUpstream(t, "A")
	upstreamACanary := newCountedUpstream(t, "A-canary")
	upstreamB := newCountedUpstream(t, "B")
	resources := isolatedResources(upstreamA.URL(), "", upstreamB.URL(), time.Second)
	instance, addresses := startSnapshotGateway(t, resources)
	targetB := "http://" + loopback(t, addresses.HTTP) + "/b"
	client := newReconcileClient(t)

	if got := requestEndpoint(t, client, targetB); got != "B" {
		t.Fatalf("warm response = %q, want B", got)
	}
	before := upstreamB.AcceptedConnections()

	for rotation := 1; rotation <= 20; rotation++ {
		changed := isolatedResources(
			upstreamA.URL(),
			upstreamACanary.URL(),
			upstreamB.URL(),
			time.Second+time.Duration(rotation)*time.Millisecond,
		)
		if err := instance.Apply(uint64(rotation+1), changed); err != nil {
			t.Fatalf("Apply(%d) error = %v", rotation+1, err)
		}
		if got := requestEndpoint(t, client, targetB); got != "B" {
			t.Fatalf("response after rotation %d = %q, want B", rotation, got)
		}
		if got := upstreamB.AcceptedConnections(); got != before {
			t.Fatalf(
				"upstream B connections after rotation %d = %d, want %d",
				rotation,
				got,
				before,
			)
		}
	}
}

func TestUpstreamReconcileRejectedApplyKeepsLastKnownGood(t *testing.T) {
	upstreamA := newCountedUpstream(t, "A")
	resources := singleUpstreamResources(upstreamA.URL(), "1")
	instance, addresses := startSnapshotGateway(t, resources)
	target := "http://" + loopback(t, addresses.HTTP) + "/hello"
	client := newReconcileClient(t)
	assertRevisionEndpoint(t, client, target, "A", "1")

	duplicate := model.CloneResourceSet(resources)
	duplicate.Upstreams[0].Endpoints = append(
		duplicate.Upstreams[0].Endpoints,
		model.Endpoint{URL: upstreamA.URL() + "/", Weight: 1},
	)
	assertRejectedApplyKeepsRevision(
		t, instance, client, target, 2, duplicate, "UPSTREAM_ENDPOINT_DUPLICATE",
	)

	allZero := model.CloneResourceSet(resources)
	allZero.Upstreams[0].Endpoints[0].Weight = 0
	assertRejectedApplyKeepsRevision(
		t, instance, client, target, 3, allZero, "UPSTREAM_NO_ACTIVE_ENDPOINT",
	)

	invalidPlugin := model.CloneResourceSet(resources)
	invalidPlugin.Routes[0].Plugins = append(invalidPlugin.Routes[0].Plugins, model.PluginAttachment{
		Name:    "does-not-exist",
		Enabled: true,
	})
	assertRejectedApplyKeepsRevision(
		t, instance, client, target, 4, invalidPlugin, "PLUGIN_COMPILE_FAILED",
	)

	assertRejectedApplyKeepsRevision(
		t,
		instance,
		client,
		target,
		5,
		balancerBudgetResources(),
		"BALANCER_BUDGET_EXCEEDED",
	)
}

func TestHTTP2StreamSurvivesTLSMaterialRotation(t *testing.T) {
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var requests atomic.Int32
	var connections atomic.Int32
	upstreamServer := httptest.NewUnstartedServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		if request.ProtoMajor != 2 {
			t.Errorf("upstream protocol=%s, want HTTP/2", request.Proto)
		}
		if requests.Add(1) != 1 {
			_, _ = io.WriteString(writer, "revision-2\n")
			return
		}
		_, _ = io.WriteString(writer, "revision-1-first\n")
		writer.(http.Flusher).Flush()
		close(firstStarted)
		<-releaseFirst
		_, _ = io.WriteString(writer, "revision-1-second\n")
	}))
	upstreamServer.EnableHTTP2 = true
	upstreamServer.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	upstreamServer.StartTLS()
	t.Cleanup(upstreamServer.Close)
	rootPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: upstreamServer.Certificate().Raw,
	})
	firstBundle, err := tlsmaterial.NewTrustBundle("stream-roots", rootPEM)
	if err != nil {
		t.Fatal(err)
	}
	resources := tlsHTTP2StreamResources(upstreamServer.URL, firstBundle)
	instance, addresses := startSnapshotGateway(t, resources)
	target := "http://" + loopback(t, addresses.HTTP) + "/stream"

	firstResponse, err := http.Get(target)
	if err != nil {
		t.Fatal(err)
	}
	firstReader := bufio.NewReader(firstResponse.Body)
	firstLine, err := firstReader.ReadString('\n')
	if err != nil || firstLine != "revision-1-first\n" {
		t.Fatalf("first stream line=%q err=%v", firstLine, err)
	}
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("revision-1 stream did not start")
	}

	unrelatedCertificateFile, _ := writeCertificatePair(t)
	unrelatedPEM, err := os.ReadFile(unrelatedCertificateFile)
	if err != nil {
		t.Fatal(err)
	}
	rotatedBundle, err := tlsmaterial.NewTrustBundle(
		"stream-roots",
		append(append([]byte(nil), rootPEM...), unrelatedPEM...),
	)
	if err != nil {
		t.Fatal(err)
	}
	rotated := model.CloneResourceSet(resources)
	rotated.TrustBundles[0] = rotatedBundle
	if err := instance.Apply(2, rotated); err != nil {
		t.Fatal(err)
	}

	secondResponse, err := http.Get(target)
	if err != nil {
		t.Fatal(err)
	}
	secondBody, err := io.ReadAll(secondResponse.Body)
	_ = secondResponse.Body.Close()
	if err != nil || string(secondBody) != "revision-2\n" {
		t.Fatalf("revision-2 response=%q err=%v", secondBody, err)
	}
	if got := connections.Load(); got < 2 {
		t.Fatalf("upstream HTTP/2 connections=%d, want a new material generation", got)
	}

	close(releaseFirst)
	remaining, err := io.ReadAll(firstReader)
	_ = firstResponse.Body.Close()
	if err != nil || string(remaining) != "revision-1-second\n" {
		t.Fatalf("revision-1 remaining stream=%q err=%v", remaining, err)
	}
}

func TestMaterialReloadFailureKeepsLastKnownGood(t *testing.T) {
	upstreamServer := httptest.NewUnstartedServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		if request.ProtoMajor != 2 {
			t.Errorf("upstream protocol=%s, want HTTP/2", request.Proto)
		}
		_, _ = io.WriteString(writer, "trusted")
	}))
	upstreamServer.EnableHTTP2 = true
	upstreamServer.StartTLS()
	t.Cleanup(upstreamServer.Close)
	rootPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: upstreamServer.Certificate().Raw,
	})
	certificateFile, privateKeyFile := writeCertificatePair(t)
	directory := t.TempDir()
	caFile := filepath.Join(directory, "upstream-ca.pem")
	configFile := filepath.Join(directory, "gateway.yaml")
	if err := os.WriteFile(caFile, rootPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	document := phase3C1ReloadDocument(
		certificateFile,
		privateKeyFile,
		caFile,
		upstreamServer.URL,
		"1",
	)
	if err := os.WriteFile(configFile, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	_, revision1, err := config.Load(configFile)
	if err != nil {
		t.Fatal(err)
	}
	instance, addresses := startSnapshotGateway(t, revision1)
	target := "http://" + loopback(t, addresses.HTTP) + "/reload"
	assertMaterialRevision(t, target, "1")

	unrelatedCertificateFile, _ := writeCertificatePair(t)
	unrelatedPEM, err := os.ReadFile(unrelatedCertificateFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		caFile,
		append(append([]byte(nil), rootPEM...), unrelatedPEM...),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	document = phase3C1ReloadDocument(
		certificateFile,
		privateKeyFile,
		caFile,
		upstreamServer.URL,
		"2",
	)
	if err := os.WriteFile(configFile, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	_, revision2, err := config.Load(configFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := instance.Apply(2, revision2); err != nil {
		t.Fatal(err)
	}
	assertMaterialRevision(t, target, "2")

	failures := []struct {
		name   string
		mutate func() error
	}{
		{
			name: "malformed",
			mutate: func() error {
				return os.WriteFile(caFile, []byte("not PEM"), 0o600)
			},
		},
		{
			name: "oversized",
			mutate: func() error {
				return os.WriteFile(
					caFile,
					bytes.Repeat([]byte{'x'}, int(tlsmaterial.MaxCAFileBytes)+1),
					0o600,
				)
			},
		},
		{
			name: "missing",
			mutate: func() error {
				return os.Remove(caFile)
			},
		},
	}
	for _, failure := range failures {
		t.Run(failure.name, func(t *testing.T) {
			if err := os.WriteFile(caFile, rootPEM, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := failure.mutate(); err != nil {
				t.Fatal(err)
			}
			if _, _, err := config.Load(configFile); err == nil {
				t.Fatal("material reload unexpectedly succeeded")
			}
			assertMaterialRevision(t, target, "2")
		})
	}
}

func phase3C1ReloadDocument(
	certificateFile, privateKeyFile, caFile, endpoint, revision string,
) string {
	return fmt.Sprintf(`api_version: gateway/v1alpha5

runtime:
  max_retired_snapshots: 64

listeners:
  http:
    address: "127.0.0.1:18080"
  https:
    address: "127.0.0.1:18443"
    certificate_file: %s
    private_key_file: %s
  admin:
    address: "127.0.0.1:19090"

server:
  read_header_timeout: 1s
  idle_timeout: 1m
  shutdown_timeout: 3s
  max_header_bytes: 1048576
  max_request_body_bytes: 1048576

telemetry:
  request_metrics_enabled: false
  profiling_enabled: false

trust_bundles:
  - id: reload-roots
    ca_file: %s

certificates: []

routes:
  - id: reload
    match:
      path: /reload
      methods: [GET]
    upstream_ref: reload
    plugins:
      - name: header-rewrite
        enabled: true
        config:
          response:
            set:
              X-Revision: "%s"

services: []

upstreams:
  - id: reload
    endpoints:
      - url: %s
        weight: 1
    balancer:
      type: weighted_round_robin
    transport:
      protocol: http2
      tls:
        trust_bundle_ref: reload-roots
      dial_timeout: 1s
      response_header_timeout: 1s
      idle_connection_timeout: 1m
      max_idle_connections: 8
      max_idle_connections_per_host: 8
`,
		filepath.ToSlash(certificateFile),
		filepath.ToSlash(privateKeyFile),
		filepath.ToSlash(caFile),
		revision,
		endpoint,
	)
}

func assertMaterialRevision(t *testing.T, target, wantRevision string) {
	t.Helper()
	response, err := http.Get(target)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK ||
		string(body) != "trusted" ||
		response.Header.Get("X-Revision") != wantRevision {
		t.Fatalf(
			"material response=%d %q revision=%q, want 200 trusted/%s",
			response.StatusCode,
			body,
			response.Header.Get("X-Revision"),
			wantRevision,
		)
	}
}

func tlsHTTP2StreamResources(
	endpoint string,
	bundle *tlsmaterial.TrustBundle,
) model.ResourceSet {
	noTotalTimeout := time.Duration(0)
	return model.ResourceSet{
		Routes: []model.Route{{
			ID: "stream",
			Match: model.RouteMatch{
				Path:    "/stream",
				Methods: []string{http.MethodGet},
			},
			UpstreamRef: "stream",
			Resilience: model.RouteResiliencePolicy{
				TotalTimeout: &noTotalTimeout,
			},
		}},
		Upstreams: []model.Upstream{{
			ID:        "stream",
			Endpoints: []model.Endpoint{{URL: endpoint, Weight: 1}},
			Balancer:  model.BalancerPolicy{Type: model.BalancerWeightedRoundRobin},
			Transport: model.TransportConfig{
				Protocol: model.TransportProtocolHTTP2,
				TLS: &model.UpstreamTLSPolicy{
					TrustBundleRef: "stream-roots",
				},
				DialTimeout:               time.Second,
				ResponseHeaderTimeout:     time.Second,
				IdleConnectionTimeout:     time.Minute,
				MaxIdleConnections:        8,
				MaxIdleConnectionsPerHost: 8,
			},
		}},
		TrustBundles: []*tlsmaterial.TrustBundle{bundle},
	}
}

type countedUpstream struct {
	server   *httptest.Server
	accepted atomic.Int64
}

func newCountedUpstream(t *testing.T, body string) *countedUpstream {
	t.Helper()
	upstream := &countedUpstream{}
	upstream.server = httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, body)
	}))
	upstream.server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			upstream.accepted.Add(1)
		}
	}
	upstream.server.Start()
	t.Cleanup(upstream.server.Close)
	return upstream
}

func (u *countedUpstream) URL() string {
	return u.server.URL
}

func (u *countedUpstream) AcceptedConnections() int64 {
	return u.accepted.Load()
}

func newReconcileClient(t *testing.T) *http.Client {
	t.Helper()
	transport := &http.Transport{
		MaxIdleConns:        8,
		MaxIdleConnsPerHost: 8,
	}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport}
}

func requestEndpoints(t *testing.T, client *http.Client, target string, count int) map[string]bool {
	t.Helper()
	endpoints := make(map[string]bool)
	for range count {
		endpoints[requestEndpoint(t, client, target)] = true
	}
	return endpoints
}

func requestEndpoint(t *testing.T, client *http.Client, target string) string {
	t.Helper()
	response, err := client.Get(target)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET %s status = %d, body = %q", target, response.StatusCode, body)
	}
	return string(body)
}

func assertEndpointSet(t *testing.T, got map[string]bool, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("selected endpoints = %v, want %v", got, want)
	}
	for _, endpoint := range want {
		if !got[endpoint] {
			t.Fatalf("selected endpoints = %v, missing %q", got, endpoint)
		}
	}
}

func assertRevisionEndpoint(
	t *testing.T,
	client *http.Client,
	target string,
	wantEndpoint string,
	wantRevision string,
) {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK ||
		string(body) != wantEndpoint ||
		response.Header.Get("X-Revision") != wantRevision {
		t.Fatalf(
			"response = status %d endpoint %q revision %q, want 200/%s/%s",
			response.StatusCode,
			body,
			response.Header.Get("X-Revision"),
			wantEndpoint,
			wantRevision,
		)
	}
}

func assertRejectedApplyKeepsRevision(
	t *testing.T,
	instance interface {
		Apply(uint64, model.ResourceSet) error
	},
	client *http.Client,
	target string,
	revision uint64,
	resources model.ResourceSet,
	code string,
) {
	t.Helper()
	err := instance.Apply(revision, resources)
	if err == nil || !strings.Contains(err.Error(), code) {
		t.Fatalf("Apply(%d) error = %v, want %s", revision, err, code)
	}
	assertRevisionEndpoint(t, client, target, "A", "1")
}

func balancedResources(endpointA, endpointB string, weightA, weightB uint32) model.ResourceSet {
	return model.ResourceSet{
		Routes: []model.Route{{
			ID:          "balanced",
			Match:       model.RouteMatch{Path: "/hello", Methods: []string{http.MethodGet}},
			UpstreamRef: "balanced",
		}},
		Upstreams: []model.Upstream{{
			ID: "balanced",
			Endpoints: []model.Endpoint{
				{URL: endpointA, Weight: weightA},
				{URL: endpointB, Weight: weightB},
			},
			Balancer:  model.BalancerPolicy{Type: model.BalancerWeightedRoundRobin},
			Transport: reconcileTransport(time.Second),
		}},
	}
}

func isolatedResources(endpointA, canaryA, endpointB string, timeoutA time.Duration) model.ResourceSet {
	endpointsA := []model.Endpoint{{URL: endpointA, Weight: 1}}
	if canaryA != "" {
		endpointsA = append(endpointsA, model.Endpoint{URL: canaryA, Weight: 1})
	}
	return model.ResourceSet{
		Routes: []model.Route{
			{
				ID:          "route-a",
				Match:       model.RouteMatch{Path: "/a", Methods: []string{http.MethodGet}},
				UpstreamRef: "upstream-a",
			},
			{
				ID:          "route-b",
				Match:       model.RouteMatch{Path: "/b", Methods: []string{http.MethodGet}},
				UpstreamRef: "upstream-b",
			},
		},
		Upstreams: []model.Upstream{
			{
				ID:        "upstream-a",
				Endpoints: endpointsA,
				Balancer:  model.BalancerPolicy{Type: model.BalancerWeightedRoundRobin},
				Transport: reconcileTransport(timeoutA),
			},
			{
				ID:        "upstream-b",
				Endpoints: []model.Endpoint{{URL: endpointB, Weight: 1}},
				Balancer:  model.BalancerPolicy{Type: model.BalancerWeightedRoundRobin},
				Transport: reconcileTransport(time.Second),
			},
		},
	}
}

func singleUpstreamResources(endpoint, marker string) model.ResourceSet {
	resources := balancedResources(endpoint, endpoint, 1, 0)
	resources.Upstreams[0].Endpoints = resources.Upstreams[0].Endpoints[:1]
	resources.Routes[0].Plugins = []model.PluginAttachment{{
		Name:      "header-rewrite",
		Enabled:   true,
		RawConfig: json.RawMessage(`{"response":{"set":{"X-Revision":"` + marker + `"}}}`),
	}}
	return resources
}

func reconcileTransport(responseHeaderTimeout time.Duration) model.TransportConfig {
	return model.TransportConfig{
		DialTimeout:               time.Second,
		ResponseHeaderTimeout:     responseHeaderTimeout,
		IdleConnectionTimeout:     time.Minute,
		MaxIdleConnections:        32,
		MaxIdleConnectionsPerHost: 32,
	}
}

func balancerBudgetResources() model.ResourceSet {
	const upstreamCount = upstream.MaxSnapshotWRRSlots/upstream.MaxWRRSchedule + 1
	resources := model.ResourceSet{
		Routes: []model.Route{{
			ID:          "budget",
			Match:       model.RouteMatch{Path: "/hello", Methods: []string{http.MethodGet}},
			UpstreamRef: "budget-0",
		}},
		Upstreams: make([]model.Upstream, upstreamCount),
	}
	for upstreamIndex := range resources.Upstreams {
		endpoints := make([]model.Endpoint, 10)
		for endpointIndex := range endpoints {
			endpoints[endpointIndex] = model.Endpoint{
				URL: fmt.Sprintf(
					"http://u%d-e%d.example:8080",
					upstreamIndex,
					endpointIndex,
				),
				Weight: uint32(upstream.MaxEndpointWeight - endpointIndex),
			}
		}
		resources.Upstreams[upstreamIndex] = model.Upstream{
			ID:        fmt.Sprintf("budget-%d", upstreamIndex),
			Endpoints: endpoints,
			Balancer:  model.BalancerPolicy{Type: model.BalancerWeightedRoundRobin},
			Transport: reconcileTransport(time.Second),
		}
	}
	return resources
}

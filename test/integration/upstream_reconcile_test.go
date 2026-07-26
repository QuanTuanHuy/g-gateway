package integration_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
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

	changed := isolatedResources(
		upstreamA.URL(),
		upstreamACanary.URL(),
		upstreamB.URL(),
		2*time.Second,
	)
	if err := instance.Apply(2, changed); err != nil {
		t.Fatalf("Apply(2) error = %v", err)
	}
	if got := requestEndpoint(t, client, targetB); got != "B" {
		t.Fatalf("response after unrelated change = %q, want B", got)
	}
	if got := upstreamB.AcceptedConnections(); got != before {
		t.Fatalf("upstream B connections after unrelated change = %d, want %d", got, before)
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

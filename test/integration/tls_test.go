package integration_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuanTuanHuy/g-gateway/internal/config"
	"github.com/QuanTuanHuy/g-gateway/internal/gateway"
	"github.com/QuanTuanHuy/g-gateway/internal/model"
	"github.com/QuanTuanHuy/g-gateway/internal/testupstream"
	"github.com/QuanTuanHuy/g-gateway/internal/tlsmaterial"
)

func TestHTTP2TLSDownstreamUsesHTTP1Upstream(t *testing.T) {
	upstream := httptest.NewServer(testupstream.New(discardLogger()))
	defer upstream.Close()
	addresses := startGateway(t, upstream.URL, "/headers", 1<<20, time.Second)
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetHTTP2(true)
	transport := &http.Transport{
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: true}, // Test certificate is generated per test.
		ForceAttemptHTTP2: true,
		Protocols:         protocols,
	}
	defer transport.CloseIdleConnections()

	response, err := (&http.Client{Transport: transport}).Get("https://" + loopback(t, addresses.HTTPS) + "/headers")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.ProtoMajor != 2 {
		t.Fatalf("downstream protocol=%s, want HTTP/2", response.Proto)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) == "" || !strings.Contains(string(body), "HTTP/1.1") {
		t.Fatalf("upstream response=%q", body)
	}
}

func TestGatewayTLSUsesCustomTrustForHTTPSUpstream(t *testing.T) {
	upstreamServer := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.ProtoMajor != 1 {
			t.Errorf("upstream protocol=%s", request.Proto)
		}
		_, _ = io.WriteString(writer, "secure")
	}))
	defer upstreamServer.Close()
	rootPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: upstreamServer.Certificate().Raw,
	})
	bundle, err := tlsmaterial.NewTrustBundle("private-root", rootPEM)
	if err != nil {
		t.Fatal(err)
	}
	resources := tlsGatewayResources(upstreamServer.URL)
	resources.Upstreams[0].Transport.TLS = &model.UpstreamTLSPolicy{
		TrustBundleRef: "private-root",
	}
	resources.TrustBundles = []*tlsmaterial.TrustBundle{bundle}
	addresses := startGatewayWithTLSResources(t, resources, discardLogger())

	response, err := http.Get("http://" + loopback(t, addresses.HTTP) + "/secure")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || string(body) != "secure" {
		t.Fatalf("response=%d %q", response.StatusCode, body)
	}
}

func TestGatewayTLSFailureRetriesOneDistinctTrustedEndpoint(t *testing.T) {
	fixture := startTLSRetryFixture(t, 0)
	resources := tlsGatewayResources(fixture.badURL)
	resources.Upstreams[0].Endpoints = []model.Endpoint{
		{URL: fixture.badURL, Weight: 1},
		{URL: fixture.goodURL, Weight: 1},
	}
	resources.Upstreams[0].Transport.TLS = &model.UpstreamTLSPolicy{
		TrustBundleRef: "trusted-root",
	}
	resources.Upstreams[0].Retry = model.RetryPolicy{
		MaxAttempts: 2,
		Methods:     []string{http.MethodGet},
		RetryOn: model.RetryOnPolicy{
			ConnectionFailure: true,
		},
		Budget: model.RetryBudgetPolicy{
			RatioPer1000: 1000,
			Burst:        2,
			MaxInflight:  1,
		},
	}
	resources.TrustBundles = []*tlsmaterial.TrustBundle{fixture.bundle}
	addresses := startGatewayWithTLSResources(t, resources, discardLogger())

	response, err := http.Get("http://" + loopback(t, addresses.HTTP) + "/secure")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || string(body) != "trusted" {
		t.Fatalf("response=%d %q", response.StatusCode, body)
	}
	if fixture.badHTTPCalls.Load() != 0 || fixture.goodHTTPCalls.Load() != 1 {
		t.Fatalf(
			"HTTP calls bad=%d good=%d",
			fixture.badHTTPCalls.Load(),
			fixture.goodHTTPCalls.Load(),
		)
	}
}

func TestGatewayTLSRetryHonorsReplayBudgetAndDeadlineGuards(t *testing.T) {
	tests := []struct {
		name          string
		method        string
		ratio         uint16
		totalTimeout  time.Duration
		nonReplayable bool
		wantStatus    int
	}{
		{
			name:          "non replayable body",
			method:        http.MethodPost,
			ratio:         1000,
			nonReplayable: true,
			wantStatus:    http.StatusBadGateway,
		},
		{
			name:       "exhausted retry budget",
			method:     http.MethodGet,
			ratio:      0,
			wantStatus: http.StatusBadGateway,
		},
		{
			name:         "expired total deadline",
			method:       http.MethodGet,
			ratio:        1000,
			totalTimeout: time.Millisecond,
			wantStatus:   http.StatusGatewayTimeout,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			badDelay := time.Duration(0)
			if test.totalTimeout > 0 {
				badDelay = 25 * time.Millisecond
			}
			fixture := startTLSRetryFixture(t, badDelay)
			resources := tlsGatewayResources(fixture.badURL)
			resources.Routes[0].Match.Methods = []string{http.MethodGet, http.MethodPost}
			resources.Upstreams[0].Endpoints = []model.Endpoint{
				{URL: fixture.badURL, Weight: 1},
				{URL: fixture.goodURL, Weight: 1},
			}
			resources.Upstreams[0].Transport.TLS = &model.UpstreamTLSPolicy{
				TrustBundleRef: "trusted-root",
			}
			resources.Upstreams[0].Retry = model.RetryPolicy{
				MaxAttempts: 2,
				Methods:     []string{test.method},
				RetryOn: model.RetryOnPolicy{
					ConnectionFailure:     true,
					ResponseHeaderTimeout: true,
				},
				Budget: model.RetryBudgetPolicy{
					RatioPer1000: test.ratio,
					Burst:        2,
					MaxInflight:  1,
				},
				TotalTimeout: test.totalTimeout,
			}
			resources.TrustBundles = []*tlsmaterial.TrustBundle{fixture.bundle}
			addresses := startGatewayWithTLSResources(t, resources, discardLogger())
			target := "http://" + loopback(t, addresses.HTTP) + "/secure"
			var request *http.Request
			if test.nonReplayable {
				request, _ = http.NewRequest(
					test.method,
					target,
					io.NopCloser(strings.NewReader("payload")),
				)
				request.GetBody = nil
			} else {
				request, _ = http.NewRequest(test.method, target, nil)
			}
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			_ = response.Body.Close()
			if response.StatusCode != test.wantStatus {
				t.Fatalf("status=%d, want %d", response.StatusCode, test.wantStatus)
			}
			if test.nonReplayable && !response.Close {
				t.Fatal("HTTP/1 connection remained reusable after an early upstream failure with unread body")
			}
			if fixture.goodHTTPCalls.Load() != 0 {
				t.Fatalf("unsafe/out-of-budget retry reached good endpoint %d times", fixture.goodHTTPCalls.Load())
			}
		})
	}
}

func TestGatewayRedactsHTTPSUpstreamTLSFailure(t *testing.T) {
	upstreamServer := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer upstreamServer.Close()
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	addresses := startGatewayWithTLSResources(
		t,
		tlsGatewayResources(upstreamServer.URL),
		logger,
	)

	response, err := http.Get("http://" + loopback(t, addresses.HTTP) + "/secure")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var body map[string]string
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusBadGateway ||
		body["code"] != "UPSTREAM_TLS_FAILED" ||
		body["message"] != "upstream TLS failed" {
		t.Fatalf("response=%d body=%v", response.StatusCode, body)
	}
	if strings.Contains(logs.String(), upstreamServer.URL) ||
		!strings.Contains(logs.String(), "class=tls") {
		t.Fatalf("TLS failure log is not bounded/redacted: %q", logs.String())
	}
}

func tlsGatewayResources(endpoint string) model.ResourceSet {
	return model.ResourceSet{
		Routes: []model.Route{{
			ID:          "secure",
			Match:       model.RouteMatch{Path: "/secure", Methods: []string{http.MethodGet}},
			UpstreamRef: "secure",
		}},
		Upstreams: []model.Upstream{{
			ID:        "secure",
			Endpoints: []model.Endpoint{{URL: endpoint, Weight: 1}},
			Balancer:  model.BalancerPolicy{Type: model.BalancerWeightedRoundRobin},
			Transport: model.TransportConfig{
				Protocol:                  model.TransportProtocolHTTP1,
				DialTimeout:               time.Second,
				ResponseHeaderTimeout:     time.Second,
				IdleConnectionTimeout:     time.Minute,
				MaxIdleConnections:        8,
				MaxIdleConnectionsPerHost: 8,
			},
		}},
	}
}

type tlsRetryFixture struct {
	badURL        string
	goodURL       string
	bundle        *tlsmaterial.TrustBundle
	badHTTPCalls  *atomic.Int32
	goodHTTPCalls *atomic.Int32
}

func startTLSRetryFixture(t *testing.T, badHandshakeDelay time.Duration) tlsRetryFixture {
	t.Helper()
	badHTTPCalls := new(atomic.Int32)
	goodHTTPCalls := new(atomic.Int32)
	first := httptest.NewUnstartedServer(nil)
	second := httptest.NewUnstartedServer(nil)
	servers := []*httptest.Server{first, second}
	sort.Slice(servers, func(left, right int) bool {
		return servers[left].Listener.Addr().String() < servers[right].Listener.Addr().String()
	})
	badServer, goodServer := servers[0], servers[1]
	badServer.Config.Handler = http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		badHTTPCalls.Add(1)
		writer.WriteHeader(http.StatusInternalServerError)
	})
	goodServer.Config.Handler = http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		goodHTTPCalls.Add(1)
		_, _ = io.WriteString(writer, "trusted")
	})
	certificateFile, privateKeyFile := writeCertificatePair(t)
	badCertificate, err := tls.LoadX509KeyPair(certificateFile, privateKeyFile)
	if err != nil {
		t.Fatal(err)
	}
	badServer.TLS = &tls.Config{
		Certificates: []tls.Certificate{badCertificate},
		GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
			if badHandshakeDelay > 0 {
				time.Sleep(badHandshakeDelay)
			}
			return nil, nil
		},
	}
	badServer.StartTLS()
	goodServer.StartTLS()
	t.Cleanup(badServer.Close)
	t.Cleanup(goodServer.Close)
	goodRootPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: goodServer.Certificate().Raw,
	})
	bundle, err := tlsmaterial.NewTrustBundle("trusted-root", goodRootPEM)
	if err != nil {
		t.Fatal(err)
	}
	return tlsRetryFixture{
		badURL:        badServer.URL,
		goodURL:       goodServer.URL,
		bundle:        bundle,
		badHTTPCalls:  badHTTPCalls,
		goodHTTPCalls: goodHTTPCalls,
	}
}

func startGatewayWithTLSResources(
	t *testing.T,
	resources model.ResourceSet,
	logger *slog.Logger,
) gateway.Addresses {
	t.Helper()
	certificateFile, privateKeyFile := writeCertificatePair(t)
	bootstrap := config.BootstrapConfig{
		HTTP: config.ListenerConfig{Address: "127.0.0.1:0"},
		HTTPS: config.TLSListenerConfig{
			Address:         "127.0.0.1:0",
			CertificateFile: certificateFile,
			PrivateKeyFile:  privateKeyFile,
		},
		Admin: config.ListenerConfig{Address: "127.0.0.1:0"},
		Server: config.ServerConfig{
			ReadHeaderTimeout:   time.Second,
			IdleTimeout:         time.Minute,
			ShutdownTimeout:     3 * time.Second,
			MaxHeaderBytes:      1 << 20,
			MaxRequestBodyBytes: 1 << 20,
		},
	}
	instance, err := gateway.New(bootstrap, resources, logger)
	if err != nil {
		t.Fatal(err)
	}
	addresses, err := instance.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := instance.Shutdown(ctx); err != nil {
			t.Errorf("Shutdown() error=%v", err)
		}
	})
	return addresses
}

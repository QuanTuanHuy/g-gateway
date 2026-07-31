package proxy

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
	"github.com/QuanTuanHuy/g-gateway/internal/plugin"
	"github.com/QuanTuanHuy/g-gateway/internal/requestctx"
	"github.com/QuanTuanHuy/g-gateway/internal/runtime"
	"github.com/QuanTuanHuy/g-gateway/internal/upstream"
)

var _ requestctx.SnapshotRef = (*runtime.Snapshot)(nil)
var _ requestctx.RuntimeRoute = (*runtime.CompiledRoute)(nil)

func TestRuntimeHandlerReturnsGatewayNotReady(t *testing.T) {
	resources := runtimeProxyResources("http://127.0.0.1:1", "http://127.0.0.1:2")
	handler, _, _ := newRuntimeTestHandler(t, resources, false)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://gateway/users/42?tenant=acme", nil))

	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "GATEWAY_NOT_READY") {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
}

func TestRuntimeHandlerRoutesPluginsAndRevisionSwaps(t *testing.T) {
	upstreamA := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("X-Upstream", "A")
		response.Header().Set("X-Remove-Response", "secret")
		_, _ = response.Write([]byte(request.Header.Get("X-Rewrite-Request")))
	}))
	defer upstreamA.Close()
	upstreamB := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("X-Upstream", "B")
		_, _ = response.Write([]byte("B"))
	}))
	defer upstreamB.Close()

	resources := runtimeProxyResources(upstreamA.URL, upstreamB.URL)
	handler, manager, _ := newRuntimeTestHandler(t, resources, true)
	request := httptest.NewRequest(http.MethodGet, "http://gateway/users/42?tenant=acme", nil)
	request.Header.Set("X-Request-ID", "safe-id")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK ||
		response.Header().Get("X-Upstream") != "A" ||
		response.Header().Get("X-Request-ID") != "safe-id" ||
		response.Header().Get("X-Rewrite-Response") != "response" ||
		response.Header().Get("X-Remove-Response") != "" ||
		response.Body.String() != "request" {
		t.Fatalf("revision 1 response = %d %#v %q", response.Code, response.Header(), response.Body.String())
	}

	revision2 := model.CloneResourceSet(resources)
	revision2.Routes[0].UpstreamRef = "upstream-b"
	if err := manager.Apply(2, revision2); err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://gateway/users/42?tenant=acme", nil))
	if response.Code != http.StatusOK || response.Header().Get("X-Upstream") != "B" || response.Body.String() != "B" {
		t.Fatalf("revision 2 response = %d %#v %q", response.Code, response.Header(), response.Body.String())
	}

	invalid := model.CloneResourceSet(revision2)
	invalid.Routes[0].Plugins = []model.PluginAttachment{{Name: "unknown", Enabled: true}}
	if err := manager.Apply(3, invalid); err == nil {
		t.Fatal("invalid plugin apply unexpectedly succeeded")
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://gateway/users/42?tenant=acme", nil))
	if response.Header().Get("X-Upstream") != "B" {
		t.Fatalf("failed apply changed active behavior: %#v", response.Header())
	}
}

func TestRuntimeHandlerMapsRoutingErrors(t *testing.T) {
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer upstreamServer.Close()
	resources := runtimeProxyResources(upstreamServer.URL, upstreamServer.URL)
	handler, _, _ := newRuntimeTestHandler(t, resources, true)

	tests := []struct {
		name   string
		method string
		target string
		status int
		code   string
		allow  string
	}{
		{name: "not found", method: http.MethodGet, target: "http://gateway/missing", status: 404, code: "ROUTE_NOT_FOUND"},
		{name: "method", method: http.MethodPost, target: "http://gateway/users/42?tenant=acme", status: 405, code: "METHOD_NOT_ALLOWED", allow: "GET"},
		{name: "invalid query", method: http.MethodGet, target: "http://gateway/users/42", status: 400, code: "INVALID_QUERY"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequest(tt.method, tt.target, nil)
			if tt.name == "invalid query" {
				request.URL.RawQuery = "tenant=%zz"
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != tt.status || !strings.Contains(response.Body.String(), tt.code) {
				t.Fatalf("response = %d %s", response.Code, response.Body.String())
			}
			if response.Header().Get("Allow") != tt.allow {
				t.Fatalf("Allow = %q, want %q", response.Header().Get("Allow"), tt.allow)
			}
		})
	}
}

func TestRuntimeHandlerEnforcesTotalDeadlineAfterMatch(t *testing.T) {
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		time.Sleep(100 * time.Millisecond)
		writer.WriteHeader(http.StatusOK)
	}))
	defer upstreamServer.Close()
	resources := runtimeProxyResources(upstreamServer.URL, upstreamServer.URL)
	resources.Upstreams[0].Retry = model.RetryPolicy{
		MaxAttempts:  1,
		Budget:       model.RetryBudgetPolicy{Burst: 10, MaxInflight: 32},
		TotalTimeout: 20 * time.Millisecond,
	}
	handler, _, _ := newRuntimeTestHandler(t, resources, true)
	response := httptest.NewRecorder()
	started := time.Now()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://gateway/users/42?tenant=acme", nil))
	if response.Code != http.StatusGatewayTimeout || !strings.Contains(response.Body.String(), "UPSTREAM_TIMEOUT") {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
	if time.Since(started) > 250*time.Millisecond {
		t.Fatal("total deadline was not enforced")
	}
}

func TestRuntimeHandlerEarlierClientDeadlineWins(t *testing.T) {
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		time.Sleep(100 * time.Millisecond)
		writer.WriteHeader(http.StatusOK)
	}))
	defer upstreamServer.Close()
	resources := runtimeProxyResources(upstreamServer.URL, upstreamServer.URL)
	resources.Upstreams[0].Retry = model.RetryPolicy{
		MaxAttempts:  1,
		Budget:       model.RetryBudgetPolicy{Burst: 10, MaxInflight: 32},
		TotalTimeout: time.Second,
	}
	handler, _, _ := newRuntimeTestHandler(t, resources, true)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	request := httptest.NewRequest(http.MethodGet, "http://gateway/users/42?tenant=acme", nil).WithContext(ctx)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusGatewayTimeout {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
}

func TestRuntimeHandlerLegacyPolicyHasNoTotalDeadline(t *testing.T) {
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		time.Sleep(30 * time.Millisecond)
		writer.WriteHeader(http.StatusOK)
	}))
	defer upstreamServer.Close()
	resources := runtimeProxyResources(upstreamServer.URL, upstreamServer.URL)
	handler, _, _ := newRuntimeTestHandler(t, resources, true)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://gateway/users/42?tenant=acme", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
}

func newRuntimeTestHandler(
	t testing.TB,
	resources model.ResourceSet,
	apply bool,
) (http.Handler, *runtime.Manager, *upstream.Registry) {
	t.Helper()
	upstreamRegistry, err := upstream.NewRegistry(upstream.RegistryOptions{MaxRetiredSnapshots: 64, HealthWorkers: 2, HealthQueueCapacity: 16})
	if err != nil {
		t.Fatal(err)
	}
	pluginRegistry, err := plugin.NewBuiltinRegistry()
	if err != nil {
		t.Fatal(err)
	}
	builder, err := runtime.NewBuilder(pluginRegistry)
	if err != nil {
		t.Fatal(err)
	}
	manager := runtime.NewManager(builder, upstreamRegistry, nil)
	if apply {
		if err := manager.Apply(1, resources); err != nil {
			t.Fatal(err)
		}
	}
	handler, err := NewRuntime(RuntimeOptions{
		Snapshots:           manager,
		MaxRequestBodyBytes: 1024 * 1024,
		Logger:              slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := manager.Close(ctx); err != nil {
			t.Errorf("Manager.Close() error = %v", err)
		}
	})
	return requestctx.Middleware(handler), manager, upstreamRegistry
}

func runtimeProxyResources(firstEndpoint, secondEndpoint string) model.ResourceSet {
	transport := model.TransportConfig{
		DialTimeout:               time.Second,
		ResponseHeaderTimeout:     2 * time.Second,
		IdleConnectionTimeout:     3 * time.Second,
		MaxIdleConnections:        10,
		MaxIdleConnectionsPerHost: 10,
	}
	return model.ResourceSet{
		Routes: []model.Route{{
			ID:          "users",
			UpstreamRef: "upstream-a",
			Match: model.RouteMatch{
				Path:    "/users/{id}",
				Methods: []string{"GET"},
				Queries: []model.Predicate{{
					Name:     "tenant",
					Operator: model.PredicateEquals,
					Values:   []string{"acme"},
				}},
			},
			Plugins: []model.PluginAttachment{
				{Name: "request-id", Enabled: true, RawConfig: json.RawMessage(`{}`)},
				{Name: "header-rewrite", Enabled: true, RawConfig: json.RawMessage(`{
					"request":{"set":{"X-Rewrite-Request":"request"}},
					"response":{
						"remove":["X-Remove-Response"],
						"set":{"X-Rewrite-Response":"response"}
					}
				}`)},
			},
		}},
		Upstreams: []model.Upstream{
			{
				ID:        "upstream-a",
				Endpoints: []model.Endpoint{{URL: firstEndpoint, Weight: 1}},
				Balancer:  model.BalancerPolicy{Type: model.BalancerWeightedRoundRobin},
				Transport: transport,
			},
			{
				ID:        "upstream-b",
				Endpoints: []model.Endpoint{{URL: secondEndpoint, Weight: 1}},
				Balancer:  model.BalancerPolicy{Type: model.BalancerWeightedRoundRobin},
				Transport: transport,
			},
		},
	}
}

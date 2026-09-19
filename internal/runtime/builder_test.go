package runtime

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuanTuanHuy/g-gateway/internal/downstreamtls"
	"github.com/QuanTuanHuy/g-gateway/internal/model"
	"github.com/QuanTuanHuy/g-gateway/internal/plugin"
	"github.com/QuanTuanHuy/g-gateway/internal/requestctx"
	"github.com/QuanTuanHuy/g-gateway/internal/tlsmaterial"
	"github.com/QuanTuanHuy/g-gateway/internal/upstream"
)

func TestBuilderResolvesServiceAndCompilesRoute(t *testing.T) {
	resources := testResources()
	registry, err := plugin.NewBuiltinRegistry()
	if err != nil {
		t.Fatal(err)
	}
	builder, err := NewBuilder(registry)
	if err != nil {
		t.Fatal(err)
	}

	candidate := mustCandidate(t, resources.Upstreams)
	snapshot, err := builder.Build(7, resources, candidate)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "http://api.example.com/users/42", nil)
	match, err := snapshot.Match(request)
	if err != nil {
		t.Fatal(err)
	}
	if !match.Found ||
		match.Route.Meta().ID != "users" ||
		match.Route.ServiceMeta().ID != "users-service" ||
		match.Route.UpstreamMeta().ID != "users-upstream" {
		t.Fatalf("match = %+v", match)
	}
	state := &requestctx.Context{}
	if result := match.Route.RunRequest(state, request); result.Err != nil {
		t.Fatal(result.Err)
	}
	if got := request.Header.Get("X-Scope"); got != "route" {
		t.Fatalf("route plugin did not replace service config: X-Scope=%q", got)
	}

	resources.Routes[0].ID = "mutated"
	resources.Services[0].ID = "mutated"
	resources.Upstreams[0].ID = "mutated"
	if match.Route.Meta().ID != "users" ||
		match.Route.ServiceMeta().ID != "users-service" ||
		match.Route.UpstreamMeta().ID != "users-upstream" {
		t.Fatal("published snapshot aliases input")
	}
	if snapshot.Revision() != 7 {
		t.Fatalf("Revision() = %d", snapshot.Revision())
	}
}

func TestBuilderStoresCompiledDownstreamTLS(t *testing.T) {
	resources := testResources()
	resources.Certificates = []*tlsmaterial.Certificate{
		runtimeTestCertificate(t, "default", 11, []string{"default.example"}),
		runtimeTestCertificate(t, "exact", 12, []string{"api.example.com"}),
	}
	resources.DownstreamTLS = &model.DownstreamTLSPolicy{
		DefaultCertificateRef: "default",
		SNIBindings:           []model.SNIBinding{{CertificateRef: "exact", Hosts: []string{"api.example.com"}}},
	}
	builder := mustBuilder(t, resources.Upstreams)
	snapshot, err := builder.Build(1, resources, mustCandidate(t, resources.Upstreams))
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := snapshot.downstreamTLS.GetCertificate(&tls.ClientHelloInfo{ServerName: "api.example.com"})
	if err != nil || certificate.Leaf == nil || certificate.Leaf.SerialNumber.Int64() != 12 {
		t.Fatalf("downstream certificate = (%v, %v)", certificate, err)
	}
	if snapshot.stats.DownstreamCertificateCount != 2 || snapshot.stats.DownstreamExactCount != 1 {
		t.Fatalf("snapshot TLS stats = %+v", snapshot.stats)
	}
}

func TestBuilderMapsDownstreamTLSValidationError(t *testing.T) {
	resources := testResources()
	resources.Certificates = []*tlsmaterial.Certificate{
		runtimeTestCertificate(t, "default", 11, []string{"default.example"}),
		runtimeTestCertificate(t, "exact", 12, []string{"api.example.com"}),
	}
	resources.DownstreamTLS = &model.DownstreamTLSPolicy{
		DefaultCertificateRef: "default",
		SNIBindings:           []model.SNIBinding{{CertificateRef: "exact", Hosts: []string{"other.example.com"}}},
	}
	builder := mustBuilder(t, resources.Upstreams)
	_, err := builder.Build(1, resources, mustCandidate(t, resources.Upstreams))
	var buildErr *BuildError
	if !errors.As(err, &buildErr) || buildErr.Code != downstreamtls.CodeCertificateHostnameMismatch ||
		buildErr.Stage != StageValidate || buildErr.ResourceKind != "certificate" {
		t.Fatalf("Build() error = %+v", err)
	}
}

func TestBuilderUsesLegacyDownstreamCertificateProvider(t *testing.T) {
	resources := testResources()
	legacy := &runtimeTestCertificateProvider{certificate: runtimeTestCertificate(t, "legacy", 9, []string{"legacy.example"}).TLSCertificate()}
	plugins, err := plugin.NewBuiltinRegistry()
	if err != nil {
		t.Fatal(err)
	}
	builder, err := NewBuilderWithCertificateProvider(plugins, legacy)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := builder.Build(1, resources, mustCandidate(t, resources.Upstreams))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.downstreamTLS != legacy {
		t.Fatal("snapshot did not retain legacy provider")
	}
}

func TestBuilderRouteDisableRemovesInheritedPlugin(t *testing.T) {
	resources := testResources()
	resources.Routes[0].Plugins = []model.PluginAttachment{{Name: "header-rewrite", Enabled: false}}
	builder := mustBuilder(t, resources.Upstreams)
	candidate := mustCandidate(t, resources.Upstreams)

	snapshot, err := builder.Build(1, resources, candidate)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "http://api.example.com/users/42", nil)
	match, err := snapshot.Match(request)
	if err != nil {
		t.Fatal(err)
	}
	if result := match.Route.RunRequest(&requestctx.Context{}, request); result.Err != nil {
		t.Fatal(result.Err)
	}
	if got := request.Header.Get("X-Scope"); got != "" {
		t.Fatalf("disabled inherited plugin set X-Scope=%q", got)
	}
}

func TestBuilderCompilesImmutableEffectiveRetryPolicy(t *testing.T) {
	resources := testResources()
	resources.Upstreams[0].Retry = model.RetryPolicy{
		MaxAttempts:  2,
		Methods:      []string{"GET", "HEAD"},
		RetryOn:      model.RetryOnPolicy{ConnectFailure: true, Statuses: []uint16{503}},
		Budget:       model.RetryBudgetPolicy{RatioPer1000: 100, Burst: 10, MaxInflight: 32},
		TotalTimeout: 30 * time.Second,
	}
	timeout := 2 * time.Second
	attempts := uint8(3)
	methods := []string{}
	resources.Routes[0].Resilience = model.RouteResiliencePolicy{
		TotalTimeout: &timeout,
		MaxAttempts:  &attempts,
		Methods:      &methods,
		RetryOn:      &model.RetryOnPolicy{Statuses: []uint16{503}},
	}
	builder := mustBuilder(t, resources.Upstreams)
	candidate := mustCandidate(t, resources.Upstreams)
	snapshot, err := builder.Build(1, resources, candidate)
	if err != nil {
		t.Fatal(err)
	}
	policy := snapshot.routes[0].RetryPolicy()
	if policy.MaxAttempts != 3 ||
		policy.TotalTimeout != 2*time.Second ||
		len(policy.Methods) != 0 ||
		policy.RetryOn.ConnectFailure ||
		len(policy.RetryOn.Statuses) != 1 ||
		policy.RetryOn.Statuses[0] != 503 {
		t.Fatalf("effective policy = %+v", policy)
	}
	resources.Upstreams[0].Retry.RetryOn.Statuses[0] = 504
	resources.Routes[0].Resilience.RetryOn.Statuses[0] = 502
	if snapshot.routes[0].RetryPolicy().RetryOn.Statuses[0] != 503 {
		t.Fatal("compiled retry policy aliases resources")
	}
}

func TestBuildCompilesEffectiveWebSocketPolicy(t *testing.T) {
	enabled, disabled := true, false
	fiveMinutes := 5 * time.Minute
	oneMinute := time.Minute
	tests := []struct {
		name   string
		mutate func(*model.ResourceSet)
		route  int
		want   model.WebSocketPolicy
	}{
		{name: "defaults", mutate: func(*model.ResourceSet) {}, want: model.WebSocketPolicy{IdleTimeout: time.Minute}},
		{name: "service enable and idle", mutate: func(resources *model.ResourceSet) {
			resources.Services[0].WebSocket = model.WebSocketPolicyOverride{Enabled: &enabled, IdleTimeout: &fiveMinutes}
		}, want: model.WebSocketPolicy{Enabled: true, IdleTimeout: fiveMinutes}},
		{name: "route explicit false", mutate: func(resources *model.ResourceSet) {
			resources.Services[0].WebSocket.Enabled = &enabled
			resources.Routes[0].WebSocket.Enabled = &disabled
		}, want: model.WebSocketPolicy{IdleTimeout: time.Minute}},
		{name: "route idle only", mutate: func(resources *model.ResourceSet) {
			resources.Services[0].WebSocket.Enabled = &enabled
			resources.Routes[0].WebSocket.IdleTimeout = &fiveMinutes
		}, want: model.WebSocketPolicy{Enabled: true, IdleTimeout: fiveMinutes}},
		{name: "direct upstream route", mutate: func(resources *model.ResourceSet) {
			resources.Routes[1].WebSocket = model.WebSocketPolicyOverride{Enabled: &enabled, IdleTimeout: &oneMinute}
		}, route: 1, want: model.WebSocketPolicy{Enabled: true, IdleTimeout: oneMinute}},
		{name: "auto upstream", mutate: func(resources *model.ResourceSet) {
			resources.Routes[0].WebSocket.Enabled = &enabled
			resources.Upstreams[0].Transport.Protocol = model.TransportProtocolAuto
		}, want: model.WebSocketPolicy{Enabled: true, IdleTimeout: time.Minute}},
		{name: "http1 upstream", mutate: func(resources *model.ResourceSet) {
			resources.Routes[0].WebSocket.Enabled = &enabled
			resources.Upstreams[0].Transport.Protocol = model.TransportProtocolHTTP1
		}, want: model.WebSocketPolicy{Enabled: true, IdleTimeout: time.Minute}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resources := testResources()
			test.mutate(&resources)
			builder := mustBuilder(t, resources.Upstreams)
			candidate := mustCandidate(t, resources.Upstreams)
			snapshot, err := builder.Build(1, resources, candidate)
			if err != nil {
				t.Fatal(err)
			}
			if policy := snapshot.routes[test.route].WebSocketPolicy(); policy != test.want {
				t.Fatalf("effective websocket policy=%+v, want %+v", policy, test.want)
			}
		})
	}
}

func TestBuildRejectsWebSocketWithStrictHTTP2(t *testing.T) {
	resources := testResources()
	enabled := true
	resources.Services[0].WebSocket.Enabled = &enabled
	resources.Upstreams[0].Transport.Protocol = model.TransportProtocolHTTP2
	builder := mustBuilder(t, resources.Upstreams)
	candidate := mustCandidate(t, resources.Upstreams)

	_, err := builder.Build(9, resources, candidate)
	var buildErr *BuildError
	if !errors.As(err, &buildErr) ||
		buildErr.Code != "WEBSOCKET_UPSTREAM_PROTOCOL_INVALID" ||
		buildErr.ResourceKind != "route" ||
		buildErr.ResourceID != "users" ||
		buildErr.Field != "websocket.enabled" {
		t.Fatalf("Build() error = %#v", err)
	}
}

func TestBuildRejectsNegativeWebSocketIdleTimeout(t *testing.T) {
	resources := testResources()
	negative := -time.Second
	resources.Routes[0].WebSocket.IdleTimeout = &negative
	builder := mustBuilder(t, resources.Upstreams)
	candidate := mustCandidate(t, resources.Upstreams)
	_, err := builder.Build(1, resources, candidate)
	assertBuildError(t, err, "WEBSOCKET_POLICY_INVALID")
}

func TestBuilderRejectsInvalidResources(t *testing.T) {
	base := testResources()
	tests := []struct {
		name   string
		mutate func(*model.ResourceSet)
		code   string
	}{
		{name: "empty routes", mutate: func(r *model.ResourceSet) { r.Routes = nil }, code: "ROUTES_EMPTY"},
		{name: "duplicate route ID", mutate: func(r *model.ResourceSet) { r.Routes = append(r.Routes, r.Routes[0]) }, code: "RESOURCE_ID_DUPLICATE"},
		{name: "duplicate service ID", mutate: func(r *model.ResourceSet) { r.Services = append(r.Services, r.Services[0]) }, code: "RESOURCE_ID_DUPLICATE"},
		{name: "duplicate upstream ID", mutate: func(r *model.ResourceSet) { r.Upstreams = append(r.Upstreams, r.Upstreams[0]) }, code: "RESOURCE_ID_DUPLICATE"},
		{name: "both targets", mutate: func(r *model.ResourceSet) { r.Routes[0].UpstreamRef = "users-upstream" }, code: "ROUTE_TARGET_INVALID"},
		{name: "no target", mutate: func(r *model.ResourceSet) { r.Routes[0].ServiceRef = "" }, code: "ROUTE_TARGET_INVALID"},
		{name: "unresolved service", mutate: func(r *model.ResourceSet) { r.Routes[0].ServiceRef = "absent" }, code: "REFERENCE_NOT_FOUND"},
		{name: "unresolved direct upstream", mutate: func(r *model.ResourceSet) { r.Routes[1].UpstreamRef = "absent" }, code: "REFERENCE_NOT_FOUND"},
		{name: "unresolved service upstream", mutate: func(r *model.ResourceSet) { r.Services[0].UpstreamRef = "absent" }, code: "REFERENCE_NOT_FOUND"},
		{name: "unknown plugin", mutate: func(r *model.ResourceSet) {
			r.Routes[0].Plugins = []model.PluginAttachment{{Name: "unknown", Enabled: true}}
		}, code: "PLUGIN_COMPILE_FAILED"},
		{name: "invalid plugin config", mutate: func(r *model.ResourceSet) {
			r.Routes[0].Plugins[0].RawConfig = json.RawMessage(`{"unknown":true}`)
		}, code: "PLUGIN_COMPILE_FAILED"},
		{name: "duplicate normalized match", mutate: func(r *model.ResourceSet) {
			duplicate := r.Routes[0]
			duplicate.ID = "duplicate"
			duplicate.Plugins = nil
			r.Routes[0].Plugins = nil
			r.Routes = append(r.Routes, duplicate)
		}, code: "ROUTER_COMPILE_FAILED"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resources := model.CloneResourceSet(base)
			builder := mustBuilder(t, resources.Upstreams)
			candidate := mustCandidate(t, resources.Upstreams)
			tt.mutate(&resources)
			_, err := builder.Build(1, resources, candidate)
			var buildErr *BuildError
			if !errors.As(err, &buildErr) || buildErr.Code != tt.code {
				t.Fatalf("Build() error = %#v, want code %q", err, tt.code)
			}
		})
	}
}

func TestBuilderRejectsRevisionZero(t *testing.T) {
	resources := testResources()
	builder := mustBuilder(t, resources.Upstreams)
	candidate := mustCandidate(t, resources.Upstreams)
	_, err := builder.Build(0, resources, candidate)
	var buildErr *BuildError
	if !errors.As(err, &buildErr) || buildErr.Code != "REVISION_INVALID" {
		t.Fatalf("Build() error = %#v", err)
	}
}

func testResources() model.ResourceSet {
	rewrite := func(value string) json.RawMessage {
		return json.RawMessage(`{"request":{"set":{"X-Scope":"` + value + `"}}}`)
	}
	return model.ResourceSet{
		Routes: []model.Route{
			{
				ID:         "users",
				Priority:   10,
				ServiceRef: "users-service",
				Match: model.RouteMatch{
					Hosts:   []string{"api.example.com"},
					Path:    "/users/{id}",
					Methods: []string{"GET"},
				},
				Plugins: []model.PluginAttachment{{
					Name:      "header-rewrite",
					Enabled:   true,
					RawConfig: rewrite("route"),
				}},
			},
			{
				ID:          "health",
				UpstreamRef: "users-upstream",
				Match: model.RouteMatch{
					Path:    "/health",
					Methods: []string{"GET"},
				},
			},
		},
		Services: []model.Service{{
			ID:          "users-service",
			UpstreamRef: "users-upstream",
			Plugins: []model.PluginAttachment{{
				Name:      "header-rewrite",
				Enabled:   true,
				RawConfig: rewrite("service"),
			}},
		}},
		Upstreams: []model.Upstream{{
			ID:        "users-upstream",
			Endpoints: []model.Endpoint{{URL: "http://upstream:8080", Weight: 1}},
			Balancer:  model.BalancerPolicy{Type: model.BalancerWeightedRoundRobin},
			Transport: model.TransportConfig{
				DialTimeout:               time.Second,
				ResponseHeaderTimeout:     2 * time.Second,
				IdleConnectionTimeout:     3 * time.Second,
				MaxIdleConnections:        10,
				MaxIdleConnectionsPerHost: 5,
			},
		}},
	}
}

func mustCandidate(t *testing.T, resources []model.Upstream) *upstream.Candidate {
	t.Helper()
	registry, err := upstream.NewRegistry(upstream.RegistryOptions{MaxRetiredSnapshots: 64, HealthWorkers: 2, HealthQueueCapacity: 16})
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := registry.Prepare(model.ResourceSet{Upstreams: resources})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		candidate.Rollback()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := registry.Close(ctx); err != nil {
			t.Errorf("Registry.Close() error = %v", err)
		}
	})
	return candidate
}

func mustBuilder(t *testing.T, upstreams []model.Upstream) *Builder {
	t.Helper()
	registry, err := plugin.NewBuiltinRegistry()
	if err != nil {
		t.Fatal(err)
	}
	builder, err := NewBuilder(registry)
	if err != nil {
		t.Fatal(err)
	}
	return builder
}

func assertBuildError(t *testing.T, err error, code string) {
	t.Helper()
	var buildErr *BuildError
	if !errors.As(err, &buildErr) ||
		buildErr.Code != code ||
		!strings.Contains(buildErr.Error(), code) {
		t.Fatalf("error = %#v, want BuildError code %q", err, code)
	}
}

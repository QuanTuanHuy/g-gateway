package telemetry

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
	"github.com/QuanTuanHuy/g-gateway/internal/requestctx"
	gatewayruntime "github.com/QuanTuanHuy/g-gateway/internal/runtime"
	"github.com/QuanTuanHuy/g-gateway/internal/upstream"
)

func TestHealthAndReadinessTransitions(t *testing.T) {
	telemetry, err := New(false, false)
	if err != nil {
		t.Fatal(err)
	}

	assertAdminStatus(t, telemetry.AdminHandler(), "/healthz", http.StatusOK)
	assertAdminStatus(t, telemetry.AdminHandler(), "/readyz", http.StatusServiceUnavailable)

	telemetry.SetReady(true)
	assertAdminStatus(t, telemetry.AdminHandler(), "/readyz", http.StatusOK)

	telemetry.SetReady(false)
	assertAdminStatus(t, telemetry.AdminHandler(), "/readyz", http.StatusServiceUnavailable)
}

func TestMetricsIncludeGoAndProcessCollectors(t *testing.T) {
	telemetry, err := New(false, false)
	if err != nil {
		t.Fatal(err)
	}

	body := scrapeMetrics(t, telemetry.AdminHandler())
	for _, metric := range []string{"go_gc_duration_seconds", "process_cpu_seconds"} {
		if !strings.Contains(body, metric) {
			t.Fatalf("metrics do not contain %q", metric)
		}
	}
}

func TestRequestMetricsDisabledReturnsOriginalHandler(t *testing.T) {
	telemetry, err := New(false, false)
	if err != nil {
		t.Fatal(err)
	}
	next := &statusHandler{status: http.StatusNoContent}

	wrapped := telemetry.Wrap(next)
	if wrapped != next {
		t.Fatal("Wrap returned middleware while request metrics are disabled")
	}
	wrapped.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://gateway/hello", nil))
	if body := scrapeMetrics(t, telemetry.AdminHandler()); strings.Contains(body, "gateway_http_request") {
		t.Fatalf("disabled request metrics were exported: %s", body)
	}
}

func TestRequestMetricsUseMatchedRouteID(t *testing.T) {
	telemetry, err := New(true, false)
	if err != nil {
		t.Fatal(err)
	}
	next := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		state, ok := requestctx.From(request.Context())
		if !ok {
			t.Fatal("request context is missing")
		}
		state.Route = &requestctx.RouteMeta{ID: "dynamic-route"}
		response.WriteHeader(http.StatusCreated)
	})
	wrapped := requestctx.Middleware(telemetry.Wrap(next))
	request := httptest.NewRequest(http.MethodPost, "http://tenant.example/private/customer/123", nil)

	wrapped.ServeHTTP(httptest.NewRecorder(), request)

	body := scrapeMetrics(t, telemetry.AdminHandler())
	for _, fragment := range []string{
		`gateway_http_requests_total{method="POST",route_id="dynamic-route",status_class="2xx"} 1`,
		`gateway_http_request_duration_seconds_count{method="POST",route_id="dynamic-route",status_class="2xx"} 1`,
	} {
		if !strings.Contains(body, fragment) {
			t.Fatalf("metrics do not contain %q:\n%s", fragment, body)
		}
	}
	for _, forbidden := range []string{"customer/123", "tenant.example", "raw-upstream.example", "error="} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("metrics contain forbidden high-cardinality value %q", forbidden)
		}
	}
}

func TestRequestMetricsUseUnmatchedRouteIDFor404(t *testing.T) {
	telemetry, err := New(true, false)
	if err != nil {
		t.Fatal(err)
	}
	wrapped := requestctx.Middleware(telemetry.Wrap(&statusHandler{status: http.StatusNotFound}))

	wrapped.ServeHTTP(
		httptest.NewRecorder(),
		httptest.NewRequest(http.MethodGet, "http://gateway/not-found", nil),
	)

	body := scrapeMetrics(t, telemetry.AdminHandler())
	if fragment := `gateway_http_requests_total{method="GET",route_id="__unmatched__",status_class="4xx"} 1`; !strings.Contains(body, fragment) {
		t.Fatalf("metrics do not contain %q:\n%s", fragment, body)
	}
	if strings.Contains(body, `route_id=""`) {
		t.Fatalf("metrics contain an empty route label:\n%s", body)
	}
}

func TestSnapshotObserverExportsBoundedRuntimeMetrics(t *testing.T) {
	telemetry, err := New(false, false)
	if err != nil {
		t.Fatal(err)
	}

	telemetry.SnapshotApplied(gatewayruntime.Stats{
		Revision:      7,
		RouteCount:    11,
		ServiceCount:  3,
		UpstreamCount: 2,
		PluginCount:   5,
		BuildDuration: 25 * time.Millisecond,
	})
	telemetry.SnapshotRejected(&gatewayruntime.BuildError{
		Code:         "ROUTE_PLUGIN_INVALID",
		Stage:        gatewayruntime.StagePlugin,
		ResourceKind: "route",
		ResourceID:   "secret-customer-id",
		Cause:        errors.New("raw validation details"),
	}, 10*time.Millisecond)

	body := scrapeMetrics(t, telemetry.AdminHandler())
	for _, fragment := range []string{
		`gateway_runtime_active_revision 7`,
		`gateway_runtime_compiled_routes 11`,
		`gateway_runtime_compiled_services 3`,
		`gateway_runtime_compiled_plugins 5`,
		`gateway_runtime_snapshot_apply_total{code="",result="applied",stage=""} 1`,
		`gateway_runtime_snapshot_apply_total{code="ROUTE_PLUGIN_INVALID",result="rejected",stage="plugin_compile"} 1`,
		`gateway_runtime_snapshot_apply_duration_seconds_count 2`,
	} {
		if !strings.Contains(body, fragment) {
			t.Fatalf("metrics do not contain %q:\n%s", fragment, body)
		}
	}
	for _, forbidden := range []string{"secret-customer-id", "raw validation details"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("metrics contain forbidden high-cardinality value %q:\n%s", forbidden, body)
		}
	}
}

func TestSnapshotApplyDoesNotPrecreateRouteRequestSeries(t *testing.T) {
	telemetry, err := New(true, false)
	if err != nil {
		t.Fatal(err)
	}
	telemetry.SnapshotApplied(gatewayruntime.Stats{
		Revision:   1,
		RouteCount: 100_000,
	})

	body := scrapeMetrics(t, telemetry.AdminHandler())
	if strings.Contains(body, `gateway_http_requests_total{`) {
		t.Fatalf("snapshot apply pre-created request label series:\n%s", body)
	}

	wrapped := requestctx.Middleware(telemetry.Wrap(&statusHandler{status: http.StatusNotFound}))
	wrapped.ServeHTTP(
		httptest.NewRecorder(),
		httptest.NewRequest(http.MethodGet, "http://gateway/not-found", nil),
	)
	body = scrapeMetrics(t, telemetry.AdminHandler())
	if got := strings.Count(body, `gateway_http_requests_total{`); got != 1 {
		t.Fatalf("request counter series = %d, want exactly one observed series:\n%s", got, body)
	}
}

func TestUpstreamRegistryMetricsUseReportedCurrentState(t *testing.T) {
	telemetry, err := New(false, false)
	if err != nil {
		t.Fatal(err)
	}

	telemetry.RegistryPrepared(upstream.PrepareStats{
		CreatedEndpoints:  2,
		ReusedEndpoints:   3,
		CreatedTransports: 1,
		ReusedTransports:  4,
		CreatedSelections: 1,
		ReusedSelections:  5,
		Current: upstream.RegistryStats{
			LiveEndpoints:       5,
			LiveTransports:      2,
			LiveSelectionStates: 3,
			RetiredPlanSets:     4,
		},
	})
	telemetry.RegistryRetired(upstream.RegistryStats{
		LiveEndpoints:       5,
		LiveTransports:      2,
		LiveSelectionStates: 3,
		ActivePlanSets:      1,
		RetiredPlanSets:     4,
	})
	if body := scrapeMetrics(t, telemetry.AdminHandler()); !strings.Contains(
		body,
		`gateway_runtime_retired_snapshots 4`,
	) {
		t.Fatalf("retired snapshot gauge was not updated at retirement:\n%s", body)
	}
	telemetry.RegistryRolledBack(upstream.PrepareStats{
		Current: upstream.RegistryStats{
			LiveEndpoints:       4,
			LiveTransports:      1,
			LiveSelectionStates: 2,
			RetiredPlanSets:     3,
		},
	})
	telemetry.RegistryCleaned(upstream.CleanupStats{
		ReleasedEndpoints:  2,
		ReleasedTransports: 1,
		ClosedTransports:   1,
		Current: upstream.RegistryStats{
			LiveEndpoints:       2,
			LiveTransports:      1,
			LiveSelectionStates: 1,
			RetiredPlanSets:     0,
		},
	})

	body := scrapeMetrics(t, telemetry.AdminHandler())
	for _, fragment := range []string{
		`gateway_upstream_live_endpoints 2`,
		`gateway_upstream_live_transports 1`,
		`gateway_upstream_live_selection_states 1`,
		`gateway_runtime_retired_snapshots 0`,
		`gateway_upstream_registry_resources_total{action="created",kind="endpoint"} 2`,
		`gateway_upstream_registry_resources_total{action="reused",kind="endpoint"} 3`,
		`gateway_upstream_registry_resources_total{action="released",kind="endpoint"} 2`,
		`gateway_upstream_registry_rollbacks_total 1`,
		`gateway_upstream_transport_cleanup_total 1`,
	} {
		if !strings.Contains(body, fragment) {
			t.Fatalf("metrics do not contain %q:\n%s", fragment, body)
		}
	}
}

func TestBalancerAndHashFallbackMetricsUseOnlyBoundedLabels(t *testing.T) {
	telemetry, err := New(true, false)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := upstream.NewRegistry(upstream.RegistryOptions{MaxRetiredSnapshots: 64, HealthWorkers: 2, HealthQueueCapacity: 16})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := registry.Close(ctx); err != nil {
			t.Errorf("Registry.Close() error = %v", err)
		}
	})
	candidate, err := registry.Prepare(model.ResourceSet{Upstreams: []model.Upstream{{
		ID:        "users",
		Endpoints: []model.Endpoint{{URL: "http://secret-host.example:8080", Weight: 1}},
		Balancer: model.BalancerPolicy{
			Type: model.BalancerConsistentHash,
			HashKey: model.HashKeyPolicy{Sources: []model.HashKeySource{{
				Type: model.HashSourceHeader,
				Name: "X-Secret-Hash",
			}}},
		},
		Transport: model.TransportConfig{
			DialTimeout:               time.Second,
			ResponseHeaderTimeout:     time.Second,
			IdleConnectionTimeout:     time.Minute,
			MaxIdleConnections:        8,
			MaxIdleConnectionsPerHost: 8,
		},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	defer candidate.Rollback()
	plan, ok := candidate.Plan("users")
	if !ok {
		t.Fatal("candidate plan users is missing")
	}
	next := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		state, ok := requestctx.From(request.Context())
		if !ok {
			t.Fatal("request context is missing")
		}
		state.Upstream = &requestctx.UpstreamMeta{ID: "users"}
		state.Selection, err = plan.Select(request)
		if err != nil {
			t.Fatal(err)
		}
		response.WriteHeader(http.StatusNoContent)
	})
	wrapped := requestctx.Middleware(telemetry.Wrap(next))
	request := httptest.NewRequest(http.MethodGet, "http://gateway/private/customer/123", nil)
	request.RemoteAddr = "203.0.113.99:54321"

	wrapped.ServeHTTP(httptest.NewRecorder(), request)

	body := scrapeMetrics(t, telemetry.AdminHandler())
	for _, fragment := range []string{
		`gateway_upstream_balancer_selections_total{algorithm="consistent_hash",upstream_id="users"} 1`,
		`gateway_upstream_hash_fallback_total{upstream_id="users"} 1`,
	} {
		if !strings.Contains(body, fragment) {
			t.Fatalf("metrics do not contain %q:\n%s", fragment, body)
		}
	}
	for _, forbidden := range []string{
		"secret-host.example",
		"203.0.113.99",
		"customer/123",
		"X-Secret-Hash",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("metrics contain forbidden high-cardinality value %q:\n%s", forbidden, body)
		}
	}
}

func TestResilienceMetricsUseOnlyBoundedLabels(t *testing.T) {
	telemetry, err := New(false, false)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := upstream.NewRegistry(upstream.RegistryOptions{
		MaxRetiredSnapshots: 64,
		HealthWorkers:       1,
		HealthQueueCapacity: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = registry.Close(ctx)
	})
	if err := telemetry.RegisterResilienceProvider(registry); err != nil {
		t.Fatal(err)
	}
	body := scrapeMetrics(t, telemetry.AdminHandler())
	for _, family := range []string{
		"gateway_upstream_health_endpoints",
		"gateway_upstream_health_transitions_total",
		"gateway_upstream_health_probes_total",
		"gateway_upstream_health_probe_duration_seconds",
		"gateway_upstream_health_scheduler_queue",
		"gateway_upstream_health_scheduler_reschedules_total",
		"gateway_upstream_attempts_total",
		"gateway_upstream_retries_total",
		"gateway_upstream_retry_suppressed_total",
		"gateway_upstream_retry_inflight",
		"gateway_upstream_retry_budget_tokens",
	} {
		if !strings.Contains(body, family) {
			t.Fatalf("metrics do not contain %q:\n%s", family, body)
		}
	}
	for _, forbidden := range []string{"http://", "route_id=", "client_address=", "raw_error="} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("resilience metrics contain forbidden label %q", forbidden)
		}
	}
}

func TestPprofIsExplicitlyGated(t *testing.T) {
	disabled, err := New(false, false)
	if err != nil {
		t.Fatal(err)
	}
	assertAdminStatus(t, disabled.AdminHandler(), "/debug/pprof/", http.StatusNotFound)

	enabled, err := New(false, true)
	if err != nil {
		t.Fatal(err)
	}
	assertAdminStatus(t, enabled.AdminHandler(), "/debug/pprof/", http.StatusOK)
}

func assertAdminStatus(t *testing.T, handler http.Handler, path string, want int) {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "http://admin"+path, nil))
	if recorder.Code != want {
		t.Fatalf("GET %s status = %d, want %d; body=%q", path, recorder.Code, want, recorder.Body.String())
	}
}

func scrapeMetrics(t *testing.T, handler http.Handler) string {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "http://admin/metrics", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /metrics status = %d; body=%q", recorder.Code, recorder.Body.String())
	}
	body, err := io.ReadAll(recorder.Result().Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

type statusHandler struct {
	status int
}

func (h *statusHandler) ServeHTTP(response http.ResponseWriter, _ *http.Request) {
	response.WriteHeader(h.status)
}

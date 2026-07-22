package telemetry

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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

	wrapped := telemetry.Wrap(next, "baseline")
	if wrapped != next {
		t.Fatal("Wrap returned middleware while request metrics are disabled")
	}
	wrapped.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://gateway/hello", nil))
	if body := scrapeMetrics(t, telemetry.AdminHandler()); strings.Contains(body, "gateway_http_request") {
		t.Fatalf("disabled request metrics were exported: %s", body)
	}
}

func TestRequestMetricsUseBoundedLabels(t *testing.T) {
	telemetry, err := New(true, false)
	if err != nil {
		t.Fatal(err)
	}
	next := &statusHandler{status: http.StatusCreated}
	wrapped := telemetry.Wrap(next, "baseline")
	request := httptest.NewRequest(http.MethodPost, "http://tenant.example/private/customer/123", nil)

	wrapped.ServeHTTP(httptest.NewRecorder(), request)

	body := scrapeMetrics(t, telemetry.AdminHandler())
	for _, fragment := range []string{
		`gateway_http_requests_total{method="POST",route_id="baseline",status_class="2xx"} 1`,
		`gateway_http_request_duration_seconds_count{method="POST",route_id="baseline",status_class="2xx"} 1`,
	} {
		if !strings.Contains(body, fragment) {
			t.Fatalf("metrics do not contain %q:\n%s", fragment, body)
		}
	}
	for _, forbidden := range []string{"customer/123", "tenant.example", "upstream", "error="} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("metrics contain forbidden high-cardinality value %q", forbidden)
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

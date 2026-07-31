package telemetry

import (
	"fmt"
	"net/http"
	"net/http/pprof"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
	"github.com/QuanTuanHuy/g-gateway/internal/requestctx"
	gatewayruntime "github.com/QuanTuanHuy/g-gateway/internal/runtime"
	"github.com/QuanTuanHuy/g-gateway/internal/upstream"
)

// Telemetry owns an isolated Prometheus registry, readiness state, and the
// private admin HTTP handler. Its exported methods are safe for concurrent use.
type Telemetry struct {
	ready                 atomic.Bool
	registry              *prometheus.Registry
	requests              *prometheus.CounterVec
	duration              *prometheus.HistogramVec
	balancerSelections    *prometheus.CounterVec
	hashFallbacks         *prometheus.CounterVec
	requestMetricsEnabled bool
	activeRevision        prometheus.Gauge
	snapshotApplyDuration prometheus.Histogram
	snapshotApplyTotal    *prometheus.CounterVec
	compiledRoutes        prometheus.Gauge
	compiledServices      prometheus.Gauge
	compiledPlugins       prometheus.Gauge
	liveEndpoints         prometheus.Gauge
	liveTransports        prometheus.Gauge
	liveSelectionStates   prometheus.Gauge
	retiredSnapshots      prometheus.Gauge
	registryResources     *prometheus.CounterVec
	registryRollbacks     prometheus.Counter
	transportCleanup      prometheus.Counter
	tlsHandshake          [2][2][3]prometheus.Counter
	tlsFailures           [5]prometheus.Counter
	transportLifecycle    [3][2][3]prometheus.Counter
	adminHandler          http.Handler
}

// New constructs Telemetry with Go, process, runtime, and upstream metrics.
// requestMetricsEnabled adds bounded request and selection series;
// profilingEnabled exposes pprof routes on the admin handler. The returned
// instance starts not ready.
func New(requestMetricsEnabled, profilingEnabled bool) (*Telemetry, error) {
	registry := prometheus.NewRegistry()
	if err := registry.Register(collectors.NewGoCollector()); err != nil {
		return nil, fmt.Errorf("register Go collector: %w", err)
	}
	if err := registry.Register(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{})); err != nil {
		return nil, fmt.Errorf("register process collector: %w", err)
	}

	tlsHandshake := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "gateway",
		Subsystem: "upstream",
		Name:      "tls_handshake_total",
		Help:      "Total terminal upstream TLS handshakes by bounded outcome.",
	}, []string{"result", "mode", "protocol"})
	tlsFailures := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "gateway",
		Subsystem: "upstream",
		Name:      "tls_failure_total",
		Help:      "Total upstream TLS failures by stable class.",
	}, []string{"class"})
	transportLifecycle := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "gateway",
		Subsystem: "upstream",
		Name:      "transport_generation_total",
		Help:      "Total upstream transport generation lifecycle events.",
	}, []string{"action", "tls", "protocol"})
	telemetry := &Telemetry{
		registry:              registry,
		requestMetricsEnabled: requestMetricsEnabled,
		activeRevision: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "gateway",
			Subsystem: "runtime",
			Name:      "active_revision",
			Help:      "Revision of the active immutable runtime snapshot.",
		}),
		snapshotApplyDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: "gateway",
			Subsystem: "runtime",
			Name:      "snapshot_apply_duration_seconds",
			Help:      "Runtime snapshot apply duration in seconds.",
			Buckets:   prometheus.DefBuckets,
		}),
		snapshotApplyTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "gateway",
			Subsystem: "runtime",
			Name:      "snapshot_apply_total",
			Help:      "Total runtime snapshot apply attempts.",
		}, []string{"result", "stage", "code"}),
		compiledRoutes: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "gateway",
			Subsystem: "runtime",
			Name:      "compiled_routes",
			Help:      "Number of routes in the active runtime snapshot.",
		}),
		compiledServices: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "gateway",
			Subsystem: "runtime",
			Name:      "compiled_services",
			Help:      "Number of services in the active runtime snapshot.",
		}),
		compiledPlugins: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "gateway",
			Subsystem: "runtime",
			Name:      "compiled_plugins",
			Help:      "Number of plugin instances in the active runtime snapshot.",
		}),
		liveEndpoints: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "gateway",
			Subsystem: "upstream",
			Name:      "live_endpoints",
			Help:      "Number of endpoint runtimes currently owned by the upstream registry.",
		}),
		liveTransports: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "gateway",
			Subsystem: "upstream",
			Name:      "live_transports",
			Help:      "Number of transport runtimes currently owned by the upstream registry.",
		}),
		liveSelectionStates: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "gateway",
			Subsystem: "upstream",
			Name:      "live_selection_states",
			Help:      "Number of balancer selection states currently owned by the upstream registry.",
		}),
		retiredSnapshots: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "gateway",
			Subsystem: "runtime",
			Name:      "retired_snapshots",
			Help:      "Number of retired runtime plan sets awaiting final cleanup.",
		}),
		registryResources: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "gateway",
			Subsystem: "upstream",
			Name:      "registry_resources_total",
			Help:      "Total upstream registry resource lifecycle operations.",
		}, []string{"action", "kind"}),
		registryRollbacks: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "gateway",
			Subsystem: "upstream",
			Name:      "registry_rollbacks_total",
			Help:      "Total upstream candidate rollbacks.",
		}),
		transportCleanup: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "gateway",
			Subsystem: "upstream",
			Name:      "transport_cleanup_total",
			Help:      "Total upstream transport runtimes closed during cleanup.",
		}),
	}
	for name, collector := range map[string]prometheus.Collector{
		"active revision":         telemetry.activeRevision,
		"snapshot apply duration": telemetry.snapshotApplyDuration,
		"snapshot apply total":    telemetry.snapshotApplyTotal,
		"compiled routes":         telemetry.compiledRoutes,
		"compiled services":       telemetry.compiledServices,
		"compiled plugins":        telemetry.compiledPlugins,
		"live endpoints":          telemetry.liveEndpoints,
		"live transports":         telemetry.liveTransports,
		"live selection states":   telemetry.liveSelectionStates,
		"retired snapshots":       telemetry.retiredSnapshots,
		"registry resources":      telemetry.registryResources,
		"registry rollbacks":      telemetry.registryRollbacks,
		"transport cleanup":       telemetry.transportCleanup,
		"TLS handshakes":          tlsHandshake,
		"TLS failures":            tlsFailures,
		"transport generations":   transportLifecycle,
	} {
		if err := registry.Register(collector); err != nil {
			return nil, fmt.Errorf("register %s metric: %w", name, err)
		}
	}
	for resultIndex, result := range []string{"success", "failure"} {
		for modeIndex, mode := range []string{"server_auth", "mtls"} {
			for protocolIndex, protocol := range []string{"auto", "http1", "http2"} {
				telemetry.tlsHandshake[resultIndex][modeIndex][protocolIndex] =
					tlsHandshake.WithLabelValues(result, mode, protocol)
			}
		}
	}
	for classIndex, class := range []string{"trust", "hostname", "client_identity", "protocol", "handshake"} {
		telemetry.tlsFailures[classIndex] = tlsFailures.WithLabelValues(class)
	}
	for actionIndex, action := range []string{"create", "reuse", "retire"} {
		for tlsIndex, tlsLabel := range []string{"false", "true"} {
			for protocolIndex, protocol := range []string{"auto", "http1", "http2"} {
				telemetry.transportLifecycle[actionIndex][tlsIndex][protocolIndex] =
					transportLifecycle.WithLabelValues(action, tlsLabel, protocol)
			}
		}
	}
	if requestMetricsEnabled {
		telemetry.requests = prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "gateway",
			Subsystem: "http",
			Name:      "requests_total",
			Help:      "Total downstream HTTP requests handled by the gateway.",
		}, []string{"route_id", "method", "status_class"})
		telemetry.duration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "gateway",
			Subsystem: "http",
			Name:      "request_duration_seconds",
			Help:      "Downstream HTTP request duration in seconds.",
			Buckets:   prometheus.DefBuckets,
		}, []string{"route_id", "method", "status_class"})
		telemetry.balancerSelections = prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "gateway",
			Subsystem: "upstream",
			Name:      "balancer_selections_total",
			Help:      "Total upstream endpoint selections by configured upstream and algorithm.",
		}, []string{"upstream_id", "algorithm"})
		telemetry.hashFallbacks = prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "gateway",
			Subsystem: "upstream",
			Name:      "hash_fallback_total",
			Help:      "Total consistent-hash selections that used the bounded fallback key.",
		}, []string{"upstream_id"})
		if err := registry.Register(telemetry.requests); err != nil {
			return nil, fmt.Errorf("register request counter: %w", err)
		}
		if err := registry.Register(telemetry.duration); err != nil {
			return nil, fmt.Errorf("register request duration: %w", err)
		}
		if err := registry.Register(telemetry.balancerSelections); err != nil {
			return nil, fmt.Errorf("register balancer selection counter: %w", err)
		}
		if err := registry.Register(telemetry.hashFallbacks); err != nil {
			return nil, fmt.Errorf("register hash fallback counter: %w", err)
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "text/plain; charset=utf-8")
		response.WriteHeader(http.StatusOK)
		_, _ = response.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /readyz", func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if !telemetry.ready.Load() {
			response.WriteHeader(http.StatusServiceUnavailable)
			_, _ = response.Write([]byte("not ready\n"))
			return
		}
		response.WriteHeader(http.StatusOK)
		_, _ = response.Write([]byte("ready\n"))
	})
	mux.Handle("GET /metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
	if profilingEnabled {
		registerPprof(mux)
	}
	telemetry.adminHandler = mux
	return telemetry, nil
}

// SetReady atomically controls whether /readyz returns 200 or 503. It does not
// affect the always-live /healthz endpoint.
func (t *Telemetry) SetReady(ready bool) {
	t.ready.Store(ready)
}

// AdminHandler returns the private handler serving health, readiness, metrics,
// and optional pprof endpoints. The handler is owned by Telemetry and is safe
// for concurrent serving.
func (t *Telemetry) AdminHandler() http.Handler {
	return t.adminHandler
}

// Wrap instruments next with bounded request, latency, balancer, and hash
// fallback metrics. Matched requests use the route ID and unmatched requests
// use "__unmatched__". When request metrics are disabled, Wrap returns next
// unchanged.
func (t *Telemetry) Wrap(next http.Handler) http.Handler {
	if !t.requestMetricsEnabled {
		return next
	}
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		started := time.Now()
		writer := &metricsResponseWriter{ResponseWriter: response}
		next.ServeHTTP(writer, request)
		statusClass := strconv.Itoa(writer.statusCode()/100) + "xx"
		routeID := "__unmatched__"
		if state, ok := requestctx.From(request.Context()); ok && state.Route != nil && state.Route.ID != "" {
			routeID = state.Route.ID
		}
		labels := []string{routeID, request.Method, statusClass}
		t.requests.WithLabelValues(labels...).Inc()
		t.duration.WithLabelValues(labels...).Observe(time.Since(started).Seconds())
		if state, ok := requestctx.From(request.Context()); ok &&
			state.Upstream != nil &&
			state.Upstream.ID != "" &&
			state.Selection.Valid() {
			upstreamID := state.Upstream.ID
			t.balancerSelections.WithLabelValues(upstreamID, string(state.Selection.Balancer())).Inc()
			if state.Selection.HashFallback() {
				t.hashFallbacks.WithLabelValues(upstreamID).Inc()
			}
		}
	})
}

// SnapshotApplied records the active revision and compiled-resource gauges,
// observes build duration, and increments the applied counter.
func (t *Telemetry) SnapshotApplied(stats gatewayruntime.Stats) {
	t.activeRevision.Set(float64(stats.Revision))
	t.compiledRoutes.Set(float64(stats.RouteCount))
	t.compiledServices.Set(float64(stats.ServiceCount))
	t.compiledPlugins.Set(float64(stats.PluginCount))
	t.snapshotApplyDuration.Observe(stats.BuildDuration.Seconds())
	t.snapshotApplyTotal.WithLabelValues("applied", "", "").Inc()
}

// SnapshotRejected observes build duration and increments the rejected counter
// using only the bounded build stage and stable error code.
func (t *Telemetry) SnapshotRejected(buildErr *gatewayruntime.BuildError, duration time.Duration) {
	var stage, code string
	if buildErr != nil {
		stage = string(buildErr.Stage)
		code = buildErr.Code
	}
	t.snapshotApplyDuration.Observe(duration.Seconds())
	t.snapshotApplyTotal.WithLabelValues("rejected", stage, code).Inc()
}

// RegistryPrepared adds created and reused resource deltas and replaces the
// current registry gauges with the reported state.
func (t *Telemetry) RegistryPrepared(stats upstream.PrepareStats) {
	t.registryResources.WithLabelValues("created", "endpoint").Add(float64(stats.CreatedEndpoints))
	t.registryResources.WithLabelValues("reused", "endpoint").Add(float64(stats.ReusedEndpoints))
	t.registryResources.WithLabelValues("created", "transport").Add(float64(stats.CreatedTransports))
	t.registryResources.WithLabelValues("reused", "transport").Add(float64(stats.ReusedTransports))
	t.registryResources.WithLabelValues("created", "selection_state").Add(float64(stats.CreatedSelections))
	t.registryResources.WithLabelValues("reused", "selection_state").Add(float64(stats.ReusedSelections))
	t.addTransportGenerations(stats.TransportGenerations)
	t.setRegistryStats(stats.Current)
}

// RegistryRolledBack increments the rollback counter and replaces current
// registry gauges with the reported post-rollback state.
func (t *Telemetry) RegistryRolledBack(stats upstream.PrepareStats) {
	t.registryRollbacks.Inc()
	t.addTransportGenerations(stats.TransportGenerations)
	t.setRegistryStats(stats.Current)
}

// RegistryRetired replaces current registry gauges with the reported state.
func (t *Telemetry) RegistryRetired(stats upstream.RegistryStats) {
	t.setRegistryStats(stats)
}

// RegistryCleaned adds released-resource and transport-cleanup deltas and
// replaces current registry gauges with the reported state.
func (t *Telemetry) RegistryCleaned(stats upstream.CleanupStats) {
	t.registryResources.WithLabelValues("released", "endpoint").Add(float64(stats.ReleasedEndpoints))
	t.registryResources.WithLabelValues("released", "transport").Add(float64(stats.ReleasedTransports))
	t.transportCleanup.Add(float64(stats.ClosedTransports))
	t.addTransportGenerations(stats.TransportGenerations)
	t.setRegistryStats(stats.Current)
}

// TLSHandshake increments one pre-bound closed TLS handshake series.
func (t *Telemetry) TLSHandshake(
	result, mode string,
	protocol model.TransportProtocol,
) {
	resultIndex, ok := tlsResultIndex(result)
	if !ok {
		return
	}
	modeIndex, ok := tlsModeIndex(mode)
	if !ok {
		return
	}
	protocolIndex, ok := transportProtocolIndex(protocol)
	if !ok {
		return
	}
	t.tlsHandshake[resultIndex][modeIndex][protocolIndex].Inc()
}

// TLSFailure increments one pre-bound stable TLS failure series.
func (t *Telemetry) TLSFailure(class upstream.TLSFailureClass) {
	index, ok := tlsFailureIndex(class)
	if !ok {
		return
	}
	t.tlsFailures[index].Inc()
}

func (t *Telemetry) addTransportGenerations(deltas []upstream.TransportGenerationDelta) {
	for _, delta := range deltas {
		actionIndex, ok := transportActionIndex(delta.Action)
		if !ok || delta.Count <= 0 {
			continue
		}
		protocolIndex, ok := transportProtocolIndex(delta.Protocol)
		if !ok {
			continue
		}
		tlsIndex := 0
		if delta.TLS {
			tlsIndex = 1
		}
		t.transportLifecycle[actionIndex][tlsIndex][protocolIndex].Add(float64(delta.Count))
	}
}

func tlsResultIndex(result string) (int, bool) {
	switch result {
	case "success":
		return 0, true
	case "failure":
		return 1, true
	default:
		return 0, false
	}
}

func tlsModeIndex(mode string) (int, bool) {
	switch mode {
	case "server_auth":
		return 0, true
	case "mtls":
		return 1, true
	default:
		return 0, false
	}
}

func transportProtocolIndex(protocol model.TransportProtocol) (int, bool) {
	switch protocol {
	case model.TransportProtocolAuto:
		return 0, true
	case model.TransportProtocolHTTP1:
		return 1, true
	case model.TransportProtocolHTTP2:
		return 2, true
	default:
		return 0, false
	}
}

func tlsFailureIndex(class upstream.TLSFailureClass) (int, bool) {
	switch class {
	case upstream.TLSFailureTrust:
		return 0, true
	case upstream.TLSFailureHostname:
		return 1, true
	case upstream.TLSFailureClientIdentity:
		return 2, true
	case upstream.TLSFailureProtocol:
		return 3, true
	case upstream.TLSFailureHandshake:
		return 4, true
	default:
		return 0, false
	}
}

func transportActionIndex(action string) (int, bool) {
	switch action {
	case "create":
		return 0, true
	case "reuse":
		return 1, true
	case "retire":
		return 2, true
	default:
		return 0, false
	}
}

func (t *Telemetry) setRegistryStats(stats upstream.RegistryStats) {
	t.liveEndpoints.Set(float64(stats.LiveEndpoints))
	t.liveTransports.Set(float64(stats.LiveTransports))
	t.liveSelectionStates.Set(float64(stats.LiveSelectionStates))
	t.retiredSnapshots.Set(float64(stats.RetiredPlanSets))
}

func registerPprof(mux *http.ServeMux) {
	mux.HandleFunc("GET /debug/pprof/", pprof.Index)
	mux.HandleFunc("GET /debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("GET /debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("POST /debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("GET /debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("GET /debug/pprof/trace", pprof.Trace)
	for _, profile := range []string{"allocs", "block", "goroutine", "heap", "mutex", "threadcreate"} {
		mux.Handle("GET /debug/pprof/"+profile, pprof.Handler(profile))
	}
}

type metricsResponseWriter struct {
	http.ResponseWriter
	status int
}

// WriteHeader records the first final status code and forwards status to the
// underlying writer. Informational 1xx responses do not become the recorded
// final status.
func (w *metricsResponseWriter) WriteHeader(status int) {
	if status >= 200 && w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

// Write records an implicit 200 status before forwarding data.
func (w *metricsResponseWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(data)
}

// Unwrap returns the underlying writer so http.ResponseController can recover
// supported optional response-writer interfaces.
func (w *metricsResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *metricsResponseWriter) statusCode() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

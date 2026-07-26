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

	"github.com/QuanTuanHuy/g-gateway/internal/requestctx"
	gatewayruntime "github.com/QuanTuanHuy/g-gateway/internal/runtime"
	"github.com/QuanTuanHuy/g-gateway/internal/upstream"
)

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
	adminHandler          http.Handler
}

func New(requestMetricsEnabled, profilingEnabled bool) (*Telemetry, error) {
	registry := prometheus.NewRegistry()
	if err := registry.Register(collectors.NewGoCollector()); err != nil {
		return nil, fmt.Errorf("register Go collector: %w", err)
	}
	if err := registry.Register(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{})); err != nil {
		return nil, fmt.Errorf("register process collector: %w", err)
	}

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
	} {
		if err := registry.Register(collector); err != nil {
			return nil, fmt.Errorf("register %s metric: %w", name, err)
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

func (t *Telemetry) SetReady(ready bool) {
	t.ready.Store(ready)
}

func (t *Telemetry) AdminHandler() http.Handler {
	return t.adminHandler
}

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

func (t *Telemetry) SnapshotApplied(stats gatewayruntime.Stats) {
	t.activeRevision.Set(float64(stats.Revision))
	t.compiledRoutes.Set(float64(stats.RouteCount))
	t.compiledServices.Set(float64(stats.ServiceCount))
	t.compiledPlugins.Set(float64(stats.PluginCount))
	t.snapshotApplyDuration.Observe(stats.BuildDuration.Seconds())
	t.snapshotApplyTotal.WithLabelValues("applied", "", "").Inc()
}

func (t *Telemetry) SnapshotRejected(buildErr *gatewayruntime.BuildError, duration time.Duration) {
	var stage, code string
	if buildErr != nil {
		stage = string(buildErr.Stage)
		code = buildErr.Code
	}
	t.snapshotApplyDuration.Observe(duration.Seconds())
	t.snapshotApplyTotal.WithLabelValues("rejected", stage, code).Inc()
}

func (t *Telemetry) RegistryPrepared(stats upstream.PrepareStats) {
	t.registryResources.WithLabelValues("created", "endpoint").Add(float64(stats.CreatedEndpoints))
	t.registryResources.WithLabelValues("reused", "endpoint").Add(float64(stats.ReusedEndpoints))
	t.registryResources.WithLabelValues("created", "transport").Add(float64(stats.CreatedTransports))
	t.registryResources.WithLabelValues("reused", "transport").Add(float64(stats.ReusedTransports))
	t.registryResources.WithLabelValues("created", "selection_state").Add(float64(stats.CreatedSelections))
	t.registryResources.WithLabelValues("reused", "selection_state").Add(float64(stats.ReusedSelections))
	t.setRegistryStats(stats.Current)
}

func (t *Telemetry) RegistryRolledBack(stats upstream.PrepareStats) {
	t.registryRollbacks.Inc()
	t.setRegistryStats(stats.Current)
}

func (t *Telemetry) RegistryRetired(stats upstream.RegistryStats) {
	t.setRegistryStats(stats)
}

func (t *Telemetry) RegistryCleaned(stats upstream.CleanupStats) {
	t.registryResources.WithLabelValues("released", "endpoint").Add(float64(stats.ReleasedEndpoints))
	t.registryResources.WithLabelValues("released", "transport").Add(float64(stats.ReleasedTransports))
	t.transportCleanup.Add(float64(stats.ClosedTransports))
	t.setRegistryStats(stats.Current)
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

func (w *metricsResponseWriter) WriteHeader(status int) {
	if status >= 200 && w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *metricsResponseWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(data)
}

func (w *metricsResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *metricsResponseWriter) statusCode() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

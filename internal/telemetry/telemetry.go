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
)

type Telemetry struct {
	ready                 atomic.Bool
	registry              *prometheus.Registry
	requests              *prometheus.CounterVec
	duration              *prometheus.HistogramVec
	requestMetricsEnabled bool
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
		if err := registry.Register(telemetry.requests); err != nil {
			return nil, fmt.Errorf("register request counter: %w", err)
		}
		if err := registry.Register(telemetry.duration); err != nil {
			return nil, fmt.Errorf("register request duration: %w", err)
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

func (t *Telemetry) Wrap(next http.Handler, routeID string) http.Handler {
	if !t.requestMetricsEnabled {
		return next
	}
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		started := time.Now()
		writer := &metricsResponseWriter{ResponseWriter: response}
		next.ServeHTTP(writer, request)
		statusClass := strconv.Itoa(writer.statusCode()/100) + "xx"
		labels := []string{routeID, request.Method, statusClass}
		t.requests.WithLabelValues(labels...).Inc()
		t.duration.WithLabelValues(labels...).Observe(time.Since(started).Seconds())
	})
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

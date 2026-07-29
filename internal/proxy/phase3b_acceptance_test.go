package proxy

import (
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
)

func TestPhase3BHealthyProxyComparison(t *testing.T) {
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer upstreamServer.Close()
	legacy := runtimeProxyResources(upstreamServer.URL, upstreamServer.URL)
	phase3B := model.CloneResourceSet(legacy)
	health, retry := proxyResiliencePolicy()
	phase3B.Upstreams[0].Health, phase3B.Upstreams[0].Retry = health, retry

	requests := 200
	if os.Getenv("GATEWAY_PHASE3B_ACCEPTANCE") == "1" {
		requests = 5_000
	}
	legacyThroughput, legacyP99 := measureHealthyProxy(t, legacy, requests)
	phase3BThroughput, phase3BP99 := measureHealthyProxy(t, phase3B, requests)
	t.Logf("seed=20260727 requests=%d legacy=%.2f req/s p99=%s phase3b=%.2f req/s p99=%s",
		requests, legacyThroughput, legacyP99, phase3BThroughput, phase3BP99)
	if os.Getenv("GATEWAY_PHASE3B_ACCEPTANCE") == "1" {
		if phase3BThroughput < legacyThroughput*95/100 {
			t.Fatalf("healthy throughput = %.2f, want >= 95%% of %.2f", phase3BThroughput, legacyThroughput)
		}
		if phase3BP99 > legacyP99*110/100 {
			t.Fatalf("healthy p99 = %s, want <= 110%% of %s", phase3BP99, legacyP99)
		}
	}
}

func measureHealthyProxy(t *testing.T, resources model.ResourceSet, requests int) (float64, time.Duration) {
	t.Helper()
	handler, _, _ := newRuntimeTestHandler(t, resources, true)
	durations := make([]time.Duration, requests)
	started := time.Now()
	for index := range requests {
		requestStarted := time.Now()
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://gateway/users/42?tenant=acme", nil))
		if response.Code != http.StatusNoContent {
			t.Fatalf("response = %d %s", response.Code, response.Body.String())
		}
		durations[index] = time.Since(requestStarted)
	}
	elapsed := time.Since(started)
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	index := (len(durations)*99 + 99) / 100
	if index > 0 {
		index--
	}
	return float64(requests) / elapsed.Seconds(), durations[index]
}

func proxyResiliencePolicy() (model.HealthPolicy, model.RetryPolicy) {
	return model.HealthPolicy{Active: &model.ActiveHealthPolicy{
			Type:              model.HealthCheckHTTP,
			Timeout:           time.Second,
			HealthyInterval:   5 * time.Second,
			UnhealthyInterval: 2 * time.Second,
			HealthySuccesses:  2,
			HTTPFailures:      3,
			TransportFailures: 2,
			Timeouts:          2,
			HealthyStatuses:   []uint16{204},
			UnhealthyStatuses: []uint16{503},
			Path:              "/",
		}}, model.RetryPolicy{
			MaxAttempts:  1,
			Methods:      []string{http.MethodGet},
			Budget:       model.RetryBudgetPolicy{RatioPer1000: 100, Burst: 10, MaxInflight: 32},
			TotalTimeout: 30 * time.Second,
		}
}

func BenchmarkAttemptTransport(b *testing.B) {
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer upstreamServer.Close()
	resources := runtimeProxyResources(upstreamServer.URL, upstreamServer.URL)
	_, retry := proxyResiliencePolicy()
	resources.Upstreams[0].Retry = retry
	handler, _, _ := newRuntimeTestHandler(b, resources, true)
	var failedCalls, successfulCalls atomic.Int32
	failedURL, successfulURL := orderedRetryServers(b, &failedCalls, &successfulCalls)
	retryHandler, _, _ := newRuntimeTestHandler(b, retryProxyResources(failedURL, successfulURL, 1000), true)
	b.Run("one-attempt", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://gateway/users/42?tenant=acme", nil))
		}
	})
	b.Run("retry-success", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			response := httptest.NewRecorder()
			retryHandler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://gateway/users/42?tenant=acme", nil))
		}
	})
}

func BenchmarkProxyHealthy(b *testing.B) {
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer upstreamServer.Close()
	legacy := runtimeProxyResources(upstreamServer.URL, upstreamServer.URL)
	phase3B := model.CloneResourceSet(legacy)
	phase3B.Upstreams[0].Health, phase3B.Upstreams[0].Retry = proxyResiliencePolicy()
	for _, benchmark := range []struct {
		name      string
		resources model.ResourceSet
	}{
		{name: "phase3a-baseline", resources: legacy},
		{name: "phase3b-health-enabled", resources: phase3B},
	} {
		b.Run(benchmark.name, func(b *testing.B) {
			handler, _, _ := newRuntimeTestHandler(b, benchmark.resources, true)
			b.ReportAllocs()
			for b.Loop() {
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://gateway/users/42?tenant=acme", nil))
			}
		})
	}
}

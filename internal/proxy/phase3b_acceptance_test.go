package proxy

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"sync"
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
	legacyHandler, _, _ := newRuntimeTestHandler(t, legacy, true)
	phase3BHandler, _, _ := newRuntimeTestHandler(t, phase3B, true)
	const concurrency = 8
	warmupRequests := min(requests/2, 2_500)
	for _, handler := range []http.Handler{legacyHandler, phase3BHandler} {
		if _, _, err := measureHTTPHandler(handler, warmupRequests, concurrency); err != nil {
			t.Fatal(err)
		}
	}
	rounds := 1
	if os.Getenv("GATEWAY_PHASE3B_ACCEPTANCE") == "1" {
		rounds = 5
	}
	legacyMeasurements := make([]healthyMeasurement, 0, rounds)
	phase3BMeasurements := make([]healthyMeasurement, 0, rounds)
	measure := func(handler http.Handler) healthyMeasurement {
		throughput, p99, err := measureHTTPHandler(handler, requests, concurrency)
		if err != nil {
			t.Fatal(err)
		}
		return healthyMeasurement{throughput: throughput, p99: p99}
	}
	for round := range rounds {
		if round%2 == 0 {
			legacyMeasurements = append(legacyMeasurements, measure(legacyHandler))
			phase3BMeasurements = append(phase3BMeasurements, measure(phase3BHandler))
		} else {
			phase3BMeasurements = append(phase3BMeasurements, measure(phase3BHandler))
			legacyMeasurements = append(legacyMeasurements, measure(legacyHandler))
		}
	}
	legacyResult := medianHealthyMeasurement(legacyMeasurements)
	phase3BResult := medianHealthyMeasurement(phase3BMeasurements)
	t.Logf("seed=20260727 requests=%d concurrency=%d rounds=%d legacy=%.2f req/s p99=%s phase3b=%.2f req/s p99=%s",
		requests, concurrency, rounds, legacyResult.throughput, legacyResult.p99, phase3BResult.throughput, phase3BResult.p99)
	if os.Getenv("GATEWAY_PHASE3B_ACCEPTANCE") == "1" {
		if phase3BResult.throughput < legacyResult.throughput*95/100 {
			t.Fatalf("healthy throughput = %.2f, want >= 95%% of %.2f", phase3BResult.throughput, legacyResult.throughput)
		}
		if phase3BResult.p99 > legacyResult.p99*110/100 {
			t.Fatalf("healthy p99 = %s, want <= 110%% of %s", phase3BResult.p99, legacyResult.p99)
		}
	}
}

func TestMeasureHTTPHandlerUsesFixedConcurrency(t *testing.T) {
	var active, maximum atomic.Int32
	release := make(chan struct{})
	var releaseOnce sync.Once
	handler := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		current := active.Add(1)
		for {
			observed := maximum.Load()
			if current <= observed || maximum.CompareAndSwap(observed, current) {
				break
			}
		}
		if current == 4 {
			releaseOnce.Do(func() { close(release) })
		}
		<-release
		active.Add(-1)
		writer.WriteHeader(http.StatusNoContent)
	})

	if _, _, err := measureHTTPHandler(handler, 8, 4); err != nil {
		t.Fatal(err)
	}
	if got := maximum.Load(); got != 4 {
		t.Fatalf("maximum concurrency = %d, want 4", got)
	}
}

type healthyMeasurement struct {
	throughput float64
	p99        time.Duration
}

func medianHealthyMeasurement(measurements []healthyMeasurement) healthyMeasurement {
	throughputs := make([]float64, len(measurements))
	p99s := make([]time.Duration, len(measurements))
	for index, measurement := range measurements {
		throughputs[index] = measurement.throughput
		p99s[index] = measurement.p99
	}
	sort.Float64s(throughputs)
	sort.Slice(p99s, func(i, j int) bool { return p99s[i] < p99s[j] })
	middle := len(measurements) / 2
	return healthyMeasurement{throughput: throughputs[middle], p99: p99s[middle]}
}

func measureHTTPHandler(handler http.Handler, requests, concurrency int) (float64, time.Duration, error) {
	if handler == nil || requests < 1 || concurrency < 1 {
		return 0, 0, fmt.Errorf("handler, requests, and concurrency must be valid")
	}
	durations := make([]time.Duration, requests)
	var next atomic.Int64
	errs := make(chan error, concurrency)
	start := make(chan struct{})
	var workers sync.WaitGroup
	workers.Add(concurrency)
	for range concurrency {
		go func() {
			defer workers.Done()
			<-start
			for {
				index := int(next.Add(1) - 1)
				if index >= requests {
					return
				}
				requestStarted := time.Now()
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://gateway/users/42?tenant=acme", nil))
				if response.Code != http.StatusNoContent {
					select {
					case errs <- fmt.Errorf("response = %d %s", response.Code, response.Body.String()):
					default:
					}
					return
				}
				durations[index] = time.Since(requestStarted)
			}
		}()
	}
	started := time.Now()
	close(start)
	workers.Wait()
	close(errs)
	if err := <-errs; err != nil {
		return 0, 0, err
	}
	elapsed := time.Since(started)
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	index := (len(durations)*99 + 99) / 100
	if index > 0 {
		index--
	}
	return float64(requests) / elapsed.Seconds(), durations[index], nil
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

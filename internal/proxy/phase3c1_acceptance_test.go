package proxy

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
)

func TestPhase3C1CleartextHealthyProxyComparison(t *testing.T) {
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer upstreamServer.Close()

	legacy := runtimeProxyResources(upstreamServer.URL, upstreamServer.URL)
	phase3C1 := model.CloneResourceSet(legacy)
	phase3C1.Upstreams[0].Transport.Protocol = model.TransportProtocolAuto

	requests := 200
	rounds := 1
	full := os.Getenv("GATEWAY_PHASE3C1_ACCEPTANCE") == "1"
	if full {
		requests = 5_000
		rounds = 5
	}
	legacyHandler, _, _ := newRuntimeTestHandler(t, legacy, true)
	phase3C1Handler, _, _ := newRuntimeTestHandler(t, phase3C1, true)
	const concurrency = 8
	warmupRequests := min(requests/2, 2_500)
	for _, handler := range []http.Handler{legacyHandler, phase3C1Handler} {
		if _, _, err := measureHTTPHandler(handler, warmupRequests, concurrency); err != nil {
			t.Fatal(err)
		}
	}

	legacyMeasurements := make([]healthyMeasurement, 0, rounds)
	phase3C1Measurements := make([]healthyMeasurement, 0, rounds)
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
			phase3C1Measurements = append(phase3C1Measurements, measure(phase3C1Handler))
		} else {
			phase3C1Measurements = append(phase3C1Measurements, measure(phase3C1Handler))
			legacyMeasurements = append(legacyMeasurements, measure(legacyHandler))
		}
	}
	legacyResult := medianHealthyMeasurement(legacyMeasurements)
	phase3C1Result := medianHealthyMeasurement(phase3C1Measurements)
	legacyAllocs := phase3C1HandlerAllocs(legacyHandler)
	phase3C1Allocs := phase3C1HandlerAllocs(phase3C1Handler)
	throughputRatio := phase3C1Result.throughput / legacyResult.throughput
	p99Ratio := float64(phase3C1Result.p99) / float64(legacyResult.p99)
	t.Logf(
		"seed=20260731 requests=%d concurrency=%d rounds=%d legacy=%.2f_req/s p99=%s allocs=%.2f phase3c1=%.2f_req/s p99=%s allocs=%.2f throughput_ratio=%.4f p99_ratio=%.4f allocation_delta=%.2f",
		requests,
		concurrency,
		rounds,
		legacyResult.throughput,
		legacyResult.p99,
		legacyAllocs,
		phase3C1Result.throughput,
		phase3C1Result.p99,
		phase3C1Allocs,
		throughputRatio,
		p99Ratio,
		phase3C1Allocs-legacyAllocs,
	)
	if !full {
		return
	}
	if phase3C1Result.throughput < legacyResult.throughput*0.95 {
		t.Fatalf("throughput %.2f, want >= 95%% of %.2f", phase3C1Result.throughput, legacyResult.throughput)
	}
	if phase3C1Result.p99 > legacyResult.p99*110/100 {
		t.Fatalf("p99 %s, want <= 110%% of %s", phase3C1Result.p99, legacyResult.p99)
	}
	if phase3C1Allocs > legacyAllocs {
		t.Fatalf("allocs %.2f, want <= %.2f", phase3C1Allocs, legacyAllocs)
	}
}

func phase3C1HandlerAllocs(handler http.Handler) float64 {
	request := httptest.NewRequest(http.MethodGet, "http://gateway/users/42?tenant=acme", nil)
	return testing.AllocsPerRun(100, func() {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request.Clone(request.Context()))
		if response.Code != http.StatusNoContent {
			panic("unexpected acceptance response")
		}
	})
}

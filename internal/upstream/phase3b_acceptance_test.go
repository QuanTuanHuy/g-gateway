package upstream

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
)

type phase3BProfile struct {
	Upstreams            int
	EndpointsPerUpstream int
	Swaps                int
}

func TestPhase3BAcceptance(t *testing.T) {
	profile := phase3BProfile{Upstreams: 1_000, EndpointsPerUpstream: 10, Swaps: 2}
	if os.Getenv("GATEWAY_PHASE3B_ACCEPTANCE") == "1" {
		profile = phase3BProfile{Upstreams: 10_000, EndpointsPerUpstream: 10, Swaps: 20}
	}
	registry, err := NewRegistry(RegistryOptions{
		MaxRetiredSnapshots: 64,
		HealthWorkers:       4,
		HealthQueueCapacity: 4096,
	})
	if err != nil {
		t.Fatal(err)
	}
	resources := make([]model.Upstream, profile.Upstreams)
	for upstreamIndex := range resources {
		endpoints := make([]model.Endpoint, profile.EndpointsPerUpstream)
		for endpointIndex := range endpoints {
			endpoints[endpointIndex] = model.Endpoint{
				URL:    fmt.Sprintf("http://u-%d-e-%d.example:80", upstreamIndex, endpointIndex),
				Weight: 1,
			}
		}
		health, retry := validResiliencePolicies()
		resources[upstreamIndex] = model.Upstream{
			ID:        fmt.Sprintf("upstream-%05d", upstreamIndex),
			Endpoints: endpoints,
			Balancer:  model.BalancerPolicy{Type: model.BalancerWeightedRoundRobin},
			Transport: validTransportConfig(),
			Health:    health,
			Retry:     retry,
		}
	}
	candidate, err := registry.Prepare(model.ResourceSet{Upstreams: resources})
	if err != nil {
		t.Fatal(err)
	}
	if stats := registry.HealthCoordinatorStats(); stats.Scheduled != 0 || stats.ReadyQueue != 0 {
		t.Fatalf("unused health work = %+v", stats)
	}
	candidate.Rollback()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := registry.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if stats := registry.Stats(); stats.LiveHealthTrackers != 0 || stats.LiveRetryBudgets != 0 {
		t.Fatalf("retained resilience runtimes = %+v", stats)
	}
	t.Logf("seed=20260727 profile=%+v", profile)
}

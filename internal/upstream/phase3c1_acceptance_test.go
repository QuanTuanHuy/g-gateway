package upstream

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
	"github.com/QuanTuanHuy/g-gateway/internal/tlsmaterial"
)

const phase3C1AcceptanceSeed = 20260731

type phase3C1Profile struct {
	Upstreams            int
	EndpointsPerUpstream int
	MaterialResources    int
	Rotations            int
}

func TestPhase3C1CompileAndRotationAcceptance(t *testing.T) {
	profile := phase3C1Profile{
		Upstreams:            1_000,
		EndpointsPerUpstream: 10,
		MaterialResources:    1_000,
		Rotations:            2,
	}
	full := os.Getenv("GATEWAY_PHASE3C1_ACCEPTANCE") == "1"
	if full {
		profile = phase3C1Profile{
			Upstreams:            10_000,
			EndpointsPerUpstream: 10,
			MaterialResources:    10_000,
			Rotations:            20,
		}
	}
	if profile.EndpointsPerUpstream > MaxUpstreamEndpoints {
		t.Fatalf("endpoints per upstream=%d, limit=%d", profile.EndpointsPerUpstream, MaxUpstreamEndpoints)
	}
	totalEndpoints := profile.Upstreams * profile.EndpointsPerUpstream
	if full && totalEndpoints != 100_000 {
		t.Fatalf("full endpoint count=%d, want 100000", totalEndpoints)
	}

	firstRoot := newUpstreamTestPKI(t, "phase3c1-root-a").rootPEM
	secondRoot := newUpstreamTestPKI(t, "phase3c1-root-b").rootPEM
	if sourceBytes := uint64(profile.MaterialResources) * uint64(max(len(firstRoot), len(secondRoot))); sourceBytes > 64<<20 {
		t.Fatalf("material source input=%d bytes, limit=%d", sourceBytes, 64<<20)
	}
	firstBundles := phase3C1TrustBundles(t, profile.MaterialResources, firstRoot)
	secondBundles := phase3C1TrustBundles(t, profile.MaterialResources, secondRoot)
	upstreams := phase3C1Upstreams(profile)

	runtime.GC()
	var baseline runtime.MemStats
	runtime.ReadMemStats(&baseline)

	registry, err := NewRegistry(RegistryOptions{
		MaxRetiredSnapshots: 64,
		HealthWorkers:       2,
		HealthQueueCapacity: 16,
	})
	if err != nil {
		t.Fatal(err)
	}
	initial, err := registry.Prepare(model.ResourceSet{Upstreams: upstreams, TrustBundles: firstBundles})
	if err != nil {
		t.Fatal(err)
	}
	assertPhase3C1PrepareStats(t, initial.stats, profile, totalEndpoints, true)
	active := initial.Commit()
	if active == nil {
		t.Fatal("initial commit returned nil")
	}

	for rotation := 0; rotation < profile.Rotations; rotation++ {
		bundles := secondBundles
		if rotation%2 == 1 {
			bundles = firstBundles
		}
		candidate, prepareErr := registry.Prepare(model.ResourceSet{
			Upstreams:    upstreams,
			TrustBundles: bundles,
		})
		if prepareErr != nil {
			t.Fatalf("rotation %d: %v", rotation, prepareErr)
		}
		assertPhase3C1PrepareStats(t, candidate.stats, profile, totalEndpoints, false)
		next := candidate.Commit()
		if got := registry.Stats().LiveTransports; got != 2 {
			t.Fatalf("rotation %d live transports before retirement=%d, want 2", rotation, got)
		}
		active.Retire()
		waitForPhase3AReaper(t, registry)
		active = next
		stats := registry.Stats()
		if stats.LiveTransports != 1 || stats.RetiredPlanSets != 0 || stats.ActivePlanSets != 1 {
			t.Fatalf("rotation %d did not reach steady state: %+v", rotation, stats)
		}
	}

	runtime.GC()
	var activeMemory runtime.MemStats
	runtime.ReadMemStats(&activeMemory)
	runtime.KeepAlive(upstreams)
	runtime.KeepAlive(firstBundles)
	runtime.KeepAlive(secondBundles)
	heapDelta := phase3AHeapDelta(activeMemory.HeapAlloc, baseline.HeapAlloc)
	if full && heapDelta > 512<<20 {
		t.Fatalf("incremental active heap=%d bytes, limit=%d", heapDelta, 512<<20)
	}
	t.Logf(
		"seed=%d profile=%+v source_bytes=%d heap_delta_bytes=%d live_transports=%d retired_plan_sets=%d",
		phase3C1AcceptanceSeed,
		profile,
		uint64(profile.MaterialResources)*uint64(max(len(firstRoot), len(secondRoot))),
		heapDelta,
		registry.Stats().LiveTransports,
		registry.Stats().RetiredPlanSets,
	)

	active.Retire()
	waitForPhase3AReaper(t, registry)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := registry.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if stats := registry.Stats(); stats != (RegistryStats{}) {
		t.Fatalf("registry retained resources after close: %+v", stats)
	}
}

func phase3C1TrustBundles(
	t testing.TB,
	count int,
	document []byte,
) []*tlsmaterial.TrustBundle {
	t.Helper()
	bundles := make([]*tlsmaterial.TrustBundle, count)
	for index := range bundles {
		bundle, err := tlsmaterial.NewTrustBundle(fmt.Sprintf("root-%05d", index), document)
		if err != nil {
			t.Fatal(err)
		}
		bundles[index] = bundle
	}
	return bundles
}

func phase3C1Upstreams(profile phase3C1Profile) []model.Upstream {
	resources := make([]model.Upstream, profile.Upstreams)
	for upstreamIndex := range resources {
		endpoints := make([]model.Endpoint, profile.EndpointsPerUpstream)
		for endpointIndex := range endpoints {
			endpoints[endpointIndex] = model.Endpoint{
				URL: fmt.Sprintf(
					"https://u-%05d-e-%03d.example:443",
					upstreamIndex,
					endpointIndex,
				),
				Weight: 1,
			}
		}
		resources[upstreamIndex] = model.Upstream{
			ID:        fmt.Sprintf("upstream-%05d", upstreamIndex),
			Endpoints: endpoints,
			Balancer:  model.BalancerPolicy{Type: model.BalancerWeightedRoundRobin},
			Transport: model.TransportConfig{
				Protocol:                  model.TransportProtocolHTTP2,
				TLS:                       &model.UpstreamTLSPolicy{TrustBundleRef: fmt.Sprintf("root-%05d", upstreamIndex%profile.MaterialResources), ServerName: "upstream.internal"},
				DialTimeout:               time.Second,
				ResponseHeaderTimeout:     time.Second,
				IdleConnectionTimeout:     time.Minute,
				MaxIdleConnections:        100,
				MaxIdleConnectionsPerHost: 10,
			},
		}
	}
	return resources
}

func assertPhase3C1PrepareStats(
	t testing.TB,
	stats PrepareStats,
	profile phase3C1Profile,
	totalEndpoints int,
	initial bool,
) {
	t.Helper()
	if stats.CreatedTransports != 1 || stats.ReusedTransports != profile.Upstreams-1 {
		t.Fatalf("transport preparation stats=%+v", stats)
	}
	if initial {
		if stats.CreatedEndpoints != totalEndpoints || stats.CreatedSelections != profile.Upstreams {
			t.Fatalf("initial preparation stats=%+v", stats)
		}
	} else if stats.ReusedEndpoints != totalEndpoints || stats.ReusedSelections != profile.Upstreams {
		t.Fatalf("rotation preparation stats=%+v", stats)
	}
	if stats.Current.LiveTransports < 1 || stats.Current.LiveTransports > 2 {
		t.Fatalf("prepared live transports=%d, want 1..2", stats.Current.LiveTransports)
	}
}

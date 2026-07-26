package upstream

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	goruntime "runtime"
	"testing"
	"time"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
)

const phase3AAcceptanceSeed = 20260726

type acceptanceProfile struct {
	upstreams          int
	endpointsPerStream int
	chashPercent       int
	swaps              int
}

var normalPhase3AProfile = acceptanceProfile{
	upstreams:          1_000,
	endpointsPerStream: 10,
	chashPercent:       20,
	swaps:              2,
}

var fullPhase3AProfile = acceptanceProfile{
	upstreams:          10_000,
	endpointsPerStream: 10,
	chashPercent:       20,
	swaps:              20,
}

func TestPhase3AAcceptance(t *testing.T) {
	full := os.Getenv("GATEWAY_PHASE3A_ACCEPTANCE") == "1"
	profile := normalPhase3AProfile
	if full {
		profile = fullPhase3AProfile
	}
	resources, checksum := generatePhase3AResources(t, profile)
	registry, err := NewRegistry(64, nil)
	if err != nil {
		t.Fatal(err)
	}
	var active *PlanSet
	t.Cleanup(func() {
		if active != nil {
			active.Retire()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := registry.Close(ctx); err != nil {
			t.Errorf("Registry.Close() error = %v", err)
		}
	})

	goruntime.GC()
	var baseline goruntime.MemStats
	goruntime.ReadMemStats(&baseline)
	started := time.Now()
	candidate, err := registry.Prepare(resources)
	if err != nil {
		t.Fatal(err)
	}
	initialStats := candidate.stats
	active = candidate.Commit()
	if active == nil {
		t.Fatal("initial candidate commit returned nil")
	}
	buildElapsed := time.Since(started)
	goruntime.GC()
	var onePlanSet goruntime.MemStats
	goruntime.ReadMemStats(&onePlanSet)
	onePlanSetHeap := phase3AHeapDelta(onePlanSet.HeapAlloc, baseline.HeapAlloc)

	totalEndpoints := profile.upstreams * profile.endpointsPerStream
	if initialStats.CreatedEndpoints != totalEndpoints ||
		initialStats.CreatedSelections != profile.upstreams ||
		initialStats.CreatedTransports != 1 {
		t.Fatalf("initial prepare stats = %+v", initialStats)
	}
	if initialStats.WRRSlots > MaxSnapshotWRRSlots ||
		initialStats.HashPoints > MaxSnapshotHashPoints {
		t.Fatalf("initial balancer budget = WRR %d hash %d", initialStats.WRRSlots, initialStats.HashPoints)
	}

	for swap := 0; swap < profile.swaps; swap++ {
		reweighted := reweightPhase3AResources(resources, swap+1)
		nextCandidate, prepareErr := registry.Prepare(reweighted)
		if prepareErr != nil {
			t.Fatalf("Prepare swap %d error = %v", swap+1, prepareErr)
		}
		stats := nextCandidate.stats
		if stats.CreatedEndpoints != 0 ||
			stats.CreatedTransports != 0 ||
			stats.CreatedSelections != 0 ||
			stats.ReusedEndpoints != totalEndpoints ||
			stats.ReusedTransports != profile.upstreams ||
			stats.ReusedSelections != profile.upstreams {
			nextCandidate.Rollback()
			t.Fatalf("weight-only swap %d reuse stats = %+v", swap+1, stats)
		}
		next := nextCandidate.Commit()
		if next == nil {
			t.Fatalf("weight-only swap %d commit returned nil", swap+1)
		}
		active.Retire()
		active = next
		waitForPhase3AReaper(t, registry)
	}

	goruntime.GC()
	goruntime.GC()
	var retained goruntime.MemStats
	goruntime.ReadMemStats(&retained)
	retainedHeap := phase3AHeapDelta(retained.HeapAlloc, baseline.HeapAlloc)
	stats := registry.Stats()
	if stats.ActivePlanSets != 1 || stats.RetiredPlanSets != 0 {
		t.Fatalf("steady registry stats = %+v, want one active and zero retired plan sets", stats)
	}

	t.Logf(
		"mode=%s upstreams=%d endpoints=%d swaps=%d build=%s one_plan_set_heap=%d retained_heap=%d checksum=%s seed=%d go=%s cpus=%d",
		map[bool]string{false: "normal", true: "full"}[full],
		profile.upstreams,
		totalEndpoints,
		profile.swaps,
		buildElapsed,
		onePlanSetHeap,
		retainedHeap,
		checksum,
		phase3AAcceptanceSeed,
		goruntime.Version(),
		goruntime.NumCPU(),
	)
	if !full {
		return
	}
	if buildElapsed > 5*time.Second {
		t.Fatalf("full-envelope build = %s, want <= 5s", buildElapsed)
	}
	if onePlanSetHeap == 0 {
		t.Fatal("active plan-set heap delta is zero")
	}
	if onePlanSetHeap > 512<<20 {
		t.Fatalf("active plan-set heap = %d, want <= 512 MiB", onePlanSetHeap)
	}
	if retainedHeap > onePlanSetHeap*125/100 {
		t.Fatalf("retained heap = %d, want <= 125%% of %d", retainedHeap, onePlanSetHeap)
	}
}

func generatePhase3AResources(t testing.TB, profile acceptanceProfile) ([]model.Upstream, string) {
	t.Helper()
	resources := make([]model.Upstream, profile.upstreams)
	transport := model.TransportConfig{
		DialTimeout:               time.Second,
		ResponseHeaderTimeout:     2 * time.Second,
		IdleConnectionTimeout:     time.Minute,
		MaxIdleConnections:        1024,
		MaxIdleConnectionsPerHost: 16,
	}
	weightOffset := phase3AAcceptanceSeed % 5
	for upstreamIndex := range resources {
		endpoints := make([]model.Endpoint, profile.endpointsPerStream)
		for endpointIndex := range endpoints {
			endpoints[endpointIndex] = model.Endpoint{
				URL: fmt.Sprintf(
					"http://u%05d-e%02d.example:8080",
					upstreamIndex,
					endpointIndex,
				),
				Weight: uint32((upstreamIndex+endpointIndex+weightOffset)%5 + 1),
			}
		}
		balancer := model.BalancerPolicy{Type: model.BalancerWeightedRoundRobin}
		if upstreamIndex%100 < profile.chashPercent {
			balancer = model.BalancerPolicy{
				Type: model.BalancerConsistentHash,
				HashKey: model.HashKeyPolicy{Sources: []model.HashKeySource{{
					Type:  model.HashSourceLiteral,
					Value: "phase3a-tenant",
				}}},
			}
		}
		resources[upstreamIndex] = model.Upstream{
			ID:        fmt.Sprintf("upstream-%05d", upstreamIndex),
			Endpoints: endpoints,
			Balancer:  balancer,
			Transport: transport,
		}
	}
	canonical, err := json.Marshal(resources)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(canonical)
	return resources, hex.EncodeToString(sum[:])
}

func reweightPhase3AResources(resources []model.Upstream, revision int) []model.Upstream {
	cloned := model.CloneResourceSet(model.ResourceSet{Upstreams: resources}).Upstreams
	for upstreamIndex := range cloned {
		for endpointIndex := range cloned[upstreamIndex].Endpoints {
			cloned[upstreamIndex].Endpoints[endpointIndex].Weight = uint32(
				(upstreamIndex+endpointIndex+revision)%5 + 1,
			)
		}
	}
	return cloned
}

func waitForPhase3AReaper(t testing.TB, registry *Registry) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if registry.Stats().RetiredPlanSets == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("retired plan sets = %d, want 0", registry.Stats().RetiredPlanSets)
}

func phase3AHeapDelta(after, before uint64) uint64 {
	if after <= before {
		return 0
	}
	return after - before
}

package upstream

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
)

func TestRegistryReusesEqualRuntimeEntries(t *testing.T) {
	registry := mustRegistry(t, 64, nil)
	resource := testUpstream("users",
		testEndpoint("http://users-a:8080", 2),
		testEndpoint("http://users-b:8080", 1),
	)
	firstCandidate := mustPrepare(t, registry, []model.Upstream{resource})
	firstSet := firstCandidate.Commit()
	if firstSet == nil {
		t.Fatal("first commit = nil")
	}
	defer firstSet.Retire()

	secondCandidate := mustPrepare(t, registry, []model.Upstream{resource})
	defer secondCandidate.Rollback()
	first, _ := firstSet.Plan("users")
	second, _ := secondCandidate.Plan("users")
	if first.endpoints[0].runtime != second.endpoints[0].runtime ||
		first.transport != second.transport ||
		first.wrr.state != second.wrr.state {
		t.Fatal("equal resources did not reuse endpoint, transport, and selection state")
	}
}

func TestRegistryWeightOnlyChangeReusesRuntimesButCreatesPlan(t *testing.T) {
	registry := mustRegistry(t, 64, nil)
	resource := testUpstream("users",
		testEndpoint("http://users-a:8080", 2),
		testEndpoint("http://users-b:8080", 1),
	)
	firstCandidate := mustPrepare(t, registry, []model.Upstream{resource})
	firstSet := firstCandidate.Commit()
	defer firstSet.Retire()

	reweighted := model.CloneResourceSet(model.ResourceSet{Upstreams: []model.Upstream{resource}}).Upstreams
	reweighted[0].Endpoints[0].Weight = 7
	secondCandidate := mustPrepare(t, registry, reweighted)
	defer secondCandidate.Rollback()
	first, _ := firstSet.Plan("users")
	second, _ := secondCandidate.Plan("users")
	if first == second {
		t.Fatal("weight-only update reused immutable plan")
	}
	if first.endpoints[0].runtime != second.endpoints[0].runtime ||
		first.transport != second.transport ||
		first.wrr.state != second.wrr.state {
		t.Fatal("weight-only update failed to reuse runtime entries")
	}
}

func TestRegistryTransportOnlyChangeReusesEndpoints(t *testing.T) {
	registry := mustRegistry(t, 64, nil)
	resource := testUpstream("users", testEndpoint("http://users-a:8080", 1))
	firstCandidate := mustPrepare(t, registry, []model.Upstream{resource})
	firstSet := firstCandidate.Commit()
	defer firstSet.Retire()

	changed := resource
	changed.Endpoints = append([]model.Endpoint(nil), resource.Endpoints...)
	changed.Transport.ResponseHeaderTimeout += time.Second
	secondCandidate := mustPrepare(t, registry, []model.Upstream{changed})
	defer secondCandidate.Rollback()
	first, _ := firstSet.Plan("users")
	second, _ := secondCandidate.Plan("users")
	if first.endpoints[0].runtime != second.endpoints[0].runtime {
		t.Fatal("transport-only update recreated endpoint runtime")
	}
	if first.transport == second.transport {
		t.Fatal("transport-only update reused changed transport profile")
	}
}

func TestRegistryOwnsDisabledEndpointAndReusesItWhenEnabled(t *testing.T) {
	registry := mustRegistry(t, 64, nil)
	resource := testUpstream("users",
		testEndpoint("http://users-a:8080", 1),
		testEndpoint("http://users-b:8080", 0),
	)
	firstCandidate := mustPrepare(t, registry, []model.Upstream{resource})
	firstSet := firstCandidate.Commit()
	defer firstSet.Retire()

	disabledIdentity := endpointIdentity("users", "http://users-b:8080")
	registry.mu.Lock()
	disabledRuntime := registry.endpoints[disabledIdentity].runtime
	registry.mu.Unlock()

	enabled := model.CloneResourceSet(model.ResourceSet{Upstreams: []model.Upstream{resource}}).Upstreams
	enabled[0].Endpoints[1].Weight = 1
	secondCandidate := mustPrepare(t, registry, enabled)
	defer secondCandidate.Rollback()
	second, _ := secondCandidate.Plan("users")
	found := false
	for _, endpoint := range second.endpoints {
		if endpoint.identity == disabledIdentity {
			found = true
			if endpoint.runtime != disabledRuntime {
				t.Fatal("enabling weight-zero endpoint recreated its runtime")
			}
		}
	}
	if !found {
		t.Fatal("enabled endpoint missing from compiled plan")
	}
}

func TestRegistryBudgetFailureRollsBackPartialPrepare(t *testing.T) {
	registry := mustRegistry(t, 64, nil)
	before := registry.Stats()
	_, err := registry.Prepare(wrrBudgetResources())
	assertConfigError(t, err, "BALANCER_BUDGET_EXCEEDED", "upstreams")
	if after := registry.Stats(); after != before {
		t.Fatalf("registry stats after rollback = %+v, want %+v", after, before)
	}
}

func TestRegistryRejectsHashPointBudget(t *testing.T) {
	registry := mustRegistry(t, 64, nil)
	_, err := registry.Prepare(hashBudgetResources())
	assertConfigError(t, err, "BALANCER_BUDGET_EXCEEDED", "upstreams")
}

func TestCandidateTerminalOperationsAreExclusiveAndIdempotent(t *testing.T) {
	registry := mustRegistry(t, 64, nil)
	resource := []model.Upstream{testUpstream("users", testEndpoint("http://users:8080", 1))}

	rolledBack := mustPrepare(t, registry, resource)
	rolledBack.Rollback()
	rolledBack.Rollback()
	if set := rolledBack.Commit(); set != nil {
		t.Fatalf("commit after rollback = %+v", set)
	}

	committed := mustPrepare(t, registry, resource)
	set := committed.Commit()
	if set == nil {
		t.Fatal("commit = nil")
	}
	committed.Rollback()
	if duplicate := committed.Commit(); duplicate != nil {
		t.Fatalf("duplicate commit = %+v", duplicate)
	}
	set.Retire()
	if stats := registry.Stats(); stats.ActivePlanSets != 0 {
		t.Fatalf("stats after retirement = %+v", stats)
	}
}

func TestPlanSetDoubleReleasePanicsInsteadOfUnderflow(t *testing.T) {
	registry := mustRegistry(t, 64, nil)
	candidate := mustPrepare(t, registry, []model.Upstream{
		testUpstream("users", testEndpoint("http://users:8080", 1)),
	})
	set := candidate.Commit()
	set.Retire()
	defer func() {
		if recover() == nil {
			t.Fatal("second Release did not panic")
		}
	}()
	set.Release()
}

func TestRegistryRecoversObserverPanics(t *testing.T) {
	registry := mustRegistry(t, 64, panicRegistryObserver{})
	candidate := mustPrepare(t, registry, []model.Upstream{
		testUpstream("users", testEndpoint("http://users:8080", 1)),
	})
	candidate.Rollback()
}

type panicRegistryObserver struct{}

func (panicRegistryObserver) RegistryPrepared(PrepareStats) {
	panic("prepared")
}

func (panicRegistryObserver) RegistryRolledBack(PrepareStats) {
	panic("rolled back")
}

func (panicRegistryObserver) RegistryCleaned(CleanupStats) {
	panic("cleaned")
}

func (panicRegistryObserver) RegistryError(string, error) {
	panic("error")
}

func mustRegistry(t testing.TB, maxRetiredSnapshots int, observer Observer) *Registry {
	t.Helper()
	registry, err := NewRegistry(maxRetiredSnapshots, observer)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := registry.Close(ctx); err != nil {
			t.Errorf("Registry.Close() error = %v", err)
		}
	})
	return registry
}

func mustPrepare(t testing.TB, registry *Registry, resources []model.Upstream) *Candidate {
	t.Helper()
	candidate, err := registry.Prepare(resources)
	if err != nil {
		t.Fatal(err)
	}
	return candidate
}

func testEndpoint(rawURL string, weight uint32) model.Endpoint {
	return model.Endpoint{URL: rawURL, Weight: weight}
}

func testUpstream(id string, endpoints ...model.Endpoint) model.Upstream {
	return model.Upstream{
		ID:        id,
		Endpoints: endpoints,
		Balancer:  validWRRPolicy(),
		Transport: validTransportConfig(),
	}
}

func wrrBudgetResources() []model.Upstream {
	const upstreamCount = MaxSnapshotWRRSlots/MaxWRRSchedule + 1
	resources := make([]model.Upstream, upstreamCount)
	for upstreamIndex := range resources {
		endpoints := make([]model.Endpoint, 100)
		for endpointIndex := range endpoints {
			endpoints[endpointIndex] = testEndpoint(
				fmt.Sprintf("http://u%d-e%d.example:8080", upstreamIndex, endpointIndex),
				uint32(999+endpointIndex%2),
			)
		}
		resources[upstreamIndex] = testUpstream(fmt.Sprintf("upstream-%d", upstreamIndex), endpoints...)
	}
	return resources
}

func hashBudgetResources() []model.Upstream {
	const upstreamCount = MaxSnapshotHashPoints/MaxContinuumPoints + 1
	resources := make([]model.Upstream, upstreamCount)
	for upstreamIndex := range resources {
		endpoints := make([]model.Endpoint, 100)
		for endpointIndex := range endpoints {
			endpoints[endpointIndex] = testEndpoint(
				fmt.Sprintf("http://h%d-e%d.example:8080", upstreamIndex, endpointIndex),
				uint32(999+endpointIndex%2),
			)
		}
		resource := testUpstream(fmt.Sprintf("upstream-%d", upstreamIndex), endpoints...)
		resource.Balancer = model.BalancerPolicy{
			Type: model.BalancerConsistentHash,
			HashKey: model.HashKeyPolicy{Sources: []model.HashKeySource{{
				Type:  model.HashSourceLiteral,
				Value: "tenant",
			}}},
		}
		resources[upstreamIndex] = resource
	}
	return resources
}

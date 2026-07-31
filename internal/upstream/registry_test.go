package upstream

import (
	"context"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
	"github.com/QuanTuanHuy/g-gateway/internal/tlsmaterial"
)

func TestRegistryPrepareResourceSetUsesCompleteTransportGenerationIdentity(t *testing.T) {
	registry := mustRegistry(t, 64, nil)
	resources := tlsRegistryResourceSet(t)
	firstCandidate, err := registry.Prepare(resources)
	if err != nil {
		t.Fatal(err)
	}
	firstSet := firstCandidate.Commit()
	defer firstSet.Retire()
	firstPlan, _ := firstSet.Plan("orders")
	if firstPlan.transport.production == firstPlan.transport.probe {
		t.Fatal("production and probe transports share one pool")
	}

	sameProfile := []struct {
		name   string
		change func(*model.ResourceSet)
	}{
		{name: "weight", change: func(value *model.ResourceSet) {
			value.Upstreams[0].Endpoints[0].Weight++
		}},
		{name: "retry", change: func(value *model.ResourceSet) {
			value.Upstreams[0].Retry.TotalTimeout += time.Second
		}},
		{name: "health", change: func(value *model.ResourceSet) {
			value.Upstreams[0].Health.Active.HealthyInterval += time.Second
		}},
		{name: "route", change: func(value *model.ResourceSet) {
			value.Routes = append(value.Routes, model.Route{ID: "ignored-by-registry"})
		}},
	}
	for _, test := range sameProfile {
		t.Run("reuse "+test.name, func(t *testing.T) {
			changed := model.CloneResourceSet(resources)
			test.change(&changed)
			candidate, prepareErr := registry.Prepare(changed)
			if prepareErr != nil {
				t.Fatal(prepareErr)
			}
			defer candidate.Rollback()
			plan, _ := candidate.Plan("orders")
			if plan.transport != firstPlan.transport {
				t.Fatalf("%s-only change replaced transport generation", test.name)
			}
		})
	}

	differentProfile := []struct {
		name   string
		change func(*model.ResourceSet)
	}{
		{name: "trust bundle", change: func(value *model.ResourceSet) {
			_, bundle := tlsRegistryMaterials(t)
			value.TrustBundles[0] = bundle
		}},
		{name: "client certificate", change: func(value *model.ResourceSet) {
			certificate, _ := tlsRegistryMaterials(t)
			value.Certificates[0] = certificate
		}},
		{name: "server name", change: func(value *model.ResourceSet) {
			value.Upstreams[0].Transport.TLS.ServerName = "changed.internal"
		}},
		{name: "scheme", change: func(value *model.ResourceSet) {
			value.Upstreams[0].Endpoints[0].URL = "http://orders.internal:8080"
			value.Upstreams[0].Transport.Protocol = model.TransportProtocolHTTP1
			value.Upstreams[0].Transport.TLS = nil
		}},
		{name: "protocol", change: func(value *model.ResourceSet) {
			value.Upstreams[0].Transport.Protocol = model.TransportProtocolHTTP1
		}},
	}
	for _, test := range differentProfile {
		t.Run("rotate "+test.name, func(t *testing.T) {
			changed := model.CloneResourceSet(resources)
			test.change(&changed)
			candidate, prepareErr := registry.Prepare(changed)
			if prepareErr != nil {
				t.Fatal(prepareErr)
			}
			defer candidate.Rollback()
			plan, _ := candidate.Plan("orders")
			if plan.transport == firstPlan.transport ||
				plan.transport.production == firstPlan.transport.production ||
				plan.transport.probe == firstPlan.transport.probe {
				t.Fatalf("%s change did not rotate both transport pools", test.name)
			}
		})
	}
}

func TestRegistryPrepareResourceSetRollsBackEarlierTLSAcquisitions(t *testing.T) {
	registry := mustRegistry(t, 64, nil)
	before := registry.Stats()
	resources := tlsRegistryResourceSet(t)
	missing := testUpstream("missing", testEndpoint("https://missing.internal:8443", 1))
	missing.Transport.Protocol = model.TransportProtocolHTTP1
	missing.Transport.TLS = &model.UpstreamTLSPolicy{TrustBundleRef: "does-not-exist"}
	resources.Upstreams = append(resources.Upstreams, missing)

	_, err := registry.Prepare(resources)
	assertConfigError(t, err, "TLS_MATERIAL_REF_NOT_FOUND", "upstreams.transport.tls.trust_bundle_ref")
	if after := registry.Stats(); after != before {
		t.Fatalf("registry stats after rollback=%+v, want %+v", after, before)
	}
}

func TestMaterialRotationRetainsOldTransportUntilLeaseRelease(t *testing.T) {
	registry := mustRegistry(t, 64, nil)
	resources := tlsRegistryResourceSet(t)
	firstCandidate, err := registry.Prepare(resources)
	if err != nil {
		t.Fatal(err)
	}
	firstSet := firstCandidate.Commit()
	if !firstSet.TryAcquire() {
		t.Fatal("TryAcquire rejected committed set")
	}
	firstPlan, _ := firstSet.Plan("orders")
	var productionClosed atomic.Int32
	var probeClosed atomic.Int32
	firstPlan.transport.closeProductionIdle = func() { productionClosed.Add(1) }
	firstPlan.transport.closeProbeIdle = func() { probeClosed.Add(1) }

	rotated := model.CloneResourceSet(resources)
	_, bundle := tlsRegistryMaterials(t)
	rotated.TrustBundles[0] = bundle
	secondCandidate, err := registry.Prepare(rotated)
	if err != nil {
		t.Fatal(err)
	}
	secondSet := secondCandidate.Commit()
	defer secondSet.Retire()
	firstSet.Retire()
	registry.reapNow()
	if productionClosed.Load() != 0 || probeClosed.Load() != 0 {
		t.Fatal("old transport closed while lease remained live")
	}
	if stats := registry.Stats(); stats.LiveTransports != 2 || stats.RetiredPlanSets != 1 {
		t.Fatalf("stats with live old lease=%+v", stats)
	}

	firstSet.Release()
	registry.reapNow()
	if productionClosed.Load() != 1 || probeClosed.Load() != 1 {
		t.Fatalf("close counts production=%d probe=%d", productionClosed.Load(), probeClosed.Load())
	}
	registry.reapNow()
	if productionClosed.Load() != 1 || probeClosed.Load() != 1 {
		t.Fatal("old transport pools closed more than once")
	}
	if stats := registry.Stats(); stats.LiveTransports != 1 || stats.RetiredPlanSets != 0 {
		t.Fatalf("stats after old lease release=%+v", stats)
	}
}

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

func TestRegistryReusesHealthAndBudgetAcrossWeightOnlyRevision(t *testing.T) {
	registry := mustRegistry(t, 64, nil)
	resource := testUpstream("users", testEndpoint("http://users-a:8080", 1))
	resource.Health, resource.Retry = validResiliencePolicies()
	first := mustPrepare(t, registry, []model.Upstream{resource})
	firstPlan, _ := first.Plan("users")
	firstHealth := firstPlan.endpoints[0].health
	firstBudget := firstPlan.budget
	firstHealth.Observe(Observation{Source: SourceActive, Kind: OutcomeTimeout})
	firstHealth.Observe(Observation{Source: SourceActive, Kind: OutcomeTimeout})

	resource.Endpoints[0].Weight = 5
	second := mustPrepare(t, registry, []model.Upstream{resource})
	defer first.Rollback()
	defer second.Rollback()
	secondPlan, _ := second.Plan("users")
	if firstHealth != secondPlan.endpoints[0].health ||
		secondPlan.endpoints[0].health.State() != HealthUnhealthy {
		t.Fatal("health runtime was not reused")
	}
	if firstBudget != secondPlan.budget {
		t.Fatal("retry budget was not reused")
	}
}

func TestRegistryHealthPolicyChangeKeepsTransportButResetsHealth(t *testing.T) {
	registry := mustRegistry(t, 64, nil)
	resource := testUpstream("users", testEndpoint("http://users-a:8080", 1))
	resource.Health, resource.Retry = validResiliencePolicies()
	first := mustPrepare(t, registry, []model.Upstream{resource})
	defer first.Rollback()
	firstPlan, _ := first.Plan("users")

	resource.Health.Active.HealthyInterval += time.Second
	second := mustPrepare(t, registry, []model.Upstream{resource})
	defer second.Rollback()
	secondPlan, _ := second.Plan("users")
	if firstPlan.transport != secondPlan.transport {
		t.Fatal("health-only change replaced transport")
	}
	if firstPlan.endpoints[0].health == secondPlan.endpoints[0].health ||
		secondPlan.endpoints[0].health.State() != HealthUnknown {
		t.Fatal("health policy change did not create unknown tracker")
	}
	stats := registry.Stats()
	if stats.LiveHealthTrackers != 2 || stats.LiveRetryBudgets != 1 {
		t.Fatalf("registry stats = %+v", stats)
	}
}

func TestRegistryRollbackReleasesResilienceRuntimes(t *testing.T) {
	registry := mustRegistry(t, 64, nil)
	resource := testUpstream("users", testEndpoint("http://users-a:8080", 1))
	resource.Health, resource.Retry = validResiliencePolicies()
	candidate := mustPrepare(t, registry, []model.Upstream{resource})
	candidate.Rollback()
	stats := registry.Stats()
	if stats.LiveHealthTrackers != 0 ||
		stats.LiveRetryBudgets != 0 ||
		stats.LiveEndpoints != 0 ||
		stats.LiveTransports != 0 {
		t.Fatalf("registry stats after rollback = %+v", stats)
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

func TestRegistryUnrelatedUpstreamChangePreservesTransportIdentity(t *testing.T) {
	registry := mustRegistry(t, 64, nil)
	upstreamA := testUpstream("upstream-a", testEndpoint("http://a.example:8080", 1))
	upstreamB := testUpstream("upstream-b", testEndpoint("http://b.example:8080", 1))
	firstCandidate := mustPrepare(t, registry, []model.Upstream{upstreamA, upstreamB})
	firstSet := firstCandidate.Commit()
	defer firstSet.Retire()
	firstB, _ := firstSet.Plan("upstream-b")

	changedA := upstreamA
	changedA.Endpoints = append(changedA.Endpoints, testEndpoint("http://a-canary.example:8080", 1))
	changedA.Transport.ResponseHeaderTimeout += time.Second
	secondCandidate := mustPrepare(t, registry, []model.Upstream{changedA, upstreamB})
	defer secondCandidate.Rollback()
	secondB, _ := secondCandidate.Plan("upstream-b")

	if firstB.transport != secondB.transport {
		t.Fatal("unrelated upstream change replaced upstream B transport runtime")
	}
	if firstB.endpoints[0].runtime != secondB.endpoints[0].runtime {
		t.Fatal("unrelated upstream change replaced upstream B endpoint runtime")
	}
}

func TestRegistryTwentyRotationsPreserveUnrelatedTransportPools(t *testing.T) {
	registry := mustRegistry(t, 64, nil)
	upstreamA := testUpstream("upstream-a", testEndpoint("http://a.example:8080", 1))
	upstreamA.Transport.ResponseHeaderTimeout += time.Millisecond
	upstreamB := testUpstream("upstream-b", testEndpoint("http://b.example:8080", 1))
	resources := model.ResourceSet{Upstreams: []model.Upstream{upstreamA, upstreamB}}
	candidate, err := registry.Prepare(resources)
	if err != nil {
		t.Fatal(err)
	}
	active := candidate.Commit()
	defer func() {
		active.Retire()
	}()
	baselineB, _ := active.Plan("upstream-b")
	baselineProduction := baselineB.transport.production
	baselineProbe := baselineB.transport.probe

	for rotation := 1; rotation <= 20; rotation++ {
		nextResources := model.CloneResourceSet(resources)
		nextResources.Upstreams[0].Transport.ResponseHeaderTimeout +=
			time.Duration(rotation) * time.Millisecond
		nextCandidate, prepareErr := registry.Prepare(nextResources)
		if prepareErr != nil {
			t.Fatalf("rotation %d prepare: %v", rotation, prepareErr)
		}
		next := nextCandidate.Commit()
		nextB, _ := next.Plan("upstream-b")
		if nextB.transport.production != baselineProduction ||
			nextB.transport.probe != baselineProbe {
			t.Fatalf("rotation %d replaced unrelated production/probe pools", rotation)
		}

		active.Retire()
		registry.reapNow()
		if stats := registry.Stats(); stats.LiveTransports != 2 ||
			stats.ActivePlanSets != 1 ||
			stats.RetiredPlanSets != 0 {
			t.Fatalf("rotation %d registry did not reach steady state: %+v", rotation, stats)
		}
		active = next
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
	_, err := registry.Prepare(model.ResourceSet{Upstreams: wrrBudgetResources()})
	assertConfigError(t, err, "BALANCER_BUDGET_EXCEEDED", "upstreams")
	if after := registry.Stats(); after != before {
		t.Fatalf("registry stats after rollback = %+v, want %+v", after, before)
	}
}

func TestRegistryRejectsHashPointBudget(t *testing.T) {
	registry := mustRegistry(t, 64, nil)
	_, err := registry.Prepare(model.ResourceSet{Upstreams: hashBudgetResources()})
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
	registry.ObserveTLSHandshake("success", "server_auth", model.TransportProtocolHTTP2)
	registry.ObserveTLSFailure(TLSFailureHandshake)
	candidate := mustPrepare(t, registry, []model.Upstream{
		testUpstream("users", testEndpoint("http://users:8080", 1)),
	})
	candidate.Rollback()
	if stats := registry.Stats(); stats.LiveEndpoints != 0 || stats.LiveTransports != 0 {
		t.Fatalf("observer panic interrupted cleanup: %+v", stats)
	}
}

func TestRegistryCompactsTransportGenerationDeltas(t *testing.T) {
	observer := newRecordingRegistryObserver()
	registry := mustRegistry(t, 64, observer)
	candidate := mustPrepare(t, registry, []model.Upstream{
		testUpstream("users", testEndpoint("http://users:8080", 1)),
		testUpstream("orders", testEndpoint("http://orders:8080", 1)),
		testUpstream("billing", testEndpoint("http://billing:8080", 1)),
	})

	prepared := <-observer.prepared
	if len(prepared.TransportGenerations) != 2 {
		t.Fatalf("prepare generation deltas=%+v, want create and reuse", prepared.TransportGenerations)
	}
	assertTransportGenerationDelta(
		t,
		prepared.TransportGenerations,
		"create",
		false,
		model.TransportProtocolHTTP1,
		1,
	)
	assertTransportGenerationDelta(
		t,
		prepared.TransportGenerations,
		"reuse",
		false,
		model.TransportProtocolHTTP1,
		2,
	)

	candidate.Rollback()
	cleaned := <-observer.cleaned
	if len(cleaned.TransportGenerations) != 1 {
		t.Fatalf("cleanup generation deltas=%+v, want one retire", cleaned.TransportGenerations)
	}
	assertTransportGenerationDelta(
		t,
		cleaned.TransportGenerations,
		"retire",
		false,
		model.TransportProtocolHTTP1,
		1,
	)
}

func TestRegistryForwardsTransportTLSObserverEvents(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		_ *http.Request,
	) {
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	bundle, err := tlsmaterial.NewTrustBundle(
		"roots",
		pem.EncodeToMemory(&pem.Block{
			Type:  "CERTIFICATE",
			Bytes: server.Certificate().Raw,
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	resource := testUpstream("orders", testEndpoint(server.URL, 1))
	resource.Transport.Protocol = model.TransportProtocolHTTP1
	resource.Transport.TLS = &model.UpstreamTLSPolicy{TrustBundleRef: "roots"}
	observer := newRecordingRegistryObserver()
	registry := mustRegistry(t, 64, observer)
	candidate, err := registry.Prepare(model.ResourceSet{
		Upstreams:    []model.Upstream{resource},
		TrustBundles: []*tlsmaterial.TrustBundle{bundle},
	})
	if err != nil {
		t.Fatal(err)
	}
	set := candidate.Commit()
	plan, ok := set.Plan("orders")
	if !ok {
		t.Fatal("prepared plan is missing")
	}
	response, err := plan.transport.RoundTrip(
		mustRequest(t, context.Background(), server.URL),
	)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	select {
	case event := <-observer.handshakes:
		if event.result != "success" ||
			event.mode != "server_auth" ||
			event.protocol != model.TransportProtocolHTTP1 {
			t.Fatalf("TLS handshake event=%+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("registry did not forward TLS handshake")
	}
	set.Retire()
}

type panicRegistryObserver struct{}

func (panicRegistryObserver) RegistryPrepared(PrepareStats) {
	panic("prepared")
}

func (panicRegistryObserver) RegistryRolledBack(PrepareStats) {
	panic("rolled back")
}

func (panicRegistryObserver) RegistryRetired(RegistryStats) {
	panic("retired")
}

func (panicRegistryObserver) RegistryCleaned(CleanupStats) {
	panic("cleaned")
}

func (panicRegistryObserver) RegistryError(string, error) {
	panic("error")
}

func (panicRegistryObserver) TLSHandshake(string, string, model.TransportProtocol) {
	panic("TLS handshake")
}

func (panicRegistryObserver) TLSFailure(TLSFailureClass) {
	panic("TLS failure")
}

type recordingRegistryObserver struct {
	prepared   chan PrepareStats
	cleaned    chan CleanupStats
	handshakes chan registryTLSHandshake
}

type registryTLSHandshake struct {
	result   string
	mode     string
	protocol model.TransportProtocol
}

func newRecordingRegistryObserver() *recordingRegistryObserver {
	return &recordingRegistryObserver{
		prepared:   make(chan PrepareStats, 1),
		cleaned:    make(chan CleanupStats, 1),
		handshakes: make(chan registryTLSHandshake, 1),
	}
}

func (o *recordingRegistryObserver) RegistryPrepared(stats PrepareStats) {
	o.prepared <- stats
}

func (*recordingRegistryObserver) RegistryRolledBack(PrepareStats) {}

func (*recordingRegistryObserver) RegistryRetired(RegistryStats) {}

func (o *recordingRegistryObserver) RegistryCleaned(stats CleanupStats) {
	o.cleaned <- stats
}

func (*recordingRegistryObserver) RegistryError(string, error) {}

func (o *recordingRegistryObserver) TLSHandshake(
	result, mode string,
	protocol model.TransportProtocol,
) {
	o.handshakes <- registryTLSHandshake{
		result:   result,
		mode:     mode,
		protocol: protocol,
	}
}

func (*recordingRegistryObserver) TLSFailure(TLSFailureClass) {}

func assertTransportGenerationDelta(
	t *testing.T,
	deltas []TransportGenerationDelta,
	action string,
	tlsEnabled bool,
	protocol model.TransportProtocol,
	count int,
) {
	t.Helper()
	for _, delta := range deltas {
		if delta.Action == action && delta.TLS == tlsEnabled && delta.Protocol == protocol {
			if delta.Count != count {
				t.Fatalf("generation delta=%+v, want count %d", delta, count)
			}
			return
		}
	}
	t.Fatalf(
		"missing generation delta action=%q tls=%t protocol=%q in %+v",
		action,
		tlsEnabled,
		protocol,
		deltas,
	)
}

func mustRegistry(t testing.TB, maxRetiredSnapshots int, observer Observer) *Registry {
	t.Helper()
	registry, err := NewRegistry(RegistryOptions{
		MaxRetiredSnapshots: maxRetiredSnapshots,
		HealthWorkers:       2,
		HealthQueueCapacity: 16,
		Observer:            observer,
	})
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
	candidate, err := registry.Prepare(model.ResourceSet{Upstreams: resources})
	if err != nil {
		t.Fatal(err)
	}
	return candidate
}

func tlsRegistryResourceSet(t *testing.T) model.ResourceSet {
	t.Helper()
	certificate, bundle := tlsRegistryMaterials(t)
	health, retry := validResiliencePolicies()
	resource := testUpstream("orders", testEndpoint("https://orders.internal:8443", 1))
	resource.Transport.Protocol = model.TransportProtocolHTTP2
	resource.Transport.TLS = &model.UpstreamTLSPolicy{
		TrustBundleRef:       "roots",
		ClientCertificateRef: "client",
		ServerName:           "orders.internal",
	}
	resource.Health = health
	resource.Retry = retry
	return model.ResourceSet{
		Upstreams:    []model.Upstream{resource},
		Certificates: []*tlsmaterial.Certificate{certificate},
		TrustBundles: []*tlsmaterial.TrustBundle{bundle},
	}
}

func tlsRegistryMaterials(t *testing.T) (*tlsmaterial.Certificate, *tlsmaterial.TrustBundle) {
	t.Helper()
	certificatePEM, privateKeyPEM := profileTestPair(t)
	certificate, err := tlsmaterial.NewCertificate("client", certificatePEM, privateKeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := tlsmaterial.NewTrustBundle("roots", certificatePEM)
	if err != nil {
		t.Fatal(err)
	}
	return certificate, bundle
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

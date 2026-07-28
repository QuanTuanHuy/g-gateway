package upstream

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
)

func TestPlanSelectReturnsEndpointAndSharedTransport(t *testing.T) {
	registry := mustRegistry(t, 64, nil)
	candidate := mustPrepare(t, registry, []model.Upstream{
		testUpstream("users",
			testEndpoint("http://users-a:8080", 2),
			testEndpoint("http://users-b:8080", 1),
		),
	})
	defer candidate.Rollback()

	plan, ok := candidate.Plan("users")
	if !ok {
		t.Fatal("users plan not found")
	}
	request := httptest.NewRequest(http.MethodGet, "http://gateway.test/users", nil)
	selection, err := plan.Select(request)
	if err != nil {
		t.Fatal(err)
	}
	if !selection.Valid() || selection.Target().Host == "" {
		t.Fatalf("selection = %+v", selection)
	}
	if selection.transport != plan.transport {
		t.Fatal("selection did not retain the plan transport")
	}
	if selection.endpoint != plan.endpoints[selection.ordinal].runtime {
		t.Fatal("selection endpoint and ordinal came from different plan entries")
	}
}

func TestPlanWRRSelectsConfiguredDistribution(t *testing.T) {
	registry := mustRegistry(t, 64, nil)
	candidate := mustPrepare(t, registry, []model.Upstream{
		testUpstream("users",
			testEndpoint("http://users-a:8080", 5),
			testEndpoint("http://users-b:8080", 2),
			testEndpoint("http://users-c:8080", 1),
		),
	})
	defer candidate.Rollback()
	plan, _ := candidate.Plan("users")

	counts := make(map[string]int)
	for range 8 {
		selection, err := plan.Select(httptest.NewRequest(http.MethodGet, "http://gateway.test/", nil))
		if err != nil {
			t.Fatal(err)
		}
		counts[selection.Target().Host]++
	}
	if counts["users-a:8080"] != 5 || counts["users-b:8080"] != 2 || counts["users-c:8080"] != 1 {
		t.Fatalf("distribution = %v", counts)
	}
}

func TestPlanConsistentHashIsStickyAndExposesFallback(t *testing.T) {
	resource := testUpstream("users",
		testEndpoint("http://users-a:8080", 1),
		testEndpoint("http://users-b:8080", 1),
	)
	resource.Balancer = model.BalancerPolicy{
		Type: model.BalancerConsistentHash,
		HashKey: model.HashKeyPolicy{Sources: []model.HashKeySource{{
			Type: model.HashSourceHeader,
			Name: "X-Tenant",
		}}},
	}
	registry := mustRegistry(t, 64, nil)
	candidate := mustPrepare(t, registry, []model.Upstream{resource})
	defer candidate.Rollback()
	plan, _ := candidate.Plan("users")

	request := httptest.NewRequest(http.MethodGet, "http://gateway.test/", nil)
	request.Header.Set("X-Tenant", "acme")
	first, err := plan.Select(request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := plan.Select(request)
	if err != nil {
		t.Fatal(err)
	}
	if first.EndpointID() != second.EndpointID() || first.HashFallback() || second.HashFallback() {
		t.Fatalf("sticky selections = %+v %+v", first, second)
	}

	missing := httptest.NewRequest(http.MethodGet, "http://gateway.test/", nil)
	missing.RemoteAddr = "192.0.2.1:1234"
	fallback, err := plan.Select(missing)
	if err != nil {
		t.Fatal(err)
	}
	if !fallback.HashFallback() {
		t.Fatal("missing hash source did not expose remote_addr fallback")
	}
}

func TestPlanContainsNoRegistryLookupCallback(t *testing.T) {
	planType := reflect.TypeOf(Plan{})
	for index := range planType.NumField() {
		field := planType.Field(index)
		if field.Type.Kind() == reflect.Func {
			t.Fatalf("plan field %q is a request-time callback", field.Name)
		}
	}
}

func TestPlanSelectNextSkipsUnhealthyAndAttemptedEndpoints(t *testing.T) {
	registry := mustRegistry(t, 64, nil)
	candidate := mustPrepare(t, registry, []model.Upstream{
		testUpstream("users",
			testEndpoint("http://users-a:8080", 1),
			testEndpoint("http://users-b:8080", 1),
			testEndpoint("http://users-c:8080", 1),
		),
	})
	defer candidate.Rollback()
	plan, _ := candidate.Plan("users")
	for index := range plan.endpoints {
		plan.endpoints[index].health = newEndpointHealth(plan.endpoints[index].identity, thresholdTwoHealthPolicy(), 1)
	}
	plan.endpoints[0].health.Observe(Observation{Source: SourceActive, Kind: OutcomeTransportFailure})
	plan.endpoints[0].health.Observe(Observation{Source: SourceActive, Kind: OutcomeTransportFailure})

	var attempted AttemptSet
	if !attempted.Add(1) {
		t.Fatal("could not add untried ordinal")
	}
	selection, err := plan.SelectNext(httptest.NewRequest(http.MethodGet, "http://gateway.test/", nil), &attempted)
	if err != nil {
		t.Fatal(err)
	}
	if selection.Ordinal() != 2 {
		t.Fatalf("ordinal = %d, want 2", selection.Ordinal())
	}
}

func TestPlanSelectNextFailsClosedWhenAllEndpointsUnhealthy(t *testing.T) {
	registry := mustRegistry(t, 64, nil)
	candidate := mustPrepare(t, registry, []model.Upstream{
		testUpstream("users",
			testEndpoint("http://users-a:8080", 1),
			testEndpoint("http://users-b:8080", 1),
		),
	})
	defer candidate.Rollback()
	plan, _ := candidate.Plan("users")
	for index := range plan.endpoints {
		health := newEndpointHealth(plan.endpoints[index].identity, thresholdTwoHealthPolicy(), 1)
		health.Observe(Observation{Source: SourceActive, Kind: OutcomeTimeout})
		health.Observe(Observation{Source: SourceActive, Kind: OutcomeTimeout})
		plan.endpoints[index].health = health
	}

	if _, err := plan.SelectNext(httptest.NewRequest(http.MethodGet, "http://gateway.test/", nil), nil); !errors.Is(err, ErrNoHealthyEndpoint) {
		t.Fatalf("error = %v, want ErrNoHealthyEndpoint", err)
	}
}

func TestAttemptSetIsBoundedAndRejectsDuplicates(t *testing.T) {
	var attempted AttemptSet
	for ordinal := uint32(0); ordinal < 5; ordinal++ {
		if !attempted.Add(ordinal) || !attempted.Contains(ordinal) {
			t.Fatalf("ordinal %d was not stored", ordinal)
		}
	}
	if attempted.Add(4) {
		t.Fatal("duplicate ordinal was added")
	}
	if attempted.Add(5) {
		t.Fatal("sixth ordinal exceeded fixed capacity")
	}
}

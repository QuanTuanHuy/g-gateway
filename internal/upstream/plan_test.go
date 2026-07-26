package upstream

import (
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

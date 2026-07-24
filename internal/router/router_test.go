package router

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
)

func TestRouterMatchesRoutesAndCapturesParameters(t *testing.T) {
	compiled := mustCompile(t, []RouteSpec{
		{
			Index: 10,
			ID:    "users",
			Match: model.RouteMatch{
				Hosts:   []string{"api.example.com", "admin.example.com"},
				Path:    "/users/{id}",
				Methods: []string{"GET"},
			},
		},
	})
	request := httptest.NewRequest(http.MethodGet, "http://api.example.com/users/42", nil)

	result, err := compiled.Match(request)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Found || result.RouteIndex != 10 {
		t.Fatalf("Match() = %+v", result)
	}
	if got := materializeParams(request.URL.Path, result.Params); !reflect.DeepEqual(got, map[string]string{"id": "42"}) {
		t.Fatalf("params = %#v", got)
	}
}

func TestRouterWildcardMatchesOneLeftLabelOnly(t *testing.T) {
	compiled := mustCompile(t, []RouteSpec{{
		Index: 1,
		ID:    "wildcard",
		Match: model.RouteMatch{
			Hosts:   []string{"*.example.com"},
			Path:    "/health",
			Methods: []string{"GET"},
		},
	}})

	for _, tc := range []struct {
		host  string
		found bool
	}{
		{host: "api.example.com", found: true},
		{host: "example.com", found: false},
		{host: "v1.api.example.com", found: false},
	} {
		request := httptest.NewRequest(http.MethodGet, "http://gateway/health", nil)
		request.Host = tc.host
		result, err := compiled.Match(request)
		if err != nil {
			t.Fatal(err)
		}
		if result.Found != tc.found {
			t.Fatalf("host %q result = %+v", tc.host, result)
		}
	}
}

func TestRouterReturnsNotFound(t *testing.T) {
	compiled := mustCompile(t, []RouteSpec{{
		Index: 1,
		ID:    "health",
		Match: model.RouteMatch{Path: "/health", Methods: []string{"GET"}},
	}})
	result, err := compiled.Match(httptest.NewRequest(http.MethodGet, "http://gateway/missing", nil))
	if err != nil {
		t.Fatal(err)
	}
	if result.Found || result.MethodNotAllowed || len(result.Allow) != 0 {
		t.Fatalf("Match() = %+v", result)
	}
}

func TestRouterReturnsSortedDeduplicatedAllow(t *testing.T) {
	compiled := mustCompile(t, []RouteSpec{
		{Index: 1, ID: "first", Priority: 1, Match: model.RouteMatch{Path: "/items", Methods: []string{"POST", "GET"}}},
		{Index: 2, ID: "second", Priority: 2, Match: model.RouteMatch{Path: "/items", Methods: []string{"PUT", "GET"}}},
	})
	result, err := compiled.Match(httptest.NewRequest(http.MethodDelete, "http://gateway/items", nil))
	if err != nil {
		t.Fatal(err)
	}
	if result.Found || !result.MethodNotAllowed {
		t.Fatalf("Match() = %+v", result)
	}
	if want := []string{"GET", "POST", "PUT"}; !reflect.DeepEqual(result.Allow, want) {
		t.Fatalf("Allow = %v, want %v", result.Allow, want)
	}
}

func TestRouterPropagatesInvalidQuery(t *testing.T) {
	compiled := mustCompile(t, []RouteSpec{{
		Index: 1,
		ID:    "query",
		Match: model.RouteMatch{
			Path:    "/items",
			Methods: []string{"GET"},
			Queries: []model.Predicate{{Name: "tag", Operator: model.PredicateExists}},
		},
	}})
	request := httptest.NewRequest(http.MethodGet, "http://gateway/items", nil)
	request.URL.RawQuery = "tag=%zz"

	_, err := compiled.Match(request)
	if !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("Match() error = %v", err)
	}
}

func TestRouterRejectsDuplicateCanonicalMatch(t *testing.T) {
	_, err := Compile([]RouteSpec{
		{
			Index:    1,
			ID:       "first",
			Priority: 10,
			Match: model.RouteMatch{
				Hosts:   []string{"api.example.com", "*.example.net"},
				Path:    "/items",
				Methods: []string{"POST", "GET"},
			},
		},
		{
			Index:    2,
			ID:       "second",
			Priority: 10,
			Match: model.RouteMatch{
				Hosts:   []string{"*.example.net", "API.EXAMPLE.COM"},
				Path:    "/items",
				Methods: []string{"get", "post"},
			},
		},
	})
	if err == nil {
		t.Fatal("Compile() unexpectedly accepted duplicate canonical match")
	}
}

func TestRouterResultDoesNotDependOnDeclarationOrder(t *testing.T) {
	routes := []RouteSpec{
		{
			Index: 1,
			ID:    "z-route",
			Match: model.RouteMatch{
				Path:    "/items/{id}",
				Methods: []string{"GET"},
				Headers: []model.Predicate{{Name: "X-A", Operator: model.PredicateExists}},
			},
		},
		{
			Index: 2,
			ID:    "a-route",
			Match: model.RouteMatch{
				Path:    "/items/{id}",
				Methods: []string{"GET"},
				Headers: []model.Predicate{{Name: "X-B", Operator: model.PredicateExists}},
			},
		},
	}
	first := mustCompile(t, routes)
	second := mustCompile(t, []RouteSpec{routes[1], routes[0]})
	request := httptest.NewRequest(http.MethodGet, "http://gateway/items/42", nil)
	request.Header.Set("X-A", "1")
	request.Header.Set("X-B", "1")

	firstResult, err := first.Match(request)
	if err != nil {
		t.Fatal(err)
	}
	secondResult, err := second.Match(request)
	if err != nil {
		t.Fatal(err)
	}
	if firstResult.RouteIndex != 2 || secondResult.RouteIndex != 2 {
		t.Fatalf("results first=%+v second=%+v", firstResult, secondResult)
	}
}

func TestStaticMatchAllocations(t *testing.T) {
	compiled := mustCompile(t, []RouteSpec{{
		Index: 0,
		ID:    "static",
		Match: model.RouteMatch{Path: "/v1/users", Methods: []string{"GET"}},
	}})
	request := httptest.NewRequest(http.MethodGet, "http://gateway/v1/users", nil)
	if got := testing.AllocsPerRun(1000, func() {
		result, err := compiled.Match(request)
		if err != nil || !result.Found {
			panic("static route did not match")
		}
	}); got != 0 {
		t.Fatalf("allocations = %f, want 0", got)
	}
}

func mustCompile(t *testing.T, routes []RouteSpec) *Router {
	t.Helper()
	compiled, err := Compile(routes)
	if err != nil {
		t.Fatal(err)
	}
	return compiled
}

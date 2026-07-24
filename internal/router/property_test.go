package router

import (
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
)

func TestCompiledRouterMatchesReference(t *testing.T) {
	const cases = 10_000
	random := rand.New(rand.NewSource(20260723))
	for caseIndex := 0; caseIndex < cases; caseIndex++ {
		specs, request := generatedCase(t, random, caseIndex)
		compiled, err := Compile(specs)
		if err != nil {
			t.Fatalf("case %d compile: %v", caseIndex, err)
		}
		got, err := compiled.Match(request.Clone(request.Context()))
		if err != nil {
			t.Fatalf("case %d match: %v", caseIndex, err)
		}
		want := referenceMatch(t, specs, request.Clone(request.Context()))
		if !equivalentResult(got, want) {
			t.Fatalf("seed=20260723 case=%d got=%+v want=%+v specs=%+v request=%s", caseIndex, got, want, specs, request.URL)
		}

		shuffled := append([]RouteSpec(nil), specs...)
		random.Shuffle(len(shuffled), func(i, j int) {
			shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
		})
		shuffledRouter, err := Compile(shuffled)
		if err != nil {
			t.Fatalf("case %d shuffled compile: %v", caseIndex, err)
		}
		shuffledResult, err := shuffledRouter.Match(request.Clone(request.Context()))
		if err != nil {
			t.Fatalf("case %d shuffled match: %v", caseIndex, err)
		}
		if !equivalentResult(got, shuffledResult) {
			t.Fatalf("seed=20260723 case=%d order changed result got=%+v shuffled=%+v", caseIndex, got, shuffledResult)
		}
	}
}

func generatedCase(t *testing.T, random *rand.Rand, caseIndex int) ([]RouteSpec, *http.Request) {
	t.Helper()
	paths := []string{
		"/items/me",
		"/items/{id}",
		"/items/*",
		"/assets/{*path}",
		"/{tenant}/status",
		"/health",
	}
	requestPaths := []string{
		"/items/me",
		"/items/42",
		"/items/42/details",
		"/assets/a/b",
		"/acme/status",
		"/health",
		"/missing",
	}
	requestHosts := []string{
		"gateway.example.com",
		"other.example.com",
		"example.com",
		"v1.gateway.example.com",
	}
	requestMethods := []string{http.MethodGet, http.MethodPost, http.MethodDelete}

	request := httptest.NewRequest(
		requestMethods[random.Intn(len(requestMethods))],
		"http://gateway"+requestPaths[random.Intn(len(requestPaths))],
		nil,
	)
	request.Host = requestHosts[random.Intn(len(requestHosts))]
	query := make(url.Values)

	count := 1 + random.Intn(50)
	specs := make([]RouteSpec, 0, count)
	for i := 0; i < count; i++ {
		match := model.RouteMatch{
			Path: paths[random.Intn(len(paths))],
		}
		switch random.Intn(5) {
		case 1:
			match.Hosts = []string{"gateway.example.com"}
		case 2:
			match.Hosts = []string{"*.example.com"}
		case 3:
			match.Hosts = []string{"other.example.com"}
		case 4:
			match.Hosts = []string{"gateway.example.com", "*.example.com"}
		}
		switch random.Intn(3) {
		case 0:
			match.Methods = []string{http.MethodGet}
		case 1:
			match.Methods = []string{http.MethodPost}
		case 2:
			match.Methods = []string{http.MethodGet, http.MethodPost}
		}

		uniqueName := fmt.Sprintf("X-Case-%d-Route-%d", caseIndex, i)
		if random.Intn(2) == 0 {
			match.Headers = []model.Predicate{{
				Name:     uniqueName,
				Operator: model.PredicateExists,
			}}
			if random.Intn(4) != 0 {
				request.Header.Set(uniqueName, "present")
			}
		} else {
			queryName := fmt.Sprintf("case_%d_route_%d", caseIndex, i)
			match.Queries = []model.Predicate{{
				Name:     queryName,
				Operator: model.PredicateEquals,
				Values:   []string{"match"},
			}}
			if random.Intn(4) != 0 {
				query.Add(queryName, "match")
			}
		}
		if random.Intn(3) == 0 {
			match.Headers = append(match.Headers, model.Predicate{
				Name:     "X-Shared",
				Operator: model.PredicateOneOf,
				Values:   []string{"a", "b"},
			})
			request.Header.Set("X-Shared", "b")
		}

		specs = append(specs, RouteSpec{
			Index:    i,
			ID:       fmt.Sprintf("route-%03d", i),
			Priority: random.Intn(5) - 2,
			Match:    match,
		})
	}
	request.URL.RawQuery = query.Encode()
	return specs, request
}

func equivalentResult(left, right Result) bool {
	return left.Found == right.Found &&
		left.MethodNotAllowed == right.MethodNotAllowed &&
		left.RouteIndex == right.RouteIndex &&
		reflect.DeepEqual(left.Params, right.Params) &&
		reflect.DeepEqual(left.Allow, right.Allow)
}

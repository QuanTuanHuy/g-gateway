package router

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuanTuanHuy/g-gateway/internal/benchdataset"
	"github.com/QuanTuanHuy/g-gateway/internal/model"
)

func BenchmarkRouterScale(b *testing.B) {
	for _, routeCount := range []int{1, 1_000, 10_000, 100_000} {
		if routeCount == 1 {
			for _, position := range []string{"first", "middle", "last"} {
				resources, metadata, err := benchdataset.Generate(benchdataset.Options{
					RouteCount:       1,
					Seed:             20260723,
					Endpoint:         "http://upstream:8080",
					BaselineSentinel: position,
				})
				if err != nil {
					b.Fatal(err)
				}
				benchmarkSentinel(b, routeCount, position, compileDatasetRouter(b, resources), metadata)
			}
			continue
		}
		resources, metadata, err := benchdataset.Generate(benchdataset.Options{
			RouteCount: routeCount,
			Seed:       20260723,
			Endpoint:   "http://upstream:8080",
		})
		if err != nil {
			b.Fatal(err)
		}
		compiled := compileDatasetRouter(b, resources)
		for _, position := range []string{"first", "middle", "last"} {
			benchmarkSentinel(b, routeCount, position, compiled, metadata)
		}
	}
}

func BenchmarkRouterPatternCases(b *testing.B) {
	compiled, err := Compile([]RouteSpec{
		{
			Index: 0,
			ID:    "wildcard",
			Match: model.RouteMatch{
				Hosts:   []string{"*.example.test"},
				Path:    "/wild/static",
				Methods: []string{http.MethodGet},
			},
		},
		{
			Index: 1,
			ID:    "parameter",
			Match: model.RouteMatch{
				Path:    "/parameter/{id}",
				Methods: []string{http.MethodGet},
			},
		},
		{
			Index: 2,
			ID:    "catchall",
			Match: model.RouteMatch{
				Path:    "/catch/{*path}",
				Methods: []string{http.MethodGet},
			},
		},
		{
			Index: 3,
			ID:    "predicate",
			Match: model.RouteMatch{
				Path:    "/predicate",
				Methods: []string{http.MethodGet},
				Headers: []model.Predicate{{
					Name:     "X-Tenant",
					Operator: model.PredicateEquals,
					Values:   []string{"acme"},
				}},
			},
		},
	})
	if err != nil {
		b.Fatal(err)
	}
	cases := []struct {
		name    string
		request *http.Request
		found   bool
	}{
		{
			name:    "wildcard",
			request: httptest.NewRequest(http.MethodGet, "http://api.example.test/wild/static", nil),
			found:   true,
		},
		{
			name:    "parameter",
			request: httptest.NewRequest(http.MethodGet, "http://gateway/parameter/42", nil),
			found:   true,
		},
		{
			name:    "catchall",
			request: httptest.NewRequest(http.MethodGet, "http://gateway/catch/a/b/c", nil),
			found:   true,
		},
		{
			name: "predicate-hit",
			request: func() *http.Request {
				request := httptest.NewRequest(http.MethodGet, "http://gateway/predicate", nil)
				request.Header.Set("X-Tenant", "acme")
				return request
			}(),
			found: true,
		},
		{
			name:    "predicate-miss",
			request: httptest.NewRequest(http.MethodGet, "http://gateway/predicate", nil),
			found:   false,
		},
		{
			name:    "not-found",
			request: httptest.NewRequest(http.MethodGet, "http://gateway/missing", nil),
			found:   false,
		},
	}
	for _, test := range cases {
		b.Run(test.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				result, err := compiled.Match(test.request)
				if err != nil || result.Found != test.found {
					b.Fatalf("Match() = %+v, %v; want found=%t", result, err, test.found)
				}
			}
		})
	}
}

func BenchmarkRouterCollisionStress10K(b *testing.B) {
	const routeCount = 10_000
	specs := make([]RouteSpec, routeCount)
	for index := range specs {
		specs[index] = RouteSpec{
			Index: index,
			ID:    fmt.Sprintf("collision-%05d", index),
			Match: model.RouteMatch{
				Path:    "/collision",
				Methods: []string{http.MethodGet},
				Headers: []model.Predicate{{
					Name:     fmt.Sprintf("X-Candidate-%05d", index),
					Operator: model.PredicateEquals,
					Values:   []string{"hit"},
				}},
			},
		}
	}
	compiled, err := Compile(specs)
	if err != nil {
		b.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "http://gateway/collision", nil)
	request.Header.Set("X-Candidate-09999", "hit")
	b.ReportAllocs()
	for b.Loop() {
		result, err := compiled.Match(request)
		if err != nil || !result.Found || result.RouteIndex != 9999 {
			b.Fatalf("Match() = %+v, %v", result, err)
		}
	}
}

func benchmarkSentinel(
	b *testing.B,
	routeCount int,
	position string,
	compiled *Router,
	metadata benchdataset.Metadata,
) {
	sentinel := metadata.Sentinels[position]
	request := httptest.NewRequest(http.MethodGet, sentinel.URL, nil)
	b.Run(fmt.Sprintf("routes=%d/%s", routeCount, position), func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			result, err := compiled.Match(request)
			if err != nil || !result.Found {
				b.Fatalf("sentinel did not match: %+v, %v", result, err)
			}
		}
	})
}

func compileDatasetRouter(tb testing.TB, resources model.ResourceSet) *Router {
	tb.Helper()
	specs := make([]RouteSpec, len(resources.Routes))
	for index, route := range resources.Routes {
		specs[index] = RouteSpec{
			Index:    index,
			ID:       route.ID,
			Priority: route.Priority,
			Match:    route.Match,
		}
	}
	compiled, err := Compile(specs)
	if err != nil {
		tb.Fatal(err)
	}
	return compiled
}

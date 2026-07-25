package router

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
)

func FuzzPathPattern(f *testing.F) {
	for _, seed := range []string{"/", "/users/{id}", "/api/*", "/assets/{*path}", "/bad/{*x}/tail"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, pattern string) {
		compiled, err := compilePathPattern(pattern)
		if err != nil {
			return
		}
		if compiled.raw != pattern {
			t.Fatalf("raw pattern changed: %q", compiled.raw)
		}
	})
}

func FuzzQueryEvaluation(f *testing.F) {
	for _, seed := range []string{"a=1", "a=%2F&a=", "bad=%", "plus=a+b"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, rawQuery string) {
		request := httptest.NewRequest(http.MethodGet, "http://gateway/path", nil)
		request.URL.RawQuery = rawQuery
		evaluation := newEvaluation(request)
		_, _, err := evaluation.queryValues("a")
		if err != nil && !errors.Is(err, ErrInvalidQuery) {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func FuzzHostNormalization(f *testing.F) {
	for _, seed := range []string{"api.example.com", "API.Example.COM:8443", "api.example.com.", "", "[::1]:8080"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, authority string) {
		normalized, err := NormalizeRequestHost(authority)
		if err != nil {
			return
		}
		if normalized == "" || normalized != strings.ToLower(normalized) || strings.HasSuffix(normalized, ".") {
			t.Fatalf("invalid normalized host %q from %q", normalized, authority)
		}
		again, err := NormalizeRequestHost(normalized)
		if err != nil || again != normalized {
			t.Fatalf("normalization is not idempotent: %q => %q, %v", normalized, again, err)
		}
	})
}

func FuzzPredicateCompile(f *testing.F) {
	f.Add("X-Role", "equals", "reader", true)
	f.Add("tag", "one_of", "a", true)
	f.Add("", "exists", "", false)
	f.Fuzz(func(t *testing.T, name, operator, value string, includeValue bool) {
		predicate := model.Predicate{
			Name:     name,
			Operator: model.PredicateOperator(operator),
		}
		if includeValue {
			predicate.Values = []string{value}
		}
		_, _ = compilePredicates([]model.Predicate{predicate}, nil)
	})
}

func FuzzRouterCompileAndMatch(f *testing.F) {
	f.Add([]byte{0, 1, 2, 3})
	f.Add([]byte("router-seed"))
	f.Fuzz(func(t *testing.T, data []byte) {
		count := 1 + len(data)%64
		paths := []string{"/health", "/items/{id}", "/items/*", "/assets/{*path}"}
		specs := make([]RouteSpec, 0, count)
		for i := 0; i < count; i++ {
			value := fuzzByte(data, i)
			match := model.RouteMatch{
				Path:    paths[int(value)%len(paths)],
				Methods: []string{http.MethodGet},
				Headers: []model.Predicate{{
					Name:     fmt.Sprintf("X-Fuzz-%d", i),
					Operator: model.PredicateExists,
				}},
			}
			switch value % 3 {
			case 1:
				match.Hosts = []string{"gateway.example.com"}
			case 2:
				match.Hosts = []string{"*.example.com"}
			}
			specs = append(specs, RouteSpec{
				Index:    i,
				ID:       fmt.Sprintf("route-%d", i),
				Priority: int(int8(value)),
				Match:    match,
			})
		}

		compiled, err := Compile(specs)
		if err != nil {
			t.Fatalf("Compile() error = %v", err)
		}
		request := httptest.NewRequest(http.MethodGet, "http://gateway.example.com/items/42", nil)
		for i := 0; i < count; i++ {
			request.Header.Set(fmt.Sprintf("X-Fuzz-%d", i), "present")
		}
		first, err := compiled.Match(request.Clone(request.Context()))
		if err != nil {
			t.Fatalf("first Match() error = %v", err)
		}
		second, err := compiled.Match(request.Clone(request.Context()))
		if err != nil {
			t.Fatalf("second Match() error = %v", err)
		}
		if !equivalentResult(first, second) {
			t.Fatalf("repeat mismatch first=%+v second=%+v", first, second)
		}
	})
}

func fuzzByte(data []byte, index int) byte {
	if len(data) == 0 {
		return 0
	}
	return data[index%len(data)]
}

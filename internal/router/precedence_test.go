package router

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
)

func TestPrecedenceMatrix(t *testing.T) {
	routes := []RouteSpec{
		{Index: 0, ID: "hostless", Match: model.RouteMatch{Path: "/users/{id}", Methods: []string{"GET"}}},
		{Index: 1, ID: "wild", Match: model.RouteMatch{Hosts: []string{"*.example.com"}, Path: "/users/{id}", Methods: []string{"GET"}}},
		{Index: 2, ID: "exact-param", Match: model.RouteMatch{Hosts: []string{"api.example.com"}, Path: "/users/{id}", Methods: []string{"GET"}}},
		{Index: 3, ID: "exact-static", Match: model.RouteMatch{Hosts: []string{"api.example.com"}, Path: "/users/me", Methods: []string{"GET"}}},
		{Index: 4, ID: "priority-wins", Priority: 100, Match: model.RouteMatch{Path: "/users/{id}", Methods: []string{"GET"}}},
	}
	compiled := mustCompile(t, routes)
	result, err := compiled.Match(httptest.NewRequest(http.MethodGet, "http://api.example.com/users/me", nil))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Found || result.RouteIndex != 4 {
		t.Fatalf("result = %+v", result)
	}
}

func TestPrecedenceLayers(t *testing.T) {
	headerExists := func(name string) []model.Predicate {
		return []model.Predicate{{Name: name, Operator: model.PredicateExists}}
	}
	tests := []struct {
		name    string
		routes  []RouteSpec
		url     string
		headers map[string]string
		want    int
	}{
		{
			name: "priority",
			routes: []RouteSpec{
				{Index: 1, ID: "specific", Match: model.RouteMatch{Hosts: []string{"api.example.com"}, Path: "/items/me", Methods: []string{"GET"}}},
				{Index: 2, ID: "priority", Priority: 1, Match: model.RouteMatch{Path: "/items/{id}", Methods: []string{"GET"}}},
			},
			url:  "http://api.example.com/items/me",
			want: 2,
		},
		{
			name: "host rank",
			routes: []RouteSpec{
				{Index: 1, ID: "hostless", Match: model.RouteMatch{Path: "/items", Methods: []string{"GET"}}},
				{Index: 2, ID: "wildcard", Match: model.RouteMatch{Hosts: []string{"*.example.com"}, Path: "/items", Methods: []string{"GET"}}},
				{Index: 3, ID: "exact", Match: model.RouteMatch{Hosts: []string{"api.example.com"}, Path: "/items", Methods: []string{"GET"}}},
			},
			url:  "http://api.example.com/items",
			want: 3,
		},
		{
			name: "static over parameter",
			routes: []RouteSpec{
				{Index: 1, ID: "parameter", Match: model.RouteMatch{Path: "/items/{id}", Methods: []string{"GET"}}},
				{Index: 2, ID: "static", Match: model.RouteMatch{Path: "/items/me", Methods: []string{"GET"}}},
			},
			url:  "http://gateway/items/me",
			want: 2,
		},
		{
			name: "parameter over prefix",
			routes: []RouteSpec{
				{Index: 1, ID: "prefix", Match: model.RouteMatch{Path: "/items/*", Methods: []string{"GET"}}},
				{Index: 2, ID: "parameter", Match: model.RouteMatch{Path: "/items/{id}", Methods: []string{"GET"}}},
			},
			url:  "http://gateway/items/me",
			want: 2,
		},
		{
			name: "more static segments",
			routes: []RouteSpec{
				{Index: 1, ID: "none-static", Match: model.RouteMatch{Path: "/{first}/{second}", Methods: []string{"GET"}}},
				{Index: 2, ID: "one-static", Match: model.RouteMatch{Path: "/a/{second}", Methods: []string{"GET"}}},
			},
			url:  "http://gateway/a/b",
			want: 2,
		},
		{
			name: "longer pattern",
			routes: []RouteSpec{
				{Index: 1, ID: "short", Match: model.RouteMatch{Path: "/a/{x}/*", Methods: []string{"GET"}}},
				{Index: 2, ID: "long", Match: model.RouteMatch{Path: "/a/{x}/{y}/*", Methods: []string{"GET"}}},
			},
			url:  "http://gateway/a/b/c/d",
			want: 2,
		},
		{
			name: "more predicates",
			routes: []RouteSpec{
				{Index: 1, ID: "one", Match: model.RouteMatch{Path: "/items", Methods: []string{"GET"}, Headers: headerExists("X-A")}},
				{Index: 2, ID: "two", Match: model.RouteMatch{Path: "/items", Methods: []string{"GET"}, Headers: append(headerExists("X-A"), headerExists("X-B")...)}},
			},
			url:     "http://gateway/items",
			headers: map[string]string{"X-A": "1", "X-B": "1"},
			want:    2,
		},
		{
			name: "lower route ID",
			routes: []RouteSpec{
				{Index: 1, ID: "z-route", Match: model.RouteMatch{Path: "/items", Methods: []string{"GET"}, Headers: headerExists("X-A")}},
				{Index: 2, ID: "a-route", Match: model.RouteMatch{Path: "/items", Methods: []string{"GET"}, Headers: headerExists("X-B")}},
			},
			url:     "http://gateway/items",
			headers: map[string]string{"X-A": "1", "X-B": "1"},
			want:    2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			compiled := mustCompile(t, tt.routes)
			request := httptest.NewRequest(http.MethodGet, tt.url, nil)
			for name, value := range tt.headers {
				request.Header.Set(name, value)
			}
			result, err := compiled.Match(request)
			if err != nil {
				t.Fatal(err)
			}
			if !result.Found || result.RouteIndex != tt.want {
				t.Fatalf("Match() = %+v, want route index %d", result, tt.want)
			}
		})
	}
}

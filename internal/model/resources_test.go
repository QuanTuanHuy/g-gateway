package model

import (
	"encoding/json"
	"testing"
)

func TestCloneResourceSetDoesNotAliasInput(t *testing.T) {
	raw := json.RawMessage(`{"header_name":"X-Trace-ID"}`)
	in := ResourceSet{
		Routes: []Route{{
			ID:       "users",
			Priority: 10,
			Match: RouteMatch{
				Hosts:   []string{"api.example.com"},
				Path:    "/users/{id}",
				Methods: []string{"GET"},
				Headers: []Predicate{{
					Name:     "X-Tenant",
					Operator: PredicateEquals,
					Values:   []string{"acme"},
				}},
				Queries: []Predicate{{
					Name:     "verbose",
					Operator: PredicateOneOf,
					Values:   []string{"true", "1"},
				}},
			},
			ServiceRef: "users-service",
			Plugins: []PluginAttachment{{
				Name:      "request-id",
				Enabled:   true,
				RawConfig: raw,
			}},
		}},
		Services: []Service{{
			ID:          "users-service",
			UpstreamRef: "users-upstream",
			Plugins: []PluginAttachment{{
				Name:      "header-rewrite",
				Enabled:   true,
				RawConfig: json.RawMessage(`{"request":{"set":{"X-Service":"users"}}}`),
			}},
		}},
		Upstreams: []Upstream{{
			ID:        "users-upstream",
			Endpoints: []string{"http://upstream:8080"},
		}},
	}

	got := CloneResourceSet(in)
	in.Routes[0].Match.Hosts[0] = "mutated.example.com"
	in.Routes[0].Match.Methods[0] = "POST"
	in.Routes[0].Match.Headers[0].Values[0] = "mutated"
	in.Routes[0].Match.Queries[0].Values[0] = "mutated"
	in.Routes[0].Plugins[0].RawConfig[0] = 'x'
	in.Services[0].Plugins[0].RawConfig[0] = 'x'
	in.Upstreams[0].Endpoints[0] = "http://mutated:8080"

	route := got.Routes[0]
	if route.Match.Hosts[0] != "api.example.com" ||
		route.Match.Methods[0] != "GET" ||
		route.Match.Headers[0].Values[0] != "acme" ||
		route.Match.Queries[0].Values[0] != "true" {
		t.Fatalf("CloneResourceSet() aliases route input: %+v", route)
	}
	if string(route.Plugins[0].RawConfig) != `{"header_name":"X-Trace-ID"}` {
		t.Fatalf("route plugin config = %q", route.Plugins[0].RawConfig)
	}
	if string(got.Services[0].Plugins[0].RawConfig) != `{"request":{"set":{"X-Service":"users"}}}` {
		t.Fatalf("service plugin config = %q", got.Services[0].Plugins[0].RawConfig)
	}
	if got.Upstreams[0].Endpoints[0] != "http://upstream:8080" {
		t.Fatalf("upstream endpoints = %v", got.Upstreams[0].Endpoints)
	}
}

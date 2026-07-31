package model

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/QuanTuanHuy/g-gateway/internal/tlsmaterial"
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
			Endpoints: []Endpoint{{URL: "http://upstream:8080", Weight: 1}},
			Balancer:  BalancerPolicy{Type: BalancerWeightedRoundRobin},
		}},
	}

	got := CloneResourceSet(in)
	in.Routes[0].Match.Hosts[0] = "mutated.example.com"
	in.Routes[0].Match.Methods[0] = "POST"
	in.Routes[0].Match.Headers[0].Values[0] = "mutated"
	in.Routes[0].Match.Queries[0].Values[0] = "mutated"
	in.Routes[0].Plugins[0].RawConfig[0] = 'x'
	in.Services[0].Plugins[0].RawConfig[0] = 'x'
	in.Upstreams[0].Endpoints[0].URL = "http://mutated:8080"

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
	if got.Upstreams[0].Endpoints[0].URL != "http://upstream:8080" {
		t.Fatalf("upstream endpoints = %v", got.Upstreams[0].Endpoints)
	}
}

func TestCloneResourceSetClonesEndpointAndHashSources(t *testing.T) {
	in := ResourceSet{Upstreams: []Upstream{{
		ID:        "users",
		Endpoints: []Endpoint{{URL: "http://users:8080", Weight: 5}},
		Balancer: BalancerPolicy{
			Type: BalancerConsistentHash,
			HashKey: HashKeyPolicy{Sources: []HashKeySource{{
				Type: HashSourceHeader,
				Name: "X-Tenant",
			}}},
		},
	}}}

	got := CloneResourceSet(in)
	in.Upstreams[0].Endpoints[0].URL = "http://mutated:8080"
	in.Upstreams[0].Balancer.HashKey.Sources[0].Name = "X-Mutated"

	if got.Upstreams[0].Endpoints[0].URL != "http://users:8080" {
		t.Fatalf("endpoint URL = %q", got.Upstreams[0].Endpoints[0].URL)
	}
	if got.Upstreams[0].Balancer.HashKey.Sources[0].Name != "X-Tenant" {
		t.Fatalf("hash source = %+v", got.Upstreams[0].Balancer.HashKey.Sources[0])
	}
}

func TestCloneResourceSetClonesResiliencePolicies(t *testing.T) {
	timeout := 3 * time.Second
	attempts := uint8(3)
	methods := []string{"GET", "POST"}
	in := ResourceSet{
		Routes: []Route{{
			ID: "users",
			Resilience: RouteResiliencePolicy{
				TotalTimeout: &timeout,
				MaxAttempts:  &attempts,
				Methods:      &methods,
				RetryOn:      &RetryOnPolicy{Statuses: []uint16{503}},
			},
		}},
		Upstreams: []Upstream{{
			ID: "users",
			Health: HealthPolicy{
				Active: &ActiveHealthPolicy{
					Type:              HealthCheckHTTP,
					HealthyStatuses:   []uint16{200},
					UnhealthyStatuses: []uint16{503},
				},
				Passive: &PassiveHealthPolicy{UnhealthyStatuses: []uint16{503}},
			},
			Retry: RetryPolicy{
				Methods: []string{"GET"},
				RetryOn: RetryOnPolicy{Statuses: []uint16{503}},
			},
		}},
	}

	got := CloneResourceSet(in)
	(*in.Routes[0].Resilience.Methods)[0] = "DELETE"
	in.Routes[0].Resilience.RetryOn.Statuses[0] = 504
	in.Upstreams[0].Health.Active.HealthyStatuses[0] = 204
	in.Upstreams[0].Health.Passive.UnhealthyStatuses[0] = 500
	in.Upstreams[0].Retry.Methods[0] = "HEAD"

	if (*got.Routes[0].Resilience.Methods)[0] != "GET" ||
		got.Routes[0].Resilience.RetryOn.Statuses[0] != 503 ||
		got.Upstreams[0].Health.Active.HealthyStatuses[0] != 200 ||
		got.Upstreams[0].Health.Passive.UnhealthyStatuses[0] != 503 ||
		got.Upstreams[0].Retry.Methods[0] != "GET" {
		t.Fatalf("clone shares resilience state: %+v", got)
	}
}

func TestCloneResourceSetClonesTLSReferencesAndSharesImmutableMaterial(t *testing.T) {
	certificate := new(tlsmaterial.Certificate)
	bundle := new(tlsmaterial.TrustBundle)
	in := ResourceSet{
		Certificates: []*tlsmaterial.Certificate{certificate},
		TrustBundles: []*tlsmaterial.TrustBundle{bundle},
		Upstreams: []Upstream{{
			ID: "orders",
			Transport: TransportConfig{
				Protocol: TransportProtocolHTTP2,
				TLS: &UpstreamTLSPolicy{
					TrustBundleRef:       "roots",
					ClientCertificateRef: "client",
					ServerName:           "orders.internal",
				},
			},
		}},
	}

	got := CloneResourceSet(in)
	in.Upstreams[0].Transport.TLS.ServerName = "changed.internal"
	in.Certificates[0] = nil
	in.TrustBundles[0] = nil

	if got.Upstreams[0].Transport.TLS.ServerName != "orders.internal" {
		t.Fatal("CloneResourceSet() shares mutable TLS policy")
	}
	if got.Certificates[0] != certificate || got.TrustBundles[0] != bundle {
		t.Fatal("CloneResourceSet() did not retain immutable material handles")
	}
}

package upstream

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
)

func TestNormalizeCanonicalizesEndpointIdentity(t *testing.T) {
	got, err := Normalize([]model.Upstream{{
		ID: "users",
		Endpoints: []model.Endpoint{{
			URL:    "http://EXAMPLE.COM.:80/",
			Weight: 1,
		}},
		Balancer:  validWRRPolicy(),
		Transport: validTransportConfig(),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Endpoints[0].URL != "http://example.com:80" {
		t.Fatalf("URL = %q", got[0].Endpoints[0].URL)
	}
}

func TestNormalizeRejectsDuplicateEndpointIdentity(t *testing.T) {
	_, err := Normalize([]model.Upstream{{
		ID: "users",
		Endpoints: []model.Endpoint{
			{URL: "http://example.com", Weight: 1},
			{URL: "http://EXAMPLE.COM:80/", Weight: 2},
		},
		Balancer:  validWRRPolicy(),
		Transport: validTransportConfig(),
	}})
	assertConfigError(t, err, "UPSTREAM_ENDPOINT_DUPLICATE", "upstreams[0].endpoints[1].url")
}

func TestNormalizeRejectsInvalidResources(t *testing.T) {
	tooManyEndpoints := make([]model.Endpoint, MaxUpstreamEndpoints+1)
	for i := range tooManyEndpoints {
		tooManyEndpoints[i] = model.Endpoint{URL: "http://host-" + strings.Repeat("a", i%10+1) + ".example", Weight: 1}
	}

	tests := []struct {
		name      string
		upstreams []model.Upstream
		code      string
		field     string
	}{
		{
			name:      "empty endpoints",
			upstreams: []model.Upstream{{ID: "users", Balancer: validWRRPolicy(), Transport: validTransportConfig()}},
			code:      "UPSTREAM_ENDPOINTS_EMPTY",
			field:     "upstreams[0].endpoints",
		},
		{
			name: "no active endpoint",
			upstreams: []model.Upstream{validUpstreamWith(model.Endpoint{
				URL: "http://example.com", Weight: 0,
			})},
			code:  "UPSTREAM_NO_ACTIVE_ENDPOINT",
			field: "upstreams[0].endpoints",
		},
		{
			name: "invalid weight",
			upstreams: []model.Upstream{validUpstreamWith(model.Endpoint{
				URL: "http://example.com", Weight: MaxEndpointWeight + 1,
			})},
			code:  "UPSTREAM_WEIGHT_INVALID",
			field: "upstreams[0].endpoints[0].weight",
		},
		{
			name: "endpoint limit",
			upstreams: []model.Upstream{{
				ID: "users", Endpoints: tooManyEndpoints, Balancer: validWRRPolicy(), Transport: validTransportConfig(),
			}},
			code:  "UPSTREAM_ENDPOINT_LIMIT",
			field: "upstreams[0].endpoints",
		},
		{
			name: "invalid scheme",
			upstreams: []model.Upstream{validUpstreamWith(model.Endpoint{
				URL: "ftp://example.com", Weight: 1,
			})},
			code:  "UPSTREAM_ENDPOINT_INVALID",
			field: "upstreams[0].endpoints[0].url",
		},
		{
			name: "invalid user info",
			upstreams: []model.Upstream{validUpstreamWith(model.Endpoint{
				URL: "http://user@example.com", Weight: 1,
			})},
			code:  "UPSTREAM_ENDPOINT_INVALID",
			field: "upstreams[0].endpoints[0].url",
		},
		{
			name: "invalid query",
			upstreams: []model.Upstream{validUpstreamWith(model.Endpoint{
				URL: "http://example.com?x=1", Weight: 1,
			})},
			code:  "UPSTREAM_ENDPOINT_INVALID",
			field: "upstreams[0].endpoints[0].url",
		},
		{
			name: "invalid fragment",
			upstreams: []model.Upstream{validUpstreamWith(model.Endpoint{
				URL: "http://example.com#fragment", Weight: 1,
			})},
			code:  "UPSTREAM_ENDPOINT_INVALID",
			field: "upstreams[0].endpoints[0].url",
		},
		{
			name: "invalid path",
			upstreams: []model.Upstream{validUpstreamWith(model.Endpoint{
				URL: "http://example.com/api", Weight: 1,
			})},
			code:  "UPSTREAM_ENDPOINT_INVALID",
			field: "upstreams[0].endpoints[0].url",
		},
		{
			name: "non ASCII DNS",
			upstreams: []model.Upstream{validUpstreamWith(model.Endpoint{
				URL: "http://café.example", Weight: 1,
			})},
			code:  "UPSTREAM_ENDPOINT_INVALID",
			field: "upstreams[0].endpoints[0].url",
		},
		{
			name: "invalid port",
			upstreams: []model.Upstream{validUpstreamWith(model.Endpoint{
				URL: "http://example.com:70000", Weight: 1,
			})},
			code:  "UPSTREAM_ENDPOINT_INVALID",
			field: "upstreams[0].endpoints[0].url",
		},
		{
			name: "invalid balancer",
			upstreams: []model.Upstream{func() model.Upstream {
				upstream := validUpstreamWith(model.Endpoint{URL: "http://example.com", Weight: 1})
				upstream.Balancer.Type = "random"
				return upstream
			}()},
			code:  "BALANCER_TYPE_INVALID",
			field: "upstreams[0].balancer.type",
		},
		{
			name: "WRR hash policy",
			upstreams: []model.Upstream{func() model.Upstream {
				upstream := validUpstreamWith(model.Endpoint{URL: "http://example.com", Weight: 1})
				upstream.Balancer.HashKey.Sources = []model.HashKeySource{{Type: model.HashSourceRemoteAddr}}
				return upstream
			}()},
			code:  "HASH_KEY_INVALID",
			field: "upstreams[0].balancer.hash_key.sources",
		},
		{
			name: "consistent hash without sources",
			upstreams: []model.Upstream{func() model.Upstream {
				upstream := validUpstreamWith(model.Endpoint{URL: "http://example.com", Weight: 1})
				upstream.Balancer.Type = model.BalancerConsistentHash
				return upstream
			}()},
			code:  "HASH_KEY_INVALID",
			field: "upstreams[0].balancer.hash_key.sources",
		},
		{
			name: "too many hash sources",
			upstreams: []model.Upstream{func() model.Upstream {
				upstream := validUpstreamWith(model.Endpoint{URL: "http://example.com", Weight: 1})
				upstream.Balancer.Type = model.BalancerConsistentHash
				upstream.Balancer.HashKey.Sources = make([]model.HashKeySource, MaxHashKeySources+1)
				for i := range upstream.Balancer.HashKey.Sources {
					upstream.Balancer.HashKey.Sources[i] = model.HashKeySource{Type: model.HashSourceLiteral, Value: "tenant"}
				}
				return upstream
			}()},
			code:  "HASH_KEY_INVALID",
			field: "upstreams[0].balancer.hash_key.sources",
		},
		{
			name: "invalid transport",
			upstreams: []model.Upstream{func() model.Upstream {
				upstream := validUpstreamWith(model.Endpoint{URL: "http://example.com", Weight: 1})
				upstream.Transport.DialTimeout = 0
				return upstream
			}()},
			code:  "TRANSPORT_PROFILE_INVALID",
			field: "upstreams[0].transport.dial_timeout",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Normalize(test.upstreams)
			assertConfigError(t, err, test.code, test.field)
		})
	}
}

func TestNormalizeDefaultsWRRAndCanonicalizesHashHeader(t *testing.T) {
	wrr := validUpstreamWith(model.Endpoint{URL: "http://example.com", Weight: 1})
	wrr.Balancer.Type = ""
	got, err := Normalize([]model.Upstream{wrr})
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Balancer.Type != model.BalancerWeightedRoundRobin {
		t.Fatalf("balancer = %q", got[0].Balancer.Type)
	}

	chash := validUpstreamWith(model.Endpoint{URL: "http://example.com", Weight: 1})
	chash.Balancer = model.BalancerPolicy{
		Type: model.BalancerConsistentHash,
		HashKey: model.HashKeyPolicy{Sources: []model.HashKeySource{{
			Type: model.HashSourceHeader,
			Name: "x-tenant-id",
		}}},
	}
	got, err = Normalize([]model.Upstream{chash})
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Balancer.HashKey.Sources[0].Name != "X-Tenant-Id" {
		t.Fatalf("header name = %q", got[0].Balancer.HashKey.Sources[0].Name)
	}
}

func TestNormalizeSortsEndpointsByCanonicalIdentity(t *testing.T) {
	upstream := validUpstreamWith(model.Endpoint{URL: "http://z.example", Weight: 1})
	upstream.Endpoints = append(upstream.Endpoints, model.Endpoint{URL: "http://A.example:80/", Weight: 2})

	got, err := Normalize([]model.Upstream{upstream})
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Endpoints[0].URL != "http://a.example:80" ||
		got[0].Endpoints[1].URL != "http://z.example:80" {
		t.Fatalf("endpoints = %+v", got[0].Endpoints)
	}
}

func TestNormalizeCanonicalizesIPAddresses(t *testing.T) {
	upstream := validUpstreamWith(model.Endpoint{URL: "http://[::ffff:192.0.2.1]/", Weight: 1})

	got, err := Normalize([]model.Upstream{upstream})
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Endpoints[0].URL != "http://192.0.2.1:80" {
		t.Fatalf("URL = %q", got[0].Endpoints[0].URL)
	}
}

func TestNormalizeRejectsGlobalLimits(t *testing.T) {
	_, err := Normalize(make([]model.Upstream, MaxUpstreams+1))
	assertConfigError(t, err, "UPSTREAM_ENDPOINT_LIMIT", "upstreams")

	resources := make([]model.Upstream, MaxSnapshotEndpoints/MaxUpstreamEndpoints+1)
	for upstreamIndex := range resources {
		resources[upstreamIndex] = validUpstreamWith(model.Endpoint{URL: "http://example.com", Weight: 1})
		resources[upstreamIndex].ID = "upstream-" + strconv.Itoa(upstreamIndex)
		resources[upstreamIndex].Endpoints = make([]model.Endpoint, MaxUpstreamEndpoints)
		for endpointIndex := range resources[upstreamIndex].Endpoints {
			resources[upstreamIndex].Endpoints[endpointIndex] = model.Endpoint{
				URL:    fmt.Sprintf("http://host-%d-%d.example", upstreamIndex, endpointIndex),
				Weight: 1,
			}
		}
	}
	_, err = Normalize(resources)
	assertConfigError(t, err, "UPSTREAM_ENDPOINT_LIMIT", "upstreams[100].endpoints")
}

func TestNormalizeRejectsInvalidHashSources(t *testing.T) {
	tests := []model.HashKeySource{
		{Type: model.HashSourceHeader, Name: "bad header"},
		{Type: model.HashSourceHeader, Name: "X-Tenant", Value: "unexpected"},
		{Type: model.HashSourceCookie, Name: ""},
		{Type: model.HashSourceRemoteAddr, Name: "unexpected"},
		{Type: model.HashSourceLiteral, Value: ""},
		{Type: "unknown"},
	}
	for _, source := range tests {
		t.Run(string(source.Type)+"/"+source.Name, func(t *testing.T) {
			upstream := validUpstreamWith(model.Endpoint{URL: "http://example.com", Weight: 1})
			upstream.Balancer = model.BalancerPolicy{
				Type:    model.BalancerConsistentHash,
				HashKey: model.HashKeyPolicy{Sources: []model.HashKeySource{source}},
			}
			_, err := Normalize([]model.Upstream{upstream})
			var configErr *ConfigError
			if !errors.As(err, &configErr) || configErr.Code != "HASH_KEY_INVALID" {
				t.Fatalf("error = %#v, want HASH_KEY_INVALID", err)
			}
		})
	}
}

func TestNormalizeRejectsInvalidHealthAndRetryPolicies(t *testing.T) {
	tests := []struct {
		name     string
		change   func(*model.Upstream)
		wantCode string
	}{
		{
			name: "passive requires active",
			change: func(up *model.Upstream) {
				up.Health = model.HealthPolicy{
					Passive: &model.PassiveHealthPolicy{HTTPFailures: 1},
				}
			},
			wantCode: "PASSIVE_HEALTH_REQUIRES_ACTIVE",
		},
		{
			name: "retry attempts capped",
			change: func(up *model.Upstream) {
				up.Retry.MaxAttempts = 6
			},
			wantCode: "RETRY_POLICY_INVALID",
		},
		{
			name: "status outside retry allowlist",
			change: func(up *model.Upstream) {
				up.Retry.RetryOn.Statuses = []uint16{409}
			},
			wantCode: "RETRY_STATUS_INVALID",
		},
		{
			name: "tcp rejects http fields",
			change: func(up *model.Upstream) {
				up.Health.Active.Type = model.HealthCheckTCP
				up.Health.Active.Path = "/healthz"
			},
			wantCode: "ACTIVE_HEALTH_INVALID",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			upstream := validUpstreamWith(model.Endpoint{URL: "http://example.com", Weight: 1})
			upstream.Health, upstream.Retry = validResiliencePolicies()
			test.change(&upstream)
			_, err := Normalize([]model.Upstream{upstream})
			var configErr *ConfigError
			if !errors.As(err, &configErr) || configErr.Code != test.wantCode {
				t.Fatalf("error = %#v, want %s", err, test.wantCode)
			}
		})
	}
}

func TestNormalizeCanonicalizesRetryMethodsAndStatuses(t *testing.T) {
	upstream := validUpstreamWith(model.Endpoint{URL: "http://example.com", Weight: 1})
	upstream.Health, upstream.Retry = validResiliencePolicies()
	upstream.Retry.Methods = []string{"post", "GET", "get"}
	upstream.Retry.RetryOn.Statuses = []uint16{503, 429, 503}
	upstream.Health.Active.HealthyStatuses = []uint16{204, 200, 204}

	got, err := Normalize([]model.Upstream{upstream})
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(got[0].Retry.Methods) != "[GET POST]" ||
		fmt.Sprint(got[0].Retry.RetryOn.Statuses) != "[429 503]" ||
		fmt.Sprint(got[0].Health.Active.HealthyStatuses) != "[200 204]" {
		t.Fatalf("normalized resilience = health:%+v retry:%+v", got[0].Health, got[0].Retry)
	}
}

func TestNormalizeDefaultsZeroRetryPolicyToOneAttempt(t *testing.T) {
	upstream := validUpstreamWith(model.Endpoint{URL: "http://example.com", Weight: 1})
	got, err := Normalize([]model.Upstream{upstream})
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Retry.MaxAttempts != 1 || got[0].Retry.TotalTimeout != 0 {
		t.Fatalf("legacy retry = %+v", got[0].Retry)
	}
}

func assertConfigError(t *testing.T, err error, code, field string) {
	t.Helper()
	var configErr *ConfigError
	if !errors.As(err, &configErr) {
		t.Fatalf("error = %v, want *ConfigError", err)
	}
	if configErr.Code != code || configErr.Field != field {
		t.Fatalf("error = %+v, want code %q field %q", configErr, code, field)
	}
}

func validUpstreamWith(endpoint model.Endpoint) model.Upstream {
	return model.Upstream{
		ID:        "users",
		Endpoints: []model.Endpoint{endpoint},
		Balancer:  validWRRPolicy(),
		Transport: validTransportConfig(),
	}
}

func validWRRPolicy() model.BalancerPolicy {
	return model.BalancerPolicy{Type: model.BalancerWeightedRoundRobin}
}

func validTransportConfig() model.TransportConfig {
	return model.TransportConfig{
		DialTimeout:               time.Second,
		ResponseHeaderTimeout:     time.Second,
		IdleConnectionTimeout:     time.Minute,
		MaxIdleConnections:        100,
		MaxIdleConnectionsPerHost: 10,
	}
}

func validResiliencePolicies() (model.HealthPolicy, model.RetryPolicy) {
	return model.HealthPolicy{
			Active: &model.ActiveHealthPolicy{
				Type:              model.HealthCheckHTTP,
				Timeout:           time.Second,
				HealthyInterval:   5 * time.Second,
				UnhealthyInterval: 2 * time.Second,
				HealthySuccesses:  2,
				HTTPFailures:      3,
				TransportFailures: 2,
				Timeouts:          2,
				HealthyStatuses:   []uint16{200, 204},
				UnhealthyStatuses: []uint16{429, 500, 502, 503, 504},
				Path:              "/",
			},
		},
		model.RetryPolicy{
			MaxAttempts: 3,
			Methods:     []string{"GET", "HEAD"},
			RetryOn: model.RetryOnPolicy{
				ConnectFailure:        true,
				ConnectionFailure:     true,
				ResponseHeaderTimeout: true,
				Statuses:              []uint16{429, 503},
			},
			Budget:       model.RetryBudgetPolicy{RatioPer1000: 100, Burst: 10, MaxInflight: 32},
			TotalTimeout: 30 * time.Second,
		}
}

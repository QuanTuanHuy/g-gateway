package benchdataset

import (
	"encoding/hex"
	"fmt"
	"reflect"
	"testing"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
)

func TestGenerateStandardDatasetIsDeterministic(t *testing.T) {
	options := Options{
		RouteCount: 100_000,
		Seed:       20260723,
		Endpoint:   "http://upstream-performance:8080",
	}
	first, firstMeta, err := Generate(options)
	if err != nil {
		t.Fatal(err)
	}
	second, secondMeta, err := Generate(options)
	if err != nil {
		t.Fatal(err)
	}
	if firstMeta.Checksum != secondMeta.Checksum || !reflect.DeepEqual(first, second) {
		t.Fatalf("dataset is not deterministic: %s != %s", firstMeta.Checksum, secondMeta.Checksum)
	}
	if firstMeta.HostCounts != (HostCounts{Exact: 60_000, Wildcard: 20_000, Hostless: 20_000}) {
		t.Fatalf("host counts = %+v", firstMeta.HostCounts)
	}
	if firstMeta.PathCounts != (PathCounts{Static: 50_000, Parameter: 20_000, Prefix: 15_000, CatchAll: 15_000}) {
		t.Fatalf("path counts = %+v", firstMeta.PathCounts)
	}
	if firstMeta.MethodCounts != (MethodCounts{Standard: 90_000, Custom: 10_000}) {
		t.Fatalf("method counts = %+v", firstMeta.MethodCounts)
	}
	if firstMeta.PredicateRoutes != 20_000 {
		t.Fatalf("predicate routes = %d", firstMeta.PredicateRoutes)
	}
	if firstMeta.ServiceRoutes != 50_000 {
		t.Fatalf("service routes = %d", firstMeta.ServiceRoutes)
	}
	if got := firstMeta.PluginCounts["request-id"]; got != 10_000 {
		t.Fatalf("request-id routes = %d", got)
	}
	if got := firstMeta.PluginCounts["header-rewrite"]; got != 10_000 {
		t.Fatalf("header-rewrite routes = %d", got)
	}
	if firstMeta.RouteCount != 100_000 || len(first.Routes) != 100_000 {
		t.Fatalf("route count metadata=%d resources=%d", firstMeta.RouteCount, len(first.Routes))
	}
	checksum, err := hex.DecodeString(firstMeta.Checksum)
	if err != nil || len(checksum) != 32 {
		t.Fatalf("checksum = %q, want SHA-256 hex: %v", firstMeta.Checksum, err)
	}
	assertUniqueMatchSignatures(t, first.Routes)
	assertStandardSentinels(t, firstMeta)
}

func TestGenerateOneRouteUsesRequestedEquivalentSentinel(t *testing.T) {
	for _, position := range []string{"first", "middle", "last"} {
		t.Run(position, func(t *testing.T) {
			resources, metadata, err := Generate(Options{
				RouteCount:       1,
				Seed:             20260723,
				Endpoint:         "http://upstream-performance:8080",
				BaselineSentinel: position,
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(resources.Routes) != 1 {
				t.Fatalf("routes = %d, want 1", len(resources.Routes))
			}
			sentinel := metadata.Sentinels[position]
			route := resources.Routes[0]
			if route.ID != sentinel.RouteID ||
				route.Match.Path != sentinel.Path ||
				len(route.Match.Hosts) != 1 ||
				route.Match.Hosts[0] != sentinel.Host {
				t.Fatalf("route = %+v, sentinel = %+v", route, sentinel)
			}
			if len(route.Plugins) != 0 || route.ServiceRef != "" {
				t.Fatalf("baseline sentinel is not plugin-free/direct: %+v", route)
			}
		})
	}
}

func TestGenerateRejectsInvalidOptions(t *testing.T) {
	tests := []struct {
		name    string
		options Options
	}{
		{name: "route count", options: Options{Endpoint: "http://upstream:8080"}},
		{name: "too few standard routes", options: Options{RouteCount: 3, Endpoint: "http://upstream:8080"}},
		{name: "endpoint", options: Options{RouteCount: 1, BaselineSentinel: "first"}},
		{
			name: "missing one-route sentinel",
			options: Options{
				RouteCount: 1,
				Endpoint:   "http://upstream:8080",
			},
		},
		{
			name: "invalid one-route sentinel",
			options: Options{
				RouteCount:       1,
				Endpoint:         "http://upstream:8080",
				BaselineSentinel: "unknown",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := Generate(test.options); err == nil {
				t.Fatal("Generate() error = nil")
			}
		})
	}
}

func assertUniqueMatchSignatures(t *testing.T, routes []model.Route) {
	t.Helper()
	seen := make(map[string]string, len(routes))
	for _, route := range routes {
		signature := fmt.Sprintf(
			"%v|%s|%v|%v|%v",
			route.Match.Hosts,
			route.Match.Path,
			route.Match.Methods,
			route.Match.Headers,
			route.Match.Queries,
		)
		if previous, duplicate := seen[signature]; duplicate {
			t.Fatalf("routes %q and %q have duplicate match signature %q", previous, route.ID, signature)
		}
		seen[signature] = route.ID
	}
}

func assertStandardSentinels(t *testing.T, metadata Metadata) {
	t.Helper()
	want := map[string]Sentinel{
		"first": {
			RouteID: "sentinel-first",
			Host:    "sentinel-first.bench.test",
			Path:    "/__sentinel/first",
			URL:     "http://sentinel-first.bench.test/__sentinel/first",
		},
		"middle": {
			RouteID: "sentinel-middle",
			Host:    "sentinel-middle.bench.test",
			Path:    "/__sentinel/middle",
			URL:     "http://sentinel-middle.bench.test/__sentinel/middle",
		},
		"last": {
			RouteID: "sentinel-last",
			Host:    "sentinel-last.bench.test",
			Path:    "/__sentinel/last",
			URL:     "http://sentinel-last.bench.test/__sentinel/last",
		},
	}
	if !reflect.DeepEqual(metadata.Sentinels, want) {
		t.Fatalf("sentinels = %+v, want %+v", metadata.Sentinels, want)
	}
}

package benchdataset

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/url"
	"sort"
	"time"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
)

const (
	SchemaVersion     = 1
	benchmarkService  = "bench-service"
	benchmarkUpstream = "bench-upstream"
)

type Options struct {
	RouteCount       int
	Seed             int64
	Endpoint         string
	BaselineSentinel string
}

type HostCounts struct {
	Exact    int `json:"exact"`
	Wildcard int `json:"wildcard"`
	Hostless int `json:"hostless"`
}

type PathCounts struct {
	Static    int `json:"static"`
	Parameter int `json:"parameter"`
	Prefix    int `json:"prefix"`
	CatchAll  int `json:"catch_all"`
}

type MethodCounts struct {
	Standard int `json:"standard"`
	Custom   int `json:"custom"`
}

type Sentinel struct {
	RouteID string `json:"route_id"`
	Host    string `json:"host"`
	Path    string `json:"path"`
	URL     string `json:"url"`
}

type Metadata struct {
	SchemaVersion   int                 `json:"schema_version"`
	Seed            int64               `json:"seed"`
	RouteCount      int                 `json:"route_count"`
	Checksum        string              `json:"checksum"`
	HostCounts      HostCounts          `json:"host_counts"`
	PathCounts      PathCounts          `json:"path_counts"`
	MethodCounts    MethodCounts        `json:"method_counts"`
	PredicateRoutes int                 `json:"predicate_routes"`
	ServiceRoutes   int                 `json:"service_routes"`
	PluginCounts    map[string]int      `json:"plugin_counts"`
	Sentinels       map[string]Sentinel `json:"sentinels"`
}

func Generate(options Options) (model.ResourceSet, Metadata, error) {
	if err := validateOptions(options); err != nil {
		return model.ResourceSet{}, Metadata{}, err
	}
	if options.RouteCount == 1 {
		return generateBaseline(options)
	}

	metadata := Metadata{
		SchemaVersion: SchemaVersion,
		Seed:          options.Seed,
		RouteCount:    options.RouteCount,
		HostCounts: HostCounts{
			Exact:    ratioCount(options.RouteCount, 60),
			Wildcard: ratioCount(options.RouteCount, 20),
		},
		PathCounts: PathCounts{
			Static:    ratioCount(options.RouteCount, 50),
			Parameter: ratioCount(options.RouteCount, 20),
			Prefix:    ratioCount(options.RouteCount, 15),
		},
		MethodCounts: MethodCounts{
			Standard: ratioCount(options.RouteCount, 90),
		},
		PredicateRoutes: ratioCount(options.RouteCount, 20),
		ServiceRoutes:   ratioCount(options.RouteCount, 50),
		PluginCounts: map[string]int{
			"request-id":     ratioCount(options.RouteCount, 10),
			"header-rewrite": ratioCount(options.RouteCount, 10),
		},
		Sentinels: standardSentinels(),
	}
	metadata.HostCounts.Hostless = options.RouteCount - metadata.HostCounts.Exact - metadata.HostCounts.Wildcard
	metadata.PathCounts.CatchAll = options.RouteCount -
		metadata.PathCounts.Static -
		metadata.PathCounts.Parameter -
		metadata.PathCounts.Prefix
	metadata.MethodCounts.Custom = options.RouteCount - metadata.MethodCounts.Standard

	sentinelPositions := map[int]string{
		0:                      "first",
		options.RouteCount / 2: "middle",
		options.RouteCount - 1: "last",
	}
	exactHosts := categoryAssignments(
		options.RouteCount,
		sentinelPositions,
		metadata.HostCounts.Exact,
		metadata.HostCounts.Wildcard,
	)
	pathKinds := categoryAssignments(
		options.RouteCount,
		sentinelPositions,
		metadata.PathCounts.Static,
		metadata.PathCounts.Parameter,
		metadata.PathCounts.Prefix,
	)
	methodKinds := categoryAssignments(
		options.RouteCount,
		sentinelPositions,
		metadata.MethodCounts.Standard,
	)
	predicateRoutes := selectNonSentinels(options.RouteCount, sentinelPositions, 0, metadata.PredicateRoutes)
	serviceRoutes := selectNonSentinels(options.RouteCount, sentinelPositions, 0, metadata.ServiceRoutes)
	requestIDRoutes := selectNonSentinels(
		options.RouteCount,
		sentinelPositions,
		0,
		metadata.PluginCounts["request-id"],
	)
	headerRewriteRoutes := selectNonSentinels(
		options.RouteCount,
		sentinelPositions,
		metadata.PluginCounts["request-id"],
		metadata.PluginCounts["header-rewrite"],
	)

	resources := baseResources(options.Endpoint, metadata.ServiceRoutes > 0)
	resources.Routes = make([]model.Route, 0, options.RouteCount)
	for index := 0; index < options.RouteCount; index++ {
		if position, sentinel := sentinelPositions[index]; sentinel {
			resources.Routes = append(resources.Routes, sentinelRoute(metadata.Sentinels[position]))
			continue
		}
		route := generatedRoute(index, exactHosts[index], pathKinds[index], methodKinds[index])
		if predicateRoutes[index] {
			addPredicate(&route, index)
		}
		if serviceRoutes[index] {
			route.ServiceRef = benchmarkService
			route.UpstreamRef = ""
		}
		if requestIDRoutes[index] {
			route.Plugins = append(route.Plugins, model.PluginAttachment{
				Name:      "request-id",
				Enabled:   true,
				RawConfig: json.RawMessage(`{"header_name":"X-Bench-Request-ID"}`),
			})
		}
		if headerRewriteRoutes[index] {
			route.Plugins = append(route.Plugins, model.PluginAttachment{
				Name:    "header-rewrite",
				Enabled: true,
				RawConfig: json.RawMessage(fmt.Sprintf(
					`{"request":{"set":{"X-Bench-Route":"%s"}},"response":{"set":{"X-Bench-Gateway":"g-gateway"}}}`,
					route.ID,
				)),
			})
		}
		resources.Routes = append(resources.Routes, route)
	}
	random := rand.New(rand.NewSource(options.Seed))
	random.Shuffle(len(resources.Routes), func(i, j int) {
		resources.Routes[i], resources.Routes[j] = resources.Routes[j], resources.Routes[i]
	})
	checksum, err := resourceChecksum(resources)
	if err != nil {
		return model.ResourceSet{}, Metadata{}, err
	}
	metadata.Checksum = checksum
	return resources, metadata, nil
}

func validateOptions(options Options) error {
	if options.RouteCount <= 0 {
		return fmt.Errorf("route count must be greater than zero")
	}
	if options.RouteCount > 1 && options.RouteCount < 5 {
		return fmt.Errorf("standard dataset requires at least five routes")
	}
	endpoint, err := url.Parse(options.Endpoint)
	if err != nil || endpoint.Scheme != "http" || endpoint.Host == "" {
		return fmt.Errorf("endpoint must be an absolute HTTP URL")
	}
	if options.RouteCount == 1 {
		if _, ok := standardSentinels()[options.BaselineSentinel]; !ok {
			return fmt.Errorf("one-route dataset requires baseline sentinel first, middle, or last")
		}
	} else if options.BaselineSentinel != "" {
		return fmt.Errorf("baseline sentinel is only valid for a one-route dataset")
	}
	return nil
}

func generateBaseline(options Options) (model.ResourceSet, Metadata, error) {
	sentinel := standardSentinels()[options.BaselineSentinel]
	resources := baseResources(options.Endpoint, false)
	resources.Routes = []model.Route{sentinelRoute(sentinel)}
	checksum, err := resourceChecksum(resources)
	if err != nil {
		return model.ResourceSet{}, Metadata{}, err
	}
	return resources, Metadata{
		SchemaVersion: SchemaVersion,
		Seed:          options.Seed,
		RouteCount:    1,
		Checksum:      checksum,
		HostCounts:    HostCounts{Exact: 1},
		PathCounts:    PathCounts{Static: 1},
		MethodCounts:  MethodCounts{Standard: 1},
		PluginCounts: map[string]int{
			"request-id":     0,
			"header-rewrite": 0,
		},
		Sentinels: map[string]Sentinel{options.BaselineSentinel: sentinel},
	}, nil
}

func baseResources(endpoint string, includeService bool) model.ResourceSet {
	resources := model.ResourceSet{
		Upstreams: []model.Upstream{{
			ID:        benchmarkUpstream,
			Endpoints: []model.Endpoint{{URL: endpoint, Weight: 1}},
			Balancer:  model.BalancerPolicy{Type: model.BalancerWeightedRoundRobin},
			Transport: model.TransportConfig{
				DialTimeout:               3 * time.Second,
				ResponseHeaderTimeout:     10 * time.Second,
				IdleConnectionTimeout:     90 * time.Second,
				MaxIdleConnections:        1024,
				MaxIdleConnectionsPerHost: 1024,
			},
		}},
	}
	if includeService {
		resources.Services = []model.Service{{
			ID:          benchmarkService,
			UpstreamRef: benchmarkUpstream,
		}}
	}
	return resources
}

func sentinelRoute(sentinel Sentinel) model.Route {
	return model.Route{
		ID: sentinel.RouteID,
		Match: model.RouteMatch{
			Hosts:   []string{sentinel.Host},
			Path:    sentinel.Path,
			Methods: []string{"GET"},
		},
		UpstreamRef: benchmarkUpstream,
	}
}

func standardSentinels() map[string]Sentinel {
	return map[string]Sentinel{
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
}

func generatedRoute(index, hostKind, pathKind, methodKind int) model.Route {
	route := model.Route{
		ID:          fmt.Sprintf("route-%06d", index),
		UpstreamRef: benchmarkUpstream,
	}
	switch hostKind {
	case 0:
		route.Match.Hosts = []string{fmt.Sprintf("route-%06d.bench.test", index)}
	case 1:
		route.Match.Hosts = []string{fmt.Sprintf("*.group-%06d.bench.test", index)}
	}
	switch pathKind {
	case 0:
		route.Match.Path = fmt.Sprintf("/static/%06d", index)
	case 1:
		route.Match.Path = fmt.Sprintf("/parameter/%06d/{id}", index)
	case 2:
		route.Match.Path = fmt.Sprintf("/prefix/%06d/*", index)
	default:
		route.Match.Path = fmt.Sprintf("/catch/%06d/{*path}", index)
	}
	if methodKind == 0 {
		standard := [...]string{"GET", "POST", "PUT", "DELETE"}
		route.Match.Methods = []string{standard[index%len(standard)]}
	} else {
		route.Match.Methods = []string{fmt.Sprintf("BENCH%d", index%10)}
	}
	return route
}

func addPredicate(route *model.Route, index int) {
	operators := [...]model.PredicateOperator{
		model.PredicateExists,
		model.PredicateNotExists,
		model.PredicateEquals,
		model.PredicateNotEquals,
		model.PredicateOneOf,
	}
	operator := operators[index%len(operators)]
	predicate := model.Predicate{
		Operator: operator,
	}
	if operator != model.PredicateExists && operator != model.PredicateNotExists {
		predicate.Values = []string{fmt.Sprintf("value-%06d", index)}
		if operator == model.PredicateOneOf {
			predicate.Values = append(predicate.Values, fmt.Sprintf("alternate-%06d", index))
		}
	}
	if index%2 == 0 {
		predicate.Name = fmt.Sprintf("X-Bench-%06d", index)
		route.Match.Headers = []model.Predicate{predicate}
	} else {
		predicate.Name = fmt.Sprintf("q%06d", index)
		route.Match.Queries = []model.Predicate{predicate}
	}
}

func ratioCount(total, percentage int) int {
	return total * percentage / 100
}

func categoryAssignments(
	total int,
	sentinels map[int]string,
	counts ...int,
) []int {
	assignments := make([]int, total)
	remaining := append([]int(nil), counts...)
	if len(remaining) > 0 {
		remaining[0] -= len(sentinels)
	}
	category := 0
	for index := 0; index < total; index++ {
		if _, sentinel := sentinels[index]; sentinel {
			assignments[index] = 0
			continue
		}
		for category < len(remaining) && remaining[category] == 0 {
			category++
		}
		assignments[index] = category
		if category < len(remaining) {
			remaining[category]--
		}
	}
	return assignments
}

func selectNonSentinels(
	total int,
	sentinels map[int]string,
	offset int,
	count int,
) []bool {
	selected := make([]bool, total)
	seen := 0
	for index := 0; index < total && count > 0; index++ {
		if _, sentinel := sentinels[index]; sentinel {
			continue
		}
		if seen < offset {
			seen++
			continue
		}
		selected[index] = true
		count--
	}
	return selected
}

func resourceChecksum(resources model.ResourceSet) (string, error) {
	canonical := model.CloneResourceSet(resources)
	sort.Slice(canonical.Routes, func(i, j int) bool {
		return canonical.Routes[i].ID < canonical.Routes[j].ID
	})
	sort.Slice(canonical.Services, func(i, j int) bool {
		return canonical.Services[i].ID < canonical.Services[j].ID
	})
	sort.Slice(canonical.Upstreams, func(i, j int) bool {
		return canonical.Upstreams[i].ID < canonical.Upstreams[j].ID
	})
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("encode canonical dataset: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

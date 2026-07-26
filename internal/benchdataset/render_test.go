package benchdataset

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/QuanTuanHuy/g-gateway/internal/config"
	"github.com/QuanTuanHuy/g-gateway/internal/model"
	"go.yaml.in/yaml/v3"
)

func TestRenderGatewayEmitsStrictV1Alpha2Configuration(t *testing.T) {
	resources := rendererFixture()
	metadata := Metadata{SchemaVersion: 1, RouteCount: len(resources.Routes), Checksum: "fixture-checksum"}
	readableFile, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := RenderGateway(resources, metadata, GatewayRenderOptions{
		HTTPAddress:     "127.0.0.1:18080",
		HTTPSAddress:    "127.0.0.1:18443",
		AdminAddress:    "127.0.0.1:19090",
		CertificateFile: readableFile,
		PrivateKeyFile:  readableFile,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rendered), "# benchmark_checksum: fixture-checksum") {
		t.Fatalf("rendered gateway config lacks checksum metadata:\n%s", rendered)
	}
	path := filepath.Join(t.TempDir(), "gateway.yaml")
	if err := os.WriteFile(path, rendered, 0o600); err != nil {
		t.Fatal(err)
	}
	bootstrap, loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("strict config reload failed: %v\n%s", err, rendered)
	}
	if bootstrap.HTTP.Address != "127.0.0.1:18080" ||
		bootstrap.HTTPS.Address != "127.0.0.1:18443" ||
		bootstrap.Admin.Address != "127.0.0.1:19090" {
		t.Fatalf("listener addresses = %+v", bootstrap)
	}
	if len(loaded.Routes) != len(resources.Routes) ||
		len(loaded.Services) != len(resources.Services) ||
		len(loaded.Upstreams) != len(resources.Upstreams) {
		t.Fatalf(
			"resource counts = routes %d services %d upstreams %d",
			len(loaded.Routes),
			len(loaded.Services),
			len(loaded.Upstreams),
		)
	}
}

func TestRenderAPISIXTranslatesRoutesPredicatesAndPlugins(t *testing.T) {
	resources := rendererFixture()
	metadata := Metadata{SchemaVersion: 1, RouteCount: len(resources.Routes), Checksum: "fixture-checksum"}
	rendered, err := RenderAPISIX(resources, metadata)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rendered), "# benchmark_checksum: fixture-checksum") ||
		!strings.HasSuffix(strings.TrimSpace(string(rendered)), "#END") {
		t.Fatalf("APISIX metadata/end marker missing:\n%s", rendered)
	}

	var document map[string]any
	if err := yaml.Unmarshal(rendered, &document); err != nil {
		t.Fatalf("parse APISIX YAML: %v\n%s", err, rendered)
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, fragment := range []string{
		`"http":"radixtree_uri_with_parameter"`,
		`"uri":"/users/:id"`,
		`"uri":"/prefix/*"`,
		`"uri":"/files/*"`,
		`"http_x_tenant"`,
		`"arg_mode"`,
		`"request-id"`,
		`"include_in_response":true`,
		`"algorithm":"uuid"`,
		`"proxy-rewrite"`,
		`"response-rewrite"`,
	} {
		if !strings.Contains(text, fragment) {
			t.Fatalf("APISIX document does not contain %q:\n%s", fragment, rendered)
		}
	}
	routes, ok := document["routes"].([]any)
	if !ok || len(routes) != len(resources.Routes) {
		t.Fatalf("APISIX route count = %#v", document["routes"])
	}
	services, ok := document["services"].([]any)
	if !ok || len(services) != len(resources.Services) {
		t.Fatalf("APISIX service count = %#v", document["services"])
	}
	upstreams, ok := document["upstreams"].([]any)
	if !ok || len(upstreams) != len(resources.Upstreams) {
		t.Fatalf("APISIX upstream count = %#v", document["upstreams"])
	}
}

func rendererFixture() model.ResourceSet {
	return model.ResourceSet{
		Routes: []model.Route{
			{
				ID: "exact",
				Match: model.RouteMatch{
					Hosts:   []string{"api.example.com"},
					Path:    "/health",
					Methods: []string{"GET"},
					Headers: []model.Predicate{{
						Name:     "X-Tenant",
						Operator: model.PredicateExists,
					}},
				},
				UpstreamRef: "upstream",
				Plugins: []model.PluginAttachment{{
					Name:      "request-id",
					Enabled:   true,
					RawConfig: json.RawMessage(`{"header_name":"X-Correlation-ID"}`),
				}},
			},
			{
				ID: "parameter",
				Match: model.RouteMatch{
					Hosts:   []string{"*.example.com"},
					Path:    "/users/{id}",
					Methods: []string{"GET"},
					Queries: []model.Predicate{{
						Name:     "mode",
						Operator: model.PredicateOneOf,
						Values:   []string{"full", "compact"},
					}},
				},
				ServiceRef: "service",
				Plugins: []model.PluginAttachment{{
					Name:    "header-rewrite",
					Enabled: true,
					RawConfig: json.RawMessage(`{
						"request":{"remove":["X-Remove"],"set":{"X-Route":"users"},"add":{"X-Trace":["a","b"]}},
						"response":{"set":{"X-Gateway":"g-gateway"}}
					}`),
				}},
			},
			{
				ID:          "prefix",
				Match:       model.RouteMatch{Path: "/prefix/*", Methods: []string{"POST"}},
				UpstreamRef: "upstream",
			},
			{
				ID: "catchall",
				Match: model.RouteMatch{
					Path:    "/files/{*path}",
					Methods: []string{"BENCH"},
					Headers: []model.Predicate{{
						Name:     "X-State",
						Operator: model.PredicateNotEquals,
						Values:   []string{"disabled"},
					}},
					Queries: []model.Predicate{{
						Name:     "debug",
						Operator: model.PredicateNotExists,
					}},
				},
				UpstreamRef: "upstream",
			},
		},
		Services: []model.Service{{ID: "service", UpstreamRef: "upstream"}},
		Upstreams: []model.Upstream{{
			ID:        "upstream",
			Endpoints: []model.Endpoint{{URL: "http://upstream-performance:8080", Weight: 1}},
			Balancer:  model.BalancerPolicy{Type: model.BalancerWeightedRoundRobin},
			Transport: model.TransportConfig{
				DialTimeout:               3_000_000_000,
				ResponseHeaderTimeout:     10_000_000_000,
				IdleConnectionTimeout:     90_000_000_000,
				MaxIdleConnections:        1024,
				MaxIdleConnectionsPerHost: 1024,
			},
		}},
	}
}

func TestRendererFixtureDoesNotAliasInput(t *testing.T) {
	resources := rendererFixture()
	before, err := json.Marshal(resources)
	if err != nil {
		t.Fatal(err)
	}
	metadata := Metadata{SchemaVersion: 1, Checksum: "checksum"}
	if _, err := RenderGateway(resources, metadata, GatewayRenderOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := RenderAPISIX(resources, metadata); err != nil {
		t.Fatal(err)
	}
	after, err := json.Marshal(resources)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("renderer mutated input resources")
	}
}

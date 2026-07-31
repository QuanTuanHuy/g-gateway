package config

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
	"github.com/QuanTuanHuy/g-gateway/internal/tlsmaterial"
)

func TestDecodeValidV1Alpha5TLSResourcesAndTransport(t *testing.T) {
	bootstrap, resources, err := Decode(strings.NewReader(validV5Document(t)))
	if err != nil {
		t.Fatal(err)
	}
	if bootstrap.Runtime.Health.Workers != DefaultHealthWorkers {
		t.Fatalf("health workers = %d", bootstrap.Runtime.Health.Workers)
	}
	if len(resources.Certificates) != 1 || len(resources.TrustBundles) != 1 {
		t.Fatalf("material counts certificates=%d bundles=%d", len(resources.Certificates), len(resources.TrustBundles))
	}
	if resources.Certificates[0].Fingerprint() == (tlsmaterial.Fingerprint{}) ||
		resources.TrustBundles[0].Fingerprint() == (tlsmaterial.Fingerprint{}) {
		t.Fatal("decoded material fingerprint is zero")
	}
	transport := resources.Upstreams[0].Transport
	if transport.Protocol != model.TransportProtocolHTTP2 || transport.TLS == nil {
		t.Fatalf("transport = %+v", transport)
	}
	if transport.TLS.TrustBundleRef != "internal-ca" ||
		transport.TLS.ClientCertificateRef != "orders-client" ||
		transport.TLS.ServerName != "orders.internal" {
		t.Fatalf("TLS policy = %+v", transport.TLS)
	}
}

func TestDecodeV1Alpha5DefaultsProtocolToAuto(t *testing.T) {
	document := strings.Replace(validV5Document(t), "      protocol: http2\n", "", 1)
	_, resources, err := Decode(strings.NewReader(document))
	if err != nil {
		t.Fatal(err)
	}
	if resources.Upstreams[0].Transport.Protocol != model.TransportProtocolAuto {
		t.Fatalf("protocol = %q, want auto", resources.Upstreams[0].Transport.Protocol)
	}
}

func TestDecodeV1Alpha5RejectsUnknownAndDuplicateMaterial(t *testing.T) {
	valid := validV5Document(t)
	tests := []struct {
		name    string
		old     string
		new     string
		wantErr string
	}{
		{
			name:    "unknown material field",
			old:     "    ca_file:",
			new:     "    unknown: true\n    ca_file:",
			wantErr: "unknown",
		},
		{
			name:    "cross-kind duplicate ID",
			old:     "  - id: orders-client",
			new:     "  - id: internal-ca",
			wantErr: "duplicate TLS material id",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := replaceOnce(t, valid, test.old, test.new)
			if _, _, err := Decode(strings.NewReader(document)); err == nil ||
				!strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("Decode() error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

func TestValidateMaterialDocumentsV5RejectsResourceLimit(t *testing.T) {
	certificates := make([]certificateDocumentV5, tlsmaterial.MaxMaterialResources+1)
	if err := validateMaterialDocumentsV5(certificates, nil); err == nil ||
		!strings.Contains(err.Error(), "maximum") {
		t.Fatalf("validateMaterialDocumentsV5() error = %v", err)
	}
}

func TestDecodeV1Alpha5RejectsMissingAndOversizedMaterialFiles(t *testing.T) {
	valid := validV5Document(t)
	missing := filepath.Join(t.TempDir(), "missing-ca.pem")
	oversized := filepath.Join(t.TempDir(), "oversized-ca.pem")
	if err := os.WriteFile(
		oversized,
		bytes.Repeat([]byte{'x'}, int(tlsmaterial.MaxCAFileBytes)+1),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		path    string
		wantErr string
	}{
		{name: "missing", path: missing, wantErr: "open"},
		{name: "oversized", path: oversized, wantErr: "exceeds"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := replaceYAMLScalar(t, valid, "    ca_file: ", filepath.ToSlash(test.path))
			bootstrap, resources, err := Decode(strings.NewReader(document))
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("Decode() error = %v, want %q", err, test.wantErr)
			}
			if bootstrap != (BootstrapConfig{}) ||
				len(resources.Routes) != 0 ||
				len(resources.Certificates) != 0 ||
				len(resources.TrustBundles) != 0 {
				t.Fatalf("Decode() returned partial bootstrap=%+v resources=%+v", bootstrap, resources)
			}
		})
	}
}

func TestDecodeLegacyVersionsNormalizeToHTTP1WithoutTLS(t *testing.T) {
	certificateFile, privateKeyFile := writeTLSFiles(t)
	documents := []string{
		validDocument(certificateFile, privateKeyFile),
		validV2Document(certificateFile, privateKeyFile),
		validV3Document(t),
		strings.Replace(validV3Document(t), "gateway/v1alpha3", "gateway/v1alpha4", 1),
	}
	for _, document := range documents {
		_, resources, err := Decode(strings.NewReader(document))
		if err != nil {
			t.Fatal(err)
		}
		for _, upstream := range resources.Upstreams {
			if upstream.Transport.Protocol != model.TransportProtocolHTTP1 ||
				upstream.Transport.TLS != nil {
				t.Fatalf("legacy transport = %+v", upstream.Transport)
			}
		}
	}
}

func TestDecodeValidV1Alpha3(t *testing.T) {
	bootstrap, resources, err := Decode(strings.NewReader(validV3Document(t)))
	if err != nil {
		t.Fatal(err)
	}
	if bootstrap.Runtime.MaxRetiredSnapshots != 64 {
		t.Fatalf("max retired snapshots = %d", bootstrap.Runtime.MaxRetiredSnapshots)
	}
	upstream := resources.Upstreams[0]
	if upstream.Balancer.Type != model.BalancerConsistentHash {
		t.Fatalf("balancer = %q", upstream.Balancer.Type)
	}
	if len(upstream.Endpoints) != 2 ||
		upstream.Endpoints[0].Weight == 0 ||
		upstream.Endpoints[1].Weight != 1 {
		t.Fatalf("endpoints = %+v", upstream.Endpoints)
	}
	if len(upstream.Balancer.HashKey.Sources) != 2 ||
		upstream.Balancer.HashKey.Sources[0].Name != "X-Tenant" {
		t.Fatalf("hash key = %+v", upstream.Balancer.HashKey)
	}
}

func TestDecodeValidV1Alpha4ResilienceDefaultsAndOverrides(t *testing.T) {
	document := strings.Replace(validV3Document(t), "gateway/v1alpha3", "gateway/v1alpha4", 1)
	document = strings.Replace(document,
		"  max_retired_snapshots: 64",
		"  max_retired_snapshots: 64\n  health:\n    workers: 8\n    ready_queue_capacity: 512",
		1,
	)
	document = strings.Replace(document,
		"    upstream_ref: baseline",
		"    upstream_ref: baseline\n    resilience:\n      max_attempts: 3",
		1,
	)
	document = strings.Replace(document,
		"    transport:\n",
		"    health:\n      active: {}\n      passive: {}\n    retry: {}\n    transport:\n",
		1,
	)

	bootstrap, resources, err := Decode(strings.NewReader(document))
	if err != nil {
		t.Fatal(err)
	}
	if bootstrap.Runtime.Health.Workers != 8 ||
		bootstrap.Runtime.Health.ReadyQueueCapacity != 512 {
		t.Fatalf("health runtime = %+v", bootstrap.Runtime.Health)
	}
	if resources.Upstreams[0].Retry.TotalTimeout != 30*time.Second {
		t.Fatalf("total timeout = %s", resources.Upstreams[0].Retry.TotalTimeout)
	}
	if resources.Upstreams[0].Health.Active == nil ||
		resources.Upstreams[0].Health.Active.Type != model.HealthCheckHTTP {
		t.Fatalf("active health = %+v", resources.Upstreams[0].Health.Active)
	}
	if resources.Routes[0].Resilience.MaxAttempts == nil ||
		*resources.Routes[0].Resilience.MaxAttempts != 3 {
		t.Fatalf("route resilience = %+v", resources.Routes[0].Resilience)
	}
}

func TestDecodePhase3BExample(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "configs", "phase3b.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	certFile, keyFile := writeTLSFiles(t)
	document := strings.ReplaceAll(string(data), "/certs/server.crt", certFile)
	document = strings.ReplaceAll(document, "/certs/server.key", keyFile)
	bootstrap, resources, err := Decode(strings.NewReader(document))
	if err != nil {
		t.Fatal(err)
	}
	if bootstrap.Runtime.Health.Workers != 16 || len(resources.Upstreams) != 2 {
		t.Fatalf("example = runtime:%+v upstreams:%d", bootstrap.Runtime, len(resources.Upstreams))
	}
}

func TestDecodeV1Alpha4RejectsUnknownResilienceField(t *testing.T) {
	document := strings.Replace(validV3Document(t), "gateway/v1alpha3", "gateway/v1alpha4", 1)
	document = strings.Replace(document,
		"    upstream_ref: baseline",
		"    upstream_ref: baseline\n    resilience:\n      unknown: true",
		1,
	)
	if _, _, err := Decode(strings.NewReader(document)); err == nil ||
		!strings.Contains(err.Error(), "unknown") {
		t.Fatalf("Decode() error = %v, want unknown field", err)
	}
}

func TestDecodeLegacyResilienceDefaults(t *testing.T) {
	bootstrap, resources, err := Decode(strings.NewReader(validV3Document(t)))
	if err != nil {
		t.Fatal(err)
	}
	if bootstrap.Runtime.Health.Workers != DefaultHealthWorkers ||
		bootstrap.Runtime.Health.ReadyQueueCapacity != DefaultHealthQueueCapacity {
		t.Fatalf("legacy health runtime = %+v", bootstrap.Runtime.Health)
	}
	if resources.Upstreams[0].Health.Active != nil ||
		resources.Upstreams[0].Retry.MaxAttempts != 1 ||
		resources.Upstreams[0].Retry.TotalTimeout != 0 {
		t.Fatalf("legacy resilience changed: %+v", resources.Upstreams[0])
	}
}

func TestDecodeV1Alpha3PreservesExplicitZeroWeight(t *testing.T) {
	document := replaceOnce(t, validV3Document(t), "weight: 5", "weight: 0")
	_, resources, err := Decode(strings.NewReader(document))
	if err != nil {
		t.Fatal(err)
	}
	if resources.Upstreams[0].Endpoints[0].Weight != 0 {
		t.Fatalf("weight = %d, want 0", resources.Upstreams[0].Endpoints[0].Weight)
	}
}

func TestDecodeV1Alpha3RejectsInvalidFieldsAndRuntimeLimit(t *testing.T) {
	valid := validV3Document(t)
	tests := []struct {
		name    string
		old     string
		new     string
		wantErr string
	}{
		{name: "unknown field", old: "runtime:\n", new: "unknown: true\nruntime:\n", wantErr: "unknown"},
		{name: "missing hash key", old: "      hash_key:\n        sources:\n          - type: header\n            name: X-Tenant\n          - type: cookie\n            name: session_id\n", new: "", wantErr: "HASH_KEY_INVALID"},
		{name: "zero retired snapshots", old: "max_retired_snapshots: 64", new: "max_retired_snapshots: 0", wantErr: "runtime.max_retired_snapshots"},
		{name: "too many retired snapshots", old: "max_retired_snapshots: 64", new: "max_retired_snapshots: 1025", wantErr: "runtime.max_retired_snapshots"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := replaceOnce(t, valid, test.old, test.new)
			_, _, err := Decode(strings.NewReader(document))
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("Decode() error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

func TestDecodeLegacyVersionsDefaultRetiredSnapshots(t *testing.T) {
	certFile, keyFile := writeTLSFiles(t)
	for _, document := range []string{
		validDocument(certFile, keyFile),
		validV2Document(certFile, keyFile),
	} {
		bootstrap, _, err := Decode(strings.NewReader(document))
		if err != nil {
			t.Fatal(err)
		}
		if bootstrap.Runtime.MaxRetiredSnapshots != 64 {
			t.Fatalf("max retired snapshots = %d", bootstrap.Runtime.MaxRetiredSnapshots)
		}
	}
}

func TestDecodeValidV1Alpha2(t *testing.T) {
	certFile, keyFile := writeTLSFiles(t)

	bootstrap, resources, err := Decode(strings.NewReader(validV2Document(certFile, keyFile)))
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}

	if bootstrap.HTTP.Address != ":8080" {
		t.Fatalf("HTTP address = %q", bootstrap.HTTP.Address)
	}
	if len(resources.Routes) != 2 || len(resources.Services) != 1 || len(resources.Upstreams) != 1 {
		t.Fatalf("resource counts = routes:%d services:%d upstreams:%d", len(resources.Routes), len(resources.Services), len(resources.Upstreams))
	}
	route := resources.Routes[0]
	if route.ID != "users" || route.Priority != 100 || route.ServiceRef != "users-service" || route.UpstreamRef != "" {
		t.Fatalf("route = %+v", route)
	}
	if len(route.Match.Hosts) != 2 || route.Match.Hosts[1] != "*.example.net" {
		t.Fatalf("hosts = %v", route.Match.Hosts)
	}
	if len(route.Match.Headers) != 1 || route.Match.Headers[0].Operator != "one_of" {
		t.Fatalf("header predicates = %+v", route.Match.Headers)
	}
	if len(route.Plugins) != 1 || !route.Plugins[0].Enabled {
		t.Fatalf("route plugins = %+v", route.Plugins)
	}
	var pluginConfig map[string]any
	if err := json.Unmarshal(route.Plugins[0].RawConfig, &pluginConfig); err != nil {
		t.Fatalf("plugin config is not JSON: %v", err)
	}
	if pluginConfig["header_name"] != "X-Request-ID" {
		t.Fatalf("plugin config = %#v", pluginConfig)
	}
	service := resources.Services[0]
	if service.ID != "users-service" || service.UpstreamRef != "baseline" || len(service.Plugins) != 1 {
		t.Fatalf("service = %+v", service)
	}
	if resources.Routes[1].UpstreamRef != "baseline" {
		t.Fatalf("direct route = %+v", resources.Routes[1])
	}
}

func TestDecodeV1Alpha2RejectsInvalidResources(t *testing.T) {
	certFile, keyFile := writeTLSFiles(t)
	valid := validV2Document(certFile, keyFile)

	tests := []struct {
		name    string
		old     string
		new     string
		wantErr string
	}{
		{
			name:    "unknown route field",
			old:     "    priority: 100",
			new:     "    priority: 100\n    unknown: true",
			wantErr: "unknown",
		},
		{
			name:    "duplicate route ID",
			old:     "  - id: health",
			new:     "  - id: users",
			wantErr: "duplicate route id",
		},
		{
			name:    "duplicate service ID",
			old:     "services:\n  - id: users-service",
			new:     "services:\n  - id: users-service\n    upstream_ref: baseline\n  - id: users-service",
			wantErr: "duplicate service id",
		},
		{
			name:    "duplicate upstream ID",
			old:     "upstreams:\n  - id: baseline",
			new:     "upstreams:\n  - id: baseline\n    endpoints: [http://upstream:8080]\n    transport:\n      dial_timeout: 3s\n      response_header_timeout: 10s\n      idle_connection_timeout: 90s\n      max_idle_connections: 1024\n      max_idle_connections_per_host: 1024\n  - id: baseline",
			wantErr: "duplicate upstream id",
		},
		{
			name:    "both route targets",
			old:     "    service_ref: users-service",
			new:     "    service_ref: users-service\n    upstream_ref: baseline",
			wantErr: "exactly one",
		},
		{
			name:    "missing route target",
			old:     "    upstream_ref: baseline",
			new:     "",
			wantErr: "exactly one",
		},
		{
			name:    "unresolved service",
			old:     "    service_ref: users-service",
			new:     "    service_ref: absent",
			wantErr: "service_ref",
		},
		{
			name:    "unresolved direct upstream",
			old:     "    upstream_ref: baseline",
			new:     "    upstream_ref: absent",
			wantErr: "upstream_ref",
		},
		{
			name:    "unresolved service upstream",
			old:     "    upstream_ref: baseline\n    plugins:",
			new:     "    upstream_ref: absent\n    plugins:",
			wantErr: "services[0].upstream_ref",
		},
		{
			name:    "invalid predicate operator",
			old:     "          operator: one_of",
			new:     "          operator: regex",
			wantErr: "operator",
		},
		{
			name:    "exists with values",
			old:     "          operator: one_of\n          values: [acme, globex]",
			new:     "          operator: exists\n          values: [acme]",
			wantErr: "values",
		},
		{
			name:    "equals without one value",
			old:     "          operator: equals\n          values: [\"true\"]",
			new:     "          operator: equals\n          values: []",
			wantErr: "values",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			document := replaceOnce(t, valid, tt.old, tt.new)
			_, _, err := Decode(strings.NewReader(document))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Decode() error = %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestDecodeV1Alpha1RejectsV1Alpha2Services(t *testing.T) {
	certFile, keyFile := writeTLSFiles(t)
	document := validDocument(certFile, keyFile) + "\nservices: []\n"

	_, _, err := Decode(strings.NewReader(document))
	if err == nil || !strings.Contains(err.Error(), "services") {
		t.Fatalf("Decode() error = %v, want strict unknown-field error", err)
	}
}

func TestDecodeValidPhase1Config(t *testing.T) {
	certFile, keyFile := writeTLSFiles(t)

	bootstrap, resources, err := Decode(strings.NewReader(validDocument(certFile, keyFile)))
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}

	if bootstrap.HTTP.Address != ":8080" {
		t.Fatalf("HTTP address = %q, want :8080", bootstrap.HTTP.Address)
	}
	if bootstrap.HTTPS.CertificateFile != certFile || bootstrap.HTTPS.PrivateKeyFile != keyFile {
		t.Fatalf("TLS files = %q, %q", bootstrap.HTTPS.CertificateFile, bootstrap.HTTPS.PrivateKeyFile)
	}
	if bootstrap.Server.ReadHeaderTimeout != 5*time.Second {
		t.Fatalf("read header timeout = %s, want 5s", bootstrap.Server.ReadHeaderTimeout)
	}
	if !bootstrap.Telemetry.RequestMetricsEnabled || bootstrap.Telemetry.ProfilingEnabled {
		t.Fatalf("telemetry = %+v", bootstrap.Telemetry)
	}
	if len(resources.Routes) != 1 || resources.Routes[0].ID != "baseline" {
		t.Fatalf("routes = %+v", resources.Routes)
	}
	if got := resources.Routes[0].Match.Methods; len(got) != 2 || got[0] != "GET" || got[1] != "POST" {
		t.Fatalf("methods = %v, want [GET POST]", got)
	}
	if len(resources.Upstreams) != 1 ||
		resources.Upstreams[0].Endpoints[0].URL != "http://upstream:8080" ||
		resources.Upstreams[0].Endpoints[0].Weight != 1 {
		t.Fatalf("upstreams = %+v", resources.Upstreams)
	}
	if resources.Upstreams[0].Transport.MaxIdleConnectionsPerHost != 1024 {
		t.Fatalf("transport = %+v", resources.Upstreams[0].Transport)
	}
}

func TestDecodeSeparatesBootstrapFromResources(t *testing.T) {
	certFile, keyFile := writeTLSFiles(t)

	bootstrap, resources, err := Decode(strings.NewReader(validDocument(certFile, keyFile)))
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}

	if bootstrap.HTTP.Address == "" || bootstrap.Admin.Address == "" {
		t.Fatalf("bootstrap listeners were not decoded: %+v", bootstrap)
	}
	if resources.Routes[0].Match.Path != "/hello" || resources.Routes[0].UpstreamRef != "baseline" {
		t.Fatalf("canonical route was not decoded: %+v", resources.Routes[0])
	}
}

func TestDecodeRejectsUnknownField(t *testing.T) {
	certFile, keyFile := writeTLSFiles(t)
	document := strings.Replace(validDocument(certFile, keyFile), "api_version: gateway/v1alpha1", "api_version: gateway/v1alpha1\nunknown: true", 1)

	_, _, err := Decode(strings.NewReader(document))
	if err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("Decode() error = %v, want unknown-field error", err)
	}
}

func TestDecodeRejectsMultipleDocuments(t *testing.T) {
	certFile, keyFile := writeTLSFiles(t)
	document := validDocument(certFile, keyFile) + "\n---\napi_version: gateway/v1alpha1\n"

	_, _, err := Decode(strings.NewReader(document))
	if err == nil || !strings.Contains(err.Error(), "multiple YAML documents") {
		t.Fatalf("Decode() error = %v, want multiple-document error", err)
	}
}

func TestLoadReadsConfigFile(t *testing.T) {
	certFile, keyFile := writeTLSFiles(t)
	configFile := filepath.Join(t.TempDir(), "gateway.yaml")
	if err := os.WriteFile(configFile, []byte(validDocument(certFile, keyFile)), 0o600); err != nil {
		t.Fatal(err)
	}

	bootstrap, resources, err := Load(configFile)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if bootstrap.Admin.Address != ":9090" || resources.Upstreams[0].ID != "baseline" {
		t.Fatalf("Load() returned bootstrap=%+v resources=%+v", bootstrap, resources)
	}
}

func TestDecodeRejectsInvalidConfig(t *testing.T) {
	certFile, keyFile := writeTLSFiles(t)
	valid := validDocument(certFile, keyFile)
	missingCert := filepath.ToSlash(filepath.Join(t.TempDir(), "missing.crt"))

	tests := []struct {
		name    string
		old     string
		new     string
		wantErr string
	}{
		{name: "wrong API version", old: "gateway/v1alpha1", new: "gateway/v2", wantErr: "api_version"},
		{name: "zero routes", old: validRouteBlock, new: "routes: []", wantErr: "exactly one route"},
		{name: "two routes", old: validRouteBlock, new: validRouteBlock + "\n  - id: second\n    match:\n      path: /second\n      methods: [GET]\n    upstream_ref: baseline", wantErr: "exactly one route"},
		{name: "zero upstreams", old: validUpstreamBlock, new: "upstreams: []", wantErr: "exactly one upstream"},
		{name: "two upstreams with duplicate ID", old: validUpstreamBlock, new: validUpstreamBlock + "\n  - id: baseline\n    endpoints: [http://other:8080]\n    transport:\n      dial_timeout: 3s\n      response_header_timeout: 10s\n      idle_connection_timeout: 90s\n      max_idle_connections: 1\n      max_idle_connections_per_host: 1", wantErr: "duplicate upstream id"},
		{name: "empty route ID", old: "- id: baseline\n    match:", new: "- id: \"\"\n    match:", wantErr: "routes[0].id"},
		{name: "empty upstream ID", old: "- id: baseline\n    endpoints:", new: "- id: \"\"\n    endpoints:", wantErr: "upstreams[0].id"},
		{name: "unresolved reference", old: "upstream_ref: baseline", new: "upstream_ref: absent", wantErr: "upstream_ref"},
		{name: "relative path", old: "path: /hello", new: "path: hello", wantErr: "routes[0].match.path"},
		{name: "empty methods", old: "methods: [GET, POST]", new: "methods: []", wantErr: "methods"},
		{name: "invalid method", old: "methods: [GET, POST]", new: "methods: [GET, \"BAD METHOD\"]", wantErr: "methods"},
		{name: "duplicate method after canonicalization", old: "methods: [GET, POST]", new: "methods: [GET, get]", wantErr: "duplicate method"},
		{name: "non HTTP endpoint", old: "http://upstream:8080", new: "https://upstream:8443", wantErr: "scheme http"},
		{name: "multiple endpoints", old: "- http://upstream:8080", new: "- http://upstream:8080\n      - http://second:8080", wantErr: "exactly one endpoint"},
		{name: "listener collision", old: "admin:\n    address: \":9090\"", new: "admin:\n    address: \":8080\"", wantErr: "listener address"},
		{name: "missing certificate", old: certFile, new: missingCert, wantErr: "certificate_file"},
		{name: "zero server timeout", old: "read_header_timeout: 5s", new: "read_header_timeout: 0s", wantErr: "read_header_timeout"},
		{name: "zero server limit", old: "max_header_bytes: 1048576", new: "max_header_bytes: 0", wantErr: "max_header_bytes"},
		{name: "zero transport timeout", old: "dial_timeout: 3s", new: "dial_timeout: 0s", wantErr: "dial_timeout"},
		{name: "zero transport limit", old: "max_idle_connections: 1024", new: "max_idle_connections: 0", wantErr: "max_idle_connections"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			document := replaceOnce(t, valid, tt.old, tt.new)
			_, _, err := Decode(strings.NewReader(document))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Decode() error = %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}

func writeTLSFiles(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	certFile := filepath.ToSlash(filepath.Join(dir, "server.crt"))
	keyFile := filepath.ToSlash(filepath.Join(dir, "server.key"))
	for _, path := range []string{certFile, keyFile} {
		if err := os.WriteFile(path, []byte("readable for config validation"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return certFile, keyFile
}

func replaceOnce(t *testing.T, document, old, new string) string {
	t.Helper()
	if !strings.Contains(document, old) {
		t.Fatalf("valid document does not contain %q", old)
	}
	return strings.Replace(document, old, new, 1)
}

func replaceYAMLScalar(t *testing.T, document, prefix, value string) string {
	t.Helper()
	start := strings.Index(document, prefix)
	if start < 0 {
		t.Fatalf("document does not contain %q", prefix)
	}
	valueStart := start + len(prefix)
	valueEnd := strings.IndexByte(document[valueStart:], '\n')
	if valueEnd < 0 {
		valueEnd = len(document)
	} else {
		valueEnd += valueStart
	}
	return document[:valueStart] + value + document[valueEnd:]
}

func validDocument(certFile, keyFile string) string {
	return fmt.Sprintf(`api_version: gateway/v1alpha1

listeners:
  http:
    address: ":8080"
  https:
    address: ":8443"
    certificate_file: %s
    private_key_file: %s
  admin:
    address: ":9090"

server:
  read_header_timeout: 5s
  idle_timeout: 60s
  shutdown_timeout: 30s
  max_header_bytes: 1048576
  max_request_body_bytes: 67108864

telemetry:
  request_metrics_enabled: true
  profiling_enabled: false

%s

%s
`, certFile, keyFile, validRouteBlock, validUpstreamBlock)
}

const validRouteBlock = `routes:
  - id: baseline
    match:
      path: /hello
      methods: [GET, POST]
    upstream_ref: baseline`

const validUpstreamBlock = `upstreams:
  - id: baseline
    endpoints:
      - http://upstream:8080
    transport:
      dial_timeout: 3s
      response_header_timeout: 10s
      idle_connection_timeout: 90s
      max_idle_connections: 1024
      max_idle_connections_per_host: 1024`

func validV2Document(certFile, keyFile string) string {
	return fmt.Sprintf(`api_version: gateway/v1alpha2

listeners:
  http:
    address: ":8080"
  https:
    address: ":8443"
    certificate_file: %s
    private_key_file: %s
  admin:
    address: ":9090"

server:
  read_header_timeout: 5s
  idle_timeout: 60s
  shutdown_timeout: 30s
  max_header_bytes: 1048576
  max_request_body_bytes: 67108864

telemetry:
  request_metrics_enabled: true
  profiling_enabled: false

routes:
  - id: users
    priority: 100
    match:
      hosts: [api.example.com, "*.example.net"]
      path: /users/{id}
      methods: [GET]
      headers:
        - name: X-Tenant
          operator: one_of
          values: [acme, globex]
      queries:
        - name: verbose
          operator: equals
          values: ["true"]
    service_ref: users-service
    plugins:
      - name: request-id
        config:
          header_name: X-Request-ID
  - id: health
    match:
      path: /health
      methods: [GET]
    upstream_ref: baseline

services:
  - id: users-service
    upstream_ref: baseline
    plugins:
      - name: header-rewrite
        enabled: true
        config:
          request:
            set:
              X-Service: users

upstreams:
  - id: baseline
    endpoints: [http://upstream:8080]
    transport:
      dial_timeout: 3s
      response_header_timeout: 10s
      idle_connection_timeout: 90s
      max_idle_connections: 1024
      max_idle_connections_per_host: 1024
`, certFile, keyFile)
}

func validV3Document(t *testing.T) string {
	t.Helper()
	certFile, keyFile := writeTLSFiles(t)
	return fmt.Sprintf(`api_version: gateway/v1alpha3

runtime:
  max_retired_snapshots: 64

listeners:
  http:
    address: ":8080"
  https:
    address: ":8443"
    certificate_file: %s
    private_key_file: %s
  admin:
    address: ":9090"

server:
  read_header_timeout: 5s
  idle_timeout: 60s
  shutdown_timeout: 30s
  max_header_bytes: 1048576
  max_request_body_bytes: 67108864

telemetry:
  request_metrics_enabled: true
  profiling_enabled: false

routes:
  - id: users
    priority: 100
    match:
      path: /users/{id}
      methods: [GET]
    upstream_ref: baseline

services: []

upstreams:
  - id: baseline
    endpoints:
      - url: http://upstream-a:8080
        weight: 5
      - url: http://upstream-b:8080
    balancer:
      type: consistent_hash
      hash_key:
        sources:
          - type: header
            name: X-Tenant
          - type: cookie
            name: session_id
    transport:
      dial_timeout: 3s
      response_header_timeout: 10s
      idle_connection_timeout: 90s
      max_idle_connections: 1024
      max_idle_connections_per_host: 1024
`, certFile, keyFile)
}

func validV5Document(t *testing.T) string {
	t.Helper()
	certificateFile, privateKeyFile, caFile := writeV5MaterialFiles(t)
	document := strings.Replace(validV3Document(t), "gateway/v1alpha3", "gateway/v1alpha5", 1)
	document = strings.Replace(document, "http://upstream-a:8080", "https://127.0.0.1:8443", 1)
	document = strings.Replace(document, "http://upstream-b:8080", "https://127.0.0.2:8443", 1)
	document = strings.Replace(document,
		"routes:\n",
		fmt.Sprintf(`trust_bundles:
  - id: internal-ca
    ca_file: %s

certificates:
  - id: orders-client
    certificate_file: %s
    private_key_file: %s

routes:
`, filepath.ToSlash(caFile), filepath.ToSlash(certificateFile), filepath.ToSlash(privateKeyFile)),
		1,
	)
	document = strings.Replace(document,
		"    transport:\n",
		`    transport:
      protocol: http2
      tls:
        trust_bundle_ref: internal-ca
        client_certificate_ref: orders-client
        server_name: orders.internal
`,
		1,
	)
	return document
}

func writeV5MaterialFiles(t *testing.T) (string, string, string) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(31),
		Subject:               pkix.Name{CommonName: "Phase 3C1 Test Root"},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              []string{"orders.internal"},
	}
	certificateDER, err := x509.CreateCertificate(rand.Reader, template, template, publicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	privateKeyDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	certificatePEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificateDER})
	privateKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateKeyDER})
	certificateFile := filepath.Join(directory, "client.crt")
	privateKeyFile := filepath.Join(directory, "client.key")
	caFile := filepath.Join(directory, "ca.pem")
	for path, data := range map[string][]byte{
		certificateFile: certificatePEM,
		privateKeyFile:  privateKeyPEM,
		caFile:          certificatePEM,
	} {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return certificateFile, privateKeyFile, caFile
}

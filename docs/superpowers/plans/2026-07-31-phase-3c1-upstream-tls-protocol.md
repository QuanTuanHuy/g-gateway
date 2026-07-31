# Phase 3C1 Upstream TLS and Protocol Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add verified upstream TLS/mTLS, deterministic HTTP/1.1/HTTP/2/h2c selection, native gRPC pass-through, profile-aware active health, atomic material rotation, and bounded TLS telemetry while preserving Phase 3B retry and lifecycle semantics.

**Architecture:** Strict `gateway/v1alpha5` loading creates immutable certificate and trust-bundle resources, and the upstream registry compiles each upstream into one complete transport profile. One native Go 1.26 `http.Transport` owns the production pool and one separate probe pool per profile; both use the same verified TLS and protocol policy, while snapshot leases retain old generations through streaming requests.

**Tech Stack:** Go 1.26.5, `net/http`, `crypto/tls`, `crypto/x509`, `httputil.ReverseProxy`, Prometheus client_golang, strict YAML v3, gRPC-Go v1.82.1 for interoperability tests, Go unit/integration/fuzz/race tests, `httptest`, and Go benchmarks.

## Global Constraints

- Execute on `codex/phase3c1-upstream-tls-protocol-design`; do not create or use a Git worktree.
- Preserve strict `gateway/v1alpha1` through `gateway/v1alpha4`; they continue to normalize to cleartext HTTP/1.1 upstream behavior.
- Add strict `gateway/v1alpha5`; reject unknown fields and trailing YAML documents.
- Supported endpoint schemes are exactly `http` and `https`; all positive-weight endpoints in one upstream use one scheme.
- Supported protocol modes are exactly `auto`, `http1`, and `http2`.
- `auto+http` is HTTP/1.1; `http1+http` is HTTP/1.1-only; `http2+http` is h2c prior knowledge.
- `auto+https` prefers HTTP/2 and falls back to HTTP/1.1; `http1+https` is HTTP/1.1-only; `http2+https` requires ALPN `h2`.
- TLS verification is always enabled; no insecure bypass field or code path is permitted.
- An absent trust reference uses system roots; a present trust bundle replaces system roots.
- An absent server-name override uses the selected endpoint hostname for SNI and verification.
- TLS identity stays independent from the forwarded inbound HTTP `Host` or HTTP/2 `:authority`.
- TLS minimum version is fixed to TLS 1.2; keep Go default cipher suites and curves.
- Each production and probe transport generation owns a private `tls.NewLRUClientSessionCache(64)`.
- CA bundle files are bounded to 1 MiB; certificate-chain and key files are each bounded to 256 KiB.
- Accept at most 10,000 certificate and trust-bundle resources combined and 64 MiB of source material per candidate.
- Read material only during `config.Load` or `config.Decode`; do not add a filesystem watcher or inline PEM fields.
- Do not retain raw PEM after parsed immutable material has been constructed.
- Never parse YAML/PEM/X.509, resolve references, choose protocol, build `tls.Config`, or read files on the request hot path.
- HTTP active health uses the same TLS/protocol profile through a separate pool; TCP health remains raw TCP.
- Preserve Phase 3B replay safety, retry budget, distinct-endpoint retry, and total-timeout behavior.
- TLS and strict-protocol failures use connection-failure retry/health semantics; never retry based on `grpc-status`.
- Preserve unary and streaming gRPC headers, messages, trailers, status, and cancellation without protobuf inspection.
- Long streams require `total_timeout: 0`; do not infer timeout policy from content type.
- Keep downstream cleartext HTTP/1.1 and TLS HTTP/1.1/HTTP/2; downstream h2c is outside Phase 3C1.
- Public errors and metrics never expose endpoint hostnames, SNI, material IDs, fingerprints, paths, subjects, SANs, PEM, keys, or remote alert text.
- Keep request-path metrics free of dynamic label lookup; all TLS labels come from closed enums.
- Full-envelope acceptance remains 10,000 upstreams, 100,000 total endpoints, 1,000 endpoints per upstream, and at most 512 MiB incremental active heap.
- Cleartext HTTP/1.1 throughput must remain at least 95% of Phase 3B and p99 no more than 110%; the disabled-TLS path gains no allocations.
- Use TDD for every behavior change and commit after each task's focused suite passes.
- Use `apply_patch` for authored edits; formatting tools may perform mechanical rewrites.
- Record race as pending when the host lacks CGO and a C compiler; never report an unexecuted gate as passed.
- Integrated APISIX comparison remains Phase 3D work.

---

## File and Responsibility Map

Create:

- `internal/tlsmaterial/material.go` — immutable parsed certificate and trust-bundle values, fingerprints, and TLS runtime copies.
- `internal/tlsmaterial/load.go` — bounded file reads and PEM/X.509/key validation.
- `internal/tlsmaterial/material_test.go` — parsing, canonicalization, key-match, size, and immutability tests.
- `internal/tlsmaterial/fuzz_test.go` — certificate and trust-document boundary fuzzing.
- `internal/config/wire_v1alpha5.go` — strict v1alpha5 wire schema, file-backed material conversion, and TLS/protocol fields.
- `internal/upstream/profile.go` — reference resolution and immutable complete transport-profile compilation.
- `internal/upstream/profile_test.go` — scheme/protocol/TLS matrix and reference resolution tests.
- `internal/upstream/tls_error.go` — typed TLS/protocol failure categories and safe classification.
- `internal/upstream/tls_error_test.go` — typed classification and redaction tests.
- `internal/upstream/protocol_integration_test.go` — HTTP/1.1, HTTP/2 TLS, fallback, strict ALPN, h2c, trailers, and streaming tests.
- `internal/upstream/tls_integration_test.go` — trust, hostname, SNI, mTLS, TLS version, and session-isolation tests.
- `internal/proxy/grpc_integration_test.go` — real gRPC unary/streaming/metadata/trailer/status/cancellation coverage.
- `internal/upstream/phase3c1_acceptance_test.go` — full-envelope material/profile compile and rotation acceptance.
- `internal/proxy/phase3c1_acceptance_test.go` — Phase 3B versus Phase 3C1 cleartext healthy-path comparison.
- `internal/upstream/phase3c1_benchmark_test.go` — handshake, resumption, H1/H2/h2c, profile, and rotation benchmarks.
- `configs/phase3c1.yaml` — runnable v1alpha5 configuration contract with mounted secret paths.
- `docs/operations/phase-3c1-runbook.md` — security, protocol, health, rotation, gRPC, metrics, and failure operations.
- `docs/benchmarks/phase-3c1-current-status.md` — observed local evidence ledger and pending canonical gates.

Modify:

- `go.mod` and `go.sum` — pin gRPC-Go v1.82.1 for interoperability tests.
- `internal/model/resources.go` — material collections, protocol enum, TLS reference policy, and deep clone.
- `internal/model/resources_test.go` — resource clone isolation and immutable material-handle sharing.
- `internal/config/load.go` — v1alpha5 dispatch and transactional material conversion.
- `internal/config/load_test.go` — strict/default/compatibility/material-limit matrix.
- `internal/config/validate.go` — v1alpha5 resource count, reference, and runtime validation entry.
- `internal/upstream/config.go` — HTTPS endpoint normalization, uniform scheme checks, protocol/TLS validation.
- `internal/upstream/config_test.go` — endpoint and policy normalization table tests.
- `internal/upstream/transport.go` — native protocol flags, verified TLS dial, production/probe pools, and idempotent cleanup.
- `internal/upstream/transport_test.go` — complete key, protocol flags, TLS config, and cleanup tests.
- `internal/upstream/probe.go` — target-specific HTTP RoundTripper and profile metadata.
- `internal/upstream/probe_http.go` — stateless HTTP probe over the target's probe transport.
- `internal/upstream/probe_test.go` — pool isolation, HTTPS/h2c, failure classification, and cancellation.
- `internal/upstream/health_scheduler.go` — remove ownership of one global HTTP transport while retaining the bounded scheduler.
- `internal/upstream/health_scheduler_test.go` — target transport dispatch and shutdown tests.
- `internal/upstream/fingerprint.go` — include complete transport profile in health identity.
- `internal/upstream/fingerprint_test.go` — profile-sensitive health reuse tests.
- `internal/upstream/registry.go` — prepare complete resource sets, profile reuse/generation ownership, probe association, and transactional rollback.
- `internal/upstream/registry_test.go` — reuse, rotation, rollback, unrelated-upstream isolation, and retirement tests.
- `internal/upstream/reaper.go` and `internal/upstream/reaper_test.go` — old HTTP/2/gRPC generation retirement evidence.
- `internal/upstream/observer.go` — bounded TLS handshake/failure and transport-generation events.
- `internal/upstream/plan.go` — preserve selected scheme and typed transport failures.
- `internal/runtime/manager.go` and `internal/runtime/manager_test.go` — pass the complete resource set into registry preparation.
- `internal/proxy/retry.go` and `internal/proxy/retry_test.go` — classify TLS as connection failure without changing gRPC trailer behavior.
- `internal/proxy/route_transport.go` and `internal/proxy/attempt_transport_test.go` — retain typed final TLS errors across attempts.
- `internal/proxy/handler.go` and `internal/proxy/runtime_handler_test.go` — stable `UPSTREAM_TLS_FAILED` mapping and redacted logging.
- `internal/telemetry/telemetry.go` and `internal/telemetry/telemetry_test.go` — exact bounded Phase 3C1 metric families.
- `internal/gateway/lifecycle_observer.go` and `internal/gateway/lifecycle_observer_test.go` — TLS event forwarding and redacted lifecycle logs.
- `internal/gateway/gateway.go` and `internal/gateway/gateway_test.go` — registry observer wiring and drain/cleanup evidence.
- `test/integration/tls_test.go` — black-box v1alpha5 trust/mTLS/SNI/protocol cases.
- `test/integration/upstream_reconcile_test.go` — live stream and pool behavior across revisions.
- `README.md` — Phase 3C1 navigation and current capability statement.
- `docs/superpowers/specs/2026-07-21-go-native-api-gateway-phase-roadmap-design.md` — Phase 3C1 checkpoint and 3C2 handoff.
- `docs/superpowers/specs/2026-07-30-phase-3c1-upstream-tls-protocol-design.md` — only reconcile names that differ from the delivered contract.

## Locked Cross-Task Interfaces

The immutable material package introduced in Task 1 exposes:

```go
// internal/tlsmaterial/material.go
type Fingerprint [32]byte

type Certificate struct {
	id          string
	certificate tls.Certificate
	fingerprint Fingerprint
}

func NewCertificate(id string, certificatePEM, privateKeyPEM []byte) (*Certificate, error)
func (c *Certificate) ID() string
func (c *Certificate) Fingerprint() Fingerprint
func (c *Certificate) TLSCertificate() tls.Certificate

type TrustBundle struct {
	id           string
	certificates []*x509.Certificate
	fingerprint  Fingerprint
}

func NewTrustBundle(id string, caPEM []byte) (*TrustBundle, error)
func (b *TrustBundle) ID() string
func (b *TrustBundle) Fingerprint() Fingerprint
func (b *TrustBundle) CertPool() *x509.CertPool

const (
	MaxCAFileBytes          int64 = 1 << 20
	MaxCertificateFileBytes int64 = 256 << 10
	MaxPrivateKeyFileBytes  int64 = 256 << 10
	MaxMaterialResources          = 10_000
	MaxCandidateSourceBytes int64 = 64 << 20
)

func LoadCertificate(id, certificatePath, privateKeyPath string) (*Certificate, int64, error)
func LoadTrustBundle(id, path string) (*TrustBundle, int64, error)
```

`TLSCertificate` copies the certificate-chain slice and `CertPool` constructs a fresh pool. Raw PEM is accepted only by constructors and is not stored.

The canonical model introduced in Tasks 1–2 is:

```go
// internal/model/resources.go
type TransportProtocol string

const (
	TransportProtocolAuto  TransportProtocol = "auto"
	TransportProtocolHTTP1 TransportProtocol = "http1"
	TransportProtocolHTTP2 TransportProtocol = "http2"
)

type UpstreamTLSPolicy struct {
	TrustBundleRef      string
	ClientCertificateRef string
	ServerName          string
}

type TransportConfig struct {
	Protocol                  TransportProtocol
	TLS                       *UpstreamTLSPolicy
	DialTimeout               time.Duration
	ResponseHeaderTimeout     time.Duration
	IdleConnectionTimeout     time.Duration
	MaxIdleConnections        int
	MaxIdleConnectionsPerHost int
}

type ResourceSet struct {
	Routes       []Route
	Services     []Service
	Upstreams    []Upstream
	Certificates []*tlsmaterial.Certificate
	TrustBundles []*tlsmaterial.TrustBundle
}
```

`CloneResourceSet` clones all mutable slices and TLS policy pointers. It copies the material slices but shares their immutable pointed-to values.

The complete upstream profile introduced in Task 4 is:

```go
// internal/upstream/profile.go
type materialIndex struct {
	certificates map[string]*tlsmaterial.Certificate
	trustBundles map[string]*tlsmaterial.TrustBundle
}

type transportProfile struct {
	scheme            string
	protocol          model.TransportProtocol
	transport         model.TransportConfig
	trustBundle       *tlsmaterial.TrustBundle
	clientCertificate *tlsmaterial.Certificate
	serverName        string
}

func newMaterialIndex(resources model.ResourceSet) (materialIndex, error)
func compileTransportProfile(resource model.Upstream, materials materialIndex) (transportProfile, error)
```

The transport and failure contracts introduced in Tasks 5–6 are:

```go
// internal/upstream/tls_error.go
type TLSFailureClass string

const (
	TLSFailureTrust          TLSFailureClass = "trust"
	TLSFailureHostname       TLSFailureClass = "hostname"
	TLSFailureClientIdentity TLSFailureClass = "client_identity"
	TLSFailureProtocol       TLSFailureClass = "protocol"
	TLSFailureHandshake      TLSFailureClass = "handshake"
)

type TLSFailureError struct {
	Class TLSFailureClass
	Err   error
}

func (e *TLSFailureError) Error() string
func (e *TLSFailureError) Unwrap() error
func IsTLSFailure(err error) bool
func TLSFailureClassOf(err error) (TLSFailureClass, bool)

// internal/upstream/transport.go
type TLSObserver interface {
	ObserveTLSHandshake(result, mode string, protocol model.TransportProtocol)
	ObserveTLSFailure(class TLSFailureClass)
}

type transportRuntime struct {
	key        transportKey
	production *http.Transport
	probe      *http.Transport
	closeOnce  sync.Once
}

func newTransportRuntime(profile transportProfile, observer TLSObserver) *transportRuntime
func (r *transportRuntime) RoundTrip(*http.Request) (*http.Response, error)
func (r *transportRuntime) ProbeTransport() http.RoundTripper
func (r *transportRuntime) CloseIdleConnections()
```

The active-health target contract introduced in Task 7 is:

```go
type ProbeTarget struct {
	EndpointID string
	URL        *url.URL
	Generation uint64
	Policy     model.ActiveHealthPolicy
	Transport  http.RoundTripper
}
```

The registry prepares the complete resource set:

```go
func (r *Registry) Prepare(resources model.ResourceSet) (*Candidate, error)
```

All existing callers wrap upstream-only fixtures with `model.ResourceSet{Upstreams: resources}`.

The bounded observer additions introduced in Task 11 are:

```go
type TransportGenerationDelta struct {
	Action   string
	TLS      bool
	Protocol model.TransportProtocol
	Count    int
}

type PrepareStats struct {
	// existing fields
	TransportGenerations []TransportGenerationDelta
}

type CleanupStats struct {
	// existing fields
	TransportGenerations []TransportGenerationDelta
}

type Observer interface {
	// existing methods
	TLSHandshake(result, mode string, protocol model.TransportProtocol)
	TLSFailure(class TLSFailureClass)
}
```

`PrepareStats.TransportGenerations` contains only `create` and `reuse`; `CleanupStats.TransportGenerations` contains only `retire`. Each slice is compacted to at most 18 closed combinations before observer delivery.

---

### Task 1: Create immutable TLS material parsing and fingerprints

**Files:**
- Create: `internal/tlsmaterial/material.go`
- Create: `internal/tlsmaterial/load.go`
- Create: `internal/tlsmaterial/material_test.go`

**Interfaces:**
- Consumes: standard-library `crypto/tls`, `crypto/x509`, `encoding/pem`, and bounded regular files.
- Produces: the locked `Certificate`, `TrustBundle`, fingerprints, size constants, and loaders.

- [ ] **Step 1: Write RED parsing and immutability tests**

Create tests with an Ed25519 root CA and issued leaf certificates. Cover:

```go
func TestNewCertificateRejectsMismatch(t *testing.T) {
	firstCert, firstKey := issuePair(t, "client.internal", x509.ExtKeyUsageClientAuth)
	_, secondKey := issuePair(t, "other.internal", x509.ExtKeyUsageClientAuth)
	if _, err := NewCertificate("client", firstCert, secondKey); err == nil {
		t.Fatal("NewCertificate() accepted mismatched key")
	}
	_ = firstKey
}

func TestNewTrustBundleCanonicalizesOrderAndDuplicates(t *testing.T) {
	caA := newRootPEM(t, "A")
	caB := newRootPEM(t, "B")
	first, err := NewTrustBundle("roots", append(append([]byte{}, caA...), caB...))
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewTrustBundle("roots", append(append(append([]byte{}, caB...), caA...), caA...))
	if err != nil {
		t.Fatal(err)
	}
	if first.Fingerprint() != second.Fingerprint() {
		t.Fatal("semantic-equivalent bundles produced different fingerprints")
	}
}
```

Also assert rejection of empty IDs, empty CA sets, non-certificate PEM blocks, malformed DER, malformed or unsupported keys, and certificate files without private keys. Mutate the result of `TLSCertificate()` and prove a second call is unchanged.

- [ ] **Step 2: Run the material suite to verify RED**

Run:

```powershell
go test ./internal/tlsmaterial -count=1
```

Expected: package or declarations do not exist.

- [ ] **Step 3: Add domain-separated immutable constructors**

Use `tls.X509KeyPair`, parse every returned chain element with `x509.ParseCertificate`, and set `certificate.Leaf`. Calculate fingerprints as:

```go
func fingerprint(domain string, documents [][]byte) Fingerprint {
	digest := sha256.New()
	writeLengthPrefixed(digest, []byte(domain))
	for _, document := range documents {
		writeLengthPrefixed(digest, document)
	}
	var out Fingerprint
	copy(out[:], digest.Sum(nil))
	return out
}
```

For a certificate, hash the ordered certificate-chain DER with domain `gateway/certificate/v1`. For a trust bundle, reject non-certificate PEM blocks, sort unique DER byte slices, and hash them with domain `gateway/trust-bundle/v1`.

`TLSCertificate()` copies `Certificate`, `OCSPStaple`, and `SignedCertificateTimestamps`, while retaining the parsed private-key interface. `CertPool()` creates a new pool and calls `AddCert` for every immutable parsed CA.

- [ ] **Step 4: Add bounded regular-file loading**

Implement a helper that opens one regular file, rejects `size > limit`, reads through `io.LimitReader(file, limit+1)`, and returns the exact bytes read. Wrap errors with the resource field but do not include file content.

`LoadCertificate` reads certificate then key and returns their combined byte count. `LoadTrustBundle` returns its byte count. Do not retain the source byte slices after constructor return.

- [ ] **Step 5: Run and commit the material package**

Run:

```powershell
go test ./internal/tlsmaterial -count=1
go vet ./internal/tlsmaterial
```

Expected: PASS.

Commit:

```powershell
git add internal/tlsmaterial
git commit -m "feat: add immutable tls material resources"
```

### Task 2: Add canonical material and transport policy types

**Files:**
- Modify: `internal/model/resources.go`
- Modify: `internal/model/resources_test.go`

**Interfaces:**
- Consumes: Task 1 immutable material handles.
- Produces: the locked `TransportProtocol`, `UpstreamTLSPolicy`, extended `TransportConfig`, and `ResourceSet`.

- [ ] **Step 1: Write the failing clone-isolation test**

Add:

```go
func TestCloneResourceSetClonesTLSReferencesAndSharesImmutableMaterial(t *testing.T) {
	certificate := testCertificate(t, "client")
	bundle := testTrustBundle(t, "roots")
	in := ResourceSet{
		Certificates: []*tlsmaterial.Certificate{certificate},
		TrustBundles: []*tlsmaterial.TrustBundle{bundle},
		Upstreams: []Upstream{{
			ID: "orders",
			Transport: TransportConfig{
				Protocol: TransportProtocolHTTP2,
				TLS: &UpstreamTLSPolicy{
					TrustBundleRef: "roots",
					ClientCertificateRef: "client",
					ServerName: "orders.internal",
				},
			},
		}},
	}

	got := CloneResourceSet(in)
	in.Upstreams[0].Transport.TLS.ServerName = "changed.internal"
	in.Certificates[0] = nil
	if got.Upstreams[0].Transport.TLS.ServerName != "orders.internal" {
		t.Fatal("clone shares mutable TLS policy")
	}
	if got.Certificates[0] != certificate || got.TrustBundles[0] != bundle {
		t.Fatal("clone did not retain immutable material handles")
	}
}
```

- [ ] **Step 2: Run the model test to verify RED**

Run:

```powershell
go test ./internal/model -run TestCloneResourceSetClonesTLSReferencesAndSharesImmutableMaterial -count=1
```

Expected: compile failure for the missing types and fields.

- [ ] **Step 3: Add the locked canonical declarations**

Add the declarations from **Locked Cross-Task Interfaces** with exported comments. Keep legacy zero `Protocol` distinguishable until upstream normalization. Clone:

```go
if in.Upstreams[i].Transport.TLS != nil {
	value := *in.Upstreams[i].Transport.TLS
	out.Upstreams[i].Transport.TLS = &value
}
out.Certificates = append([]*tlsmaterial.Certificate(nil), in.Certificates...)
out.TrustBundles = append([]*tlsmaterial.TrustBundle(nil), in.TrustBundles...)
```

- [ ] **Step 4: Run model and dependent compile tests**

Run:

```powershell
go test ./internal/model ./internal/config ./internal/upstream -count=1
```

Expected: PASS with legacy transports still accepted.

- [ ] **Step 5: Commit**

```powershell
git add internal/model/resources.go internal/model/resources_test.go
git commit -m "feat: model upstream tls protocol policy"
```

### Task 3: Decode strict gateway/v1alpha5 and material files

**Files:**
- Create: `internal/config/wire_v1alpha5.go`
- Modify: `internal/config/load.go`
- Modify: `internal/config/load_test.go`
- Modify: `internal/config/validate.go`

**Interfaces:**
- Consumes: Tasks 1–2 material loaders and canonical model.
- Produces: strict v1alpha5 conversion with atomic material loading and unchanged v1alpha1–v1alpha4 compatibility.

- [ ] **Step 1: Write RED v1alpha5 success and compatibility tests**

Build a complete v1alpha5 document using temporary CA/client files:

```yaml
api_version: gateway/v1alpha5
trust_bundles:
  - id: internal-ca
    ca_file: /tmp/internal-ca.pem
certificates:
  - id: orders-client
    certificate_file: /tmp/orders-client.crt
    private_key_file: /tmp/orders-client.key
upstreams:
  - id: orders
    endpoints:
      - url: https://127.0.0.1:8443
        weight: 1
    transport:
      protocol: http2
      tls:
        trust_bundle_ref: internal-ca
        client_certificate_ref: orders-client
        server_name: orders.internal
      dial_timeout: 3s
      response_header_timeout: 10s
      idle_connection_timeout: 90s
      max_idle_connections: 1024
      max_idle_connections_per_host: 256
```

Assert one material of each kind, `TransportProtocolHTTP2`, all three TLS fields, and parsed fingerprints. Decode v1alpha1–v1alpha4 fixtures and assert:

```go
if got.Protocol != model.TransportProtocolHTTP1 || got.TLS != nil {
	t.Fatalf("legacy transport = %+v", got)
}
```

- [ ] **Step 2: Write RED strictness and resource-bound tests**

Add table cases for unknown material fields, duplicate IDs within and across material kinds, missing/empty paths, missing files, each file limit, 10,001 combined resources, and source bytes above 64 MiB. Use sparse files for size checks and small constructor fixtures for semantic checks.

- [ ] **Step 3: Run config tests to verify RED**

Run:

```powershell
go test ./internal/config -run 'V1Alpha5|LegacyTransportProtocol|Material' -count=1
```

Expected: unsupported `gateway/v1alpha5` and missing material fields.

- [ ] **Step 4: Add the v1alpha5 wire schema and conversion**

Compose v4 route, service, health, and retry wire types. Add:

```go
type certificateDocumentV5 struct {
	ID              string `yaml:"id"`
	CertificateFile string `yaml:"certificate_file"`
	PrivateKeyFile  string `yaml:"private_key_file"`
}

type trustBundleDocumentV5 struct {
	ID     string `yaml:"id"`
	CAFile string `yaml:"ca_file"`
}

type transportTLSDocumentV5 struct {
	TrustBundleRef       string `yaml:"trust_bundle_ref"`
	ClientCertificateRef string `yaml:"client_certificate_ref"`
	ServerName           string `yaml:"server_name"`
}
```

The v1alpha5 transport embeds existing timeout/pool fields and adds `Protocol string` plus `TLS *transportTLSDocumentV5`. Default omitted v1alpha5 protocol to `auto`. Legacy converters set `Protocol: model.TransportProtocolHTTP1`.

Load material into local slices, track combined source bytes after every file pair/bundle, and return an empty `ResourceSet` on any error.

- [ ] **Step 5: Add dispatch and v1alpha5 validation entry**

Add `apiVersionV1Alpha5`, strict decode, `convertV5`, and `validateV5`. Validate material counts/IDs before calling the existing v4 bootstrap/resource validation logic and upstream normalization.

Keep cross-resource TLS reference resolution in `internal/upstream/profile.go`, where programmatic `Gateway.Apply` receives the same validation as YAML input.

- [ ] **Step 6: Run compatibility and commit**

Run:

```powershell
go test ./internal/config ./internal/model -count=1
go test ./internal/runtime ./internal/gateway -count=1
```

Expected: PASS for all five API versions.

Commit:

```powershell
git add internal/config internal/model
git commit -m "feat: decode gateway v1alpha5 tls resources"
```

### Task 4: Normalize HTTPS endpoints and compile complete profiles

**Files:**
- Create: `internal/upstream/profile.go`
- Create: `internal/upstream/profile_test.go`
- Modify: `internal/upstream/config.go`
- Modify: `internal/upstream/config_test.go`
- Modify: `internal/upstream/config_fuzz_test.go`

**Interfaces:**
- Consumes: canonical v1alpha5 resources.
- Produces: deterministic endpoint normalization, full validation matrix, material reference resolution, and `transportProfile`.

- [ ] **Step 1: Write RED endpoint and protocol matrix tests**

Use one table with every approved combination:

```go
tests := []struct {
	scheme   string
	protocol model.TransportProtocol
	tls      *model.UpstreamTLSPolicy
	wantErr  string
}{
	{"http", model.TransportProtocolAuto, nil, ""},
	{"http", model.TransportProtocolHTTP1, nil, ""},
	{"http", model.TransportProtocolHTTP2, nil, ""},
	{"https", model.TransportProtocolAuto, nil, ""},
	{"https", model.TransportProtocolHTTP1, nil, ""},
	{"https", model.TransportProtocolHTTP2, nil, ""},
	{"http", model.TransportProtocolHTTP1, &model.UpstreamTLSPolicy{}, "TRANSPORT_TLS_INVALID"},
	{"ftp", model.TransportProtocolHTTP1, nil, "UPSTREAM_ENDPOINT_INVALID"},
}
```

Also reject mixed positive-weight `http`/`https`, unsupported protocol, empty reference values when the TLS block field was explicitly populated with whitespace, invalid server names, URL credentials/query/fragment/path, missing references, duplicate material IDs, and material with an ID unused by an upstream only when its own value is invalid.

- [ ] **Step 2: Run normalization tests to verify RED**

Run:

```powershell
go test ./internal/upstream -run 'Endpoint|Protocol|TransportProfile|MaterialIndex' -count=1
```

Expected: HTTPS rejected by current HTTP-only normalizer.

- [ ] **Step 3: Generalize canonical endpoint normalization**

Accept only `http` and `https`; use default port `80` or `443`; preserve canonical bracketed IPv6:

```go
defaultPort := "80"
if parsed.Scheme == "https" {
	defaultPort = "443"
}
return parsed.Scheme + "://" + net.JoinHostPort(normalizedHost, port), nil
```

After endpoint sorting, derive the scheme from positive-weight endpoints and reject any different positive-weight scheme with `UPSTREAM_SCHEME_MIXED`. Weight-zero endpoints are still canonicalized but do not determine or constrain the active scheme. A subsequent apply that gives a differently schemed endpoint positive weight must pass the same uniform-scheme check.

- [ ] **Step 4: Normalize protocol and TLS policy**

Zero protocol becomes `http1` to preserve existing programmatic callers. The strict v1alpha5 converter writes explicit `auto` when the YAML field is omitted, so new configuration still receives the approved default. Reject TLS on `http`.

Validate `server_name` with `net.ParseIP` or the existing ASCII DNS normalization without changing the configured logical value except lowercasing DNS and trimming one trailing dot. Reject whitespace, port suffixes, wildcards, and empty labels.

- [ ] **Step 5: Resolve immutable material and compile the profile**

Build maps with cross-kind duplicate rejection. Resolve refs and return:

```go
return transportProfile{
	scheme:            scheme,
	protocol:          resource.Transport.Protocol,
	transport:         resource.Transport,
	trustBundle:       materials.trustBundles[tlsPolicy.TrustBundleRef],
	clientCertificate: materials.certificates[tlsPolicy.ClientCertificateRef],
	serverName:        tlsPolicy.ServerName,
}, nil
```

Absent refs leave the corresponding pointer nil. Reject a non-empty missing reference with `TLS_MATERIAL_REF_NOT_FOUND`.

- [ ] **Step 6: Fuzz and commit**

Extend `FuzzNormalizeEndpoint` seeds with HTTPS, IPv4, IPv6, default ports, and malformed schemes. Add a protocol-policy fuzz target that asserts normalization never panics and every successful output belongs to the six-row matrix.

Run:

```powershell
go test ./internal/upstream -count=1
go test ./internal/upstream -run '^$' -fuzz FuzzTransportPolicy -fuzztime 10s
```

Expected: PASS.

Commit:

```powershell
git add internal/upstream/config.go internal/upstream/config_test.go internal/upstream/config_fuzz_test.go internal/upstream/profile.go internal/upstream/profile_test.go
git commit -m "feat: compile upstream transport profiles"
```

### Task 5: Build native protocol transports and complete identity

**Files:**
- Modify: `internal/upstream/transport.go`
- Modify: `internal/upstream/transport_test.go`

**Interfaces:**
- Consumes: Task 4 `transportProfile`.
- Produces: one complete key, native protocol flags, separate production/probe pools, private session caches, and idempotent cleanup.

- [ ] **Step 1: Write RED complete-key table tests**

Start with one HTTPS/mTLS HTTP/2 profile. Independently mutate scheme, protocol, every timeout/pool field, system-root sentinel, trust fingerprint, client-certificate fingerprint, server name, and fixed TLS policy version. Assert every mutation changes `transportKey`; changing weight, health, retry, or route fields is impossible because those fields are absent from `transportProfile`.

Assert identical profiles produce identical keys and that raw PEM/private-key bytes have no representation in `transportKey`.

- [ ] **Step 2: Write RED native protocol and pool-isolation tests**

Inspect transport configuration:

```go
tests := []struct {
	scheme string
	mode model.TransportProtocol
	http1, http2, h2c bool
}{
	{"http", model.TransportProtocolAuto, true, false, false},
	{"http", model.TransportProtocolHTTP1, true, false, false},
	{"http", model.TransportProtocolHTTP2, false, false, true},
	{"https", model.TransportProtocolAuto, true, true, false},
	{"https", model.TransportProtocolHTTP1, true, false, false},
	{"https", model.TransportProtocolHTTP2, false, true, false},
}
```

Assert `production != probe`, their `TLSClientConfig != nil` only for HTTPS, their client session caches are distinct, `MinVersion == tls.VersionTLS12`, and `DisableCompression == true`.

- [ ] **Step 3: Run transport tests to verify RED**

Run:

```powershell
go test ./internal/upstream -run 'TransportKey|NativeProtocols|SessionCache|CloseIdle' -count=1
```

Expected: current key is HTTP/1-only and lacks profile fields.

- [ ] **Step 4: Replace config-only key with complete profile key**

Use fixed sentinels:

```go
const tlsPolicyVersion uint8 = 1

type transportKey struct {
	scheme, serverName string
	protocol model.TransportProtocol
	// existing timeouts and limits
	tlsEnabled bool
	tlsPolicyVersion uint8
	trustSystem bool
	trustFingerprint tlsmaterial.Fingerprint
	clientFingerprint tlsmaterial.Fingerprint
	minTLSVersion uint16
	disableCompression bool
}
```

- [ ] **Step 5: Build two native transports from one profile**

Create `newHTTPTransport(profile, observer)` twice. Configure `http.Protocols` according to the six-row matrix with `SetHTTP1`, `SetHTTP2`, and `SetUnencryptedHTTP2`.

For HTTPS, create a fresh `tls.Config` per pool:

```go
tlsConfig := &tls.Config{
	MinVersion: tls.VersionTLS12,
	ClientSessionCache: tls.NewLRUClientSessionCache(64),
}
if profile.trustBundle != nil {
	tlsConfig.RootCAs = profile.trustBundle.CertPool()
}
if profile.clientCertificate != nil {
	tlsConfig.Certificates = []tls.Certificate{profile.clientCertificate.TLSCertificate()}
}
```

Set `NextProtos` explicitly because Task 6 supplies a custom verified dialer: `auto` gets `[]string{"h2", "http/1.1"}`, `http1` gets `[]string{"http/1.1"}`, and strict `http2` gets `[]string{"h2"}`.

Install the verified dialer from Task 6 in the next task; this task may use `TLSClientConfig` directly while preserving verification.

- [ ] **Step 6: Make cleanup close both pools exactly once**

Inject close functions in tests and assert:

```go
runtime.CloseIdleConnections()
runtime.CloseIdleConnections()
if productionCalls != 1 || probeCalls != 1 {
	t.Fatalf("close calls production=%d probe=%d", productionCalls, probeCalls)
}
```

- [ ] **Step 7: Run and commit**

Run:

```powershell
go test ./internal/upstream -run 'Transport' -count=1
```

Expected: PASS.

Commit:

```powershell
git add internal/upstream/transport.go internal/upstream/transport_test.go
git commit -m "feat: build native upstream protocol transports"
```

### Task 6: Add verified TLS dialing and typed failure classification

**Files:**
- Create: `internal/upstream/tls_error.go`
- Create: `internal/upstream/tls_error_test.go`
- Modify: `internal/upstream/transport.go`
- Modify: `internal/upstream/transport_test.go`

**Interfaces:**
- Consumes: Task 5 HTTPS transports and `TLSObserver`.
- Produces: endpoint-derived or overridden SNI, strict ALPN, handshake observation, and stable typed TLS errors.

- [ ] **Step 1: Write RED classification and redaction tests**

Construct `x509.UnknownAuthorityError`, `x509.HostnameError`, `x509.CertificateInvalidError`, `tls.CertificateVerificationError`, generic `tls.RecordHeaderError`, explicit protocol sentinel, context deadline, and generic `net.OpError`.

Assert trust/hostname/handshake/protocol classes, and assert deadline/network errors do not become `TLSFailureError`. For an mTLS profile, map typed TLS alert numbers `42`, `43`, `44`, `45`, `46`, `48`, and `116` to `client_identity`; other alerts remain `handshake`.

Assert `TLSFailureError.Error()` is exactly `upstream TLS failed: <class>` and never includes the wrapped error text.

- [ ] **Step 2: Run failure tests to verify RED**

Run:

```powershell
go test ./internal/upstream -run 'TLSFailure|TLSDial|StrictALPN|Redact' -count=1
```

Expected: missing typed error and dialer.

- [ ] **Step 3: Add safe typed classification**

Implement `classifyTLSFailure(err, mtls bool)` with `errors.As` against typed X.509/TLS errors. Check context cancellation, deadline, and `net.Error.Timeout()` first and return `(zero, false)`.

Define:

```go
var errHTTP2Required = errors.New("upstream requires negotiated HTTP/2")
```

Map only this sentinel to `protocol`. Do not use error-string matching.

- [ ] **Step 4: Install a verified `DialTLSContext`**

Dial TCP with the configured `net.Dialer`, clone the immutable pool TLS config, and choose verification identity:

```go
serverName := profile.serverName
if serverName == "" {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	serverName = host
}
attemptTLS := baseTLS.Clone()
attemptTLS.ServerName = serverName
connection := tls.Client(raw, attemptTLS)
if err := connection.HandshakeContext(ctx); err != nil {
	// close raw, classify typed TLS failure, observe once, and return
}
```

For strict `http2+https`, require `connection.ConnectionState().NegotiatedProtocol == "h2"`; close and return `TLSFailureProtocol` otherwise. `auto+https` accepts `h2`, `http/1.1`, or empty ALPN; `http1+https` offers only HTTP/1.1.

Observe one handshake success or final typed TLS failure per connection establishment. Mode is exactly `server_auth` or `mtls`.

- [ ] **Step 5: Prove SNI/Host separation**

Run a TLS server that records `ClientHelloInfo.ServerName` and an HTTP handler that records `request.Host`. Send through an IP endpoint with `server_name: orders.internal` and inbound host `api.example.test`. Assert:

```go
if gotSNI != "orders.internal" || gotHost != "api.example.test" {
	t.Fatalf("SNI=%q Host=%q", gotSNI, gotHost)
}
```

- [ ] **Step 6: Run and commit**

Run:

```powershell
go test ./internal/upstream -run 'TLS|Transport' -count=1
```

Expected: PASS.

Commit:

```powershell
git add internal/upstream/tls_error.go internal/upstream/tls_error_test.go internal/upstream/transport.go internal/upstream/transport_test.go
git commit -m "feat: verify and classify upstream tls"
```

### Task 7: Make active HTTP health profile-aware

**Files:**
- Modify: `internal/upstream/probe.go`
- Modify: `internal/upstream/probe_http.go`
- Modify: `internal/upstream/probe_test.go`
- Modify: `internal/upstream/health_scheduler.go`
- Modify: `internal/upstream/health_scheduler_test.go`
- Modify: `internal/upstream/fingerprint.go`
- Modify: `internal/upstream/fingerprint_test.go`

**Interfaces:**
- Consumes: `transportRuntime.ProbeTransport()` and the locked `ProbeTarget`.
- Produces: shared policy with an isolated pool, profile-sensitive health reuse, and unchanged raw TCP probing.

- [ ] **Step 1: Write RED target-transport tests**

Use two counting RoundTrippers in two targets and assert the stateless HTTP prober invokes only the target's transport. Verify redirect responses remain responses, body drain remains capped at 4 KiB plus one byte, cancellation returns `OutcomeTimeout`, typed TLS errors return `OutcomeTransportFailure`, and a nil target transport returns transport failure without panic.

- [ ] **Step 2: Write RED profile/fingerprint tests**

Hold endpoint URL and health policy constant while changing trust bundle, client certificate, server name, scheme, and protocol. Assert every change creates a different health key. Change only weight/retry/route policy and assert the health key remains equal.

- [ ] **Step 3: Run probe tests to verify RED**

Run:

```powershell
go test ./internal/upstream -run 'Probe|HealthKey|ProfileAware' -count=1
```

Expected: current prober ignores target transport and owns one global pool.

- [ ] **Step 4: Make `httpProber` stateless**

Remove its `http.Client` and `http.Transport`. Create the request as before and invoke:

```go
response, err := target.Transport.RoundTrip(request)
```

`CloseIdleConnections` becomes a no-op because transport generation cleanup owns both pools. Keep `tcpProber` unchanged.

- [ ] **Step 5: Update coordinator ownership**

The coordinator still owns one HTTP prober object, one TCP prober, one scheduler, one bounded ready queue, and fixed workers. It no longer owns an HTTP connection pool. Preserve `Close` ordering; closing the stateless prober is harmless.

- [ ] **Step 6: Include transport identity in health identity**

Change:

```go
func makeHealthKey(endpointIdentity string, policy model.HealthPolicy, transport transportKey) healthKey
```

Hash the complete comparable transport key with domain `gateway/health-key/v2`. Update every call site in the following registry task.

- [ ] **Step 7: Run and commit**

Run:

```powershell
go test ./internal/upstream -run 'Probe|Health|Scheduler|Fingerprint' -count=1
```

Expected: PASS.

Commit:

```powershell
git add internal/upstream/probe.go internal/upstream/probe_http.go internal/upstream/probe_test.go internal/upstream/health_scheduler.go internal/upstream/health_scheduler_test.go internal/upstream/fingerprint.go internal/upstream/fingerprint_test.go
git commit -m "feat: align active health with transport profiles"
```

### Task 8: Reconcile transport generations transactionally

**Files:**
- Modify: `internal/upstream/registry.go`
- Modify: `internal/upstream/registry_test.go`
- Modify: `internal/upstream/reaper.go`
- Modify: `internal/upstream/reaper_test.go`
- Modify: `internal/upstream/plan.go`
- Modify: `internal/runtime/manager.go`
- Modify: `internal/runtime/manager_test.go`

**Interfaces:**
- Consumes: complete `model.ResourceSet`, material index, transport profiles, and profile-aware health keys.
- Produces: atomic create/reuse/rollback/retirement with old-stream retention.

- [ ] **Step 1: Write RED registry identity tests**

Prepare and commit a custom-CA HTTPS profile, then prepare:

- weight-only, retry-only, health-threshold-only, and route-only changes: same transport pointer;
- CA, client certificate, server name, scheme, and protocol changes: different transport pointer;
- an unrelated upstream change: unchanged transport pointer for the unaffected upstream.

Assert production and probe transports rotate together and remain distinct.

- [ ] **Step 2: Write RED rollback and retirement tests**

Fail after one valid upstream has acquired a new profile by placing an invalid second upstream. Assert all live registry counts return to pre-prepare values and both new pools close exactly once.

Hold a plan-set lease, commit a material rotation, retire the old set, and assert the old transport remains open until lease release and asynchronous reaping.

- [ ] **Step 3: Run registry tests to verify RED**

Run:

```powershell
go test ./internal/upstream ./internal/runtime -run 'TransportGeneration|MaterialRotation|Rollback|Retire|PrepareResourceSet' -count=1
```

Expected: current `Prepare` accepts only upstreams and cannot resolve profiles.

- [ ] **Step 4: Prepare the complete resource set**

Change manager:

```go
candidate, err := m.upstreams.Prepare(resources)
```

Inside `Registry.Prepare`, clone/normalize the upstream slice, build `materialIndex` once, and compile each profile before acquiring its transport. Do not store route/service objects in the registry.

Update upstream-only test helpers:

```go
candidate, err := registry.Prepare(model.ResourceSet{Upstreams: resources})
```

- [ ] **Step 5: Associate probe transports and profile health keys**

When registering HTTP health:

```go
target := ProbeTarget{
	EndpointID: identity,
	URL: entry.runtime.target,
	Generation: health.Generation(),
	Policy: *resource.Health.Active,
	Transport: transport.runtime.ProbeTransport(),
}
```

TCP targets may leave `Transport` nil. Compute health keys with the transport key for both acquisition and plan registration lookup.

- [ ] **Step 6: Preserve transactional cleanup**

Profile compilation occurs before reference increments for that upstream. Any error releases all earlier endpoint, transport, selection, health, and budget references. Close both pools outside the registry mutex. Candidate commit/rollback remains exclusive and idempotent.

- [ ] **Step 7: Run repeated lifecycle tests and commit**

Run:

```powershell
go test ./internal/upstream ./internal/runtime -count=1
go test ./internal/upstream ./internal/runtime -run 'Rotation|Rollback|Retire|Reaper' -count=20
```

Expected: PASS with steady registry counts.

Commit:

```powershell
git add internal/upstream internal/runtime/manager.go internal/runtime/manager_test.go
git commit -m "feat: reconcile tls transport generations"
```

### Task 9: Preserve retry semantics and expose the stable TLS error

**Files:**
- Modify: `internal/proxy/retry.go`
- Modify: `internal/proxy/retry_test.go`
- Modify: `internal/proxy/route_transport.go`
- Modify: `internal/proxy/attempt_transport_test.go`
- Modify: `internal/proxy/handler.go`
- Modify: `internal/proxy/runtime_handler_test.go`

**Interfaces:**
- Consumes: typed `TLSFailureError`.
- Produces: connection-failure retry/health classification, final `502 UPSTREAM_TLS_FAILED`, and no gRPC-status retry.

- [ ] **Step 1: Write RED classifier tests**

For every TLS failure class, assert:

```go
decision := classifyAttempt(policyWithConnectionRetry, nil, tlsErr)
if !decision.Retry ||
	decision.Reason != retryReasonConnectionFailure ||
	decision.Observation.Kind != upstream.OutcomeTransportFailure {
	t.Fatalf("decision = %+v", decision)
}
```

Assert TLS timeout remains `OutcomeTimeout`, and an HTTP/2 response with `grpc-status: 14` in trailers returns `Retry == false` unless its HTTP status itself is configured.

- [ ] **Step 2: Write RED final mapping and redaction tests**

Return a TLS error containing a fake endpoint, path, subject, and alert string from the final attempt. Assert response:

```json
{"code":"UPSTREAM_TLS_FAILED","message":"upstream TLS failed"}
```

and assert none of the fake sensitive values appears in the response or captured log output.

- [ ] **Step 3: Run proxy tests to verify RED**

Run:

```powershell
go test ./internal/proxy -run 'TLS|GRPCStatus|Retry' -count=1
```

Expected: TLS currently maps to generic connection failure.

- [ ] **Step 4: Classify TLS before generic network errors**

In `classifyAttempt`, after timeout detection and before `net.OpError` connection checks:

```go
if upstream.IsTLSFailure(err) {
	decision.Observation.Kind = upstream.OutcomeTransportFailure
	decision.Retry = policy.RetryOn.ConnectionFailure
	decision.Reason = retryReasonConnectionFailure
	return decision
}
```

Do not inspect response trailers in retry policy.

- [ ] **Step 5: Preserve the final typed error**

The attempt loop returns the final `roundTripErr` unchanged after retry exhaustion. `handleProxyError` checks `upstream.IsTLSFailure(err)` after deadline/no-healthy checks and maps to the stable TLS response.

Log only:

```go
h.logger.Error("upstream request failed", "class", publicUpstreamErrorClass(err))
```

where the closed classes are `tls`, `timeout`, `unhealthy`, and `connection`. Do not log `err`.

- [ ] **Step 6: Run and commit**

Run:

```powershell
go test ./internal/proxy -count=1
go test ./internal/runtime ./internal/gateway -count=1
```

Expected: PASS.

Commit:

```powershell
git add internal/proxy
git commit -m "feat: map upstream tls failures safely"
```

### Task 10: Prove HTTP protocol, TLS, and health interoperability

**Files:**
- Create: `internal/upstream/protocol_integration_test.go`
- Create: `internal/upstream/tls_integration_test.go`
- Modify: `test/integration/tls_test.go`

**Interfaces:**
- Consumes: the complete transport, registry, and proxy path.
- Produces: deterministic evidence for all protocol rows and TLS security cases.

- [ ] **Step 1: Add the six-row protocol integration table**

Start explicit H1, TLS H1, TLS H2, dual TLS H1/H2, and h2c upstream servers. For every matrix row, send a request and assert the upstream-observed `request.ProtoMajor`.

For `auto+https`, run one H2 server and one H1-only server to prove both negotiation and fallback. For strict HTTP/2 against H1-only TLS, assert typed `TLSFailureProtocol`.

- [ ] **Step 2: Add streaming and trailer cases**

Over TLS H2 and h2c, stream multiple flushed chunks and set declared trailers. Assert the caller receives chunks before completion and final trailers after EOF. Cancel mid-stream and assert upstream context cancellation within a bounded channel wait.

- [ ] **Step 3: Add TLS trust, hostname, SNI, and mTLS cases**

Use a private root and issued server/client certificates. Cover:

- custom CA success;
- system-root mode rejecting the private CA;
- replacement private bundle not trusting an unrelated root;
- hostname mismatch;
- IP endpoint with valid `server_name`;
- mTLS success;
- missing client identity rejected;
- untrusted client identity rejected;
- TLS 1.1-only upstream rejected.

Assert failures are typed and public integration responses remain redacted. Prove a second connection within one unchanged generation can resume its TLS session, then rotate trust/client material and prove the new generation does not reuse the prior session.

- [ ] **Step 4: Add HTTPS active-health cases**

Start one HTTPS endpoint that requires custom trust and mTLS. Prove the active probe reaches healthy only with the correct profile, uses the configured protocol, and opens a probe connection distinct from the production connection count.

Keep one TCP health case against the same TLS port and prove it reports raw reachability even when HTTP/TLS policy would fail.

Configure two HTTPS endpoints under one profile where the first returns a typed hostname/trust failure and the second succeeds. For a replayable request, assert one budgeted gateway retry selects the second endpoint. Repeat with a non-replayable body, exhausted retry budget, and expired total deadline; assert no unsafe or out-of-budget attempt occurs.

- [ ] **Step 5: Run focused and repeated integration**

Run:

```powershell
go test ./internal/upstream ./test/integration -run 'Protocol|TLS|MTLS|SNI|ActiveHealth' -count=1
go test ./internal/upstream ./test/integration -run 'Protocol|TLS|MTLS|SNI|ActiveHealth' -count=10
```

Expected: PASS without flakes or sensitive output.

- [ ] **Step 6: Commit**

```powershell
git add internal/upstream/protocol_integration_test.go internal/upstream/tls_integration_test.go test/integration/tls_test.go
git commit -m "test: prove upstream tls protocol interoperability"
```

### Task 11: Add real gRPC pass-through interoperability

**Files:**
- Modify: `go.mod`
- Modify: `go.sum`
- Create: `internal/proxy/grpc_integration_test.go`

**Interfaces:**
- Consumes: native downstream TLS HTTP/2 and upstream HTTP/2 TLS/h2c.
- Produces: real gRPC-Go unary and streaming pass-through evidence without production gRPC dependencies.

- [ ] **Step 1: Pin the test dependency**

Run:

```powershell
go get google.golang.org/grpc@v1.82.1
go mod tidy
```

Expected: `go.mod` pins `google.golang.org/grpc v1.82.1`; production packages do not import it.

- [ ] **Step 2: Add a real test service**

Use `google.golang.org/grpc/interop/grpc_testing` generated messages and register a local implementation of `grpc_testing.TestServiceServer`. Implement:

- `UnaryCall` echoing payload and setting headers/trailers;
- `StreamingInputCall` summing received payload sizes;
- `StreamingOutputCall` sending one response per requested parameter;
- `FullDuplexCall` echoing each received payload and observing cancellation.

Return `status.Error(codes.PermissionDenied, "denied")` for a designated unary request.

- [ ] **Step 3: Test grpcs upstream through the gateway**

Start the gRPC server with TLS, configure custom trust and `protocol: http2`, start the gateway TLS listener, and connect a real `grpc.ClientConn` to the gateway with downstream credentials.

Assert unary payload, incoming/outgoing metadata, headers, trailers, and `codes.PermissionDenied` propagate unchanged.

- [ ] **Step 4: Test all streaming shapes**

Use the generated client to verify client-streaming aggregation, ordered server-streaming payloads, and bidirectional echo. Set `total_timeout: 0`.

Cancel a bidi context after the first response and wait for the upstream handler's cancellation channel. Assert no synthetic HTTP error body and no second gateway retry.

- [ ] **Step 5: Test h2c upstream**

Run the same unary and bidi cases against a cleartext gRPC server with endpoint `http://...` and `protocol: http2`. Downstream remains gateway TLS HTTP/2.

- [ ] **Step 6: Run and commit**

Run:

```powershell
go test ./internal/proxy -run 'GRPC' -count=1 -v
go test ./internal/proxy -run 'GRPCStreaming|GRPCCancellation' -count=10
```

Expected: PASS.

Commit:

```powershell
git add go.mod go.sum internal/proxy/grpc_integration_test.go
git commit -m "test: prove native grpc pass through"
```

### Task 12: Add bounded TLS and generation telemetry

**Files:**
- Modify: `internal/upstream/observer.go`
- Modify: `internal/upstream/registry.go`
- Modify: `internal/upstream/transport.go`
- Modify: `internal/telemetry/telemetry.go`
- Modify: `internal/telemetry/telemetry_test.go`
- Modify: `internal/gateway/lifecycle_observer.go`
- Modify: `internal/gateway/lifecycle_observer_test.go`
- Modify: `internal/gateway/gateway.go`
- Modify: `internal/gateway/gateway_test.go`

**Interfaces:**
- Consumes: TLS dial events and registry generation deltas.
- Produces: the three exact bounded metric families and redacted lifecycle events.

- [ ] **Step 1: Write RED exact-family/cardinality tests**

Assert:

```text
gateway_upstream_tls_handshake_total{result="success|failure",mode="server_auth|mtls",protocol="auto|http1|http2"}
gateway_upstream_tls_failure_total{class="trust|hostname|client_identity|protocol|handshake"}
gateway_upstream_transport_generation_total{action="create|reuse|retire",tls="true|false",protocol="auto|http1|http2"}
```

Scrape after representative events and assert no upstream ID, endpoint, hostname, material ID, fingerprint, revision, path, subject, SAN, or error text appears as a label.

- [ ] **Step 2: Run telemetry tests to verify RED**

Run:

```powershell
go test ./internal/telemetry ./internal/gateway -run 'TLS|TransportGeneration|Cardinality' -count=1
```

Expected: metric families do not exist.

- [ ] **Step 3: Add closed observer events**

Extend `Observer` with the locked TLS methods. The registry implements `TLSObserver` by forwarding to its observer through the existing panic boundary.

Compact generation deltas by `(action,tls,protocol)` before observer delivery. Count create/reuse during preparation and retire only when the final transport reference is removed.

- [ ] **Step 4: Register exact collectors**

Create CounterVecs with only the specified label names. Pre-initialize all closed combinations at telemetry construction so a scrape exposes stable families before traffic. Store the returned counters in fixed arrays:

```go
tlsHandshake      [2][2][3]prometheus.Counter
tlsFailures       [5]prometheus.Counter
transportLifecycle [3][2][3]prometheus.Counter
```

Map result, mode, protocol, failure class, action, and TLS boolean to array indexes with closed `switch` functions. Request/handshake callbacks increment the stored counter directly and never call `WithLabelValues`.

`lifecycleObserver` forwards handshake/failure to telemetry. Lifecycle logs contain only action, TLS boolean, protocol, counts, and stable failure class.

- [ ] **Step 5: Prove observer panic isolation and shutdown**

Extend `panicRegistryObserver` with the new methods. Trigger observer panic from a TLS handshake and a generation retirement; assert the request/cleanup completes and final registry counts are zero.

- [ ] **Step 6: Run and commit**

Run:

```powershell
go test ./internal/upstream ./internal/telemetry ./internal/gateway -count=1
```

Expected: PASS.

Commit:

```powershell
git add internal/upstream/observer.go internal/upstream/registry.go internal/upstream/transport.go internal/telemetry internal/gateway
git commit -m "feat: expose bounded upstream tls telemetry"
```

### Task 13: Prove rotation, rollback, stream drain, and shutdown

**Files:**
- Modify: `internal/upstream/registry_test.go`
- Modify: `internal/gateway/gateway_test.go`
- Modify: `test/integration/upstream_reconcile_test.go`

**Interfaces:**
- Consumes: complete Phase 3C1 lifecycle and observability.
- Produces: last-known-good behavior, unrelated pool stability, live-stream retention, steady-state cleanup, and deterministic shutdown evidence.

- [ ] **Step 1: Add material reload and last-known-good tests**

Load revision 1 from valid files, replace CA/client contents, load again, and apply revision 2. Assert new traffic uses the new identity and the old pool retires.

Then load malformed/oversized/missing material and assert no `Apply` occurs, active revision remains 2, old traffic continues, and live registry counts do not change.

- [ ] **Step 2: Add active HTTP/2 stream rotation**

Open an HTTP/2 response stream under revision 1, wait for the first frame, apply changed CA/SNI/protocol material as revision 2, and assert the old stream completes through its old transport. A new request must use revision 2's transport.

- [ ] **Step 3: Add active gRPC bidi rotation**

Keep a bidi RPC open across a material rotation. Exchange messages before and after apply, then close normally. Assert the old generation retires only after the RPC lease releases.

- [ ] **Step 4: Add unrelated-upstream and repeated-rotation checks**

Warm upstream B, rotate A twenty times, and assert B's production/probe transport pointers and connection counts remain stable. After every old lease releases and the reaper reaches steady state:

```go
if stats.LiveTransports != activeUniqueProfiles ||
	stats.RetiredPlanSets != 0 {
	t.Fatalf("registry did not reach steady state: %+v", stats)
}
```

- [ ] **Step 5: Add readiness-first shutdown with live streams**

Start one HTTP/2 stream and one gRPC stream, begin shutdown, assert readiness returns 503, allow both streams to finish, and assert probe work stops before registry closure. Final endpoint, transport, health, budget, active-set, and retired-set counts must all be zero.

- [ ] **Step 6: Run repeated lifecycle and race**

Run:

```powershell
go test ./internal/upstream ./internal/gateway ./test/integration -run 'Material|Rotation|HTTP2Stream|GRPCStream|Shutdown|Rollback' -count=20
go test -race ./internal/upstream ./internal/runtime ./internal/proxy ./internal/gateway ./test/integration -run 'Material|Rotation|HTTP2|GRPC|Shutdown' -count=1
```

Expected: repeated suite passes. Race passes on a CGO-capable host; otherwise record the exact environment error in Task 15.

- [ ] **Step 7: Commit**

```powershell
git add internal/upstream/registry_test.go internal/gateway/gateway_test.go test/integration/upstream_reconcile_test.go
git commit -m "test: prove phase 3c1 transport lifecycle"
```

### Task 14: Add fuzz, acceptance, and performance evidence code

**Files:**
- Create: `internal/tlsmaterial/fuzz_test.go`
- Create: `internal/upstream/phase3c1_acceptance_test.go`
- Create: `internal/proxy/phase3c1_acceptance_test.go`
- Create: `internal/upstream/phase3c1_benchmark_test.go`

**Interfaces:**
- Consumes: complete Phase 3C1 runtime.
- Produces: bounded parser/policy fuzzing, full-envelope acceptance, healthy-path regression checks, and named protocol benchmarks.

- [ ] **Step 1: Add material and policy fuzz targets**

Create:

```text
FuzzCertificateDocument
FuzzTrustBundleDocument
FuzzTransportPolicy
FuzzTransportKey
FuzzTLSFailureRedaction
```

Seed valid/malformed PEM, duplicate/reordered CA blocks, all protocol/scheme rows, and typed failure wrappers. Assert no panic, successful fingerprints are deterministic, transport keys are deterministic, and public error strings never include input bytes.

- [ ] **Step 2: Add normal/full compile and rotation acceptance**

Use seed `20260731`.

Normal:

```go
phase3C1Profile{
	Upstreams: 1_000,
	EndpointsPerUpstream: 10,
	MaterialResources: 1_000,
	Rotations: 2,
}
```

Full when `GATEWAY_PHASE3C1_ACCEPTANCE=1`:

```go
phase3C1Profile{
	Upstreams: 10_000,
	EndpointsPerUpstream: 10,
	MaterialResources: 10_000,
	Rotations: 20,
}
```

Assert 100,000 total endpoints, no more than 1,000 per upstream, at most 64 MiB source input in loader fixtures, at most 512 MiB incremental active heap, exact active transport counts, and steady-state return after retirement.

- [ ] **Step 3: Add cleartext Phase 3B comparison**

Use the same warmed in-process gateway/upstream workload for Phase 3B legacy H1 and Phase 3C1 v1alpha5 `http+auto`. Record throughput, sorted p99, and `testing.AllocsPerRun` for selection/lease/RoundTrip setup.

In full acceptance:

```go
if phase3C1Throughput < phase3BThroughput*0.95 {
	t.Fatalf("throughput %.2f, want >= 95%% of %.2f", phase3C1Throughput, phase3BThroughput)
}
if phase3C1P99 > time.Duration(float64(phase3BP99)*1.10) {
	t.Fatalf("p99 %s, want <= 110%% of %s", phase3C1P99, phase3BP99)
}
if phase3C1Allocs > phase3BAllocs {
	t.Fatalf("allocs %.2f, want <= %.2f", phase3C1Allocs, phase3BAllocs)
}
```

- [ ] **Step 4: Add locked benchmark names**

Create:

```text
BenchmarkTransportProfile/cleartext-http1
BenchmarkTransportProfile/https-http1
BenchmarkTransportProfile/https-http2
BenchmarkTransportProfile/h2c
BenchmarkTLSHandshake/full
BenchmarkTLSHandshake/resumed
BenchmarkHTTPProtocol/https-http1
BenchmarkHTTPProtocol/https-http2-multiplexed
BenchmarkHTTPProtocol/h2c
BenchmarkGRPC/unary
BenchmarkGRPC/server-streaming
BenchmarkTransportGeneration/create
BenchmarkTransportGeneration/reuse
BenchmarkTransportGeneration/rotate
```

Call `b.ReportAllocs()` and keep local listeners outside the timed setup when measuring request operations.

- [ ] **Step 5: Run normal acceptance, fuzz smoke, and benchmarks**

Run:

```powershell
go test ./internal/tlsmaterial ./internal/upstream ./internal/proxy -run 'TestPhase3C1' -count=1 -v
go test ./internal/tlsmaterial -run '^$' -fuzz FuzzCertificateDocument -fuzztime 10s
go test ./internal/upstream -run '^$' -fuzz FuzzTransportPolicy -fuzztime 10s
go test ./internal/upstream ./internal/proxy -run '^$' -bench 'BenchmarkTransportProfile|BenchmarkTLSHandshake|BenchmarkHTTPProtocol|BenchmarkGRPC|BenchmarkTransportGeneration' -benchmem -count=3
```

Expected: normal acceptance and fuzz smoke pass; benchmarks produce observed numbers without thresholds inferred from one run.

- [ ] **Step 6: Commit**

```powershell
git add internal/tlsmaterial/fuzz_test.go internal/upstream/phase3c1_acceptance_test.go internal/upstream/phase3c1_benchmark_test.go internal/proxy/phase3c1_acceptance_test.go
git commit -m "test: add phase 3c1 acceptance benchmarks"
```

### Task 15: Document operations and record the verification checkpoint

**Files:**
- Create: `configs/phase3c1.yaml`
- Create: `docs/operations/phase-3c1-runbook.md`
- Create: `docs/benchmarks/phase-3c1-current-status.md`
- Modify: `README.md`
- Modify: `docs/superpowers/specs/2026-07-21-go-native-api-gateway-phase-roadmap-design.md`
- Modify only for delivered-name reconciliation: `docs/superpowers/specs/2026-07-30-phase-3c1-upstream-tls-protocol-design.md`

**Interfaces:**
- Consumes: all delivered behavior and observed command output.
- Produces: runnable configuration contract, operator guidance, evidence ledger, roadmap checkpoint, and a clean branch.

- [ ] **Step 1: Write the runnable configuration contract**

`configs/phase3c1.yaml` demonstrates:

- HTTPS HTTP/2 with custom trust, client certificate, and fixed server name;
- HTTPS auto with system roots;
- h2c;
- `total_timeout: 0` on a streaming route;
- HTTP active health using the production security policy;
- raw TCP health.

Use mounted paths such as `/secrets/internal-ca.pem`; do not add certificates or keys to the repository.

- [ ] **Step 2: Write the runbook**

Document:

- the exact protocol matrix;
- trust replacement versus system roots;
- SNI/verification versus HTTP Host;
- mTLS and fixed TLS 1.2 floor;
- material limits, read-on-apply behavior, and no watcher;
- safe rotation/rollback/retirement;
- production versus probe pool separation;
- gRPC streaming and `total_timeout: 0`;
- retry, health, and stable error mapping;
- exact metrics and forbidden labels;
- readiness-first shutdown;
- troubleshooting using stable categories without logging secrets.

- [ ] **Step 3: Create the evidence ledger with conservative status**

Use exactly one status:

```text
implementation in progress
implementation complete; canonical protocol evidence pending
Phase 3C1 accepted
```

Record toolchain, OS/architecture, CGO/compiler availability, commands, elapsed time, acceptance profile/seed, heap, live/retired counts, throughput ratio, p99 ratio, allocation delta, benchmark medians, fuzz duration, race result, and omitted reference-Linux/APISIX gates.

- [ ] **Step 4: Update navigation and phase handoff**

README and roadmap state:

- Phase 3C1 adds outbound TLS/mTLS, HTTP/2/h2c, and gRPC pass-through;
- Phase 3C2 receives generic immutable `Certificate` resources for downstream exact/wildcard SNI;
- Phase 3C3 receives HTTP listener/runtime foundations for WebSocket lifecycle;
- Phase 3D still owns access logs and integrated APISIX comparison.

Do not mark umbrella Phase 3C complete.

- [ ] **Step 5: Run formatting, static, unit, integration, and build gates**

Run:

```powershell
$unformatted = @(gofmt -l .)
if ($unformatted.Count -ne 0) {
	$unformatted
	exit 1
}
go vet ./...
staticcheck -tests=false ./...
revive -set_exit_status -config revive.toml -formatter default ./...
go test -p 1 ./... -count=1
go build ./cmd/...
```

Expected: all commands pass.

- [ ] **Step 6: Run race, fuzz, normal/full acceptance, and benchmarks**

Run:

```powershell
go test -race ./internal/tlsmaterial ./internal/upstream ./internal/runtime ./internal/proxy ./internal/gateway ./test/integration -count=1
go test ./internal/tlsmaterial -run '^$' -fuzz FuzzCertificateDocument -fuzztime 30s
go test ./internal/tlsmaterial -run '^$' -fuzz FuzzTrustBundleDocument -fuzztime 30s
go test ./internal/upstream -run '^$' -fuzz FuzzTransportPolicy -fuzztime 30s
go test ./internal/upstream ./internal/proxy -run 'TestPhase3C1' -count=1 -v
$env:GATEWAY_PHASE3C1_ACCEPTANCE = '1'
go test ./internal/upstream ./internal/proxy -run 'TestPhase3C1' -count=1 -v
Remove-Item Env:GATEWAY_PHASE3C1_ACCEPTANCE
go test ./internal/upstream ./internal/proxy -run '^$' -bench 'BenchmarkTransportProfile|BenchmarkTLSHandshake|BenchmarkHTTPProtocol|BenchmarkGRPC|BenchmarkTransportGeneration' -benchmem -count=5
```

Expected: record actual output. If race cannot start because CGO/compiler support is absent, preserve the exact failure and pending status.

- [ ] **Step 7: Repeat lifecycle and protocol suites**

Run:

```powershell
go test ./internal/upstream ./internal/proxy ./internal/gateway ./test/integration -run 'TLS|MTLS|Protocol|HTTP2|GRPC|Rotation|Rollback|Retire|Shutdown' -count=20
```

Expected: PASS without leaked transports, probe work, sessions, health trackers, plan sets, or live streams.

- [ ] **Step 8: Verify documentation and repository state**

Run the repository's existing Markdown link-check pattern over README, runbook, evidence, roadmap, design, and this plan. Then run:

```powershell
git diff --check
git status --short
git log --oneline --decorate -30
```

Expected: no broken links or whitespace errors; only Task 15 documentation files are uncommitted.

- [ ] **Step 9: Commit observed documentation**

```powershell
git add configs/phase3c1.yaml docs/operations/phase-3c1-runbook.md docs/benchmarks/phase-3c1-current-status.md README.md docs/superpowers/specs/2026-07-21-go-native-api-gateway-phase-roadmap-design.md docs/superpowers/specs/2026-07-30-phase-3c1-upstream-tls-protocol-design.md
git commit -m "docs: record phase 3c1 protocol runtime"
```

- [ ] **Step 10: Confirm the clean Phase 3C1 checkpoint**

Run:

```powershell
git status --short --branch
```

Expected: clean `codex/phase3c1-upstream-tls-protocol-design`. Use `superpowers:finishing-a-development-branch` only after the evidence ledger reflects every executed and pending gate.

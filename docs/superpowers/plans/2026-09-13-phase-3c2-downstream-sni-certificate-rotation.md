# Phase 3C2 Downstream SNI Certificate Rotation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add strict v1alpha7 default/exact/wildcard downstream certificate selection and atomic new-connection rotation without restarting listeners or interrupting established traffic.

**Architecture:** Compile an immutable downstream TLS selector into each runtime snapshot and make `runtime.Manager.GetCertificate` the HTTPS listener callback. Route, upstream, plugin, and certificate state are published by the existing single snapshot pointer, so rejected candidates preserve the complete last-known-good revision.

**Tech Stack:** Go standard library (`crypto/tls`, `crypto/x509`, `net`, `strings`, `sync/atomic`, `testing`), strict YAML via `go.yaml.in/yaml/v3`, Prometheus client metrics, existing runtime/upstream lifecycle, package-local and process-level Go tests.

**Spec:** `docs/superpowers/specs/2026-09-13-phase-3c2-downstream-sni-certificate-rotation-design.md`

## Global Constraints

- Keep one existing downstream HTTPS listener; do not add listener resources or restart listeners during rotation.
- `gateway/v1alpha7` requires `downstream_tls.default_certificate_ref`; v1alpha1 through v1alpha6 retain the static bootstrap certificate behavior.
- Accept ASCII DNS names only, remove one trailing dot, lowercase ASCII, and reject IP literals, invalid labels, names over 253 bytes, or labels over 63 bytes.
- Wildcards have exactly one leading `*.` and match exactly one label; exact matches always win.
- Reject duplicate exact names and wildcard suffixes after canonicalization, even when they reference the same certificate.
- Validate downstream references, DNS SAN coverage, and inclusive `NotBefore`/`NotAfter` at candidate build time.
- Bound aggregate SNI hosts at exactly 10,000, in addition to the existing 10,000 material-resource and 64 MiB source-byte limits.
- Invalid ClientHello SNI and absent SNI select the default certificate; invalid configured names reject the entire candidate.
- Successful rotation affects new TLS connections only; established HTTP/1.1, HTTP/2, gRPC, and WebSocket connections remain open.
- Set `tls.Config.SessionTicketsDisabled` to `true`; do not implement ticket-key rotation.
- Never emit hostname, certificate ID, DER/PEM, private-key data, file contents, or raw parse errors as metric labels or lifecycle log fields.
- Do not add downstream client mTLS, ACME, OCSP refresh, filesystem watch, SecretRef resolution, multiple listeners, access logging, or APISIX parity claims.
- Follow TDD for every behavior change and commit each task with the Conventional Commit subject shown.

## File Map

- Create `internal/model/downstream_tls.go` for canonical policy and binding types.
- Modify `internal/model/resources.go` only to attach and deep-clone downstream TLS policy.
- Create `internal/downstreamtls/hostname.go` for canonical exact/wildcard parsing.
- Create `internal/downstreamtls/selector.go` for immutable selector compilation, errors, statistics, and lookup.
- Create `internal/downstreamtls/hostname_test.go`, `selector_test.go`, `selector_fuzz_test.go`, and `selector_benchmark_test.go` for the package contract.
- Modify `internal/downstreamtls/provider.go` only to share selection result types and retain legacy static behavior.
- Create `internal/config/wire_v1alpha7.go` for the strict wire document and conversion.
- Modify `internal/config/load.go` and `internal/config/validate.go` to register and validate v1alpha7.
- Modify `internal/config/load_test.go` and add `configs/phase3c2.yaml` for decoder acceptance and example loading.
- Modify `internal/runtime/builder.go`, `snapshot.go`, `manager.go`, and their tests to compile, publish, and serve the selector from the active snapshot.
- Modify `internal/gateway/gateway.go`, `lifecycle_observer.go`, and tests to wire the runtime callback and disable session tickets.
- Modify `internal/telemetry/telemetry.go` and `telemetry_test.go` for bounded downstream TLS gauges and selection counters.
- Create `test/integration/downstream_tls_test.go` for end-to-end selection, rotation, rejection, and connection continuity.
- Create `internal/gateway/phase3c2_lifecycle_test.go` for repeated concurrent rotation and steady-state ownership.
- Create `docs/operations/phase-3c2-runbook.md` and `docs/benchmarks/phase-3c2-current-status.md`; update `README.md` and the phase roadmap.

---

### Task 1: Add the Canonical Downstream TLS Policy

**Files:**
- Create: `internal/model/downstream_tls.go`
- Modify: `internal/model/resources.go`
- Test: `internal/model/resources_test.go`

**Interfaces:**
- Consumes: existing immutable `tlsmaterial.Certificate` handles in `model.ResourceSet.Certificates`.
- Produces: `model.DownstreamTLSPolicy`, `model.SNIBinding`, and `ResourceSet.DownstreamTLS *DownstreamTLSPolicy` for config and runtime compilation.

- [ ] **Step 1: Write failing clone-ownership tests**

Add `TestCloneResourceSetOwnsDownstreamTLSPolicy` and `TestCloneResourceSetPreservesAbsentDownstreamTLSPolicy`:

```go
func TestCloneResourceSetOwnsDownstreamTLSPolicy(t *testing.T) {
	in := ResourceSet{DownstreamTLS: &DownstreamTLSPolicy{
		DefaultCertificateRef: "default",
		SNIBindings: []SNIBinding{{CertificateRef: "wild", Hosts: []string{"*.example.com"}}},
	}}
	out := CloneResourceSet(in)
	out.DownstreamTLS.DefaultCertificateRef = "changed"
	out.DownstreamTLS.SNIBindings[0].CertificateRef = "changed"
	out.DownstreamTLS.SNIBindings[0].Hosts[0] = "api.example.com"
	if in.DownstreamTLS.DefaultCertificateRef != "default" ||
		in.DownstreamTLS.SNIBindings[0].CertificateRef != "wild" ||
		in.DownstreamTLS.SNIBindings[0].Hosts[0] != "*.example.com" {
		t.Fatal("CloneResourceSet aliased downstream TLS policy")
	}
}
```

- [ ] **Step 2: Run the focused test and confirm RED**

Run: `go test ./internal/model -run 'TestCloneResourceSet.*DownstreamTLS' -count=1`

Expected: FAIL because `DownstreamTLSPolicy`, `SNIBinding`, and `ResourceSet.DownstreamTLS` do not exist.

- [ ] **Step 3: Add the policy types and deep clone**

Create the focused model file:

```go
package model

// DownstreamTLSPolicy selects the default certificate and explicit SNI bindings for one runtime revision.
type DownstreamTLSPolicy struct {
	DefaultCertificateRef string
	SNIBindings           []SNIBinding
}

// SNIBinding maps explicit exact or wildcard DNS hosts to one certificate resource.
type SNIBinding struct {
	CertificateRef string
	Hosts          []string
}
```

Add `DownstreamTLS *DownstreamTLSPolicy` to `ResourceSet`. Extend `CloneResourceSet` using:

```go
func cloneDownstreamTLSPolicy(in *DownstreamTLSPolicy) *DownstreamTLSPolicy {
	if in == nil {
		return nil
	}
	out := &DownstreamTLSPolicy{
		DefaultCertificateRef: in.DefaultCertificateRef,
		SNIBindings:           make([]SNIBinding, len(in.SNIBindings)),
	}
	for index := range in.SNIBindings {
		out.SNIBindings[index] = in.SNIBindings[index]
		out.SNIBindings[index].Hosts = append([]string(nil), in.SNIBindings[index].Hosts...)
	}
	return out
}
```

- [ ] **Step 4: Run model tests and format**

Run: `gofmt -w internal/model/downstream_tls.go internal/model/resources.go internal/model/resources_test.go`

Run: `go test ./internal/model -count=1`

Expected: PASS.

- [ ] **Step 5: Commit the canonical model**

```bash
git add internal/model/downstream_tls.go internal/model/resources.go internal/model/resources_test.go
git commit -m "feat: model downstream sni bindings"
```

### Task 2: Canonicalize Exact and Wildcard Hostnames

**Files:**
- Create: `internal/downstreamtls/hostname.go`
- Create: `internal/downstreamtls/hostname_test.go`
- Create: `internal/downstreamtls/hostname_fuzz_test.go`

**Interfaces:**
- Consumes: raw configured hosts and ClientHello `ServerName` strings.
- Produces: `canonicalizeServerName(string, *[253]byte) ([]byte, error)` for allocation-free lookup and `normalizeBindingHost(string) (canonical string, wildcard bool, error)` for selector compilation.

- [ ] **Step 1: Write the hostname matrix tests**

Use one table for accepted values and one for rejection:

```go
func TestNormalizeBindingHost(t *testing.T) {
	tests := []struct {
		raw, want string
		wildcard bool
	}{
		{"API.Example.COM.", "api.example.com", false},
		{"*.Example.COM", "example.com", true},
		{"a-b.example", "a-b.example", false},
	}
	for _, test := range tests {
		got, wildcard, err := normalizeBindingHost(test.raw)
		if err != nil || got != test.want || wildcard != test.wildcard {
			t.Fatalf("normalizeBindingHost(%q)=(%q,%v,%v)", test.raw, got, wildcard, err)
		}
	}
}

func TestNormalizeBindingHostRejectsInvalidNames(t *testing.T) {
	for _, raw := range []string{"", ".example.com", "a..example.com", "*.com", "*.*.example.com", "127.0.0.1", "münich.example", "bad_name.example", "example.com.."} {
		if _, _, err := normalizeBindingHost(raw); err == nil {
			t.Errorf("normalizeBindingHost(%q) error=nil", raw)
		}
	}
}
```

Also generate 64-byte labels and 254-byte canonical names and assert rejection at the exact bounds.

- [ ] **Step 2: Run the focused test and confirm RED**

Run: `go test ./internal/downstreamtls -run 'TestNormalize' -count=1`

Expected: FAIL because the normalization functions do not exist.

- [ ] **Step 3: Implement allocation-conscious normalization**

Implement one shared byte-scanning canonicalizer. It removes one trailing dot, lowercases ASCII into a caller-owned `[253]byte` scratch buffer, rejects IP literals with `net/netip`, and validates label boundaries without `strings.Split`. `normalizeBindingHost` removes a single leading `*.` before canonicalization, requires at least two suffix labels, and copies the canonical bytes into the string retained by the compiled map. Do not use path matching, regular expressions, unsafe string conversion, or IDNA conversion.

Core validation must have this shape:

```go
func canonicalizeServerName(raw string, scratch *[253]byte) ([]byte, error) {
	if strings.HasSuffix(raw, ".") {
		raw = raw[:len(raw)-1]
	}
	if len(raw) == 0 || len(raw) > len(scratch) {
		return nil, errInvalidServerName
	}
	labelStart := 0
	for index := range raw {
		character := raw[index]
		if character >= 'A' && character <= 'Z' {
			character += 'a' - 'A'
		}
		if character == '.' {
			if index == labelStart || index-labelStart > 63 || scratch[labelStart] == '-' || scratch[index-1] == '-' {
				return nil, errInvalidServerName
			}
			labelStart = index + 1
		} else if !((character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '-') {
			return nil, errInvalidServerName
		}
		scratch[index] = character
	}
	if len(raw)-labelStart == 0 || len(raw)-labelStart > 63 || scratch[labelStart] == '-' || scratch[len(raw)-1] == '-' {
		return nil, errInvalidServerName
	}
	canonical := scratch[:len(raw)]
	if _, err := netip.ParseAddr(string(canonical)); err == nil {
		return nil, errInvalidServerName
	}
	return canonical, nil
}
```

`Selector.Select` must call this function with a stack scratch buffer and use `map[string]` lookups directly as `index[string(canonical)]`. Go optimizes this non-escaping map-key conversion; the benchmark and `AllocsPerRun` assertion in Task 3 are the required guard against regression.

- [ ] **Step 4: Add and run the normalization fuzz invariant**

```go
func FuzzNormalizeBindingHost(f *testing.F) {
	for _, seed := range []string{"api.example.com", "API.EXAMPLE.COM.", "*.example.com", "a..example"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		canonical, wildcard, err := normalizeBindingHost(raw)
		if err != nil {
			return
		}
		if canonical != strings.ToLower(canonical) || strings.HasSuffix(canonical, ".") {
			t.Fatalf("non-canonical result %q", canonical)
		}
		if wildcard && strings.Count(canonical, ".") < 1 {
			t.Fatalf("wildcard suffix too broad: %q", canonical)
		}
	})
}
```

Run: `gofmt -w internal/downstreamtls/hostname.go internal/downstreamtls/hostname_test.go internal/downstreamtls/hostname_fuzz_test.go`

Run: `go test ./internal/downstreamtls -count=1`

Expected: PASS.

- [ ] **Step 5: Commit hostname semantics**

```bash
git add internal/downstreamtls/hostname.go internal/downstreamtls/hostname_test.go internal/downstreamtls/hostname_fuzz_test.go
git commit -m "feat: normalize downstream sni hosts"
```

### Task 3: Compile and Select Immutable Certificates

**Files:**
- Modify: `internal/downstreamtls/provider.go`
- Create: `internal/downstreamtls/selector.go`
- Create: `internal/downstreamtls/selector_test.go`
- Create: `internal/downstreamtls/selector_fuzz_test.go`
- Create: `internal/downstreamtls/selector_benchmark_test.go`
- Test: `internal/tlsmaterial/material_test.go`

**Interfaces:**
- Consumes: `*model.DownstreamTLSPolicy`, `[]*tlsmaterial.Certificate`, and an explicit build time.
- Produces: `Compile(*model.DownstreamTLSPolicy, []*tlsmaterial.Certificate, time.Time) (*Selector, error)`, `Selector.Select(*tls.ClientHelloInfo) (*tls.Certificate, Selection, error)`, stable `ConfigError`, `Stats`, and selection constants.

- [ ] **Step 1: Write failing selector success tests**

Generate temporary in-memory certificates with DNS SANs `default.example`, `api.example.com`, and `*.example.com`. Cover default, invalid ClientHello SNI, exact-over-wildcard, wildcard, trailing dot, and case folding:

```go
func TestSelectorUsesExactBeforeWildcardAndFallsBackToDefault(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	certificates := testCertificates(t, now)
	selector, err := Compile(&model.DownstreamTLSPolicy{
		DefaultCertificateRef: "default",
		SNIBindings: []model.SNIBinding{
			{CertificateRef: "wild", Hosts: []string{"*.example.com"}},
			{CertificateRef: "exact", Hosts: []string{"api.example.com"}},
		},
	}, certificates, now)
	if err != nil {
		t.Fatal(err)
	}
	assertSelection(t, selector, "API.EXAMPLE.COM.", SelectionExact, 3)
	assertSelection(t, selector, "shop.example.com", SelectionWildcard, 2)
	assertSelection(t, selector, "a.b.example.com", SelectionDefault, 1)
	assertSelection(t, selector, "127.0.0.1", SelectionDefault, 1)
}
```

- [ ] **Step 2: Write failing selector rejection tests**

Use table cases for nil policy, missing default, missing binding reference, empty hosts, exact conflict after case/trailing-dot normalization, wildcard conflict, invalid wildcard shape, exact SAN mismatch, wildcard SAN mismatch, Common-Name-only certificate, not-yet-valid leaf, expired leaf, nil material, duplicate material IDs, and 10,001 aggregate hosts. Assert exact codes from this closed set:

```go
const (
	CodeDownstreamTLSRequired       = "DOWNSTREAM_TLS_REQUIRED"
	CodeDefaultCertificateNotFound = "DEFAULT_CERTIFICATE_NOT_FOUND"
	CodeSNIBindingInvalid          = "SNI_BINDING_INVALID"
	CodeSNIBindingConflict         = "SNI_BINDING_CONFLICT"
	CodeCertificateHostnameMismatch = "CERTIFICATE_HOSTNAME_MISMATCH"
	CodeCertificateNotYetValid     = "CERTIFICATE_NOT_YET_VALID"
	CodeCertificateExpired         = "CERTIFICATE_EXPIRED"
	CodeSNIIndexLimitExceeded      = "SNI_INDEX_LIMIT_EXCEEDED"
)
```

- [ ] **Step 3: Run the selector tests and confirm RED**

Run: `go test ./internal/downstreamtls -run 'TestSelector|TestCompile' -count=1`

Expected: FAIL because the selector compiler and selection types do not exist.

- [ ] **Step 4: Implement compiler, statistics, and selection**

Define:

```go
const MaxSNINames = 10_000

type Selection string

const (
	SelectionExact    Selection = "exact"
	SelectionWildcard Selection = "wildcard"
	SelectionDefault  Selection = "default"
	SelectionError    Selection = "error"
)

type Stats struct {
	CertificateCount int
	ExactCount       int
	WildcardCount    int
	EarliestExpiry   time.Time
}

type ConfigError struct {
	Code          string
	ResourceID    string
	Field         string
	Cause         error
}

type Selector struct {
	defaultCertificate *tls.Certificate
	exact              map[string]*tls.Certificate
	wildcard           map[string]*tls.Certificate
	stats              Stats
}

type SelectingCertificateProvider interface {
	CertificateProvider
	Select(*tls.ClientHelloInfo) (*tls.Certificate, Selection, error)
}
```

Index certificates by ID, clone each selected `tls.Certificate` exactly once with `TLSCertificate`, validate `Leaf`, validity, and SANs before inserting, and deduplicate `CertificateCount` by referenced certificate ID. For exact SAN validation call `Leaf.VerifyHostname(canonical)`. For wildcard bindings inspect `Leaf.DNSNames`, canonicalize SAN wildcard values, and require the same wildcard suffix.

Selection must use normalized ClientHello SNI, exact lookup, then exactly one first-label removal for wildcard lookup, then default. Invalid ClientHello names return default without an error. Nil/empty selector returns `SelectionError` and `errCertificateUnavailable`.

- [ ] **Step 5: Preserve the provider seam**

Keep `CertificateProvider.GetCertificate(*tls.ClientHelloInfo)` unchanged. Make `Selector.GetCertificate` delegate to `Select`, and make `StaticProvider` expose `Select` returning `SelectionDefault`. This lets runtime observe fixed selection classes without changing `tls.Config.GetCertificate` compatibility.

- [ ] **Step 6: Add selector fuzz and benchmarks**

The fuzz target constructs a bounded policy from generated exact/wildcard strings, accepts either a stable `ConfigError` or a selector, and calls `Select` for the fuzzed SNI without panic.

Benchmarks must be named:

```go
func BenchmarkSelectorDefault10K(b *testing.B)
func BenchmarkSelectorExact10K(b *testing.B)
func BenchmarkSelectorWildcard10K(b *testing.B)
func BenchmarkCompileSelector10K(b *testing.B)
```

Call `b.ReportAllocs()` and fail the three lookup benchmarks during implementation if `testing.AllocsPerRun(1000, lookup)` is nonzero.

- [ ] **Step 7: Run package verification**

Run: `gofmt -w internal/downstreamtls internal/tlsmaterial/material_test.go`

Run: `go test ./internal/downstreamtls ./internal/tlsmaterial -count=1`

Run: `go test ./internal/downstreamtls -run '^$' -bench 'BenchmarkSelector(Default|Exact|Wildcard)10K' -benchmem -count=3`

Expected: tests PASS; lookup benchmarks report `0 allocs/op`.

- [ ] **Step 8: Commit the selector**

```bash
git add internal/downstreamtls internal/tlsmaterial/material_test.go
git commit -m "feat: compile downstream sni selector"
```

### Task 4: Decode Strict v1alpha7 Configuration

**Files:**
- Create: `internal/config/wire_v1alpha7.go`
- Modify: `internal/config/load.go`
- Modify: `internal/config/validate.go`
- Modify: `internal/config/load_test.go`
- Create: `configs/phase3c2.yaml`

**Interfaces:**
- Consumes: v1alpha6 documents and existing certificate file loading.
- Produces: `apiVersionV1Alpha7 = "gateway/v1alpha7"`, strict wire structs, and populated `ResourceSet.DownstreamTLS`.

- [ ] **Step 1: Write failing v1alpha7 decode tests**

Add table-driven tests that decode a generated valid document and reject missing `downstream_tls`, empty default ref, empty binding ref, empty host list, and unknown downstream fields. The success assertion must be:

```go
if resources.DownstreamTLS == nil ||
	resources.DownstreamTLS.DefaultCertificateRef != "default-server" ||
	len(resources.DownstreamTLS.SNIBindings) != 1 ||
	resources.DownstreamTLS.SNIBindings[0].Hosts[0] != "*.example.com" {
	t.Fatalf("downstream TLS policy=%+v", resources.DownstreamTLS)
}
```

Also add `TestLegacyVersionsLeaveDownstreamTLSPolicyAbsent` for representative v1alpha5 and v1alpha6 documents.

- [ ] **Step 2: Run decoder tests and confirm RED**

Run: `go test ./internal/config -run 'TestDecodeV1Alpha7|TestLegacyVersionsLeaveDownstream' -count=1`

Expected: FAIL with unsupported `gateway/v1alpha7`.

- [ ] **Step 3: Add strict wire conversion**

Define wire types:

```go
type downstreamTLSDocumentV7 struct {
	DefaultCertificateRef string                 `yaml:"default_certificate_ref"`
	SNIBindings           []sniBindingDocumentV7 `yaml:"sni_bindings"`
}

type sniBindingDocumentV7 struct {
	CertificateRef string   `yaml:"certificate_ref"`
	Hosts          []string `yaml:"hosts"`
}
```

`documentV7` repeats the v1alpha6 top-level fields and adds `DownstreamTLS downstreamTLSDocumentV7`. `convertV7` delegates common conversion to `convertV6`, then allocates and owns the canonical policy slices. Do not normalize hostnames in config conversion; runtime compilation is the single canonical semantic validator.

Add the v1alpha7 switch case in `Decode` and `validateV7` that checks the version and required shape before delegating legacy bootstrap/resource validation.

- [ ] **Step 4: Add the Phase 3C2 example and load test**

Create `configs/phase3c2.yaml` from `configs/phase3c3.yaml`, change `api_version` to `gateway/v1alpha7`, retain WebSocket examples, and add certificate resources plus the exact `downstream_tls` shape from the spec. Use deployment-style `/certs` and `/secrets` paths; do not add real certificate files.

Add `TestPhase3C2ExampleConfigurationHasDownstreamTLSShape` that reads the example text, substitutes temporary generated file paths using the established example-test helper, decodes it, and asserts the default plus exact/wildcard bindings.

- [ ] **Step 5: Run config verification**

Run: `gofmt -w internal/config/wire_v1alpha7.go internal/config/load.go internal/config/validate.go internal/config/load_test.go`

Run: `go test ./internal/config -count=1`

Expected: PASS, including legacy decoder cases.

- [ ] **Step 6: Commit the wire contract**

```bash
git add internal/config/wire_v1alpha7.go internal/config/load.go internal/config/validate.go internal/config/load_test.go configs/phase3c2.yaml
git commit -m "feat: decode phase 3c2 downstream tls policy"
```

### Task 5: Publish the Selector Inside the Runtime Snapshot

**Files:**
- Modify: `internal/runtime/builder.go`
- Modify: `internal/runtime/snapshot.go`
- Modify: `internal/runtime/manager.go`
- Modify: `internal/runtime/errors.go`
- Modify: `internal/runtime/builder_test.go`
- Modify: `internal/runtime/manager_test.go`

**Interfaces:**
- Consumes: `downstreamtls.Compile`, the legacy `downstreamtls.CertificateProvider`, and runtime `Observer`.
- Produces: `NewBuilderWithCertificateProvider(*plugin.Registry, downstreamtls.CertificateProvider)`, snapshot-owned provider/statistics, and `Manager.GetCertificate(*tls.ClientHelloInfo)`.

- [ ] **Step 1: Write failing builder transaction tests**

Add tests proving a valid dynamic policy is stored with revision 1, a hostname mismatch returns a validate-stage `BuildError` with `CERTIFICATE_HOSTNAME_MISMATCH`, and an absent policy uses the supplied legacy provider.

The failure assertion must check bounded metadata:

```go
var buildErr *BuildError
if !errors.As(err, &buildErr) || buildErr.Code != downstreamtls.CodeCertificateHostnameMismatch ||
	buildErr.Stage != StageValidate || buildErr.ResourceKind != "certificate" {
	t.Fatalf("Build() error=%+v", err)
}
```

- [ ] **Step 2: Write failing manager atomicity tests**

Add `TestManagerGetCertificateTracksPublishedRevision` and `TestManagerRejectedDownstreamTLSKeepsLastGoodCertificate`. Use leaf serial numbers to distinguish revisions. During a blocked concurrent build, assert selection still returns the old serial; after successful `Apply`, assert it returns the new serial.

- [ ] **Step 3: Run focused runtime tests and confirm RED**

Run: `go test ./internal/runtime -run 'TestBuilder.*Downstream|TestManager.*Certificate' -count=1`

Expected: FAIL because snapshots and the manager do not serve certificates.

- [ ] **Step 4: Extend builder and snapshot state**

Keep `NewBuilder(*plugin.Registry)` source-compatible and add:

```go
func NewBuilderWithCertificateProvider(
	plugins *plugin.Registry,
	legacy downstreamtls.CertificateProvider,
) (*Builder, error)
```

Both constructors initialize the same builder; the existing constructor passes a nil legacy provider for package tests that never serve TLS. In `Build`, compile a present policy at `time.Now()` before router compilation completes. For an absent policy, retain the legacy provider. Map `*downstreamtls.ConfigError` to `BuildError` without copying raw hostnames into observer-visible fields.

Extend `Stats` with `DownstreamCertificateCount`, `DownstreamExactCount`, `DownstreamWildcardCount`, and `DownstreamEarliestExpiry`. Extend `Snapshot` with an unexported `downstreamTLS downstreamtls.CertificateProvider`.

- [ ] **Step 5: Add manager certificate selection and optional observation**

Add an optional observer extension without breaking existing test observers:

```go
type downstreamTLSObserver interface {
	DownstreamTLSSelection(downstreamtls.Selection)
}
```

`Manager.GetCertificate` loads `m.active` once, returns a stable `DOWNSTREAM_TLS_UNAVAILABLE` error if missing, calls `Select` when the provider supports the selecting interface, otherwise calls `GetCertificate`, and isolates observer panics. Notify only one of `exact`, `wildcard`, `default`, or `error`.

- [ ] **Step 6: Run runtime suites and concurrency repetition**

Run: `gofmt -w internal/runtime`

Run: `go test ./internal/runtime -count=1`

Run: `go test ./internal/runtime -run 'TestManager(GetCertificateTracksPublishedRevision|RejectedDownstreamTLSKeepsLastGoodCertificate)' -count=50`

Expected: PASS in every repetition.

- [ ] **Step 7: Commit atomic runtime activation**

```bash
git add internal/runtime
git commit -m "feat: publish downstream tls with runtime snapshots"
```

### Task 6: Wire the Gateway and Bounded Telemetry

**Files:**
- Modify: `internal/gateway/gateway.go`
- Modify: `internal/gateway/gateway_test.go`
- Modify: `internal/gateway/lifecycle_observer.go`
- Modify: `internal/gateway/lifecycle_observer_test.go`
- Modify: `internal/telemetry/telemetry.go`
- Modify: `internal/telemetry/telemetry_test.go`

**Interfaces:**
- Consumes: static bootstrap provider, `NewBuilderWithCertificateProvider`, runtime snapshot statistics, and downstream selection classes.
- Produces: listener callback `manager.GetCertificate`, disabled session tickets, snapshot TLS gauges, and `gateway_downstream_tls_certificate_selections_total{selection=...}`.

- [ ] **Step 1: Write failing gateway wiring tests**

Replace the arbitrary-SNI static-provider assertion with tests that require:

```go
if fixture.gateway.tlsConfig.GetCertificate == nil {
	t.Fatal("TLS config lacks runtime certificate callback")
}
if !fixture.gateway.tlsConfig.SessionTicketsDisabled {
	t.Fatal("TLS session tickets remain enabled")
}
certificate, err := fixture.gateway.tlsConfig.GetCertificate(&tls.ClientHelloInfo{ServerName: "api.example"})
if err != nil || len(certificate.Certificate) == 0 {
	t.Fatalf("GetCertificate()=(%v,%v)", certificate, err)
}
```

Use a v1alpha7 resource fixture for dynamic selection and retain a legacy fixture proving the bootstrap certificate remains available.

- [ ] **Step 2: Write failing telemetry tests**

Call `SnapshotApplied` with fixed TLS stats and call `DownstreamTLSSelection` for all four closed values plus an unknown value. Gather metrics and assert exact series names and that no sample contains a hostname or certificate ID.

Required metrics:

```text
gateway_downstream_tls_active_certificates
gateway_downstream_tls_exact_bindings
gateway_downstream_tls_wildcard_bindings
gateway_downstream_tls_earliest_expiry_seconds
gateway_downstream_tls_certificate_selections_total{selection="exact|wildcard|default|error"}
```

- [ ] **Step 3: Run focused tests and confirm RED**

Run: `go test ./internal/gateway ./internal/telemetry -run 'Test.*(Certificate|SessionTicket|DownstreamTLS)' -count=1`

Expected: FAIL because gateway still points directly at the static provider and metrics do not exist.

- [ ] **Step 4: Wire runtime-owned certificate selection**

Load the bootstrap static provider as today, pass it to `NewBuilderWithCertificateProvider`, apply revision 1, and then set:

```go
tlsConfig := &tls.Config{
	GetCertificate:         manager.GetCertificate,
	MinVersion:             tls.VersionTLS12,
	NextProtos:             []string{"h2", "http/1.1"},
	SessionTicketsDisabled: true,
}
```

Do not retain another dynamic provider pointer in `Gateway`. Preserve the rule that initial Apply finishes before Start binds listeners.

- [ ] **Step 5: Add pre-bound bounded telemetry**

Create three ordinary count gauges, one `prometheus.GaugeFunc` for expiry, and one counter vec in `Telemetry`. Store the active earliest expiry as Unix nanoseconds in an `atomic.Int64`; the gauge function returns zero for no expiry and otherwise returns `max(0, time.Unix(0, stored).Sub(t.now()).Seconds())`. `SnapshotApplied` updates the three counts and the stored expiry. Register all collectors with the existing private registry, pre-bind exactly `exact`, `wildcard`, `default`, and `error`, and ignore unknown selection values. Default `t.now` to `time.Now` and replace it with a fixed function in telemetry tests so repeated metric gathers prove the expiry duration decreases without wall-clock flakes.

`lifecycleObserver.DownstreamTLSSelection` forwards to telemetry without emitting a per-handshake log. Extend `runtime_snapshot_applied` log only with aggregate counts and expiry seconds.

- [ ] **Step 6: Run gateway and telemetry tests**

Run: `gofmt -w internal/gateway internal/telemetry`

Run: `go test ./internal/gateway ./internal/telemetry -count=1`

Expected: PASS.

- [ ] **Step 7: Commit listener and telemetry integration**

```bash
git add internal/gateway internal/telemetry
git commit -m "feat: serve and observe rotated downstream certificates"
```

### Task 7: Prove Default, Exact, Wildcard, and Rejected Rotation End to End

**Files:**
- Create: `test/integration/downstream_tls_test.go`
- Modify: `test/integration/gateway_test.go`

**Interfaces:**
- Consumes: process/in-process gateway fixtures, `Gateway.Apply`, and generated certificate resources.
- Produces: protocol-level acceptance for selection, successful rotation, and last-known-good rejection.

- [ ] **Step 1: Add reusable test certificate generation**

Extend the integration helper to accept a serial and DNS SAN list:

```go
type testCertificate struct {
	Material *tlsmaterial.Certificate
	Pool     *x509.CertPool
	Serial   *big.Int
}

func newTestCertificate(t *testing.T, id string, serial int64, dnsNames []string, now time.Time) testCertificate
```

Generate Ed25519 self-signed certificates valid from `now.Add(-time.Hour)` through `now.Add(24*time.Hour)`, create canonical material with `tlsmaterial.NewCertificate`, and add the leaf to a client root pool.

- [ ] **Step 2: Write selection integration tests**

Add `TestDownstreamTLSSelectsDefaultExactAndWildcardCertificates`. For each server name, disable keep-alives, dial a fresh TLS connection, verify against a pool containing all test leaves, and assert the peer leaf serial:

```go
tests := []struct {
	serverName string
	wantSerial int64
}{
	{"unknown.example", 1},
	{"api.example.com", 2},
	{"shop.example.com", 3},
	{"a.b.example.com", 1},
}
```

- [ ] **Step 3: Write rotation and rejection integration tests**

`TestDownstreamTLSRotationAffectsNewConnections` applies revision 2 with a new exact certificate and asserts a fresh connection sees its serial. `TestRejectedDownstreamTLSRotationKeepsLastGood` applies an expired or SAN-mismatched revision, asserts its stable error code, then proves both HTTP traffic and the old certificate remain active.

- [ ] **Step 4: Run integration tests and confirm behavior**

Run: `gofmt -w test/integration/downstream_tls_test.go test/integration/gateway_test.go`

Run: `go test ./test/integration -run 'TestDownstreamTLS(Selects|Rotation|Rejected)' -count=1 -v`

Expected: PASS.

- [ ] **Step 5: Commit end-to-end certificate behavior**

```bash
git add test/integration/downstream_tls_test.go test/integration/gateway_test.go
git commit -m "test: prove downstream sni rotation end to end"
```

### Task 8: Preserve Long-Lived Protocols and Disable Resumption

**Files:**
- Modify: `test/integration/downstream_tls_test.go`
- Modify: `test/integration/websocket_test.go`
- Create: `internal/gateway/phase3c2_lifecycle_test.go`

**Interfaces:**
- Consumes: existing H2/gRPC/WebSocket helpers and gateway lifecycle hooks.
- Produces: continuity, no-resumption, concurrent rotation, and steady-state evidence.

- [ ] **Step 1: Write a failing session-resumption test**

Create a client `tls.Config` with `ClientSessionCache: tls.NewLRUClientSessionCache(8)`, complete two fresh TCP/TLS connections with the same SNI, and assert both connection states have `DidResume == false`. Close the first connection before dialing the second so the test actually attempts ticket reuse.

- [ ] **Step 2: Write established-connection continuity tests**

Add subtests for:

- two requests over one HTTP/1.1 keep-alive connection across Apply;
- an open HTTP/2 or gRPC bidirectional stream that echoes before and after Apply;
- an active WebSocket tunnel that echoes before and after Apply.

Each subtest also opens a new connection after Apply and asserts the new certificate serial. The existing connection must not be force-closed merely because its certificate retired.

- [ ] **Step 3: Add the 100-rotation concurrent lifecycle test**

In `TestPhase3C2ConcurrentHandshakeRotationReturnsToSteadyState`, start bounded handshake workers, alternate two valid certificate sets through revisions 2 through 101, stop workers, close clients, and assert:

```go
if got := fixture.gateway.manager.Load().Revision(); got != 101 {
	t.Fatalf("active revision=%d, want 101", got)
}
stats := fixture.gateway.manager.UpstreamStats()
if stats.RetiredPlanSets != 0 || stats.LiveTunnelLeases != 0 {
	t.Fatalf("registry did not reach steady state: %+v", stats)
}
```

Every handshake may observe either adjacent valid serial during publication but must never observe a certificate outside the two configured sets or return a race-induced internal error.

- [ ] **Step 4: Run focused lifecycle repetition**

Run: `gofmt -w test/integration/downstream_tls_test.go test/integration/websocket_test.go internal/gateway/phase3c2_lifecycle_test.go`

Run: `go test ./test/integration ./internal/gateway -run 'Test.*(SessionResumption|SurvivesCertificateRotation|Phase3C2Concurrent)' -count=1 -v`

Run: `go test ./internal/gateway -run TestPhase3C2ConcurrentHandshakeRotationReturnsToSteadyState -count=20`

Expected: PASS in every repetition.

- [ ] **Step 5: Commit lifecycle guarantees**

```bash
git add test/integration/downstream_tls_test.go test/integration/websocket_test.go internal/gateway/phase3c2_lifecycle_test.go
git commit -m "test: cover downstream certificate lifecycle"
```

### Task 9: Run Fuzz, Performance, and Acceptance Evidence

**Files:**
- Modify: `internal/downstreamtls/selector_fuzz_test.go`
- Modify: `internal/downstreamtls/selector_benchmark_test.go`
- Create: `docs/benchmarks/phase-3c2-current-status.md`

**Interfaces:**
- Consumes: completed selector and lifecycle test suite.
- Produces: reproducible developer-machine evidence and an explicit canonical-evidence status.

- [ ] **Step 1: Establish focused correctness baseline**

Run: `go test ./internal/downstreamtls ./internal/config ./internal/runtime ./internal/gateway ./internal/telemetry ./test/integration -count=1`

Expected: PASS.

- [ ] **Step 2: Run each fuzz target for 30 seconds**

Run: `go test ./internal/downstreamtls -run '^$' -fuzz FuzzNormalizeBindingHost -fuzztime 30s`

Run: `go test ./internal/downstreamtls -run '^$' -fuzz FuzzCompileSelector -fuzztime 30s`

Expected: both complete without panic or invariant failure. Do not commit generated fuzz cache directories.

- [ ] **Step 3: Record lookup and build benchmarks**

Run: `go test ./internal/downstreamtls -run '^$' -bench 'Benchmark(Selector|CompileSelector)10K' -benchmem -count=5`

Expected: all lookup cases report `0 allocs/op`; record OS, architecture, Go version, CPU, command, five samples, and medians. Record compile allocations and time as observations, not portable pass/fail thresholds.

- [ ] **Step 4: Run lifecycle acceptance profiles**

Run: `go test ./internal/gateway ./test/integration -run 'Test.*Phase3C2|TestDownstreamTLS' -count=20`

Expected: PASS with no resource-growth assertion failure.

- [ ] **Step 5: Write the evidence ledger**

Create `docs/benchmarks/phase-3c2-current-status.md` with these explicit headings:

```markdown
# Phase 3C2 Current Status

## Status
Implementation complete; canonical downstream TLS evidence pending.

## Environment
## Correctness and lifecycle evidence
## Fuzz evidence
## Developer-machine benchmarks
## Pending canonical gates
## Claims boundary
```

State that CGO/race or canonical Linux gates are pending when they were not actually run. State that Phase 3D and integrated APISIX comparison remain required.

- [ ] **Step 6: Commit evidence without artifacts**

```bash
git add internal/downstreamtls/selector_fuzz_test.go internal/downstreamtls/selector_benchmark_test.go docs/benchmarks/phase-3c2-current-status.md
git commit -m "test: add phase 3c2 acceptance evidence"
```

### Task 10: Document Operations and Run Final Gates

**Files:**
- Create: `docs/operations/phase-3c2-runbook.md`
- Modify: `README.md`
- Modify: `docs/superpowers/specs/2026-07-21-go-native-api-gateway-phase-roadmap-design.md`
- Modify: `docs/benchmarks/phase-3c2-current-status.md`

**Interfaces:**
- Consumes: completed v1alpha7 behavior and verified evidence.
- Produces: operator guidance, accurate roadmap status, and a clean implementation branch.

- [ ] **Step 1: Write the runbook**

Document exact procedures for validating `configs/phase3c2.yaml`, rotating default/exact/wildcard certificates through the current internal Apply test seam, checking `/readyz` and `/metrics`, verifying a presented leaf with `openssl s_client -servername`, and diagnosing each stable error code.

Include these operational warnings verbatim in meaning:

- deleting or changing a binding does not terminate established connections;
- an invalid update preserves the active revision;
- an active certificate that later expires is not automatically replaced;
- session tickets are disabled, so every new TLS connection performs a full handshake;
- no public reload endpoint exists before Phase 4.

- [ ] **Step 2: Update README and roadmap claims**

Update the current-phase summary, capability list, exclusions, and phase sequence. Mark Phase 3C2 as implementation-complete only if the implementation and local gates have passed. Keep canonical evidence separately qualified. Keep Phase 3D as the owner of bounded access logging and integrated APISIX comparison; do not mark umbrella Phase 3C complete early.

- [ ] **Step 3: Run formatting and diff hygiene**

Run: `gofmt -w` on every changed Go file reported by `git diff --name-only main -- '*.go'`.

Run: `gofmt -l` on the same changed Go files.

Expected: no output from `gofmt -l`.

Run: `git diff --check`

Expected: no output.

- [ ] **Step 4: Run the full repository gates**

Run: `go test -p 1 ./... -count=1`

Run: `go vet ./...`

Run: `staticcheck -tests=false ./...`

Run: `revive -set_exit_status -config revive.toml -formatter default ./...`

Run: `go build ./cmd/...`

Expected: every command exits 0.

- [ ] **Step 5: Run the race gate where supported**

Run: `go test ./... -race -count=1`

Expected on a CGO-capable host: PASS. If the host reports `-race requires cgo`, copy that exact limitation into the evidence ledger and leave the canonical race gate pending; do not claim it passed.

- [ ] **Step 6: Audit documentation links and repository state**

Check every relative Markdown link added or modified by this phase resolves from its containing document. Run `git status --short` and inspect untracked files. Remove only generated caches that are proven to belong to this run; never delete user files or benchmark artifacts without explicit scope confirmation.

- [ ] **Step 7: Commit final documentation**

```bash
git add docs/operations/phase-3c2-runbook.md docs/benchmarks/phase-3c2-current-status.md README.md docs/superpowers/specs/2026-07-21-go-native-api-gateway-phase-roadmap-design.md
git commit -m "docs: record phase 3c2 downstream tls operations"
```

- [ ] **Step 8: Request independent review**

Use `superpowers:requesting-code-review` against the full diff from `main` through HEAD. Resolve only concrete findings through `superpowers:receiving-code-review`, add a regression test for every behavior or race fix, repeat the affected suite, and rerun the final gates after the last code change.

- [ ] **Step 9: Confirm final branch state**

Run: `git status --short --branch`

Run: `git log --oneline --decorate main..HEAD`

Expected: no working-tree entries and focused Conventional Commit history covering Tasks 1 through 10.

## Completion Checklist

- [ ] Strict `gateway/v1alpha7` decodes explicit default, exact, and wildcard bindings.
- [ ] Legacy v1alpha1-v1alpha6 continue using the bootstrap certificate.
- [ ] SAN, validity, conflict, hostname, reference, and 10,000-name limits reject candidates before publication.
- [ ] Runtime routing and downstream TLS share one atomically published snapshot revision.
- [ ] Invalid Apply preserves complete last-known-good state.
- [ ] New connections receive rotated certificates; established HTTP/2, gRPC, and WebSocket traffic survives.
- [ ] TLS session resumption is disabled and tested.
- [ ] Metrics remain bounded and expose no SNI or certificate identity.
- [ ] Selector lookup is O(1) and reports zero allocations at the 10,000-name benchmark.
- [ ] Fuzz, lifecycle, static-analysis, build, and available race gates are recorded truthfully.
- [ ] Phase 3D remains required and no APISIX parity or production-certification claim is made.

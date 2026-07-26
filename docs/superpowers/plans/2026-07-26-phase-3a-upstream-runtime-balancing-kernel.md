# Phase 3A Upstream Runtime and Balancing Kernel Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the fixed single-endpoint upstream table with transactional, lifecycle-aware shared runtimes and immutable weighted-round-robin or consistent-hash plans that can change through `Gateway.Apply` without breaking request snapshot consistency or unrelated connection pools.

**Architecture:** `upstream.Registry` prepares a complete candidate of immutable `Plan` objects backed by reference-counted endpoint, selection, and shared transport runtimes. `runtime.Manager` builds a snapshot against that candidate, atomically publishes it, and retires the prior `PlanSet`; requests acquire one lease and select from the plan held by that snapshot without consulting mutable registry maps.

**Tech Stack:** Go 1.26.5, `net/http`, `sync/atomic`, `net/netip`, Prometheus client_golang, `github.com/cespare/xxhash/v2`, strict YAML v3, Go unit/property/fuzz/race tests, httptest integration tests, Go benchmarks.

## Global Constraints

- Execute on `codex/phase3a-upstream-runtime-balancing-kernel-design`; do not create or use a Git worktree.
- Preserve strict `gateway/v1alpha1` and `gateway/v1alpha2` compatibility while adding strict `gateway/v1alpha3`.
- Keep route, plugin, and upstream-plan configuration immutable for a request's entire snapshot lease.
- Never perform registry map lookup, JSON/YAML parsing, reference resolution, plugin sorting, or locking on the request hot path.
- Support at most 10,000 upstreams, 100,000 endpoints per snapshot, and 1,000 endpoints per upstream.
- Accept endpoint weights `0..1000`; weight `0` is disabled and every upstream must have at least one positive-weight endpoint.
- Cap one WRR schedule at 8,192 slots and all WRR schedules at 8,000,000 slots per snapshot.
- Cap one consistent-hash continuum at 65,536 points and all continua at 8,000,000 points per snapshot.
- Allow at most eight compiled hash-key sources.
- Default `runtime.max_retired_snapshots` to `64`; accept only `1..1024`.
- Phase 3A supports HTTP/1.1 cleartext upstream only and exactly one attempt per request.
- Treat developer-machine and Docker Desktop performance as provisional; do not claim APISIX parity.
- Keep Phase 2 Task 16 visible and deferred; do not implement canonical APISIX E2E in this plan.
- Use TDD for behavior changes and keep each task's focused tests green before its commit.
- Use `apply_patch` for authored file changes; formatting and mechanical LF normalization may use `gofmt`.

## Locked Cross-Task Interfaces

The implementation tasks use these names and signatures consistently:

```go
// internal/model/resources.go
type BalancerType string
type HashSourceType string

const (
	BalancerWeightedRoundRobin BalancerType = "weighted_round_robin"
	BalancerConsistentHash     BalancerType = "consistent_hash"
	HashSourceHeader           HashSourceType = "header"
	HashSourceCookie           HashSourceType = "cookie"
	HashSourceRemoteAddr       HashSourceType = "remote_addr"
	HashSourceLiteral          HashSourceType = "literal"
)

type Endpoint struct {
	URL    string
	Weight uint32
}

type BalancerPolicy struct {
	Type    BalancerType
	HashKey HashKeyPolicy
}

type HashKeyPolicy struct {
	Sources []HashKeySource
}

type HashKeySource struct {
	Type  HashSourceType
	Name  string
	Value string
}
```

The balancing compilers share this private value type, introduced in Task 5:

```go
type weightedEndpoint struct {
	identity string
	weight   uint32
}
```

```go
// internal/upstream public package contracts
type Registry struct{}
type Candidate struct{}
type PlanSet struct{}
type Plan struct{}
type Selection struct{}

func NewRegistry(maxRetiredSnapshots int, observer Observer) (*Registry, error)
func (r *Registry) Prepare(resources []model.Upstream) (*Candidate, error)
func (r *Registry) Stats() RegistryStats
func (r *Registry) Close(ctx context.Context) error

func (c *Candidate) Plan(id string) (*Plan, bool)
func (c *Candidate) Commit() *PlanSet
func (c *Candidate) Rollback()

func (s *PlanSet) Plan(id string) (*Plan, bool)
func (s *PlanSet) TryAcquire() bool
func (s *PlanSet) Release()
func (s *PlanSet) Retire()

func (p *Plan) Select(request *http.Request) (Selection, error)
func (s Selection) Valid() bool
func (s Selection) Target() *url.URL
func (s Selection) RoundTrip(request *http.Request) (*http.Response, error)
func (s Selection) EndpointID() string
func (s Selection) Ordinal() uint32
func (s Selection) Balancer() model.BalancerType
func (s Selection) HashFallback() bool
```

```go
// internal/runtime/manager.go
type Lease struct{}

func NewManager(builder *Builder, upstreams *upstream.Registry, observer Observer) *Manager
func (m *Manager) Acquire() (*Lease, bool)
func (m *Manager) Apply(revision uint64, resources model.ResourceSet) error
func (m *Manager) Close(ctx context.Context) error
func (m *Manager) UpstreamStats() upstream.RegistryStats
func (l *Lease) Snapshot() *Snapshot
func (l *Lease) Release()
```

---

### Task 1: Enforce LF for Go sources and restore the formatting gate

**Files:**
- Create: `.gitattributes`
- Normalize working-tree line endings only: the 14 Go files listed in `docs/benchmarks/phase-2-current-status.md`
- Verify: every tracked `*.go`, `go.mod`, and `go.sum`

**Interfaces:**
- Consumes: the Phase 2 line-ending diagnosis (`core.autocrlf=true`, no repository attributes).
- Produces: deterministic LF checkout semantics for Go files and a clean `gofmt -l .` baseline.

- [ ] **Step 1: Reproduce the formatting failure**

Run:

```powershell
gofmt -l .
```

Expected: the 14 previously recorded files are listed. If the command already prints nothing, continue; the repository attribute is still required to prevent recurrence.

- [ ] **Step 2: Add the repository line-ending policy**

Create `.gitattributes` with exactly:

```gitattributes
*.go text eol=lf
go.mod text eol=lf
go.sum text eol=lf
```

- [ ] **Step 3: Normalize the affected Go working-tree files**

Run:

```powershell
$phase3aGoFiles = @(
  'cmd/bench-report/main.go',
  'cmd/gateway-dp/main.go',
  'cmd/test-upstream/main.go',
  'internal/benchreport/report.go',
  'internal/benchreport/report_test.go',
  'internal/gateway/response_state.go',
  'internal/proxy/errors.go',
  'internal/proxy/headers.go',
  'internal/proxy/headers_test.go',
  'internal/testupstream/server.go',
  'internal/testupstream/server_test.go',
  'internal/upstream/runtime_test.go',
  'test/integration/gateway_test.go',
  'test/integration/tls_test.go'
)
gofmt -w $phase3aGoFiles
```

Expected: source tokens do not change; only working-tree line endings normalize.

- [ ] **Step 4: Verify formatting and semantic cleanliness**

Run:

```powershell
$unformatted = @(gofmt -l .)
if ($unformatted.Count -ne 0) {
  $unformatted
  exit 1
}
git diff --check
go test ./... -count=1
```

Expected: no formatting output, no whitespace errors, and all tests pass.

- [ ] **Step 5: Commit**

```powershell
git add .gitattributes
git commit -m "chore: enforce LF for Go sources"
```

### Task 2: Migrate the canonical model to weighted endpoint objects

**Files:**
- Modify: `internal/model/resources.go`
- Modify: `internal/model/resources_test.go`
- Modify: `internal/config/load.go`
- Modify: `internal/config/load_test.go`
- Modify: `internal/config/validate.go`
- Modify: `internal/upstream/runtime.go`
- Modify: `internal/upstream/runtime_test.go`
- Modify: `internal/upstream/table_test.go`
- Modify: `internal/runtime/builder_test.go`
- Modify: `internal/proxy/handler_test.go`
- Modify: `internal/proxy/runtime_handler_test.go`
- Modify: `internal/gateway/gateway_test.go`
- Modify: `internal/benchdataset/generator.go`
- Modify: `internal/benchdataset/render.go`
- Modify: `internal/benchdataset/render_test.go`
- Modify: `test/integration/gateway_test.go`
- Modify: `test/integration/snapshot_test.go`

**Interfaces:**
- Consumes: the current `model.Upstream{Endpoints: []string}` representation.
- Produces: the locked canonical types in this plan and `model.Upstream{Endpoints []Endpoint, Balancer BalancerPolicy}`.

- [ ] **Step 1: Add a failing deep-clone test**

Add to `internal/model/resources_test.go`:

```go
func TestCloneResourceSetClonesEndpointAndHashSources(t *testing.T) {
	in := ResourceSet{Upstreams: []Upstream{{
		ID: "users",
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
```

- [ ] **Step 2: Run the model test to verify RED**

Run:

```powershell
go test ./internal/model -run TestCloneResourceSetClonesEndpointAndHashSources -count=1
```

Expected: compile failure because `Endpoint`, `BalancerPolicy`, and hash source types do not exist.

- [ ] **Step 3: Add the canonical declarations and clone logic**

In `internal/model/resources.go`, add the locked declarations from this plan, change `Upstream.Endpoints` to `[]Endpoint`, add `Balancer BalancerPolicy`, and clone with:

```go
for i := range in.Upstreams {
	out.Upstreams[i] = in.Upstreams[i]
	out.Upstreams[i].Endpoints = append([]Endpoint(nil), in.Upstreams[i].Endpoints...)
	out.Upstreams[i].Balancer.HashKey.Sources = append(
		[]HashKeySource(nil),
		in.Upstreams[i].Balancer.HashKey.Sources...,
	)
}
```

- [ ] **Step 4: Convert legacy wire endpoints to weight-one endpoints**

In `internal/config/load.go`, replace the string copy with:

```go
endpoints := make([]model.Endpoint, len(upstream.Endpoints))
for endpointIndex, rawURL := range upstream.Endpoints {
	endpoints[endpointIndex] = model.Endpoint{URL: rawURL, Weight: 1}
}
resources.Upstreams = append(resources.Upstreams, model.Upstream{
	ID:        upstream.ID,
	Endpoints: endpoints,
	Balancer: model.BalancerPolicy{
		Type: model.BalancerWeightedRoundRobin,
	},
	Transport: model.TransportConfig{
		DialTimeout:               dialTimeout,
		ResponseHeaderTimeout:     responseHeaderTimeout,
		IdleConnectionTimeout:     idleConnectionTimeout,
		MaxIdleConnections:        upstream.Transport.MaxIdleConnections,
		MaxIdleConnectionsPerHost: upstream.Transport.MaxIdleConnectionsPerHost,
	},
})
```

Update current validation and fixed runtime access from `Endpoints[0]` to `Endpoints[0].URL`; require its migrated weight to equal `1`.

- [ ] **Step 5: Mechanically migrate every Go fixture**

Replace every construction shaped as:

```go
Endpoints: []string{endpoint},
```

with:

```go
Endpoints: []model.Endpoint{{URL: endpoint, Weight: 1}},
Balancer:  model.BalancerPolicy{Type: model.BalancerWeightedRoundRobin},
```

For code already in package `model`, omit the `model.` qualifier. Update endpoint comparisons and APISIX dataset rendering to read `endpoint.URL` and `endpoint.Weight`. Preserve the Phase 2 dataset checksum only after intentionally updating its schema/version expectation; do not silently assert the old checksum against a new canonical JSON shape.

- [ ] **Step 6: Run focused and full tests**

Run:

```powershell
go test ./internal/model ./internal/config ./internal/upstream ./internal/benchdataset -count=1
go test ./... -count=1
```

Expected: all packages pass with unchanged one-endpoint runtime behavior.

- [ ] **Step 7: Commit**

```powershell
git add internal/model internal/config internal/upstream internal/runtime internal/proxy internal/gateway internal/benchdataset test/integration
git commit -m "refactor: model weighted upstream endpoints"
```

### Task 3: Add canonical upstream normalization and bounded validation

**Files:**
- Create: `internal/upstream/config.go`
- Create: `internal/upstream/config_test.go`
- Create: `internal/upstream/config_fuzz_test.go`
- Modify: `internal/config/validate.go`
- Modify: `internal/runtime/validate.go`

**Interfaces:**
- Consumes: canonical endpoint and balancer model types from Task 2.
- Produces: `upstream.Normalize([]model.Upstream) ([]model.Upstream, error)` and typed `*upstream.ConfigError` with stable `Code` and `Field`.

- [ ] **Step 1: Write table tests for normalization and stable errors**

Create `internal/upstream/config_test.go` with cases equivalent to:

```go
func TestNormalizeCanonicalizesEndpointIdentity(t *testing.T) {
	got, err := Normalize([]model.Upstream{{
		ID: "users",
		Endpoints: []model.Endpoint{{
			URL:    "http://EXAMPLE.COM.:80/",
			Weight: 1,
		}},
		Balancer: validWRRPolicy(),
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
		Balancer: validWRRPolicy(),
		Transport: validTransportConfig(),
	}})
	assertConfigError(t, err, "UPSTREAM_ENDPOINT_DUPLICATE", "upstreams[0].endpoints[1].url")
}
```

Cover empty endpoints, no active endpoint, invalid scheme/user/query/fragment/path/host/port, non-ASCII DNS, per-upstream and snapshot endpoint limits, invalid weights, hash source count, wrong hash policy for an algorithm, and invalid transport fields.

- [ ] **Step 2: Run the normalization tests to verify RED**

Run:

```powershell
go test ./internal/upstream -run 'TestNormalize' -count=1
```

Expected: compile failure because `Normalize` and `ConfigError` do not exist.

- [ ] **Step 3: Implement the typed error and constants**

Add:

```go
const (
	MaxUpstreams          = 10_000
	MaxSnapshotEndpoints  = 100_000
	MaxUpstreamEndpoints  = 1_000
	MaxEndpointWeight     = 1_000
	MaxHashKeySources     = 8
	MaxWRRSchedule        = 8_192
	MaxSnapshotWRRSlots   = 8_000_000
	MaxContinuumPoints    = 65_536
	MaxSnapshotHashPoints = 8_000_000
)

type ConfigError struct {
	Code       string
	Field      string
	UpstreamID string
	Cause      error
}

func (e *ConfigError) Error() string {
	if e.Cause == nil {
		return e.Code + ": " + e.Field
	}
	return e.Code + ": " + e.Field + ": " + e.Cause.Error()
}

func (e *ConfigError) Unwrap() error {
	return e.Cause
}
```

- [ ] **Step 4: Implement endpoint normalization**

Implement `normalizeEndpoint` using `url.Parse`, `netip.ParseAddr`, `net.JoinHostPort`, ASCII DNS validation, lowercase/trailing-dot rules, default port `80`, and path canonicalization. Return a canonical URL string and identity:

```go
type endpointConfig struct {
	endpoint model.Endpoint
	identity string
}

func endpointIdentity(upstreamID, canonicalURL string) string {
	return upstreamID + "\x00" + canonicalURL
}
```

Validate the input index before sorting, then sort normalized endpoints by identity so config list order cannot change balancing tie-breaks.

- [ ] **Step 5: Implement resource and policy validation**

Implement:

```go
func Normalize(resources []model.Upstream) ([]model.Upstream, error)
```

It must clone input, validate the global envelope, default an empty balancer type to `weighted_round_robin`, canonicalize header names with `http.CanonicalHeaderKey`, validate cookie/header tokens, require `Name` only for header/cookie, require `Value` only for literal, and return the stable codes specified by the design.

- [ ] **Step 6: Use normalization from config and runtime validation**

In `internal/config/validate.go`, retain the existing exactly-one-endpoint rule for both v1alpha1 and v1alpha2, then call `upstream.Normalize` for common upstream validation and copy normalized values back into the resource set. Multiple endpoints are exposed only by v1alpha3.

In `internal/runtime/validate.go`, remove the fixed-table immutability check and leave route/service/plugin reference validation. Registry preparation will perform canonical upstream validation for every internal `Apply`.

- [ ] **Step 7: Add fuzz seeds and run focused tests**

Create fuzz targets:

```go
func FuzzNormalizeEndpoint(f *testing.F) {
	for _, seed := range []string{
		"http://example.com",
		"http://127.0.0.1:8080/",
		"http://[::1]:80",
		"http://user@example.com",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		resources := []model.Upstream{validUpstream(raw)}
		_, _ = Normalize(resources)
	})
}
```

Run:

```powershell
go test ./internal/upstream ./internal/config ./internal/runtime -count=1
```

Expected: all focused suites pass.

- [ ] **Step 8: Commit**

```powershell
git add internal/upstream/config.go internal/upstream/config_test.go internal/upstream/config_fuzz_test.go internal/config/validate.go internal/runtime/validate.go
git commit -m "feat: validate canonical upstream resources"
```

### Task 4: Add strict `gateway/v1alpha3` configuration

**Files:**
- Create: `internal/config/wire_v1alpha3.go`
- Modify: `internal/config/types.go`
- Modify: `internal/config/load.go`
- Modify: `internal/config/validate.go`
- Modify: `internal/config/load_test.go`
- Create: `configs/phase3a.yaml`

**Interfaces:**
- Consumes: canonical model and `upstream.Normalize`.
- Produces: `apiVersionV1Alpha3`, `config.RuntimeConfig`, strict v1alpha3 wire conversion, and default retired-snapshot configuration.

- [ ] **Step 1: Write failing v1alpha3 decode tests**

Add tests that decode two weighted endpoints and a compound hash key:

```go
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
	if len(upstream.Endpoints) != 2 || upstream.Endpoints[0].Weight == 0 {
		t.Fatalf("endpoints = %+v", upstream.Endpoints)
	}
}
```

Also assert explicit weight `0`, omitted weight default `1`, unknown-field rejection, missing hash key, `runtime.max_retired_snapshots` values `0` and `1025`, and unchanged v1alpha1/v1alpha2 decode behavior.

- [ ] **Step 2: Run the new config tests to verify RED**

Run:

```powershell
go test ./internal/config -run 'V1Alpha3|RetiredSnapshots' -count=1
```

Expected: failure because v1alpha3 is unsupported.

- [ ] **Step 3: Add bootstrap runtime configuration**

In `internal/config/types.go`, add:

```go
const DefaultMaxRetiredSnapshots = 64

type RuntimeConfig struct {
	MaxRetiredSnapshots int
}
```

Add `Runtime RuntimeConfig` to `BootstrapConfig`. Ensure v1alpha1/v1alpha2 conversion always sets `DefaultMaxRetiredSnapshots`.

- [ ] **Step 4: Add exact v1alpha3 wire types**

Define in `wire_v1alpha3.go`:

```go
type endpointDocumentV3 struct {
	URL    string  `yaml:"url"`
	Weight *uint32 `yaml:"weight"`
}

type hashKeySourceDocumentV3 struct {
	Type  string `yaml:"type"`
	Name  string `yaml:"name"`
	Value string `yaml:"value"`
}

type balancerDocumentV3 struct {
	Type string `yaml:"type"`
	HashKey struct {
		Sources []hashKeySourceDocumentV3 `yaml:"sources"`
	} `yaml:"hash_key"`
}

type runtimeDocumentV3 struct {
	MaxRetiredSnapshots *int `yaml:"max_retired_snapshots"`
}
```

`documentV3` reuses the v1alpha2 route, service, plugin, listener, server, telemetry, and transport wire types; it uses v3 upstream and runtime documents. The pointer distinguishes an omitted value, which defaults to `64`, from an explicit `0`, which validation rejects.

- [ ] **Step 5: Implement conversion and validation**

Implement `convertV3(documentV3)` so nil endpoint weights become `1`, explicit zero remains zero, balancer/hash sources map to canonical enums, empty retired limit defaults to `64`, and `upstream.Normalize` canonicalizes the result.

Add `apiVersionV1Alpha3 = "gateway/v1alpha3"`, a `Decode` switch branch, and `validateV3`. Validate the bootstrap limit with:

```go
if got := bootstrap.Runtime.MaxRetiredSnapshots; got < 1 || got > 1024 {
	return fmt.Errorf("runtime.max_retired_snapshots: must be between 1 and 1024")
}
```

- [ ] **Step 6: Add a runnable Phase 3A example**

Create `configs/phase3a.yaml` with two `http://upstream-a:8080` and `http://upstream-b:8080` endpoints, weights `5` and `2`, WRR as the default example, current listeners/plugins, and `runtime.max_retired_snapshots: 64`.

- [ ] **Step 7: Run config compatibility tests**

Run:

```powershell
go test ./internal/config -count=1
go test ./... -count=1
```

Expected: v1alpha1, v1alpha2, and v1alpha3 tests pass.

- [ ] **Step 8: Commit**

```powershell
git add internal/config configs/phase3a.yaml
git commit -m "feat: add gateway v1alpha3 upstream config"
```

### Task 5: Implement deterministic weighted round-robin

**Files:**
- Create: `internal/upstream/wrr.go`
- Create: `internal/upstream/wrr_test.go`
- Create: `internal/upstream/wrr_benchmark_test.go`

**Interfaces:**
- Consumes: normalized positive endpoint weights and the 8,192-slot cap.
- Produces: `compileWRR([]weightedEndpoint, *selectionState) (wrrSelector, error)` and `wrrSelector.selectIndex() uint32`.

- [ ] **Step 1: Write failing distribution tests**

Create tests for exact `5:2:1`, deterministic endpoint-order ties, a single-endpoint fast path, capped weights, and zero exclusion:

```go
func TestWRRExactDistribution(t *testing.T) {
	selector, err := compileWRR(
		testWeightedEndpoints(5, 2, 1),
		&selectionState{},
	)
	if err != nil {
		t.Fatal(err)
	}
	counts := make([]int, 3)
	for range 8 {
		counts[selector.selectIndex()]++
	}
	if !slices.Equal(counts, []int{5, 2, 1}) {
		t.Fatalf("distribution = %v, want [5 2 1]", counts)
	}
}
```

- [ ] **Step 2: Run WRR tests to verify RED**

Run:

```powershell
go test ./internal/upstream -run WRR -count=1
```

Expected: compile failure because WRR types do not exist.

- [ ] **Step 3: Implement weight normalization and capped apportionment**

Add:

```go
type selectionState struct {
	sequence atomic.Uint64
}

type weightedEndpoint struct {
	identity string
	weight   uint32
}

type wrrSelector struct {
	state    *selectionState
	schedule []uint32
	direct   uint32
}
```

Divide by GCD. For a normalized sum over `MaxWRRSchedule`, reserve one slot per endpoint and distribute remaining slots by Hamilton largest remainder. Compare remainders by cross multiplication to avoid floating-point drift; break ties by endpoint identity.

- [ ] **Step 4: Implement deterministic offline interleaving**

Build a min-heap of entries keyed by the next rational deadline `(emitted+1)/assignedSlots`. Emit one endpoint index, advance its emitted count, and reinsert it until the schedule is full. Compare rational deadlines by integer cross multiplication and use identity as the final tie-break.

Selection must be:

```go
func (s *wrrSelector) selectIndex() uint32 {
	if len(s.schedule) == 0 {
		return s.direct
	}
	next := s.state.sequence.Add(1) - 1
	return s.schedule[next%uint64(len(s.schedule))]
}
```

Represent the single-endpoint fast path with `schedule == nil`.

- [ ] **Step 5: Add allocation and scale benchmarks**

Benchmark 1, 100, and 1,000 endpoints as sub-benchmarks, report allocations, and keep selector construction outside the timed loop:

```go
func BenchmarkWRRSelect(b *testing.B) {
	for _, endpointCount := range []int{1, 100, 1000} {
		b.Run(strconv.Itoa(endpointCount), func(b *testing.B) {
			selector := mustBenchmarkWRR(b, endpointCount)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				benchmarkIndex = selector.selectIndex()
			}
		})
	}
}
```

- [ ] **Step 6: Run tests and benchmark**

Run:

```powershell
go test ./internal/upstream -run WRR -count=1
go test ./internal/upstream -run '^$' -bench WRRSelect -benchmem -count=3
```

Expected: tests pass and selection reports `0 B/op`, `0 allocs/op`.

- [ ] **Step 7: Commit**

```powershell
git add internal/upstream/wrr.go internal/upstream/wrr_test.go internal/upstream/wrr_benchmark_test.go
git commit -m "feat: add deterministic weighted round robin"
```

### Task 6: Compile zero-allocation consistent-hash keys

**Files:**
- Modify: `go.mod`
- Modify: `go.sum`
- Create: `internal/upstream/hashkey.go`
- Create: `internal/upstream/hashkey_test.go`
- Create: `internal/upstream/hashkey_fuzz_test.go`
- Create: `internal/upstream/hashkey_benchmark_test.go`

**Interfaces:**
- Consumes: normalized `model.HashKeyPolicy`.
- Produces: `compileHashKey(model.HashKeyPolicy) (hashKeyExtractor, error)` and `hashKeyExtractor.sum64(*http.Request) (uint64, bool)`.

- [ ] **Step 1: Promote xxHash and write failing key tests**

Run:

```powershell
go get github.com/cespare/xxhash/v2@v2.3.0
```

Add tests proving source-order sensitivity, length-prefix collision resistance, all header values in slice order, first valid cookie selection, compound missing markers, immediate-peer normalization, and fallback only when all dynamic sources are absent.

Use this collision regression:

```go
func TestHashKeyLengthPrefixesComponents(t *testing.T) {
	first := requestHash(t,
		[]model.HashKeySource{
			{Type: model.HashSourceLiteral, Value: "ab"},
			{Type: model.HashSourceLiteral, Value: "c"},
		},
	)
	second := requestHash(t,
		[]model.HashKeySource{
			{Type: model.HashSourceLiteral, Value: "a"},
			{Type: model.HashSourceLiteral, Value: "bc"},
		},
	)
	if first == second {
		t.Fatal("length framing collision")
	}
}
```

- [ ] **Step 2: Run hash-key tests to verify RED**

Run:

```powershell
go test ./internal/upstream -run HashKey -count=1
```

Expected: compile failure because the extractor does not exist.

- [ ] **Step 3: Implement compiled source metadata**

Add:

```go
type compiledHashSource struct {
	kind  model.HashSourceType
	name  string
	value string
}

type hashKeyExtractor struct {
	sources []compiledHashSource
}
```

`compileHashKey` copies source strings and rejects invalid combinations even if a caller bypasses wire decoding.

- [ ] **Step 4: Implement streaming framed hashing**

Use a stack-local `xxhash.Digest`, fixed `[10]byte` varint buffer, and direct writes. Encode source type, presence marker, component count, and byte length before bytes. Do not concatenate a key string.

Implement cookie lookup by scanning each raw `Cookie` header segment without `Request.Cookie`, returning the first valid exact-name value. Implement peer extraction from `Request.RemoteAddr` without reading forwarding headers.

Return `(sum, true)` only when fallback to peer address occurs. A literal or one present dynamic component prevents compound fallback and preserves missing markers.

- [ ] **Step 5: Add fuzz and allocation benchmarks**

Fuzz raw Cookie and RemoteAddr strings while keeping a compiled extractor. Bench header, cookie, compound, and fallback cases with `b.ReportAllocs()`.

- [ ] **Step 6: Run tests and benchmarks**

Run:

```powershell
go test ./internal/upstream -run 'HashKey|FuzzHashKey' -count=1
go test ./internal/upstream -run '^$' -bench HashKey -benchmem -count=3
```

Expected: deterministic tests pass and all benchmark cases report zero allocations.

- [ ] **Step 7: Commit**

```powershell
git add go.mod go.sum internal/upstream/hashkey.go internal/upstream/hashkey_test.go internal/upstream/hashkey_fuzz_test.go internal/upstream/hashkey_benchmark_test.go
git commit -m "feat: compile consistent hash request keys"
```

### Task 7: Implement the bounded Ketama-style continuum

**Files:**
- Create: `internal/upstream/chash.go`
- Create: `internal/upstream/chash_test.go`
- Create: `internal/upstream/chash_benchmark_test.go`

**Interfaces:**
- Consumes: `hashKeyExtractor`, normalized plan endpoints, and continuum caps.
- Produces: `compileContinuum([]weightedEndpoint) (continuum, error)` and `continuum.selectIndex(uint64) uint32`.

- [ ] **Step 1: Write failing continuum tests**

Cover stable recompilation, framed point encoding, sorted point hashes, collision tie-breaking, one-endpoint fast path, weighted distribution over one million deterministic keys, and remap ratio after adding/removing one equal-weight endpoint.

```go
func TestContinuumIsStableAcrossCompiles(t *testing.T) {
	endpoints := testWeightedEndpoints(1, 2, 3)
	first, err := compileContinuum(endpoints)
	if err != nil {
		t.Fatal(err)
	}
	second, err := compileContinuum(endpoints)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"tenant-a", "tenant-b", "tenant-c"} {
		sum := xxhash.Sum64String(key)
		if first.selectIndex(sum) != second.selectIndex(sum) {
			t.Fatalf("selection changed for %q", key)
		}
	}
}
```

Add `TestContinuumPointEncoding` with an explicit framed byte slice and `xxhash.Sum64` expected hash so endpoint/virtual-index framing cannot change silently.

- [ ] **Step 2: Run continuum tests to verify RED**

Run:

```powershell
go test ./internal/upstream -run Continuum -count=1
```

Expected: compile failure because continuum types do not exist.

- [ ] **Step 3: Implement weighted point allocation**

Add:

```go
type continuum struct {
	hashes  []uint64
	indexes []uint32
	direct  uint32
}
```

Normalize by GCD, target 64 points per normalized weight unit, cap at 65,536, and apply Hamilton largest remainder with at least one point per active endpoint. Hash a framed endpoint identity and big-endian virtual index.

- [ ] **Step 4: Implement sorting and lookup**

Sort a temporary point array by hash, endpoint identity, then virtual index. Copy hashes and indexes into parallel slices. Lookup with `sort.Search` and wrap:

```go
func (c *continuum) selectIndex(sum uint64) uint32 {
	if len(c.hashes) == 0 {
		return c.direct
	}
	index := sort.Search(len(c.hashes), func(i int) bool {
		return c.hashes[i] >= sum
	})
	if index == len(c.hashes) {
		index = 0
	}
	return c.indexes[index]
}
```

- [ ] **Step 5: Add scale and allocation benchmarks**

Benchmark 1, 100, and 1,000 endpoints under `BenchmarkConsistentHashSelect/{1,100,1000}` with precomputed request hashes and report allocations.

- [ ] **Step 6: Run tests and benchmarks**

Run:

```powershell
go test ./internal/upstream -run 'Continuum|ConsistentHashDistribution|ConsistentHashRemap' -count=1
go test ./internal/upstream -run '^$' -bench ConsistentHashSelect -benchmem -count=3
```

Expected: correctness tests pass and lookup reports zero allocations.

- [ ] **Step 7: Commit**

```powershell
git add internal/upstream/chash.go internal/upstream/chash_test.go internal/upstream/chash_benchmark_test.go
git commit -m "feat: add bounded consistent hash continuum"
```

### Task 8: Split shared transport runtime from endpoint runtime

**Files:**
- Create: `internal/upstream/transport.go`
- Create: `internal/upstream/transport_test.go`
- Create: `internal/upstream/endpoint.go`
- Create: `internal/upstream/endpoint_test.go`
- Modify: `internal/upstream/runtime.go`
- Modify: `internal/upstream/runtime_test.go`

**Interfaces:**
- Consumes: normalized endpoint URL and `model.TransportConfig`.
- Produces: comparable `transportKey`, `transportRuntime`, and stable `endpointRuntime` primitives used by the registry.

- [ ] **Step 1: Write failing transport identity tests**

Test that identical complete profiles compare equal, every transport field changes the key, endpoint identity ignores weight, and two upstream IDs pointing at one URL have different endpoint identities.

```go
func TestTransportKeyIncludesEveryPoolSemantic(t *testing.T) {
	base := validTransportConfig()
	key := makeTransportKey(base)
	changed := base
	changed.ResponseHeaderTimeout++
	if key == makeTransportKey(changed) {
		t.Fatal("response-header timeout missing from transport key")
	}
}
```

- [ ] **Step 2: Run primitive tests to verify RED**

Run:

```powershell
go test ./internal/upstream -run 'TransportKey|EndpointIdentity' -count=1
```

Expected: compile failure because the primitive types do not exist.

- [ ] **Step 3: Implement the comparable transport key**

Add:

```go
type transportKey struct {
	dialTimeout               time.Duration
	responseHeaderTimeout     time.Duration
	idleConnectionTimeout     time.Duration
	maxIdleConnections        int
	maxIdleConnectionsPerHost int
	disableCompression        bool
	http1Only                bool
}
```

`makeTransportKey` sets `disableCompression=true` and `http1Only=true` explicitly.

- [ ] **Step 4: Implement transport and endpoint runtimes**

`transportRuntime` owns one `*http.Transport`, exposes `RoundTrip` and idempotent `CloseIdleConnections`, and never owns a target URL.

`endpointRuntime` contains:

```go
type endpointRuntime struct {
	identity string
	target   *url.URL
}
```

Parse/copy the normalized target once. Return the stored target only to internal selection code; no public caller may mutate it.

- [ ] **Step 5: Adapt the temporary fixed `Runtime` wrapper**

Until Task 11 removes the fixed table, make existing `Runtime` compose one endpoint runtime and one transport runtime. Preserve `Target`, `RoundTripper`, and `CloseIdleConnections` behavior so the full suite stays green.

- [ ] **Step 6: Run transport correctness tests**

Run:

```powershell
go test ./internal/upstream -count=1
go test ./... -count=1
```

Expected: existing pooling, timeout, cancellation, and close tests still pass.

- [ ] **Step 7: Commit**

```powershell
git add internal/upstream/transport.go internal/upstream/transport_test.go internal/upstream/endpoint.go internal/upstream/endpoint_test.go internal/upstream/runtime.go internal/upstream/runtime_test.go
git commit -m "refactor: separate upstream transport and endpoint runtimes"
```

### Task 9: Build transactional registry candidates and immutable plans

**Files:**
- Create: `internal/upstream/plan.go`
- Create: `internal/upstream/plan_test.go`
- Create: `internal/upstream/registry.go`
- Create: `internal/upstream/registry_test.go`
- Create: `internal/upstream/observer.go`

**Interfaces:**
- Consumes: normalization, WRR, hash-key, continuum, endpoint runtime, and transport runtime from Tasks 3–8.
- Produces: the locked `Registry`, `Candidate`, `PlanSet`, `Plan`, `Selection`, `RegistryStats`, and observer contracts.

- [ ] **Step 1: Write failing plan-selection tests**

Create tests proving WRR chooses the configured distribution, consistent hash is sticky, request hash fallback is exposed, the selected target and transport belong to the same plan entry, and a plan contains no registry lookup callback.

```go
func TestPlanSelectReturnsEndpointAndSharedTransport(t *testing.T) {
	registry := mustRegistry(t, 64, nil)
	candidate := mustPrepare(t, registry, []model.Upstream{
		testUpstream("users",
			testEndpoint("http://users-a:8080", 2),
			testEndpoint("http://users-b:8080", 1),
		),
	})
	defer candidate.Rollback()

	plan, ok := candidate.Plan("users")
	if !ok {
		t.Fatal("users plan not found")
	}
	request := httptest.NewRequest(http.MethodGet, "http://gateway.test/users", nil)
	selection, err := plan.Select(request)
	if err != nil {
		t.Fatal(err)
	}
	if !selection.Valid() || selection.Target().Host == "" {
		t.Fatalf("selection = %+v", selection)
	}
}
```

- [ ] **Step 2: Write failing registry transaction tests**

Cover:

- equal resources reuse endpoint, selection, and transport entries;
- weight-only changes reuse all runtimes but create a new immutable plan;
- transport-only changes create one transport and reuse endpoints;
- partial prepare failure restores pre-prepare `RegistryStats`;
- `Candidate.Rollback` and `Candidate.Commit` are mutually exclusive and idempotent after the first terminal operation;
- a double plan-set lease release is detected instead of underflowing;
- duplicate `Commit` cannot transfer ownership twice;
- total WRR and continuum budgets reject with `BALANCER_BUDGET_EXCEEDED`.

- [ ] **Step 3: Run registry tests to verify RED**

Run:

```powershell
go test ./internal/upstream -run 'Plan|Registry|Candidate|Budget' -count=1
```

Expected: compile failure because registry and plan contracts do not exist.

- [ ] **Step 4: Add lifecycle observer and stats types**

In `observer.go`, define:

```go
type PrepareStats struct {
	CreatedEndpoints    int
	ReusedEndpoints     int
	CreatedTransports   int
	ReusedTransports    int
	CreatedSelections   int
	ReusedSelections    int
	WRRSlots            int
	HashPoints          int
	Current             RegistryStats
}

type CleanupStats struct {
	ReleasedEndpoints  int
	ReleasedTransports int
	ClosedTransports   int
	Current            RegistryStats
}

type Observer interface {
	RegistryPrepared(PrepareStats)
	RegistryRolledBack(PrepareStats)
	RegistryCleaned(CleanupStats)
	RegistryError(code string, err error)
}

type RegistryStats struct {
	LiveEndpoints       int
	LiveTransports      int
	LiveSelectionStates int
	ActivePlanSets      int
	RetiredPlanSets     int
}
```

All observer calls recover their own panic at the registry boundary.

- [ ] **Step 5: Implement immutable plan and selection**

Define plan entries and selection:

```go
type planEndpoint struct {
	runtime  *endpointRuntime
	identity string
	weight   uint32
}

type Plan struct {
	id        string
	algorithm model.BalancerType
	endpoints []planEndpoint
	transport *transportRuntime
	wrr       wrrSelector
	continuum continuum
	hashKey   hashKeyExtractor
}

type Selection struct {
	endpoint     *endpointRuntime
	transport    *transportRuntime
	ordinal      uint32
	balancer     model.BalancerType
	hashFallback bool
}
```

Compile WRR and continuum inputs by projecting each `planEndpoint` to the Task 5 `weightedEndpoint{identity, weight}` type. `Plan.Select` chooses an ordinal through WRR or `hashKey.sum64` plus continuum, checks the ordinal invariant, and returns `ErrNoEndpoint` only for an impossible compiled state. `Selection.Target` returns the internal immutable parsed URL; `RoundTrip` delegates to the selected plan's shared transport.

- [ ] **Step 6: Implement registry identity maps and candidate acquisition**

Use one registry mutex off-path and entries shaped as:

```go
type endpointEntry struct {
	runtime *endpointRuntime
	refs    int
}

type transportEntry struct {
	runtime *transportRuntime
	refs    int
}

type selectionKey struct {
	upstreamID string
	algorithm  model.BalancerType
}

type selectionEntry struct {
	state *selectionState
	refs  int
}
```

`Prepare` must:

1. reject a closed registry;
2. call `Normalize`;
3. acquire or create entries for every configured endpoint, including weight-zero endpoints, so later enablement can reuse identity;
4. compile each plan from positive-weight endpoints only;
5. accumulate total slot/point budgets;
6. rollback automatically on any error;
7. emit `RegistryPrepared` only after the complete candidate exists.

- [ ] **Step 7: Implement ownership transfer**

`Candidate.Commit` atomically marks the candidate done and returns a `PlanSet` containing the plan map and acquired resource references. `Candidate.Rollback` releases those references synchronously and emits `RegistryRolledBack`. A second terminal operation is a no-op.

At this task's checkpoint, committed plan-set references finalize synchronously when their count reaches zero. Task 10 moves retired finalization to the reaper while preserving the public methods and double-release invariant.

- [ ] **Step 8: Run registry and balancer tests**

Run:

```powershell
go test ./internal/upstream -count=1
go test ./internal/upstream -run '^$' -bench 'WRR|Continuum|HashKey' -benchmem -count=3
```

Expected: all upstream tests pass and selection primitives remain zero-allocation.

- [ ] **Step 9: Commit**

```powershell
git add internal/upstream/plan.go internal/upstream/plan_test.go internal/upstream/registry.go internal/upstream/registry_test.go internal/upstream/observer.go
git commit -m "feat: prepare transactional upstream plans"
```

### Task 10: Add plan-set leases, bounded retirement, and the registry reaper

**Files:**
- Create: `internal/upstream/reaper.go`
- Create: `internal/upstream/reaper_test.go`
- Modify: `internal/upstream/registry.go`
- Modify: `internal/upstream/registry_test.go`
- Modify: `internal/upstream/plan.go`

**Interfaces:**
- Consumes: `PlanSet` ownership from Task 9.
- Produces: non-blocking `TryAcquire`/`Release`, manager-owner `Retire`, a bounded retired queue, periodic cleanup, and `Registry.Close`.

- [ ] **Step 1: Write failing lease and cleanup tests**

Add tests for:

```go
func TestRetiredPlanSetWaitsForFinalLease(t *testing.T) {
	closed := atomic.Int64{}
	registry := newTestRegistry(t, 64, func() {
		closed.Add(1)
	})
	set := committedTestPlanSet(t, registry)
	if !set.TryAcquire() {
		t.Fatal("TryAcquire rejected active plan set")
	}

	set.Retire()
	registry.reapNow()
	if closed.Load() != 0 {
		t.Fatal("transport closed while request lease remained")
	}

	set.Release()
	registry.reapNow()
	if closed.Load() != 1 {
		t.Fatalf("transport close count = %d", closed.Load())
	}
}
```

Also cover CAS failure after reference count reaches zero, non-blocking release when the wake channel is full, periodic cleanup after a coalesced wake, max-retired rejection, exact-once transport close, and context-bounded registry close.

- [ ] **Step 2: Run reaper tests to verify RED**

Run:

```powershell
go test ./internal/upstream -run 'Retired|Reaper|PlanSetLease|RegistryClose' -count=1
```

Expected: failures because retired plan sets are still released synchronously.

- [ ] **Step 3: Implement atomic plan-set references**

Give every committed plan set one manager ownership reference:

```go
type PlanSet struct {
	registry *Registry
	plans    map[string]*Plan
	refs     atomic.Int64
	retired  atomic.Bool
	finalized atomic.Bool
	owned    resourceRefs
}

func (s *PlanSet) TryAcquire() bool {
	for {
		current := s.refs.Load()
		if current <= 0 {
			return false
		}
		if s.refs.CompareAndSwap(current, current+1) {
			return true
		}
	}
}
```

`Release` decrements once per owner and sends a non-blocking wake when the result is zero. Underflow panics in tests and production because it is an internal invariant violation.

- [ ] **Step 4: Implement retirement registration and limit checks**

`Retire` uses CAS to register the set in the registry's retired list and release the manager reference. `Prepare` rejects before acquiring resources when `len(retired) >= maxRetiredSnapshots`, returning:

```go
&ConfigError{
	Code:  "RETIRED_SNAPSHOT_LIMIT",
	Field: "runtime.max_retired_snapshots",
	Cause: fmt.Errorf("retired plan sets reached %d", r.maxRetiredSnapshots),
}
```

The active plan set is not counted in the retired limit.

- [ ] **Step 5: Implement the reaper loop**

Use one goroutine, a size-one wake channel, and a fixed 250 ms ticker:

```go
const reapInterval = 250 * time.Millisecond

func (r *Registry) signalReaper() {
	select {
	case r.reapWake <- struct{}{}:
	default:
	}
}
```

Each scan removes zero-reference retired sets under the registry mutex, releases their owned references, removes zero-reference entries, collects transports to close, unlocks, then closes idle pools and notifies the observer outside the mutex.

- [ ] **Step 6: Implement bounded registry close**

`Registry.Close(ctx)` rejects future prepare calls, wakes the reaper, waits until all plan sets are finalized, closes the reaper stop channel, joins its goroutine, and returns `ctx.Err()` if live leases outlast the context. It must not close a transport referenced by a live plan set.

- [ ] **Step 7: Run lease, race, and full upstream tests**

Run:

```powershell
go test ./internal/upstream -count=1
go test -race ./internal/upstream -run 'Retired|Reaper|Registry|PlanSet' -count=1
```

Expected: tests and focused race detector pass.

- [ ] **Step 8: Commit**

```powershell
git add internal/upstream/reaper.go internal/upstream/reaper_test.go internal/upstream/registry.go internal/upstream/registry_test.go internal/upstream/plan.go
git commit -m "feat: retire upstream plan sets safely"
```

### Task 11: Activate registry-backed snapshots and endpoint selection

**Files:**
- Modify: `internal/runtime/builder.go`
- Modify: `internal/runtime/builder_test.go`
- Modify: `internal/runtime/manager.go`
- Modify: `internal/runtime/manager_test.go`
- Modify: `internal/runtime/snapshot.go`
- Modify: `internal/runtime/validate.go`
- Modify: `internal/requestctx/context.go`
- Modify: `internal/requestctx/context_test.go`
- Modify: `internal/proxy/handler.go`
- Modify: `internal/proxy/handler_test.go`
- Modify: `internal/proxy/route_transport.go`
- Modify: `internal/gateway/gateway.go`
- Modify: `internal/gateway/gateway_test.go`
- Delete: `internal/upstream/table.go`
- Delete: `internal/upstream/table_test.go`
- Delete after migrating its remaining tests: `internal/upstream/runtime.go`

**Interfaces:**
- Consumes: complete registry/plan/lease primitives from Tasks 9–10.
- Produces: the locked `runtime.Manager` lease API, registry-backed snapshot apply, select-after-request-plugins, and graceful registry shutdown.

- [ ] **Step 1: Write failing manager transaction tests**

Add tests asserting:

- candidate prepare failure retains active revision and registry counts;
- router/plugin build failure rolls back the candidate;
- successful publication commits one plan set and retires the old set;
- `Manager.Acquire` never returns a zero-reference retired plan set;
- `Manager.Close` retires the active plan set and closes the registry after lease release.

Use a real registry and an injected builder failure hook; do not mock registry maps.

- [ ] **Step 2: Write a failing proxy ordering test**

Add to `internal/proxy/handler_test.go`:

```go
func TestRequestPluginRunsBeforeConsistentHashSelection(t *testing.T) {
	handler, rewrittenValue, expectedBody := newConsistentHashHandler(t, "X-Tenant")
	request := httptest.NewRequest(http.MethodGet, "http://gateway.test/users", nil)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Body.String() != expectedBody {
		t.Fatalf(
			"rewritten value %q selected body %q, want %q",
			rewrittenValue,
			recorder.Body.String(),
			expectedBody,
		)
	}
}
```

`newConsistentHashHandler` starts two httptest upstreams returning bodies `A` and `B`, compiles the same plan directly, and searches deterministic values `tenant-0`, `tenant-1`, and onward until it finds one whose header mapping differs from the request's remote-address fallback. It returns that value and mapped body, then attaches a request `header-rewrite` plugin that sets the value. The assertion therefore fails if selection happens before request plugins.

- [ ] **Step 3: Run focused tests to verify RED**

Run:

```powershell
go test ./internal/runtime ./internal/proxy -run 'Candidate|Acquire|RequestPluginRunsBefore' -count=1
```

Expected: compile/test failures because runtime still uses the fixed table and proxy has no selection.

- [ ] **Step 4: Refactor builder to resolve candidate plans**

Change construction to:

```go
type Builder struct {
	plugins     *plugin.Registry
	beforeBuild func(uint64)
}

func NewBuilder(plugins *plugin.Registry) (*Builder, error)

func (b *Builder) Build(
	revision uint64,
	input model.ResourceSet,
	candidate *upstream.Candidate,
) (*Snapshot, error)
```

Resolve every route/service upstream with `candidate.Plan(upstreamID)`. Store `*upstream.Plan` in `CompiledRoute`. The snapshot receives its committed `PlanSet` only in manager publication, never inside builder.

- [ ] **Step 5: Refactor manager apply into prepare/build/publish**

Change `Manager` to own `*upstream.Registry`. In `Apply`:

```go
candidate, err := m.upstreams.Prepare(resources.Upstreams)
if err != nil {
	return m.rejectUpstreamError(revision, started, err)
}
defer candidate.Rollback()

snapshot, err := m.builder.Build(revision, resources, candidate)
if err != nil {
	m.notifyRejected(asBuildError(revision, err), time.Since(started))
	return err
}
snapshot.plans = candidate.Commit()
old := m.active.Swap(snapshot)
if old != nil {
	old.plans.Retire()
}
```

Keep `applyMu` across prepare, build, commit, swap, retirement registration, and observer notification ordering.

- [ ] **Step 6: Implement manager leases**

Add:

```go
type Lease struct {
	snapshot *Snapshot
	plans    *upstream.PlanSet
	released atomic.Bool
}
```

`Acquire` loops over `active.Load()` and `snapshot.plans.TryAcquire()`. `Release` uses CAS to call `plans.Release()` exactly once. `Snapshot()` returns the held pointer. Keep `Load()` only for read-only tests/diagnostics; production proxy uses `Acquire`.

Add `Manager.UpstreamStats() upstream.RegistryStats` as a read-only diagnostic used by telemetry and same-package gateway tests; it delegates to `Registry.Stats` and exposes no registry map or runtime pointer.

- [ ] **Step 7: Move endpoint selection into typed request state**

In `requestctx.Context`, add:

```go
Selection upstream.Selection
Attempt   int
```

Change `RuntimeRoute` to:

```go
type RuntimeRoute interface {
	Select(*http.Request) (upstream.Selection, error)
	RunResponse(*Context, *http.Response) error
}
```

`CompiledRoute.Select` delegates to its plan. Remove `Target` and `RoundTrip` from `CompiledRoute`.

- [ ] **Step 8: Select after request plugins and before ReverseProxy**

At the start of `ServeHTTP`, acquire and defer release:

```go
lease, ok := h.snapshots.Acquire()
if !ok {
	writeError(writer, http.StatusServiceUnavailable, "GATEWAY_NOT_READY", "gateway not ready")
	return
}
defer lease.Release()
snapshot := lease.Snapshot()
```

After request plugins and before `h.proxy.ServeHTTP`:

```go
selection, err := state.Runtime.Select(request)
if err != nil {
	h.writeMatchedResponse(
		writer,
		request,
		state,
		http.StatusServiceUnavailable,
		"UPSTREAM_UNAVAILABLE",
		"upstream unavailable",
		nil,
	)
	return
}
state.Selection = selection
state.Attempt = 1
```

`ReverseProxy.Rewrite` uses `state.Selection.Target()`. `routeTransport.RoundTrip` checks `Selection.Valid()` and calls `Selection.RoundTrip`.

- [ ] **Step 9: Replace gateway composition and shutdown**

Construct telemetry first, then:

```go
maxRetiredSnapshots := bootstrap.Runtime.MaxRetiredSnapshots
if maxRetiredSnapshots == 0 {
	maxRetiredSnapshots = config.DefaultMaxRetiredSnapshots
}
upstreamRegistry, err := upstream.NewRegistry(
	maxRetiredSnapshots,
	nil,
)
builder, err := gatewayruntime.NewBuilder(pluginRegistry)
manager := gatewayruntime.NewManager(builder, upstreamRegistry, telemetryRuntime)
```

Task 13 replaces the nil registry observer and runtime telemetry observer with one lifecycle observer that forwards metrics and logs. Store the registry through the manager, not as a second gateway owner.

Add a gateway-owned `trafficRequests sync.WaitGroup` middleware immediately outside the proxy/request-context chain:

```go
func (g *Gateway) trackTraffic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		g.trafficRequests.Add(1)
		defer g.trafficRequests.Done()
		next.ServeHTTP(writer, request)
	})
}
```

After listeners stop accepting, `Shutdown` or forced `Close` finishes, wait for `trafficRequests` before calling `manager.Close`. This guarantees every proxy lease defer ran before synchronous registry cleanup. Do not call `Wait` while a listener can still dispatch a new handler.

Every `Gateway.New` error after registry construction must close the registry with a bounded local context. An initial `manager.Apply` failure rolls back its candidate before registry close; no constructor error may leave the reaper goroutine running.

Delete the fixed table and wrapper after migrating transport correctness tests to `transportRuntime`.

- [ ] **Step 10: Run focused, full, and race tests**

Run:

```powershell
go test ./internal/runtime ./internal/requestctx ./internal/proxy ./internal/gateway -count=1
go test ./... -count=1
go test -race ./internal/upstream ./internal/runtime ./internal/proxy ./internal/gateway -count=1
```

Expected: all tests pass and the fixed table no longer exists.

- [ ] **Step 11: Commit**

```powershell
git add internal/runtime internal/requestctx internal/proxy internal/gateway internal/upstream
git commit -m "feat: activate registry backed upstream plans"
```

### Task 12: Prove dynamic reconcile, snapshot consistency, and pool reuse

**Files:**
- Modify: `test/integration/snapshot_test.go`
- Create: `test/integration/upstream_reconcile_test.go`
- Modify: `internal/gateway/gateway_test.go`
- Modify: `internal/upstream/registry_test.go`

**Interfaces:**
- Consumes: the complete vertical runtime path from Task 11.
- Produces: black-box regression evidence for dynamic upstream membership/weight updates and exact lifecycle behavior.

- [ ] **Step 1: Write an in-flight old-plan/new-plan integration test**

Create two controlled upstream servers. Hold a revision-1 request after selection, apply revision 2 with a different endpoint set and weight policy, issue a second request, and assert:

```go
if held.endpoint != "A" || held.revision != "1" {
	t.Fatalf("held request = %+v, want endpoint A revision 1", held)
}
if current.endpoint != "B" || current.revision != "2" {
	t.Fatalf("current request = %+v, want endpoint B revision 2", current)
}
```

Release the held request and complete the black-box response assertion. Add a companion same-package gateway test that uses `manager.UpstreamStats()` to poll until the retired plan-set count returns to zero.

- [ ] **Step 2: Write a weight-only keepalive reuse test**

Use `http.Server.ConnState` to count accepted upstream connections. Warm both endpoints, apply only a weight change, send additional requests, and assert no new connection is required for a warmed endpoint:

```go
before := upstreamA.AcceptedConnections()
if err := instance.Apply(2, weightRevision(7, 1)); err != nil {
	t.Fatal(err)
}
requestEndpoint(t, client, gatewayURL, "A")
if got := upstreamA.AcceptedConnections(); got != before {
	t.Fatalf("connections after weight update = %d, want %d", got, before)
}
```

- [ ] **Step 3: Write an unrelated-upstream isolation test**

Warm upstream B, change membership and transport settings only on upstream A, route to B again, and assert B's accepted-connection count remains unchanged. In `internal/upstream/registry_test.go`, inspect the package-private transport entry before and after the candidate to assert pointer identity directly; do not export registry maps or pointers.

- [ ] **Step 4: Write rejected-apply and request-error tests**

Apply duplicate canonical endpoints, all-zero weights, a budget-exceeding resource set, and an invalid plugin candidate. After each rejection, assert active revision, successful response endpoint, and live registry counts match the last-known-good values.

Preserve and extend proxy error assertions so:

- an injected impossible empty compiled plan returns `503 UPSTREAM_UNAVAILABLE`;
- DNS/dial/connection failure returns `502 UPSTREAM_CONNECTION_FAILED`;
- dial or response-header timeout returns `504 UPSTREAM_TIMEOUT`;
- client cancellation writes no replacement gateway response and releases its plan-set lease;
- no failure selects a second endpoint in Phase 3A.

- [ ] **Step 5: Run dynamic and concurrent tests**

Run:

```powershell
go test ./test/integration -run 'Upstream|Snapshot|Reconcile|Keepalive' -count=1
go test ./internal/gateway ./internal/upstream -run 'Apply|Rollback|Unrelated|Retired' -count=1
go test -race ./test/integration -run 'ConcurrentRequestsNeverMixSnapshotRevisions|UpstreamReconcile' -count=1
```

Expected: all tests pass without mixed revision or unexpected connection creation.

- [ ] **Step 6: Commit**

```powershell
git add test/integration/snapshot_test.go test/integration/upstream_reconcile_test.go internal/gateway/gateway_test.go internal/upstream/registry_test.go
git commit -m "test: prove dynamic upstream reconcile semantics"
```

### Task 13: Add bounded registry and balancer telemetry

**Files:**
- Modify: `internal/telemetry/telemetry.go`
- Modify: `internal/telemetry/telemetry_test.go`
- Modify: `internal/upstream/observer.go`
- Modify: `internal/upstream/registry.go`
- Modify: `internal/requestctx/context.go`
- Create: `internal/gateway/lifecycle_observer.go`
- Create: `internal/gateway/lifecycle_observer_test.go`
- Modify: `internal/gateway/gateway.go`

**Interfaces:**
- Consumes: `upstream.Observer`, `PrepareStats`, `CleanupStats`, `RegistryStats`, and request `Selection`.
- Produces: bounded registry lifecycle metrics and request balancer/fallback counters.

- [ ] **Step 1: Write failing metric registration and update tests**

Assert these metric families exist and change:

```text
gateway_upstream_live_endpoints
gateway_upstream_live_transports
gateway_upstream_live_selection_states
gateway_runtime_retired_snapshots
gateway_upstream_registry_resources_total{action,kind}
gateway_upstream_registry_rollbacks_total
gateway_upstream_transport_cleanup_total
gateway_upstream_balancer_selections_total{upstream_id,algorithm}
gateway_upstream_hash_fallback_total{upstream_id}
```

Use configured upstream IDs only. Assert a raw endpoint URL, hostname, client address, and hash value never appear in gathered label values.

- [ ] **Step 2: Run telemetry tests to verify RED**

Run:

```powershell
go test ./internal/telemetry -run 'Upstream|Balancer|HashFallback' -count=1
```

Expected: missing metric assertions fail.

- [ ] **Step 3: Register lifecycle collectors**

Add gauges/counters to `Telemetry`, register them with its private Prometheus registry, and implement:

```go
func (t *Telemetry) RegistryPrepared(stats upstream.PrepareStats)
func (t *Telemetry) RegistryRolledBack(stats upstream.PrepareStats)
func (t *Telemetry) RegistryCleaned(stats upstream.CleanupStats)
```

Update live gauges from the `Current RegistryStats` field defined in Task 9, not by independently guessing reference transitions.

- [ ] **Step 4: Record request selections in the telemetry wrapper**

After `next.ServeHTTP`, if request metrics are enabled and `state.Selection.Valid()`:

```go
upstreamID := state.Upstream.ID
algorithm := string(state.Selection.Balancer())
t.balancerSelections.WithLabelValues(upstreamID, algorithm).Inc()
if state.Selection.HashFallback() {
	t.hashFallbacks.WithLabelValues(upstreamID).Inc()
}
```

Do not add endpoint identity as a label. Keep unmatched requests out of balancer counters.

- [ ] **Step 5: Add bounded structured lifecycle logs**

Create a gateway `lifecycleObserver` that forwards runtime and upstream observer calls to telemetry and emits structured `slog` events:

```go
type lifecycleObserver struct {
	telemetry *telemetry.Telemetry
	logger    *slog.Logger
}
```

Log one event for snapshot applied/rejected, registry prepared/rolled back/cleaned, `RegistryError`, and final shutdown cleanup. Include revision, bounded code/stage, resource counts, and duration; exclude raw endpoint URL, hostname, hash key, and client address. Pass this observer to both `NewRegistry` and `NewManager`.

- [ ] **Step 6: Run telemetry, logging, and full tests**

Run:

```powershell
go test ./internal/telemetry ./internal/upstream ./internal/proxy ./internal/gateway -count=1
go test ./... -count=1
```

Expected: metric tests and the full suite pass.

- [ ] **Step 7: Commit**

```powershell
git add internal/telemetry internal/upstream/observer.go internal/upstream/registry.go internal/requestctx/context.go internal/gateway/lifecycle_observer.go internal/gateway/lifecycle_observer_test.go internal/gateway/gateway.go
git commit -m "feat: expose upstream runtime telemetry"
```

### Task 14: Add Phase 3A scale, allocation, memory, and race acceptance

**Files:**
- Create: `internal/upstream/acceptance_test.go`
- Create: `internal/upstream/registry_benchmark_test.go`
- Create: `internal/runtime/lease_benchmark_test.go`
- Modify: `internal/upstream/wrr_benchmark_test.go`
- Modify: `internal/upstream/chash_benchmark_test.go`
- Modify: `internal/runtime/acceptance_test.go`

**Interfaces:**
- Consumes: completed registry, manager lease, WRR, chash, and reaper.
- Produces: deterministic normal/full acceptance profiles and benchmark names used by the runbook.

- [ ] **Step 1: Add the deterministic upstream dataset helper**

Inside `acceptance_test.go`, generate:

```go
type acceptanceProfile struct {
	upstreams          int
	endpointsPerStream int
	chashPercent       int
	swaps              int
}

var normalPhase3AProfile = acceptanceProfile{
	upstreams:          1_000,
	endpointsPerStream: 10,
	chashPercent:       20,
	swaps:              2,
}

var fullPhase3AProfile = acceptanceProfile{
	upstreams:          10_000,
	endpointsPerStream: 10,
	chashPercent:       20,
	swaps:              20,
}
```

Use seed `20260726`, weights cycling through `1..5`, deterministic hostnames, and valid transport profiles. Compute and log a SHA-256 checksum over canonical JSON.

- [ ] **Step 2: Write the acceptance test**

`TestPhase3AAcceptance` chooses the full profile only when `GATEWAY_PHASE3A_ACCEPTANCE=1`. It measures prepare/commit time, one active plan-set heap, and retained heap after weight-only swaps, reaper quiescence, and two boundary GCs.

Assert in full mode:

```go
if buildElapsed > 5*time.Second {
	t.Fatalf("full-envelope build = %s, want <= 5s", buildElapsed)
}
if onePlanSetHeap > 512<<20 {
	t.Fatalf("active plan-set heap = %d, want <= 512 MiB", onePlanSetHeap)
}
if retainedHeap > onePlanSetHeap*125/100 {
	t.Fatalf("retained heap = %d, want <= 125%% of %d", retainedHeap, onePlanSetHeap)
}
```

Normal mode still asserts budget compliance, zero retired sets after quiescence, and runtime reuse counts.

- [ ] **Step 3: Lock benchmark names and add registry/lease benchmarks**

Keep the selector benchmarks created in Tasks 5 and 7 under the exact sub-benchmark names below, then add the lease and registry benchmarks:

```text
BenchmarkSnapshotAcquireRelease
BenchmarkWRRSelect/1
BenchmarkWRRSelect/100
BenchmarkWRRSelect/1000
BenchmarkConsistentHashSelect/1
BenchmarkConsistentHashSelect/100
BenchmarkConsistentHashSelect/1000
BenchmarkRegistryReconcile/full
BenchmarkRegistryReconcile/weight-only
```

Every selector and lease benchmark calls `b.ReportAllocs()`. Reconcile benchmarks stop the timer while generating resources.

- [ ] **Step 4: Preserve Phase 2 acceptance behavior**

Update `internal/runtime/acceptance_test.go` to construct the new registry/manager, acquire the active snapshot through a lease for sentinel checks, release it, and wait for reaper quiescence before reading steady-state memory. Keep Phase 2 route thresholds and transitional evidence semantics unchanged.

- [ ] **Step 5: Run normal acceptance and benchmarks**

Run:

```powershell
go test ./internal/upstream ./internal/runtime -run 'TestPhase2Acceptance|TestPhase3AAcceptance' -count=1 -v
go test ./internal/upstream ./internal/runtime -run '^$' -bench 'BenchmarkSnapshotAcquireRelease|BenchmarkWRRSelect|BenchmarkConsistentHashSelect|BenchmarkRegistryReconcile' -benchmem -count=3
```

Expected: normal acceptance passes; lease/select benchmarks report zero allocations and satisfy relative-scale targets.

- [ ] **Step 6: Run focused race verification**

Run:

```powershell
go test -race ./internal/upstream ./internal/runtime ./internal/proxy ./internal/gateway ./test/integration -count=1
```

Expected: race detector passes.

- [ ] **Step 7: Commit**

```powershell
git add internal/upstream/acceptance_test.go internal/upstream/registry_benchmark_test.go internal/upstream/wrr_benchmark_test.go internal/upstream/chash_benchmark_test.go internal/runtime/lease_benchmark_test.go internal/runtime/acceptance_test.go
git commit -m "bench: add phase 3a upstream acceptance"
```

### Task 15: Document operations, evidence status, and Phase 3B handoff

**Files:**
- Create: `docs/operations/phase-3a-runbook.md`
- Create: `docs/benchmarks/phase-3a-current-status.md`
- Modify: `README.md`
- Modify: `docs/superpowers/specs/2026-07-21-go-native-api-gateway-phase-roadmap-design.md`
- Modify: `docs/superpowers/specs/2026-07-26-phase-3a-upstream-runtime-balancing-kernel-design.md`

**Interfaces:**
- Consumes: actual implemented commands, config, metrics, errors, benchmark names, and evidence from Tasks 1–14.
- Produces: operator instructions, an honest evidence ledger, and a stable Phase 3B entry boundary.

- [ ] **Step 1: Write the Phase 3A runbook**

Document:

- v1alpha3 startup with `configs/phase3a.yaml`;
- internal-only `Gateway.Apply` status;
- endpoint identity, weight zero, WRR, and hash fallback semantics;
- stable configuration and request errors;
- pool reuse and retired-snapshot limit;
- metrics with cardinality warnings;
- graceful shutdown/reaper behavior;
- fast verification commands;
- full acceptance, benchmark, race, and fuzz commands;
- explicit exclusions owned by 3B/3C/3D.

Do not document a public reload endpoint.

- [ ] **Step 2: Write the evidence-status document**

Use exactly one of:

```text
implementation in progress
implementation complete; canonical resource evidence pending
Phase 3A accepted
```

Record commands as passed only after their output has been observed. Keep separate tables for correctness, allocation/relative scale, full-envelope resource evidence, and deferred APISIX E2E. Repeat the Phase 2 Task 16 debt link.

- [ ] **Step 3: Update repository navigation and roadmap**

Update README capabilities/exclusions/layout/run commands to Phase 3A. In the roadmap, mark 3A implementation status without marking the entire Phase 3 complete. State that:

- Phase 3B receives immutable plans, endpoint identity, shared transports, leases, and registry lifecycle, but must not move health into snapshots;
- Phase 3C owns upstream TLS/protocol expansion, dynamic downstream SNI certificates, and WebSocket;
- Phase 3D owns bounded access logging and canonical integrated APISIX comparison.

- [ ] **Step 4: Audit the design against implementation**

Read every design section and change only statements whose implemented names or measured thresholds differ. Do not weaken an unmet gate; record it in the evidence-status document.

- [ ] **Step 5: Verify local Markdown links**

Run a PowerShell link check that resolves every relative Markdown target in README, runbook, evidence status, roadmap, Phase 3A design, and this plan. The script must set `$ErrorActionPreference='Stop'`, resolve each source file to an absolute path before `Split-Path`, ignore HTTP(S) links, and exit non-zero on a missing target.

Expected: zero missing targets.

- [ ] **Step 6: Commit**

```powershell
git add README.md docs/operations/phase-3a-runbook.md docs/benchmarks/phase-3a-current-status.md docs/superpowers/specs/2026-07-21-go-native-api-gateway-phase-roadmap-design.md docs/superpowers/specs/2026-07-26-phase-3a-upstream-runtime-balancing-kernel-design.md
git commit -m "docs: record phase 3a upstream runtime"
```

### Task 16: Run final verification and record the checkpoint

**Files:**
- Modify with observed evidence only: `docs/benchmarks/phase-3a-current-status.md`

**Interfaces:**
- Consumes: the complete Phase 3A implementation and documentation.
- Produces: a clean branch with evidence-backed checkpoint status; it does not produce APISIX parity evidence.

- [ ] **Step 1: Run formatting, vet, unit/integration, and build gates**

Run:

```powershell
$unformatted = @(gofmt -l .)
if ($unformatted.Count -ne 0) {
  $unformatted
  exit 1
}
go vet ./...
go test ./... -count=1
go build ./cmd/...
```

Expected: every command passes.

- [ ] **Step 2: Run race verification**

Run:

```powershell
go test -race ./internal/upstream ./internal/runtime ./internal/proxy ./internal/gateway ./test/integration -count=1
```

Expected: PASS. If the host cannot provide reliable race execution, record the exact command and failure; do not mark it passed.

- [ ] **Step 3: Run normal and full Phase 3A acceptance**

Run:

```powershell
go test ./internal/upstream ./internal/runtime -run 'TestPhase2Acceptance|TestPhase3AAcceptance' -count=1 -v
$env:GATEWAY_PHASE3A_ACCEPTANCE = '1'
go test ./internal/upstream -run TestPhase3AAcceptance -count=1 -v
Remove-Item Env:GATEWAY_PHASE3A_ACCEPTANCE
```

Expected: normal acceptance passes. Full results are recorded as provisional unless run on the reference Linux environment.

- [ ] **Step 4: Run five-count microbenchmarks**

Run:

```powershell
go test ./internal/upstream ./internal/runtime -run '^$' -bench 'BenchmarkSnapshotAcquireRelease|BenchmarkWRRSelect|BenchmarkConsistentHashSelect|BenchmarkRegistryReconcile' -benchmem -count=5
```

Expected: lease and selector cases report `0 B/op`, `0 allocs/op`; calculate and record the 1/100/1,000 relative ratios.

- [ ] **Step 5: Run bounded fuzz smoke**

Run:

```powershell
go test ./internal/upstream -run '^$' -fuzz FuzzNormalizeEndpoint -fuzztime 30s
go test ./internal/upstream -run '^$' -fuzz FuzzHashKey -fuzztime 30s
```

Expected: both smoke runs pass. The runbook preserves five-minute extended commands; an omitted extended run is recorded as not run.

- [ ] **Step 6: Inspect shutdown and goroutine cleanup**

Run the focused lifecycle suite repeatedly:

```powershell
go test ./internal/upstream ./internal/gateway ./test/integration -run 'Shutdown|Retired|Reaper|Reconcile' -count=20
```

Expected: PASS without timeout, leaked retired plan sets, or inconsistent connection-close counts.

- [ ] **Step 7: Update evidence status from observed output**

Record toolchain, OS/environment, dataset seed/checksum, build time, heap, retained ratio, benchmark medians/ratios, race status, fuzz duration, and omitted canonical commands. Use:

- `Phase 3A accepted` only if the reference-Linux absolute gates passed;
- otherwise `implementation complete; canonical resource evidence pending` when all mandatory development gates passed;
- otherwise `implementation in progress`.

- [ ] **Step 8: Run final documentation and Git checks**

Run:

```powershell
git diff --check
git status --short
git log --oneline --decorate -20
```

Repeat the Task 15 local-link checker after updating evidence.

Expected: no whitespace errors, only the evidence update is uncommitted, and task commits are visible.

- [ ] **Step 9: Commit the observed evidence**

```powershell
git add docs/benchmarks/phase-3a-current-status.md
git commit -m "docs: record phase 3a verification evidence"
```

- [ ] **Step 10: Confirm a clean checkpoint**

Run:

```powershell
git status --short --branch
```

Expected: clean `codex/phase3a-upstream-runtime-balancing-kernel-design` branch. Use `superpowers:finishing-a-development-branch` only after this verification is complete.

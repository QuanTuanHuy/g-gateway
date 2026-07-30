# Go Source Documentation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Backfill idiomatic, contract-focused Go source documentation across every production package and enforce its continued completeness in CI.

**Architecture:** Work in dependency order so each package's vocabulary is documented before its consumers. Add package and declaration comments without changing behavior or signatures, verify each package slice independently, and enable the repository-wide Staticcheck and Revive gates only after the baseline is clean.

**Tech Stack:** Go 1.26.5, Go doc comments, `gofmt`, `go doc`, Staticcheck 2026.1, Revive v1.15.0, `go vet`, `go test`, GitHub Actions.

> **Approved gate correction (2026-07-30):** Staticcheck `ST1020`,
> `ST1021`, and `ST1022` validate the form of existing comments but do not
> report missing declaration comments. Every documentation verification step
> therefore runs both Staticcheck and Revive. Revive's sole `exported` rule is
> the missing-declaration gate; its configuration excludes tests. In the
> partial upstream slices, file-level diagnostic filtering applies to Revive
> output while Staticcheck must be clean for all comments already present.

## Global Constraints

- Follow `docs/superpowers/specs/2026-07-30-go-source-documentation-design.md`.
- Write source comments in English.
- Do not change runtime behavior, API signatures, package boundaries, configuration semantics, error codes, or tests except when an executable example is explicitly specified by this plan.
- Give every production package exactly one canonical package comment.
- Give every exported production type, function, method, variable, and constant a name-led complete-sentence doc comment.
- Audit every exported struct field; add a field comment when its meaning, unit, default, sentinel, ownership, lifetime, or interaction is not completely explained by the type comment.
- Document concurrency, zero values, lifecycle, ownership, mutation, cancellation, stable errors, panics, units, limits, and deterministic ordering wherever applicable.
- Comment unexported implementation only to explain a verified invariant, constraint, compatibility rule, or trade-off.
- Do not duplicate architecture, runbook, benchmark, or phase-specification prose in source files.
- Do not add general-purpose lint rules. Staticcheck enables only `ST1000`, `ST1020`, `ST1021`, and `ST1022`; Revive enables only `exported`.
- Exclude tests from the documentation gate with Staticcheck `-tests=false` and Revive's `TEST` rule exclusion.
- Keep every task behavior-preserving and commit it independently.

---

## Baseline Inventory

The inventory was produced from the Go AST over non-test files beneath
`internal/` and `cmd/`.

| Package | Package doc | Exported declarations | Exported fields to audit |
|---|---:|---:|---:|
| `cmd/bench-dataset` | no | 0 | 0 |
| `cmd/bench-report` | no | 0 | 0 |
| `cmd/gateway-dp` | no | 0 | 0 |
| `cmd/test-upstream` | no | 0 | 0 |
| `internal/benchdataset` | no | 11 | 33 |
| `internal/benchreport` | yes | 13 | 30 |
| `internal/config` | no | 12 | 21 |
| `internal/gateway` | no | 18 | 3 |
| `internal/model` | no | 37 | 77 |
| `internal/plugin` | no | 23 | 15 |
| `internal/proxy` | no | 7 | 3 |
| `internal/requestctx` | no | 11 | 23 |
| `internal/router` | no | 7 | 9 |
| `internal/runtime` | no | 40 | 18 |
| `internal/telemetry` | no | 18 | 0 |
| `internal/testupstream` | no | 2 | 0 |
| `internal/upstream` | no | 112 | 59 |
| **Total** | **1 of 17** | **311** | **291** |

Revive is the mechanical missing-declaration gate, while Staticcheck validates
package comments and the form of comments that exist. Field completeness and
semantic quality remain explicit review responsibilities because neither tool
validates exported struct fields or the truth of a comment.

No executable examples are added in this backfill. The core APIs require
non-trivial fixtures, network state, or lifecycle cleanup, and the existing
unit and integration tests already provide executable evidence without forcing
large examples into rendered package documentation.

---

### Task 1: Establish the Documentation Gate and Document the Canonical Model

**Files:**
- Create: `staticcheck.conf`
- Create: `revive.toml`
- Modify: `internal/model/resources.go`
- Read for contract evidence: `internal/model/resources_test.go`
- Read for contract evidence: `docs/superpowers/specs/2026-07-27-phase-3b-health-timeout-retry-design.md`

**Interfaces:**
- Consumes: the existing model types and `CloneResourceSet(model.ResourceSet) model.ResourceSet`.
- Produces: the repository-wide documentation rule configuration and a lint-clean `internal/model` package for later configuration, router, runtime, and upstream tasks.

**Required declaration coverage:**
- Constants: `PredicateEquals`, `PredicateNotEquals`, `PredicateExists`, `PredicateNotExists`, `PredicateOneOf`, `BalancerWeightedRoundRobin`, `BalancerConsistentHash`, `HashSourceHeader`, `HashSourceCookie`, `HashSourceRemoteAddr`, `HashSourceLiteral`, `HealthCheckHTTP`, `HealthCheckTCP`.
- Types: `PredicateOperator`, `BalancerType`, `HashSourceType`, `HealthCheckType`, `ResourceSet`, `Route`, `RouteMatch`, `Predicate`, `Service`, `PluginAttachment`, `Upstream`, `HealthPolicy`, `ActiveHealthPolicy`, `PassiveHealthPolicy`, `RetryOnPolicy`, `RetryBudgetPolicy`, `RetryPolicy`, `RouteResiliencePolicy`, `Endpoint`, `BalancerPolicy`, `HashKeyPolicy`, `HashKeySource`, `TransportConfig`.
- Function: `CloneResourceSet`.
- Audit all 77 exported fields for units, defaults, optionality, precedence, and aliasing.

- [ ] **Step 1: Add the narrow lint configurations**

Create `staticcheck.conf` with exactly:

```toml
checks = ["ST1000", "ST1020", "ST1021", "ST1022"]
```

Create `revive.toml` with only the `exported` rule, enable
`check-private-receivers` and `check-public-interface`, disable the unrelated
stuttering check, and exclude `TEST`.

- [ ] **Step 2: Install and confirm the pinned analyzers**

Run:

```powershell
go install honnef.co/go/tools/cmd/staticcheck@2026.1
go install github.com/mgechev/revive@v1.15.0
$staticcheck = Join-Path (go env GOPATH) 'bin\staticcheck.exe'
$revive = Join-Path (go env GOPATH) 'bin\revive.exe'
& $staticcheck -version
& $revive -version
```

Expected: the version output identifies Staticcheck `2026.1` and Revive
`v1.15.0`.

- [ ] **Step 3: Prove the model baseline fails**

Run:

```powershell
& (Join-Path (go env GOPATH) 'bin\staticcheck.exe') -tests=false ./internal/model
& (Join-Path (go env GOPATH) 'bin\revive.exe') -set_exit_status -config revive.toml -formatter default ./internal/model/...
```

Expected: Staticcheck reports the missing package comment. Revive reports the
currently undocumented exported types, constants, and function.

- [ ] **Step 4: Add the model package contract**

Add this opening package comment directly above `package model`:

```go
// Package model defines the canonical resources consumed by the gateway
// compiler and runtime.
//
// Resource values are treated as immutable after validation and compilation.
// Use CloneResourceSet when a caller needs an independently owned copy.
package model
```

- [ ] **Step 5: Document enum types and constants**

Document the four enum types and every constant listed above. State that:

- predicate operators describe exact header/query comparison semantics;
- balancer values select deterministic weighted round-robin or consistent hash;
- hash-source order is meaningful and participates in compound key formation;
- health-check values distinguish application-level HTTP probing from raw TCP
  reachability.

Use name-led comments, for example:

```go
// BalancerWeightedRoundRobin selects endpoints according to their configured
// positive weights using a deterministic schedule.
BalancerWeightedRoundRobin BalancerType = "weighted_round_robin"
```

- [ ] **Step 6: Document resource containers and routing types**

Document `ResourceSet`, `Route`, `RouteMatch`, `Predicate`, `Service`, and
`PluginAttachment`. Cover route/service reference ownership, AND semantics
within match predicates, declaration order versus compiled precedence, and
plugin attachment inheritance/disable behavior proven by the model, router,
and plugin tests.

- [ ] **Step 7: Document upstream and resilience types**

Document `Upstream`, the health types, retry types, `Endpoint`,
`BalancerPolicy`, `HashKeyPolicy`, `HashKeySource`, and `TransportConfig`.
Cover:

- zero weight disables selection without deleting endpoint identity;
- duration fields use `time.Duration`;
- retry attempts include the primary attempt;
- a zero total timeout disables the gateway-owned total deadline;
- retry methods/statuses are normalized later by `internal/upstream`;
- retry-budget zero values disable the budget;
- transport fields control pool identity and timeout behavior.

- [ ] **Step 8: Document field contracts**

Audit all 77 exported fields. Add individual field comments for every field
whose unit, default, sentinel, optionality, precedence, or ownership is not
fully described in its enclosing type comment. In particular, comment duration
fields, counts/limits, `Weight`, `Attempts`, `TotalTimeout`, `PerTryTimeout`,
`Burst`, `MaxInflight`, `Sources`, and optional pointer policies.

- [ ] **Step 9: Document cloning semantics**

Document `CloneResourceSet` as returning a deep, independently mutable copy.
State that it clones nested slices, pointer policies, and maps represented by
resource collections; do not claim validation or normalization.

- [ ] **Step 10: Format and verify the model documentation**

Run:

```powershell
gofmt -w internal/model/resources.go
& (Join-Path (go env GOPATH) 'bin\staticcheck.exe') -tests=false ./internal/model
& (Join-Path (go env GOPATH) 'bin\revive.exe') -set_exit_status -config revive.toml -formatter default ./internal/model/...
go vet ./internal/model
go test ./internal/model -count=1
go doc -all ./internal/model
git diff --check
```

Expected: Staticcheck prints no diagnostics; vet and tests pass; rendered docs
show all enums, resources, and clone semantics without malformed lists.

- [ ] **Step 11: Commit**

```powershell
git add staticcheck.conf revive.toml internal/model/resources.go
git commit -m "docs: document canonical resource model"
```

---

### Task 2: Document Configuration and Request Context Foundations

**Files:**
- Create: `internal/config/doc.go`
- Modify: `internal/config/load.go`
- Modify: `internal/config/types.go`
- Modify: `internal/requestctx/context.go`
- Read for contract evidence: `internal/config/load_test.go`
- Read for contract evidence: `internal/config/validate.go`
- Read for contract evidence: `internal/config/wire_v1alpha2.go`
- Read for contract evidence: `internal/config/wire_v1alpha3.go`
- Read for contract evidence: `internal/config/wire_v1alpha4.go`
- Read for contract evidence: `internal/requestctx/context_test.go`

**Interfaces:**
- Consumes: documented `model.ResourceSet` and upstream selection/retry handles.
- Produces: lint-clean `config` and `requestctx` contracts used by all compile and request-path packages.

**Required declaration coverage:**
- Config constants: `DefaultMaxRetiredSnapshots`, `DefaultHealthWorkers`, `DefaultHealthQueueCapacity`.
- Config types: `BootstrapConfig`, `RuntimeConfig`, `HealthRuntimeConfig`, `ListenerConfig`, `TLSListenerConfig`, `ServerConfig`, `TelemetryConfig`.
- Config functions: `Load`, `Decode`.
- Request context types: `RouteMeta`, `ServiceMeta`, `UpstreamMeta`, `ParamSpan`, `SnapshotRef`, `RuntimeRoute`, `Context`.
- Request context functions/method: `Attach`, `From`, `Middleware`, `Context.AllocateScratch`.
- Audit 44 exported fields across the two packages.

- [ ] **Step 1: Prove both package baselines fail**

Run:

```powershell
& (Join-Path (go env GOPATH) 'bin\staticcheck.exe') -tests=false ./internal/config ./internal/requestctx
```

Expected: FAIL with missing package and declaration documentation diagnostics.

- [ ] **Step 2: Add the configuration package contract**

Create `internal/config/doc.go`:

```go
// Package config loads, strictly decodes, and validates versioned gateway
// configuration into bootstrap settings and canonical model resources.
//
// Decoding rejects unknown fields and multiple YAML documents. Successful
// results are ready for runtime compilation but remain caller-owned values.
package config
```

- [ ] **Step 3: Document configuration defaults and bootstrap types**

In `types.go`, document all three defaults, all seven exported types, and their
21 fields. State exact units and zero/default behavior:

- listener addresses use Go `net.Listen` address syntax;
- timeouts use `time.Duration`;
- byte limits are bytes;
- runtime worker and queue zero values are replaced by the named defaults by
  gateway construction;
- TLS certificate and key paths are startup inputs;
- telemetry flags are opt-in.

- [ ] **Step 4: Document loading and decoding**

In `load.go`, document:

- `Load` reads one file, wraps path/read/decode errors, and delegates strict
  format validation to `Decode`;
- `Decode` accepts the supported API versions, returns bootstrap and canonical
  resources separately, rejects unknown fields and additional YAML documents,
  applies version-specific compatibility defaults, and returns no partial
  successful configuration on error.

- [ ] **Step 5: Add the request-context package contract**

Add directly above `package requestctx`:

```go
// Package requestctx attaches mutable gateway request state to an HTTP request
// through a private typed context key.
//
// Each attached Context belongs to one request and must not be shared across
// requests. Snapshot and runtime references remain valid only for the request
// lease that installed them.
package requestctx
```

- [ ] **Step 6: Document metadata, interfaces, and mutable state**

Document all seven exported types and 23 fields. Required contracts include:

- `ParamSpan.Start` and `End` are byte offsets into `Context.Path`, with `End`
  exclusive;
- `SnapshotRef` exposes the revision retained by the active request lease;
- `RuntimeRoute` is the request-path contract for retry, selection, and plugin
  response execution;
- `Context` is mutable, request-owned, and not safe for cross-request sharing;
- `Scratch` indices are assigned by the compiled plugin chain;
- attempt and response fields are observational state, not configuration.

- [ ] **Step 7: Document attachment and scratch allocation**

Document:

- `Attach` returns a shallow request copy with a fresh state and allocates
  scratch only for positive slot counts;
- `From` reports whether the private typed state exists;
- `Middleware` installs one fresh state before invoking the next handler;
- `AllocateScratch` replaces scratch storage for positive counts and leaves it
  unchanged for zero or negative counts, matching the implementation.

- [ ] **Step 8: Format and verify**

Run:

```powershell
gofmt -w internal/config/doc.go internal/config/load.go internal/config/types.go internal/requestctx/context.go
& (Join-Path (go env GOPATH) 'bin\staticcheck.exe') -tests=false ./internal/config ./internal/requestctx
go vet ./internal/config ./internal/requestctx
go test ./internal/config ./internal/requestctx -count=1
go doc -all ./internal/config
go doc -all ./internal/requestctx
git diff --check
```

Expected: documentation checks are clean and both package suites pass.

- [ ] **Step 9: Commit**

```powershell
git add internal/config/doc.go internal/config/load.go internal/config/types.go internal/requestctx/context.go
git commit -m "docs: document configuration and request context"
```

---

### Task 3: Document Router and Plugin Compilation Contracts

**Files:**
- Create: `internal/router/doc.go`
- Modify: `internal/router/pattern.go`
- Modify: `internal/router/query.go`
- Modify: `internal/router/router.go`
- Create: `internal/plugin/doc.go`
- Modify: `internal/plugin/chain.go`
- Modify: `internal/plugin/header_rewrite.go`
- Modify: `internal/plugin/registry.go`
- Modify: `internal/plugin/request_id.go`
- Read for contract evidence: `internal/router/*_test.go`
- Read for contract evidence: `internal/plugin/*_test.go`

**Interfaces:**
- Consumes: canonical model routes and request-owned `requestctx.Context`.
- Produces: documented immutable router and plugin contracts consumed by the runtime builder.

**Required declaration coverage:**
- Router: `RouteSpec`, `Router`, `Result`, `Compile`, `Router.Match`, `NormalizeRequestHost`, `ErrInvalidQuery`.
- Plugin: `Action`, `Continue`, `ShortCircuit`, `ShortCircuitResponse`, `RequestResult`, `RequestHook`, `ResponseHook`, `Chain`, `Chain.Names`, `Chain.ScratchSlots`, `Chain.RunRequest`, `Chain.RunResponse`, `CompiledPlugin`, `Definition`, `Registry`, `NewBuiltinRegistry`, `NewRegistry`, `Registry.CompileChain`, plus exported hook methods in built-ins.
- Audit 24 exported fields across both packages.

- [ ] **Step 1: Prove the router and plugin baselines fail**

```powershell
& (Join-Path (go env GOPATH) 'bin\staticcheck.exe') -tests=false ./internal/router ./internal/plugin
```

Expected: FAIL with `ST1000`, `ST1020`, `ST1021`, and `ST1022`.

- [ ] **Step 2: Add the router package contract**

Create `internal/router/doc.go`:

```go
// Package router compiles canonical route match expressions into an immutable,
// deterministic HTTP request router.
//
// Matching performs no configuration lookup. Route priority and compiled
// specificity, rather than declaration order, determine the winning route.
package router
```

- [ ] **Step 3: Document router construction and results**

Document the seven router declarations and nine fields. Cover:

- `Compile` rejects empty input, empty/duplicate IDs, invalid patterns, and
  duplicate canonical match expressions;
- a compiled `Router` is immutable and safe for concurrent matching;
- `Result.Params` spans refer to the request path used for matching;
- `MethodNotAllowed` and sorted/deduplicated `Allow` are distinct from not
  found;
- `Match` can return `ErrInvalidQuery` for malformed query escaping;
- `NormalizeRequestHost` lowercases DNS hosts, removes a valid port, and
  handles malformed authorities according to its boolean result.

- [ ] **Step 4: Add the plugin package contract**

Create `internal/plugin/doc.go`:

```go
// Package plugin compiles configured gateway plugins into immutable request
// and response hook chains.
//
// Chains run request hooks in ascending request order and response hooks in
// ascending response order, using plugin names as deterministic tie-breakers.
package plugin
```

- [ ] **Step 5: Document actions, hooks, and chain execution**

Document `Action`, both constants, response/result types, hook interfaces, and
all `Chain` methods. State:

- a request hook either continues, short-circuits with a validated response, or
  returns an error;
- short-circuit headers and bodies are cloned and bounded;
- response hooks run only for entries present in the compiled response chain;
- `Names` returns a copy;
- nil chains behave as empty chains;
- scratch slots are contiguous and owned by the request context.

- [ ] **Step 6: Document registry and built-in hooks**

Document registry types/functions and the exported `OnRequest`, `OnResponse`,
and `UnmarshalJSON` methods implemented by header-rewrite and request-ID
plugins. Explain compile-time validation, deterministic ordering, immutability
of compiled values, request-ID replacement rules, and response restoration
without exposing implementation-only concrete types as public API.

- [ ] **Step 7: Format and verify**

```powershell
gofmt -w internal/router/doc.go internal/router/pattern.go internal/router/query.go internal/router/router.go internal/plugin/doc.go internal/plugin/chain.go internal/plugin/header_rewrite.go internal/plugin/registry.go internal/plugin/request_id.go
& (Join-Path (go env GOPATH) 'bin\staticcheck.exe') -tests=false ./internal/router ./internal/plugin
go vet ./internal/router ./internal/plugin
go test ./internal/router ./internal/plugin -count=1
go doc -all ./internal/router
go doc -all ./internal/plugin
git diff --check
```

Expected: both packages are documentation-clean and all router/plugin tests
pass.

- [ ] **Step 8: Commit**

```powershell
git add internal/router internal/plugin
git commit -m "docs: document router and plugin contracts"
```

---

### Task 4: Document Upstream Normalization and Selection Kernels

**Files:**
- Create: `internal/upstream/doc.go`
- Modify: `internal/upstream/config.go`
- Modify: `internal/upstream/endpoint.go`
- Modify: `internal/upstream/fingerprint.go`
- Modify: `internal/upstream/hashkey.go`
- Modify: `internal/upstream/chash.go`
- Modify: `internal/upstream/wrr.go`
- Read for contract evidence: `internal/upstream/config_test.go`
- Read for contract evidence: `internal/upstream/endpoint_test.go`
- Read for contract evidence: `internal/upstream/fingerprint_test.go`
- Read for contract evidence: `internal/upstream/hashkey_test.go`
- Read for contract evidence: `internal/upstream/chash_test.go`
- Read for contract evidence: `internal/upstream/wrr_test.go`

**Interfaces:**
- Consumes: documented model upstream resources.
- Produces: canonical normalization and deterministic selection contracts used by upstream plans and the registry.

**Required declaration coverage:**
- Limits: `MaxUpstreams`, `MaxUpstreamEndpoints`, `MaxEndpointWeight`, `MaxWRRSchedule`, `MaxContinuumPoints`, `MaxHashKeySources`, `MaxSnapshotEndpoints`, `MaxSnapshotWRRSlots`, `MaxSnapshotHashPoints`.
- Error/API: `ConfigError`, `ConfigError.Error`, `ConfigError.Unwrap`, `Normalize`.
- Heap-interface methods: exported `Len`, `Less`, `Swap`, `Push`, and `Pop` methods in `wrr.go`.

- [ ] **Step 1: Add the upstream package contract**

Create `internal/upstream/doc.go`:

```go
// Package upstream compiles canonical upstream resources into immutable,
// health-aware selection plans backed by shared transport runtimes.
//
// Registry candidates acquire runtime ownership transactionally. Plans remain
// valid while their owning snapshot lease is held and release resources only
// after retirement and final lease release.
package upstream
```

- [ ] **Step 2: Prove this slice fails**

```powershell
& (Join-Path (go env GOPATH) 'bin\staticcheck.exe') -tests=false ./internal/upstream
& (Join-Path (go env GOPATH) 'bin\revive.exe') -set_exit_status -config revive.toml -formatter default ./internal/upstream/...
```

Expected: Staticcheck is clean after the package comment. Revive fails for
normalization, selection, health, and lifecycle declarations that remain
undocumented.

- [ ] **Step 3: Document limits, configuration errors, and normalization**

Document every limit separately with its unit and enforcement scope.
Document `ConfigError` as a stable coded/path-aware configuration error,
including unwrap behavior. Document `Normalize` as returning a canonical
top-level resource slice or an error with no usable partial result. State that
normalization may rewrite nested input-owned slices and pointer policies, so a
caller that must preserve the input must clone it first. Cover endpoint URL
canonicalization, deterministic sorting, default policies, duplicate rejection,
and global budget enforcement.

- [ ] **Step 4: Explain endpoint and fingerprint invariants**

Add focused implementation comments stating:

- endpoint identity includes upstream ID and canonical URL but excludes weight;
- weight-only updates may therefore reuse endpoint runtime state;
- health fingerprints change with health policy but not endpoint weight;
- retry-budget fingerprints include only budget semantics.

Do not expose fingerprint formats as stable public contracts.

- [ ] **Step 5: Explain hash-key and consistent-hash invariants**

Add focused comments for:

- length-prefixed components preventing compound-key ambiguity;
- source order being semantically significant;
- explicit missing markers and fallback only when all dynamic sources are
  absent;
- deterministic point sorting/collision tie-breaking;
- bounded continuum size while retaining every active endpoint.

- [ ] **Step 6: Document WRR interface methods and schedule invariants**

Document exported heap-interface methods even though their receiver types are
internal. Explain that ordering uses deadline first and canonical endpoint
identity as a deterministic tie-breaker. Add an implementation comment for the
bounded schedule and single-endpoint fast path.

- [ ] **Step 7: Format and verify the slice**

```powershell
gofmt -w internal/upstream/doc.go internal/upstream/config.go internal/upstream/endpoint.go internal/upstream/fingerprint.go internal/upstream/hashkey.go internal/upstream/chash.go internal/upstream/wrr.go
& (Join-Path (go env GOPATH) 'bin\staticcheck.exe') -tests=false ./internal/upstream
$diagnostics = @(& (Join-Path (go env GOPATH) 'bin\revive.exe') -set_exit_status -config revive.toml -formatter default ./internal/upstream/... 2>&1)
$completedFileDiagnostics = @($diagnostics | Where-Object {
    $_ -match 'internal[\\/]upstream[\\/](doc|config|endpoint|fingerprint|hashkey|chash|wrr)\.go:'
})
if ($completedFileDiagnostics.Count -ne 0) {
    throw ($completedFileDiagnostics -join "`n")
}
go vet ./internal/upstream
go test ./internal/upstream -run 'Normalize|Endpoint|Fingerprint|HashKey|Continuum|WRR' -count=1
go doc ./internal/upstream ConfigError
go doc ./internal/upstream Normalize
git diff --check
```

Expected: Staticcheck is clean. Revive still reports only declarations assigned
to Tasks 5 and 6; no diagnostics may point to files completed in this task.

- [ ] **Step 8: Commit**

```powershell
git add internal/upstream/doc.go internal/upstream/config.go internal/upstream/endpoint.go internal/upstream/fingerprint.go internal/upstream/hashkey.go internal/upstream/chash.go internal/upstream/wrr.go
git commit -m "docs: document upstream normalization and selection"
```

---

### Task 5: Document Upstream Health, Probes, and Retry Budgets

**Files:**
- Modify: `internal/upstream/budget.go`
- Modify: `internal/upstream/health.go`
- Modify: `internal/upstream/health_scheduler.go`
- Modify: `internal/upstream/observer.go`
- Modify: `internal/upstream/probe.go`
- Modify: `internal/upstream/probe_http.go`
- Modify: `internal/upstream/probe_tcp.go`
- Read for contract evidence: matching `*_test.go` and fuzz tests in `internal/upstream`

**Interfaces:**
- Consumes: normalized policies and endpoint generations from Task 4.
- Produces: documented concurrent health, scheduling, observation, and retry-budget contracts for plans and telemetry.

**Required declaration coverage:**
- Retry: `RetryPermit` and all seven exported retry-budget/permit methods.
- Health: `HealthState`, its three constants, `ObservationSource`, its two
  constants, `OutcomeKind`, its five constants, `Observation`,
  `HealthTransition`, `EndpointHealth`, and all exported health methods.
- Scheduling: `HealthCoordinatorStats`, `HealthCoordinator`, heap-interface
  methods, `Register`, `ActivateHealth`, `Retire`, `Stats`, `StopHealth`,
  `Close`.
- Observation: `PrepareStats`, `CleanupStats`, `RegistryStats`,
  `ResilienceStats`, `Observer`.
- Probing: `ProbeTarget`, `ProbeResult`, `Prober`, and exported `Probe` and
  `CloseIdleConnections` methods for HTTP and TCP probers.

- [ ] **Step 1: Inventory remaining diagnostics for this slice**

```powershell
& (Join-Path (go env GOPATH) 'bin\staticcheck.exe') -tests=false ./internal/upstream
& (Join-Path (go env GOPATH) 'bin\revive.exe') -set_exit_status -config revive.toml -formatter default ./internal/upstream/...
```

Expected: diagnostics in the seven files listed for modification and the
lifecycle files reserved for Task 6.

- [ ] **Step 2: Document retry permits and budget behavior**

Document permit ownership, single-release requirement, double-release panic,
fixed-point credits, burst cap, maximum in-flight retry cap, nil/disabled
behavior, and concurrency safety. Distinguish primary-attempt credits from retry
acquisition.

- [ ] **Step 3: Document health state and observations**

Document every enum and value, `Observation`, `HealthTransition`, and
`EndpointHealth`. Cover:

- initial state and selectability;
- active/passive source handling;
- failure and success thresholds;
- active-only recovery behavior;
- ignored observations after retirement;
- transition-hook invocation and generation identity;
- concurrent safety.

- [ ] **Step 4: Document coordinator lifecycle and heap methods**

Document fixed worker/queue ownership, lazy activation, generation-based stale
work rejection, idempotent retirement, bounded stats, cancellation, and close
semantics. Document exported heap-interface methods as internal scheduler
support rather than general collection APIs.

- [ ] **Step 5: Document observer payloads and interface**

For every stats field, state whether it is a delta for the current transaction
or a current gauge after cleanup. Document `Observer` callback ordering,
bounded payload expectations, and the registry's panic isolation around
observer implementations.

- [ ] **Step 6: Document probe contracts**

Document:

- `ProbeTarget` generation and policy ownership;
- `ProbeResult` classification;
- `Prober` cancellation contract;
- HTTP redirect suppression, configured success/failure statuses, timeout
  classification, and distinct probe transport ownership;
- TCP reachability semantics and the fact that TCP success does not establish
  HTTP/application health;
- idempotent idle-connection cleanup.

- [ ] **Step 7: Format and verify this slice**

```powershell
gofmt -w internal/upstream/budget.go internal/upstream/health.go internal/upstream/health_scheduler.go internal/upstream/observer.go internal/upstream/probe.go internal/upstream/probe_http.go internal/upstream/probe_tcp.go
& (Join-Path (go env GOPATH) 'bin\staticcheck.exe') -tests=false ./internal/upstream
$diagnostics = @(& (Join-Path (go env GOPATH) 'bin\revive.exe') -set_exit_status -config revive.toml -formatter default ./internal/upstream/... 2>&1)
$completedFileDiagnostics = @($diagnostics | Where-Object {
    $_ -match 'internal[\\/]upstream[\\/](budget|health|health_scheduler|observer|probe|probe_http|probe_tcp)\.go:'
})
if ($completedFileDiagnostics.Count -ne 0) {
    throw ($completedFileDiagnostics -join "`n")
}
go vet ./internal/upstream
go test ./internal/upstream -run 'RetryBudget|EndpointHealth|HealthCoordinator|Prober|Probe|Observer' -count=1
go test ./internal/upstream -run '^$' -fuzz FuzzEndpointHealth -fuzztime 10s
go doc ./internal/upstream EndpointHealth
go doc ./internal/upstream Observer
git diff --check
```

Expected: Staticcheck is clean and remaining Revive diagnostics are confined to
Task 6 lifecycle files; targeted tests and fuzzing pass.

- [ ] **Step 8: Commit**

```powershell
git add internal/upstream/budget.go internal/upstream/health.go internal/upstream/health_scheduler.go internal/upstream/observer.go internal/upstream/probe.go internal/upstream/probe_http.go internal/upstream/probe_tcp.go
git commit -m "docs: document upstream health and retry contracts"
```

---

### Task 6: Document Upstream Registry, Plan Ownership, and Retirement

**Files:**
- Modify: `internal/upstream/plan.go`
- Modify: `internal/upstream/registry.go`
- Modify: `internal/upstream/reaper.go`
- Modify: `internal/upstream/transport.go`
- Read for contract evidence: `internal/upstream/plan_test.go`
- Read for contract evidence: `internal/upstream/registry_test.go`
- Read for contract evidence: `internal/upstream/reaper_test.go`
- Read for contract evidence: `internal/upstream/runtime_test.go`
- Read for contract evidence: `internal/upstream/transport_test.go`

**Interfaces:**
- Consumes: selection, health, probe, and budget contracts from Tasks 4 and 5.
- Produces: a fully documentation-clean `internal/upstream` package and stable lifecycle contracts for runtime snapshots.

**Required declaration coverage:**
- Errors and attempts: `ErrNoEndpoint`, `ErrNoHealthyEndpoint`, `AttemptSet`,
  `AttemptSet.Add`, `AttemptSet.Contains`.
- Plans and selection: `Plan`, all plan methods, `Selection`, all selection
  methods, `PlanSet`, `PlanSet.Plan`, `TryAcquire`, `Release`, `Retire`.
- Registry: `RegistryOptions`, `Registry`, `Candidate`, `NewRegistry`,
  `Registry.Prepare`, `Stats`, `ResilienceStats`, `HealthCoordinatorStats`,
  `Close`, `StopHealth`, and candidate `Plan`, `Commit`, `Rollback`.
- Transport: exported `RoundTrip` and `CloseIdleConnections` methods.

- [ ] **Step 1: Prove the final upstream slice still fails**

```powershell
& (Join-Path (go env GOPATH) 'bin\staticcheck.exe') -tests=false ./internal/upstream
& (Join-Path (go env GOPATH) 'bin\revive.exe') -set_exit_status -config revive.toml -formatter default ./internal/upstream/...
```

Expected: FAIL only for declarations in `plan.go`, `registry.go`, and
`transport.go`.

- [ ] **Step 2: Document attempt and selection semantics**

Document the bounded five-ordinal `AttemptSet`, duplicate rejection, nil
behavior, fail-closed selection, attempted/unhealthy exclusion, consistent-hash
fallback reporting, target URL ownership, selection validity, transport errors,
and passive health observation.

- [ ] **Step 3: Document plan behavior**

Document that a `Plan` is immutable after preparation and safe for concurrent
request selection, while referenced health/budget runtimes are internally
synchronized. Cover health activation, primary credit, retry permits, selection
algorithms, and sentinel errors.

- [ ] **Step 4: Document PlanSet ownership**

Document:

- one initial ownership reference;
- `TryAcquire` only while live;
- every successful acquire requires exactly one `Release`;
- nil or underflow release panics;
- `Retire` is idempotent and drops the owner reference;
- final release schedules asynchronous cleanup rather than closing request-path
  resources inline.

- [ ] **Step 5: Document registry and candidate transactions**

Document constructor bounds and background ownership, concurrency safety,
transactional `Prepare`, candidate exclusivity, commit/rollback idempotence,
resource reuse keys, last-known-good behavior, retired-generation backpressure,
health shutdown, and context-aware final close. State which methods may return
stable `ConfigError` values.

- [ ] **Step 6: Explain reaper and transport invariants**

Add focused implementation comments for:

- non-blocking reaper wake-up;
- cleanup only after final plan-set reference release;
- exactly-once resource-reference decrement;
- closing idle connections outside the registry mutex;
- transport wrapper ownership and idempotent idle cleanup.

- [ ] **Step 7: Complete upstream verification**

```powershell
gofmt -w internal/upstream/plan.go internal/upstream/registry.go internal/upstream/reaper.go internal/upstream/transport.go
& (Join-Path (go env GOPATH) 'bin\staticcheck.exe') -tests=false ./internal/upstream
& (Join-Path (go env GOPATH) 'bin\revive.exe') -set_exit_status -config revive.toml -formatter default ./internal/upstream/...
go vet ./internal/upstream
go test ./internal/upstream -count=1
go test -race ./internal/upstream -run 'Registry|Reaper|Coordinator|RetryBudget' -count=1
go doc -all ./internal/upstream
git diff --check
```

Expected: Staticcheck and Revive print no diagnostics for `internal/upstream`;
all unit and targeted race tests pass.

- [ ] **Step 8: Commit**

```powershell
git add internal/upstream/plan.go internal/upstream/registry.go internal/upstream/reaper.go internal/upstream/transport.go
git commit -m "docs: document upstream lifecycle and ownership"
```

---

### Task 7: Document Runtime Snapshots and Proxy Request Execution

**Files:**
- Create: `internal/runtime/doc.go`
- Modify: `internal/runtime/builder.go`
- Modify: `internal/runtime/errors.go`
- Modify: `internal/runtime/manager.go`
- Modify: `internal/runtime/snapshot.go`
- Create: `internal/proxy/doc.go`
- Modify: `internal/proxy/handler.go`
- Modify: `internal/proxy/route_transport.go`
- Read for invariants: `internal/proxy/headers.go`
- Read for invariants: `internal/proxy/retry.go`
- Read for contract evidence: matching tests in both packages

**Interfaces:**
- Consumes: documented router, plugin, request-context, and upstream contracts.
- Produces: documented atomic snapshot lifecycle and HTTP request-path behavior for gateway composition.

**Required declaration coverage:**
- Runtime: `Builder`, `NewBuilder`, `Builder.Build`, `BuildStage`, all four
  stage constants, `BuildError`, its methods, `Observer`, `Manager`, `Lease`,
  constructor and manager methods, `Lease.Snapshot`, `Lease.Release`, `Stats`,
  `CompiledRoute`, `Snapshot`, `Match`, and all exported snapshot/route methods.
- Proxy: `RuntimeOptions`, `NewRuntime`, exported `Error` and `Unwrap` on the
  response-plugin wrapper, `ServeHTTP`, `Allow`, and exported `RoundTrip` on
  the route transport.
- Audit 21 exported fields across both packages.

- [ ] **Step 1: Prove both package baselines fail**

```powershell
& (Join-Path (go env GOPATH) 'bin\staticcheck.exe') -tests=false ./internal/runtime ./internal/proxy
& (Join-Path (go env GOPATH) 'bin\revive.exe') -set_exit_status -config revive.toml -formatter default ./internal/runtime/... ./internal/proxy/...
```

Expected: FAIL with package and declaration diagnostics.

- [ ] **Step 2: Add runtime package documentation**

Create `internal/runtime/doc.go`:

```go
// Package runtime compiles canonical resources into immutable request
// snapshots and publishes them atomically through a lease-based manager.
//
// A successful lease retains one snapshot revision and its upstream plans
// until Release. Failed builds and stale revisions leave the active snapshot
// unchanged.
package runtime
```

- [ ] **Step 3: Document builder and build errors**

Document constructor requirements, build-stage values, wrapping behavior,
revision validation, reference resolution, plugin/router/upstream candidate
coordination, and rollback on any build failure.

- [ ] **Step 4: Document manager and lease lifecycle**

Document manager concurrency safety, serialized apply order, stale-revision
rejection, atomic publication, observer ordering, health stop, context-aware
close, acquire-not-ready behavior, lease ownership, exactly-once release, and
panic behavior for invalid release where present.

- [ ] **Step 5: Document immutable snapshots and compiled routes**

Document stats units, snapshot match semantics, route metadata pointer
lifetimes, scratch sizing, plugin execution, retry policy immutability, upstream
activation/selection, and the requirement that all returned route/snapshot data
remain within the lease lifetime.

- [ ] **Step 6: Add proxy package documentation**

Create `internal/proxy/doc.go`:

```go
// Package proxy executes the gateway HTTP request path against leased runtime
// snapshots.
//
// It matches routes, runs compiled plugins, applies bounded retry and timeout
// policy, streams request and response bodies, and maps failures to stable
// gateway responses.
package proxy
```

- [ ] **Step 7: Document handler construction and stable errors**

Document required options, maximum-body semantics, logger/snapshot
preconditions, error unwrap behavior, 404 versus 405 behavior, sorted `Allow`,
deadline precedence, streaming/cancellation ownership, response commitment, and
stable failure mapping.

- [ ] **Step 8: Document attempt transport**

Document exported `RoundTrip` as one gateway retry transaction over distinct
eligible endpoints. Cover replayability, bounded response draining, retry
budget ownership, total deadline, request cloning, attempt observation, and the
returned final response/error contract.

- [ ] **Step 9: Format and verify**

```powershell
gofmt -w internal/runtime/doc.go internal/runtime/builder.go internal/runtime/errors.go internal/runtime/manager.go internal/runtime/snapshot.go internal/proxy/doc.go internal/proxy/handler.go internal/proxy/route_transport.go
& (Join-Path (go env GOPATH) 'bin\staticcheck.exe') -tests=false ./internal/runtime ./internal/proxy
& (Join-Path (go env GOPATH) 'bin\revive.exe') -set_exit_status -config revive.toml -formatter default ./internal/runtime/... ./internal/proxy/...
go vet ./internal/runtime ./internal/proxy
go test ./internal/runtime ./internal/proxy -count=1
go doc -all ./internal/runtime
go doc -all ./internal/proxy
git diff --check
```

Expected: both tools report clean packages and tests pass.

- [ ] **Step 10: Commit**

```powershell
git add internal/runtime internal/proxy
git commit -m "docs: document runtime and proxy contracts"
```

---

### Task 8: Document Telemetry and Gateway Lifecycle

**Files:**
- Create: `internal/telemetry/doc.go`
- Modify: `internal/telemetry/resilience.go`
- Modify: `internal/telemetry/telemetry.go`
- Create: `internal/gateway/doc.go`
- Modify: `internal/gateway/gateway.go`
- Modify: `internal/gateway/lifecycle_observer.go`
- Modify: `internal/gateway/response_state.go`
- Read for contract evidence: matching tests in both packages
- Read for lifecycle evidence: `test/integration/process_test.go`

**Interfaces:**
- Consumes: documented runtime manager, proxy handler, and upstream observer contracts.
- Produces: documented process composition, observability, readiness, startup, apply, wait, and shutdown behavior.

**Required declaration coverage:**
- Telemetry: `ResilienceStatsProvider`, its collector methods,
  `RegisterResilienceProvider`, `Telemetry`, `New`, readiness/admin/wrap
  methods, snapshot/registry observer methods, and response-writer methods.
- Gateway: `Addresses`, `Gateway`, `New`, `Apply`, `Start`, `Wait`,
  `Shutdown`, lifecycle-observer methods, and response-state writer methods.

- [ ] **Step 1: Prove both baselines fail**

```powershell
& (Join-Path (go env GOPATH) 'bin\staticcheck.exe') -tests=false ./internal/telemetry ./internal/gateway
& (Join-Path (go env GOPATH) 'bin\revive.exe') -set_exit_status -config revive.toml -formatter default ./internal/telemetry/... ./internal/gateway/...
```

Expected: FAIL with missing package and declaration docs.

- [ ] **Step 2: Add telemetry package documentation**

Create `internal/telemetry/doc.go`:

```go
// Package telemetry exposes bounded gateway health, readiness, request,
// runtime, and upstream metrics through the private admin handler.
//
// Metric names and label names are fixed by construction. Request metrics use
// the observed HTTP method and configuration-bounded route and upstream IDs;
// lifecycle and resilience metrics avoid raw endpoints, revisions, error text,
// request paths, and request hosts as label values.
package telemetry
```

- [ ] **Step 3: Document collectors, readiness, and response wrapping**

Document provider registration uniqueness, Prometheus collector contracts,
readiness transitions, admin handler ownership, optional pprof/request metrics,
observed method and configured route/upstream ID labels, forbidden raw request
and error labels, observer gauge/delta behavior, and optional response-writer
interface preservation through `Unwrap`.

- [ ] **Step 4: Add gateway package documentation**

Create `internal/gateway/doc.go`:

```go
// Package gateway composes configuration, runtime snapshots, proxying,
// telemetry, listeners, and graceful process lifecycle into one data-plane
// instance.
//
// New validates and activates the initial snapshot before Start binds
// listeners. Shutdown removes readiness before draining traffic and releasing
// runtime resources.
package gateway
```

- [ ] **Step 5: Document construction, apply, start, wait, and shutdown**

Document:

- `New` requires a non-nil logger, loads the TLS key pair, builds all runtime
  components, and returns no partially usable gateway on error;
- `Start` binds admin before traffic, is single-use, and returns actual bound
  addresses;
- `Apply` is disabled after shutdown begins and preserves last-known-good state
  on rejection;
- `Wait` reports unexpected server termination;
- `Shutdown` is idempotent and readiness-first; its caller context bounds
  graceful server shutdown, while deadline fallback force-closes traffic,
  waits for tracked handlers, and gives runtime cleanup a separate two-second
  context before final admin cleanup;
- a `Gateway` must not be copied after first use and is safe only through its
  documented methods.

- [ ] **Step 6: Document observer and response-state methods**

Document lifecycle observer callbacks as bounded metric/log forwarding with
panic-free behavior. Document response-state writer semantics, first-status
capture, implicit status on `Write`, and optional-interface recovery through
`Unwrap`.

- [ ] **Step 7: Format and verify**

```powershell
gofmt -w internal/telemetry/doc.go internal/telemetry/resilience.go internal/telemetry/telemetry.go internal/gateway/doc.go internal/gateway/gateway.go internal/gateway/lifecycle_observer.go internal/gateway/response_state.go
& (Join-Path (go env GOPATH) 'bin\staticcheck.exe') -tests=false ./internal/telemetry ./internal/gateway
& (Join-Path (go env GOPATH) 'bin\revive.exe') -set_exit_status -config revive.toml -formatter default ./internal/telemetry/... ./internal/gateway/...
go vet ./internal/telemetry ./internal/gateway
go test ./internal/telemetry ./internal/gateway -count=1
go doc -all ./internal/telemetry
go doc -all ./internal/gateway
git diff --check
```

Expected: both tools report clean packages and lifecycle/telemetry tests pass.

- [ ] **Step 8: Commit**

```powershell
git add internal/telemetry internal/gateway
git commit -m "docs: document telemetry and gateway lifecycle"
```

---

### Task 9: Document Deterministic Benchmark and Test Tooling

**Files:**
- Create: `internal/benchdataset/doc.go`
- Modify: `internal/benchdataset/generator.go`
- Modify: `internal/benchdataset/render.go`
- Modify: `internal/benchreport/report.go`
- Modify: `internal/testupstream/server.go`
- Read for contract evidence: matching tests in all three packages
- Read for artifact semantics: `bench/README.md`

**Interfaces:**
- Consumes: model resources and benchmark artifact schemas.
- Produces: documented deterministic dataset, evidence-summary, and test-upstream contracts for command packages.

**Required declaration coverage:**
- Bench dataset: `SchemaVersion`, option/count/sentinel/metadata types,
  `Generate`, `GatewayRenderOptions`, `RenderGateway`, `RenderAPISIX`.
- Bench report: `Verdict`, five verdict/environment constants,
  `ErrInvalidEvidence`, options/summary/comparison types, `Generate`.
- Test upstream: `New`, exported `ServeHTTP`.
- Audit 63 exported fields across the benchmark packages.

- [ ] **Step 1: Prove all three package baselines fail**

```powershell
& (Join-Path (go env GOPATH) 'bin\staticcheck.exe') -tests=false ./internal/benchdataset ./internal/benchreport ./internal/testupstream
& (Join-Path (go env GOPATH) 'bin\revive.exe') -set_exit_status -config revive.toml -formatter default ./internal/benchdataset/... ./internal/benchreport/... ./internal/testupstream/...
```

Expected: FAIL; `benchreport` has a package comment but its declarations remain
undocumented.

- [ ] **Step 2: Add the benchdataset package contract**

Create `internal/benchdataset/doc.go`:

```go
// Package benchdataset generates deterministic, equivalent gateway and APISIX
// benchmark datasets plus metadata describing their distribution.
//
// Generation and rendering do not mutate caller-owned model resources.
package benchdataset
```

- [ ] **Step 3: Document generation and rendering**

Document schema version stability, option bounds, exact count meanings,
sentinel equivalence, metadata totals, deterministic ordering, independent
ownership, strict gateway version rendering, APISIX translation, and error/no
partial-output behavior. Audit all 33 exported fields.

- [ ] **Step 4: Complete benchreport declaration docs**

Preserve and refine the existing package comment only if needed. Document
verdict/environment constants, sentinel error matching, input paths, minimum
run counts, percentile/median semantics, provisional versus dedicated
environment behavior, comparison ratios, and invalid-evidence handling. Audit
all 30 exported fields.

- [ ] **Step 5: Add testupstream package and handler docs**

Add above `package testupstream`:

```go
// Package testupstream provides a deterministic HTTP handler for gateway
// correctness, streaming, cancellation, connection, and lifecycle tests.
package testupstream
```

Document `New` as returning an independently usable handler and `ServeHTTP` as
implementing the package's fixed endpoint contract. Point detailed endpoint
behavior to the package comment only when necessary; do not paste the full test
matrix into both comments.

- [ ] **Step 6: Format and verify**

```powershell
gofmt -w internal/benchdataset/doc.go internal/benchdataset/generator.go internal/benchdataset/render.go internal/benchreport/report.go internal/testupstream/server.go
& (Join-Path (go env GOPATH) 'bin\staticcheck.exe') -tests=false ./internal/benchdataset ./internal/benchreport ./internal/testupstream
& (Join-Path (go env GOPATH) 'bin\revive.exe') -set_exit_status -config revive.toml -formatter default ./internal/benchdataset/... ./internal/benchreport/... ./internal/testupstream/...
go vet ./internal/benchdataset ./internal/benchreport ./internal/testupstream
go test ./internal/benchdataset ./internal/benchreport ./internal/testupstream -count=1
go doc -all ./internal/benchdataset
go doc -all ./internal/benchreport
go doc -all ./internal/testupstream
git diff --check
```

Expected: both tools report clean tooling packages and deterministic tests
pass.

- [ ] **Step 7: Commit**

```powershell
git add internal/benchdataset internal/benchreport/report.go internal/testupstream/server.go
git commit -m "docs: document deterministic tooling"
```

---

### Task 10: Document Commands and Enable the Repository-Wide Gate

**Files:**
- Modify: `cmd/bench-dataset/main.go`
- Modify: `cmd/bench-report/main.go`
- Modify: `cmd/gateway-dp/main.go`
- Modify: `cmd/test-upstream/main.go`
- Modify: `README.md`
- Modify: `.github/workflows/ci.yml`
- Verify: `staticcheck.conf`
- Verify: `revive.toml`

**Interfaces:**
- Consumes: all documentation-clean internal packages from Tasks 1–9.
- Produces: documented command packages, canonical local instructions, and a mandatory CI documentation gate over all production source.

- [ ] **Step 1: Prove only command package comments remain**

Run:

```powershell
& (Join-Path (go env GOPATH) 'bin\staticcheck.exe') -tests=false ./...
& (Join-Path (go env GOPATH) 'bin\revive.exe') -set_exit_status -config revive.toml -formatter default ./...
```

Expected: both gates pass. Staticcheck exempts command packages from `ST1000`,
and the narrow Revive gate checks exported declarations rather than package
comments, so the four command comments remain an explicit inventory item. Any
diagnostic under `internal/` must be fixed in its owning earlier task before
continuing.

- [ ] **Step 2: Document each command package**

Add one command comment directly above each `package main`:

```go
// Package main implements the bench-dataset command, which generates
// deterministic equivalent G-Gateway and APISIX benchmark configuration
// artifacts.
package main
```

```go
// Package main implements the bench-report command, which validates benchmark
// evidence and renders deterministic comparison summaries and verdicts.
package main
```

```go
// Package main implements the gateway-dp command, which runs the G-Gateway data
// plane from a versioned configuration file until shutdown or an unrecoverable
// listener error.
package main
```

```go
// Package main implements the test-upstream command, which runs the
// deterministic HTTP upstream used by correctness and integration tests.
package main
```

- [ ] **Step 3: Update canonical local verification**

In `README.md`, add the pinned install command:

```bash
go install honnef.co/go/tools/cmd/staticcheck@2026.1
go install github.com/mgechev/revive@v1.15.0
```

Update the canonical verification pipeline to include:

```bash
test -z "$(gofmt -l .)" &&
staticcheck -tests=false ./... &&
revive -set_exit_status -config revive.toml -formatter default ./... &&
go vet ./... &&
go test ./... -race -count=1 &&
go build ./cmd/...
```

State that root `staticcheck.conf` intentionally enables only package and
existing-comment form checks, while `revive.toml` enables only the missing
exported-declaration check.

- [ ] **Step 4: Add the CI installation and documentation steps**

After `actions/setup-go` and before `go vet`, add:

```yaml
      - name: Install Staticcheck
        run: go install honnef.co/go/tools/cmd/staticcheck@2026.1
      - name: Install Revive
        run: go install github.com/mgechev/revive@v1.15.0
      - name: Check Go documentation
        run: |
          staticcheck -tests=false ./...
          revive -set_exit_status -config revive.toml -formatter default ./...
```

Do not replace or remove the existing format, vet, test, race, command-build,
or Docker-build steps.

- [ ] **Step 5: Format and run the global documentation gate**

```powershell
gofmt -w cmd/bench-dataset/main.go cmd/bench-report/main.go cmd/gateway-dp/main.go cmd/test-upstream/main.go
& (Join-Path (go env GOPATH) 'bin\staticcheck.exe') -tests=false ./...
& (Join-Path (go env GOPATH) 'bin\revive.exe') -set_exit_status -config revive.toml -formatter default ./...
go doc ./cmd/bench-dataset
go doc ./cmd/bench-report
go doc ./cmd/gateway-dp
go doc ./cmd/test-upstream
git diff --check
```

Expected: Staticcheck and Revive print no diagnostics and all command docs
render with their binary purpose.

- [ ] **Step 6: Run the canonical repository verification**

Run:

```powershell
$unformatted = @(gofmt -l .)
if ($unformatted.Count -ne 0) { throw "Unformatted files: $($unformatted -join ', ')" }
& (Join-Path (go env GOPATH) 'bin\staticcheck.exe') -tests=false ./...
& (Join-Path (go env GOPATH) 'bin\revive.exe') -set_exit_status -config revive.toml -formatter default ./...
go vet ./...
go test ./... -count=1
go test ./... -race -count=1
go build ./cmd/...
docker build --build-arg COMMAND=gateway-dp -t gateway-go:ci .
```

Expected:

- format check returns no files;
- Staticcheck, Revive, and vet print no diagnostics;
- normal and race test suites pass;
- every command builds;
- the gateway Docker image builds.

If race or Docker cannot run because of the local environment, capture the exact
command and error in the task handoff and rely on GitHub Actions for the
canonical Linux result. Do not report that blocked gate as passed.

- [ ] **Step 7: Review the complete documentation diff**

Run:

```powershell
git diff --stat 86bab89..HEAD
git diff --check
git status --short
```

Review criteria:

- all changes are comments, lint configuration, README instructions, or CI
  wiring;
- no Go signature, expression, statement, tag, literal, or control flow changed;
- package comments are unique;
- no comment merely repeats the next declaration;
- rendered lists and links follow Go doc syntax;
- CI and README use the identical Staticcheck and Revive versions and commands.

- [ ] **Step 8: Commit**

```powershell
git add cmd/bench-dataset/main.go cmd/bench-report/main.go cmd/gateway-dp/main.go cmd/test-upstream/main.go README.md .github/workflows/ci.yml
git commit -m "ci: enforce Go source documentation"
```

---

## Final Acceptance

The implementation is complete only when all ten task commits exist and:

```powershell
& (Join-Path (go env GOPATH) 'bin\staticcheck.exe') -tests=false ./...
& (Join-Path (go env GOPATH) 'bin\revive.exe') -set_exit_status -config revive.toml -formatter default ./...
go vet ./...
go test ./... -count=1
go test ./... -race -count=1
go build ./cmd/...
```

all pass, the Docker gate passes locally or in GitHub Actions, and the final
review confirms that no runtime or API behavior changed.

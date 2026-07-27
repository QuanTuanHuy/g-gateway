# Phase 3B Health, Timeout, and Replay-Safe Retry Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add bounded active/passive upstream health, true total request deadlines, replay-safe multi-endpoint retry, and adaptive per-upstream retry budgets without moving mutable state into immutable snapshots or regressing Phase 3A pool reuse.

**Architecture:** `upstream.Registry` owns reference-counted endpoint-health and retry-budget runtimes beside the existing endpoint, selection, and transport runtimes. A single bounded `HealthCoordinator` schedules HTTP/TCP probes, while `proxy.routeTransport` performs the request-local attempt loop against an immutable effective route policy and the shared runtime handles held by the request's leased plan.

**Tech Stack:** Go 1.26.5, `net/http`, `httputil.ReverseProxy`, `sync/atomic`, `container/heap`, Prometheus client_golang, strict YAML v3, injected clocks, Go unit/property/fuzz/race tests, `httptest`, and Go benchmarks.

## Global Constraints

- Execute on `codex/phase3b-health-timeout-retry-design`; do not create or use a Git worktree.
- Preserve strict `gateway/v1alpha1`, `gateway/v1alpha2`, and `gateway/v1alpha3` behavior while adding strict `gateway/v1alpha4`.
- Legacy resources keep health disabled, `max_attempts=1`, and no new total timeout.
- New v1alpha4 defaults are `max_attempts=1`, methods `GET/HEAD/OPTIONS`, total timeout `30s`, retry ratio `100/1000`, burst `10`, and max inflight retries `32`.
- Keep health state, health counters, scheduler state, and retry-budget state outside `RuntimeSnapshot`.
- Health transitions must not rebuild or swap a snapshot.
- Never perform YAML parsing, reference resolution, registry-map lookup, dynamic Prometheus label lookup, or global locking on the request hot path.
- All-unhealthy is fail-closed and returns `503 UPSTREAM_UNHEALTHY`.
- Never retry a request with a non-replayable body and never attempt the same endpoint twice in one request.
- `max_attempts` includes the primary attempt and is bounded to `1..5`.
- Passive health is valid only when active health is enabled.
- Active checker types in Phase 3B are exactly `http` and `tcp`.
- Active health uses one scheduler, one ready queue, and a fixed worker pool; never create one goroutine or ticker per endpoint.
- Probe workers are bounded to `1..256`; ready queue capacity is bounded to `1..65536`.
- Retry budgets are per-upstream, use fixed-point integer credits, and never exceed configured burst or inflight limits.
- Preserve shared production transport pools across route-only, positive weight-only, health-state, and unrelated-upstream changes.
- Keep HTTP/1.1 cleartext upstream transport; TLS/protocol/WebSocket work belongs to Phase 3C.
- Keep access logging and canonical APISIX comparison in Phase 3D.
- Treat developer-machine evidence as provisional and keep Phase 2 Task 16 plus Phase 3A canonical evidence visibly pending.
- Use TDD for every behavior change and commit after each task's focused suite passes.
- Use `apply_patch` for authored edits; formatting tools may perform mechanical rewrites.

## File and Responsibility Map

Create:

- `internal/config/wire_v1alpha4.go` — strict v1alpha4 wire schema, defaults, and conversion.
- `internal/upstream/health.go` — endpoint state machine and observation classification input.
- `internal/upstream/health_test.go` — deterministic transition tests.
- `internal/upstream/health_fuzz_test.go` — state-machine invariant fuzzing.
- `internal/upstream/fingerprint.go` — stable health/budget policy keys.
- `internal/upstream/fingerprint_test.go` — reuse-key stability tests.
- `internal/upstream/budget.go` — fixed-point retry token/inflight accounting.
- `internal/upstream/budget_test.go` — sequential and concurrent budget tests.
- `internal/upstream/probe.go` — prober interface and target/result types.
- `internal/upstream/probe_http.go` — isolated HTTP prober.
- `internal/upstream/probe_tcp.go` — isolated TCP prober.
- `internal/upstream/probe_test.go` — HTTP/TCP outcome and cancellation tests.
- `internal/upstream/health_scheduler.go` — bounded time heap, ready queue, and workers.
- `internal/upstream/health_scheduler_test.go` — fake-clock scheduling and saturation tests.
- `internal/proxy/retry.go` — eligibility, error/status classification, and bounded drain.
- `internal/proxy/retry_test.go` — classifier and replay tests.
- `internal/proxy/attempt_transport_test.go` — multi-attempt transport behavior.
- `internal/upstream/resilience_benchmark_test.go` — health-aware selector and budget benchmarks.
- `internal/upstream/phase3b_acceptance_test.go` — normal/full resource acceptance.
- `internal/proxy/phase3b_acceptance_test.go` — healthy end-to-end throughput and p99 comparison.
- `test/integration/upstream_resilience_test.go` — black-box failure/recovery/retry tests.
- `configs/phase3b.yaml` — runnable v1alpha4 example.
- `docs/operations/phase-3b-runbook.md` — operator contract and commands.
- `docs/benchmarks/phase-3b-current-status.md` — observed evidence ledger.

Modify:

- `internal/model/resources.go` — canonical health/retry/route override types and deep clone.
- `internal/model/resources_test.go` — clone isolation.
- `internal/config/types.go` — scheduler bootstrap settings/defaults.
- `internal/config/load.go` — v1alpha4 dispatch and legacy default normalization.
- `internal/config/load_test.go` — strict/default/compatibility matrix.
- `internal/config/validate.go` — v1alpha4 bootstrap/resource validation.
- `internal/upstream/config.go` — canonical resilience normalization and stable errors.
- `internal/upstream/config_test.go` — policy bounds/status/method tests.
- `internal/upstream/endpoint.go` — endpoint health handle.
- `internal/upstream/plan.go` — health-aware selection, activation, outcome, and budget facade.
- `internal/upstream/wrr.go` — bounded schedule scan.
- `internal/upstream/chash.go` — bounded clockwise distinct-endpoint scan.
- `internal/upstream/registry.go` — health/budget reuse, coordinator ownership, and stats.
- `internal/upstream/reaper.go` — resilience-runtime finalization.
- `internal/upstream/observer.go` — bounded lifecycle/resilience stats.
- `internal/runtime/builder.go` — effective route policy compilation.
- `internal/runtime/snapshot.go` — immutable effective policy and retry-plan methods.
- `internal/runtime/builder_test.go` — route override inheritance.
- `internal/requestctx/context.go` — final attempt/outcome fields and route interface.
- `internal/proxy/handler.go` — post-plugin total deadline and attempt initialization.
- `internal/proxy/route_transport.go` — replay-safe attempt loop and per-attempt URL rewrite.
- `internal/proxy/runtime_handler_test.go` — final status/plugin/deadline behavior.
- `internal/telemetry/telemetry.go` — bounded resilience collectors.
- `internal/telemetry/telemetry_test.go` — values and cardinality assertions.
- `internal/gateway/gateway.go` — coordinator options and shutdown order.
- `internal/gateway/lifecycle_observer.go` — transition/degradation logs.
- `internal/gateway/gateway_test.go` — constructor/shutdown cleanup.
- `internal/testupstream/server.go` — deterministic status/header-delay/stream endpoints.
- `internal/testupstream/server_test.go` — test-upstream contracts.
- `README.md` and the phase roadmap — Phase 3B status and handoff.

## Locked Cross-Task Interfaces

The canonical model introduced in Task 1 is:

```go
// internal/model/resources.go
type HealthCheckType string

const (
	HealthCheckHTTP HealthCheckType = "http"
	HealthCheckTCP  HealthCheckType = "tcp"
)

type HealthPolicy struct {
	Active  *ActiveHealthPolicy
	Passive *PassiveHealthPolicy
}

type ActiveHealthPolicy struct {
	Type                HealthCheckType
	Timeout             time.Duration
	HealthyInterval     time.Duration
	UnhealthyInterval   time.Duration
	HealthySuccesses    uint8
	HTTPFailures        uint8
	TransportFailures   uint8
	Timeouts            uint8
	HealthyStatuses     []uint16
	UnhealthyStatuses   []uint16
	Path                string
	Host                string
}

type PassiveHealthPolicy struct {
	HTTPFailures      uint8
	TransportFailures uint8
	Timeouts          uint8
	UnhealthyStatuses []uint16
}

type RetryOnPolicy struct {
	ConnectFailure        bool
	ConnectionFailure     bool
	ResponseHeaderTimeout bool
	Statuses              []uint16
}

type RetryBudgetPolicy struct {
	RatioPer1000 uint16
	Burst        uint16
	MaxInflight  uint16
}

type RetryPolicy struct {
	MaxAttempts uint8
	Methods     []string
	RetryOn     RetryOnPolicy
	Budget      RetryBudgetPolicy
	TotalTimeout time.Duration
}

type RouteResiliencePolicy struct {
	TotalTimeout *time.Duration
	MaxAttempts  *uint8
	Methods      *[]string
	RetryOn      *RetryOnPolicy
}
```

Add these fields:

```go
type Route struct {
	// existing fields
	Resilience RouteResiliencePolicy
}

type Upstream struct {
	// existing fields
	Health HealthPolicy
	Retry  RetryPolicy
}
```

The bootstrap scheduler contract is:

```go
const (
	DefaultHealthWorkers       = 16
	DefaultHealthQueueCapacity = 4096
)

type HealthRuntimeConfig struct {
	Workers            int
	ReadyQueueCapacity int
}

type RuntimeConfig struct {
	MaxRetiredSnapshots int
	Health              HealthRuntimeConfig
}
```

The upstream public contracts introduced across Tasks 3–8 are:

```go
type HealthState uint32
type ObservationSource uint8
type OutcomeKind uint8

const (
	HealthUnknown HealthState = iota
	HealthHealthy
	HealthUnhealthy
)

const (
	SourceActive ObservationSource = iota
	SourcePassive
)

const (
	OutcomeSuccess OutcomeKind = iota
	OutcomeHTTPFailure
	OutcomeTransportFailure
	OutcomeTimeout
	OutcomeNeutral
)

type Observation struct {
	Source ObservationSource
	Kind   OutcomeKind
	Status int
}

type AttemptSet struct {
	ordinals [5]uint32
	count    uint8
}

func (a *AttemptSet) Add(ordinal uint32) bool
func (a *AttemptSet) Contains(ordinal uint32) bool

type RetryPermit struct{}

func (p *RetryPermit) Release()

type RegistryOptions struct {
	MaxRetiredSnapshots int
	HealthWorkers       int
	HealthQueueCapacity int
	Observer            Observer
}

func NewRegistry(options RegistryOptions) (*Registry, error)
func (r *Registry) StopHealth()
func (p *Plan) ActivateHealth()
func (p *Plan) SelectNext(request *http.Request, attempted *AttemptSet) (Selection, error)
func (p *Plan) CreditPrimary()
func (p *Plan) AcquireRetry() (RetryPermit, bool)
func (s Selection) Observe(outcome Observation)
```

Keep `Plan.Select(*http.Request)` as a compatibility wrapper around `SelectNext(request, nil)`.

The compiled-route/request-context contract introduced in Task 9 is:

```go
// internal/runtime/snapshot.go
func (r *CompiledRoute) RetryPolicy() model.RetryPolicy
func (r *CompiledRoute) ActivateUpstream()
func (r *CompiledRoute) CreditPrimary()
func (r *CompiledRoute) AcquireRetry() (upstream.RetryPermit, bool)
func (r *CompiledRoute) SelectNext(*http.Request, *upstream.AttemptSet) (upstream.Selection, error)

// internal/runtime/manager.go
func (m *Manager) StopHealth()

// internal/requestctx/context.go
type RuntimeRoute interface {
	RetryPolicy() model.RetryPolicy
	ActivateUpstream()
	CreditPrimary()
	AcquireRetry() (upstream.RetryPermit, bool)
	SelectNext(*http.Request, *upstream.AttemptSet) (upstream.Selection, error)
	RunResponse(*Context, *http.Response) error
}
```

---

### Task 1: Add canonical Phase 3B policy types and deep cloning

**Files:**
- Modify: `internal/model/resources.go`
- Modify: `internal/model/resources_test.go`

**Interfaces:**
- Consumes: existing `Route`, `Upstream`, and `CloneResourceSet`.
- Produces: the locked canonical model declarations above.

- [ ] **Step 1: Write the failing clone-isolation test**

Add:

```go
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
				RetryOn: &RetryOnPolicy{
					Statuses: []uint16{503},
				},
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
				Passive: &PassiveHealthPolicy{
					UnhealthyStatuses: []uint16{503},
				},
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
```

- [ ] **Step 2: Run the model test to verify RED**

Run:

```powershell
go test ./internal/model -run TestCloneResourceSetClonesResiliencePolicies -count=1
```

Expected: compile failure for missing health/retry declarations.

- [ ] **Step 3: Add the locked types and clone helpers**

Add the declarations from **Locked Cross-Task Interfaces**. Clone all pointers and slices with focused helpers:

```go
func cloneUint16s(in []uint16) []uint16 {
	if in == nil {
		return nil
	}
	return append([]uint16{}, in...)
}

func cloneStrings(in []string) []string {
	if in == nil {
		return nil
	}
	return append([]string{}, in...)
}
```

Clone `Active`, `Passive`, `RetryOn`, `Methods`, and every status slice. Preserve `nil` versus non-`nil` empty route methods so an explicit empty override remains distinguishable from inheritance.

- [ ] **Step 4: Run model tests**

Run:

```powershell
go test ./internal/model -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```powershell
git add internal/model/resources.go internal/model/resources_test.go
git commit -m "feat: model phase 3b resilience policies"
```

### Task 2: Add strict gateway/v1alpha4 decoding and compatibility defaults

**Files:**
- Create: `internal/config/wire_v1alpha4.go`
- Modify: `internal/config/types.go`
- Modify: `internal/config/load.go`
- Modify: `internal/config/load_test.go`
- Modify: `internal/config/validate.go`

**Interfaces:**
- Consumes: Task 1 canonical types.
- Produces: strict v1alpha4 conversion plus scheduler bootstrap defaults.

- [ ] **Step 1: Write RED tests for v1alpha4 and legacy behavior**

Add table cases that decode:

```yaml
api_version: gateway/v1alpha4
runtime:
  max_retired_snapshots: 64
  health:
    workers: 8
    ready_queue_capacity: 512
```

with one upstream containing the approved health/retry example and one route overriding `max_attempts: 3`. Assert:

```go
if got.Runtime.Health.Workers != 8 || got.Runtime.Health.ReadyQueueCapacity != 512 {
	t.Fatalf("health runtime = %+v", got.Runtime.Health)
}
if resources.Upstreams[0].Retry.TotalTimeout != 30*time.Second {
	t.Fatalf("total timeout = %s", resources.Upstreams[0].Retry.TotalTimeout)
}
if resources.Routes[0].Resilience.MaxAttempts == nil ||
	*resources.Routes[0].Resilience.MaxAttempts != 3 {
	t.Fatalf("route resilience = %+v", resources.Routes[0].Resilience)
}
```

Add a legacy v1alpha3 assertion:

```go
if resources.Upstreams[0].Health.Active != nil ||
	resources.Upstreams[0].Retry.MaxAttempts != 1 ||
	resources.Upstreams[0].Retry.TotalTimeout != 0 {
	t.Fatalf("legacy resilience changed: %+v", resources.Upstreams[0])
}
```

Add an unknown v1alpha4 field and expect strict decode failure.

- [ ] **Step 2: Run config tests to verify RED**

Run:

```powershell
go test ./internal/config -run 'V1Alpha4|LegacyResilience' -count=1
```

Expected: unsupported `gateway/v1alpha4` or missing field failures.

- [ ] **Step 3: Add wire types and conversion**

Define `documentV4` by composing the existing listener/server/service/plugin wire types and explicit v4 route/upstream types. Use pointer wire fields for optional route overrides and health sections. Convert durations with `parseDuration`.

Apply these v1alpha4 defaults when fields are omitted:

```go
model.RetryPolicy{
	MaxAttempts: 1,
	Methods:     []string{"GET", "HEAD", "OPTIONS"},
	RetryOn: model.RetryOnPolicy{
		ConnectFailure:        true,
		ConnectionFailure:     true,
		ResponseHeaderTimeout: true,
	},
	Budget: model.RetryBudgetPolicy{
		RatioPer1000: 100,
		Burst:        10,
		MaxInflight:  32,
	},
	TotalTimeout: 30 * time.Second,
}
```

When `health.active` is present and its type is omitted or `http`, default its omitted fields to:

```go
model.ActiveHealthPolicy{
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
}
```

When active type is `tcp`, use the same timeout, intervals, healthy-success, transport-failure, and timeout defaults, but keep `HTTPFailures=0`, both HTTP status lists `nil`, and path/host empty.

When `health.passive` is present, default omitted thresholds/statuses to:

```go
model.PassiveHealthPolicy{
	HTTPFailures:      5,
	TransportFailures: 2,
	Timeouts:          2,
	UnhealthyStatuses: []uint16{429, 500, 502, 503, 504},
}
```

Default scheduler settings in every API version:

```go
Health: HealthRuntimeConfig{
	Workers:            DefaultHealthWorkers,
	ReadyQueueCapacity: DefaultHealthQueueCapacity,
},
```

Only v1alpha4 wire fields may override these settings.

- [ ] **Step 4: Add v1alpha4 dispatch and validation entry**

Add `apiVersionV1Alpha4`, decode `documentV4` in `Decode`, call `convertV4`, then `validateV4`.

`validateV4` reuses v3 resource ID/reference checks and calls `upstream.Normalize`. Validate scheduler bounds before resource normalization:

```go
if got := bootstrap.Runtime.Health.Workers; got < 1 || got > 256 {
	return fmt.Errorf("runtime.health.workers: must be between 1 and 256")
}
if got := bootstrap.Runtime.Health.ReadyQueueCapacity; got < 1 || got > 65536 {
	return fmt.Errorf("runtime.health.ready_queue_capacity: must be between 1 and 65536")
}
```

- [ ] **Step 5: Run focused and compatibility tests**

Run:

```powershell
go test ./internal/config -count=1
go test ./internal/model ./internal/runtime ./internal/gateway -count=1
```

Expected: all versions decode with their locked behavior.

- [ ] **Step 6: Commit**

```powershell
git add internal/config internal/model
git commit -m "feat: add gateway v1alpha4 resilience config"
```

### Task 3: Normalize resilience policies and lock reuse fingerprints

**Files:**
- Modify: `internal/upstream/config.go`
- Modify: `internal/upstream/config_test.go`
- Create: `internal/upstream/fingerprint.go`
- Create: `internal/upstream/fingerprint_test.go`

**Interfaces:**
- Consumes: canonical v1alpha4 model.
- Produces: normalized methods/statuses and stable health/budget fingerprints.

- [ ] **Step 1: Write failing policy validation tests**

Add table cases for:

```go
{
	name: "passive requires active",
	change: func(up *model.Upstream) {
		up.Health.Passive = &model.PassiveHealthPolicy{HTTPFailures: 1}
	},
	wantCode: "PASSIVE_HEALTH_REQUIRES_ACTIVE",
},
{
	name: "retry attempts capped",
	change: func(up *model.Upstream) { up.Retry.MaxAttempts = 6 },
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
```

Add a success case asserting methods become uppercase/sorted/deduplicated and statuses become sorted/deduplicated.

- [ ] **Step 2: Run normalization tests to verify RED**

Run:

```powershell
go test ./internal/upstream -run 'Normalize.*Health|Normalize.*Retry|Fingerprint' -count=1
```

Expected: missing validation/fingerprint failures.

- [ ] **Step 3: Implement exact policy bounds**

Extend `Normalize` with:

```go
func normalizeHealth(resource *model.Upstream, field string) error
func normalizeRetry(resource *model.Upstream, field string) error
```

Before validating, treat an entirely zero-valued `model.RetryPolicy` as the programmatic/legacy compatibility policy:

```go
model.RetryPolicy{
	MaxAttempts: 1,
}
```

This keeps existing direct `model.ResourceSet` fixtures and internal `Gateway.Apply` callers at one attempt with no total timeout. The v1alpha4 converter always supplies its explicit safe defaults, so it never takes this compatibility branch.

Enforce:

- active timeout `10ms..30s`;
- active intervals `100ms..1h`;
- thresholds `1..254`;
- passive requires active and at least one passive threshold;
- HTTP active lists non-empty/disjoint;
- TCP rejects path, host, and active HTTP status lists;
- total timeout `1ms..10m` when non-zero;
- attempts `1..5`;
- ratio `0..1000`, burst/inflight `1..1000`;
- retry statuses only `408`, `425`, `429`, `500..599`;
- methods are valid HTTP tokens, uppercase, sorted, and deduplicated.

Use stable error codes from the tests and fields rooted at `upstreams[n].health` or `upstreams[n].retry`.

- [ ] **Step 4: Implement deterministic fingerprints**

Add:

```go
type healthKey struct {
	endpointIdentity string
	fingerprint      [32]byte
}

type budgetKey struct {
	upstreamID  string
	fingerprint [32]byte
}

func makeHealthKey(upstreamID, endpointIdentity string, policy model.HealthPolicy) healthKey
func makeBudgetKey(upstreamID string, policy model.RetryBudgetPolicy) budgetKey
```

Encode normalized scalar fields and length-prefixed status/method values into SHA-256. Do not hash raw Go struct memory or map iteration.

- [ ] **Step 5: Prove stable and changed keys**

Assert positive weight changes and route-only changes preserve keys, while changing active interval/status/threshold changes `healthKey`, and changing ratio/burst/inflight changes `budgetKey`.

Run:

```powershell
go test ./internal/upstream -run 'Normalize.*Health|Normalize.*Retry|Fingerprint' -count=1
go test ./internal/config ./internal/upstream -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```powershell
git add internal/upstream/config.go internal/upstream/config_test.go internal/upstream/fingerprint.go internal/upstream/fingerprint_test.go
git commit -m "feat: validate resilience policies"
```

### Task 4: Implement the deterministic endpoint health state machine

**Files:**
- Create: `internal/upstream/health.go`
- Create: `internal/upstream/health_test.go`
- Create: `internal/upstream/health_fuzz_test.go`

**Interfaces:**
- Consumes: normalized health policy.
- Produces: `EndpointHealth`, `HealthState`, `Observation`, and atomic selectable reads.

- [ ] **Step 1: Write RED transition tests**

Construct policy thresholds of two and assert:

```go
health := newEndpointHealth("users\x00http://a:80", policy, 1)
if health.State() != HealthUnknown || !health.Selectable() {
	t.Fatalf("initial state = %v selectable=%v", health.State(), health.Selectable())
}
health.Observe(Observation{Source: SourcePassive, Kind: OutcomeHTTPFailure, Status: 503})
health.Observe(Observation{Source: SourcePassive, Kind: OutcomeHTTPFailure, Status: 503})
if health.State() != HealthUnhealthy || health.Selectable() {
	t.Fatalf("failed state = %v selectable=%v", health.State(), health.Selectable())
}
health.Observe(Observation{Source: SourcePassive, Kind: OutcomeSuccess, Status: 200})
if health.State() != HealthUnhealthy {
	t.Fatalf("passive recovered unhealthy endpoint: %v", health.State())
}
health.Observe(Observation{Source: SourceActive, Kind: OutcomeSuccess, Status: 200})
health.Observe(Observation{Source: SourceActive, Kind: OutcomeSuccess, Status: 200})
if health.State() != HealthHealthy || !health.Selectable() {
	t.Fatalf("recovered state = %v", health.State())
}
```

Add cases for counter-category retention, success reset, neutral result, timeout threshold, transport threshold, and observations after retirement.

- [ ] **Step 2: Run health tests to verify RED**

Run:

```powershell
go test ./internal/upstream -run 'TestEndpointHealth' -count=1
```

Expected: missing type/function compile failures.

- [ ] **Step 3: Implement the tracker**

Use:

```go
type EndpointHealth struct {
	state      atomic.Uint32
	generation uint64
	policy     model.HealthPolicy
	mu         sync.Mutex
	successes  uint8
	http       uint8
	transport  uint8
	timeouts   uint8
	retired    atomic.Bool
}
```

`Selectable` performs only:

```go
return HealthState(h.state.Load()) != HealthUnhealthy
```

`Observe` invokes its configured bounded transition hook only when state changes. Reset counters on success/state transition exactly as the design specifies and ignore observations after retirement. The coordinator, which owns `ProbeTarget.Generation`, rejects mismatched generations before calling `Observe`.

- [ ] **Step 4: Add state-machine fuzz invariants**

Seed sequences containing every source/outcome. Fuzz byte slices into observations and assert:

```go
state := health.State()
if state > HealthUnhealthy {
	t.Fatalf("illegal state %d", state)
}
if health.Selectable() == (state == HealthUnhealthy) {
	t.Fatalf("selectable mismatch for %v", state)
}
```

Also assert passive success never moves `unhealthy` to selectable.

- [ ] **Step 5: Run unit and fuzz smoke**

Run:

```powershell
go test ./internal/upstream -run 'TestEndpointHealth' -count=1
go test ./internal/upstream -run '^$' -fuzz FuzzEndpointHealth -fuzztime 10s
```

Expected: PASS.

- [ ] **Step 6: Commit**

```powershell
git add internal/upstream/health.go internal/upstream/health_test.go internal/upstream/health_fuzz_test.go
git commit -m "feat: add endpoint health state machine"
```

### Task 5: Implement the adaptive retry budget

**Files:**
- Create: `internal/upstream/budget.go`
- Create: `internal/upstream/budget_test.go`

**Interfaces:**
- Consumes: `model.RetryBudgetPolicy`.
- Produces: primary credit, retry permit, fixed-point token, and inflight bounds.

- [ ] **Step 1: Write RED accounting tests**

Add:

```go
func TestRetryBudgetCreditsAndCaps(t *testing.T) {
	budget := newRetryBudget(model.RetryBudgetPolicy{
		RatioPer1000: 100,
		Burst:        2,
		MaxInflight:  1,
	})
	for range 20 {
		budget.CreditPrimary()
	}
	first, ok := budget.Acquire()
	if !ok {
		t.Fatal("first retry denied")
	}
	if _, ok := budget.Acquire(); ok {
		t.Fatal("inflight cap exceeded")
	}
	first.Release()
	second, ok := budget.Acquire()
	if !ok {
		t.Fatal("second retry denied")
	}
	second.Release()
	if _, ok := budget.Acquire(); ok {
		t.Fatal("token cap exceeded")
	}
}
```

Add a 100-goroutine contention test and assert `MaxObservedInflight() <= MaxInflight`, credits never negative, and permits can be released only once.

- [ ] **Step 2: Run budget tests to verify RED**

Run:

```powershell
go test ./internal/upstream -run TestRetryBudget -count=1
```

Expected: missing budget declarations.

- [ ] **Step 3: Implement fixed-point atomic accounting**

Use `atomic.Uint64` credits capped at `burst*1000` and `atomic.Uint32` inflight. `CreditPrimary` uses a saturating CAS loop. `Acquire` reserves inflight, consumes `1000` credits, and releases inflight immediately if token consumption fails.

Implement `RetryPermit.Release` with a per-permit atomic once guard and panic on double release in tests/builds rather than silently leaking accounting errors.

- [ ] **Step 4: Run focused and race tests**

Run:

```powershell
go test ./internal/upstream -run TestRetryBudget -count=10
go test -race ./internal/upstream -run TestRetryBudgetConcurrent -count=1
```

Expected: PASS with no race and no bound violation.

- [ ] **Step 5: Commit**

```powershell
git add internal/upstream/budget.go internal/upstream/budget_test.go
git commit -m "feat: add bounded retry budget"
```

### Task 6: Make WRR and consistent hash health/exclusion aware

**Files:**
- Modify: `internal/upstream/plan.go`
- Modify: `internal/upstream/plan_test.go`
- Modify: `internal/upstream/wrr.go`
- Modify: `internal/upstream/wrr_test.go`
- Modify: `internal/upstream/chash.go`
- Modify: `internal/upstream/chash_test.go`
- Modify: `internal/upstream/endpoint.go`

**Interfaces:**
- Consumes: `EndpointHealth`.
- Produces: `AttemptSet`, `Plan.SelectNext`, and `ErrNoHealthyEndpoint`.

- [ ] **Step 1: Write RED plan-selection tests**

Create three endpoints with deterministic health handles. Mark the first unhealthy, add the second ordinal to `AttemptSet`, then assert the third is selected:

```go
var attempted AttemptSet
attempted.Add(1)
selection, err := plan.SelectNext(request, &attempted)
if err != nil {
	t.Fatal(err)
}
if selection.Ordinal() != 2 {
	t.Fatalf("ordinal = %d, want 2", selection.Ordinal())
}
```

Mark every endpoint unhealthy and assert:

```go
if _, err := plan.SelectNext(request, nil); !errors.Is(err, ErrNoHealthyEndpoint) {
	t.Fatalf("error = %v, want ErrNoHealthyEndpoint", err)
}
```

Add WRR fairness over remaining healthy endpoints and consistent-hash clockwise/distinct-endpoint cases.

- [ ] **Step 2: Run selector tests to verify RED**

Run:

```powershell
go test ./internal/upstream -run 'Health|AttemptSet|SelectNext' -count=1
```

Expected: missing selection APIs.

- [ ] **Step 3: Add bounded attempt storage**

Implement `AttemptSet` as the locked fixed array. `Add` returns false for duplicates or when full; `Contains` scans at most five ordinals. It performs no allocation.

- [ ] **Step 4: Add health handles to plan endpoints**

Extend:

```go
type planEndpoint struct {
	runtime  *endpointRuntime
	health   *EndpointHealth
	identity string
	weight   uint32
}
```

Add `selectable(ordinal, attempted)` and bounded scan methods to WRR and continuum. For consistent hash, scan ring points but count each distinct ordinal once; stop after `len(endpoints)` distinct ordinals.

Keep:

```go
func (p *Plan) Select(request *http.Request) (Selection, error) {
	return p.SelectNext(request, nil)
}
```

- [ ] **Step 5: Run selector and allocation tests**

Run:

```powershell
go test ./internal/upstream -run 'WRR|ConsistentHash|Plan|AttemptSet' -count=1
go test ./internal/upstream -run '^$' -bench 'BenchmarkWRRSelect|BenchmarkConsistentHashSelect' -benchmem -count=3
```

Expected: tests pass and all-healthy selection remains `0 allocs/op`.

- [ ] **Step 6: Commit**

```powershell
git add internal/upstream/plan.go internal/upstream/plan_test.go internal/upstream/wrr.go internal/upstream/wrr_test.go internal/upstream/chash.go internal/upstream/chash_test.go internal/upstream/endpoint.go
git commit -m "feat: select only healthy untried endpoints"
```

### Task 7: Implement isolated HTTP and TCP probers

**Files:**
- Create: `internal/upstream/probe.go`
- Create: `internal/upstream/probe_http.go`
- Create: `internal/upstream/probe_tcp.go`
- Create: `internal/upstream/probe_test.go`

**Interfaces:**
- Consumes: endpoint target, active policy, and context.
- Produces: independently testable `Prober` implementations returning `Observation`.

- [ ] **Step 1: Write RED HTTP/TCP probe tests**

Use `httptest.Server` for `200`, `503`, redirect, delay, and a body larger than `4 KiB`. Assert:

```go
got := httpProber.Probe(ctx, target)
if got.Source != SourceActive || got.Kind != OutcomeHTTPFailure || got.Status != 503 {
	t.Fatalf("observation = %+v", got)
}
```

Use a real TCP listener for success, a closed port for transport failure, and a canceled context for timeout/cancellation classification.

- [ ] **Step 2: Run probe tests to verify RED**

Run:

```powershell
go test ./internal/upstream -run 'TestHTTPProber|TestTCPProber' -count=1
```

Expected: missing prober APIs.

- [ ] **Step 3: Define the prober contract**

Add:

```go
type ProbeTarget struct {
	EndpointID string
	URL        *url.URL
	Generation uint64
	Policy     model.ActiveHealthPolicy
}

type ProbeResult struct {
	Target      ProbeTarget
	Observation Observation
	Duration    time.Duration
}

type Prober interface {
	Probe(context.Context, ProbeTarget) ProbeResult
	CloseIdleConnections()
}
```

- [ ] **Step 4: Implement HTTP and TCP probers**

HTTP uses its own `http.Transport`, disables redirects, applies the probe timeout through context, reads at most `4 KiB+1`, and closes the body. TCP uses `net.Dialer.DialContext` then closes immediately.

Classify configured healthy/unhealthy statuses through the active policy; return `OutcomeNeutral` for other statuses. Do not mutate health inside a prober.

- [ ] **Step 5: Run focused tests**

Run:

```powershell
go test ./internal/upstream -run 'TestHTTPProber|TestTCPProber' -count=10
```

Expected: PASS without leaked connections or goroutines.

- [ ] **Step 6: Commit**

```powershell
git add internal/upstream/probe.go internal/upstream/probe_http.go internal/upstream/probe_tcp.go internal/upstream/probe_test.go
git commit -m "feat: add http and tcp health probers"
```

### Task 8: Add the bounded lazy health coordinator

**Files:**
- Create: `internal/upstream/health_scheduler.go`
- Create: `internal/upstream/health_scheduler_test.go`

**Interfaces:**
- Consumes: `EndpointHealth`, `Prober`, scheduler bootstrap bounds.
- Produces: lazy activation, deterministic jitter, bounded queue/workers, retirement, and close.

- [ ] **Step 1: Write RED fake-clock scheduler tests**

Define a package-private injected clock/timer interface and fake. Test:

```go
coordinator := newHealthCoordinator(options)
coordinator.Register(runtime)
if got := coordinator.Stats().Scheduled; got != 0 {
	t.Fatalf("scheduled before activation = %d", got)
}
runtime.ActivateHealth()
fakeClock.Advance(healthyInterval + healthyInterval/10)
eventually(t, func() bool { return prober.Calls() == 1 })
```

Add tests for:

- deterministic initial jitter;
- unhealthy interval after transition;
- ready queue saturation rescheduling without blocking;
- duplicate coalescing;
- stale generation discard;
- worker panic recovery without endpoint ejection;
- constant worker goroutine count;
- bounded `Close`.

- [ ] **Step 2: Run scheduler tests to verify RED**

Run:

```powershell
go test ./internal/upstream -run 'TestHealthCoordinator' -count=1
```

Expected: missing coordinator APIs.

- [ ] **Step 3: Implement scheduler heap and ready queue**

Use one heap item per activated endpoint:

```go
type scheduledProbe struct {
	due        time.Time
	sequence   uint64
	target     ProbeTarget
	health     *EndpointHealth
	cancelled  atomic.Bool
	index      int
}
```

One scheduler goroutine waits for the earliest due item. Workers read a bounded channel. Queue-full handling marks the item pending and reschedules with a capped backoff no greater than its configured interval.

Wrap each worker task in a recovery boundary. A panic increments the closed internal-failure outcome and reschedules the target; it must not submit a health failure observation.

- [ ] **Step 4: Implement lazy activation and close**

`Register` stores no ready task. `ActivateHealth` uses `sync.Once`/atomic CAS to enqueue the endpoint set. `Retire` prevents future enqueue and invalidates generation. `Close(ctx)` stops scheduling, cancels probe contexts, closes probe idle connections, and waits for all workers.

- [ ] **Step 5: Run scheduler/race tests**

Run:

```powershell
go test ./internal/upstream -run 'TestHealthCoordinator' -count=20
go test -race ./internal/upstream -run 'TestHealthCoordinator' -count=1
```

Expected: PASS with no sleeps in fake-clock tests.

- [ ] **Step 6: Commit**

```powershell
git add internal/upstream/health_scheduler.go internal/upstream/health_scheduler_test.go
git commit -m "feat: add bounded health coordinator"
```

### Task 9: Integrate resilience runtimes into registry ownership

**Files:**
- Modify: `internal/upstream/registry.go`
- Modify: `internal/upstream/registry_test.go`
- Modify: `internal/upstream/reaper.go`
- Modify: `internal/upstream/reaper_test.go`
- Modify: `internal/upstream/plan.go`
- Modify: `internal/upstream/observer.go`
- Modify: `internal/runtime/manager.go`
- Modify: `internal/runtime/manager_test.go`
- Modify: `internal/gateway/gateway.go`
- Modify: `internal/gateway/gateway_test.go`

**Interfaces:**
- Consumes: Tasks 3–8.
- Produces: `RegistryOptions`, reference-counted health/budget reuse, plan activation/outcome/budget facade, and resilience stats.

- [ ] **Step 1: Write RED registry reuse/rollback tests**

Prepare/commit revision 1, mark endpoint unhealthy, then prepare a positive weight-only revision. Assert pointer/state reuse through same-package inspection:

```go
if firstHealth != secondHealth || secondHealth.State() != HealthUnhealthy {
	t.Fatalf("health runtime was not reused")
}
if firstBudget != secondBudget {
	t.Fatalf("retry budget was not reused")
}
```

Change health interval and assert a new `unknown` tracker but the same transport. Add candidate rollback and reaper tests asserting:

```go
stats := registry.Stats()
if stats.LiveHealthTrackers != wantHealth ||
	stats.LiveRetryBudgets != wantBudgets ||
	stats.RetiredResilienceRuntimes != 0 {
	t.Fatalf("registry stats = %+v", stats)
}
```

- [ ] **Step 2: Run registry tests to verify RED**

Run:

```powershell
go test ./internal/upstream -run 'Registry.*Health|Registry.*Budget|Reaper.*Resilience' -count=1
```

Expected: missing registry fields/options.

- [ ] **Step 3: Replace the registry constructor**

Implement:

```go
func NewRegistry(options RegistryOptions) (*Registry, error)
```

Validate all option bounds and construct the coordinator once. Mechanically update existing call sites/tests from `NewRegistry(max, observer)` to `NewRegistry(RegistryOptions{...})` with default worker/queue values. `gateway.New` passes the bootstrap worker/queue settings.

- [ ] **Step 4: Add health and budget entries/refcounts**

Add registry maps keyed by `healthKey` and `budgetKey`, extend `resourceRefs`, `PrepareStats`, `CleanupStats`, and `RegistryStats`, and attach positive-weight endpoint health plus upstream budget to each plan.

Do not register weight-zero endpoints with the coordinator.

- [ ] **Step 5: Add plan facade methods**

Implement the locked `ActivateHealth`, `CreditPrimary`, `AcquireRetry`, and `Selection.Observe` methods. These delegate to the shared runtime; they do not look up registry maps.

- [ ] **Step 6: Extend reaping and close**

Finalization retires scheduler registrations before dropping health/budget references. Implement idempotent `Registry.StopHealth` to stop new enqueue/activation without canceling active request attempts. `Manager.StopHealth` delegates to the registry. `Registry.Close` stops new preparation, retires plan sets, cancels/waits for the coordinator, then confirms all endpoint/transport/selection/health/budget counts are zero.

- [ ] **Step 7: Run upstream and full tests**

Run:

```powershell
go test ./internal/upstream ./internal/runtime ./internal/gateway -count=1
go test ./... -count=1
go test -race ./internal/upstream ./internal/runtime ./internal/gateway -run 'Registry|Reaper|Coordinator|StopHealth' -count=1
```

Expected: PASS and no leaked coordinator goroutine.

- [ ] **Step 8: Commit**

```powershell
git add internal/upstream internal/runtime/manager.go internal/runtime/manager_test.go internal/gateway/gateway.go internal/gateway/gateway_test.go
git commit -m "feat: own resilience runtimes in upstream registry"
```

### Task 10: Compile immutable effective route retry policies

**Files:**
- Modify: `internal/runtime/builder.go`
- Modify: `internal/runtime/builder_test.go`
- Modify: `internal/runtime/snapshot.go`
- Modify: `internal/requestctx/context.go`
- Modify: `internal/model/resources.go`

**Interfaces:**
- Consumes: upstream retry defaults and route overrides.
- Produces: an immutable effective `model.RetryPolicy` on each `CompiledRoute`.

- [ ] **Step 1: Write RED inheritance/replacement tests**

Build routes with no override, scalar overrides, an explicit empty methods list, and a replacement `retry_on`. Assert:

```go
policy := snapshot.routes[0].RetryPolicy()
if policy.MaxAttempts != 3 ||
	policy.TotalTimeout != 2*time.Second ||
	len(policy.Methods) != 0 ||
	!slices.Equal(policy.RetryOn.Statuses, []uint16{503}) {
	t.Fatalf("effective policy = %+v", policy)
}
```

Mutate the original resource after build and assert the compiled policy does not change.

- [ ] **Step 2: Run builder tests to verify RED**

Run:

```powershell
go test ./internal/runtime -run 'RetryPolicy|ResilienceOverride' -count=1
```

Expected: missing compiled policy API.

- [ ] **Step 3: Add policy compilation**

Index canonical upstream resources by ID during build. Add:

```go
func effectiveRetryPolicy(base model.RetryPolicy, override model.RouteResiliencePolicy) model.RetryPolicy
```

Deep-copy method/status slices. Replace only explicitly present route fields.

- [ ] **Step 4: Extend compiled-route and request interfaces**

Store:

```go
retry model.RetryPolicy
```

on `CompiledRoute`, and implement the locked route methods by delegating to its plan. Replace the old `RuntimeRoute.Select` contract with `SelectNext` and the plan/budget methods.

Add request-context result fields:

```go
Attempts          int
RetrySuppressed   string
UpstreamOutcome   string
```

Keep the existing `Attempt` field as the currently executing 1-based attempt for plugin/future log context.

- [ ] **Step 5: Run runtime/request/proxy compile tests**

Run:

```powershell
go test ./internal/runtime ./internal/requestctx ./internal/proxy -count=1
```

Expected: PASS after mechanically adapting test doubles to the locked interface.

- [ ] **Step 6: Commit**

```powershell
git add internal/runtime internal/requestctx internal/model/resources.go internal/proxy
git commit -m "feat: compile route resilience policy"
```

### Task 11: Implement retry eligibility and outcome classification

**Files:**
- Create: `internal/proxy/retry.go`
- Create: `internal/proxy/retry_test.go`

**Interfaces:**
- Consumes: effective retry policy and Go transport errors/responses.
- Produces: closed retry/passive outcome classification and bounded response drain.

- [ ] **Step 1: Write RED classifier tests**

Table-test:

- bodyless configured method is eligible;
- unconfigured method is not;
- `GetBody` request is replayable;
- unknown/chunked body without `GetBody` is not;
- canceled/deadline context is not;
- `net.OpError`, reset/EOF, timeout, and configured statuses map correctly;
- status `409` is not retryable;
- passive status policy remains independent.

Use:

```go
decision := classifyAttempt(policy, response, err)
if decision.Retry != wantRetry || decision.Observation.Kind != wantKind {
	t.Fatalf("decision = %+v", decision)
}
```

- [ ] **Step 2: Run classifier tests to verify RED**

Run:

```powershell
go test ./internal/proxy -run 'RetryEligibility|ClassifyAttempt|DrainRetryResponse' -count=1
```

Expected: missing helper declarations.

- [ ] **Step 3: Implement closed decisions**

Add:

```go
type retryReason uint8

type attemptDecision struct {
	Retry       bool
	Reason      retryReason
	Observation upstream.Observation
}

func retryEligible(*http.Request, model.RetryPolicy) bool
func classifyAttempt(model.RetryPolicy, *http.Response, error) attemptDecision
func cloneAttemptRequest(*http.Request, upstream.Selection, int) (*http.Request, error)
func drainRetryResponse(*http.Response) bool
```

`cloneAttemptRequest` reconstructs body only for attempt index greater than one, copies the URL, and changes only scheme/host to the selected target.

`drainRetryResponse` reads at most `32 KiB+1`, closes the body, and returns true only if EOF was reached within the cap.

For a completed HTTP response, the proxy emits a passive observation carrying the status. `EndpointHealth` applies its own passive unhealthy-status policy and converts that status to success or HTTP failure. The retry classifier uses only `RetryOnPolicy`; it must not import or duplicate passive policy.

- [ ] **Step 4: Run focused tests**

Run:

```powershell
go test ./internal/proxy -run 'RetryEligibility|ClassifyAttempt|DrainRetryResponse' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```powershell
git add internal/proxy/retry.go internal/proxy/retry_test.go
git commit -m "feat: classify replay safe retries"
```

### Task 12: Implement the multi-attempt route transport

**Files:**
- Modify: `internal/proxy/route_transport.go`
- Create: `internal/proxy/attempt_transport_test.go`
- Modify: `internal/proxy/handler.go`
- Modify: `internal/proxy/runtime_handler_test.go`

**Interfaces:**
- Consumes: Tasks 9–11.
- Produces: one request-local attempt loop with endpoint exclusion, passive reporting, budget, and final result.

- [ ] **Step 1: Write RED transport tests**

Use a fake runtime route and scripted selections/round trips to assert:

```go
if got := runtime.SelectedOrdinals(); !slices.Equal(got, []uint32{0, 1, 2}) {
	t.Fatalf("ordinals = %v", got)
}
if state.Attempts != 3 || state.Selection.Ordinal() != 2 {
	t.Fatalf("state = %+v", state)
}
```

Add cases for:

- retryable connection failure then success;
- retryable `503` then success;
- non-replayable POST makes one attempt;
- replayable configured POST rebuilds identical body;
- budget denial preserves first response/error;
- no untried endpoint stops early;
- context cancellation never retries;
- response plugins see only final response.

- [ ] **Step 2: Run attempt tests to verify RED**

Run:

```powershell
go test ./internal/proxy -run 'AttemptTransport|Replayable|RetryBudget' -count=1
```

Expected: current one-attempt behavior fails.

- [ ] **Step 3: Move target rewrite into route transport**

Remove `proxyRequest.SetURL` from `ReverseProxy.Rewrite`; keep hop-by-hop removal and forwarding headers there.

In `routeTransport.RoundTrip`, obtain request state, call `ActivateUpstream` and `CreditPrimary`, and execute:

```go
var attempted upstream.AttemptSet
for attempt := 1; attempt <= int(policy.MaxAttempts); attempt++ {
	selection, err := state.Runtime.SelectNext(request, &attempted)
	if err != nil {
		return nil, finalSelectionError(err, lastResponse, lastError)
	}
	attempted.Add(selection.Ordinal())
	state.Attempt = attempt
	state.Attempts = attempt
	state.Selection = selection
	// clone, round trip, observe, classify, maybe acquire permit and continue
}
```

Acquire a retry permit only after a retryable result and before the next selection. Release it when that retry attempt ends. Drain retryable HTTP responses before continuing.

- [ ] **Step 4: Preserve stable final mapping**

Return typed sentinel/wrapped errors so `handleProxyError` maps:

```text
ErrNoHealthyEndpoint -> 503 UPSTREAM_UNHEALTHY
timeout              -> 504 UPSTREAM_TIMEOUT
transport failure    -> 502 UPSTREAM_CONNECTION_FAILED
```

A final HTTP response is returned unchanged. Set `RetrySuppressed` to a closed reason and `UpstreamOutcome` to a closed final outcome.

- [ ] **Step 5: Run proxy tests**

Run:

```powershell
go test ./internal/proxy -count=1
go test ./internal/runtime ./internal/gateway -count=1
```

Expected: PASS, including pre-existing headers, trailers, and plugin tests.

- [ ] **Step 6: Commit**

```powershell
git add internal/proxy
git commit -m "feat: execute bounded upstream retries"
```

### Task 13: Apply the true total proxy deadline

**Files:**
- Modify: `internal/proxy/handler.go`
- Modify: `internal/proxy/runtime_handler_test.go`
- Modify: `internal/proxy/handler_test.go`

**Interfaces:**
- Consumes: immutable effective route policy.
- Produces: post-request-plugin deadline covering attempts and response streaming.

- [ ] **Step 1: Write RED deadline tests**

Add:

- request plugin completes before the total timer begins;
- an earlier client deadline wins;
- header delay expires as JSON `504`;
- response commits then stream stalls, causing stream termination rather than a second JSON response;
- legacy policy with timeout `0` creates no new deadline.

Use an injected/short real timeout only at the handler integration boundary:

```go
started := time.Now()
response := serveRequest(t, routeWithTimeout(30*time.Millisecond), delayedUpstream)
if response.Code != http.StatusGatewayTimeout {
	t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
}
if time.Since(started) > 250*time.Millisecond {
	t.Fatal("total deadline was not enforced")
}
```

- [ ] **Step 2: Run deadline tests to verify RED**

Run:

```powershell
go test ./internal/proxy -run 'TotalDeadline|ClientDeadline|CommittedStreamTimeout' -count=1
```

Expected: timeout currently covers only transport dial/header behavior.

- [ ] **Step 3: Install deadline after request plugins**

Immediately before proxy execution:

```go
policy := state.Runtime.RetryPolicy()
if policy.TotalTimeout > 0 {
	ctx, cancel := context.WithTimeout(request.Context(), policy.TotalTimeout)
	defer cancel()
	request = request.WithContext(ctx)
}
```

The request-context value survives because `WithTimeout` derives from the attached context. Do not apply this deadline to route/plugin short circuits.

- [ ] **Step 4: Preserve committed-response semantics**

Before headers, map deadline to `504`. After headers, allow `ReverseProxy`/server cancellation to terminate the stream, set final outcome to timeout, and emit no second body.

- [ ] **Step 5: Run proxy and protocol regression tests**

Run:

```powershell
go test ./internal/proxy -count=1
go test ./test/integration -run 'Streaming|Trailers|Cancellation|Timeout' -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```powershell
git add internal/proxy
git commit -m "feat: enforce total proxy deadlines"
```

### Task 14: Prove reconcile isolation and resilience lifecycle

**Files:**
- Modify: `internal/upstream/registry_test.go`
- Modify: `internal/gateway/gateway.go`
- Modify: `internal/gateway/gateway_test.go`
- Modify: `test/integration/upstream_reconcile_test.go`
- Create: `test/integration/upstream_resilience_test.go`

**Interfaces:**
- Consumes: the complete in-process resilience path.
- Produces: revision, health, budget, pool, rollback, retirement, and shutdown evidence.

- [ ] **Step 1: Add dynamic health preservation tests**

Start two upstreams, cause one endpoint to become unhealthy, apply a positive weight-only revision, and assert the endpoint remains unhealthy while the warmed transport pointer/accepted connection count remains unchanged.

Change only the health interval and assert:

```go
if got := newHealth.State(); got != upstream.HealthUnknown {
	t.Fatalf("new policy state = %v", got)
}
if connectionsAfter != connectionsBefore {
	t.Fatalf("policy-only update replaced pool: %d -> %d", connectionsBefore, connectionsAfter)
}
```

- [ ] **Step 2: Add stale probe and unrelated-upstream tests**

Hold a probe for upstream A, replace A's policy, complete the old probe, and assert the new tracker stays `unknown`. Warm upstream B and prove A's health/retry update does not replace B's transport, health, or budget pointer.

- [ ] **Step 3: Add rollback/backpressure tests**

Apply invalid passive-only config and invalid retry status. Assert active revision, health state, budget credits, live registry counts, and traffic response remain exactly last-known-good.

Force retired-snapshot backpressure with held request leases and assert no resilience task/runtime leaks from the rejected candidate.

- [ ] **Step 4: Add shutdown tests**

Shutdown with one request retry and one active probe in flight. Assert readiness falls first, retries drain, probes cancel within context, coordinator workers exit, and final registry counts are zero.

Change `Gateway.shutdown` to:

```go
g.telemetry.SetReady(false)
g.manager.StopHealth()
// shut down traffic listeners
// wait for g.trafficRequests
// manager.Close cancels probes, retires plans, and closes the registry
```

Do not call `Manager.Close` until traffic handlers and retry permits have drained.

- [ ] **Step 5: Run focused, repeated, and race tests**

Run:

```powershell
go test ./internal/upstream ./internal/gateway ./test/integration -run 'Health|Retry|Reconcile|Rollback|Shutdown' -count=1
go test ./internal/upstream ./internal/gateway ./test/integration -run 'Health|Retry|Reconcile|Shutdown' -count=20
go test -race ./internal/upstream ./internal/gateway ./test/integration -run 'Health|Retry|Reconcile|Shutdown' -count=1
```

Expected: PASS without state reset, pool replacement, stale result, or leaked worker.

- [ ] **Step 6: Commit**

```powershell
git add internal/upstream/registry_test.go internal/gateway/gateway.go internal/gateway/gateway_test.go test/integration/upstream_reconcile_test.go test/integration/upstream_resilience_test.go
git commit -m "test: prove resilience reconcile semantics"
```

### Task 15: Add bounded resilience telemetry

**Files:**
- Modify: `internal/upstream/observer.go`
- Modify: `internal/upstream/registry.go`
- Modify: `internal/telemetry/telemetry.go`
- Modify: `internal/telemetry/telemetry_test.go`
- Modify: `internal/gateway/lifecycle_observer.go`
- Modify: `internal/gateway/lifecycle_observer_test.go`
- Modify: `internal/gateway/gateway.go`

**Interfaces:**
- Consumes: registry/runtime atomics and transition events.
- Produces: the approved metrics and transition/degradation logs without dynamic request-path labels.

- [ ] **Step 1: Write RED metric/cardinality tests**

Assert the exact families:

```text
gateway_upstream_health_endpoints{upstream_id,state}
gateway_upstream_health_transitions_total{source,to_state}
gateway_upstream_health_probes_total{type,outcome}
gateway_upstream_health_probe_duration_seconds{type}
gateway_upstream_health_scheduler_queue
gateway_upstream_health_scheduler_reschedules_total{reason}
gateway_upstream_attempts_total{upstream_id,outcome}
gateway_upstream_retries_total{upstream_id,result}
gateway_upstream_retry_suppressed_total{reason}
gateway_upstream_retry_inflight{upstream_id}
gateway_upstream_retry_budget_tokens{upstream_id}
```

Assert endpoint URL/ID, raw error text, raw status, client address, and route ID do not appear in resilience labels.

- [ ] **Step 2: Run telemetry tests to verify RED**

Run:

```powershell
go test ./internal/telemetry ./internal/gateway -run 'Health|Retry|Resilience' -count=1
```

Expected: missing collectors/events.

- [ ] **Step 3: Add a scrape-time stats provider**

Expose:

```go
type ResilienceStatsProvider interface {
	ResilienceStats() []upstream.ResilienceStats
	HealthCoordinatorStats() upstream.HealthCoordinatorStats
}
```

Registry snapshots configured upstream IDs under its control-plane mutex, then reads per-runtime atomics. Implement a custom Prometheus collector so request attempts update atomics only; do not call `WithLabelValues` from `routeTransport`.

- [ ] **Step 4: Add closed global events**

Observer callbacks update global transition/probe/reschedule collectors. Lifecycle logs only state transitions, coordinator degraded/recovered transitions, reconcile failures, and shutdown failures.

Use canonical endpoint identity in structured transition logs but never in metric labels.

- [ ] **Step 5: Wire collector registration**

After registry construction, register the provider with telemetry. Constructor failure must close the registry/coordinator with a bounded context.

- [ ] **Step 6: Run telemetry and full tests**

Run:

```powershell
go test ./internal/telemetry ./internal/upstream ./internal/gateway -count=1
go test ./... -count=1
```

Expected: PASS with the exact bounded label sets.

- [ ] **Step 7: Commit**

```powershell
git add internal/upstream/observer.go internal/upstream/registry.go internal/telemetry internal/gateway
git commit -m "feat: expose bounded resilience telemetry"
```

### Task 16: Add Phase 3B acceptance, benchmarks, examples, and operations docs

**Files:**
- Create: `internal/upstream/resilience_benchmark_test.go`
- Create: `internal/upstream/phase3b_acceptance_test.go`
- Create: `internal/proxy/phase3b_acceptance_test.go`
- Modify: `internal/testupstream/server.go`
- Modify: `internal/testupstream/server_test.go`
- Create: `configs/phase3b.yaml`
- Create: `docs/operations/phase-3b-runbook.md`
- Create: `docs/benchmarks/phase-3b-current-status.md`
- Modify: `README.md`
- Modify: `docs/superpowers/specs/2026-07-21-go-native-api-gateway-phase-roadmap-design.md`
- Modify: `docs/superpowers/specs/2026-07-27-phase-3b-health-timeout-retry-design.md`

**Interfaces:**
- Consumes: completed Phase 3B runtime and observed local results.
- Produces: deterministic normal/full evidence, runnable config, runbook, and Phase 3C handoff.

- [ ] **Step 1: Extend the programmable test upstream**

Add deterministic endpoints:

```text
GET  /status/{code}
GET  /header-delay/{duration}
GET  /stream-delay/{duration}
POST /echo
GET  /close
```

Validate code/duration bounds and add focused handler tests. Keep `/debug/state` counts sufficient to prove attempts and cancellations.

- [ ] **Step 2: Add locked benchmark names**

Create:

```text
BenchmarkHealthAwareWRR/all-healthy
BenchmarkHealthAwareWRR/one-unhealthy
BenchmarkHealthAwareConsistentHash/all-healthy
BenchmarkHealthAwareConsistentHash/one-unhealthy
BenchmarkRetryBudget/credit
BenchmarkRetryBudget/acquire-release
BenchmarkAttemptTransport/one-attempt
BenchmarkAttemptTransport/retry-success
BenchmarkProxyHealthy/phase3a-baseline
BenchmarkProxyHealthy/phase3b-health-enabled
```

Call `b.ReportAllocs()`. Compare selector medians against Phase 3A benchmark cases in the same run.

- [ ] **Step 3: Add normal/full acceptance profiles**

Use seed `20260727`.

Normal:

```go
phase3BProfile{Upstreams: 1_000, EndpointsPerUpstream: 10, Swaps: 2}
```

Full when `GATEWAY_PHASE3B_ACCEPTANCE=1`:

```go
phase3BProfile{Upstreams: 10_000, EndpointsPerUpstream: 10, Swaps: 20}
```

Assert before activation:

```go
if stats.Scheduled != 0 || stats.ReadyQueue != 0 {
	t.Fatalf("unused health work = %+v", stats)
}
```

After controlled activation, assert worker count is configured constant, retry invariants hold, recovery meets the formula plus `250ms`, retired resilience counts return to zero, and retained heap is at most `125%` of one active Phase 3B footprint.

In `internal/proxy/phase3b_acceptance_test.go`, run the same warmed in-process handler/upstream workload twice: once with legacy one-attempt/health-disabled policy and once with v1alpha4 health enabled but no failures. Use a fixed concurrency and request count, capture per-request durations, sort them, and report throughput plus p99. In full acceptance mode assert:

```go
if phase3BThroughput < phase3AThroughput*95/100 {
	t.Fatalf("healthy throughput = %.2f, want >= 95%% of %.2f", phase3BThroughput, phase3AThroughput)
}
if phase3BP99 > phase3AP99*110/100 {
	t.Fatalf("healthy p99 = %s, want <= 110%% of %s", phase3BP99, phase3AP99)
}
```

- [ ] **Step 4: Run normal acceptance and benchmarks**

Run:

```powershell
go test ./internal/upstream ./internal/proxy -run 'TestPhase3BAcceptance|TestPhase3BHealthyProxyComparison' -count=1 -v
go test ./internal/upstream ./internal/proxy -run '^$' -bench 'BenchmarkHealthAware|BenchmarkRetryBudget|BenchmarkAttemptTransport|BenchmarkProxyHealthy' -benchmem -count=5
```

Expected: normal acceptance passes, selectors report `0 allocs/op`, and measured medians are recorded rather than guessed.

- [ ] **Step 5: Write config, runbook, and evidence ledger**

`configs/phase3b.yaml` demonstrates:

- HTTP and TCP active-check upstreams;
- passive policy;
- safe default plus explicit route retry override;
- scheduler bootstrap bounds.

The runbook documents activation, states, fail-closed behavior, body replay, budget, deadline, metrics, reconcile, shutdown, and exact verification commands.

The evidence ledger uses exactly one status:

```text
implementation in progress
implementation complete; canonical resilience evidence pending
Phase 3B accepted
```

Only record commands as passed after observing output.

- [ ] **Step 6: Update navigation and handoff**

Update README and roadmap without marking all Phase 3 accepted. State:

- Phase 3C receives shared transports, health-aware plans, retry/deadline behavior, and clean lifecycle;
- Phase 3C owns upstream TLS/mTLS, protocols, dynamic downstream SNI, and WebSocket;
- Phase 3D still owns access logging and integrated APISIX comparison.

Audit the design and change only names that differ from the implemented contract; do not weaken unmet gates.

- [ ] **Step 7: Verify docs and commit**

Run the existing local Markdown link checker pattern over README, runbook, evidence, roadmap, design, and plan. Then:

```powershell
git diff --check
go test ./internal/testupstream ./internal/upstream ./internal/proxy -count=1
```

Expected: no missing links, whitespace errors, or focused test failures.

Commit:

```powershell
git add internal/testupstream internal/upstream/resilience_benchmark_test.go internal/upstream/phase3b_acceptance_test.go internal/proxy/phase3b_acceptance_test.go configs/phase3b.yaml docs/operations/phase-3b-runbook.md docs/benchmarks/phase-3b-current-status.md README.md docs/superpowers/specs
git commit -m "docs: record phase 3b resilience runtime"
```

### Task 17: Run final verification and record the Phase 3B checkpoint

**Files:**
- Modify with observed evidence only: `docs/benchmarks/phase-3b-current-status.md`

**Interfaces:**
- Consumes: the complete implementation and documentation.
- Produces: an evidence-backed clean checkpoint, not an APISIX parity claim.

- [ ] **Step 1: Run formatting, vet, test, and build gates**

Run:

```powershell
$unformatted = @(gofmt -l .)
if ($unformatted.Count -ne 0) {
	$unformatted
	exit 1
}
go vet ./...
go test -p 1 ./... -count=1
go build ./cmd/...
```

Expected: every command passes.

- [ ] **Step 2: Run focused race verification**

Run:

```powershell
go test -race ./internal/upstream ./internal/runtime ./internal/proxy ./internal/gateway ./test/integration -count=1
```

Expected: PASS on a CGO-capable host. If unavailable, record the exact command/error and keep canonical evidence pending.

- [ ] **Step 3: Run normal and full acceptance**

Run:

```powershell
go test ./internal/upstream ./internal/proxy -run 'TestPhase3BAcceptance|TestPhase3BHealthyProxyComparison' -count=1 -v
$env:GATEWAY_PHASE3B_ACCEPTANCE = '1'
go test ./internal/upstream ./internal/proxy -run 'TestPhase3BAcceptance|TestPhase3BHealthyProxyComparison' -count=1 -v
Remove-Item Env:GATEWAY_PHASE3B_ACCEPTANCE
```

Expected: normal passes. Full output is provisional unless run on reference Linux.

- [ ] **Step 4: Run five-count resilience benchmarks**

Run:

```powershell
go test ./internal/upstream ./internal/proxy -run '^$' -bench 'BenchmarkHealthAware|BenchmarkRetryBudget|BenchmarkAttemptTransport|BenchmarkProxyHealthy' -benchmem -count=5
```

Expected: record medians, allocation counts, Phase 3A selector ratios, one-attempt throughput ratio, and retry overhead.

- [ ] **Step 5: Run bounded fuzz smoke**

Run:

```powershell
go test ./internal/upstream -run '^$' -fuzz FuzzEndpointHealth -fuzztime 30s
go test ./internal/upstream -run '^$' -fuzz FuzzNormalizeEndpoint -fuzztime 30s
go test ./internal/upstream -run '^$' -fuzz FuzzHashKey -fuzztime 30s
```

Expected: all available smoke runs pass.

- [ ] **Step 6: Repeat lifecycle/failure suites**

Run:

```powershell
go test ./internal/upstream ./internal/gateway ./test/integration -run 'Health|Retry|Timeout|Shutdown|Retired|Reaper|Reconcile' -count=20
```

Expected: PASS without leaked probes, retries, permits, health trackers, budgets, or retired runtimes.

- [ ] **Step 7: Record only observed evidence**

Record toolchain, host, seed/checksum, profiles, build time, heap, retained ratio, worker/goroutine counts, recovery latency, retry amplification, benchmark medians, race status, fuzz duration, and omitted canonical commands.

Use `implementation complete; canonical resilience evidence pending` only when all mandatory development gates pass. Use `Phase 3B accepted` only after reference-Linux absolute gates pass. Otherwise retain `implementation in progress`.

- [ ] **Step 8: Run final documentation and Git checks**

Run:

```powershell
git diff --check
git status --short
git log --oneline --decorate -25
```

Repeat the Task 16 link checker after the evidence update.

Expected: only the evidence update is uncommitted and all task commits are visible.

- [ ] **Step 9: Commit observed evidence**

```powershell
git add docs/benchmarks/phase-3b-current-status.md
git commit -m "docs: record phase 3b verification evidence"
```

- [ ] **Step 10: Confirm a clean checkpoint**

Run:

```powershell
git status --short --branch
```

Expected: clean `codex/phase3b-health-timeout-retry-design` branch. Use `superpowers:finishing-a-development-branch` only after this verification is complete.

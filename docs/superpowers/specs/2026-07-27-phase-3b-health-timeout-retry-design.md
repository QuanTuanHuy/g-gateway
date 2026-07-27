# Phase 3B — Health, Timeout, and Replay-Safe Retry

- Date: 2026-07-27
- Status: Approved
- Delivery strategy: risk-first vertical slice
- Architecture choice: registry-managed shared resilience runtime
- Compatibility target: Go-native safety-first semantics, not exact APISIX behavioral parity

## 1. Purpose

Phase 3B adds bounded upstream resilience to the standalone Go data plane delivered by Phase 3A. It must detect failed endpoints, stop selecting known-unhealthy endpoints, recover endpoints through active probes, and retry only when the request and available system budget make retry safe.

The phase removes four connected risks:

1. mutable health state could accidentally enter immutable snapshots or force snapshot rebuilds;
2. retries could replay unsafe requests or amplify an upstream outage;
3. active checking could create one goroutine per endpoint or an unbounded probe queue;
4. timeout and retry behavior could become inconsistent across routes, transports, and response streaming.

Phase 3B does not claim exact APISIX runtime semantics. It targets equivalent capability with safer defaults:

- all-unhealthy is fail-closed;
- retries are opt-in and replay-safe;
- retry amplification is bounded across requests;
- health and retry mutable state are explicitly separated from configuration snapshots.

## 2. Starting point and invariants

Phase 3B starts from the Phase 3A contracts:

- strict `gateway/v1alpha3` resources;
- immutable runtime snapshots and one-revision request leases;
- canonical endpoint identity;
- registry-backed upstream plans;
- shared HTTP/1.1 transports and connection pools;
- deterministic weighted round-robin and consistent hash;
- transactional prepare, commit, rollback, retirement, and reaping;
- bounded upstream lifecycle telemetry.

The following invariants remain mandatory:

- a request sees one immutable configuration revision for its entire lifecycle;
- health transitions do not rebuild or swap `RuntimeSnapshot`;
- the request hot path performs no configuration decoding, reference resolution, or global locking;
- unrelated configuration updates preserve connection pools and mutable resilience state;
- failed candidate preparation leaves the active revision unchanged;
- queues, workers, attempts, retained generations, and metric label sets are bounded;
- request and response bodies remain streamed rather than buffered by default.

## 3. Scope

### 3.1. Included

Phase 3B includes:

- strict `gateway/v1alpha4` resources and compatibility normalization;
- active HTTP and TCP health checks;
- passive health outcome tracking;
- a three-state endpoint health model;
- health-aware WRR and consistent-hash selection;
- a bounded shared probe scheduler and worker pool;
- upstream defaults plus limited route overrides for timeout and retry;
- a true total proxy deadline;
- replay-safe retries with per-request endpoint exclusion;
- adaptive per-upstream retry budgets;
- transactional resilience-runtime reuse and retirement;
- bounded health/retry metrics and transition logs;
- deterministic integration tests and provisional local performance evidence.

### 3.2. Excluded

Phase 3B does not include:

- circuit breaking or half-open traffic probes;
- priority groups, least-connections, EWMA, or hedged requests;
- HTTPS active probes, upstream TLS/mTLS, or HTTP/2 upstreams;
- dynamic downstream certificates or SNI;
- WebSocket or CONNECT;
- response-body buffering or disk spooling for retry;
- public per-endpoint health-control APIs;
- access logging;
- service discovery;
- a public configuration update surface or remote control plane;
- canonical APISIX comparison or production certification.

Phase 3C owns TLS, protocol, dynamic certificate, and WebSocket work. Phase 3D owns bounded access logging, richer integrated resilience telemetry, and canonical APISIX comparison.

## 4. Resource and compatibility contract

### 4.1. Schema version

Phase 3B introduces strict `gateway/v1alpha4`.

`gateway/v1alpha1`, `gateway/v1alpha2`, and `gateway/v1alpha3` remain accepted and normalize into the new canonical model without changing their runtime behavior:

- active and passive health remain disabled;
- `max_attempts` is `1`;
- no new total timeout is applied;
- existing dial and response-header timeout values remain unchanged.

New `v1alpha4` resources receive the safety defaults defined below.

### 4.2. Upstream health policy

An upstream may define:

```yaml
health:
  active:
    type: http
    timeout: 1s
    healthy_interval: 5s
    unhealthy_interval: 2s
    healthy_successes: 2
    http_failures: 3
    transport_failures: 2
    timeouts: 2
    healthy_statuses: [200, 204]
    unhealthy_statuses: [429, 500, 502, 503, 504]
    path: /healthz
    host: backend.internal
  passive:
    http_failures: 5
    transport_failures: 2
    timeouts: 2
    unhealthy_statuses: [429, 500, 502, 503, 504]
```

The active checker type is `http` or `tcp`. HTTPS belongs to Phase 3C.

For active HTTP results:

- a status in `healthy_statuses` is a success;
- a status in `unhealthy_statuses` is an HTTP failure;
- any other valid HTTP status is neutral and changes no counter.

For passive HTTP results:

- a status in `unhealthy_statuses` is an HTTP failure;
- any other completed HTTP response is a success.

Timeout errors increment only the timeout category. Other connection, dial, reset, or premature-EOF errors increment only the transport-failure category.

Passive health is valid only when active health is also configured. This prevents a fail-closed endpoint from becoming permanently unreachable with no recovery mechanism.

### 4.3. Upstream retry policy

An upstream may define:

```yaml
retry:
  max_attempts: 1
  methods: [GET, HEAD, OPTIONS]
  retry_on:
    connect_failure: true
    connection_failure: true
    response_header_timeout: true
    statuses: []
  budget:
    ratio_per_1000: 100
    burst: 10
    max_inflight: 32
  total_timeout: 30s
```

`max_attempts` includes the primary attempt. The default value `1` means retry is disabled until the operator explicitly enables it.

The default method set is `GET`, `HEAD`, and `OPTIONS`. Operators may configure other methods, but method permission alone never makes a body replayable.

Retryable HTTP statuses are configurable. Only `408`, `425`, `429`, and `500` through `599` are valid retry statuses.

### 4.4. Route override

A route may define a `resilience` override containing only:

- `total_timeout`;
- `max_attempts`;
- `methods`;
- `retry_on`.

Route fields replace the corresponding upstream field when present. Lists replace rather than merge. Missing fields inherit the upstream value.

A route cannot override:

- active or passive health;
- retry-budget ratio, burst, or concurrency;
- transport dial timeout;
- transport response-header timeout;
- probe scheduler limits.

This boundary permits route-specific request semantics without fragmenting shared connection pools or shared health state.

### 4.5. Global health scheduler settings

Bootstrap runtime configuration adds:

```yaml
runtime:
  health:
    workers: 16
    ready_queue_capacity: 4096
```

Validation bounds are:

- workers: `1..256`;
- ready queue capacity: `1..65536`;
- `max_attempts`: `1..5`;
- `ratio_per_1000`: `0..1000`;
- retry burst: `1..1000`;
- retry max inflight: `1..1000`;
- active probe timeout: `10ms..30s`;
- active intervals: `100ms..1h`;
- total timeout: `1ms..10m`.

For an active HTTP checker, the health path must begin with `/`, and healthy/unhealthy status lists must be non-empty, contain valid statuses, contain no duplicates, and be disjoint. Active HTTP-only fields and status lists are rejected for a TCP checker. Passive HTTP status classification remains valid regardless of the active checker type because proxied application traffic is HTTP.

At least one active unhealthy threshold must be enabled. If passive health is present, at least one passive unhealthy threshold must be enabled. Thresholds are positive integers with a hard maximum of `254`.

Invalid resources reject the entire candidate transaction.

## 5. Runtime architecture and ownership

The ownership graph is:

```text
RuntimeSnapshot (immutable)
  └─ CompiledRoute
       ├─ effective immutable route resilience override
       └─ Upstream Plan
            ├─ immutable balancer data
            ├─ shared TransportRuntime
            ├─ shared UpstreamResilienceRuntime
            │    └─ RetryBudget
            └─ EndpointHealth handles

Upstream Registry
  ├─ transactional reference ownership
  ├─ health and budget fingerprint reuse
  └─ retirement and cleanup

HealthCoordinator
  ├─ one bounded scheduler
  ├─ one bounded ready queue
  └─ bounded HTTPProber/TCPProber workers
```

### 5.1. Immutable plan data

The plan continues to own immutable endpoint order, weights, WRR schedule, consistent-hash continuum, hash-key extractor, and transport reference.

Each positive-weight plan endpoint also holds a pointer to an `EndpointHealth` runtime. Selection reads only the runtime's atomic selectable state.

Weight-zero endpoints remain administratively disabled:

- they are not selectable;
- they are not actively probed;
- they do not receive a health tracker.

A positive-to-positive weight change preserves health state. Crossing the zero-weight boundary changes active endpoint membership and creates a new `unknown` tracker when the endpoint is enabled again.

### 5.2. Mutable endpoint health

`EndpointHealth` owns:

- atomic public state;
- success and categorized failure counters;
- policy fingerprint and generation;
- scheduler activation and retirement flags;
- bounded transition and probe statistics.

Counter updates occur on probe/attempt completion paths. Selection does not acquire the counter mutex; it performs one atomic state load.

### 5.3. Mutable upstream resilience

`UpstreamResilienceRuntime` owns:

- one adaptive retry budget;
- references to active endpoint-health runtimes;
- lazy checker activation state;
- pre-bound or atomic telemetry handles.

The runtime is keyed by upstream identity and the relevant retry-budget fingerprint. A route-policy change does not reset the budget when the upstream budget itself is unchanged.

### 5.4. Reuse keys

Health reuse requires:

- the same canonical endpoint identity;
- the same positive-weight membership;
- the same health-policy fingerprint.

Retry-budget reuse requires:

- the same upstream identity;
- the same retry-budget fingerprint.

Changing health thresholds, intervals, checker type, path, host, or status classification creates a new health runtime in `unknown`. Changing retry rules without changing the budget does not reset earned tokens.

## 6. Endpoint health state machine

The public state is one of:

- `unknown`;
- `healthy`;
- `unhealthy`.

`unknown` and `healthy` are selectable. `unhealthy` is not selectable.

All endpoints start as `unknown` without waiting for an initial probe. This avoids cold-start `503` responses while preserving explicit observability of unverified endpoints.

Transitions are:

```text
unknown   -- healthy_successes reached --> healthy
unknown   -- any failure threshold reached --> unhealthy
healthy   -- any failure threshold reached --> unhealthy
unhealthy -- active healthy_successes reached --> healthy
```

Counter behavior is deterministic:

- a success resets all failure counters and increments the success streak;
- any failure resets the success streak;
- a failure increments only its category counter;
- other failure-category counters retain their values until a success or state transition;
- a neutral active HTTP result changes no counter;
- every state transition resets all counters;
- passive success may move `unknown` to `healthy`;
- only active success may move `unhealthy` to `healthy`.

Active and passive observations feed the same tracker. A successful active probe therefore clears passive failures, and a successful passive request clears active failures while the endpoint remains selectable.

## 7. Active health scheduling

### 7.1. Lazy activation

An upstream checker activates on the first matched request that reaches upstream selection. Activation is a non-blocking, once-only signal.

Configured but unused upstreams create no scheduled probe tasks. With 10,000 unused upstreams and 100,000 endpoints, the ready queue remains empty and goroutine count remains constant.

After activation, positive-weight endpoints remain scheduled until their resilience runtime retires.

### 7.2. Scheduler

The coordinator uses:

- one time-ordered scheduler;
- one bounded ready queue;
- a fixed worker pool.

It does not create a ticker or goroutine per endpoint.

Initial probe time includes deterministic jitter derived from endpoint identity. Jitter is in `[0, 10%]` of the applicable interval. Subsequent healthy/unknown probes use `healthy_interval`; unhealthy probes use `unhealthy_interval`.

If the ready queue is full:

- request traffic and reconcile never block;
- duplicate readiness for the same endpoint is coalesced;
- the probe is rescheduled with bounded backoff;
- a closed-enum reschedule metric is incremented.

### 7.3. Probers

`HTTPProber` and `TCPProber` implement a small internal probe interface and are independently testable.

HTTP probes:

- use the endpoint host/port, configured path, and optional Host header;
- use a dedicated bounded probe transport, not the production proxy pool;
- do not follow redirects;
- inspect headers/status only;
- drain at most `4 KiB` before closing the body.

TCP probes:

- dial with the configured probe timeout;
- treat successful connection establishment as success;
- close the connection immediately.

Probe completion applies its result only when the endpoint generation is still active. Results from retired or replaced policies are discarded.

## 8. Health-aware selection

`Plan.Select` evolves into selection with request-local exclusions.

For the primary attempt, the exclusion set is empty. Each attempted endpoint ordinal is then excluded from later attempts.

WRR:

- advances through the existing deterministic schedule;
- skips unhealthy and excluded ordinals;
- performs a bounded scan;
- preserves the relative schedule of the remaining selectable endpoints.

Consistent hash:

- starts at the normal hash point;
- walks clockwise;
- skips unhealthy and excluded endpoints;
- tracks endpoint ordinal so virtual nodes do not cause the same endpoint to be attempted twice;
- stops after all distinct positive-weight endpoints have been considered.

The balancer never selects a known-unhealthy endpoint as a last resort.

If no selectable endpoint remains:

- before any attempt, selection returns `ErrNoHealthyEndpoint`;
- the proxy returns `503 UPSTREAM_UNHEALTHY`;
- after one or more attempts, the proxy returns the most recent response or mapped transport error.

## 9. Timeout semantics

### 9.1. Transport timeouts

Dial timeout and response-header timeout remain upstream transport-profile properties. They continue to participate in the transport fingerprint and connection-pool reuse rules.

Phase 3B does not add a separate response-body idle timeout.

### 9.2. Total timeout

After request plugins complete and before primary selection, the proxy computes:

```text
effective deadline = min(existing client deadline, now + effective total_timeout)
```

The total deadline covers:

- selection;
- all primary and retry attempts;
- request-body streaming;
- response-header wait;
- response-body streaming to the downstream client.

A route/plugin short circuit does not enter this timeout scope.

If the deadline expires before response headers are committed, the gateway returns:

```text
504 UPSTREAM_TIMEOUT
```

If it expires after downstream headers are committed, the gateway terminates the stream and records a bounded timeout outcome. It does not attempt to write a second JSON response.

Client cancellation always wins and is never retried.

## 10. Replay-safe retry

### 10.1. Eligibility

A request is eligible for retry only when:

- its method is in the effective configured method set;
- its body is absent (`nil` or `http.NoBody`) or `GetBody` can construct a fresh body;
- its context is not canceled;
- its total deadline has not expired.

Phase 3B never buffers or spools a body to make it replayable.

For server-side inbound requests, a body with unknown length and no `GetBody` is non-replayable even if no bytes happened to be read before failure. It receives one attempt.

### 10.2. Attempt loop

Request plugins execute once before the loop. Response plugins execute once on the final response.

The attempt loop is:

1. activate the upstream checker;
2. credit the upstream retry budget for one primary request;
3. select a healthy/unknown endpoint not already attempted;
4. clone the outbound request and set the selected target;
5. reconstruct the body for attempts after the first;
6. perform the transport round trip;
7. report exactly one passive outcome;
8. decide whether the result is retryable;
9. for a retry, acquire budget and an untried endpoint;
10. return the final response or error.

The same endpoint is never attempted twice within one request. The actual attempt count may therefore be lower than `max_attempts`.

### 10.3. Retry classification

Retry classification and passive health classification are separate policies.

Retry may be configured for:

- connection establishment failure;
- connection reset, premature EOF, or other connection failure before valid response headers;
- response-header timeout;
- an explicitly listed HTTP status.

The following are never retried:

- client cancellation;
- expiration of the total deadline;
- a response already committed downstream;
- a non-replayable body;
- an error/status not listed in effective `retry_on`;
- lack of another untried selectable endpoint;
- lack of retry budget.

For a retryable HTTP response, the proxy drains at most `32 KiB` and closes the body before the next attempt. Reaching EOF permits connection reuse. Exceeding the cap abandons reuse for that connection and prevents unbounded drain work.

### 10.4. Final result

When retry stops:

- a final HTTP response is passed through unchanged;
- a final timeout maps to `504 UPSTREAM_TIMEOUT`;
- a final transport failure maps to `502 UPSTREAM_CONNECTION_FAILED`;
- an all-unhealthy selection before any attempt maps to `503 UPSTREAM_UNHEALTHY`;
- budget suppression preserves the most recent response or error.

## 11. Adaptive retry budget

The budget is local to one upstream runtime and has two independent bounds:

- token credits limit retry rate over time;
- `max_inflight` limits concurrent retry attempts.

Accounting uses fixed-point integer credits:

- one retry token is `1000` credits;
- each primary request adds `ratio_per_1000` credits;
- the bucket starts with `burst * 1000` credits;
- credits saturate at `burst * 1000`;
- each retry consumes `1000` credits permanently.

For example, `ratio_per_1000: 100` earns one retry for every ten primary requests after the initial burst.

Retry acquisition first reserves an inflight slot, then consumes a token. If no token is available, the inflight slot is immediately released. The inflight slot is released when that retry attempt ends. Tokens are not returned after a failed retry.

The following invariants are mandatory under concurrency:

- token credits never become negative;
- inflight retries never exceed `max_inflight`;
- total retries never exceed earned whole tokens plus initial burst;
- a budget failure suppresses retry rather than failing the primary result;
- no global retry lock is used across upstreams.

## 12. Reconcile, retirement, and shutdown

### 12.1. Transactional reconcile

Reconcile remains:

```text
decode and validate
  -> prepare plans and resilience runtimes
  -> commit snapshot atomically
  -> retire old plan set
  -> reap unreferenced runtimes
```

Candidate preparation must acquire all health, budget, scheduler-registration, endpoint, selection, and transport references transactionally.

Rollback releases every acquired reference. It leaves no scheduled task, worker, goroutine, token bucket, or probe transport behind.

Positive-weight changes, route-only changes, and unrelated-upstream changes preserve health, budget, and production transport when their reuse keys are unchanged.

### 12.2. Retirement

Removing an endpoint, upstream, or policy retires the corresponding runtime after the owning plan-set leases drain.

On retirement:

- future scheduler enqueue is disabled;
- queued work is ignored by generation check;
- in-flight probe results are discarded;
- counters remain readable until final cleanup;
- owned resources are released exactly once.

Registry resource/backpressure statistics expand to include live health trackers, live retry budgets, and retired resilience runtimes.

### 12.3. Failure isolation

Probe queue saturation does not block requests or reconcile.

A worker panic is recovered at the probe-task boundary. It increments an internal-failure metric but does not directly mark the endpoint unhealthy.

Retry-budget bookkeeping failure behaves as no budget and suppresses retry.

Health-coordinator degradation does not automatically make the whole gateway unready. The active snapshot can continue serving using its last known health states. A bounded degraded/recovered event and metric expose the condition.

### 12.4. Shutdown

Shutdown order is:

1. set readiness false and stop accepting new traffic;
2. stop scheduling new probes;
3. drain active requests and retries;
4. cancel remaining probes with a bounded deadline;
5. retire and finalize plan sets and resilience runtimes;
6. close production and probe idle transports;
7. wait for the scheduler and workers to exit.

Unsuccessful drain returns a non-zero process exit as in earlier phases.

## 13. Observability

Phase 3B adds the minimum bounded resilience telemetry needed for operation and verification.

Metrics are:

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

Label rules:

- `upstream_id` is allowed because upstream count is configuration-bounded;
- endpoint identity, endpoint URL, route ID, raw status, and error text are not new resilience labels;
- state, source, type, outcome, result, and reason are closed enums;
- HTTP statuses are classified into a closed outcome/status-class set.

Request-path metrics use pre-bound handles or runtime atomics. They do not perform dynamic Prometheus label lookup on every attempt.

Structured logs are emitted only for:

- endpoint state transition;
- health coordinator degraded/recovered transition;
- reconcile/lifecycle failure;
- unsuccessful shutdown drain.

The gateway does not log every probe, attempt, or retry.

Request context retains final endpoint, total attempt count, retry-suppression reason, and final upstream outcome so Phase 3D can add access logs without changing the attempt engine.

Phase 3B does not add a full endpoint-health dump. A surface for 100,000 endpoints requires pagination, authorization, and output budgets and belongs to a later phase.

## 14. Testing strategy

### 14.1. Unit tests

Unit tests cover:

- strict `v1alpha4` decoding and unknown-field rejection;
- legacy normalization with no behavior change;
- defaulting and route override replacement;
- invalid method, status, duration, threshold, and budget bounds;
- health-policy and budget fingerprint stability;
- every health transition and counter reset rule;
- HTTP and TCP outcome classification;
- lazy activation and deterministic jitter;
- retry eligibility and body replay;
- retry-budget fixed-point and inflight accounting;
- WRR and consistent-hash health/exclusion behavior;
- final error/status mapping.

Time-dependent tests use an injected clock and do not sleep.

### 14.2. Property and fuzz tests

Property/fuzz tests assert:

- an unhealthy endpoint is never selected;
- one request never attempts the same endpoint twice;
- attempts never exceed effective `max_attempts`;
- body reconstruction occurs exactly once per retry;
- token credits and inflight counts stay within bounds;
- arbitrary health event sequences preserve legal states;
- arbitrary strict configuration input cannot panic the decoder/compiler.

### 14.3. Concurrency and race tests

Concurrency tests cover:

- selection while active and passive state changes occur;
- many requests contending for one retry budget;
- reconcile while probes are queued or running;
- stale probe completion after policy replacement;
- rollback after partial resilience-runtime preparation;
- retirement while request leases remain;
- scheduler saturation and shutdown.

The race detector is mandatory on a CGO-capable reference environment.

### 14.4. Integration tests

A programmable upstream test matrix covers:

- active HTTP failure and recovery;
- active TCP failure and recovery;
- passive status/transport/timeout ejection;
- all-unhealthy `503`;
- connection, reset, response-header-timeout, and status retries;
- non-replayable request sent once;
- replayable request reconstructed correctly;
- retry endpoint exclusion;
- retry-budget exhaustion and recovery;
- total deadline before headers and during response streaming;
- route override precedence;
- positive weight-only update preserving health and pool;
- health-policy update resetting only affected health runtime;
- unrelated-upstream update isolation;
- lazy activation and bounded queue saturation;
- graceful shutdown with active probes and retries.

## 15. Benchmark and resource evidence

Phase 3B records both a normal developer profile and an opt-in full-envelope profile.

Normal profile:

- 1,000 upstreams;
- 10,000 endpoints;
- bounded activated subset;
- health/retry microbenchmarks;
- repeated reconcile and quiescence.

Full profile:

- 10,000 upstreams;
- 100,000 endpoints;
- all unused for lazy-scheduler evidence;
- a controlled activated subset for scheduler evidence;
- 20 reconcile swaps.

Acceptance targets are:

- all-healthy health-aware selection remains `0 alloc/op`;
- all-healthy selector median is no more than `2x` the Phase 3A selector median on the same host/run;
- healthy end-to-end throughput is at least `95%` of Phase 3A baseline;
- healthy p99 latency is no more than `110%` of Phase 3A baseline;
- unused configured endpoints create no probe queue entries;
- health subsystem goroutine count is `O(worker_count)`, not `O(endpoint_count)`;
- retry inflight and token amplification invariants hold under contention;
- normal-load recovery completes within
  `healthy_successes * unhealthy_interval + probe_timeout + 250ms`;
- after reconcile storm, lease drain, and boundary GC, retired resilience runtimes return to zero;
- retained heap after quiescence is no more than `1.25x` one active Phase 3B plan-set/runtime footprint.

Developer-machine results are provisional. Absolute resource gates, full race evidence, and canonical Linux results remain pending until run in the required environment.

## 16. Exit criteria

Phase 3B implementation is complete when:

- strict `v1alpha4` and legacy compatibility tests pass;
- health state remains outside snapshots and transitions without snapshot rebuild;
- active HTTP/TCP and passive health failure/recovery tests pass;
- all-unhealthy behavior deterministically returns `503`;
- non-replayable requests are never retried;
- replayable retries never repeat an endpoint;
- attempts, deadlines, tokens, inflight retries, queues, workers, and retained runtimes respect their bounds;
- route override and transport-pool reuse tests pass;
- failure, reconcile, retirement, and shutdown tests pass;
- formatting, vet, unit, integration, fuzz-smoke, build, and locally available race gates pass;
- normal benchmark/resource evidence is recorded;
- example configuration, runbook, benchmark status, README, and roadmap are updated.

When functional and provisional local gates pass, the allowed status is:

```text
implementation complete; canonical resilience evidence pending
```

This status does not mean:

- Phase 2 deferred Task 16 is complete;
- Phase 3A canonical evidence is complete;
- Phase 3 is accepted;
- APISIX parity is proven;
- the gateway is production-certified.

## 17. Implementation boundaries

Expected ownership by package is:

```text
internal/model       canonical v1alpha4 policy types
internal/config      strict decode, defaults, compatibility, validation
internal/upstream    health, probe, scheduler, budget, selection, lifecycle
internal/proxy       effective policy, deadline, attempt loop, final mapping
internal/telemetry   bounded resilience metrics
internal/gateway     composition, lifecycle events, shutdown ordering
test/integration     failure, recovery, retry, and deadline scenarios
```

Files and types should remain focused:

- health transition logic must be independently testable without networking;
- HTTP/TCP probers must not own scheduling;
- the scheduler must not understand balancer algorithms;
- retry budget must not understand HTTP classification;
- the attempt engine must consume interfaces rather than registry internals;
- registry lifecycle remains the only owner of shared-runtime reuse and cleanup.

No extension point is added unless Phase 3B has a real implementation using it.

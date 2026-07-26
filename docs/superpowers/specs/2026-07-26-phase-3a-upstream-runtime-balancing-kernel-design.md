# Phase 3A — Upstream runtime and balancing kernel

- Date: 2026-07-26
- Status: Approved
- Implementation status: `implementation complete; canonical resource evidence pending`
- Parent phase: Phase 3 — Upstream resilience
- Delivery strategy: Risk-first vertical slice

## 1. Purpose

Phase 3 is too large for one implementation cycle. It is split into four independently verifiable sub-phases:

1. Phase 3A — upstream runtime registry, transport reuse, dynamic reconcile, weighted round-robin, and consistent hash;
2. Phase 3B — active/passive health, timeout policy, replay-safe retry, and retry budget;
3. Phase 3C — upstream TLS/mTLS, dynamic downstream TLS/SNI, upstream protocols, and WebSocket;
4. Phase 3D — bounded access logging, resilience telemetry, integrated acceptance, and APISIX comparison.

Phase 3A proves that the standalone data plane can change multi-endpoint upstream configuration without restarting, preserve unrelated connection pools, and select endpoints on a bounded zero-allocation hot path.

It replaces the fixed Phase 2 upstream table while preserving the Phase 2 contracts:

- canonical resource IDs and resolved upstream references;
- immutable request snapshots;
- shadow build and atomic activation;
- deterministic router precedence;
- compiled plugin and typed request-context contracts;
- no mutable registry lookup from the request path.

Phase 2 remains at `implementation complete; canonical evidence pending`. Phase 3A does not hide or satisfy the deferred Phase 2 Task 16 evidence gates.

## 2. Goals

Phase 3A must provide:

- strict `gateway/v1alpha3` upstream configuration with v1alpha1/v1alpha2 compatibility;
- multiple weighted endpoints per upstream;
- dynamic upstream add, remove, and update through the internal `Gateway.Apply` entry point;
- immutable compiled upstream plans bound to runtime snapshots;
- a lifecycle-aware registry for endpoint, balancer, and transport operational state;
- transport sharing by a complete transport-profile identity;
- deterministic weighted round-robin;
- deterministic Ketama-style consistent hash;
- exact snapshot retirement after in-flight requests release their leases;
- transactional prepare, build, publish, rollback, and cleanup behavior;
- bounded configuration, schedule, continuum, retired-generation, and telemetry cardinality;
- correctness, concurrency, allocation, scale, memory, and connection-reuse evidence.

## 3. Non-goals

Phase 3A does not add:

- active or passive health checks;
- retry, retry budgets, or total request deadlines;
- circuit breaking;
- endpoint priority, least-connections, EWMA, or random balancing;
- service discovery or DNS-driven endpoint membership;
- HTTPS/mTLS upstreams, upstream HTTP/2, or gRPC;
- dynamic downstream SNI certificates;
- WebSocket or CONNECT;
- a request access-log exporter;
- a public reload endpoint, Admin API, control plane, or configuration distribution;
- canonical Phase 2 or Phase 3 APISIX end-to-end performance evidence.

Connect and response-header timeouts already represented by `http.Transport` remain supported. Phase 3B owns attempt, retry, and total-deadline semantics.

## 4. Architecture

The accepted architecture separates immutable configuration from mutable operational state:

```text
Gateway.Apply(revision, ResourceSet)
    |
    +-> validate and canonicalize v1alpha3
    |
    +-> Registry.Prepare(upstream resources)
    |      +-> reuse/create TransportRuntime by transport profile
    |      +-> reuse/create EndpointRuntime by canonical identity
    |      +-> reuse/create SelectionState
    |      +-> compile immutable UpstreamPlan
    |
    +-> SnapshotBuilder.Build(routes, plugins, plans)
    |
    +-> atomically publish RuntimeSnapshot
    |
    +-> retire old snapshot
           +-> wait for its final request lease
           +-> release plan/endpoint/transport ownership off-path
           +-> close idle pools with no remaining owner
```

Component responsibilities are:

- `runtime.Snapshot` owns immutable routes, plugin chains, and `UpstreamPlan` references for one revision.
- `upstream.Registry` owns resource identity, reference counts, candidate transactions, retirement, and cleanup.
- `upstream.TransportRuntime` owns one shared `http.Transport` and its connection pools.
- `upstream.EndpointRuntime` owns the stable identity and future operational state of one endpoint within one upstream.
- `upstream.SelectionState` owns the WRR atomic cursor for an upstream and balancer family.
- `upstream.Plan` owns immutable endpoint entries, compiled selection data, and a compiled hash-key extractor.
- `runtime.Manager` owns publication, snapshot leases, and retirement.
- `proxy` acquires one snapshot, selects through its plan, and never looks up current upstream configuration in the registry.

No new general-purpose shared, utility, or adapter layer is introduced. Interfaces are added only where Phase 3A has more than one real implementation or a test needs a failure seam.

## 5. Canonical model and wire compatibility

### 5.1. Canonical types

The canonical model becomes:

```text
Upstream
  ID        string
  Endpoints []Endpoint
  Balancer  BalancerPolicy
  Transport TransportConfig

Endpoint
  URL       string
  Weight    uint32

BalancerPolicy
  Type      weighted_round_robin | consistent_hash
  HashKey   HashKeyPolicy

HashKeyPolicy
  Sources   []HashKeySource

HashKeySource
  Type      header | cookie | remote_addr | literal
  Name      string
  Value     string
```

The canonical model contains configuration only. It does not contain counters, health, connection pools, locks, or registry ownership.

### 5.2. `gateway/v1alpha3`

An example v1alpha3 upstream is:

```yaml
api_version: gateway/v1alpha3

runtime:
  max_retired_snapshots: 64

upstreams:
  - id: users
    endpoints:
      - url: http://users-a:8080
        weight: 5
      - url: http://users-b:8080
        weight: 2

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
      max_idle_connections: 4096
      max_idle_connections_per_host: 512
```

`runtime.max_retired_snapshots` is bootstrap configuration, defaults to `64`, and accepts `1..1024`. The reaper interval remains an internal implementation constant because operators do not need it as a tuning surface in Phase 3A.

### 5.3. Backward compatibility

- v1alpha1 and v1alpha2 upstream endpoint strings convert to endpoint objects with weight `1`.
- Their balancer defaults to `weighted_round_robin`.
- Their one-endpoint behavior and transport configuration remain unchanged.
- Unknown fields remain rejected in every wire version.
- Wire types convert into the canonical model before domain validation; runtime code does not branch on wire version.

## 6. Endpoint identity and validation

The endpoint identity is:

```text
upstream ID + scheme + normalized host + effective port
```

Weight, list order, balancer policy, and transport policy do not participate in endpoint identity.

Normalization rules are:

- only `http` is accepted in Phase 3A;
- URL user information, query, and fragment are rejected;
- path must be empty or `/` and canonicalizes to empty;
- DNS names are ASCII, lowercase, and have one trailing dot removed;
- IP literals use canonical `netip.Addr.String()` form;
- IPv4-mapped IPv6 addresses are unmapped;
- an omitted HTTP port canonicalizes to `80`;
- empty hosts and invalid ports are rejected.

DNS resolution stays inside `net.Dialer` when a connection is opened. A DNS answer change does not change registry membership or endpoint identity.

Validation limits are:

- at most 10,000 upstreams;
- at most 100,000 endpoints in one resource set;
- at most 1,000 endpoints per upstream;
- endpoint weight in `0..1000`;
- at least one endpoint with positive weight per upstream;
- no duplicate canonical endpoint identity within an upstream;
- at most eight hash-key sources;
- bounded header, cookie, and literal names/values under the existing header/config byte limits;
- no hash-key policy on weighted round-robin;
- a non-empty valid hash-key policy on consistent hash.

Weight `0` means administratively disabled and excludes the endpoint from compiled selection structures. It does not create a request-time branch that can select the endpoint.

## 7. Transport and endpoint runtime registry

### 7.1. Transport profile

Transport runtimes are shared by a collision-free comparable profile key. For Phase 3A the key contains:

- upstream protocol, fixed to HTTP/1.1;
- dial timeout;
- response-header timeout;
- idle-connection timeout;
- maximum idle connections;
- maximum idle connections per host;
- compression behavior and every other field that changes `http.Transport` connection semantics.

Request total timeout, balancing policy, endpoint weight, and future retry policy are not part of the transport key.

Future Phase 3C TLS roots, client identity, verification policy, SNI behavior, and protocol settings must extend this key before TLS transports can be shared.

One `http.Transport` may serve multiple upstreams with the same profile. Go's transport continues to partition pooled connections by origin. The registry closes idle connections only when the final plan owner releases the transport runtime.

### 7.2. Endpoint runtime

`EndpointRuntime` contains:

- canonical endpoint identity;
- immutable parsed target URL.

Endpoint runtime identity includes the upstream ID. Two upstream resources pointing to the same origin do not share health state, although their plans may share a transport runtime.

Phase 3A does not add unused health fields or a health-provider interface. Phase 3B may extend this focused type when health behavior has a real implementation.

### 7.3. Selection state

Selection-state ownership is keyed by stable upstream ID and balancer family. The state contains an atomic sequence used by WRR. A weight or endpoint-set change may reuse the state; an algorithm-family change creates a new state. Consistent-hash selection does not mutate the state in Phase 3A.

Mutable registry state is reachable through pointers held by a plan, but plan configuration and membership remain immutable.

## 8. Transactional reconcile

`Gateway.Apply` remains serialized. It executes:

1. reject updates after shutdown begins;
2. reject stale revisions and an exceeded retired-snapshot limit;
3. strictly validate and canonicalize the complete resource set;
4. call `Registry.Prepare` to acquire provisional resource references and compile plans;
5. build the complete snapshot off-path;
6. on any failure, rollback every provisional reference and close only newly created unowned transports;
7. transfer candidate ownership to the new snapshot;
8. atomically swap the active pointer;
9. retire the old snapshot and release its manager ownership;
10. notify observers after publication.

The active revision is unchanged on validation, prepare, plan compile, router compile, plugin compile, or snapshot invariant failure.

Reuse rules are:

- changing only weight or balancer configuration creates a new plan and reuses endpoint and transport runtimes;
- changing only transport configuration creates a new transport runtime and reuses endpoint runtimes;
- adding an endpoint creates one endpoint runtime;
- removing an endpoint keeps its runtime alive while any retired snapshot still references it;
- updating upstream A does not change the endpoint, selection, or transport ownership of unrelated upstream B;
- changing an upstream ID creates new endpoint and selection identities.

## 9. Snapshot leases and retirement

Each published snapshot starts with one manager-ownership reference.

`Manager.Acquire`:

1. loads the active pointer;
2. uses CAS to increment its positive reference count;
3. retries with the current active pointer if the loaded snapshot already reached zero.

`Lease.Release` performs one guarded atomic decrement and is idempotent for the same lease value. The underlying plan-set reference counter still treats an unguarded double release or underflow as a programming invariant violation in tests.

Publication uses an atomic pointer swap. A request that acquired the old snapshot before publication may finish on the old plan. A request that acquires after publication receives the new snapshot. No request combines route/plugin state from one revision with upstream membership from another.

Final release does not scan plans, take registry locks, or close connections on the request goroutine. It emits a non-blocking reaper wake-up. The reaper also scans on a fixed periodic tick so a coalesced or missed wake-up cannot leak retired resources.

The reaper:

- tracks retired snapshots registered during serialized apply;
- releases their plan/resource ownership after the reference count reaches zero;
- performs plan and transport cleanup outside request goroutines;
- removes fully released registry entries;
- calls `CloseIdleConnections` exactly once for an unowned transport.

At most `runtime.max_retired_snapshots` snapshots may wait for leases. Once the limit is reached, `Apply` returns `RETIRED_SNAPSHOT_LIMIT` and retains the active revision. Requests and streams already using old snapshots are never terminated to make room for a new revision.

## 10. Weighted round-robin

The WRR compiler excludes zero-weight endpoints and emits a deterministic `[]uint32` schedule of endpoint indexes.

Compilation:

1. divide positive weights by their GCD;
2. if the normalized sum is at most 8,192, assign the exact number of slots;
3. otherwise apportion 8,192 slots by largest remainder, with at least one slot for every active endpoint;
4. interleave assigned slots using an offline weighted-fair min-heap;
5. break ties by canonical endpoint order.

The resulting slot-count error against the apportioned target is zero. Against the ideal uncapped proportion, largest-remainder allocation differs by less than one slot per endpoint.

Selection:

- one active endpoint uses a direct fast path;
- otherwise increment the shared atomic sequence and index the immutable schedule modulo its length;
- perform no allocation, lock, map lookup, parsing, or registry lookup.

The snapshot-wide WRR schedule budget is 8,000,000 slots. A candidate exceeding it is rejected with `BALANCER_BUDGET_EXCEEDED`.

## 11. Consistent hash

Phase 3A uses a Ketama-style immutable continuum with a stable xxHash64 function. The project promotes `github.com/cespare/xxhash/v2`, already present transitively, to a direct dependency.

Continuum compilation:

1. divide positive weights by their GCD;
2. target 64 virtual points per normalized weight unit;
3. cap one upstream at 65,536 points;
4. when capped, apportion points by largest remainder with at least one point per active endpoint;
5. hash an unambiguous endpoint-identity and virtual-index encoding;
6. sort by point hash, then endpoint identity and virtual index for collision ties;
7. store point hashes and endpoint indexes in parallel slices to avoid struct padding.

The snapshot-wide continuum budget is 8,000,000 points. A candidate exceeding it is rejected with `BALANCER_BUDGET_EXCEEDED`.

Selection:

- one active endpoint uses a direct fast path;
- stream the compiled hash-key sources into xxHash64 without building a concatenated string;
- encode every component with a type marker and length prefix;
- binary-search the first point greater than or equal to the request hash;
- wrap to point zero at the end of the continuum.

The supported hash-key sources are:

- `header`: all values for the canonical header name in wire order;
- `cookie`: the first syntactically valid cookie with the configured name;
- `remote_addr`: the normalized immediate peer address;
- `literal`: a compile-time literal.

For a single missing header/cookie, or when all dynamic sources in a compound key are absent, selection falls back to normalized immediate `remote_addr`. A compound key with at least one present dynamic component or a literal preserves explicit missing-component markers and does not fallback.

Phase 3A does not trust `Forwarded` or `X-Forwarded-For`; trusted-proxy policy is not yet available. Hash keys and client addresses are never written to logs or metric labels.

This design targets APISIX-class consistent-hash behavior, not identical node selection. APISIX uses `resty.chash` with 160 points per normalized server weight; this implementation uses a separately bounded xxHash64 continuum and documents that compatibility boundary.

## 12. Request data flow

The request path is:

```text
acquire snapshot lease
    -> route match
    -> populate typed request context
    -> run request plugins
    -> select endpoint from the matched UpstreamPlan
    -> store selected endpoint and balancer metadata in request context
    -> ReverseProxy rewrites to the selected target
    -> shared TransportRuntime performs RoundTrip
    -> run response plugins
    -> release snapshot lease
```

Selection runs after request plugins. A compiled header-rewrite plugin may therefore intentionally affect a consistent-hash key.

The request context records:

- upstream ID;
- selected endpoint identity or bounded ordinal;
- balancer family;
- hash fallback flag;
- attempt number, fixed to one in Phase 3A.

It does not expose registry maps or mutable configuration.

Phase 3A performs one endpoint selection and one attempt. A connect or response failure does not select a different endpoint. Phase 3B will add eligible-node traversal and retry without changing snapshot-plan ownership.

## 13. Failure semantics

New stable build errors include:

- `UPSTREAM_ENDPOINTS_EMPTY`;
- `UPSTREAM_ENDPOINT_DUPLICATE`;
- `UPSTREAM_NO_ACTIVE_ENDPOINT`;
- `UPSTREAM_ENDPOINT_LIMIT`;
- `UPSTREAM_WEIGHT_INVALID`;
- `BALANCER_TYPE_INVALID`;
- `HASH_KEY_INVALID`;
- `BALANCER_BUDGET_EXCEEDED`;
- `TRANSPORT_PROFILE_INVALID`;
- `RETIRED_SNAPSHOT_LIMIT`.

Every build error includes a stable stage and field path. Candidate rollback must restore registry live-resource counts to their pre-prepare values.

Request errors are:

- missing plan or selectable endpoint caused by an internal invariant: `503 UPSTREAM_UNAVAILABLE`;
- DNS, dial, or connection failure: `502 UPSTREAM_CONNECTION_FAILED`;
- dial or response-header timeout: `504 UPSTREAM_TIMEOUT`;
- client cancellation: no new gateway response and no upstream-failure classification.

Missing hash input follows the remote-address fallback policy and does not fail the request.

## 14. Telemetry

Phase 3A adds:

- active and retired snapshot gauges;
- live endpoint-runtime, transport-profile, and selection-state gauges;
- registry create, reuse, release, rollback, and transport-cleanup counters;
- snapshot apply/reconcile duration;
- candidate rollback count;
- balancer selections by algorithm;
- hash-key fallback count;
- build rejection count by bounded error code;
- transport idle-pool cleanup count.

Allowed labels are bounded error code, balancer algorithm, revision outcome, and stable configured upstream ID. Raw URL, hostname, selected hash, header/cookie value, and client address are forbidden metric labels.

Structured logs are emitted for:

- apply/reconcile summaries;
- rollback and invariant failures;
- unexpected registry/reaper lifecycle failures;
- shutdown cleanup summary.

Endpoint selection and hash fallback do not emit per-request logs. Bounded structured request access logging belongs to Phase 3D.

## 15. Shutdown

Shutdown order is:

1. set readiness false and reject further `Apply`;
2. stop traffic listeners from accepting new requests;
3. drain requests that hold snapshot leases;
4. if graceful drain reaches its deadline, force-close traffic connections, cancel request base contexts, and wait for handler lease defers;
5. release manager ownership of the active snapshot;
6. let the reaper process retired snapshots;
7. run a synchronous final registry cleanup;
8. close every remaining shared transport idle pool exactly once;
9. stop the reaper and exit.

Forced connection close must cause request cancellation and lease release through `defer`; registry cleanup cannot run while a live handler still owns a lease. A process kill imposed outside this shutdown sequence remains outside graceful guarantees.

## 16. Test strategy

### 16.1. Configuration and model

- strict v1alpha3 decoding and unknown-field rejection;
- v1alpha1/v1alpha2 compatibility;
- endpoint normalization and identity;
- duplicate, weight, algorithm, hash-key, scale, and budget validation;
- stable code, stage, and field-path assertions.

### 16.2. Balancer correctness

- WRR schedule against an independent distribution oracle;
- exact and capped slot allocation;
- deterministic tie-breaking;
- stable xxHash64 and continuum test vectors;
- equal and weighted consistent-hash distribution;
- bounded remap ratio when an endpoint is added or removed;
- header, cookie, compound, literal, and remote-address inputs;
- missing-input fallback;
- fuzz endpoint normalization, weight compilation, hash-key streaming, and continuum construction.

### 16.3. Registry and lifecycle

- model-based prepare/commit/rollback transitions;
- endpoint, selection-state, and transport reuse;
- newly created resource rollback;
- double-release/underflow protection;
- retired-snapshot limit;
- concurrent acquire/apply/release under the race detector;
- reaper wake-up coalescing and periodic fallback;
- idempotent shutdown cleanup.

### 16.4. Integration

- multiple deterministic upstream servers return endpoint identity;
- dynamic add, remove, weight change, and algorithm change;
- an in-flight request keeps its old plan while a later request uses the new plan;
- a weight-only update retains endpoint runtimes and keepalive connections;
- updating upstream A preserves upstream B's transport and connection;
- a rejected apply retains last-known-good behavior;
- DNS, dial, and timeout error mapping.

### 16.5. Benchmarks and resources

Microbenchmarks cover:

- snapshot acquire/release;
- WRR with 1, 2, 100, and 1,000 endpoints, where 2 is the relative-scale baseline;
- consistent hash with 1, 10, 100, and 1,000 endpoints, where 10 is the relative-scale baseline;
- header, cookie, compound, and fallback hash keys;
- full and weight-only reconcile;
- active heap and retained heap after 20 swaps.

The deterministic full-envelope dataset contains 10,000 upstreams and 100,000 endpoints, with an 80/20 WRR/consistent-hash mix. Its normal endpoint weights use deterministic ratios from `1..5`, keeping selection structures inside the accepted budgets. Separate adversarial fixtures exercise weights `0` and `1000`, per-upstream caps, and snapshot-wide rejection.

Mandatory Phase 3A implementation gates on every supported development environment are:

- snapshot lease and both selectors report 0 allocations;
- WRR selection at 1,000 endpoints is no more than 125% of its two-endpoint result;
- consistent-hash selection at 1,000 endpoints is no more than 250% of its ten-endpoint result;
- the full-envelope build completes without exceeding configured schedule/continuum budgets;
- 20-swap retained heap is at most 125% of one active snapshot after reaper quiescence and boundary GC.

On the reference Go 1.26.5 Linux x86_64 environment:

- snapshot acquire/release: at most 100 ns/op and 0 allocations;
- WRR selection: at most 100 ns/op and 0 allocations;
- consistent-hash selection: at most 750 ns/op and 0 allocations;
- full-envelope plan/registry build: at most 5 seconds;
- incremental active heap for the full envelope: at most 512 MiB;
- retained heap after 20 swaps, reaper quiescence, and boundary GC: at most 125% of one active snapshot.

Docker Desktop and developer-machine values are provisional. Canonical APISIX end-to-end comparison is intentionally deferred to Phase 3D, together with the still-visible Phase 2 Task 16 debt.

## 17. Exit criteria

The Phase 3A implementation checkpoint is complete only when:

1. v1alpha1, v1alpha2, and v1alpha3 supported configurations pass compatibility tests.
2. Dynamic upstream update works through internal `Gateway.Apply`.
3. A request sees one immutable route/plugin/upstream plan revision for its entire lifecycle.
4. Weight-only update reuses 100% of unchanged endpoint and transport runtimes.
5. An unrelated upstream update preserves the other upstream's transport pointer and keepalive connection.
6. Candidate failure changes neither active revision nor live-resource counts.
7. WRR and consistent hash meet deterministic distribution and remap assertions.
8. Snapshot lease and both balancers meet zero-allocation gates.
9. Manager, registry, reaper, and balancer core pass the race detector.
10. Mandatory development-environment allocation, relative-scale, full-envelope, and 20-swap gates pass.
11. Fast unit/integration tests, `go vet ./...`, `go test ./... -count=1`, and `go build ./cmd/...` pass.
12. Repository formatting verification passes after the line-ending preflight.
13. The runbook and Phase 3A evidence-status document distinguish implemented behavior, provisional evidence, and deferred canonical evidence.

The stricter status `Phase 3A accepted` additionally requires the absolute reference-Linux time and memory gates in Section 16.5. If that environment is unavailable after implementation, the only permitted transitional status is `implementation complete; canonical resource evidence pending`. Phase 3B design may start from that transitional checkpoint, but production certification and Phase 3D integrated acceptance may not treat it as a pass.

An omitted long-running or reference-environment command is never reported as passed.

## 18. Repository and delivery slices

Phase 3A extends:

```text
internal/model       canonical upstream and balancer configuration
internal/config      v1alpha3 wire decoder and backward adapters
internal/upstream    registry, plans, transports, endpoints, WRR, chash, reaper
internal/runtime     snapshot lease and candidate-plan integration
internal/requestctx  selected endpoint and balancer metadata
internal/proxy       selection and endpoint-aware proxying
internal/telemetry   bounded registry/balancer metrics
test/integration     dynamic update and connection-reuse behavior
```

Implementation proceeds in these vertical slices:

1. add repository `.gitattributes`, enforce LF for Go sources, and restore the full `gofmt -l .` gate;
2. add v1alpha3 canonical model, decoder, validation, and compatibility;
3. implement WRR and Ketama kernels test-first;
4. implement shared transport/endpoint registry and candidate transaction;
5. implement snapshot lease, retirement, and reaper;
6. integrate builder, gateway, request context, and proxy;
7. integrate telemetry, failure semantics, and shutdown;
8. add concurrency, scale, resource, benchmark, runbook, and evidence documentation.

The line-ending change is a verification prerequisite recorded by Phase 2, not a gateway feature. It should remain a mechanically isolated commit.

## 19. Handoff to later Phase 3 sub-phases

Phase 3B may add:

- mutable health state to `EndpointRuntime`;
- active HTTP/TCP check workers;
- passive failure accounting;
- eligible-endpoint traversal for WRR and consistent hash;
- replay-safe request bodies;
- attempt, retry-budget, and total-deadline state.

It must not rebuild snapshots on health transition or move health state into immutable plans.

Phase 3C may extend:

- transport profile identity with TLS, client identity, roots, SNI, and protocol;
- downstream listener certificate selection;
- WebSocket request/lease/drain behavior.

It must preserve transport isolation across security profiles.

Phase 3D may use selected endpoint and attempt metadata already stored in the typed request context to add bounded access logging and integrated resilience benchmarks.

No later sub-phase may erase the deferred Phase 2 Task 16 evidence debt or represent provisional developer-machine results as APISIX parity.

## 20. Related decisions and evidence

- [Go-native gateway north-star design](2026-07-21-go-native-api-gateway-design.md)
- [Phased delivery roadmap](2026-07-21-go-native-api-gateway-phase-roadmap-design.md)
- [Phase 2 runtime snapshot and router kernel design](2026-07-23-phase-2-runtime-snapshot-router-kernel-design.md)
- [Phase 2 deferred benchmark and Phase 3 handoff](2026-07-26-phase-2-deferred-benchmark-handoff-design.md)
- [Phase 2 current evidence status](../../benchmarks/phase-2-current-status.md)
- [Phase 3A current evidence status](../../benchmarks/phase-3a-current-status.md)
- [Phase 3A operational runbook](../../operations/phase-3a-runbook.md)
- [APISIX architecture and source analysis](../../architecture/apache-api-six-architecture-design.md)

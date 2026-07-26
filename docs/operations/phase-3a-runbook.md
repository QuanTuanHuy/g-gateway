# Phase 3A operational runbook

## Scope and safety boundary

Phase 3A is a standalone Go data plane with immutable route/plugin/upstream plans and a lifecycle-aware upstream runtime registry. It adds strict `gateway/v1alpha3`, weighted multi-endpoint upstreams, weighted round-robin, consistent hash, transport reuse, snapshot leases, retired-plan reaping, and bounded lifecycle telemetry. `gateway/v1alpha1` and `gateway/v1alpha2` remain supported.

Phase 3A intentionally has no public reload endpoint, Admin API, file watcher, control plane, health checking, retry, circuit breaker, HTTPS/mTLS upstream, upstream HTTP/2 or gRPC, dynamic downstream SNI certificates, WebSocket/CONNECT, or access-log exporter. Keep the admin listener private.

## Build and start

Go 1.26.5 is the canonical toolchain:

```powershell
go build ./cmd/...
go run ./cmd/gateway-dp -config configs/phase3a.yaml
```

The checked-in configuration expects:

- TLS files at `/certs/server.crt` and `/certs/server.key`;
- upstream origins `http://upstream-a:8080` and `http://upstream-b:8080`;
- traffic listeners on `:8080` and `:8443`;
- a private admin listener on `:9090`.

Copy the file and change certificate/origin paths for direct host execution. Startup strictly validates and canonicalizes the complete resource set, prepares registry resources, builds revision 1 off-path, and publishes it before readiness becomes true.

## Configuration and selection semantics

An endpoint identity is the configured upstream ID plus canonical HTTP origin. DNS names are lowercase with one trailing dot removed, omitted HTTP ports become `80`, and a path may only be empty or `/`. User information, query, fragment, HTTPS, invalid ports, and duplicate canonical endpoints are rejected.

Endpoint weight is `0..1000`. Weight `0` keeps the endpoint runtime owned by the registry but excludes it from selection. Every upstream needs at least one positive weight.

`weighted_round_robin` compiles a deterministic schedule capped at 8,192 slots per upstream. `consistent_hash` compiles a bounded xxHash64 continuum. Supported hash sources are `header`, `cookie`, `remote_addr`, and `literal`. Missing dynamic input falls back to the normalized immediate peer address according to the compound-key rules in the [Phase 3A design](../superpowers/specs/2026-07-26-phase-3a-upstream-runtime-balancing-kernel-design.md).

Request plugins execute before endpoint selection, so an intentional header rewrite can change a consistent-hash key. Phase 3A selects once and performs exactly one attempt; it never retries another endpoint.

## Internal revision updates

`Gateway.Apply(revision, ResourceSet)` is an internal composition API. It:

1. rejects shutdown and stale revisions;
2. prepares a transactional upstream candidate;
3. builds the complete immutable snapshot;
4. rolls back the candidate on any failure;
5. commits and atomically publishes the new revision;
6. retires the old plan-set after its final request lease.

Weight-only changes reuse unchanged endpoint and transport runtimes. Updating one upstream preserves unrelated runtime and keepalive pools. A rejected candidate leaves the active revision and live registry counts unchanged.

There is no external caller for `Apply` in Phase 3A. Process startup remains the only supported operator configuration entry point. Do not build reload automation around this internal method before the Phase 4 control-plane contract exists.

## Stable failures

Configuration and reconcile errors include:

- `UPSTREAM_ENDPOINTS_EMPTY`
- `UPSTREAM_ENDPOINT_DUPLICATE`
- `UPSTREAM_NO_ACTIVE_ENDPOINT`
- `UPSTREAM_ENDPOINT_LIMIT`
- `UPSTREAM_WEIGHT_INVALID`
- `BALANCER_TYPE_INVALID`
- `HASH_KEY_INVALID`
- `BALANCER_BUDGET_EXCEEDED`
- `TRANSPORT_PROFILE_INVALID`
- `RETIRED_SNAPSHOT_LIMIT`

Request failures remain stable JSON:

- `503 UPSTREAM_UNAVAILABLE` for an internal missing/selectable-plan invariant;
- `502 UPSTREAM_CONNECTION_FAILED` for DNS, dial, reset, or connection failure;
- `504 UPSTREAM_TIMEOUT` for dial or response-header timeout;
- client cancellation does not receive a replacement gateway error response.

Phase 3A does not retry or change health state after any request failure.

## Health, readiness, metrics, and logs

```powershell
curl.exe --fail http://127.0.0.1:9090/healthz
curl.exe --fail http://127.0.0.1:9090/readyz
curl.exe --fail http://127.0.0.1:9090/metrics
```

Phase 3A adds:

- `gateway_upstream_live_endpoints`
- `gateway_upstream_live_transports`
- `gateway_upstream_live_selection_states`
- `gateway_runtime_retired_snapshots`
- `gateway_upstream_registry_resources_total{action,kind}`
- `gateway_upstream_registry_rollbacks_total`
- `gateway_upstream_transport_cleanup_total`
- `gateway_upstream_balancer_selections_total{upstream_id,algorithm}`
- `gateway_upstream_hash_fallback_total{upstream_id}`

Request/balancer series are emitted only when request metrics are enabled and observed. The only request-selection identity label is a configured `upstream_id`. Never add raw URL, hostname, endpoint identity, client address, header/cookie value, or hash value as a metric label.

Structured lifecycle logs cover applied/rejected snapshots, prepared/rolled-back/cleaned registry state, bounded registry error codes, and shutdown cleanup. They exclude endpoint URLs, hostnames, hash keys, and client addresses. Per-request access logging belongs to Phase 3D.

## Pool reuse and retired-plan backpressure

Transports are shared by their complete Phase 3A transport profile. Go's transport still partitions pooled connections by origin. A weight or membership change with an unchanged transport profile reuses its transport; an unrelated upstream update cannot close another upstream's pool.

`runtime.max_retired_snapshots` defaults to `64` and accepts `1..1024`. If long requests retain that many old revisions, the next apply fails with `RETIRED_SNAPSHOT_LIMIT`; active and in-flight traffic continue. Do not terminate streams to free this budget. Investigate long-lived requests and update cadence.

## Graceful shutdown

Allow at least `server.shutdown_timeout` plus a small process-stop margin:

1. readiness becomes false and new applies are rejected;
2. HTTP/HTTPS listeners stop accepting work;
3. active handlers drain and release their snapshot leases;
4. an expired drain force-closes traffic connections and propagates cancellation;
5. manager ownership is retired;
6. the reaper and final registry cleanup release resources;
7. idle pools with no owner close exactly once;
8. the admin server stops.

A process kill outside this sequence can interrupt streams and bypass cleanup.

## Fast verification

```powershell
gofmt -l .
go vet ./...
go test -p 1 ./... -count=1
go build ./cmd/...

go test -p 1 ./internal/upstream ./internal/runtime `
  -run 'TestPhase2Acceptance|TestPhase3AAcceptance' -count=1 -v

go test -p 1 ./internal/upstream ./internal/runtime -run '^$' `
  -bench 'BenchmarkSnapshotAcquireRelease|BenchmarkWRRSelect|BenchmarkConsistentHashSelect|BenchmarkRegistryReconcile' `
  -benchmem -count=3
```

`-p 1` is recommended on resource-constrained Windows hosts. It does not change test semantics.

## Extended verification

Run the full resource profile only on an appropriate dedicated machine:

```powershell
$env:GATEWAY_PHASE3A_ACCEPTANCE='1'
go test ./internal/upstream -run TestPhase3AAcceptance -count=1 -v
Remove-Item Env:GATEWAY_PHASE3A_ACCEPTANCE
```

Race verification requires a CGO-capable toolchain. The canonical portable option is the pinned Linux container:

```powershell
docker run --rm -v "${PWD}:/src" -w /src golang:1.26.5-bookworm `
  go test -race ./internal/upstream ./internal/runtime ./internal/proxy ./internal/gateway ./test/integration -count=1
```

Fuzz targets:

```powershell
go test ./internal/upstream -run=^$ -fuzz=FuzzNormalizeEndpoint -fuzztime=5m
go test ./internal/upstream -run=^$ -fuzz=FuzzHashKey -fuzztime=5m
```

Commands not actually executed must remain pending in the [Phase 3A evidence ledger](../benchmarks/phase-3a-current-status.md).

## Failure response

- Rejected startup/apply: fix the stable field-path error; never loop on a known-invalid candidate.
- `RETIRED_SNAPSHOT_LIMIT`: inspect long-lived requests and update frequency; do not force-retire live leases.
- Connection/timeout response: inspect the selected origin and transport profile. Phase 3A has no health or retry fallback.
- Unexpected growth in live/retired gauges: preserve lifecycle logs and registry stats, then reproduce with the focused registry/reaper tests.
- Race/fuzz/acceptance failure: preserve the seed, minimized corpus, environment, and raw output before changing code.
- Benchmark gap: do not infer APISIX parity from selector or registry microbenchmarks.

## Phase 3B/3C/3D handoff

Phase 3B receives immutable plans, endpoint identity, shared transports, request leases, and registry lifecycle. It may add mutable health and retry state to endpoint runtimes, but must not put health into snapshots or rebuild snapshots on a health transition.

Phase 3C owns upstream TLS/mTLS and protocol expansion, dynamic downstream SNI certificates, and WebSocket correctness. Every security/protocol field that changes pool safety must extend transport identity.

Phase 3D owns bounded access logging, integrated resilience acceptance, and canonical APISIX comparison. The [deferred Phase 2 Task 16 debt](../benchmarks/phase-2-current-status.md#deferred-task-16) remains visible and mandatory before production certification.

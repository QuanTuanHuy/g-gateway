# Phase 2 operational runbook

## Scope and safety boundary

Phase 2 is a standalone Go data plane with immutable runtime snapshots and a compiled router. It supports strict `gateway/v1alpha1` compatibility, strict `gateway/v1alpha2`, multiple routes and services, and a fixed set of upstream runtimes created during startup.

The router supports exact, one-label wildcard, and hostless hosts; exact, prefix, parameter, and catch-all paths; method constraints; and compiled header/query predicates. The built-in plugin set contains request-id and request/response header-rewrite.

Phase 2 intentionally has no public configuration update endpoint, mutable upstream membership, health checks, load balancing, retries, regex routing, control plane, authentication, rate limiting, WebSocket, or CONNECT. Keep the admin listener private.

## Build and start

Go 1.26.5 is the canonical toolchain:

```powershell
go build ./cmd/...
```

The Phase 1 configuration remains a supported `gateway/v1alpha1` compatibility input:

```powershell
go run ./cmd/gateway-dp -config configs/phase1.yaml
```

Use `configs/phase2.yaml` for the current `gateway/v1alpha2` example:

```powershell
go run ./cmd/test-upstream -listen :8081
go run ./cmd/gateway-dp -config configs/phase2.yaml
```

The checked-in Phase 2 config uses the container-network endpoint `http://upstream:8080`. When running both commands directly on the host, copy the config and change that endpoint to `http://127.0.0.1:8081`. Its TLS listener also expects `/certs/server.crt` and `/certs/server.key`; provide those files or disable the HTTPS listener in the local copy.

Before any traffic listener binds, the gateway strictly decodes and validates the complete config, resolves route/service/upstream references, compiles plugins and router indexes, creates the fixed upstream runtimes, and publishes revision 1. Unknown fields, invalid matches, duplicate IDs, unresolved references, invalid plugin config, or unreadable TLS material cause a non-zero startup exit.

## Configuration revisions

`Gateway.Apply(revision, ResourceSet)` is an internal composition API. It:

1. validates and builds the complete candidate off the traffic path;
2. rejects non-monotonic revisions;
3. atomically publishes the new immutable snapshot;
4. notifies snapshot observers in revision order.

A request retains one snapshot for its complete lifecycle, so an activation cannot expose a partial revision. Old snapshots become collectible after requests holding them finish.

Phase 2 exposes no Admin API, file watcher, signal reload, etcd watch, or other external caller for `Apply`. Process startup remains the only supported operator configuration entry point. Do not build operational automation around the internal method before the Phase 4 control-plane contract exists.

## Health, readiness, and telemetry

```powershell
curl.exe --fail http://127.0.0.1:9090/healthz
curl.exe --fail http://127.0.0.1:9090/readyz
curl.exe --fail http://127.0.0.1:9090/metrics
curl.exe --fail -H "Host: api.example.com" http://127.0.0.1:8080/health
```

- Health means the process and admin listener are alive.
- Readiness becomes true only after the initial snapshot and traffic listeners are active.
- Request metrics use bounded route IDs from the active config plus `__unmatched__`; they do not derive labels from arbitrary request paths or hosts.
- Snapshot observer metrics report activation outcome without moving build work into the request path.
- Startup, listener, revision, and shutdown events are structured JSON logs.
- Profiling is opt-in and belongs only on the private admin listener.

## Graceful shutdown

Allow at least the configured `server.shutdown_timeout` plus a small container-stop margin:

1. readiness becomes false;
2. traffic listeners stop accepting new work;
3. active requests drain while retaining their snapshot;
4. traffic/admin servers close;
5. all fixed upstream runtimes close idle connections;
6. the process exits.

A forced kill can interrupt streaming responses and bypass idle-connection cleanup.

## Fast verification

These checks are suitable for normal development:

```powershell
gofmt -l .
go vet ./...
go test ./... -count=1
go build ./cmd/...
go test ./internal/router -run TestCompiledRouterMatchesReference -count=1 -v
go test ./internal/runtime -run TestPhase2Acceptance -count=1 -v
```

`TestPhase2Acceptance` uses the normal 10,000-route profile unless `GATEWAY_PHASE2_ACCEPTANCE=1` is set. The reference/property test uses a fixed seed and compares the compiled router with the independent matcher oracle.

## Extended correctness and resource verification

The following commands are part of Phase 2 acceptance but were not rerun for the 2026-07-26 documentation checkpoint:

```powershell
docker run --rm -v "${PWD}:/src" -w /src golang:1.26.5-bookworm go test ./... -race -count=1

go test ./internal/router -run=^$ -fuzz=FuzzPathPattern -fuzztime=5m
go test ./internal/router -run=^$ -fuzz=FuzzQueryEvaluation -fuzztime=5m
go test ./internal/router -run=^$ -fuzz=FuzzHostNormalization -fuzztime=5m
go test ./internal/router -run=^$ -fuzz=FuzzPredicateCompile -fuzztime=5m
go test ./internal/router -run=^$ -fuzz=FuzzRouterCompileAndMatch -fuzztime=5m

$env:GATEWAY_PHASE2_ACCEPTANCE='1'
go test ./internal/runtime -run TestPhase2Acceptance -count=1 -v
go test ./internal/router -run=^$ -bench BenchmarkRouterScale -benchmem -count=5
Remove-Item Env:GATEWAY_PHASE2_ACCEPTANCE
```

Absolute compile/memory acceptance uses a Linux x86_64 container with 8 dedicated logical CPUs, a 16 GiB memory limit, Go 1.26.5, swap disabled, and no competing workload. Docker Desktop results are provisional even when the same limits are applied.

Generate the deterministic standard dataset independently of the missing end-to-end harness:

```powershell
go run ./cmd/bench-dataset `
  -routes 100000 `
  -seed 20260723 `
  -gateway-out bench/generated/phase2/gateway-100000.yaml `
  -apisix-out bench/generated/phase2/apisix-100000.yaml `
  -metadata-out bench/generated/phase2/dataset-100000.json
```

Generated configs and metadata are benchmark inputs, not performance evidence.

## Deferred end-to-end benchmark

The isolated Phase 2 end-to-end benchmark is deferred in full by the [approved handoff decision](../superpowers/specs/2026-07-26-phase-2-deferred-benchmark-handoff-design.md). Its executable specification remains [Task 16 of the Phase 2 implementation plan](../superpowers/plans/2026-07-23-phase-2-runtime-snapshot-router-kernel.md#task-16-add-the-isolated-phase-2-end-to-end-benchmark-and-report).

The following artifacts do not exist yet:

- `bench/run-phase2.ps1` and its shared PowerShell library;
- the isolated Phase 2 Compose/APISIX/scenario profiles;
- raw-run and summary schemas;
- the Phase 2 report package and command;
- semantic preflight results against pinned APISIX;
- Phase 2 smoke and five-repetition canonical results;
- end-to-end throughput, p99, sentinel-spread, error, CPU, and memory verdicts.

Do not run or document `run-phase2.ps1` as available until Task 16 implements it. Router microbenchmarks and compile/memory acceptance cannot substitute for the missing end-to-end relative gates.

## Failure response

- Configuration/startup failure: use the stable field/path error, correct the entire candidate, and restart. A rejected config never becomes active.
- Snapshot apply failure in an internal test/embedding: retain the current revision, record the stable build error, and do not retry a known-invalid candidate in a loop.
- Route miss: verify normalized Host, path precedence, method, and predicates before changing priority.
- Upstream connect/timeout failure: inspect the fixed endpoint. Phase 2 does not retry or mark endpoint health.
- Readiness loss: inspect listener and lifecycle logs; do not route traffic based only on `/healthz`.
- Race/fuzz/acceptance failure: preserve the seed or minimized corpus, fix the owning module, then rerun both the focused target and the relevant full gate.
- Benchmark gap: preserve raw evidence and environment metadata. Do not infer APISIX parity or change the HTTP engine from provisional data.

## Phase 3 boundary

Phase 3 may replace the fixed upstream table with a lifecycle-aware registry and add balancing, health, timeout, retry, TLS, WebSocket, and bounded access-log behavior.

It must preserve canonical resource IDs, immutable request snapshots, router precedence, compiled plugin/request-context contracts, and the separation between immutable configuration and mutable upstream runtime state. The deferred Phase 2 benchmark debt remains visible and mandatory before production certification.


# G-Gateway

G-Gateway is a Go data-plane experiment that targets APISIX-class gateway semantics and performance through incremental, evidence-driven phases. Phase 3A is implemented: immutable runtime snapshots and the compiled router/plugin pipeline now use registry-backed multi-endpoint upstream plans, shared transports, weighted round-robin, and consistent hash. Current checkpoint: `implementation complete; canonical resource evidence pending`. This is not yet a general-purpose or production-certified API gateway.

The accepted architecture and phased roadmap are documented in [`docs/architecture/apache-api-six-architecture-design.md`](docs/architecture/apache-api-six-architecture-design.md). The [Phase 3A design](docs/superpowers/specs/2026-07-26-phase-3a-upstream-runtime-balancing-kernel-design.md), [current evidence status](docs/benchmarks/phase-3a-current-status.md), and [operational runbook](docs/operations/phase-3a-runbook.md) define the current checkpoint. The [deferred Phase 2 Task 16 evidence](docs/benchmarks/phase-2-current-status.md#deferred-task-16) remains mandatory before production certification.

## Current capabilities

- Strict `gateway/v1alpha3` resources plus `gateway/v1alpha1` and `gateway/v1alpha2` compatibility.
- Immutable versioned runtime snapshots built off-path and activated atomically.
- Multiple routes and services resolved to immutable registry-backed upstream plans.
- Dynamic add/remove/update through internal `Gateway.Apply`, with transactional rollback and last-known-good behavior.
- Weighted endpoints, deterministic weighted round-robin, and bounded xxHash64 consistent hash.
- Canonical endpoint identity, weight-zero disablement, and shared transport profiles that preserve unrelated keepalive pools.
- Per-request snapshot leases, bounded retired generations, asynchronous reaping, and final registry cleanup.
- Compiled exact/wildcard/hostless host routing and exact/prefix/parameter/catch-all path routing.
- Compiled method, header, and query predicates with deterministic precedence.
- Typed request context and compiled request-id/header-rewrite plugins.
- HTTP/1.1 cleartext downstream and HTTP/1.1 or HTTP/2 over TLS downstream.
- Explicit shared HTTP/1.1 upstream transports with connection pooling and bounded dial/response-header timeouts.
- Streaming request/response bodies, cancellation propagation, trailers, forwarding-header rebuilding, and hop-by-hop header removal.
- Stable JSON errors for route, method, body-size, timeout, connection, upgrade, and panic failures.
- Separate admin listener with health, readiness, bounded runtime/upstream Prometheus metrics, and opt-in pprof.
- Graceful SIGINT/SIGTERM drain with readiness removed before traffic shutdown, request leases drained, and unowned pools closed.

Current exclusions include a public configuration update surface, health checks, retries, circuit breaking, HTTPS/mTLS or HTTP/2 upstreams, dynamic downstream SNI certificates, regex routing, authentication/rate limiting, WebSocket/CONNECT, access logging, and distributed control-plane behavior. Phase 3B owns health/retry, Phase 3C owns TLS/protocol/WebSocket, and Phase 3D owns bounded access logging and integrated APISIX comparison.

## Repository layout

```text
cmd/gateway-dp       production data-plane composition root
cmd/test-upstream    deterministic correctness upstream
cmd/bench-report     black-box deterministic benchmark summarizer
cmd/bench-dataset    deterministic Phase 2 dataset generator
bench                isolated APISIX/Go comparison harness and schemas
configs              versioned example configuration
internal/benchreport raw-evidence parser, aggregation, and verdicts
internal/benchdataset deterministic 1/100,000-route fixtures and renderers
internal/config      strict bootstrap/resource decoding and validation
internal/model       canonical route and upstream resources
internal/plugin      compiled plugin contracts and built-ins
internal/proxy       routing and reverse-proxy semantics
internal/requestctx  typed request-scoped route/plugin state
internal/router      deterministic compiled router
internal/runtime     immutable snapshot builder, atomic manager, and request leases
internal/telemetry   health, readiness, metrics, and profiling
internal/upstream    plans, balancers, shared transports, registry, and reaper
internal/gateway     listeners and graceful lifecycle
internal/testupstream deterministic protocol test endpoints
test/integration     black-box protocol and process tests
```

Commands remain thin so later phases can extend domain/runtime packages without moving behavior into process wiring.

## Run locally

Go 1.26.5 is the canonical toolchain. Provide a TLS certificate and key matching the paths in the configuration, then point the upstream endpoint at a reachable service:

```bash
go run ./cmd/test-upstream -listen :8081
go run ./cmd/gateway-dp -config configs/phase3a.yaml
```

The checked-in Phase 3A example expects `/certs/server.crt`, `/certs/server.key`, and container-network endpoints `http://upstream-a:8080` and `http://upstream-b:8080`; adapt them for direct host execution. `configs/phase1.yaml` and `configs/phase2.yaml` remain compatibility examples. Traffic listeners default to `:8080` and `:8443`; the private admin listener defaults to `:9090`.

```bash
curl http://localhost:9090/healthz
curl http://localhost:9090/readyz
curl http://localhost:9090/metrics
```

Keep the admin listener on a private network. Profiling is disabled by default and, when enabled, is exposed only beneath `/debug/pprof/` on that listener.

## Build and verify

The canonical verification runs in the pinned Go container:

```bash
docker run --rm -v "$PWD:/src" -w /src golang:1.26.5-bookworm sh -c \
  'test -z "$(gofmt -l .)" && go vet ./... && go test ./... -race -count=1 && go build ./cmd/...'
```

Build the runtime, test upstream, Phase 1 report command, and Phase 2 dataset command from the same multi-stage Dockerfile:

```bash
docker build --build-arg COMMAND=gateway-dp -t g-gateway:phase3a .
docker build --build-arg COMMAND=test-upstream -t g-gateway-test-upstream:phase3a .
docker build --build-arg COMMAND=bench-report -t g-gateway-bench-report:phase1 .
docker build --build-arg COMMAND=bench-dataset -t g-gateway-bench-dataset:phase2 .
```

The runtime image is distroless and runs as its predefined non-root user. Mount the gateway config and certificate files read-only when starting `gateway-dp`:

```bash
docker run --rm \
  -p 8080:8080 -p 8443:8443 -p 127.0.0.1:9090:9090 \
  -v "$PWD/configs/phase3a.yaml:/config/gateway.yaml:ro" \
  -v "$PWD/certs:/certs:ro" \
  g-gateway:phase3a -config /config/gateway.yaml
```

Startup, listener, and shutdown events are JSON logs. Invalid configuration, bind failures, unexpected listener termination, or unsuccessful shutdown return a non-zero process exit code.

## Benchmark and operations

The [benchmark guide](bench/README.md) documents the implemented Phase 1 APISIX/Go harness. The [Phase 3A current status](docs/benchmarks/phase-3a-current-status.md) records normal acceptance and allocation/relative-scale evidence while keeping full-envelope, race, fuzz, reference-Linux, and APISIX E2E gates pending.

The [Phase 3A operational runbook](docs/operations/phase-3a-runbook.md) covers startup, internal revision semantics, balancing, pool reuse, backpressure, metrics, shutdown, verification, and the Phase 3B/3C/3D boundary. Developer-machine evidence is deliberately provisional; official parity and production performance certification require the dedicated Linux gates planned for later phases.

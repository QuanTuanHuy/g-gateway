# G-Gateway

G-Gateway is a Go data-plane experiment that targets APISIX-class gateway semantics and performance through incremental, evidence-driven phases. Phase 2 core is implemented: the Phase 1 reverse-proxy baseline now runs behind immutable runtime snapshots, a compiled router, and a minimal compiled plugin pipeline. Current checkpoint: `implementation complete; canonical evidence pending`. This is not yet a general-purpose or production-certified API gateway.

The accepted architecture and phased roadmap are documented in [`docs/architecture/apache-api-six-architecture-design.md`](docs/architecture/apache-api-six-architecture-design.md). The [Phase 2 design](docs/superpowers/specs/2026-07-23-phase-2-runtime-snapshot-router-kernel-design.md), [current evidence status](docs/benchmarks/phase-2-current-status.md), and [deferred-benchmark handoff decision](docs/superpowers/specs/2026-07-26-phase-2-deferred-benchmark-handoff-design.md) define the current checkpoint.

## Current capabilities

- Strict `gateway/v1alpha2` resources plus `gateway/v1alpha1` compatibility.
- Immutable versioned runtime snapshots built off-path and activated atomically.
- Multiple routes and services resolved to a fixed set of pooled upstream runtimes.
- Compiled exact/wildcard/hostless host routing and exact/prefix/parameter/catch-all path routing.
- Compiled method, header, and query predicates with deterministic precedence.
- Typed request context and compiled request-id/header-rewrite plugins.
- HTTP/1.1 cleartext downstream and HTTP/1.1 or HTTP/2 over TLS downstream.
- Explicit HTTP/1.1 upstream transport with connection pooling and bounded timeouts.
- Streaming request/response bodies, cancellation propagation, trailers, forwarding-header rebuilding, and hop-by-hop header removal.
- Stable JSON errors for route, method, body-size, timeout, connection, upgrade, and panic failures.
- Separate admin listener with health, readiness, Prometheus metrics, and opt-in pprof.
- Graceful SIGINT/SIGTERM drain with readiness removed before traffic shutdown and upstream idle connections closed.

Current exclusions include a public configuration update surface, mutable upstream membership, retries, load balancing, health checks, regex routing, authentication/rate limiting, WebSocket/CONNECT, and distributed control-plane behavior. Phase 3 begins upstream resilience design while preserving the Phase 2 snapshot/router contracts.

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
internal/runtime     immutable snapshot builder and atomic manager
internal/telemetry   health, readiness, metrics, and profiling
internal/upstream    pooled outbound transport runtime
internal/gateway     listeners and graceful lifecycle
internal/testupstream deterministic protocol test endpoints
test/integration     black-box protocol and process tests
```

Commands remain thin so later phases can extend domain/runtime packages without moving behavior into process wiring.

## Run locally

Go 1.26.5 is the canonical toolchain. Provide a TLS certificate and key matching the paths in the configuration, then point the upstream endpoint at a reachable service:

```bash
go run ./cmd/test-upstream -listen :8081
go run ./cmd/gateway-dp -config configs/phase2.yaml
```

The checked-in Phase 2 example expects `/certs/server.crt`, `/certs/server.key`, and the container-network endpoint `http://upstream:8080`; adapt those three values for direct host execution. `configs/phase1.yaml` remains a supported v1alpha1 compatibility example. Traffic listeners default to `:8080` and `:8443`; the private admin listener defaults to `:9090`.

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
docker build --build-arg COMMAND=gateway-dp -t g-gateway:phase2 .
docker build --build-arg COMMAND=test-upstream -t g-gateway-test-upstream:phase2 .
docker build --build-arg COMMAND=bench-report -t g-gateway-bench-report:phase1 .
docker build --build-arg COMMAND=bench-dataset -t g-gateway-bench-dataset:phase2 .
```

The runtime image is distroless and runs as its predefined non-root user. Mount the gateway config and certificate files read-only when starting `gateway-dp`:

```bash
docker run --rm \
  -p 8080:8080 -p 8443:8443 -p 127.0.0.1:9090:9090 \
  -v "$PWD/configs/phase2.yaml:/config/gateway.yaml:ro" \
  -v "$PWD/certs:/certs:ro" \
  g-gateway:phase2 -config /config/gateway.yaml
```

Startup, listener, and shutdown events are JSON logs. Invalid configuration, bind failures, unexpected listener termination, or unsuccessful shutdown return a non-zero process exit code.

## Benchmark and operations

The [benchmark guide](bench/README.md) documents the implemented Phase 1 APISIX/Go harness. The isolated Phase 2 end-to-end harness is deferred in full; the [Phase 2 current status](docs/benchmarks/phase-2-current-status.md) lists the exact missing evidence without treating microbenchmarks as a substitute.

The [Phase 2 operational runbook](docs/operations/phase-2-runbook.md) covers startup, internal revision semantics, fast and extended verification, deterministic dataset generation, failure response, and the Phase 3 boundary. Docker Desktop evidence is deliberately provisional; official parity and production performance certification require the dedicated Linux gates planned for Phase 7.

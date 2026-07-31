# G-Gateway

G-Gateway is a Go data-plane experiment that targets APISIX-class gateway semantics and performance through incremental, evidence-driven phases. Phase 3C1 implementation is complete with canonical protocol evidence pending: registry-backed upstream plans now add verified outbound TLS/mTLS, HTTP/2 over TLS, h2c, native gRPC pass-through, and transport-generation rotation while preserving Phase 3B resilience behavior. This is not yet a general-purpose or production-certified API gateway.

The accepted architecture and phased roadmap are documented in [`docs/architecture/apache-api-six-architecture-design.md`](docs/architecture/apache-api-six-architecture-design.md). The [Phase 3C1 design](docs/superpowers/specs/2026-07-30-phase-3c1-upstream-tls-protocol-design.md), [current evidence status](docs/benchmarks/phase-3c1-current-status.md), and [operational runbook](docs/operations/phase-3c1-runbook.md) define the current checkpoint. The [deferred Phase 2 Task 16 evidence](docs/benchmarks/phase-2-current-status.md#deferred-task-16) remains mandatory before production certification.

## Current capabilities

- Strict `gateway/v1alpha5` TLS/protocol resources plus `gateway/v1alpha1`–`gateway/v1alpha4` compatibility.
- Immutable versioned runtime snapshots built off-path and activated atomically.
- Multiple routes and services resolved to immutable registry-backed upstream plans.
- Dynamic add/remove/update through internal `Gateway.Apply`, with transactional rollback and last-known-good behavior.
- Weighted endpoints, deterministic weighted round-robin, and bounded xxHash64 consistent hash.
- Canonical endpoint identity, weight-zero disablement, and shared transport profiles that preserve unrelated keepalive pools.
- Per-request snapshot leases, bounded retired generations, asynchronous reaping, and final registry cleanup.
- Lazy bounded HTTP/TCP active health, optional passive health, fail-closed all-unhealthy behavior, and policy-fingerprint state reuse.
- Replay-safe retries across distinct endpoints, adaptive fixed-point retry budgets, and post-plugin total request deadlines.
- Compiled exact/wildcard/hostless host routing and exact/prefix/parameter/catch-all path routing.
- Compiled method, header, and query predicates with deterministic precedence.
- Typed request context and compiled request-id/header-rewrite plugins.
- HTTP/1.1 cleartext downstream and HTTP/1.1 or HTTP/2 over TLS downstream.
- Shared HTTP/1.1, HTTP/2-over-TLS, and h2c upstream transports with connection pooling and bounded dial/response-header timeouts.
- Verified outbound TLS with system or replacement trust, optional mTLS client identity, fixed or endpoint-derived SNI, and a TLS 1.2 floor.
- Native gRPC unary and streaming pass-through with metadata, trailers, status, and cancellation propagation.
- Atomic TLS/protocol transport-generation rotation, separate production/probe pools, typed redacted failures, and bounded lifecycle telemetry.
- Streaming request/response bodies, cancellation propagation, trailers, forwarding-header rebuilding, and hop-by-hop header removal.
- Stable JSON errors for route, method, body-size, timeout, connection, upgrade, and panic failures.
- Separate admin listener with health, readiness, bounded runtime/upstream Prometheus metrics, and opt-in pprof.
- Graceful SIGINT/SIGTERM drain with readiness removed before traffic shutdown, request leases drained, and unowned pools closed.

Current exclusions include a public configuration update surface, circuit breaking, dynamic downstream SNI certificates, regex routing, authentication/rate limiting, WebSocket/CONNECT, access logging, and distributed control-plane behavior. Phase 3C2 owns generic immutable `Certificate` resources bound to downstream exact/wildcard SNI, Phase 3C3 owns HTTP listener/runtime foundations for WebSocket lifecycle, and Phase 3D owns bounded access logging plus integrated APISIX comparison.

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
internal/tlsmaterial immutable bounded certificate/trust parsing and fingerprints
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
go run ./cmd/gateway-dp -config configs/phase3c1.yaml
```

The checked-in Phase 3C1 example expects downstream files beneath `/certs`, outbound trust/client material beneath `/secrets`, and container-network upstream names; adapt them for direct host execution. Earlier files remain compatibility examples. Traffic listeners default to `:8080` and `:8443`; the private admin listener defaults to `:9090`.

```bash
curl http://localhost:9090/healthz
curl http://localhost:9090/readyz
curl http://localhost:9090/metrics
```

Keep the admin listener on a private network. Profiling is disabled by default and, when enabled, is exposed only beneath `/debug/pprof/` on that listener.

## Build and verify

Install the pinned documentation analyzers:

```bash
go install honnef.co/go/tools/cmd/staticcheck@2026.1
go install github.com/mgechev/revive@v1.15.0
```

The canonical verification pipeline is:

```bash
test -z "$(gofmt -l .)" &&
staticcheck -tests=false ./... &&
revive -set_exit_status -config revive.toml -formatter default ./... &&
go vet ./... &&
go test ./... -race -count=1 &&
go build ./cmd/...
```

The root `staticcheck.conf` intentionally enables only package-comment and
existing-comment form checks. The root `revive.toml` enables only the missing
exported-declaration check. Together they enforce Go documentation presence and
form without enabling unrelated style policy.

Run the same pipeline in the pinned Go container with the analyzers installed
inside the disposable environment:

```bash
docker run --rm -v "$PWD:/src" -w /src golang:1.26.5-bookworm sh -c \
  'go install honnef.co/go/tools/cmd/staticcheck@2026.1 &&
   go install github.com/mgechev/revive@v1.15.0 &&
   test -z "$(gofmt -l .)" &&
   staticcheck -tests=false ./... &&
   revive -set_exit_status -config revive.toml -formatter default ./... &&
   go vet ./... &&
   go test ./... -race -count=1 &&
   go build ./cmd/...'
```

Build the runtime, test upstream, Phase 1 report command, and Phase 2 dataset command from the same multi-stage Dockerfile:

```bash
docker build --build-arg COMMAND=gateway-dp -t g-gateway:phase3c1 .
docker build --build-arg COMMAND=test-upstream -t g-gateway-test-upstream:phase3c1 .
docker build --build-arg COMMAND=bench-report -t g-gateway-bench-report:phase1 .
docker build --build-arg COMMAND=bench-dataset -t g-gateway-bench-dataset:phase2 .
```

The runtime image is distroless and runs as its predefined non-root user. Mount the gateway config and certificate files read-only when starting `gateway-dp`:

```bash
docker run --rm \
  -p 8080:8080 -p 8443:8443 -p 127.0.0.1:9090:9090 \
  -v "$PWD/configs/phase3c1.yaml:/config/gateway.yaml:ro" \
  -v "$PWD/certs:/certs:ro" \
  -v "$PWD/secrets:/secrets:ro" \
  g-gateway:phase3c1 -config /config/gateway.yaml
```

Startup, listener, and shutdown events are JSON logs. Invalid configuration, bind failures, unexpected listener termination, or unsuccessful shutdown return a non-zero process exit code.

## Benchmark and operations

The [benchmark guide](bench/README.md) documents the implemented Phase 1 APISIX/Go harness. The [Phase 3C1 current status](docs/benchmarks/phase-3c1-current-status.md) records normal/full acceptance, fuzz, lifecycle, and local protocol benchmark evidence while keeping race, reference-Linux, and APISIX E2E gates pending.

The [Phase 3C1 operational runbook](docs/operations/phase-3c1-runbook.md) covers protocol selection, trust/mTLS, rotation, health/probe separation, gRPC streaming, telemetry, shutdown, and troubleshooting. Developer-machine evidence is deliberately provisional; official parity and production performance certification require the dedicated Linux gates planned for later phases.

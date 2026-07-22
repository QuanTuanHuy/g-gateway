# G-Gateway

G-Gateway is a Go data-plane experiment that targets APISIX-class gateway semantics and performance through incremental, evidence-driven phases. Phase 1 establishes a deliberately narrow, measurable reverse-proxy baseline; it is not yet a general-purpose API gateway.

The accepted architecture and phased roadmap are documented in [`docs/architecture/apache-api-six-architecture-design.md`](docs/architecture/apache-api-six-architecture-design.md). The executable Phase 1 plan is in [`docs/superpowers/plans/2026-07-22-phase-1-proxy-baseline-benchmark.md`](docs/superpowers/plans/2026-07-22-phase-1-proxy-baseline-benchmark.md).

## Phase 1 capabilities

- One exact-path route and one HTTP upstream, loaded from strict YAML at startup.
- HTTP/1.1 cleartext downstream and HTTP/1.1 or HTTP/2 over TLS downstream.
- Explicit HTTP/1.1 upstream transport with connection pooling and bounded timeouts.
- Streaming request/response bodies, cancellation propagation, trailers, forwarding-header rebuilding, and hop-by-hop header removal.
- Stable JSON errors for route, method, body-size, timeout, connection, upgrade, and panic failures.
- Separate admin listener with health, readiness, Prometheus metrics, and opt-in pprof.
- Graceful SIGINT/SIGTERM drain with readiness removed before traffic shutdown.

Phase 1 intentionally excludes dynamic configuration, multiple routes/upstreams, retries, load balancing, plugins, authentication, WebSocket/CONNECT, and distributed control-plane behavior. Those are later phases built around the existing model, runtime, proxy, telemetry, and composition-root boundaries.

## Repository layout

```text
cmd/gateway-dp       production data-plane composition root
cmd/test-upstream    deterministic correctness upstream
configs              versioned example configuration
internal/config      strict bootstrap/resource decoding and validation
internal/model       canonical route and upstream resources
internal/upstream    pooled outbound transport runtime
internal/proxy       routing and reverse-proxy semantics
internal/telemetry   health, readiness, metrics, and profiling
internal/gateway     listeners and graceful lifecycle
internal/testupstream deterministic protocol test endpoints
test/integration     black-box protocol and process tests
```

Commands remain thin so later phases can extend domain/runtime packages without moving behavior into process wiring.

## Run locally

Go 1.26.5 is the canonical toolchain. Provide a TLS certificate and key matching the paths in the configuration, then point the upstream endpoint at a reachable service:

```bash
go run ./cmd/test-upstream -listen :8081
go run ./cmd/gateway-dp -config configs/phase1.yaml
```

The checked-in example expects `/certs/server.crt`, `/certs/server.key`, and `http://upstream:8080`; adapt those three values for local execution. Traffic listeners default to `:8080` and `:8443`; the private admin listener defaults to `:9090`.

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

Build either executable from the same multi-stage Dockerfile:

```bash
docker build --build-arg COMMAND=gateway-dp -t g-gateway:phase1 .
docker build --build-arg COMMAND=test-upstream -t g-gateway-test-upstream:phase1 .
```

The runtime image is distroless and runs as its predefined non-root user. Mount the gateway config and certificate files read-only when starting `gateway-dp`:

```bash
docker run --rm \
  -p 8080:8080 -p 8443:8443 -p 127.0.0.1:9090:9090 \
  -v "$PWD/configs/phase1.yaml:/config/gateway.yaml:ro" \
  -v "$PWD/certs:/certs:ro" \
  g-gateway:phase1 -config /config/gateway.yaml
```

Startup, listener, and shutdown events are JSON logs. Invalid configuration, bind failures, unexpected listener termination, or unsuccessful shutdown return a non-zero process exit code.

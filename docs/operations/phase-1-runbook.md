# Phase 1 operational runbook

## Scope and safety boundary

Phase 1 is a static, single-route/single-upstream data plane. It supports HTTP/1.1 cleartext, HTTP/1.1 TLS, HTTP/2 TLS downstream, and HTTP/1.1 upstream. It intentionally has no dynamic control plane, retry, load balancing, authentication, plugins, WebSocket/CONNECT, or service discovery.

Keep the admin listener private. `/healthz`, `/readyz`, `/metrics`, and opt-in `/debug/pprof/` are operational endpoints, not public traffic endpoints.

## Build and start

Use the pinned build image:

```powershell
docker build --build-arg COMMAND=gateway-dp -t g-gateway:phase1 .
docker build --build-arg COMMAND=test-upstream -t g-gateway-test-upstream:phase1 .
docker build --build-arg COMMAND=bench-report -t g-gateway-bench-report:phase1 .
```

Before binding listeners, the gateway strictly decodes and validates the complete YAML file, resolves the one route to the one upstream, loads TLS material, and constructs the upstream transport. Invalid configuration exits non-zero and never becomes ready.

Start with configuration and keys mounted read-only:

```powershell
docker run --rm --name g-gateway `
  -p 8080:8080 -p 8443:8443 -p 127.0.0.1:9090:9090 `
  -v "${PWD}/configs/phase1.yaml:/config/gateway.yaml:ro" `
  -v "${PWD}/certs:/certs:ro" `
  g-gateway:phase1 -config /config/gateway.yaml
```

Do not place private keys in the image. Protect file permissions and rotate by replacing the mounted files and restarting the static Phase 1 process.

## Health, readiness, and telemetry

```powershell
curl.exe --fail http://127.0.0.1:9090/healthz
curl.exe --fail http://127.0.0.1:9090/readyz
curl.exe --fail http://127.0.0.1:9090/metrics
```

- Health means the process/admin server is alive.
- Readiness becomes true only after traffic listeners start. Shutdown removes readiness before draining traffic.
- Request metrics are optional; disable them for performance comparison.
- JSON startup/listener/shutdown logs go to stdout/stderr. Stable proxy failures are JSON responses with a request ID.

Alert on readiness loss, repeated 502/504 responses, shutdown timeouts, process restarts, and sustained latency/error increases. Phase 1 has no automatic upstream health checks or retries; upstream failure requires operator action.

## Graceful shutdown

Send SIGTERM/SIGINT and allow at least the configured `server.shutdown_timeout` plus a small container-stop margin. The lifecycle is:

1. readiness flips false;
2. new work is rejected/listeners begin shutdown;
3. active requests drain;
4. traffic/admin servers and upstream idle connections close;
5. the process exits.

Use a container stop grace period greater than the gateway shutdown timeout. A forced kill can interrupt streaming responses.

## Benchmark acceptance

The exact benchmark procedure, pins, commands, raw layout, verdict meanings, and cleanup are in [`bench/README.md`](../../bench/README.md). Docker Desktop evidence is explicitly provisional. Official parity and any HTTP-engine ADR require a dedicated Linux rerun in Phase 7.

Keep the full ignored result directory outside Git or attach it to release/PR evidence. Commit only schemas, the redacted example, and human-readable conclusions.

The benchmark's direct control deliberately speaks clear HTTP/1.1 to Nginx, matching the Phase 1 upstream protocol. HTTP/2/TLS is exercised only on the downstream side of each gateway target. h2load stages request logs inside its Linux container during measurement and copies them out afterward; changing it to write each record directly to a Docker Desktop bind mount invalidates generator headroom.

## Diagnostic profiling after a provisional miss

Profiling is a separate rerun and must never replace comparison samples.

1. Identify the worst missed scenario/payload from `summary.json`.
2. Copy the corresponding generated gateway configuration, keep the same route/upstream/limits, and set `telemetry.profiling_enabled: true`. Do not enable per-request metrics.
3. Start only Nginx upstream and the Go target with the same Compose CPU/memory settings. Reuse the pinned generator and exact scenario settings.
4. Warm the target, then drive the same load while collecting a 30-second CPU profile:

   ```powershell
   curl.exe --fail --output cpu.pprof "http://127.0.0.1:19090/debug/pprof/profile?seconds=30"
   ```

5. While load is active, capture timestamped `docker stats --no-stream` samples for the Go target, upstream, and load generator. Repeat the same resource trace for APISIX without profiling so resource comparisons remain visible.
6. Immediately after load, collect the Go heap profile:

   ```powershell
   curl.exe --fail --output heap.pprof "http://127.0.0.1:19090/debug/pprof/heap"
   ```

7. Store CPU/heap profiles, Docker traces, exact commands, source/image IDs, and the original summary together in a diagnostic directory outside the comparison result tree.

Stop all Compose profiles using the cleanup command in `bench/README.md`. Disable profiling again before any measured rerun.

## Failure response

- Configuration/startup: correct the stable field/path error, verify certificate readability and listener availability, then restart. Do not retry a known-invalid file in a loop.
- Upstream connect/close/timeout: inspect the upstream first; Phase 1 returns stable 502/504 errors and does not retry.
- Body too large: confirm `max_request_body_bytes`; raising it increases resource exposure.
- Readiness stuck false: inspect startup logs and listener state; do not route traffic to `/healthz`-only instances.
- Drain timeout: retain logs/request IDs, investigate long streams/cancellation, and increase timeout only with evidence.
- Benchmark invalid: preserve raw files and fix the harness/environment; do not interpret parity numbers.

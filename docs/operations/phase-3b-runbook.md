# Phase 3B Resilience Runbook

Phase 3B adds lazy active health checks, optional passive health, bounded replay-safe retries, adaptive per-upstream retry budgets, and a total proxy deadline. The runnable example is [`configs/phase3b.yaml`](../../configs/phase3b.yaml).

## Runtime contract

- Endpoints begin `unknown`; `unknown` and `healthy` are selectable.
- `unhealthy` endpoints are excluded. If every endpoint is unhealthy, the gateway returns `503 UPSTREAM_UNHEALTHY`.
- Active checks start only after the first request reaches an upstream. HTTP and TCP checks share one bounded scheduler and worker pool.
- Passive health requires active health. Passive success cannot recover an unhealthy endpoint; active success can.
- `max_attempts` includes the primary request and is limited to 1–5.
- Retries use a different endpoint and require a configured method plus an empty body or `GetBody`.
- Retry credits are per upstream and bounded by burst and inflight limits.
- `total_timeout` begins after request plugins and covers selection, attempts, response headers, and response streaming.

## Operations

```powershell
go run ./cmd/gateway-dp -config configs/phase3b.yaml
curl.exe http://localhost:9090/readyz
curl.exe http://localhost:9090/metrics
```

Keep the admin listener private. Resilience metric labels never include endpoint URLs, client addresses, route IDs, or raw errors.

Route-only and positive weight-only changes retain transport pools and compatible health/budget runtimes. A health-policy fingerprint change creates a new `unknown` tracker without replacing the transport. Invalid candidates roll back completely.

Shutdown removes readiness, stops new probe activation, drains traffic/retry permits, then closes probes, plans, transports, and registry runtimes.

## Verification

```powershell
go test ./internal/upstream ./internal/runtime ./internal/proxy ./internal/gateway -count=1
go test ./test/integration -count=1
go test ./internal/upstream -run TestPhase3BAcceptance -count=1 -v
go test ./internal/upstream -run '^$' -bench 'BenchmarkHealthAware|BenchmarkRetryBudget' -benchmem -count=5
```

Set `GATEWAY_PHASE3B_ACCEPTANCE=1` only for the 10,000-upstream/100,000-endpoint profile. Canonical acceptance and race evidence require the reference Linux/CGO environment.

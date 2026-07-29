# Phase 3B Current Status

Status: `implementation complete; canonical resilience evidence pending`

Observed locally on 2026-07-29:

- Host: Windows/amd64, Intel Core i7-12700H, Go 1.26.5.
- Seed: `20260727`.
- Formatting, `go vet ./...`, `go build ./cmd/...`, and `go test -p 1 ./... -count=1`: PASS. The final sequential suite completed in 117.7 seconds.
- Normal acceptance: 1,000 upstreams x 10 endpoints with two swaps; PASS in 0.04 seconds. No probe was scheduled before activation and resilience runtimes returned to zero after rollback.
- Full local acceptance: 10,000 upstreams x 10 endpoints with 20 swaps; PASS in 0.43 seconds.
- Normal healthy-proxy comparison, 200 requests at concurrency 8: baseline 16,448 req/s at 1.570 ms p99; Phase 3B 16,060 req/s at 1.623 ms p99. This short run is informational only.
- Full healthy-proxy comparison, median of five order-alternating rounds of 5,000 requests at concurrency 8: baseline 10,748 req/s at 1.872 ms p99; Phase 3B 10,999 req/s at 1.884 ms p99. The local 95% throughput and 110% p99 gates passed.
- The full proxy comparison also passed three consecutive diagnostic repetitions after disabled passive-health observations were removed from the contended hot path.
- Repeated health/retry/timeout/shutdown/retired/reaper/reconcile suite (`-count=20`): PASS.
- Thirty-second fuzz gates: `FuzzEndpointHealth` PASS with 3,401,121 executions; `FuzzNormalizeEndpoint` PASS with 2,687,309 executions; `FuzzHashKey` PASS with 3,114,046 executions.
- Race command: `go test -race ./internal/upstream ./internal/runtime ./internal/proxy ./internal/gateway ./test/integration -count=1`.
- Race result: pending on this host. Exact error: `go: -race requires cgo; enable cgo by setting CGO_ENABLED=1`. `CGO_ENABLED=0`, and `gcc` is not installed.
- Reference-Linux canonical acceptance: pending.

Five-count local benchmark medians:

| Benchmark | Median | Allocation |
|---|---:|---:|
| Health-aware WRR, all healthy | 12.83 ns/op | 0 B, 0 allocs |
| Health-aware WRR, one unhealthy | 11.98 ns/op | 0 B, 0 allocs |
| Health-aware consistent hash, all healthy | 10.01 ns/op | 0 B, 0 allocs |
| Health-aware consistent hash, one unhealthy | 65.32 ns/op | 112 B, 1 alloc |
| Retry budget credit | 0.653 ns/op | 0 B, 0 allocs |
| Retry budget acquire/release | 24.39 ns/op | 0 B, 0 allocs |
| Attempt transport, one attempt | 82.03 us/op | 48,878 B, 131 allocs |
| Attempt transport, retry then success | 150.84 us/op | 54,748 B, 199 allocs |
| Healthy proxy, Phase 3A baseline | 80.71 us/op | 47,908 B, 123 allocs |
| Healthy proxy, Phase 3B enabled | 84.23 us/op | 48,896 B, 131 allocs |

These are developer-machine measurements, not APISIX parity evidence. Phase 2 deferred Task 16, Phase 3A canonical resource evidence, Phase 3B race verification, and reference-Linux canonical acceptance remain pending. Integrated APISIX comparison belongs to Phase 3D.

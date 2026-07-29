# Phase 3B Current Status

Status: `implementation in progress`

Observed locally on 2026-07-28:

- Host: Windows/amd64, Intel Core i7-12700H, Go 1.26.5.
- Seed: `20260727`.
- Normal acceptance: 1,000 upstreams × 10 endpoints; PASS in 0.04 s. No probe was scheduled before activation and resilience runtimes returned to zero after rollback.
- Normal healthy-proxy comparison (200 sequential requests): legacy 9,752 req/s at 680 µs p99; Phase 3B 12,031 req/s at 579 µs p99. This short developer run is informational only.
- `go test ./... -count=1`: PASS.
- Repeated health/retry/reconcile/shutdown suite (`-count=20`): PASS.
- Endpoint-health fuzz smoke: PASS for 10 seconds, 585,640 executions.
- Race gates: pending because this host has `CGO_ENABLED=0` and no C compiler.
- Full 10,000-upstream/100,000-endpoint acceptance: not run yet.
- Reference-Linux canonical acceptance: pending.

Five-count local benchmark medians:

| Benchmark | Median | Allocation |
|---|---:|---:|
| Health-aware WRR, all healthy | 12.80 ns/op | 0 B, 0 allocs |
| Health-aware WRR, one unhealthy | 14.24 ns/op | 0 B, 0 allocs |
| Health-aware consistent hash, all healthy | 10.46 ns/op | 0 B, 0 allocs |
| Health-aware consistent hash, one unhealthy | 161.0 ns/op | 112 B, 1 alloc |
| Retry budget credit | 1.05 ns/op | 0 B, 0 allocs |
| Retry budget acquire/release | 33.20 ns/op | 0 B, 0 allocs |
| Attempt transport, one attempt | 118.82 µs/op | 48,860 B, 131 allocs |
| Attempt transport, retry then success | 182.78 µs/op | 54,811 B, 199 allocs |
| Healthy proxy, Phase 3A baseline | 110.45 µs/op | 47,928 B, 124 allocs |
| Healthy proxy, Phase 3B enabled | 110.30 µs/op | 48,903 B, 132 allocs |

These are developer-machine measurements, not APISIX parity evidence. Phase 2 Task 16, Phase 3A canonical resource evidence, the Phase 3B full profile, race verification, and integrated APISIX comparison remain pending. APISIX comparison belongs to Phase 3D.

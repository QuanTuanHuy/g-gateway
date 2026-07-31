# Phase 3C1 Current Status

Status: `implementation complete; canonical protocol evidence pending`

Observed locally on 2026-07-31 at implementation checkpoint `b97af2a` plus the documented gRPC pass-through benchmark correction:

- Host: Windows/amd64, Intel Core i7-12700H.
- Toolchain: Go 1.26.5, staticcheck 2026.1 (v0.7.0), revive 1.15.0.
- Concurrency environment: `CGO_ENABLED=0`; gcc is not installed.
- Acceptance seed: `20260731`.

## Verification

- Formatting, `go vet ./...`, `staticcheck -tests=false ./...`, `revive`, `go test -p 1 ./... -count=1`, and `go build ./cmd/...`: PASS. The final combined sequential gate completed in 245.9 seconds. The build exited successfully but reported a non-fatal denied write to the user module-download stat cache; workspace `GOCACHE` and staticcheck cache were used for generated cache data.
- A fresh post-change `go test -p 1 ./... -count=1` finishing gate also passed on the final code tree in 302.2 seconds.
- The checked-in `configs/phase3c1.yaml` strict-decoder test passed after replacing mounted paths with temporary valid material, proving the example's v1alpha5 shape, references, routes, and transport policies load successfully.
- Race command: `go test -race ./internal/tlsmaterial ./internal/upstream ./internal/runtime ./internal/proxy ./internal/gateway ./test/integration -count=1`.
- Race result: pending on this host. Exact error: `go: -race requires cgo; enable cgo by setting CGO_ENABLED=1`.
- Repeated upstream/proxy/gateway TLS, mTLS, protocol, HTTP/2, gRPC, rotation, rollback, retirement, and shutdown suite (`-count=20`): PASS.
- The first combined repetition command reached its 300-second outer timeout only after upstream, proxy, and gateway had passed. A systematic split showed that the integration package alone requires about seven minutes on this host; the direct integration repetition completed PASS in 391.287 seconds. No runtime or Go-test timeout, panic, leak, or test failure occurred.

## Acceptance

Normal profile:

- 1,000 upstreams, 10 endpoints per upstream, 1,000 material resources, and 2 rotations.
- Material source input: 457,000 bytes.
- Incremental active heap: 5,565,176 bytes.
- Steady state: 1 live transport and 0 retired plan sets.
- Cleartext comparison, 200 requests at concurrency 8: legacy 17,020.84 req/s at 1.3377 ms p99; Phase 3C1 16,879.35 req/s at 1.2749 ms p99.
- Ratios: throughput 0.9917, p99 0.9531, allocation delta 0.00; both paths used 115.00 allocations per measured request.

Full local profile:

- 10,000 upstreams, 10 endpoints per upstream, 10,000 material resources, and 20 rotations.
- 100,000 total endpoints; no upstream exceeds the 1,000-endpoint limit.
- Material source input: 4,570,000 bytes, below the 64 MiB bound.
- Incremental active heap: 54,243,168 bytes, below the 512 MiB bound.
- Steady state after every rotation: 1 live transport and 0 retired plan sets.
- Median of five alternating 5,000-request rounds at concurrency 8: legacy 18,108.06 req/s at 1.1628 ms p99; Phase 3C1 17,857.16 req/s at 1.2211 ms p99.
- Ratios: throughput 0.9861, p99 1.0501, allocation delta 0.00. The local 95% throughput, 110% p99, and no-additional-allocation gates passed.

## Fuzz

Thirty-second fuzz gates passed:

| Target | Executions |
|---|---:|
| `FuzzCertificateDocument` | 7,005,401 |
| `FuzzTrustBundleDocument` | 7,398,399 |
| `FuzzTransportPolicy` | 1,267,270 |

The package seed corpus also exercises deterministic transport keys, typed TLS failure redaction, valid/malformed PEM, duplicate/reordered trust roots, and every supported scheme/protocol row.

## Local benchmark medians

The following medians come from the final five-count command on the developer machine. Listener and server setup is outside timed request operations. `BenchmarkGRPC` measures TLS client → gateway → TLS gRPC upstream pass-through, not a direct grpc-go baseline.

| Benchmark | Median | Bytes/op | Allocs/op |
|---|---:|---:|---:|
| Transport profile, cleartext HTTP/1 | 365.5 ns | 144 | 1 |
| Transport profile, HTTPS HTTP/1 | 594.6 ns | 144 | 1 |
| Transport profile, HTTPS HTTP/2 | 547.7 ns | 144 | 1 |
| Transport profile, h2c | 497.2 ns | 144 | 1 |
| TLS handshake, full | 2.477 ms | 160,494 | 1,108 |
| TLS handshake, resumed | 230.5 us | 4,386 | 53 |
| HTTP protocol, HTTPS HTTP/1 | 180.9 us | 4,358 | 53 |
| HTTP protocol, HTTPS HTTP/2 multiplexed | 47.39 us | 5,262 | 56 |
| HTTP protocol, h2c | 201.3 us | 5,828 | 67 |
| gRPC pass-through, unary | 533.9 us | 77,658 | 366 |
| gRPC pass-through, server streaming | 593.0 us | 71,077 | 343 |
| Transport generation, create | 12.06 us | 6,640 | 65 |
| Transport generation, reuse | 12.99 us | 2,995 | 38 |
| Transport generation, rotate | 30.94 us | 9,874 | 107 |

These numbers are informational developer-machine observations. The five runs showed substantial host scheduling variance, so no absolute performance conclusion is inferred from them.

## Pending canonical evidence

Phase 3C1 is not marked accepted because these gates remain:

- race verification on a CGO/compiler-capable reference host;
- reference-Linux protocol and performance reproduction;
- canonical external interoperability evidence under the production deployment shape;
- integrated APISIX comparison, which remains owned by Phase 3D;
- deferred Phase 2 Task 16 and earlier phase canonical evidence.

The current checkpoint demonstrates implemented behavior and bounded local regression evidence. It is not APISIX parity or production certification, and it does not mark umbrella Phase 3C complete. Phase 3C2 receives generic immutable `Certificate` resources for downstream exact/wildcard SNI; Phase 3C3 receives HTTP listener/runtime foundations for WebSocket lifecycle.

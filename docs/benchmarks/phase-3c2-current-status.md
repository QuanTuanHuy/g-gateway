# Phase 3C2 Current Status

## Status

Implementation complete; canonical downstream TLS evidence pending.

## Environment

Evidence below was observed on 2026-09-17 on a Windows developer host:

- OS/architecture: `windows/amd64`
- Go: `go1.26.5`
- CPU: 12th Gen Intel(R) Core(TM) i7-12700H
- `CGO_ENABLED=0`

This is developer-machine evidence, not production certification or APISIX parity.

## Correctness and lifecycle evidence

- The focused correctness command passed for `internal/downstreamtls`, `internal/config`, `internal/runtime`, `internal/gateway`, `internal/telemetry`, and `test/integration`:

  ```text
  go test ./internal/downstreamtls ./internal/config ./internal/runtime ./internal/gateway ./internal/telemetry ./test/integration -count=1
  ```

- The planned lifecycle profile passed 20/20:

  ```text
  go test ./internal/gateway ./test/integration -run 'Test.*Phase3C2|TestDownstreamTLS' -count=20
  ```

- The plan's lifecycle regular expression does not select the rejected-update, HTTP/1.1, HTTP/2, or WebSocket rotation test names. The following supplemental profile passed 20/20:

  ```text
  go test ./test/integration -run 'Test(RejectedDownstreamTLS|HTTP1ConnectionSurvivesCertificateRotation|HTTP2StreamSurvivesCertificateRotation|WebSocketSurvivesCertificateRotation)' -count=20
  ```

The profiles exercised 100 consecutive certificate rotations during concurrent real TLS handshakes, last-good retention after a rejected update, disabled session resumption, continuity of established HTTP/1.1, HTTP/2, and WebSocket connections, new-connection certificate selection, and return to zero retired plan sets and tunnel leases.

Independent review found that canonicalizing a certificate SAN such as `*.example.com.` could admit a leaf that Go clients reject. The final implementation additionally verifies the individual wildcard SAN with `x509.VerifyHostname`; unit and live-Apply regressions prove rejection and last-good retention. The concurrent lifecycle test now requires all four workers to complete an initial handshake and requires further handshake progress in both halves of the 100-rotation run.

Final repository gates passed on the completed tree:

```text
go test -p 1 ./... -count=1
go vet ./...
staticcheck -tests=false ./...
revive -set_exit_status -config revive.toml -formatter default ./...
go build ./cmd/...
```

`gofmt -l` reported no changed Go files, `git diff --check` passed, and the relative-link audit resolved 31 links across the six Markdown files changed by Phase 3C2.

## Fuzz evidence

Both 30-second fuzz gates passed without a panic or invariant failure:

| Target | Executions | New interesting inputs |
| --- | ---: | ---: |
| `FuzzNormalizeBindingHost` | 1,931,109 | 52 |
| `FuzzCompileSelector` | 126,619 | 8 |

Commands:

```text
go test ./internal/downstreamtls -run '^$' -fuzz FuzzNormalizeBindingHost -fuzztime 30s
go test ./internal/downstreamtls -run '^$' -fuzz FuzzCompileSelector -fuzztime 30s
```

No generated fuzz corpus or cache directory was added to the worktree.

## Developer-machine benchmarks

The Task 9 command's expression, `Benchmark(Selector|CompileSelector)10K`, selects only `BenchmarkCompileSelector10K`; Task 3 intentionally names the lookup cases with an intervening `Default`, `Exact`, or `Wildcard`. The following corrected expression was used to collect all four benchmarks:

```text
go test ./internal/downstreamtls -run '^$' -bench 'Benchmark(Selector(Default|Exact|Wildcard)|CompileSelector)10K' -benchmem -count=5
```

Lookup samples in ns/op:

| Benchmark | Sample 1 | Sample 2 | Sample 3 | Sample 4 | Sample 5 | Median | B/op | Allocs/op |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| Default | 45.99 | 199.5 | 202.1 | 240.1 | 247.6 | 202.1 | 0 | 0 |
| Exact | 284.9 | 279.4 | 266.1 | 256.9 | 268.5 | 268.5 | 0 | 0 |
| Wildcard | 432.9 | 454.6 | 425.0 | 443.2 | 476.8 | 443.2 | 0 | 0 |

Selector compilation samples:

| Measurement | Sample 1 | Sample 2 | Sample 3 | Sample 4 | Sample 5 | Median |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| ns/op | 378,921,300 | 309,889,725 | 328,289,275 | 448,535,633 | 367,046,125 | 367,046,125 |
| B/op | 51,328,480 | 51,328,536 | 51,328,536 | 51,328,368 | 51,328,620 | 51,328,536 |
| allocs/op | 1,050,400 | 1,050,400 | 1,050,400 | 1,050,399 | 1,050,401 | 1,050,400 |

All lookup cases satisfied the zero-allocation invariant. Compile time and allocation observations are developer-host measurements, not portable pass/fail thresholds.

## Pending canonical gates

- `go test ./... -race -count=1` on a CGO/compiler-capable host. This developer host reports `CGO_ENABLED=0`; the attempted command returned exactly `go: -race requires cgo; enable cgo by setting CGO_ENABLED=1`.
- Reference-Linux correctness, lifecycle, resource, and performance reproduction.
- Phase 3D and its integrated APISIX comparison.
- Deferred canonical evidence from earlier phases where still listed by their status ledgers.

## Claims boundary

This checkpoint demonstrates implemented exact/wildcard/default SNI selection, atomic certificate rotation, last-good rejection behavior, bounded observability, connection continuity, and zero-allocation selector lookup under local tests. It does not establish production readiness, reference-Linux behavior, APISIX parity, or completion of the umbrella Phase 3C acceptance program.

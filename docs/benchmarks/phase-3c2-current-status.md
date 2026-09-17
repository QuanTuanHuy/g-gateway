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
| `FuzzCompileSelector` | 190,641 | 53 |

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
| Default | 43.91 | 206.3 | 238.0 | 210.5 | 241.0 | 210.5 | 0 | 0 |
| Exact | 235.5 | 250.4 | 224.9 | 227.5 | 255.7 | 235.5 | 0 | 0 |
| Wildcard | 282.7 | 292.2 | 324.1 | 323.0 | 270.6 | 292.2 | 0 | 0 |

Selector compilation samples:

| Measurement | Sample 1 | Sample 2 | Sample 3 | Sample 4 | Sample 5 | Median |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| ns/op | 284,759,500 | 333,516,200 | 268,160,820 | 223,471,180 | 269,678,050 | 269,678,050 |
| B/op | 51,328,424 | 51,328,396 | 51,328,412 | 51,328,390 | 51,328,452 | 51,328,412 |
| allocs/op | 1,050,399 | 1,050,399 | 1,050,399 | 1,050,399 | 1,050,399 | 1,050,399 |

All lookup cases satisfied the zero-allocation invariant. Compile time and allocation observations are developer-host measurements, not portable pass/fail thresholds.

## Pending canonical gates

- `go test ./... -race -count=1` on a CGO/compiler-capable host. This developer host reports `CGO_ENABLED=0`; the attempted command returned exactly `go: -race requires cgo; enable cgo by setting CGO_ENABLED=1`.
- Reference-Linux correctness, lifecycle, resource, and performance reproduction.
- Phase 3D and its integrated APISIX comparison.
- Deferred canonical evidence from earlier phases where still listed by their status ledgers.

## Claims boundary

This checkpoint demonstrates implemented exact/wildcard/default SNI selection, atomic certificate rotation, last-good rejection behavior, bounded observability, connection continuity, and zero-allocation selector lookup under local tests. It does not establish production readiness, reference-Linux behavior, APISIX parity, or completion of the umbrella Phase 3C acceptance program.

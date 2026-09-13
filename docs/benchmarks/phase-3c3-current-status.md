# Phase 3C3 Current Status

Status: **implementation complete; canonical WebSocket evidence pending**.

Evidence below was observed on Windows/amd64, Go 1.26.5, Intel Core i7-12700H on 2026-09-13. It is developer-host evidence, not production certification or APISIX parity.

## Passed evidence

- Baseline before implementation: `go test ./... -count=1` passed.
- WebSocket unit/integration repetition: `go test ./internal/testupstream -count=1` passed; `go test ./test/integration -run 'TestWebSocket' -count=10` passed in 9.222s.
- Normal lifecycle: `go test ./internal/tunnel ./internal/proxy ./internal/gateway -count=1` passed. The locked profile used seed `20260731`, 200 tunnels, 20 messages per tunnel, and 2 rotations. One observed proxy run delivered 4,000 messages at 4,082.81 msg/s with 2.0092ms p99, 14,016,512-byte heap delta, and +6 goroutines before test cleanup. The Gateway 200-tunnel drain ended with +1 goroutine and zero registry/upstream ownership.
- Session benchmark: `BenchmarkPhase3C3SessionCopy` observed 4,819ns/op, 13,598.81 MB/s, 722 B/op, and 7 allocs/session. Its companion test proved allocations did not increase when input grew from 1 to 128 chunks.
- Request fuzz: `go test ./internal/websocket -run '^$' -fuzz '^FuzzRequestHandshake$' -fuzztime=30s` passed with 1,643,107 executions and 39 new interesting inputs.
- Response fuzz: `go test ./internal/websocket -run '^$' -fuzz '^FuzzResponseHandshake$' -fuzztime=30s` passed with 1,842,691 executions and 19 new interesting inputs.
- Documentation/configuration: the six-file relative Markdown link audit, `git diff --check`, and `go test ./internal/config -run TestPhase3C3ExampleConfigurationLoads -count=1 -v` passed.
- Quality gate: Go formatting, `go vet ./...`, `staticcheck -tests=false ./...`, and `revive -set_exit_status -config revive.toml -formatter default ./...` passed. After the final lifecycle review fixes, `go test -p 1 ./... -count=1` passed in 67.827s, followed by `go build ./cmd/...` passing.
- Lifecycle repetition: the proxy Phase 3C3 profile passed 20/20 in 38.793s; focused Gateway shutdown/lifecycle passed 20/20 in 7.353s; focused integration WebSocket/shutdown passed 20/20 in 15.129s. The five focused pre-commit timeout/cancellation arbitration tests passed 20/20 in 1.940s after independent concurrency review. The package and integration regression suite then passed. Repetition was run sequentially because concurrent package repetition exhausted the Windows loopback ephemeral-port pool. No test process remained after that aborted concurrent attempt.

## Developer-host benchmark snapshot

Command: `go test ./internal/proxy -run '^$' -bench 'BenchmarkPhase3C3' -benchmem -count=5` (42.186s).

Median observations:

| Measurement | Direct | Gateway |
| --- | ---: | ---: |
| Handshake latency | 1,753,005 ns/op | 3,590,275 ns/op |
| Handshake allocations | 113-114 allocs/op | 364 allocs/op |
| Message latency | 100,917 ns/op | 247,745 ns/op |
| Message allocations | 0 allocs/op | 0 allocs/op |

The Windows developer-host relative performance does not satisfy the locked full gates (90% handshake throughput, 125% handshake p99, 95% message throughput, and 110% message p99). No acceptance claim is made from this snapshot.

## Pending canonical evidence

- `GATEWAY_PHASE3C3_ACCEPTANCE=1` full profile: 10,000 tunnels, 100 messages per tunnel, 20 rotations, alternating five-round direct/Gateway measurements, and the four locked relative gates. It was not run on this Windows developer host because the preliminary locked benchmark already failed the relative thresholds; run it on the reference Linux host.
- `go test -p 1 ./... -race -count=1` on a CGO-capable host. This host reports `CGO_ENABLED=0`; the observed result was `go: -race requires cgo; enable cgo by setting CGO_ENABLED=1`.
- Reference-Linux lifecycle, socket-limit, heap/goroutine, and performance evidence.
- Integrated APISIX comparison, owned by Phase 3D.
- Deferred Phase 2 Task 16 evidence remains mandatory.

The umbrella Phase 3C remains incomplete: Phase 3C2 dynamic downstream certificate selection and all canonical gates are still outstanding.

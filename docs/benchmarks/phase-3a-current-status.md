# Phase 3A benchmark and verification — current status

- Updated: 2026-07-26
- Branch: `codex/phase3a-upstream-runtime-balancing-kernel-design`
- Status: `implementation complete; canonical resource evidence pending`
- Development environment: Windows amd64, 20 logical CPUs, Go 1.26.5
- Evidence classification: `provisional development evidence`

## Executive status

Phase 3A implementation is complete through strict v1alpha3 configuration, transactional upstream reconcile, registry-backed immutable plans, shared transports, WRR, consistent hash, request leases, reaper cleanup, dynamic integration tests, bounded telemetry, normal acceptance, and selector/lease/reconcile benchmarks.

This checkpoint is not `Phase 3A accepted`. The opt-in 10,000-upstream/100,000-endpoint resource profile, a CGO-capable race run, five-minute fuzzing, reference-Linux absolute gates, and APISIX end-to-end comparison have not been completed. No omitted command is reported as passed.

## Correctness evidence

| Evidence | Outcome | Observed command or source |
|---|---|---|
| Strict v1alpha1/v1alpha2/v1alpha3 compatibility and canonical validation | Passed | full Go suite |
| WRR/hash distribution, determinism, fallback, and remap tests | Passed | `go test -p 1 ./... -count=1` |
| Candidate prepare/commit/rollback and last-known-good behavior | Passed | registry, manager, gateway, and integration suites |
| In-flight old-plan/new-plan consistency | Passed | snapshot integration and manager lease tests |
| Weight-only keepalive reuse | Passed | `TestUpstreamReconcileWeightOnlyUpdateReusesWarmConnections` |
| Unrelated-upstream transport/pool isolation | Passed | package-private pointer and black-box connection tests |
| Bounded metric labels and lifecycle logs | Passed | telemetry and lifecycle observer tests |
| Normal Phase 2 and Phase 3A acceptance | Passed | acceptance command below |
| Full repository test suite | Passed | `go test -p 1 ./... -count=1` |
| Formatting check at implementation checkpoints | Passed | `gofmt -l .` returned no files |
| Static analysis | Passed | `go vet ./...` |
| Command builds | Passed | `go build ./cmd/...` |
| Local Markdown targets | Passed | six-document relative-link audit; zero missing targets |
| Race detector | Pending | Windows host has `CGO_ENABLED=0`; `-race` stops before tests |
| Five-minute upstream fuzz targets | Pending | not run |

The full suite was rerun after the lease hot-path optimization and Phase 3A acceptance scaffolding. It passed all packages, including `test/integration`.

## Normal acceptance evidence

Observed command:

```powershell
go test -p 1 ./internal/upstream ./internal/runtime `
  -run 'TestPhase2Acceptance|TestPhase3AAcceptance' -count=1 -v
```

Observed Phase 3A normal profile:

| Metric | Result |
|---|---:|
| Seed | `20260726` |
| Canonical JSON checksum | `2883f9ffaffb9619a528ff9fb6d51ced58d29dd824dcf072b94e1441550df87c` |
| Upstreams / endpoints | `1,000 / 10,000` |
| Weight-only swaps | `2` |
| Initial build | `77.7852 ms` |
| One active plan-set incremental heap | `8,518,184 bytes` |
| Retained heap after swaps and boundary GC | `8,302,928 bytes` |
| Retired plan-sets after quiescence | `0` |
| Unchanged endpoint/transport reuse | `100%` |

These are developer-machine results, not reference-Linux certification.

## Allocation and relative-scale evidence

The planned benchmark command was observed with `-benchmem -count=3`.

| Benchmark | Observed range | Allocations |
|---|---:|---:|
| Snapshot acquire/release | `19.69–20.29 ns/op` | `0 B/op`, `0 allocs/op` |
| WRR / 2 endpoints | `10.42–11.27 ns/op` | `0 B/op`, `0 allocs/op` |
| WRR / 1,000 endpoints | `10.14–11.17 ns/op` | `0 B/op`, `0 allocs/op` |
| Consistent hash / 10 endpoints | `33.27–34.73 ns/op` | `0 B/op`, `0 allocs/op` |
| Consistent hash / 1,000 endpoints | `79.18–86.28 ns/op` | `0 B/op`, `0 allocs/op` |
| Registry full reconcile | `76.5–83.3 ms/op` | approximately `32.5 MB/op`, `198.6k allocs/op` |
| Registry weight-only reconcile | `70.8–73.5 ms/op` | approximately `27.9 MB/op`, `148.6k allocs/op` |

Using the three-run medians, WRR/1,000 is about `97.6%` of WRR/2 and consistent-hash/1,000 is about `237.9%` of consistent-hash/10. Both development relative-scale gates pass. Reconcile allocation counts are recorded for visibility; Phase 3A does not define a zero-allocation reconcile gate.

## Full-envelope resource evidence

The deterministic full profile is implemented:

- 10,000 upstreams;
- 100,000 endpoints;
- 80/20 WRR/consistent-hash mix;
- weights cycling through `1..5`;
- 20 weight-only swaps;
- build, one-plan-set heap, retained heap, budget, reuse, and reaper assertions.

`GATEWAY_PHASE3A_ACCEPTANCE=1` was not run at this checkpoint. Therefore these gates remain pending:

1. full build completes within five seconds;
2. one active plan-set uses at most 512 MiB incremental heap;
3. retained heap after 20 swaps is at most 125% of one active plan-set;
4. the full profile remains within snapshot WRR/hash budgets on the reference environment.

The stricter status `Phase 3A accepted` is not permitted until those absolute gates pass on the defined reference Linux environment.

## Race and fuzz status

The Windows development host reports `CGO_ENABLED=0` and has no C compiler configured. The focused `go test -race` command terminates before executing tests with:

```text
go: -race requires cgo; enable cgo by setting CGO_ENABLED=1
```

This is recorded as unavailable, not passed. Run the pinned Linux container command in the [Phase 3A runbook](../operations/phase-3a-runbook.md#extended-verification).

The two five-minute upstream fuzz targets are also pending.

## Deferred APISIX E2E

Phase 3A intentionally contains no canonical APISIX end-to-end comparison. Selector/reconcile microbenchmarks do not establish gateway throughput, latency, CPU, memory, or APISIX parity. Integrated resilience comparison belongs to Phase 3D and reference-Linux production certification remains later roadmap work.

The [Phase 2 Task 16 debt](phase-2-current-status.md#deferred-task-16) is still deferred in full. Phase 3A neither replaces nor satisfies its 1-route/100,000-route end-to-end gates.

## Handoff

Phase 3B may begin from:

- immutable upstream plans and one-revision request leases;
- canonical endpoint identity;
- shared transport profiles and preserved keepalive pools;
- deterministic WRR and consistent hash;
- transactional reconcile and last-known-good behavior;
- registry ownership, retirement, reaper, and bounded telemetry.

Phase 3B must not move health state into snapshots. Phase 3C owns transport security/protocol and WebSocket expansion. Phase 3D owns bounded access logging and canonical integrated APISIX evidence.

## Evidence sources

- [Phase 3A design](../superpowers/specs/2026-07-26-phase-3a-upstream-runtime-balancing-kernel-design.md)
- [Phase 3A implementation plan](../superpowers/plans/2026-07-26-phase-3a-upstream-runtime-balancing-kernel.md)
- [Phase 3A operational runbook](../operations/phase-3a-runbook.md)
- [Phase 2 current status and deferred Task 16](phase-2-current-status.md)
- Implementation commits `999f9cd` through `33b7d9c`
- Relative-scale baseline commit `0427bfa`

# Phase 3A benchmark and verification — current status

- Updated: 2026-07-26
- Branch: `codex/phase3a-upstream-runtime-balancing-kernel-design`
- Status: `implementation complete; canonical resource evidence pending`
- Development environment: Windows amd64, 20 logical CPUs, Go 1.26.5
- Evidence classification: `provisional development evidence`

## Executive status

Phase 3A implementation is complete through strict v1alpha3 configuration, transactional upstream reconcile, registry-backed immutable plans, shared transports, WRR, consistent hash, request leases, reaper cleanup, dynamic integration tests, bounded telemetry, normal acceptance, and selector/lease/reconcile benchmarks.

This checkpoint is not `Phase 3A accepted`. The opt-in 10,000-upstream/100,000-endpoint resource profile passes on the Windows development machine, but a CGO-capable race run, five-minute fuzzing, reference-Linux absolute gates, and APISIX end-to-end comparison have not been completed. No omitted command is reported as passed.

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
| Shutdown/reaper/reconcile repetition | Passed | focused lifecycle suite, `-count=20` |
| Formatting check at implementation checkpoints | Passed | `gofmt -l .` returned no files |
| Static analysis | Passed | `go vet ./...` |
| Command builds | Passed | `go build ./cmd/...` |
| Local Markdown targets | Passed | six-document relative-link audit; zero missing targets |
| Race detector | Pending | Windows host has `CGO_ENABLED=0`; `-race` stops before tests |
| 30-second upstream fuzz smoke | Passed | both targets; approximately 3.28M and 3.59M executions |
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

The final planned benchmark command was observed with `-benchmem -count=5`.

| Benchmark | Observed range | Allocations |
|---|---:|---:|
| Snapshot acquire/release | `19.32–23.45 ns/op`; median `19.75` | `0 B/op`, `0 allocs/op` |
| WRR / 2 endpoints | `10.17–10.77 ns/op`; median `10.40` | `0 B/op`, `0 allocs/op` |
| WRR / 1,000 endpoints | `10.45–11.23 ns/op`; median `11.00` | `0 B/op`, `0 allocs/op` |
| Consistent hash / 10 endpoints | `31.87–42.85 ns/op`; median `40.20` | `0 B/op`, `0 allocs/op` |
| Consistent hash / 1,000 endpoints | `69.52–82.60 ns/op`; median `72.05` | `0 B/op`, `0 allocs/op` |
| Registry full reconcile | `77.1–93.5 ms/op`; median `82.08` | approximately `32.5 MB/op`, `198.6k allocs/op` |
| Registry weight-only reconcile | `78.9–82.7 ms/op`; median `79.67` | approximately `27.9 MB/op`, `148.6k allocs/op` |

Using the five-run medians, WRR/1,000 is about `105.8%` of WRR/2 and consistent-hash/1,000 is about `179.2%` of consistent-hash/10. Both development relative-scale gates pass.

For the explicitly requested 1/100/1,000 series, WRR medians are `0.5209 / 11.14 / 11.00 ns/op`; WRR/100 and WRR/1,000 are about `2,138.5%` and `2,111.7%` of the one-endpoint direct fast path, while WRR/1,000 is `98.7%` of WRR/100. Consistent-hash medians are `1.237 / 52.57 / 72.05 ns/op`; hash/100 and hash/1,000 are about `4,249.8%` and `5,824.6%` of the direct fast path, while hash/1,000 is `137.1%` of hash/100. The design gates intentionally use WRR/2 and hash/10 because the single-endpoint cases bypass schedule/continuum lookup.

Reconcile allocation counts are recorded for visibility; Phase 3A does not define a zero-allocation reconcile gate.

## Full-envelope resource evidence

The deterministic full profile is implemented:

- 10,000 upstreams;
- 100,000 endpoints;
- 80/20 WRR/consistent-hash mix;
- weights cycling through `1..5`;
- 20 weight-only swaps;
- build, one-plan-set heap, retained heap, budget, reuse, and reaper assertions.

Observed Windows development result:

| Metric | Result |
|---|---:|
| Seed | `20260726` |
| Canonical JSON checksum | `f39765a94d0debbefd78e436cc68aa757670b48485424d85d653a183611b0536` |
| Upstreams / endpoints | `10,000 / 100,000` |
| Weight-only swaps | `20` |
| Initial build | `763.2531 ms` |
| One active plan-set incremental heap | `85,994,248 bytes` |
| Retained heap after swaps and boundary GC | `83,929,720 bytes` |
| Retained / one-plan-set heap | approximately `97.6%` |
| Budget, reuse, and reaper assertions | Passed |

All full-envelope thresholds pass provisionally on this host. The stricter status `Phase 3A accepted` is not permitted until the same absolute gates and race verification pass on the defined reference Linux environment.

## Race and fuzz status

The Windows development host reports `CGO_ENABLED=0` and has no C compiler configured. The focused `go test -race` command terminates before executing tests with:

```text
go: -race requires cgo; enable cgo by setting CGO_ENABLED=1
```

This exact command was rerun at the final checkpoint and is recorded as unavailable, not passed. Run the pinned Linux container command in the [Phase 3A runbook](../operations/phase-3a-runbook.md#extended-verification).

Both 30-second fuzz smoke runs passed:

- `FuzzNormalizeEndpoint`: approximately 3.28 million executions;
- `FuzzHashKey`: approximately 3.59 million executions.

The two five-minute extended fuzz runs remain pending.

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

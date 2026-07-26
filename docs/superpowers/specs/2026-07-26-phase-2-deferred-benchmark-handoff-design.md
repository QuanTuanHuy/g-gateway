# Phase 2 — Deferred benchmark and Phase 3 handoff

- Date: 2026-07-26
- Status: Approved design; awaiting written-spec review
- Parent phase design: [Phase 2 — Runtime snapshot and router kernel](2026-07-23-phase-2-runtime-snapshot-router-kernel-design.md)
- Parent roadmap: [Go-native API Gateway phase roadmap](2026-07-21-go-native-api-gateway-phase-roadmap-design.md)

## 1. Decision

Phase 2 core implementation is allowed to enter a transitional checkpoint before the isolated end-to-end benchmark harness in Task 16 is implemented.

The checkpoint status is:

```text
implementation complete; canonical evidence pending
```

This status means the runtime snapshot, router kernel, compiled plugin pipeline, proxy integration, deterministic 100,000-route dataset, acceptance test, and router microbenchmarks are implemented. It does not mean the Phase 2 end-to-end relative scalability gates or APISIX comparison have passed.

Task 16 is deferred in full. No partial runner, report schema, report command, Docker profile, or Phase 2 end-to-end smoke result will be presented as completed work.

## 2. Rationale

Task 16 is a self-contained performance-evidence slice whose implementation and Docker verification require a substantial uninterrupted session. Deferring that slice lets work begin on Phase 3 upstream resilience without weakening the immutable snapshot and routing contracts already established by Phase 2.

The deferral changes project scheduling, not the performance requirements. All missing Phase 2 evidence remains mandatory before production certification.

## 3. Documentation deliverables

The transitional checkpoint updates:

- `docs/operations/phase-2-runbook.md`;
- `docs/benchmarks/phase-2-current-status.md`;
- the repository `README.md`;
- the Phase 2 design audit;
- the phase roadmap.

The runbook documents supported startup and verification workflows, internal-only snapshot activation, known exclusions, and exact commands for the deferred benchmark work.

The status document separates:

1. implemented capabilities;
2. evidence already captured;
3. checks intentionally not rerun at this checkpoint;
4. deferred Task 16 artifacts;
5. gates that remain unproven.

No committed document may claim APISIX parity, end-to-end relative scalability success, or production readiness without the corresponding raw evidence.

## 4. Evidence policy

Existing evidence may be cited only when it can be traced to committed tests or previously captured command output. The checkpoint records the existing deterministic dataset checksum, compile and heap acceptance values, and zero-allocation router benchmark result as provisional development evidence.

The following remain pending:

- the complete isolated Task 16 benchmark harness and report pipeline;
- semantic preflight against the pinned APISIX checkout;
- Phase 2 end-to-end smoke runs;
- five-repetition canonical comparisons;
- end-to-end throughput, p99, sentinel-spread, and resource verdicts;
- the long fuzz acceptance suite and final full Docker race rerun when not executed during this documentation checkpoint;
- dedicated Linux certification.

Smoke results, microbenchmarks, and compile/memory acceptance data cannot substitute for the missing end-to-end relative gates.

## 5. Phase 3 entry boundary

Phase 3 may begin with the following Phase 2 contracts treated as stable:

- canonical resource IDs and resolved upstream references;
- one immutable runtime snapshot observed for the full request lifecycle;
- shadow build followed by atomic activation;
- compiled router and plugin pipeline;
- request context ownership and route identity;
- the boundary between immutable configuration and mutable upstream runtime state;
- deterministic dataset and benchmark evidence conventions.

Phase 3 may replace the fixed upstream table with a lifecycle-aware runtime registry and add balancing, health, timeout, retry, TLS, WebSocket, and bounded access-log behavior.

Phase 3 must not:

- move mutable endpoint health or connection-pool state into `RuntimeSnapshot`;
- introduce locks, parsing, or configuration resolution into route matching;
- invalidate Phase 2 precedence or snapshot-consistency guarantees;
- silently absorb or remove the deferred Phase 2 performance gates.

## 6. Verification at this checkpoint

Because this change is documentation-only, the checkpoint runs documentation validation and inexpensive repository checks. It does not run the deferred Task 16 workload, multi-hour canonical comparison, five-minute-per-target fuzz suite, or other explicitly long Docker workloads.

Any verification not executed is listed as pending rather than inferred from an earlier or narrower result.

## 7. Phase 3 preparation

The handoff documentation supplies the starting constraints and unresolved debt for a separate Phase 3 brainstorming cycle. Phase 3 receives its own approved design and implementation plan; this checkpoint does not preselect detailed balancing, health-check, retry, or lifecycle algorithms.


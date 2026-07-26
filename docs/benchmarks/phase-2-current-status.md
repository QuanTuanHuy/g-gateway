# Phase 2 benchmark and verification — current status

- Updated: 2026-07-26
- Branch: `codex/phase2-runtime-snapshot-router-kernel-design`
- Handoff decision commit: `468552d`
- Status: `implementation complete; canonical evidence pending`
- Development environment: Docker Desktop, Linux containers, x86_64, 20 logical CPUs, Go 1.26.5
- Evidence classification: `provisional development evidence`

## Executive status

Phase 2 core implementation is complete through the deterministic dataset, 100,000-route acceptance test, and router microbenchmark slice. Immutable runtime activation, deterministic routing, compiled plugins, proxy integration, and request consistency are covered by committed unit, integration, property, fuzz-regression, concurrency, and race tests.

The isolated end-to-end benchmark and report in Task 16 are deferred in full. Consequently, this checkpoint does not prove the Go 1-route versus 100,000-route end-to-end throughput/p99 gates and contains no Phase 2 APISIX comparison. It is not an APISIX parity claim or production performance certification.

Phase 3 design may begin using the stable Phase 2 contracts. All missing Phase 2 performance evidence remains mandatory before production certification.

## Implemented capabilities

- Canonical Route, Service, and Upstream resources.
- Strict `gateway/v1alpha2` decoding and `gateway/v1alpha1` compatibility.
- Fixed upstream runtime table with pooled transports and explicit cleanup.
- Immutable runtime snapshot builder, monotonic manager, and atomic activation.
- Per-request snapshot consistency across concurrent revision changes.
- Compiled deterministic host/path/method/header/query routing.
- Request context and compiled plugin registry/chain.
- Built-in request-id and request/response header-rewrite plugins.
- Runtime-backed proxy handler preserving Phase 1 HTTP semantics.
- Deterministic 1-route and 100,000-route Go/APISIX dataset generation.

The implementation slices are traceable through commits `d19b2e9` through `181aae2`.

## Evidence captured

| Evidence | Result | Source |
|---|---|---|
| Strict configuration and canonical validation | Passed during implementation | `d19b2e9`, `7b643e9` |
| Fixed upstream lifecycle | Passed unit/integration/race verification | `3956bc3`, `27813bc` |
| Router reference/property comparison | Passed fixed 10,000-case suite | `ca6cf80`, `6392c1a` |
| Fuzz regression corpus | Passed normal corpus replay | `6392c1a` |
| Plugin compile/execution | Passed unit and integration tests | `c18afa3`, `a71850a`, `69c29ef` |
| Snapshot consistency | Held requests retain old revision; new requests observe new revision | `34fc617`, `126000b`, `27813bc` |
| Process/protocol integration | v1alpha2 exact/parameter/plugin behavior passed over HTTP/1.1 and HTTP/2 | `27813bc` |
| Metric-cardinality guard | 100,000 configured routes do not precreate request series | `27813bc` |
| Deterministic dataset and checksums | Passed generator/render/reload tests | `181aae2` |
| Static match allocations | `0 B/op`, `0 allocs/op` | `181aae2` |

The implementation session also ran the focused suites, Docker race suite, and full Go suite after the runtime integration changes. Those long commands were not rerun during this documentation-only checkpoint; the current short verification outcome is recorded separately below.

## Provisional resource and microbenchmark results

Standard dataset metadata:

- seed: `20260723`;
- checksum: `a418eede5fa0e865b0bf39464a8e83a37b69dd4329138e35f2a12048635c154d`;
- route count: `100,000`;
- host distribution: exact 60%, wildcard 20%, hostless 20%;
- path distribution: exact 50%, parameter 20%, prefix 15%, catch-all 15%;
- method distribution: unconstrained 90%, constrained 10%;
- predicate routes: 20%;
- service-referenced routes: 50%;
- each built-in plugin: 10%.

Previously captured opt-in acceptance values:

| Metric | Result |
|---|---:|
| 100,000-route compile | `503.1312 ms` |
| One active snapshot incremental heap | `115,167,144 bytes` |
| Retained heap after 20 swaps and boundary GC | `89,013,496 bytes` |
| Steady-state / one-snapshot heap | `77.3%` |
| Normal 10,000-route acceptance compile | approximately `41 ms` |
| Static first/middle/last match allocations | `0 B/op`, `0 allocs/op` |
| Typical 1-route static match | approximately `130–160 ns/op` |
| Typical 100,000-route static match | approximately `144–209 ns/op` |

These numbers came from the previously executed Go acceptance and five-run router microbenchmark commands on the development environment. They show the compile, heap, steady-state, and zero-allocation development gates behaving within their thresholds. No committed Phase 2 raw-result directory exists, so these values remain provisional and are not a replacement for reference-Linux or end-to-end evidence.

## Documentation checkpoint verification

The following short checks were run on 2026-07-26 with local Go 1.26.5:

| Command | Outcome |
|---|---|
| Local Markdown target check across README, runbook, status, roadmap, and handoff specs | Passed; zero missing targets |
| `go vet ./...` | Passed |
| `go test ./... -count=1` | Passed |
| `go build ./cmd/...` | Passed |
| `gofmt -l .` | Did not pass; reported 14 previously committed files with CRLF working-tree line endings |

The `gofmt` investigation found `core.autocrlf=true`, no repository `.gitattributes`, and CRLF sequences in the reported working-tree files. `gofmt -d cmd/bench-report/main.go` showed a line-ending-only rewrite with no Go token change. This documentation checkpoint deliberately did not normalize unrelated source files.

The reported files were:

```text
cmd/bench-report/main.go
cmd/gateway-dp/main.go
cmd/test-upstream/main.go
internal/benchreport/report.go
internal/benchreport/report_test.go
internal/gateway/response_state.go
internal/proxy/errors.go
internal/proxy/headers.go
internal/proxy/headers_test.go
internal/testupstream/server.go
internal/testupstream/server_test.go
internal/upstream/runtime_test.go
test/integration/gateway_test.go
test/integration/tls_test.go
```

This is a repository line-ending hygiene issue, not evidence that the Go test/build gate failed. Formatting is nevertheless recorded as not passing until line-ending policy is addressed in a separately scoped change.

## Deferred Task 16

Task 16 is deferred in full. The repository currently has no:

- isolated Phase 2 PowerShell runner or Compose profile;
- Phase 2 APISIX semantic preflight;
- raw/resource trace schema;
- deterministic Phase 2 aggregation/report command;
- Phase 2 end-to-end smoke result;
- canonical five-repetition result.

Therefore, all of these Phase 2 gates remain unproven:

1. Go 100,000-route median throughput is at least 90% of the Go 1-route baseline.
2. Go 100,000-route median p99 is no more than 110% of the Go 1-route baseline.
3. First/middle/last median-throughput spread is at most 5%.
4. First/middle/last median-p99 spread is at most 10%.
5. End-to-end valid runs have zero unexpected errors.
6. APISIX comparative throughput, p99, CPU, and memory are recorded.

The complete future harness contract remains in [Task 16 of the implementation plan](../superpowers/plans/2026-07-23-phase-2-runtime-snapshot-router-kernel.md#task-16-add-the-isolated-phase-2-end-to-end-benchmark-and-report). The scheduling decision is recorded in the [deferred-benchmark handoff design](../superpowers/specs/2026-07-26-phase-2-deferred-benchmark-handoff-design.md).

## Verification not rerun at this checkpoint

The following intentionally remain pending for a later authorized long-running session:

- five minutes for each of the five fuzz targets;
- final complete Docker race rerun;
- Task 16 implementation and its semantic/smoke verification;
- five-repetition canonical Phase 2 comparison;
- dedicated Linux compile, memory, and performance certification.

An omitted command is not treated as a pass. The [Phase 2 runbook](../operations/phase-2-runbook.md) preserves the exact extended commands.

## Phase 3 handoff

Phase 3 receives these stable contracts:

- canonical resource IDs and resolved upstream references;
- immutable snapshot/request-consistency guarantees;
- shadow build and atomic activation;
- compiled router/plugin pipeline and typed request context;
- fixed boundary between immutable config and mutable upstream runtime state;
- deterministic evidence and dataset conventions.

Phase 3 may design a lifecycle-aware upstream runtime registry, balancing, health, timeout, retry, TLS, WebSocket, and bounded access logging. It must not move mutable endpoint health or connection-pool state into `RuntimeSnapshot`, alter routing precedence, or hide the deferred Phase 2 gates.

## Evidence sources

- [Phase 2 design](../superpowers/specs/2026-07-23-phase-2-runtime-snapshot-router-kernel-design.md)
- [Phase 2 implementation plan](../superpowers/plans/2026-07-23-phase-2-runtime-snapshot-router-kernel.md)
- [Deferred benchmark handoff design](../superpowers/specs/2026-07-26-phase-2-deferred-benchmark-handoff-design.md)
- [Phase 2 operational runbook](../operations/phase-2-runbook.md)
- Dataset/acceptance implementation commit: `181aae2`
- Runtime integration commit: `27813bc`

The result root is Git-ignored by design. No Phase 2 end-to-end raw evidence is present at this checkpoint.

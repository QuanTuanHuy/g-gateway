# Phase 2 Deferred Benchmark Handoff Documentation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close Phase 2 at the honest transitional status `implementation complete; canonical evidence pending`, preserve Task 16 as explicit performance debt, and document the stable boundary for a separate Phase 3 design cycle.

**Architecture:** This is a documentation-only checkpoint. A runbook describes supported operations and separates runnable commands from deferred benchmark commands; an evidence status records only verified results; existing design, roadmap, and repository entry points are updated to reference that status as the authority.

**Tech Stack:** Markdown, Go 1.26.5 verification commands, PowerShell, Docker command documentation, Git.

## Global Constraints

- Task 16 is deferred in full: do not create `bench/run-phase2.ps1`, the Phase 2 report package, report schemas, Compose profile, or partial benchmark artifacts.
- Use the exact status `implementation complete; canonical evidence pending`.
- Do not claim APISIX parity, Phase 2 end-to-end relative scalability success, or production readiness.
- Existing compile/memory and microbenchmark results are provisional development evidence, not substitutes for end-to-end gates.
- Do not run the Task 16 workload, multi-hour canonical comparison, five-minute-per-target fuzz suite, or full Docker race suite in this checkpoint.
- Phase 3 preparation documents stable contracts and unresolved debt only; it does not select balancing, health-check, retry, TLS, or lifecycle algorithms.
- `Gateway.Apply` remains an internal composition API; document no external reload endpoint.
- Preserve Phase 1 benchmark artifacts and behavior unchanged.

---

### Task 1: Create the Phase 2 Operational Runbook and Evidence Status

**Files:**
- Create: `docs/operations/phase-2-runbook.md`
- Create: `docs/benchmarks/phase-2-current-status.md`

**Interfaces:**
- Consumes: Phase 2 design contracts, commits `d19b2e9` through `181aae2`, deterministic dataset checksum `a418eede5fa0e865b0bf39464a8e83a37b69dd4329138e35f2a12048635c154d`, and the approved deferred-benchmark decision.
- Produces: the operational procedure and authoritative human-readable Phase 2 evidence status referenced by README, design audit, and roadmap.

- [ ] **Step 1: Write the runbook scope and safety boundary**

Create `docs/operations/phase-2-runbook.md` with these sections:

```text
# Phase 2 operational runbook
## Scope and safety boundary
## Build and start
## Configuration revisions
## Health, readiness, and telemetry
## Graceful shutdown
## Fast verification
## Extended correctness and resource verification
## Deferred end-to-end benchmark
## Failure response
## Phase 3 boundary
```

State that Phase 2 supports strict `gateway/v1alpha1` compatibility, strict `gateway/v1alpha2`, multiple routes/services/fixed upstream runtimes, immutable snapshot activation, exact/wildcard/hostless hosts, exact/prefix/parameter/catch-all paths, method/header/query predicates, request-id, and header-rewrite.

State these exclusions exactly: no public configuration update endpoint, mutable upstream membership, health checks, load balancing, retries, regex routing, control plane, authentication, rate limiting, WebSocket, or CONNECT.

- [ ] **Step 2: Document runnable startup and operational commands**

Include the checked-in Phase 2 startup:

```powershell
go run ./cmd/test-upstream -listen :8081
go run ./cmd/gateway-dp -config configs/phase2.yaml
```

Explain that `configs/phase2.yaml` uses container DNS endpoint `http://upstream:8080`; operators running both commands directly must copy the config and change the endpoint to `http://127.0.0.1:8081`.

Include:

```powershell
curl.exe --fail http://127.0.0.1:9090/healthz
curl.exe --fail http://127.0.0.1:9090/readyz
curl.exe --fail http://127.0.0.1:9090/metrics
curl.exe --fail -H "Host: api.example.com" http://127.0.0.1:8080/health
```

Document startup validation, listener readiness, JSON logs, shutdown ordering, idle-connection cleanup, and the private-admin-listener requirement.

- [ ] **Step 3: Document revision behavior without inventing a control plane**

State that the process builds revision 1 before binding traffic listeners. Explain the internal `Gateway.Apply(revision, ResourceSet)` behavior: complete shadow validation/build, monotonic revision check, atomic publish, observer notification, and old-snapshot lifetime through held request references.

Explicitly state that Phase 2 exposes no Admin API, file watcher, signal reload, etcd watch, or other external caller for `Apply`; process startup remains the supported configuration entry point.

- [ ] **Step 4: Document fast and extended verification separately**

Put these inexpensive commands in `Fast verification`:

```powershell
gofmt -l .
go vet ./...
go test ./... -count=1
go build ./cmd/...
go test ./internal/router -run TestCompiledRouterMatchesReference -count=1 -v
go test ./internal/runtime -run TestPhase2Acceptance -count=1 -v
```

Explain that the last command uses the normal 10,000-route profile.

Put these commands in `Extended correctness and resource verification`, clearly labeled as documented but not executed at this checkpoint:

```powershell
docker run --rm -v "${PWD}:/src" -w /src golang:1.26.5-bookworm go test ./... -race -count=1
go test ./internal/router -run=^$ -fuzz=FuzzPathPattern -fuzztime=5m
go test ./internal/router -run=^$ -fuzz=FuzzQueryEvaluation -fuzztime=5m
go test ./internal/router -run=^$ -fuzz=FuzzHostNormalization -fuzztime=5m
go test ./internal/router -run=^$ -fuzz=FuzzPredicateCompile -fuzztime=5m
go test ./internal/router -run=^$ -fuzz=FuzzRouterCompileAndMatch -fuzztime=5m
$env:GATEWAY_PHASE2_ACCEPTANCE='1'
go test ./internal/runtime -run TestPhase2Acceptance -count=1 -v
go test ./internal/router -run=^$ -bench BenchmarkRouterScale -benchmem -count=5
Remove-Item Env:GATEWAY_PHASE2_ACCEPTANCE
```

Describe the reference environment: Linux x86_64 container, 8 dedicated logical CPUs, 16 GiB limit, Go 1.26.5, swap disabled, and no competing workload.

- [ ] **Step 5: Document deterministic dataset generation and the deferred benchmark**

Include a runnable dataset command:

```powershell
go run ./cmd/bench-dataset `
  -routes 100000 `
  -seed 20260723 `
  -gateway-out bench/generated/phase2/gateway-100000.yaml `
  -apisix-out bench/generated/phase2/apisix-100000.yaml `
  -metadata-out bench/generated/phase2/dataset-100000.json
```

State that the generated files are inputs, not benchmark evidence.

In `Deferred end-to-end benchmark`, link Task 16 of `docs/superpowers/plans/2026-07-23-phase-2-runtime-snapshot-router-kernel.md` and the approved handoff design. List every missing artifact and gate. Do not show `run-phase2.ps1` as currently runnable because it does not exist.

- [ ] **Step 6: Write the authoritative current-status document**

Create `docs/benchmarks/phase-2-current-status.md` with:

```text
# Phase 2 benchmark and verification — current status
## Executive status
## Implemented capabilities
## Evidence captured
## Provisional resource and microbenchmark results
## Deferred Task 16
## Verification not rerun at this checkpoint
## Phase 3 handoff
## Evidence sources
```

Record:

- branch `codex/phase2-runtime-snapshot-router-kernel-design`;
- handoff decision commit `468552d`;
- status `implementation complete; canonical evidence pending`;
- environment `Docker Desktop, Linux containers, x86_64, 20 logical CPUs, Go 1.26.5`;
- seed `20260723`;
- checksum `a418eede5fa0e865b0bf39464a8e83a37b69dd4329138e35f2a12048635c154d`;
- compile time `503.1312 ms`;
- one-snapshot incremental heap `115,167,144 bytes`;
- 20-swap steady-state retained heap `89,013,496 bytes`;
- static router benchmark result `0 B/op, 0 allocs/op`;
- normal 10,000-route acceptance compile time of approximately `41 ms`.

Label every numeric result above as provisional development evidence from the previously executed acceptance/microbenchmark commands. State that no Phase 2 end-to-end Go/APISIX throughput, p99, sentinel spread, or resource comparison exists.

- [ ] **Step 7: Check Task 1 documents**

Run:

```powershell
$patterns = 'T'+'BD|T'+'ODO|FIX'+'ME|place'+'holder|chưa quyết định'
rg -n $patterns docs/operations/phase-2-runbook.md docs/benchmarks/phase-2-current-status.md
git diff --check
```

Expected: `rg` has no matches and `git diff --check` reports no whitespace errors.

- [ ] **Step 8: Commit the operational and evidence documents**

```powershell
git add docs/operations/phase-2-runbook.md docs/benchmarks/phase-2-current-status.md
git commit -m "docs: record phase 2 transitional status"
```

---

### Task 2: Update Repository Entry Points, Design Audit, and Roadmap

**Files:**
- Modify: `README.md`
- Modify: `docs/superpowers/specs/2026-07-23-phase-2-runtime-snapshot-router-kernel-design.md`
- Modify: `docs/superpowers/specs/2026-07-21-go-native-api-gateway-phase-roadmap-design.md`

**Interfaces:**
- Consumes: the runbook and evidence status from Task 1.
- Produces: consistent repository navigation, an honest Phase 2 audit state, and an explicit Phase 3 design entry boundary.

- [ ] **Step 1: Update README from a Phase 1-only description**

Change the introduction to say Phase 2 core is implemented while canonical Phase 2 performance evidence is pending.

Replace `Phase 1 capabilities` with `Current capabilities` covering:

- immutable revision snapshots and atomic activation;
- multiple routes, services, and fixed upstream runtimes;
- compiled host/path/method/header/query routing;
- request-id and header-rewrite plugins;
- preserved Phase 1 proxy, telemetry, and lifecycle semantics.

Keep a concise `Current exclusions` paragraph containing Phase 3 and later features.

- [ ] **Step 2: Update README repository layout and commands**

Add:

```text
cmd/bench-dataset   deterministic Phase 2 dataset generator
internal/benchdataset deterministic 1/100,000-route fixtures and renderers
internal/plugin     compiled plugin contracts and built-ins
internal/requestctx typed request-scoped route/plugin state
internal/router     deterministic compiled router
internal/runtime    immutable snapshot builder and atomic manager
```

Show `configs/phase2.yaml` as the current configuration example while retaining the Phase 1 compatibility statement. Link:

- `docs/operations/phase-2-runbook.md`;
- `docs/benchmarks/phase-2-current-status.md`;
- the deferred-benchmark handoff design.

State that `bench/README.md` remains the implemented Phase 1 harness guide and that the isolated Phase 2 harness is deferred.

- [ ] **Step 3: Update the Phase 2 design audit**

Change the Phase 2 design status to:

```text
Implementation complete; canonical evidence pending
```

Add a dated audit section after the existing handoff section that:

- links the authoritative current-status document and approved deferred-benchmark design;
- identifies delivery slices 1–6 as implemented;
- identifies Task 16/delivery slice 7 as deferred;
- identifies operations/status documentation as completed by this checkpoint;
- states which original exit criteria are proven and which performance criteria remain pending;
- permits a separate Phase 3 design cycle without treating Phase 2 as production-certified.

Do not delete or weaken the original benchmark design and exit criteria.

- [ ] **Step 4: Update the roadmap checkpoint and next step**

Under Phase 2, add a dated status note with the exact transitional status and links to the evidence status and deferral decision.

Under Phase 3, add an entry note that Phase 3 design may start from the stable contracts but must preserve the deferred Phase 2 debt.

Replace the stale `Bước tiếp theo` statement that names Phase 1 with:

```text
Bước tiếp theo duy nhất là brainstorm Phase 3 — Upstream resilience. Phase 3 phải nhận các contract và performance debt được ghi trong Phase 2 current status; implementation chỉ bắt đầu sau khi Phase 3 design và plan được duyệt.
```

- [ ] **Step 5: Cross-check status language**

Run:

```powershell
rg -n "implementation complete; canonical evidence pending|Implementation complete; canonical evidence pending" README.md docs
rg -n "APISIX parity|production" README.md docs/benchmarks/phase-2-current-status.md docs/superpowers/specs/2026-07-23-phase-2-runtime-snapshot-router-kernel-design.md
git diff --check
```

Expected: the transitional status appears in README, current status, Phase 2 design, and roadmap; parity/production mentions are negated or explicitly pending; no whitespace errors.

- [ ] **Step 6: Commit repository and roadmap updates**

```powershell
git add README.md docs/superpowers/specs/2026-07-23-phase-2-runtime-snapshot-router-kernel-design.md docs/superpowers/specs/2026-07-21-go-native-api-gateway-phase-roadmap-design.md
git commit -m "docs: prepare phase 3 design handoff"
```

---

### Task 3: Run the Documentation Checkpoint Verification

**Files:**
- Verify: `README.md`
- Verify: `docs/operations/phase-2-runbook.md`
- Verify: `docs/benchmarks/phase-2-current-status.md`
- Verify: `docs/superpowers/specs/2026-07-23-phase-2-runtime-snapshot-router-kernel-design.md`
- Verify: `docs/superpowers/specs/2026-07-21-go-native-api-gateway-phase-roadmap-design.md`
- Verify: `docs/superpowers/specs/2026-07-26-phase-2-deferred-benchmark-handoff-design.md`

**Interfaces:**
- Consumes: all documentation changes from Tasks 1–2.
- Produces: a clean, internally consistent Phase 2 documentation checkpoint ready for Phase 3 brainstorming.

- [ ] **Step 1: Verify all local Markdown link targets**

Run a read-only PowerShell link-target check over the six files above. For every Markdown link without a URI scheme or anchor-only target, resolve it relative to the source file and require `Test-Path -LiteralPath` to return true.

Expected: zero missing local targets.

- [ ] **Step 2: Run the inexpensive repository verification**

Run:

```powershell
gofmt -l .
go vet ./...
go test ./... -count=1
go build ./cmd/...
```

Expected: formatting prints nothing and all other commands exit 0. If an unrelated environment limitation prevents a command from running, record the exact failure in the current-status document; do not describe it as passed.

- [ ] **Step 3: Run unfinished-marker and whitespace checks**

Run:

```powershell
$unfinished = @('T'+'BD', 'T'+'ODO', 'FIX'+'ME', 'place'+'holder', 'chưa quyết định') -join '|'
rg -n $unfinished README.md docs bench/README.md --glob '!docs/superpowers/plans/*.md'
git diff --check
git status --short
```

Expected: no unfinished-marker hits in active documentation, no whitespace errors, and no uncommitted files after Task 2.

- [ ] **Step 4: Record verification evidence if outcomes differ**

If Step 2 or Step 3 differs from the expected result, update only `docs/benchmarks/phase-2-current-status.md` with the exact command, date, outcome, and reason. Commit that correction:

```powershell
git add docs/benchmarks/phase-2-current-status.md
git commit -m "docs: correct phase 2 verification evidence"
```

If all results match, make no empty commit.

- [ ] **Step 5: Confirm the Phase 3 brainstorming entry point**

Run:

```powershell
git status --short --branch
git log -5 --oneline
```

Expected: the worktree is clean on `codex/phase2-runtime-snapshot-router-kernel-design`, the documentation commits are visible, and the next action documented by the roadmap is a separate Phase 3 brainstorming cycle.


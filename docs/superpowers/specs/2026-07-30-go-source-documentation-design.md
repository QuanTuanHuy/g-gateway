# Go Source Documentation Design

## 1. Status

This document defines the accepted design for backfilling and enforcing Go
source documentation across G-Gateway. The work is documentation-only except
for the lint configuration, CI wiring, and developer verification instructions
required to keep the documentation complete.

The rollout covers all production Go packages beneath `internal/` and `cmd/`.
Tests, benchmarks, fuzz targets, and test-only helpers remain outside the
documentation-completeness gate.

## 2. Goals

The documentation work must:

1. give every production package one canonical package comment;
2. give every exported production declaration an idiomatic Go doc comment;
3. document caller-visible contracts that Go signatures cannot express;
4. explain only non-obvious implementation invariants, constraints, and
   trade-offs;
5. preserve all runtime behavior and public signatures;
6. make documentation completeness an automated CI requirement; and
7. keep source documentation distinct from architecture, operations, and
   benchmark evidence.

The source comments are written in English, matching the existing identifiers,
errors, README, runbooks, and current implementation specifications.

## 3. Non-goals

This work does not:

- change API signatures, package boundaries, runtime behavior, configuration
  semantics, or error codes;
- refactor an API merely because documenting it reveals awkward naming or
  boundaries;
- comment every unexported declaration or restate self-explanatory code;
- duplicate architecture documents, operational runbooks, benchmark evidence,
  or phase specifications inside Go files;
- require examples for every package or exported declaration;
- add general-purpose lint rules unrelated to source documentation; or
- require doc comments for tests, benchmarks, fuzz targets, or test-only
  helpers.

Ambiguous or structurally weak APIs discovered during the work are recorded for
separate follow-up rather than changed in a documentation commit.

## 4. Documentation contract

### 4.1. Package comments

Every production package has exactly one canonical package comment. The comment
starts with `Package <name>` for libraries and follows the Go command
documentation convention for `package main`.

Non-trivial multi-file packages may use a focused `doc.go`. Small single-file
packages may keep the package comment at the top of their existing production
file. Existing package documentation is retained and improved rather than
duplicated.

A package comment states:

- the package's responsibility;
- its main abstraction or entry point;
- the most important lifecycle or ownership boundary; and
- an important exclusion when that prevents likely misuse.

### 4.2. Exported declarations

Every exported production type, function, method, variable, and constant has a
complete doc comment whose opening sentence names the declaration.

Comments describe caller-visible behavior rather than implementation steps.
Where applicable, they state:

- whether a zero value is usable;
- whether concurrent use is safe;
- initialization and shutdown requirements;
- ownership and release responsibilities;
- mutation, aliasing, and lifetime rules;
- blocking and cancellation behavior;
- stable errors and partial-result behavior;
- validation and normalization rules;
- panic conditions;
- units, defaults, limits, and sentinel values; and
- deterministic ordering or precedence guarantees.

Exported struct fields receive field comments when their meaning, units,
default, sentinel values, ownership, or interaction with other fields is not
clear from the name and type. Each exported constant and variable has its own
name-led doc comment. A group-level comment may add context for a cohesive
declaration block, but it does not replace the declaration comments required by
the documentation gate.

### 4.3. Implementation comments

Unexported implementation comments are added only when they explain:

- why an operation or ordering is required;
- a concurrency, ownership, or lifecycle invariant;
- a security or protocol constraint;
- a non-obvious performance trade-off;
- a compatibility workaround; or
- why apparently simpler code would be incorrect.

Comments that merely narrate the next statement are removed or rewritten.
Implementation details do not appear in API comments unless callers depend on
their complexity or observable semantics.

### 4.4. Examples and links

Executable examples are added only when they are short, deterministic, and
materially clarify how declarations cooperate. Complicated gateway or network
setup is not recreated merely to obtain an example.

Go doc links such as `[Router]` and `[runtime.Manager]` are used when they
improve navigation. Long-form behavior remains in the existing README,
architecture documents, runbooks, and phase specifications; source comments
may point to those documents only when the additional context is necessary.

## 5. Rollout architecture

The rollout follows package dependencies and keeps each review focused on one
domain vocabulary.

### 5.1. Domain and configuration foundations

Packages:

- `internal/model`
- `internal/config`
- `internal/requestctx`

The comments define canonical resource semantics, configuration defaults,
version compatibility conversion, validation, cloning and immutability, and
request-scoped value lifetimes.

### 5.2. Compilation kernels

Packages:

- `internal/router`
- `internal/plugin`

The comments define compile-time validation, deterministic route precedence,
parameter span lifetimes, plugin ordering, allowed mutation, and short-circuit
behavior.

### 5.3. Upstream runtime

Package:

- `internal/upstream`

Because this is the largest and most lifecycle-sensitive package, it is reviewed
in three independently verifiable slices:

1. normalization, fingerprinting, endpoint identity, and balancer selection;
2. health state, active probes, retry budgets, scheduling, and observation; and
3. registry candidate transactions, plans, transport reuse, ownership,
   retirement, reaping, rollback, and shutdown.

The slices are review boundaries, not new package or API boundaries.

### 5.4. Snapshot and request execution

Packages:

- `internal/runtime`
- `internal/proxy`

The comments define immutable snapshot construction, leases, atomic activation,
last-known-good rollback, request streaming ownership, retry eligibility,
deadline propagation, response commitment, and stable error mapping.

### 5.5. Process lifecycle and observability

Packages:

- `internal/telemetry`
- `internal/gateway`

The comments define readiness semantics, fixed metric and label names,
configured route/upstream ID and observed-method request labels, avoidance of
raw request/error labels, listener startup, concurrent use, request draining,
shutdown fallback ordering, and cleanup ownership.

### 5.6. Deterministic tooling

Packages:

- `internal/benchdataset`
- `internal/benchreport`
- `internal/testupstream`

The comments define deterministic inputs and outputs, artifact ownership,
comparison semantics, and test-server lifecycle. The existing
`internal/benchreport` package comment is used as the baseline style.

### 5.7. Commands

Packages:

- `cmd/bench-dataset`
- `cmd/bench-report`
- `cmd/gateway-dp`
- `cmd/test-upstream`

Each command package documents its purpose, primary inputs, observable outputs,
important exit conditions, and resource lifecycle. Command-internal helpers are
documented only when exported and therefore covered by the production-source
gate.

## 6. Review and evidence rules

Comments are derived from tests, call sites, implementation behavior, and the
accepted design documents. A reviewer must check semantic claims against those
sources rather than accept comments based solely on a declaration's name.

A package review rejects:

- comments that only repeat the declaration name;
- claims not supported by implementation or tests;
- duplicated long-form architecture or operations text;
- vague concurrency or ownership language;
- comments that describe an implementation algorithm as an API guarantee
  without caller-visible need; and
- documentation changes mixed with behavior or API changes.

Each package or small package group forms a separate commit. The three upstream
slices form separate commits because they cover distinct contracts and carry a
larger review surface.

## 7. Static analysis and CI

The repository adds a root `staticcheck.conf` that enables only:

```toml
checks = ["ST1000", "ST1020", "ST1021", "ST1022"]
```

The selected checks enforce package comments and the form of existing exported
function, method, type, variable, and constant doc comments. Staticcheck does
not report an exported declaration merely because its comment is absent.

Staticcheck is pinned to release `2026.1`, which supports the project's Go 1.26
toolchain.

The repository also adds `revive.toml` with only Revive's `exported` rule.
Revive is pinned to `v1.15.0` and supplies the missing-declaration coverage that
Staticcheck does not provide. The rule checks exported methods on private
receivers and methods declared by public interfaces, disables unrelated
stuttering advice, and excludes test files. No unrelated Revive or Staticcheck
rule is introduced by this work.

Production source is checked with:

```text
staticcheck -tests=false ./...
revive -set_exit_status -config revive.toml -formatter default ./...
```

During the rollout, each package slice runs both tools for the packages and
files it has completed. The global CI steps are added only after the complete
production baseline passes, so intermediate documentation commits do not leave
the main CI workflow permanently failing.

The README's canonical local verification instructions include installation of
both pinned tools and both full documentation checks.

## 8. Verification strategy

Every rollout slice must:

1. run `gofmt` on changed Go files;
2. run the documentation checks on the completed packages;
3. run `go vet` for the completed packages;
4. run their unit tests;
5. inspect representative `go doc` output for package, type, and function
   rendering; and
6. confirm that the diff contains no behavioral or signature change.

Executable examples, when added, run as part of the package test suite.

The final documentation gate must pass:

```text
test -z "$(gofmt -l .)"
staticcheck -tests=false ./...
revive -set_exit_status -config revive.toml -formatter default ./...
go vet ./...
go test ./... -count=1
go test ./... -race -count=1
go build ./cmd/...
docker build --build-arg COMMAND=gateway-dp -t gateway-go:ci .
```

The existing platform policy remains in force: if a canonical race or Docker
gate cannot run in the development environment, the exact environmental
blocker is recorded without claiming that gate passed. The GitHub Actions
workflow remains the canonical Linux verification.

## 9. Completion criteria

The backfill is complete when:

1. all production packages beneath `internal/` and `cmd/` have one canonical
   package comment;
2. all exported production declarations pass Revive's `exported` rule and all
   existing comments pass `ST1020`, `ST1021`, and `ST1022`;
3. the important caller-visible contracts listed in Section 4 are documented
   wherever applicable;
4. non-exported comments explain only meaningful invariants, constraints, or
   trade-offs;
5. representative `go doc` output is readable and correctly structured;
6. the root Staticcheck and Revive configurations, README instructions, and CI
   gate agree on the exact pinned tools and commands;
7. the full verification pipeline passes or records an explicit
   environment-only blocker for a canonical external gate; and
8. no documentation commit changes runtime behavior or an API signature.

## 10. Implementation planning boundary

The implementation plan will inventory production declarations by package,
split work according to Section 5, identify the exact files and verification
commands for each slice, and end each slice with a reviewable commit. It will
not generate generic comment bodies mechanically. Implementers must derive each
contract from the implementation, tests, and call sites named by the plan.

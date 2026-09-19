# Repository Guidelines

## Codex Working Agreement

- Read the relevant code, nearby tests, and applicable directory instructions before editing. Start with `git status --short` and preserve existing user changes.
- Keep changes focused on the requested outcome. Follow established package boundaries and avoid unrelated refactors, dependency upgrades, or generated artifacts.
- Use `rg` and `rg --files` for targeted discovery. Run commands from the repository root unless a task requires another directory, and use syntax appropriate to the active shell.
- Proceed with routine, reversible work within the request. Ask for clarification only when missing information materially affects correctness or scope.
- Use the user's language for explanations; keep code identifiers, Go documentation, and repository documentation consistent with the existing English style.
- Before finishing, inspect the diff and report what changed, which checks actually ran, their results, and any remaining limitations. Do not claim unrun checks passed.

## Project Structure & Module Organization

G-Gateway is an experimental Go data plane. Keep process wiring thin and place reusable behavior in the appropriate internal package.

- `cmd/gateway-dp`: gateway composition root; `cmd/test-upstream`: deterministic local and integration upstream.
- `cmd/bench-report`, `cmd/bench-dataset`, `internal/benchreport`, and `internal/benchdataset`: benchmark reporting and fixture tooling.
- `internal/config`, `internal/model`: strict configuration decoding, validation, and canonical resources.
- `internal/router`, `internal/plugin`, `internal/requestctx`, `internal/proxy`: route compilation, plugins, request state, and proxy semantics.
- `internal/runtime`, `internal/upstream`: immutable snapshots, leases, balancing, health, retries, transports, and resource cleanup.
- `internal/downstreamtls`, `internal/tlsmaterial`: downstream certificate selection and certificate/trust material handling.
- `internal/websocket`, `internal/tunnel`: classic WebSocket handshake validation and bidirectional tunnel lifecycle.
- `internal/gateway`, `internal/telemetry`, `internal/testupstream`: listeners and shutdown, admin observability, and test endpoints.
- `configs/`: versioned YAML examples; `test/integration/`: black-box tests; `docs/`: architecture, designs, plans, runbooks, and evidence.
- `bench/`: isolated benchmark harness. Do not run the full harness as part of ordinary development unless the task requires benchmark evidence.

Use `README.md` for orientation and `docs/architecture/apache-api-six-architecture-design.md` for the accepted architecture. Read the relevant design under `docs/superpowers/specs/` and runbook under `docs/operations/` when changing a subsystem. Confirm current behavior against code and tests; roadmap items and historical plans are not proof that a feature exists. Keep benchmark and production-readiness claims tied to evidence in `docs/benchmarks/`.

## Build, Test, and Development Commands

Use Go 1.26.5, as pinned in `go.mod` and CI. Run commands from the repository root.

- `go run ./cmd/test-upstream -listen :8081` starts the deterministic local upstream.
- `go run ./cmd/gateway-dp -config configs/local.yaml` runs the gateway with the local example.
- `go test ./... -count=1` runs all unit and integration tests without cached results.
- `go test ./... -race -count=1` checks concurrency behavior.
- `go vet ./...` performs standard Go static analysis.
- `staticcheck -tests=false ./...` and `revive -set_exit_status -config revive.toml -formatter default ./...` enforce documentation rules.
- `go build ./cmd/...` builds every command.
- `docker build --build-arg COMMAND=gateway-dp -t gateway-go:ci .` builds the gateway image used by CI.

The local example requires the certificate and key referenced in `configs/local.yaml`; they are not checked in. Configure valid local TLS material before starting the gateway. Newer phase examples may also require trust files and container-network upstream names; follow the README and relevant runbook.

Install the CI-pinned analyzers when needed:

```text
go install honnef.co/go/tools/cmd/staticcheck@2026.1
go install github.com/mgechev/revive@v1.15.0
```

Run `gofmt -w` on changed Go files, then check `gofmt -l .` for unexpected output. Start with focused package tests during development. Before submitting code changes, run the CI checks above: formatting, both linters, vet, normal and race tests, command builds, and the gateway Docker build. If the environment cannot run a check, report the exact limitation rather than weakening the check. For documentation-only edits, check the diff, referenced paths, and commands; Go tests and image builds are unnecessary unless executable behavior or build inputs changed. Run `git diff --check` before finishing.

## Coding Style & Naming Conventions

Follow idiomatic Go and `gofmt` output (tabs for Go indentation). Use short, lowercase package names and descriptive exported identifiers. Exported packages, types, functions, and methods require comments that begin with the declared name. Keep configuration parsing strict, runtime state immutable where established, and errors explicit rather than silently falling back.

The analyzer configuration intentionally checks Go documentation presence and form. Do not broaden lint policy as part of unrelated work.

## Architecture Constraints

- Build and validate runtime state off the request path, then activate it atomically. Failed updates must preserve the last known good state.
- Preserve snapshot and transport lease ownership across requests, reloads, tunnels, and shutdown. Release resources on success, failure, and cancellation paths.
- Keep routing and balancing deterministic. Preserve bounded resource usage and bounded metric cardinality.
- Preserve streaming, trailers, cancellation, protocol negotiation, and retry replay safety when modifying proxy behavior.
- Keep configuration decoding strict and preserve supported version compatibility. Update validation, examples, tests, and operational documentation together when changing configuration behavior.

## Testing Guidelines

Use the standard `testing` package. Name tests `TestBehavior`, benchmarks `BenchmarkOperation`, and fuzz targets `FuzzInput`. Prefer table-driven tests for policy or routing matrices. Add package-local tests beside implementation files and reserve `test/integration/` for process, protocol, TLS, and snapshot behavior. No numeric coverage threshold is configured; changes must cover important success, failure, and concurrency paths.

## Commit & Pull Request Guidelines

History follows Conventional Commit-style prefixes such as `feat:`, `fix:`, `test:`, `docs:`, `perf:`, `bench:`, and `ci:`. Keep subjects imperative and focused. Pull requests should explain the behavior change, link relevant issues or design documents, list verification commands, and call out configuration or operational impact. Include logs or request/response examples when they clarify externally visible behavior; screenshots are usually unnecessary.

## Security & Configuration

Do not commit private keys, certificates, benchmark results, or profiles. Keep the admin listener private and mount configuration and TLS material read-only in containers.

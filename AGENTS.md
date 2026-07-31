# Repository Guidelines

## Project Structure & Module Organization

Executables live in `cmd/`: `gateway-dp` is the gateway, while `test-upstream` supports local and integration testing. Core behavior is organized by responsibility under `internal/` (`config`, `router`, `proxy`, `runtime`, `upstream`, and `gateway`). Keep process wiring thin and place reusable behavior in the appropriate internal package. Example YAML configurations are in `configs/`; black-box tests are in `test/integration/`; architecture and operational documentation is in `docs/`. The `bench/` tree is isolated benchmark tooling, not part of the development loop.

## Build, Test, and Development Commands

- `go run ./cmd/test-upstream -listen :8081` starts the deterministic local upstream.
- `go run ./cmd/gateway-dp -config configs/local.yaml` runs the gateway with the local example.
- `go test ./... -count=1` runs all unit and integration tests without cached results.
- `go test ./... -race -count=1` checks concurrency behavior.
- `go vet ./...` performs standard Go static analysis.
- `staticcheck -tests=false ./...` and `revive -set_exit_status -config revive.toml -formatter default ./...` enforce documentation rules.
- `go build ./cmd/...` builds every command.

Before submitting, also require `gofmt -w` on changed Go files. CI runs formatting, both linters, vet, normal and race tests, command builds, and the gateway Docker build.

## Coding Style & Naming Conventions

Follow idiomatic Go and `gofmt` output (tabs for Go indentation). Use short, lowercase package names and descriptive exported identifiers. Exported packages, types, functions, and methods require comments that begin with the declared name. Keep configuration parsing strict, runtime state immutable where established, and errors explicit rather than silently falling back.

## Testing Guidelines

Use the standard `testing` package. Name tests `TestBehavior`, benchmarks `BenchmarkOperation`, and fuzz targets `FuzzInput`. Prefer table-driven tests for policy or routing matrices. Add package-local tests beside implementation files and reserve `test/integration/` for process, protocol, TLS, and snapshot behavior. No numeric coverage threshold is configured; changes must cover important success, failure, and concurrency paths.

## Commit & Pull Request Guidelines

History follows Conventional Commit-style prefixes such as `feat:`, `fix:`, `test:`, `docs:`, `perf:`, `bench:`, and `ci:`. Keep subjects imperative and focused. Pull requests should explain the behavior change, link relevant issues or design documents, list verification commands, and call out configuration or operational impact. Include logs or request/response examples when they clarify externally visible behavior; screenshots are usually unnecessary.

## Security & Configuration

Do not commit private keys, certificates, benchmark results, or profiles. Keep the admin listener private and mount configuration and TLS material read-only in containers.

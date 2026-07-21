# Phase 1 Proxy Baseline and Benchmark Harness Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Xây dựng data plane Go tối thiểu nhưng production-shaped, proxy đúng HTTP/1.1 và HTTP/2 downstream tới một HTTP/1.1 upstream, đồng thời tạo benchmark harness black-box có thể so sánh tái lập với APISIX tại commit đã khóa.

**Architecture:** `cmd/gateway-dp` chỉ composition process; `internal/gateway` sở hữu listeners và lifecycle; `internal/config` strict-decode YAML thành `BootstrapConfig` và `model.ResourceSet`; `internal/proxy` thực thi exact-route, normalization và reverse proxy; `internal/upstream` sở hữu endpoint/connection pool; `internal/telemetry` sở hữu health/readiness/metrics. Benchmark chạy hoàn toàn qua Docker Compose và HTTP, không import runtime packages của gateway.

**Tech Stack:** Go 1.26.5, `net/http`, `net/http/httputil`, `log/slog`, `go.yaml.in/yaml/v3 v3.0.4`, `prometheus/client_golang v1.23.2`, Docker Compose, Nginx, APISIX local source commit `0f35b154824ed51d0d4bdadc78d1d5fa9ed1ec62`, wrk, h2load, PowerShell 7.

## Global Constraints

- Design authority: `docs/superpowers/specs/2026-07-22-phase-1-proxy-baseline-benchmark-design.md`.
- Module path: `github.com/QuanTuanHuy/g-gateway`.
- Mọi Go build/test chuẩn dùng Go 1.26.5; máy host không cần nâng Go vì có thể chạy bằng build container.
- Không tạo `shared`, `common`, `utils`, interface một-implementation, plugin seam, dynamic reload, multi-route router, retry, health check hoặc WebSocket.
- Không dùng `io.ReadAll` trong request/response hot path. Chỉ test code và benchmark report parser được phép đọc toàn bộ artifact nhỏ.
- Mỗi task bắt đầu bằng test thất bại, chỉ viết lượng code tối thiểu để xanh, sau đó refactor khi test vẫn xanh.
- Không sửa bất kỳ file nào dưới APISIX source checkout. Harness phải fail trước build nếu commit sai hoặc worktree bẩn.
- Kết quả Docker Desktop luôn có `environment_class: provisional`; không dùng kết quả đó để tự động đổi HTTP engine.
- Trước mỗi commit chạy `gofmt` trên file Go đã chạm và command verification ghi trong task.

## Target File Map

```text
.
├── .github/workflows/ci.yml                 # format, vet, unit, integration, race, build
├── .dockerignore
├── .gitignore
├── Dockerfile                               # pinned gateway/test-upstream/bench-report images
├── README.md                                # Phase 1 local entry points
├── go.mod
├── go.sum
├── cmd/
│   ├── bench-report/main.go                 # benchmark artifact summarizer CLI
│   ├── gateway-dp/main.go                   # config + process composition only
│   └── test-upstream/main.go                # correctness-upstream composition only
├── configs/phase1.yaml                      # canonical static example
├── internal/
│   ├── benchreport/
│   │   ├── report.go
│   │   └── report_test.go
│   ├── config/
│   │   ├── load.go
│   │   ├── load_test.go
│   │   ├── types.go
│   │   └── validate.go
│   ├── gateway/
│   │   ├── gateway.go
│   │   ├── gateway_test.go
│   │   └── response_state.go
│   ├── model/resources.go
│   ├── proxy/
│   │   ├── errors.go
│   │   ├── handler.go
│   │   ├── handler_test.go
│   │   ├── headers.go
│   │   └── headers_test.go
│   ├── telemetry/
│   │   ├── telemetry.go
│   │   └── telemetry_test.go
│   ├── testupstream/
│   │   ├── server.go
│   │   └── server_test.go
│   └── upstream/
│       ├── runtime.go
│       └── runtime_test.go
├── test/integration/
│   ├── gateway_test.go
│   ├── process_test.go
│   └── tls_test.go
├── bench/
│   ├── compose.yaml
│   ├── run.ps1
│   ├── scenarios.yaml
│   ├── README.md
│   ├── apisix/config.yaml
│   ├── certs/generate.ps1
│   ├── generated/.gitkeep
│   ├── h2load/Dockerfile
│   ├── nginx/nginx.conf
│   ├── payloads/.gitkeep
│   ├── schema/{raw-run,summary}.schema.json
│   └── wrk/
│       ├── Dockerfile
│       └── report.lua
└── docs/operations/phase-1-runbook.md
```

---

### Task 1: Bootstrap the module and strict canonical configuration

**Files:**
- Create: `go.mod`
- Create: `.gitignore`
- Create: `internal/model/resources.go`
- Create: `internal/config/types.go`
- Create: `internal/config/load.go`
- Create: `internal/config/validate.go`
- Test: `internal/config/load_test.go`
- Create: `configs/phase1.yaml`

- [ ] **Step 1: Write failing strict-decoding and validation tests**

Cover `TestDecodeValidPhase1Config`, `TestDecodeRejectsUnknownField`, `TestDecodeSeparatesBootstrapFromResources`, and a table `TestDecodeRejectsInvalidConfig` containing wrong API version, route/upstream cardinality, empty/duplicate IDs, unresolved `upstream_ref`, relative path, empty/invalid/duplicate methods, non-HTTP endpoint, multiple endpoints, listener collision, missing TLS file, and non-positive limits/timeouts.

The public contract under test is:

```go
func Decode(r io.Reader) (BootstrapConfig, model.ResourceSet, error)
func Load(path string) (BootstrapConfig, model.ResourceSet, error)
```

Use `t.TempDir()` for TLS-file checks; never depend on machine-specific certificate paths. Config tests prove file presence/readability, while Task 6 proves certificate/key parsing before any listener bind.

- [ ] **Step 2: Run the tests and observe the expected red state**

Run:

```powershell
docker run --rm -v "${PWD}:/src" -w /src golang:1.26.5-bookworm go test ./internal/config -run TestDecode -v
```

Expected: compilation fails because `internal/config` and `internal/model` do not exist.

- [ ] **Step 3: Define dependency-free canonical resource types**

Create `go.mod` first with the exact baseline:

```go
module github.com/QuanTuanHuy/g-gateway

go 1.26.0

toolchain go1.26.5

require (
	github.com/prometheus/client_golang v1.23.2
	go.yaml.in/yaml/v3 v3.0.4
)
```

Implement `internal/model/resources.go` with these concrete value types:

```go
type ResourceSet struct {
	Routes    []Route
	Upstreams []Upstream
}

type Route struct {
	ID          string
	Match       RouteMatch
	UpstreamRef string
}

type RouteMatch struct {
	Path    string
	Methods []string
}

type Upstream struct {
	ID        string
	Endpoints []string
	Transport TransportConfig
}

type TransportConfig struct {
	DialTimeout              time.Duration
	ResponseHeaderTimeout    time.Duration
	IdleConnectionTimeout    time.Duration
	MaxIdleConnections       int
	MaxIdleConnectionsPerHost int
}
```

Do not put YAML tags or file/listener concepts in `model`.

- [ ] **Step 4: Implement strict wire decoding and conversion**

In `types.go`, define private YAML wire structs whose duration fields are strings plus exported runtime types. `TelemetryConfig` has exactly `RequestMetricsEnabled` and `ProfilingEnabled`; both are plain booleans and default false:

```go
type BootstrapConfig struct {
	HTTP      ListenerConfig
	HTTPS     TLSListenerConfig
	Admin     ListenerConfig
	Server    ServerConfig
	Telemetry TelemetryConfig
}
```

In `Decode`, call `yaml.NewDecoder(r)`, enable `KnownFields(true)`, decode exactly one document, reject a second document, parse durations with `time.ParseDuration`, build `BootstrapConfig` and `model.ResourceSet`, then call one private `validate` function. Wrap errors with stable locations such as `decode config`, `server.read_header_timeout`, and `routes[0].upstream_ref`.

- [ ] **Step 5: Implement complete Phase 1 validation**

Validate all rules from design section 5. In addition:

- canonicalize methods to uppercase only after rejecting a non-token value;
- reject a duplicate method after canonicalization;
- parse the endpoint with `url.Parse`, require scheme `http`, non-empty host, empty userinfo/query/fragment, and root-or-empty path;
- compare listener addresses after `net.ResolveTCPAddr` so semantically identical addresses collide;
- require TLS certificate/key paths to exist, be regular files, and be readable; cryptographic pair parsing belongs to `gateway.New` so the startup order remains decode → validate → load key pair → bind.

- [ ] **Step 6: Add the checked-in example config**

Create `configs/phase1.yaml` with the exact canonical shape from the design: ports `8080`, `8443`, `9090`, route `/hello` with `GET`/`POST`, upstream `http://upstream:8080`, and all finite server/transport limits. Add `telemetry.profiling_enabled: false`; this diagnostic-only switch exposes pprof on the admin listener only during a separate profiling rerun and remains off in every measured run.

Initialize `.gitignore` to exclude `/bench/results/`, generated certificates, generated route configs, generated payload bodies, Go binaries, and coverage/profile files while retaining each `.gitkeep` and JSON schema.

- [ ] **Step 7: Verify and commit**

Run:

```powershell
docker run --rm -v "${PWD}:/src" -w /src golang:1.26.5-bookworm sh -lc "gofmt -w internal/model internal/config && go mod tidy && go test ./internal/config -v"
```

Expected: all config tests pass and `go.sum` is created.

Commit:

```powershell
git add go.mod go.sum .gitignore internal/model internal/config configs/phase1.yaml
git commit -m "feat: add strict phase 1 configuration"
```

---

### Task 2: Build the shared HTTP/1.1 upstream runtime

**Files:**
- Create: `internal/upstream/runtime.go`
- Test: `internal/upstream/runtime_test.go`

- [ ] **Step 1: Write failing runtime tests**

Add `TestNewBuildsHTTP1OnlyTransport`, `TestRuntimeReusesConnections`, `TestRuntimeHonorsResponseHeaderTimeout`, `TestRoundTripUsesRequestCancellation`, and `TestCloseIdleConnections`. Use `httptest.NewUnstartedServer` with `ConnState` counters for reuse and a handler that blocks before headers for timeout/cancellation.

- [ ] **Step 2: Run the tests and observe red**

```powershell
docker run --rm -v "${PWD}:/src" -w /src golang:1.26.5-bookworm go test ./internal/upstream -v
```

Expected: compilation fails because `upstream.New` is absent.

- [ ] **Step 3: Implement the concrete runtime**

Implement this API without introducing an interface:

```go
type Runtime struct {
	target    *url.URL
	transport *http.Transport
}

func New(resource model.Upstream) (*Runtime, error)
func (r *Runtime) Target() *url.URL
func (r *Runtime) RoundTripper() http.RoundTripper
func (r *Runtime) CloseIdleConnections()
```

Clone returned URLs so callers cannot mutate runtime state. Configure `DialContext`, response-header timeout, idle timeout, pool sizes, `ForceAttemptHTTP2: false`, and an explicit `http.Protocols` with only HTTP/1 enabled. Do not set an overall client timeout and do not implement redirect/retry behavior.

- [ ] **Step 4: Verify and commit**

```powershell
docker run --rm -v "${PWD}:/src" -w /src golang:1.26.5-bookworm sh -lc "gofmt -w internal/upstream && go test ./internal/upstream -race -v"
git add internal/upstream
git commit -m "feat: add pooled HTTP1 upstream runtime"
```

Expected: all tests pass under the race detector.

---

### Task 3: Implement exact route matching and stable local errors

**Files:**
- Create: `internal/proxy/errors.go`
- Create: `internal/proxy/handler.go`
- Test: `internal/proxy/handler_test.go`

- [ ] **Step 1: Write failing matcher/error tests**

Add tests for exact normalized path, preserved raw query, 404 `ROUTE_NOT_FOUND`, 405 plus sorted `Allow`, 400 `INVALID_REQUEST`, 413 `REQUEST_BODY_TOO_LARGE`, and 501 `UPGRADE_NOT_SUPPORTED`. Assert every error has `Content-Type: application/json`, only `code` and `message`, and no internal error text.

- [ ] **Step 2: Run targeted tests and observe red**

```powershell
docker run --rm -v "${PWD}:/src" -w /src golang:1.26.5-bookworm go test ./internal/proxy -run "Test(Route|Method|Invalid|Body|Upgrade)" -v
```

Expected: compilation fails because `proxy.New` is absent.

- [ ] **Step 3: Implement stable error responses and pre-proxy guards**

Expose only the construction seam needed by `gateway`:

```go
type Options struct {
	Route               model.Route
	Target              *url.URL
	Transport           http.RoundTripper
	MaxRequestBodyBytes int64
	Logger              *slog.Logger
}

func New(opts Options) (http.Handler, error)
```

Internally use:

```go
type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
```

Validate request-target shape before matching, exact-match `URL.Path`, check methods case-sensitively against canonical uppercase config, set `Allow` for a path match with wrong method, reject any non-empty `Upgrade` or `Connection` token naming `upgrade`, and apply `http.MaxBytesReader` without buffering the body.

- [ ] **Step 4: Verify and commit**

```powershell
docker run --rm -v "${PWD}:/src" -w /src golang:1.26.5-bookworm sh -lc "gofmt -w internal/proxy && go test ./internal/proxy -run 'Test(Route|Method|Invalid|Body|Upgrade)' -v"
git add internal/proxy/errors.go internal/proxy/handler.go internal/proxy/handler_test.go
git commit -m "feat: add exact route and stable proxy errors"
```

---

### Task 4: Add reverse-proxy header policy, streaming, cancellation, and upstream errors

**Files:**
- Modify: `internal/proxy/handler.go`
- Create: `internal/proxy/headers.go`
- Create: `internal/proxy/headers_test.go`
- Modify: `internal/proxy/handler_test.go`

- [ ] **Step 1: Write failing forwarding-policy tests**

Test that outbound requests preserve raw query and original Host; discard inbound `Forwarded` and all `X-Forwarded-*`; rebuild `X-Forwarded-For`, `Host`, `Proto`, and `Port` from socket/Host/TLS; remove RFC hop-by-hop headers and all headers named by `Connection` tokens in both directions.

- [ ] **Step 2: Write failing streaming and failure tests**

Add `TestStreamsRequestWithoutWholeBodyBuffer`, `TestFirstResponseChunkArrivesBeforeCompletion`, `TestCancellationReachesUpstream`, `TestForwardsTrailers`, `TestConnectFailureIs502`, `TestResponseHeaderTimeoutIs504`, and `TestUpstreamResetBeforeHeadersIs502`.

Use channels and an `io.Pipe`; do not use timing-only assertions where an event channel can prove ordering. Permit bounded deadlines only to prevent hung tests.

- [ ] **Step 3: Run the proxy suite and observe failures**

```powershell
docker run --rm -v "${PWD}:/src" -w /src golang:1.26.5-bookworm go test ./internal/proxy -v
```

Expected: header, streaming, trailer, cancellation, and error-mapping assertions fail.

- [ ] **Step 4: Configure `httputil.ReverseProxy` with `Rewrite`**

Use `httputil.ReverseProxy.Rewrite`, call `ProxyRequest.SetURL`, restore `Out.Host = In.Host`, and explicitly rebuild forwarding headers from trusted request state. Keep `FlushInterval` at zero so streaming follows standard `ReverseProxy` behavior. Preserve the inbound context on the outbound request.

Implement hop-header stripping as a private function that first parses `Connection` tokens, then removes the standard hop-by-hop set. Apply it to outbound request headers and in `ModifyResponse` for upstream response headers.

- [ ] **Step 5: Map transport failures without leaking internals**

In `ErrorHandler`:

- map `*http.MaxBytesError` to 413;
- map `context.DeadlineExceeded` and `net.Error.Timeout()` occurring before response headers to 504;
- map other connect/reset failures before headers to 502;
- return silently if the downstream context is canceled.

Add a private one-message-per-second limiter around structured upstream error logs so an outage cannot create unbounded log volume. Keep the full error only in logs, never in the JSON body.

- [ ] **Step 6: Verify and commit**

```powershell
docker run --rm -v "${PWD}:/src" -w /src golang:1.26.5-bookworm sh -lc "gofmt -w internal/proxy && go test ./internal/proxy -race -v"
git add internal/proxy
git commit -m "feat: add streaming reverse proxy semantics"
```

Expected: the complete proxy package passes with `-race`.

---

### Task 5: Add readiness, liveness, and bounded Prometheus metrics

**Files:**
- Create: `internal/telemetry/telemetry.go`
- Test: `internal/telemetry/telemetry_test.go`

- [ ] **Step 1: Write failing telemetry tests**

Cover atomic readiness transitions; `/healthz` always 200 while served; `/readyz` returns 503 before ready and after shutdown transition, 200 while ready; `/metrics` includes Go/process collectors; request metrics appear only when enabled; labels are limited to `route_id`, `method`, and `status_class`. Also prove `/debug/pprof/` is absent by default and present only when profiling is explicitly enabled for a diagnostic rerun.

- [ ] **Step 2: Run and observe red**

```powershell
docker run --rm -v "${PWD}:/src" -w /src golang:1.26.5-bookworm go test ./internal/telemetry -v
```

- [ ] **Step 3: Implement concrete telemetry state**

Use a private `prometheus.Registry`, register Go/process collectors, and expose:

```go
type Telemetry struct {
	ready                atomic.Bool
	registry             *prometheus.Registry
	requests             *prometheus.CounterVec
	duration             *prometheus.HistogramVec
	requestMetricsEnabled bool
	adminHandler          http.Handler
}

func New(requestMetricsEnabled, profilingEnabled bool) (*Telemetry, error)
func (t *Telemetry) SetReady(ready bool)
func (t *Telemetry) AdminHandler() http.Handler
func (t *Telemetry) Wrap(next http.Handler, routeID string) http.Handler
```

`Wrap` must become a single direct call when request metrics are disabled. Track request count and duration only; never label by path, host, upstream address, error message, or status code. Register `net/http/pprof` handlers on the private admin mux only when `profilingEnabled` is true; benchmark measurements keep it false and diagnostic reruns enable it solely to capture CPU/heap evidence.

- [ ] **Step 4: Verify and commit**

```powershell
docker run --rm -v "${PWD}:/src" -w /src golang:1.26.5-bookworm sh -lc "gofmt -w internal/telemetry && go mod tidy && go test ./internal/telemetry -race -v"
git add go.mod go.sum internal/telemetry
git commit -m "feat: add gateway health and telemetry"
```

---

### Task 6: Own listeners and graceful lifecycle in the gateway module

**Files:**
- Create: `internal/gateway/gateway.go`
- Create: `internal/gateway/response_state.go`
- Test: `internal/gateway/gateway_test.go`

- [ ] **Step 1: Write failing lifecycle tests**

Add `TestStartBindsAdminBeforeTrafficAndBecomesReady`, `TestStartFailureNeverBecomesReady`, `TestServesHTTP1Cleartext`, `TestServesHTTP1AndHTTP2OverTLS`, `TestShutdownFlipsReadinessBeforeDrain`, `TestShutdownDrainsInFlightRequest`, `TestShutdownDeadlineCancelsRemainingRequests`, and `TestPanicBeforeCommitReturnsStable500`.

Create temp certificates in tests and use `:0` listener addresses. Prove HTTP/2 through `resp.ProtoMajor == 2`, not merely TLS success.

- [ ] **Step 2: Run and observe red**

```powershell
docker run --rm -v "${PWD}:/src" -w /src golang:1.26.5-bookworm go test ./internal/gateway -v
```

- [ ] **Step 3: Implement gateway construction and listener ownership**

Expose:

```go
type Addresses struct {
	HTTP  string
	HTTPS string
	Admin string
}

func New(bootstrap config.BootstrapConfig, resources model.ResourceSet, logger *slog.Logger) (*Gateway, error)
func (g *Gateway) Start() (Addresses, error)
func (g *Gateway) Wait() error
func (g *Gateway) Shutdown(ctx context.Context) error
```

`New` parses the certificate/key with `tls.LoadX509KeyPair`, then builds the concrete upstream runtime, proxy handler, telemetry wrapper, three `http.Server` values, and TLS config with ALPN `h2` and `http/1.1`. Set explicit server `http.Protocols`: HTTP-only listener enables HTTP/1 only; TLS listener enables HTTP/1 and HTTP/2; admin enables HTTP/1 only. A malformed or mismatched key pair must fail `New` before any bind. `Start` binds admin, HTTP, then HTTPS synchronously; if any bind fails, close already-bound listeners, keep readiness false, and return the error. Start serve goroutines only after all binds succeed, then set ready.

- [ ] **Step 4: Implement outer panic/commit state boundary**

`response_state.go` must track whether headers were committed while preserving optional capabilities via `Unwrap() http.ResponseWriter` for `http.NewResponseController`. If panic occurs before commit, emit `INTERNAL_ERROR`; after commit, log and panic with `http.ErrAbortHandler` so the stream terminates without a second status.

- [ ] **Step 5: Implement ordered shutdown**

`Shutdown` must be idempotent and execute: ready false; concurrent traffic-server shutdown; if the deadline expires, call `Close` on both traffic servers so remaining request contexts are canceled; close idle upstream connections; admin-server shutdown. Join independent errors with `errors.Join`. The caller supplies the design-configured timeout context. `Wait` returns unexpected serve errors but treats `http.ErrServerClosed` as normal.

- [ ] **Step 6: Verify and commit**

```powershell
docker run --rm -v "${PWD}:/src" -w /src golang:1.26.5-bookworm sh -lc "gofmt -w internal/gateway && go test ./internal/gateway -race -v"
git add internal/gateway
git commit -m "feat: add gateway listeners and graceful lifecycle"
```

---

### Task 7: Add the programmable correctness upstream and end-to-end protocol suite

**Files:**
- Create: `internal/testupstream/server.go`
- Test: `internal/testupstream/server_test.go`
- Create: `test/integration/gateway_test.go`
- Create: `test/integration/tls_test.go`

- [ ] **Step 1: Write failing correctness-upstream tests**

Test `/fixed/{bytes}`, `/echo`, `/headers`, `/stream`, `/delay/{duration}`, `/trailers`, and `/close`. Expose connection counters and cancellation observations through `/debug/state`; reset them through `POST /debug/reset`. Reject invalid byte counts/durations with 400 and cap fixed bodies at 64 KiB.

- [ ] **Step 2: Implement the test-upstream handler**

Expose one concrete constructor:

```go
func New(logger *slog.Logger) http.Handler
```

Use deterministic payload byte `x`, `http.Flusher` for streaming, declared trailers, request context observation, and `http.Hijacker` only for `/close`. Keep all behavior inside `internal/testupstream`; the later command remains composition-only.

- [ ] **Step 3: Write the end-to-end integration tests**

Start test-upstream and `gateway.Gateway` in-process through their production constructors. Cover the full design matrix: HTTP/1.1 clear, HTTP/1.1 TLS, HTTP/2 TLS/ALPN, streamed request and response, first chunk ordering, cancellation, headers, trailers, connect failure, upstream close, response-header timeout, keepalive reuse, readiness, graceful drain, body limit, and unsupported Upgrade.

- [ ] **Step 4: Run integration tests and fix only cross-module defects**

```powershell
docker run --rm -v "${PWD}:/src" -w /src golang:1.26.5-bookworm go test ./internal/testupstream ./test/integration -race -v
```

Expected: all correctness and protocol tests pass. If a defect belongs to an earlier package, add a focused regression test to that package before changing it.

- [ ] **Step 5: Commit**

```powershell
git add internal/testupstream test/integration internal/proxy internal/gateway internal/upstream
git commit -m "test: add proxy protocol integration suite"
```

---

### Task 8: Add composition roots, container build, and continuous integration

**Files:**
- Create: `cmd/gateway-dp/main.go`
- Create: `cmd/test-upstream/main.go`
- Create: `Dockerfile`
- Create: `.dockerignore`
- Create: `.github/workflows/ci.yml`
- Create: `README.md`
- Create: `test/integration/process_test.go`

- [ ] **Step 1: Add failing command-level tests through subprocesses**

In `process_test.go`, build `./cmd/gateway-dp` into `t.TempDir()`, run it with an invalid config, and assert non-zero exit plus structured startup error. Run a valid config, wait for `/readyz`, hold one `/stream` request open, send SIGTERM on Linux, assert `/readyz` becomes 503 while the admin listener remains available, release the stream, then assert clean exit. Gate the signal test with `runtime.GOOS == "linux"`; CI is the canonical execution environment.

- [ ] **Step 2: Implement thin composition roots**

`gateway-dp` accepts only `-config`; it creates a JSON `slog` logger, loads config, constructs gateway, starts, creates `signal.NotifyContext` for SIGINT/SIGTERM, and calls `Shutdown` with `bootstrap.Server.ShutdownTimeout`. It exits non-zero on load/start/unexpected serve/shutdown failure.

`test-upstream` accepts only `-listen` (default `:8080`), constructs `testupstream.New`, and uses finite server timeouts with signal-driven graceful shutdown. Neither command contains routing or proxy behavior.

- [ ] **Step 3: Add a pinned multi-stage image**

Use `golang:1.26.5-bookworm` as builder. Define build arg `COMMAND=gateway-dp`, build `./cmd/${COMMAND}` with `CGO_ENABLED=0`, and copy into `gcr.io/distroless/static-debian12:nonroot`. The same Dockerfile must build `gateway-dp`, `test-upstream`, and `bench-report` later.

- [ ] **Step 4: Add minimal CI**

Use `actions/checkout@v6` and `actions/setup-go@v6` with `go-version: 1.26.5`, cache keyed by `go.sum`, and `permissions: contents: read`. Required steps:

```bash
test -z "$(gofmt -l .)"
go vet ./...
go test ./... -count=1
go test ./... -race -count=1
go build ./cmd/...
docker build --build-arg COMMAND=gateway-dp -t gateway-go:ci .
```

Do not run APISIX comparison in PR CI; it is manual and resource-sensitive.

- [ ] **Step 5: Verify binaries and image**

```powershell
docker run --rm -v "${PWD}:/src" -w /src golang:1.26.5-bookworm sh -lc "gofmt -w cmd internal test && go vet ./... && go test ./... -race -count=1 && go build ./cmd/..."
docker build --build-arg COMMAND=gateway-dp -t g-gateway:phase1 .
```

Expected: all checks pass and the image builds as non-root.

- [ ] **Step 6: Commit**

```powershell
git add cmd Dockerfile .dockerignore .github README.md internal/gateway
git commit -m "build: add gateway commands container and CI"
```

---

### Task 9: Create the isolated Docker benchmark topology and source guard

**Files:**
- Create: `bench/compose.yaml`
- Create: `bench/apisix/config.yaml`
- Create: `bench/nginx/nginx.conf`
- Create: `bench/certs/generate.ps1`
- Create: `bench/scenarios.yaml`
- Create: `bench/generated/.gitkeep`
- Create: `bench/payloads/.gitkeep`
- Modify: `.gitignore`
- Create: `bench/run.ps1`

- [ ] **Step 1: Add the harness preflight first**

Implement the parameter block for:

```powershell
param(
  [ValidateSet('smoke','compare')] [string] $Mode = 'smoke',
  [ValidateSet('go','apisix','all')] [string] $Target = 'all',
  [string] $Scenario = 'all',
  [string] $ApisixSource = 'D:\User2\open_source\apisix',
  [string] $ResultsDir = ''
)
```

Before any Docker call, resolve the source path, assert `git rev-parse HEAD` equals `0f35b154824ed51d0d4bdadc78d1d5fa9ed1ec62`, and assert `git status --porcelain` is empty. Return a non-zero exit with an actionable message on mismatch. Add `-PreflightOnly` for deterministic verification without starting Docker.

- [ ] **Step 2: Verify both guard branches**

Run against the real clean checkout:

```powershell
pwsh bench/run.ps1 -PreflightOnly -ApisixSource D:\User2\open_source\apisix
```

Expected: prints the pinned commit and exits zero.

Run against a temporary Git repository initialized at a different commit. Expected: exits non-zero before any Docker output and names the expected/actual commits.

- [ ] **Step 3: Define versioned scenarios**

`bench/scenarios.yaml` must contain five named scenarios: `h1-clear`, `h1-tls`, `h1-crosscheck-clear`, `h1-crosscheck-tls`, `h2-tls`. Store generator, protocol/TLS, payload list, default threads/connections/clients/streams, warm-up, run duration, repetitions, and the 125% upstream-headroom factor. Use 3-second warm-up/one 10-second run for smoke and 15-second warm-up/five 60-second runs for compare.

- [ ] **Step 4: Define one Compose project**

Create services `gateway-go`, `apisix`, `upstream-correctness`, `upstream-performance`, `wrk`, and `h2load`. `gateway-go` and `apisix` use mutually exclusive profiles and the same `gateway` network alias. Give both gateway targets identical CPU/memory limits, worker count/GOMAXPROCS, certificates, network, disabled access logging, and disabled per-request metrics.

APISIX build must use the local checkout directly:

```yaml
build:
  context: ${APISIX_SOURCE}
  dockerfile: docker/debian-dev/Dockerfile
  args:
    CODE_PATH: .
    ENTRYPOINT_PATH: ./docker/debian-dev/docker-entrypoint.sh
    INSTALL_BROTLI: ./docker/debian-dev/install-brotli.sh
environment:
  APISIX_STAND_ALONE: "true"
```

Mount the generated standalone route file at `/usr/local/apisix/conf/apisix.yaml` and static deployment config at `/usr/local/apisix/conf/config.yaml`; do not add etcd or Admin API.

- [ ] **Step 5: Generate payloads, certificates, and per-target route config**

The Nginx config serves deterministic 0, 1024, 16384, and 65536 byte files with access logs off. Generate payload files via a short container command, not checked-in binaries. `generate.ps1` uses an OpenSSL container to create one local CA-independent certificate with SANs `gateway` and `localhost`; generated key/cert are ignored.

For every payload restart the active target with generated configs whose exact route is one of `/bytes/0`, `/bytes/1024`, `/bytes/16384`, or `/bytes/65536`. APISIX route files end with `#END`. Go configs reference the same upstream URL and route path. Never rewrite paths inside either target.

- [ ] **Step 6: Bring up each target and perform a black-box smoke request**

```powershell
pwsh bench/run.ps1 -Mode smoke -Target go -Scenario h1-clear
pwsh bench/run.ps1 -Mode smoke -Target apisix -Scenario h1-clear -ApisixSource D:\User2\open_source\apisix
```

Expected at this task boundary: both targets build, become ready, proxy the 1 KiB body, and shut down. Generator result aggregation is added in the next tasks.

- [ ] **Step 7: Commit**

```powershell
git add bench .gitignore
git commit -m "bench: add isolated Go and APISIX topology"
```

---

### Task 10: Add pinned wrk/h2load runners and raw artifact contracts

**Files:**
- Create: `bench/wrk/Dockerfile`
- Create: `bench/wrk/report.lua`
- Create: `bench/h2load/Dockerfile`
- Create: `bench/schema/raw-run.schema.json`
- Modify: `bench/compose.yaml`
- Modify: `bench/run.ps1`

- [ ] **Step 1: Define the raw-run schema before runner code**

Require: schema version, timestamp, environment class, git commit, target name/version, APISIX commit, image IDs, scenario, payload bytes, effective generator settings, target container limits, Docker/OS/CPU metadata, direct-control measurements, raw artifact paths, requests/sec, transfer/sec, p50/p95/p99 microseconds, request errors, timeouts, and non-2xx count. Disallow unknown top-level fields.

- [ ] **Step 2: Build pinned generator images**

Build wrk `4.2.0` from commit `a211dd5a7050b1f9e8a9870b95513060e72ac4a0` and h2load from nghttp2 tag `v1.69.0`; record those pins as image labels and in raw metadata. Use `nginx:1.31.3-alpine` for the performance upstream. Do not use floating `latest` tags. Images run as non-root and write only to the mounted results directory.

- [ ] **Step 3: Emit machine-readable wrk output**

`report.lua` must run with latency collection enabled and write a single JSON object containing request count, bytes, duration, socket/read/write/timeout errors, non-2xx count, and p50/p95/p99 latency. Preserve the normal wrk stdout in a sibling `.log` file.

- [ ] **Step 4: Preserve h2load request-level evidence**

Invoke h2load with `--log-file` and `--output-file`, plus `--h1` for cross-check scenarios. Store raw log, structured output, and stderr/stdout separately. Treat any request failure, timeout, or non-2xx as a failed measurement.

- [ ] **Step 5: Orchestrate direct control and alternating target order**

For each scenario/payload: run direct Nginx control first; invalidate the scenario unless direct throughput is at least 125% of the faster gateway result; warm active target; run measurements; stop it; run the other target. Alternate first target by repetition index. Ensure only one service owns network alias `gateway` at a time.

- [ ] **Step 6: Verify a smoke artifact set and commit**

```powershell
pwsh bench/run.ps1 -Mode smoke -Target all -Scenario h1-clear -ApisixSource D:\User2\open_source\apisix
```

Expected: timestamped directory contains metadata, direct-control raw output, Go/APISIX raw outputs, generator logs, and no missing required schema fields.

```powershell
git add bench
git commit -m "bench: add pinned load generators and raw artifacts"
```

---

### Task 11: Implement deterministic benchmark summaries and parity classification

**Files:**
- Create: `internal/benchreport/report.go`
- Test: `internal/benchreport/report_test.go`
- Create: `cmd/bench-report/main.go`
- Create: `bench/schema/summary.schema.json`
- Modify: `bench/run.ps1`

- [ ] **Step 1: Write failing report tests with checked-in inline fixtures**

Cover wrk percentile parsing, h2load request-log percentile calculation, median across five runs, direct-headroom invalidation, gateway error invalidation, Go-vs-APISIX throughput/p99/error classification, missing/corrupt artifacts, and forced `environment_class: provisional` on Docker Desktop.

The report API is:

```go
type Options struct {
	InputDir  string
	OutputDir string
}

func Generate(opts Options) (Summary, error)
```

- [ ] **Step 2: Run and observe red**

```powershell
docker run --rm -v "${PWD}:/src" -w /src golang:1.26.5-bookworm go test ./internal/benchreport -v
```

- [ ] **Step 3: Implement deterministic aggregation**

Read raw artifacts, sort by scenario/payload/target/repetition, compute exact nearest-rank p50/p95/p99 from h2load request records, median throughput and p99 across valid repetitions, and classify:

- `invalid` when direct headroom is below 1.25 or any required artifact/error condition fails;
- `provisional_pass` when Go median throughput is at least APISIX, Go p99 is at most 110% of APISIX, and Go error rate is no higher;
- `provisional_miss` otherwise.

Generate `summary.json`, `summary.csv`, and `summary.md` from the same in-memory `Summary`. Never turn `provisional_miss` into a non-zero process exit; invalid/corrupt evidence does exit non-zero.

Assert with `go list -deps ./cmd/bench-report` that the report command imports none of `internal/gateway`, `internal/config`, `internal/model`, `internal/proxy`, `internal/upstream`, `internal/telemetry`, or `internal/testupstream`; this preserves the benchmark's black-box boundary.

- [ ] **Step 4: Wire the CLI and image target**

`cmd/bench-report` accepts `-input` and `-output`, calls `benchreport.Generate`, and prints the three artifact paths. Build it with the shared Dockerfile `COMMAND=bench-report`; `run.ps1` invokes it after target measurements.

- [ ] **Step 5: Verify and commit**

```powershell
docker run --rm -v "${PWD}:/src" -w /src golang:1.26.5-bookworm sh -lc "gofmt -w internal/benchreport cmd/bench-report && go test ./internal/benchreport -race -v && go build ./cmd/bench-report"
git add internal/benchreport cmd/bench-report bench/schema/summary.schema.json bench/run.ps1
git commit -m "bench: add deterministic comparison reports"
```

---

### Task 12: Complete the benchmark matrix, operational runbook, and Phase 1 acceptance

**Files:**
- Modify: `bench/run.ps1`
- Create: `bench/README.md`
- Create: `docs/operations/phase-1-runbook.md`
- Modify: `README.md`
- Modify: `docs/superpowers/specs/2026-07-22-phase-1-proxy-baseline-benchmark-design.md`

- [ ] **Step 1: Document exact operator flows**

Document prerequisites, Docker Desktop resource settings, source-guard behavior, certificate generation, smoke/compare syntax, result directory layout, interpretation of invalid/provisional results, cleanup, common failures, and how to preserve profiles when parity misses. State explicitly that official parity requires dedicated Linux in Phase 7.

- [ ] **Step 2: Run repository-wide static and correctness gates**

```powershell
docker run --rm -v "${PWD}:/src" -w /src golang:1.26.5-bookworm sh -lc 'test -z "$(gofmt -l .)" && go vet ./... && go test ./... -count=1 && go test ./... -race -count=1 && go build ./cmd/...'
docker build --build-arg COMMAND=gateway-dp -t g-gateway:phase1 .
docker build --build-arg COMMAND=test-upstream -t g-gateway-test-upstream:phase1 .
docker build --build-arg COMMAND=bench-report -t g-gateway-bench-report:phase1 .
```

Expected: every command exits zero.

- [ ] **Step 3: Run all smoke scenarios**

```powershell
pwsh bench/run.ps1 -Mode smoke -Target all -Scenario all -ApisixSource D:\User2\open_source\apisix
```

Expected: all five protocol scenarios and all declared payloads complete with zero gateway errors; direct-control headroom and complete artifacts are recorded.

- [ ] **Step 4: Run the canonical compare suite**

```powershell
pwsh bench/run.ps1 -Mode compare -Target all -Scenario all -ApisixSource D:\User2\open_source\apisix
```

Expected: five 60-second repetitions per scenario/payload, 15-second warm-ups, complete raw artifacts, valid summaries, and `environment_class: provisional`. On `provisional_miss`, rerun the worst missed Go scenario outside the comparison sample with `profiling_enabled: true`, drive the same load while collecting a 30-second `/debug/pprof/profile`, then collect `/debug/pprof/heap`; retain those profiles plus Docker CPU/memory traces for both targets. The diagnostic rerun never replaces measured results and the miss does not fail Phase 1 by itself.

- [ ] **Step 5: Audit design-spec coverage**

Cross-check every item in design sections 18–20 against a passing test, artifact, command, or documented deliberate exclusion. Update the design status to `Implemented; provisional benchmark captured` only if every blocking gate passes. Leave status unchanged and record the exact failed gate otherwise.

- [ ] **Step 6: Commit the acceptance evidence and documentation**

Do not commit bulk raw benchmark results. Commit only the runbook, schemas, a small redacted example summary, and the design status update; archive the full timestamped result directory outside Git or attach it to the future PR/release evidence.

```powershell
git add README.md bench/README.md bench/schema docs/operations docs/superpowers/specs/2026-07-22-phase-1-proxy-baseline-benchmark-design.md
git commit -m "docs: record phase 1 verification workflow"
```

## Final Acceptance Checklist

- [ ] Exact-route HTTP/1.1 clear, HTTP/1.1 TLS, and HTTP/2 TLS proxy tests pass.
- [ ] Request/response streaming, trailers, cancellation, body limits, timeout/error semantics, and connection reuse pass under `-race`.
- [ ] Invalid config never binds traffic or becomes ready.
- [ ] Shutdown flips readiness, rejects new work, drains active work, then closes upstream/admin resources.
- [ ] Gateway/test-upstream/bench-report build from the pinned Go 1.26.5 image.
- [ ] APISIX source guard proves exact clean commit before build and standalone mode uses no etcd/Admin API.
- [ ] Direct-upstream headroom is at least 125% for every valid comparison.
- [ ] Smoke and compare suites retain raw evidence and deterministic summaries.
- [ ] Docker Desktop comparison is visibly provisional; parity miss does not trigger an HTTP-engine decision.
- [ ] Repository CI, full tests, race tests, vet, formatting, command builds, and container builds pass.

## Execution Handoff

After this plan is reviewed, choose one execution mode:

1. **Subagent-Driven (recommended):** execute one task at a time in this session with a fresh implementation worker and review checkpoint after each task.
2. **Inline Execution:** execute sequentially in the current task with `superpowers:executing-plans`, stopping at the written checkpoints.

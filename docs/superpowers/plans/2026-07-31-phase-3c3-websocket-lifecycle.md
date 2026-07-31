# Phase 3C3 WebSocket Lifecycle Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add opt-in RFC 6455 WebSocket proxying with Route/Service inheritance, retry-aware HTTP/1.1 handshakes, snapshot-independent opaque tunnels, idle timeout, reload safety, graceful drain, and bounded telemetry.

**Architecture:** Keep `httputil.ReverseProxy` for ordinary HTTP, extract its attempt loop into a shared executor, and route enabled HTTP/1.1 Upgrade candidates through a dedicated Upgrade Executor. After a valid upstream `101`, acquire a transport-generation `TunnelLease`, register an asynchronous opaque stream session, release the runtime snapshot, and let the Gateway-owned tunnel registry control idle timeout and shutdown.

**Tech Stack:** Go 1.26.5 standard library (`net/http`, `crypto/tls`, `bufio`, `io`, `sync`, `context`), existing Prometheus client, existing strict YAML v3 loader, standard `testing`/fuzz/benchmark tooling; no new WebSocket dependency.

## Global Constraints

- Implement only classic RFC 6455 over downstream and upstream HTTP/1.1; RFC 8441 Extended CONNECT is outside this phase.
- Add strict `gateway/v1alpha6`; v1alpha1 through v1alpha5 normalize to WebSocket disabled.
- Public policy is `websocket.enabled` plus `websocket.idle_timeout` on Route and Service, with per-field Route-over-Service inheritance.
- Effective defaults are `enabled: false` and `idle_timeout: 60s`; explicit `idle_timeout: 0` disables expiration.
- Enabled Routes may use upstream `protocol: auto` or `http1`; strict `http2` must fail snapshot compilation.
- Request/response plugins execute before commitment, but handshake-control semantics may not be changed by plugins.
- Retry, health, TLS, budget, and total timeout apply only before downstream `101`; no retry or health mutation occurs after commitment.
- Established tunnels must release the complete snapshot and retain only the selected upstream transport generation.
- Config apply never closes an established tunnel; Gateway shutdown closes admission, drains until its deadline, then force-closes.
- Tunnel copying is frame-transparent, uses two pooled 32 KiB buffers, has no payload queue, and performs best-effort `CloseWrite` propagation.
- Do not add frame parsing, ping/pong generation, close-frame generation, reconnect, admission quotas, access logs, dynamic SNI selection, or APISIX comparison.
- New WebSocket metrics use only the exact bounded labels defined in the design; never label with Route, upstream, host, endpoint, client, certificate, or revision.
- Never log payload, WebSocket key, cookie, authorization, host, endpoint URL, client address, raw certificate data, or arbitrary peer error text.
- Preserve all existing Phase 1 through Phase 3C1 behavior and benchmark gates.
- On Windows, set `$env:GOCACHE = Join-Path (Get-Location) '.cache\go-build'` before Go commands. Record race as pending when `CGO_ENABLED=0`; do not claim it passed.
- Design authority: `docs/superpowers/specs/2026-07-31-phase-3c3-websocket-lifecycle-design.md`.

## File Structure

- `internal/model/resources.go` — canonical override/effective WebSocket policy and deep cloning.
- `internal/config/wire_v1alpha6.go` — strict v1alpha6 wire document and presence-aware conversion.
- `internal/config/load.go`, `internal/config/validate.go` — version dispatch and compatibility validation.
- `internal/runtime/builder.go`, `internal/runtime/snapshot.go`, `internal/runtime/validate.go` — effective policy compilation and strict-H2 rejection.
- `internal/websocket/handshake.go` — pure candidate, request, header, accept, subprotocol, extension, and response validation.
- `internal/tunnel/session.go` — opaque bidirectional copy, activity tracking, idle timer, half-close, and terminal result.
- `internal/tunnel/registry.go` — pending/active registration, admission gate, asynchronous ownership, drain, and force-close.
- `internal/downstreamtls/provider.go` — static `tls.Config.GetCertificate` provider seam for Phase 3C2.
- `internal/upstream/transport.go`, `internal/upstream/plan.go`, `internal/upstream/registry.go` — H1 upgrade RoundTrip and transport-generation `TunnelLease`.
- `internal/proxy/attempt.go`, `internal/proxy/route_transport.go` — shared retry/selection/health attempt executor and ordinary adapter.
- `internal/proxy/websocket.go`, `internal/proxy/response_forward.go`, `internal/proxy/handler.go` — Upgrade Executor, non-101 response terminal, and request dispatch.
- `internal/telemetry/websocket.go`, `internal/telemetry/telemetry.go` — exact WebSocket metric families and handshake status accounting.
- `internal/gateway/gateway.go` — provider/registry wiring and readiness-first tunnel shutdown ordering.
- `internal/testupstream/server.go`, `test/integration/websocket_test.go` — deterministic process-level raw echo upstream and black-box WebSocket tests.
- `internal/proxy/phase3c3_acceptance_test.go` — direct versus Gateway relative performance gates.
- `configs/phase3c3.yaml`, `docs/operations/phase-3c3-runbook.md`, `docs/benchmarks/phase-3c3-current-status.md`, `README.md`, and the phase roadmap — runnable contract, operations, evidence, and handoff.

---

### Task 1: Add Canonical WebSocket Policy Types

**Files:**
- Modify: `internal/model/resources.go`
- Modify: `internal/model/resources_test.go`

**Interfaces:**
- Consumes: existing `Route`, `Service`, and `CloneResourceSet` ownership rules.
- Produces: `model.WebSocketPolicyOverride`, `model.WebSocketPolicy`, `model.DefaultWebSocketIdleTimeout`, and `Route.WebSocket`/`Service.WebSocket` fields.

- [ ] **Step 1: Write failing clone and presence tests**

Add `TestCloneResourceSetClonesWebSocketOverrides` and `TestDefaultWebSocketIdleTimeout`:

```go
func TestCloneResourceSetClonesWebSocketOverrides(t *testing.T) {
	enabled := true
	idle := 5 * time.Minute
	in := ResourceSet{
		Routes: []Route{{ID: "events", WebSocket: WebSocketPolicyOverride{Enabled: &enabled}}},
		Services: []Service{{ID: "realtime", WebSocket: WebSocketPolicyOverride{IdleTimeout: &idle}}},
	}
	got := CloneResourceSet(in)
	*in.Routes[0].WebSocket.Enabled = false
	*in.Services[0].WebSocket.IdleTimeout = time.Second
	if got.Routes[0].WebSocket.Enabled == nil || !*got.Routes[0].WebSocket.Enabled {
		t.Fatal("route enabled override was not cloned")
	}
	if got.Services[0].WebSocket.IdleTimeout == nil || *got.Services[0].WebSocket.IdleTimeout != 5*time.Minute {
		t.Fatal("service idle override was not cloned")
	}
}

func TestDefaultWebSocketIdleTimeout(t *testing.T) {
	if DefaultWebSocketIdleTimeout != 60*time.Second {
		t.Fatalf("default idle timeout=%s", DefaultWebSocketIdleTimeout)
	}
}
```

- [ ] **Step 2: Run the model tests and confirm RED**

Run: `go test ./internal/model -run 'Test(CloneResourceSetClonesWebSocketOverrides|DefaultWebSocketIdleTimeout)' -count=1 -v`

Expected: compile failure because the WebSocket policy types and fields do not exist.

- [ ] **Step 3: Add the canonical types and deep clone helper**

Add:

```go
const DefaultWebSocketIdleTimeout = 60 * time.Second

type WebSocketPolicyOverride struct {
	Enabled     *bool
	IdleTimeout *time.Duration
}

type WebSocketPolicy struct {
	Enabled     bool
	IdleTimeout time.Duration
}
```

Add `WebSocket WebSocketPolicyOverride` to `Route` and `Service`. Add `cloneWebSocketPolicyOverride` that allocates new pointed-to values, and call it from both clone loops.

- [ ] **Step 4: Run package tests and format**

Run: `gofmt -w internal/model/resources.go internal/model/resources_test.go`

Run: `go test ./internal/model -count=1`

Expected: PASS.

- [ ] **Step 5: Commit the canonical policy**

```powershell
git add internal/model/resources.go internal/model/resources_test.go
git commit -m "feat: model websocket policy inheritance"
```

### Task 2: Decode Strict gateway/v1alpha6

**Files:**
- Create: `internal/config/wire_v1alpha6.go`
- Modify: `internal/config/load.go`
- Modify: `internal/config/validate.go`
- Modify: `internal/config/load_test.go`

**Interfaces:**
- Consumes: Task 1 `model.WebSocketPolicyOverride`; v1alpha5 conversion for all existing fields.
- Produces: `apiVersionV1Alpha6`, `documentV6`, `convertV6`, and strict compatibility behavior.

- [ ] **Step 1: Add failing decode, inheritance-presence, and compatibility tests**

Add table tests covering a v1alpha6 Route explicit `false`, Service `true`, explicit zero, absent fields, unknown `websocket` field, negative duration, and v1alpha5 disabled normalization. The core assertion must include:

```go
if resources.Services[0].WebSocket.Enabled == nil || !*resources.Services[0].WebSocket.Enabled {
	t.Fatal("service websocket enabled presence was lost")
}
if resources.Routes[0].WebSocket.Enabled == nil || *resources.Routes[0].WebSocket.Enabled {
	t.Fatal("route explicit false was lost")
}
if resources.Routes[0].WebSocket.IdleTimeout == nil || *resources.Routes[0].WebSocket.IdleTimeout != 0 {
	t.Fatal("route explicit zero idle timeout was lost")
}
```

- [ ] **Step 2: Confirm v1alpha6 is unsupported**

Run: `go test ./internal/config -run 'TestV1Alpha6|TestOlderVersionsDisableWebSocket' -count=1 -v`

Expected: FAIL with `api_version: unsupported "gateway/v1alpha6"`.

- [ ] **Step 3: Implement the strict v1alpha6 wire converter**

Define presence-aware documents:

```go
type websocketDocumentV6 struct {
	Enabled     *bool   `yaml:"enabled"`
	IdleTimeout *string `yaml:"idle_timeout"`
}

type routeDocumentV6 struct {
	routeDocumentV4 `yaml:",inline"`
	WebSocket websocketDocumentV6 `yaml:"websocket"`
}

type serviceDocumentV6 struct {
	serviceDocumentV2 `yaml:",inline"`
	WebSocket websocketDocumentV6 `yaml:"websocket"`
}
```

`convertV6` must project existing fields into `documentV5`, call `convertV5`, then assign converted overrides by matching slice position. Use this exact converter for each policy:

```go
func convertWebSocketV6(field string, wire websocketDocumentV6) (model.WebSocketPolicyOverride, error) {
	out := model.WebSocketPolicyOverride{Enabled: wire.Enabled}
	if wire.IdleTimeout == nil {
		return out, nil
	}
	duration, err := parseDuration(field+".idle_timeout", *wire.IdleTimeout)
	if err != nil {
		return model.WebSocketPolicyOverride{}, err
	}
	if duration < 0 {
		return model.WebSocketPolicyOverride{}, fmt.Errorf("%s.idle_timeout: must be non-negative", field)
	}
	out.IdleTimeout = &duration
	return out, nil
}
```

Add v1alpha6 dispatch in `Decode`; `validateV6` checks the exact version and delegates all existing validation through v1alpha5.

- [ ] **Step 4: Run strict config tests**

Run: `gofmt -w internal/config/wire_v1alpha6.go internal/config/load.go internal/config/validate.go internal/config/load_test.go`

Run: `go test ./internal/config -count=1`

Expected: PASS, including unknown-field and negative-duration rejection.

- [ ] **Step 5: Commit v1alpha6**

```powershell
git add internal/config/wire_v1alpha6.go internal/config/load.go internal/config/validate.go internal/config/load_test.go
git commit -m "feat: decode websocket policy in v1alpha6"
```

### Task 3: Compile Effective Route WebSocket Policy

**Files:**
- Modify: `internal/runtime/builder.go`
- Modify: `internal/runtime/snapshot.go`
- Modify: `internal/runtime/validate.go`
- Modify: `internal/runtime/builder_test.go`

**Interfaces:**
- Consumes: Task 1 canonical override/effective types and existing Service/upstream resolution.
- Produces: `effectiveWebSocketPolicy`, `CompiledRoute.WebSocketPolicy()`, and build error `WEBSOCKET_UPSTREAM_PROTOCOL_INVALID`.

- [ ] **Step 1: Add failing compiler matrix tests**

Use table cases for defaults, Service enable, Route explicit false, Route idle-only override, direct-upstream Route, `auto`, `http1`, and strict `http2`. Assert:

```go
policy := snapshot.routes[0].WebSocketPolicy()
if !policy.Enabled || policy.IdleTimeout != 5*time.Minute {
	t.Fatalf("effective websocket policy=%+v", policy)
}
```

For strict H2, assert a `*BuildError` with code `WEBSOCKET_UPSTREAM_PROTOCOL_INVALID`, kind `route`, resource ID, and field `websocket.enabled`.

- [ ] **Step 2: Confirm effective policy is absent**

Run: `go test ./internal/runtime -run 'TestBuild.*WebSocket' -count=1 -v`

Expected: compile failure because `CompiledRoute.WebSocketPolicy` does not exist.

- [ ] **Step 3: Implement inheritance and compile validation**

Add:

```go
func effectiveWebSocketPolicy(service, route model.WebSocketPolicyOverride) model.WebSocketPolicy {
	out := model.WebSocketPolicy{IdleTimeout: model.DefaultWebSocketIdleTimeout}
	if service.Enabled != nil { out.Enabled = *service.Enabled }
	if service.IdleTimeout != nil { out.IdleTimeout = *service.IdleTimeout }
	if route.Enabled != nil { out.Enabled = *route.Enabled }
	if route.IdleTimeout != nil { out.IdleTimeout = *route.IdleTimeout }
	return out
}
```

Store the result on `CompiledRoute`, expose a value-returning accessor, and reject a negative programmatic override in `validateResources`. In `Builder.Build`, resolve the Service override before compiling plugins, resolve the referenced upstream, and reject effective enabled plus `model.TransportProtocolHTTP2`.

- [ ] **Step 4: Run runtime and config regression tests**

Run: `gofmt -w internal/runtime/builder.go internal/runtime/snapshot.go internal/runtime/validate.go internal/runtime/builder_test.go`

Run: `go test ./internal/runtime ./internal/config -count=1`

Expected: PASS.

- [ ] **Step 5: Commit compiled policy**

```powershell
git add internal/runtime/builder.go internal/runtime/snapshot.go internal/runtime/validate.go internal/runtime/builder_test.go
git commit -m "feat: compile effective websocket policy"
```

### Task 4: Implement Pure RFC 6455 Handshake Validation

**Files:**
- Create: `internal/websocket/doc.go`
- Create: `internal/websocket/handshake.go`
- Create: `internal/websocket/handshake_test.go`
- Create: `internal/websocket/fuzz_test.go`

**Interfaces:**
- Consumes: `net/http` request/response values only.
- Produces: `Candidate`, `ValidateRequest`, `RequestHandshake.Equal`, `CanonicalizeRequestHeaders`, `ValidateResponse`, and `ValidateFinalResponse`.

- [ ] **Step 1: Write the failing RFC table tests**

Lock these types and calls in tests:

```go
type RequestHandshake struct {
	Key        string
	Protocols  []string
	Extensions []string
}

type ResponseHandshake struct {
	Accept     string
	Protocol   string
	Extensions []string
}

captured, err := ValidateRequest(request)
negotiated, err := ValidateResponse(captured, response)
err = ValidateFinalResponse(negotiated, response)
```

Cover token casing/lists, method, protocol, body, version, base64 key decoding to 16 bytes, exact accept, one offered subprotocol, and deterministic extensions.

- [ ] **Step 2: Confirm the package is RED**

Run: `go test ./internal/websocket -count=1 -v`

Expected: compile failure because the package implementation is absent.

- [ ] **Step 3: Implement strict request and response functions**

Use RFC GUID `258EAFA5-E914-47DA-95CA-C5AB0DC85B11`, SHA-1, and base64 for accept calculation. Export one sentinel:

```go
var ErrInvalidHandshake = errors.New("invalid WebSocket handshake")

func Candidate(request *http.Request) bool
func ValidateRequest(request *http.Request) (RequestHandshake, error)
func (h RequestHandshake) Equal(other RequestHandshake) bool
func CanonicalizeRequestHeaders(header http.Header, captured RequestHandshake)
func ValidateResponse(request RequestHandshake, response *http.Response) (ResponseHandshake, error)
func ValidateFinalResponse(expected ResponseHandshake, response *http.Response) error
```

Errors must describe only stable field categories and wrap `ErrInvalidHandshake`; they must never include keys or header values. Canonicalization removes standard and Connection-nominated hop headers, then restores validated Upgrade control headers and captured `Sec-WebSocket-*` negotiation headers.

- [ ] **Step 4: Add fuzz invariants and run them briefly**

Seed valid and malformed handshakes. Fuzz must call validation twice and assert equal success/error classification without panics or secret text.

Run: `gofmt -w internal/websocket`

Run: `go test ./internal/websocket -count=1`

Run: `go test ./internal/websocket -run '^$' -fuzz '^Fuzz(Request|Response)Handshake$' -fuzztime=10s`

Expected: all unit tests pass; both fuzz targets complete without a crash.

- [ ] **Step 5: Commit RFC validation**

```powershell
git add internal/websocket
git commit -m "feat: validate websocket handshakes"
```

### Task 5: Build the Opaque Tunnel Session

**Files:**
- Create: `internal/tunnel/doc.go`
- Create: `internal/tunnel/session.go`
- Create: `internal/tunnel/session_test.go`

**Interfaces:**
- Consumes: opaque readers/writers/closers; no HTTP types.
- Produces: `Endpoint`, `Session`, `Result`, `Direction`, `CloseReason`, `NewSession`, `Run`, and `ForceClose`.

- [ ] **Step 1: Write failing bidirectional, idle, half-close, and idempotence tests**

Tests construct `net.Pipe` endpoints and a fake close-writer around each side. Lock these public types:

```go
type Endpoint struct {
	Reader      io.Reader
	Writer      io.Writer
	Closer      io.Closer
	CloseWriter func() error
}

type Result struct {
	Reason             CloseReason
	Duration           time.Duration
	DownstreamToUpstream uint64
	UpstreamToDownstream uint64
}

session, err := NewSession(downstream, upstream, 50*time.Millisecond)
result := session.Run(context.Background())
```

Verify both directions, activity resets from either direction, explicit zero never idles, EOF calls opposite `CloseWrite`, and concurrent `ForceClose(ReasonShutdown)` is safe.

- [ ] **Step 2: Confirm the tunnel package is RED**

Run: `go test ./internal/tunnel -run 'TestSession' -count=1 -v`

Expected: compile failure because session types are absent.

- [ ] **Step 3: Implement the session state machine**

Define exact bounded enums:

```go
type Direction uint8
const (
	DirectionDownstreamToUpstream Direction = iota
	DirectionUpstreamToDownstream
)

type CloseReason string
const (
	ReasonClientEOF CloseReason = "client_eof"
	ReasonUpstreamEOF CloseReason = "upstream_eof"
	ReasonIdleTimeout CloseReason = "idle_timeout"
	ReasonShutdown CloseReason = "shutdown"
	ReasonIOError CloseReason = "io_error"
)
```

Use two goroutines, two `sync.Pool` buffers of exactly `32 << 10`, atomic byte/activity counters, one idle timer/controller, a result channel, and `sync.Once` cleanup. Do not retain or report peer errors. Treat `http.ErrNotSupported`/unsupported close-write as non-fatal and continue reverse drain.

- [ ] **Step 4: Run session tests under repetition**

Run: `gofmt -w internal/tunnel`

Run: `go test ./internal/tunnel -run 'TestSession' -count=20`

Expected: PASS with no timing flake.

- [ ] **Step 5: Commit the tunnel engine**

```powershell
git add internal/tunnel
git commit -m "feat: add opaque tunnel session"
```

### Task 6: Add Transactional Tunnel Registry and Drain

**Files:**
- Create: `internal/tunnel/registry.go`
- Create: `internal/tunnel/registry_test.go`
- Modify: `internal/tunnel/session.go`

**Interfaces:**
- Consumes: Task 5 `Session` and `Result`.
- Produces: `Observer`, `Registry`, `Registration`, `Register`, `Activate`, `Rollback`, `CloseAdmission`, `Drain`, and `Stats`.

- [ ] **Step 1: Write failing registration and shutdown-race tests**

Lock the ownership interface:

```go
type Observer interface {
	TunnelOpened()
	TunnelBytes(Direction, uint64)
	TunnelClosed(Result)
}

type Stats struct {
	Pending uint64
	Active  uint64
}

registration, err := registry.Register(session, release)
registration.Activate(context.Background())
registration.Rollback()
registry.CloseAdmission()
err = registry.Drain(ctx)
```

Test rollback before activation, exact-once release, activation after admission close, close-admission racing Register, natural drain, deadline force-close, and zero final stats.

- [ ] **Step 2: Confirm registry tests fail**

Run: `go test ./internal/tunnel -run 'TestRegistry' -count=1 -v`

Expected: compile failure because registry APIs are absent.

- [ ] **Step 3: Implement pending-to-active ownership**

Use one mutex-protected map keyed by monotonic `uint64`, a closed-admission flag, one `sync.WaitGroup`, and per-registration `sync.Once`. Export:

```go
var ErrAdmissionClosed = errors.New("tunnel admission closed")

func NewRegistry(observer Observer) *Registry
func (r *Registry) Register(session *Session, release func()) (*Registration, error)
func (r *Registry) CloseAdmission()
func (r *Registry) Drain(ctx context.Context) error
func (r *Registry) Stats() Stats
func (r *Registration) Activate(ctx context.Context)
func (r *Registration) Rollback()
```

`Drain` closes admission, waits for zero registrations, and on `ctx.Done` calls `ForceClose(ReasonShutdown)` on every pending/active session before waiting for cleanup. Observer callbacks execute outside the registry mutex behind a panic boundary.

- [ ] **Step 4: Run tunnel tests and leak repetition**

Run: `gofmt -w internal/tunnel`

Run: `go test ./internal/tunnel -count=20`

Expected: PASS; every test ends with `Stats{}`.

- [ ] **Step 5: Commit registry ownership**

```powershell
git add internal/tunnel
git commit -m "feat: own websocket tunnel lifecycle"
```

### Task 7: Introduce the Static Downstream Certificate Provider

**Files:**
- Create: `internal/downstreamtls/doc.go`
- Create: `internal/downstreamtls/provider.go`
- Create: `internal/downstreamtls/provider_test.go`

**Interfaces:**
- Consumes: existing bootstrap certificate/key paths.
- Produces: `CertificateProvider`, `StaticProvider`, and `LoadStatic` for Gateway wiring.

- [ ] **Step 1: Write failing load, SNI-independence, and immutability tests**

Use generated test certificates and assert both different `ClientHelloInfo.ServerName` values return the same valid chain without sharing a mutable caller-owned slice.

```go
type CertificateProvider interface {
	GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error)
}

provider, err := LoadStatic(certFile, keyFile)
certificate, err := provider.GetCertificate(&tls.ClientHelloInfo{ServerName: "api.example"})
```

- [ ] **Step 2: Confirm package is RED**

Run: `go test ./internal/downstreamtls -count=1 -v`

Expected: compile failure because provider APIs are absent.

- [ ] **Step 3: Implement static provider**

`LoadStatic` uses `tls.LoadX509KeyPair`, verifies at least one certificate document, and stores one immutable provider-owned value:

```go
type StaticProvider struct {
	certificate tls.Certificate
}

func (p *StaticProvider) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	if p == nil || len(p.certificate.Certificate) == 0 {
		return nil, errors.New("downstream certificate is unavailable")
	}
	return &p.certificate, nil
}
```

The callback performs no file access, SNI lookup, or allocation proportional to configured resources. Callers treat the returned certificate as immutable, matching `tls.Config.GetCertificate` ownership.

- [ ] **Step 4: Run provider tests**

Run: `gofmt -w internal/downstreamtls`

Run: `go test ./internal/downstreamtls -count=1`

Expected: PASS.

- [ ] **Step 5: Commit the listener seam**

```powershell
git add internal/downstreamtls
git commit -m "feat: abstract downstream certificate provider"
```

### Task 8: Add H1 Upgrade Transport and TunnelLease

**Files:**
- Modify: `internal/upstream/transport.go`
- Modify: `internal/upstream/plan.go`
- Modify: `internal/upstream/registry.go`
- Modify: `internal/upstream/observer.go`
- Modify: `internal/upstream/transport_test.go`
- Modify: `internal/upstream/registry_test.go`

**Interfaces:**
- Consumes: existing transport generation keys, registry refcounts, and `Selection` lifetime.
- Produces: `Selection.RoundTripUpgrade`, `Selection.AcquireTunnelLease`, `TunnelLease.Release`, and `RegistryStats.LiveTunnelLeases`.

- [ ] **Step 1: Write failing protocol and lease-retirement tests**

Prove `auto` ordinary traffic negotiates H2 while `RoundTripUpgrade` sends HTTP/1.1 with Upgrade headers. Prove retiring the owning PlanSet removes all unrelated state but keeps one transport and one tunnel lease until release:

```go
lease, err := selection.AcquireTunnelLease()
active.Retire()
waitForPhase3AReaper(t, registry)
if got := registry.Stats(); got.LiveTransports != 1 || got.LiveTunnelLeases != 1 {
	t.Fatalf("stats while pinned=%+v", got)
}
lease.Release()
waitForPhase3AReaper(t, registry)
```

Also call `Release` twice and require no underflow or panic.

- [ ] **Step 2: Confirm APIs are absent**

Run: `go test ./internal/upstream -run 'Test(AutoUpgradeUsesHTTP1|TunnelLeasePinsTransport)' -count=1 -v`

Expected: compile failure for missing methods/types.

- [ ] **Step 3: Add upgrade transport role**

Extend `transportRuntime` with optional `upgrade *http.Transport` and `closeUpgradeIdle func()`. For `auto`, create it with HTTP/1 only and TLS ALPN `http/1.1`; for `http1`, reuse `production`; strict `http2` returns a stable `ErrUpgradeProtocol`. Add:

```go
func (s Selection) RoundTripUpgrade(request *http.Request) (*http.Response, error)
```

Ensure `CloseIdleConnections` closes each distinct pool exactly once.

- [ ] **Step 4: Add independent registry ref acquisition**

Carry the owning `*Registry` and `transportKey` in `Selection`. Add:

```go
type TunnelLease struct {
	registry *Registry
	key      transportKey
	runtime  *transportRuntime
	released atomic.Bool
}

func (s Selection) AcquireTunnelLease() (*TunnelLease, error)
func (l *TunnelLease) Release()
```

Acquisition increments the matching live transport entry and `LiveTunnelLeases` under the registry mutex. Release decrements both, deletes/closes the transport only at final reference, performs pool closing outside the mutex, and is idempotent.

- [ ] **Step 5: Run upstream lifecycle and protocol suites**

Run: `gofmt -w internal/upstream`

Run: `go test ./internal/upstream -count=1`

Expected: PASS, including existing TLS/H2/h2c/rotation tests.

- [ ] **Step 6: Commit transport ownership**

```powershell
git add internal/upstream
git commit -m "feat: lease websocket upgrade transports"
```

### Task 9: Extract the Shared Attempt Executor

**Files:**
- Create: `internal/proxy/attempt.go`
- Modify: `internal/proxy/route_transport.go`
- Modify: `internal/proxy/retry.go`
- Modify: `internal/proxy/retry_test.go`
- Modify: `internal/proxy/attempt_transport_test.go`
- Modify: `internal/proxy/phase3b_acceptance_test.go`
- Modify: `internal/proxy/phase3c1_acceptance_test.go`

**Interfaces:**
- Consumes: existing retry classifier, request state, and upstream Selection.
- Produces: `executeAttempts`, `attemptResult`, `attemptRoundTrip`, and `attemptClassifier`; ordinary `routeTransport` becomes a thin adapter.

- [ ] **Step 1: Add a failing shared-executor contract test**

Add a focused test that calls `executeAttempts` with fake round-trip/classifier functions and asserts the final selected endpoint is returned, permits release on every branch, retry response drain remains bounded, and state metadata is unchanged. Existing `routeTransport` tests remain the behavior baseline.

Lock these internal signatures:

```go
type attemptResult struct {
	Response  *http.Response
	Selection upstream.Selection
}

type attemptRoundTrip func(upstream.Selection, *http.Request) (*http.Response, error)
type attemptClassifier func(model.RetryPolicy, *http.Response, error) attemptDecision

func executeAttempts(
	request *http.Request,
	state *requestctx.Context,
	roundTrip attemptRoundTrip,
	classify attemptClassifier,
) (attemptResult, error)
```

- [ ] **Step 2: Run the contract test and confirm RED**

Run: `go test ./internal/proxy -run 'TestRouteTransport|TestAttempt' -count=1`

Expected: compile failure because `executeAttempts` and `attemptResult` do not exist.

- [ ] **Step 3: Move the loop without changing decisions**

Move the complete loop from `routeTransport.RoundTrip` into `executeAttempts`. The ordinary adapter must be exactly:

```go
func (routeTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	state, ok := requestctx.From(request.Context())
	if !ok || state.Runtime == nil {
		return nil, errors.New("proxy request missing compiled runtime route")
	}
	result, err := executeAttempts(request, state,
		func(selection upstream.Selection, attempt *http.Request) (*http.Response, error) {
			return selection.RoundTrip(attempt)
		}, classifyAttempt)
	return result.Response, err
}
```

- [ ] **Step 4: Run all proxy regression/acceptance tests**

Run: `gofmt -w internal/proxy`

Run: `go test ./internal/proxy -count=1`

Expected: PASS with unchanged Phase 3B/3C1 healthy-path thresholds and attempt metadata.

- [ ] **Step 5: Commit the behavior-preserving extraction**

```powershell
git add internal/proxy
git commit -m "refactor: share upstream attempt execution"
```

### Task 10: Implement the Dedicated Upgrade Executor

**Files:**
- Create: `internal/proxy/websocket.go`
- Create: `internal/proxy/websocket_test.go`
- Create: `internal/proxy/response_forward.go`
- Create: `internal/proxy/response_forward_test.go`
- Modify: `internal/proxy/handler.go`
- Modify: `internal/proxy/errors.go`
- Modify: `internal/proxy/headers.go`

**Interfaces:**
- Consumes: Tasks 3, 4, 6, 8, and 9 compiled policy, handshake, registry, lease, and attempt APIs.
- Produces: enabled candidate dispatch, retry-aware Upgrade handling, transactional `101`, non-101 streaming, and stable errors.

- [ ] **Step 1: Add failing dispatch and handshake terminal tests**

Cover disabled candidate stripping into ordinary HTTP, enabled malformed `400`, request plugin short circuit, request-plugin control mutation `500`, retry to a second endpoint, non-101 streaming, response plugin execution, invalid upstream `101` retry/final `502`, final plugin mutation `500`, admission closed `503`, hijack failure `500`, and successful async registration. Capture the structured logger and assert a known WebSocket key, host, endpoint URL, authorization value, and peer error text are absent.

Extend `RuntimeOptions` with exact dependencies:

```go
type WebSocketObserver interface {
	ObserveWebSocketHandshake(result string)
}

type RuntimeOptions struct {
	Snapshots           *gatewayruntime.Manager
	MaxRequestBodyBytes int64
	Logger              *slog.Logger
	Tunnels             *tunnel.Registry
	WebSockets          WebSocketObserver
}
```

- [ ] **Step 2: Run focused tests and confirm RED**

Run: `go test ./internal/proxy -run 'TestWebSocket|TestForwardResponse' -count=1 -v`

Expected: compile failure for missing options and executor.

- [ ] **Step 3: Implement candidate dispatch and manual handshake context**

After Route match/state setup, calculate `candidate := websocket.Candidate(request)` and `policy := match.Route.WebSocketPolicy()`. Disabled/non-candidate requests keep the ordinary path. Enabled candidates validate before plugins, run request plugins once, revalidate semantic equality, then call an `upgradeExecutor`.

Use an independent context with a manual timer and removable request-cancel link. Obtain the deadline from `state.Runtime.RetryPolicy().TotalTimeout`:

```go
tunnelCtx, cancel := context.WithCancel(context.Background())
stopClientLink := context.AfterFunc(request.Context(), cancel)
totalTimeout := state.Runtime.RetryPolicy().TotalTimeout
var timer *time.Timer
if totalTimeout > 0 {
	timer = time.AfterFunc(totalTimeout, cancel)
}
```

Create the timer only for a positive total timeout. On commitment call:

```go
stopClientLink()
if timer != nil {
	timer.Stop()
}
```

The tunnel registration cleanup calls `cancel` exactly once.

- [ ] **Step 4: Implement Upgrade attempts and error mapping**

Call `executeAttempts` with `Selection.RoundTripUpgrade`. Its classifier wraps the existing classifier and converts invalid `101` into retryable transport failure:

```go
func classifyWebSocketAttempt(
	request websocket.RequestHandshake,
) attemptClassifier {
	return func(policy model.RetryPolicy, response *http.Response, err error) attemptDecision {
		decision := classifyAttempt(policy, response, err)
		if err == nil && response != nil && response.StatusCode == http.StatusSwitchingProtocols {
			if _, validateErr := websocket.ValidateResponse(request, response); validateErr != nil {
				decision.Retry = policy.RetryOn.ConnectionFailure
				decision.Reason = retryReasonConnectionFailure
				decision.Observation = upstream.Observation{Source: upstream.SourcePassive, Kind: upstream.OutcomeTransportFailure}
			}
		}
		return decision
	}
}
```

Map terminal outcomes to the exact design codes and observer results; never expose validation detail.

- [ ] **Step 5: Implement non-101 HTTP response terminal**

Implement this exact terminal entry point:

```go
func (h *handler) forwardResponse(
	writer http.ResponseWriter,
	request *http.Request,
	state *requestctx.Context,
	response *http.Response,
) error
```

It runs the response plugin once, removes hop-by-hop headers, copies headers/status/body, streams permitted trailers, closes the body on all paths, and updates `state.ResponseCode`. Test cancellation and plugin failure.

- [ ] **Step 6: Implement transactional hijack and handoff**

For valid final `101`, use this ownership order:

```go
lease, err := result.Selection.AcquireTunnelLease()
connection, buffered, err := http.NewResponseController(writer).Hijack()
upstreamStream, ok := response.Body.(io.ReadWriteCloser)
if !ok {
	return errors.New("upstream 101 body is not writable")
}
session, err := tunnel.NewSession(downstreamEndpoint(connection, buffered), upstreamEndpoint(upstreamStream), policy.IdleTimeout)
registration, err := h.tunnels.Register(session, func() {
	lease.Release()
	cancel()
})
handshakeResponse := *response
handshakeResponse.Body = nil
handshakeResponse.ContentLength = 0
err = handshakeResponse.Write(buffered)
err = buffered.Flush()
state.ResponseCode = http.StatusSwitchingProtocols
registration.Activate(tunnelCtx)
```

Check every returned error and execute exact reverse-order rollback. Never call `response.Write` with its upgraded Body attached; write only the sanitized status/header clone so body copying remains owned by the tunnel.

- [ ] **Step 7: Run proxy tests and existing request behavior**

Run: `gofmt -w internal/proxy`

Run: `go test ./internal/proxy -count=1`

Expected: PASS; ordinary Upgrade remains impossible when policy is disabled, and existing HTTP/gRPC tests remain green.

- [ ] **Step 8: Commit Upgrade execution**

```powershell
git add internal/proxy
git commit -m "feat: proxy websocket upgrades"
```

### Task 11: Expose Exact Bounded WebSocket Telemetry

**Files:**
- Create: `internal/telemetry/websocket.go`
- Modify: `internal/telemetry/telemetry.go`
- Modify: `internal/telemetry/telemetry_test.go`

**Interfaces:**
- Consumes: `proxy.WebSocketObserver` and `tunnel.Observer` method sets.
- Produces: exact metric families, pre-bound bounded labels, and `101` HTTP telemetry status.

- [ ] **Step 1: Add failing metric family/cardinality tests**

Exercise all six handshake results, five close reasons, two directions, active open/close, duration, and invalid labels. Assert exact family counts and absence of Route/upstream/host/key values.

Also wrap a handler that sets `requestctx.Context.ResponseCode = 101` without calling `WriteHeader` and assert:

```text
gateway_http_requests_total{method="GET",route_id="events",status_class="1xx"} 1
```

- [ ] **Step 2: Confirm metrics are absent**

Run: `go test ./internal/telemetry -run 'TestWebSocket|TestRequestMetricsUseCommittedUpgradeStatus' -count=1 -v`

Expected: FAIL because metric methods/families are absent and status is recorded as `2xx`.

- [ ] **Step 3: Register and pre-bind exact metrics**

Add:

```go
func (t *Telemetry) ObserveWebSocketHandshake(result string)
func (t *Telemetry) TunnelOpened()
func (t *Telemetry) TunnelBytes(direction tunnel.Direction, bytes uint64)
func (t *Telemetry) TunnelClosed(result tunnel.Result)
```

Create the exact five metric families from the design. Pre-bind only the declared label values; ignore invalid enum/string input behind a no-panic boundary. `TunnelOpened` increments the gauge; `TunnelClosed` decrements it, increments bounded reason, and observes duration.

- [ ] **Step 4: Teach HTTP telemetry to honor async `101`**

After `next.ServeHTTP`, select status with:

```go
status := writer.statusCode()
if state, ok := requestctx.From(request.Context()); ok &&
	state.ResponseCode == http.StatusSwitchingProtocols {
	status = http.StatusSwitchingProtocols
}
```

This must not change ordinary informational-response handling.

- [ ] **Step 5: Run telemetry and proxy tests**

Run: `gofmt -w internal/telemetry`

Run: `go test ./internal/telemetry ./internal/proxy -count=1`

Expected: PASS with exact cardinality.

- [ ] **Step 6: Commit WebSocket telemetry**

```powershell
git add internal/telemetry
git commit -m "feat: expose bounded websocket telemetry"
```

### Task 12: Wire Static TLS Provider, Tunnels, and Shutdown

**Files:**
- Modify: `internal/gateway/gateway.go`
- Modify: `internal/gateway/gateway_test.go`
- Modify: `internal/gateway/lifecycle_observer.go`
- Modify: `internal/gateway/lifecycle_observer_test.go`

**Interfaces:**
- Consumes: Tasks 6, 7, 10, and 11 registry, provider, proxy options, and telemetry.
- Produces: live Gateway wiring and readiness-first tunnel drain ordering.

- [ ] **Step 1: Add failing TLS callback and shutdown-order tests**

Assert `Gateway.New` uses `tls.Config.GetCertificate`, still serves H1/H2 over HTTPS, and returns the startup certificate for arbitrary SNI. Add tests with one natural tunnel and one blocked tunnel to prove:

```text
readiness false -> admission closed -> traffic servers drain -> tunnel force close -> lease zero -> manager close
```

Verify Apply rejects after shutdown starts and a handshake racing admission close never becomes active.

- [ ] **Step 2: Confirm Gateway lacks tunnel wiring**

Run: `go test ./internal/gateway -run 'TestGateway.*(CertificateProvider|WebSocket|Tunnel)' -count=1 -v`

Expected: compile/test failure because fields and dependencies are absent.

- [ ] **Step 3: Wire construction**

Replace direct `tls.LoadX509KeyPair` with `downstreamtls.LoadStatic`. Build TLS config with:

```go
tlsConfig := &tls.Config{
	GetCertificate: provider.GetCertificate,
	MinVersion:     tls.VersionTLS12,
	NextProtos:     []string{"h2", "http/1.1"},
}
```

Create `tunnel.NewRegistry(telemetryRuntime)`, store it on `Gateway`, and pass it plus telemetry into `proxy.RuntimeOptions`. Construction rollback must close admission and drain an empty registry.

- [ ] **Step 4: Implement shutdown ordering**

At shutdown start, preserve this explicit order:

```go
g.telemetry.SetReady(false)
g.tunnels.CloseAdmission()
g.manager.StopHealth()
// Shut down HTTP and HTTPS concurrently using ctx.
g.trafficRequests.Wait()
if err := g.tunnels.Drain(ctx); err != nil {
	errs = append(errs, err)
}
// Close manager only after tunnel leases have been released.
```

If `Drain` returns the caller deadline, append it once and continue the existing two-second fallback manager cleanup. Lifecycle logging reports aggregate counts only; tests assert it excludes handshake and peer data.

- [ ] **Step 5: Run repeated Gateway lifecycle tests**

Run: `gofmt -w internal/gateway`

Run: `go test ./internal/gateway -count=20`

Expected: PASS with no active tunnel or transport lease after each test.

- [ ] **Step 6: Commit Gateway ownership**

```powershell
git add internal/gateway
git commit -m "feat: drain websocket tunnels on shutdown"
```

### Task 13: Prove Process-Level WebSocket Correctness and Reload Safety

**Files:**
- Modify: `internal/testupstream/server.go`
- Modify: `internal/testupstream/server_test.go`
- Create: `test/integration/websocket_test.go`
- Modify: `test/integration/snapshot_test.go`
- Modify: `test/integration/tls_test.go`

**Interfaces:**
- Consumes: complete v1alpha6 Gateway runtime.
- Produces: deterministic raw WebSocket echo endpoint and black-box ws/wss/reload/drain evidence.

- [ ] **Step 1: Add a failing deterministic upstream echo test**

Add `/websocket/echo` to `test-upstream` through a focused handler:

```go
func serveWebSocketEcho(writer http.ResponseWriter, request *http.Request) {
	handshake, err := websocket.ValidateRequest(request)
	if err != nil {
		http.Error(writer, "invalid websocket handshake", http.StatusBadRequest)
		return
	}
	_ = handshake
	// Hijack, write the validated 101 response, then read masked client frames
	// and write valid unmasked echo frames until close.
}
```

The implementation computes the exact accept header via the shared package, checks every hijack/write/flush error, and closes the connection. Add private test-upstream frame helpers that enforce masking, bounded payload length, FIN/opcode preservation, and valid server-side unmasked echo. Frame parsing exists only in the deterministic upstream, never in Gateway/tunnel packages. Unit-test valid and malformed requests without a third-party WebSocket package.

- [ ] **Step 2: Implement reusable raw integration client helpers**

In `websocket_test.go`, create this helper-owned connection shape:

```go
type rawWebSocketClient struct {
	connection net.Conn
	buffered   *bufio.ReadWriter
}

func dialRawWebSocket(t *testing.T, networkAddress, host, path string, tlsConfig *tls.Config) *rawWebSocketClient
func (c *rawWebSocketClient) WriteFrame(t *testing.T, fin bool, opcode byte, payload []byte)
func (c *rawWebSocketClient) ReadFrame(t *testing.T) (fin bool, opcode byte, payload []byte)
```

The helper writes a fixed RFC request/key, parses the response with `http.ReadResponse`, validates `101`, masks client frames with a deterministic test key, and rejects masked server frames. Do not add a module dependency.

- [ ] **Step 3: Add RED black-box scenarios**

Cover:

```text
clear downstream -> clear upstream
TLS downstream -> clear upstream
clear downstream -> verified TLS upstream
TLS downstream -> verified TLS upstream
subprotocol and extension pass-through
binary/fragmented/large opaque payload echo
disabled Route ordinary HTTP behavior
malformed request 400
strict H2 config rejection
retry first endpoint failure then second success
invalid 101 retry/final 502
```

Run: `go test ./test/integration -run 'TestWebSocket' -count=1 -v`

Expected: at least one scenario fails until test-upstream and process wiring are complete.

- [ ] **Step 4: Add reload and shutdown lifecycle scenarios**

Open a tunnel, Apply a revision that removes the Route and rotates its upstream, prove the old snapshot retires while echo continues, prove new handshake is 404, close the tunnel, and wait for transport leases to reach zero. Add natural shutdown drain and deadline force-close tests.

- [ ] **Step 5: Run integration tests under repetition**

Run: `gofmt -w internal/testupstream test/integration`

Run: `go test ./internal/testupstream -count=1`

Run: `go test ./test/integration -run 'TestWebSocket' -count=10`

Expected: PASS without leaked child processes or listeners.

- [ ] **Step 6: Commit black-box correctness**

```powershell
git add internal/testupstream test/integration
git commit -m "test: prove websocket lifecycle end to end"
```

### Task 14: Add Phase 3C3 Fuzz, Acceptance, and Benchmarks

**Files:**
- Modify: `internal/websocket/fuzz_test.go`
- Create: `internal/tunnel/session_benchmark_test.go`
- Create: `internal/proxy/phase3c3_acceptance_test.go`
- Create: `internal/proxy/websocket_benchmark_test.go`
- Create: `internal/gateway/phase3c3_lifecycle_test.go`

**Interfaces:**
- Consumes: complete handshake/tunnel/proxy/Gateway implementation.
- Produces: normal/full acceptance profiles and deterministic relative gates.

- [ ] **Step 1: Add zero-per-chunk allocation benchmark**

Benchmark established `Session` copying through preallocated streams after setup. Use `b.ReportAllocs` and fail a companion `testing.AllocsPerRun` test if allocations scale with chunk count rather than session count.

- [ ] **Step 2: Add alternating direct-versus-Gateway benchmark harness**

Use a direct Go H1 upgrade/tunnel baseline with the same payload and 32 KiB buffers. Warm both paths, alternate order for five rounds, sort per-operation latencies, and log median throughput/p99/allocations. Full-gate assertions are exactly:

```go
if gatewayHandshakeThroughput < directHandshakeThroughput*0.90 { t.Fatal("handshake throughput gate") }
if gatewayHandshakeP99 > directHandshakeP99*125/100 { t.Fatal("handshake p99 gate") }
if gatewayMessageThroughput < directMessageThroughput*0.95 { t.Fatal("message throughput gate") }
if gatewayMessageP99 > directMessageP99*110/100 { t.Fatal("message p99 gate") }
```

- [ ] **Step 3: Add normal and full lifecycle profiles**

Use explicit profile values:

```go
profile := phase3C3Profile{Tunnels: 200, MessagesPerTunnel: 20, Rotations: 2}
if os.Getenv("GATEWAY_PHASE3C3_ACCEPTANCE") == "1" {
	profile = phase3C3Profile{Tunnels: 10_000, MessagesPerTunnel: 100, Rotations: 20}
}
```

The full profile mixes idle/active traffic, applies revisions, drains, forces GC, and asserts active tunnels, tunnel leases, and retired plan sets return to zero. Log seed, OS/arch, Go version, connection counts, heap delta, goroutine delta, throughput, and p99.

- [ ] **Step 4: Run normal acceptance and locked benchmarks**

Run: `gofmt -w internal/websocket internal/tunnel internal/proxy internal/gateway`

Run: `go test ./internal/tunnel ./internal/proxy ./internal/gateway -count=1`

Run: `go test ./internal/proxy -run '^$' -bench 'BenchmarkPhase3C3' -benchmem -count=5`

Expected: normal correctness passes and benchmark output contains all locked measurements.

- [ ] **Step 5: Run 30-second fuzz targets**

Run: `go test ./internal/websocket -run '^$' -fuzz '^FuzzRequestHandshake$' -fuzztime=30s`

Run: `go test ./internal/websocket -run '^$' -fuzz '^FuzzResponseHandshake$' -fuzztime=30s`

Expected: no crash or nondeterministic classification.

- [ ] **Step 6: Run opt-in full acceptance on a suitable host**

Run: `$env:GATEWAY_PHASE3C3_ACCEPTANCE='1'; go test ./internal/proxy ./internal/gateway -run 'TestPhase3C3' -count=1 -v`

Expected: all relative gates pass. If the current developer host cannot sustain 10,000 sockets, record the exact failure/environment in the evidence ledger and leave the reference-Linux gate pending.

- [ ] **Step 7: Commit acceptance evidence code**

```powershell
git add internal/websocket internal/tunnel internal/proxy internal/gateway
git commit -m "test: add phase 3c3 websocket acceptance"
```

### Task 15: Document, Verify, and Prepare the Phase 3C2 Handoff

**Files:**
- Create: `configs/phase3c3.yaml`
- Create: `docs/operations/phase-3c3-runbook.md`
- Create: `docs/benchmarks/phase-3c3-current-status.md`
- Modify: `README.md`
- Modify: `docs/superpowers/specs/2026-07-21-go-native-api-gateway-phase-roadmap-design.md`
- Modify: `docs/superpowers/specs/2026-07-31-phase-3c3-websocket-lifecycle-design.md`

**Interfaces:**
- Consumes: verified Phase 3C3 runtime and measured evidence.
- Produces: runnable example, operations guide, authoritative evidence status, reordered roadmap checkpoint, and clean verification handoff.

- [ ] **Step 1: Add a strict checked-in example config test**

Extend `internal/config/load_test.go` with:

```go
func TestPhase3C3ExampleConfigurationLoads(t *testing.T) {
	document, err := os.ReadFile(filepath.Join("..", "..", "configs", "phase3c3.yaml"))
	if err != nil { t.Fatal(err) }
	certificateFile, privateKeyFile, caFile := writeV5MaterialFiles(t)
	rendered := string(document)
	for mounted, local := range map[string]string{
		"/certs/server.crt": filepath.ToSlash(certificateFile),
		"/certs/server.key": filepath.ToSlash(privateKeyFile),
		"/secrets/internal-ca.pem": filepath.ToSlash(caFile),
	} {
		rendered = strings.ReplaceAll(rendered, mounted, local)
	}
	bootstrap, resources, err := Decode(strings.NewReader(rendered))
	if err != nil { t.Fatal(err) }
	if len(resources.Routes) == 0 || len(resources.Services) == 0 {
		t.Fatal("phase3c3 example lacks route/service resources")
	}
	if bootstrap.HTTPS.CertificateFile == "" || bootstrap.HTTPS.PrivateKeyFile == "" {
		t.Fatal("phase3c3 example lacks static downstream identity")
	}
}
```

Add assertions that v1alpha6 resources contain one Service-enabled WebSocket Route, one Route timeout override, and `auto` upstream protocol.

- [ ] **Step 2: Write the runnable config and runbook**

The example must include nested WebSocket policies, `60s`/`0` semantics, ws/wss client commands, clear/TLS upstream examples, retry behavior, telemetry names, reload behavior, shutdown drain, troubleshooting, and mounted-secret guidance. State that RFC 8441, dynamic SNI, quotas, access logs, and APISIX comparison are absent.

- [ ] **Step 3: Record only observed evidence**

The status ledger must list every command, environment, exact result, benchmark median, fuzz execution count, lifecycle profile, and pending gate. Use status `implementation complete; canonical WebSocket evidence pending` unless race and reference-Linux evidence actually pass. Never mark APISIX parity or umbrella Phase 3C complete.

- [ ] **Step 4: Update README and roadmap**

State that 3C3 was intentionally implemented before 3C2, 3C2 still owns dynamic downstream exact/wildcard SNI certificates, and 3D owns bounded access logs plus integrated APISIX comparison. Preserve the deferred Phase 2 Task 16 debt.

- [ ] **Step 5: Run documentation checks**

Run the repository Markdown relative-link checker over README, runbook, status, roadmap, design, and plan. Then run:

```powershell
git diff --check
go test ./internal/config -run TestPhase3C3ExampleConfigurationLoads -count=1 -v
```

Expected: zero missing links, no whitespace errors, and strict example load PASS.

- [ ] **Step 6: Run the complete code quality gate**

Run:

```powershell
$goFiles = (Get-ChildItem cmd,internal,test -Recurse -Filter *.go).FullName
$unformatted = gofmt -l $goFiles
if ($unformatted) { $unformatted; exit 1 }
go vet ./...
staticcheck -tests=false ./...
revive -set_exit_status -config revive.toml -formatter default ./...
go test -p 1 ./... -count=1
go build ./cmd/...
```

Expected: every command exits zero. Record the exact duration of the full test gate.

- [ ] **Step 7: Run or record the race gate**

Run: `go test -p 1 ./... -race -count=1`

Expected on a CGO-capable host: PASS. If this host reports `go: -race requires cgo; enable cgo by setting CGO_ENABLED=1`, record that exact output as pending and do not weaken the gate.

- [ ] **Step 8: Confirm final lifecycle state**

Run focused lifecycle tests repeatedly, then confirm the evidence reports active tunnels `0`, live tunnel leases `0`, retired plan sets `0`, and no leaked test processes. Remove only the workspace cache with explicit containment validation:

```powershell
$workspacePath = (Resolve-Path -LiteralPath '.').Path
$cachePath = (Resolve-Path -LiteralPath '.cache').Path
$requiredPrefix = $workspacePath + [IO.Path]::DirectorySeparatorChar
if (-not $cachePath.StartsWith($requiredPrefix, [StringComparison]::OrdinalIgnoreCase)) {
    throw "refusing to remove cache outside workspace: $cachePath"
}
Remove-Item -LiteralPath $cachePath -Recurse -Force
```

If `.cache` is absent, record that no removal was necessary. Request the required destructive-action approval before executing `Remove-Item`.

- [ ] **Step 9: Commit the Phase 3C3 checkpoint**

```powershell
git add configs/phase3c3.yaml docs/operations/phase-3c3-runbook.md docs/benchmarks/phase-3c3-current-status.md README.md docs/superpowers/specs/2026-07-21-go-native-api-gateway-phase-roadmap-design.md docs/superpowers/specs/2026-07-31-phase-3c3-websocket-lifecycle-design.md internal/config/load_test.go
git commit -m "docs: record phase 3c3 websocket runtime"
```

- [ ] **Step 10: Verify the clean implementation branch**

Run:

```powershell
git status --short --branch
git log --oneline --decorate -20
```

Expected: no working-tree entries; the branch contains focused TDD commits for Tasks 1 through 15; Phase 3C2 is the documented next design cycle.

## Final Acceptance Checklist

- [ ] v1alpha6 is strict and v1alpha1-v1alpha5 compatibility remains intact.
- [ ] Route/Service inheritance and explicit false/zero semantics are deterministic.
- [ ] Classic ws/wss works through clear and verified TLS upstreams.
- [ ] Strict HTTP/2 WebSocket configuration fails closed; ordinary HTTP/2/gRPC remains unchanged.
- [ ] Plugins, retry, budget, health, timeout, and TLS behavior are correct before `101`.
- [ ] No retry, plugin execution, or health mutation occurs after `101`.
- [ ] Established tunnels release snapshots and pin only selected transport generations.
- [ ] Apply leaves old tunnels active while new handshakes use the new revision.
- [ ] Idle timeout, half-close, natural drain, deadline force-close, and double-close are deterministic.
- [ ] WebSocket telemetry has only the exact bounded families and labels.
- [ ] No payload, secret, peer identity, or arbitrary error reaches logs/metrics.
- [ ] Normal relative performance gates pass; full/reference gates are passed or explicitly pending.
- [ ] Fuzz and all available race gates are passed or accurately recorded pending.
- [ ] README, runbook, evidence, design, roadmap, and example config agree.
- [ ] Phase 3C2 remains required and APISIX parity is not claimed.

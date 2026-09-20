# Phase 3D1 Bounded Access Logging Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver one bounded, redacted JSON Lines access event for every request that reaches the gateway traffic handler, with accurate proxy observations, nonblocking output, fixed-cardinality metrics, and bounded shutdown.

**Architecture:** Add a strict `gateway/v1alpha8` configuration surface and a new `internal/accesslog` package containing immutable events, HTTP observation middleware, and a bounded asynchronous sink. Keep proxy decisions in `requestctx`, compose access observation around panic recovery and request metrics, inject stdout only from `cmd/gateway-dp`, and make the disabled path retain the existing handler shape and cost.

**Tech Stack:** Go 1.26.5, standard library HTTP/JSON/crypto/synchronization/testing, existing Prometheus client, YAML v3, and the repository's current gateway/proxy/runtime packages.

**Spec:** [Approved Phase 3D1 specification](../specs/2026-09-20-phase-3d1-bounded-access-logging-design.md).

**Status:** Written after user approval of the complete Phase 3D1 specification on 2026-09-20. Implementation has not started. All execution checkboxes are intentionally unchecked.

## Global Constraints

- Preserve existing user changes and begin every execution session with `git status --short`. Read the current spec, nearby implementation, and tests before editing.
- Keep standalone configuration versions v1alpha1 through v1alpha7 byte-compatible in accepted fields and behavior. Only v1alpha8 accepts `telemetry.access_log`.
- Logging disabled means no access UUID, clock reads, body/status wrapper, queue, sink worker, or stdout writes.
- Logging enabled produces exactly one event for each request entering the traffic handler. Parser/TLS/listener failures rejected before that handler remain outside scope.
- Never enqueue request-owned pointers, headers, URLs, bodies, contexts, raw errors, stack traces, or mutable slices.
- Never emit raw path, query, host, headers, cookies, authorization data, request/response body, client IP, upstream URL, certificate data, plugin configuration, or secret material.
- Bound every event string to 256 encoded bytes and each JSON line, including newline, to 4096 bytes. Hash overlong or invalid UTF-8 values as lowercase `sha256:<hex>`; reject unknown closed enums as `event_invalid`.
- Emit `endpoint_id` only as `sha256:<hex>` over `Selection.EndpointID()`; that current canonical identity contains an endpoint URL and must never be written raw.
- Use a queue whose capacity equals the configured value. Submission is nonblocking drop-new. The first writer error or panic permanently enters failed-output state and prevents further writer calls.
- Access-output degradation never changes traffic responses or readiness. Metrics use only the fixed result/reason label sets in the spec.
- Shutdown admits no new events, waits for active traffic through existing lifecycle ownership, and gives accepted access events no more than five seconds to drain.
- Preserve streaming, trailers, cancellation, HTTP/1.1, HTTP/2, WebSocket handshake/tunnel ownership, response-controller behavior, snapshot leases, and last-known-good runtime activation.
- Do not add dependencies, dynamic logging policy, file rotation, sampling, request/header capture, tunnel byte logging, APISIX parity claims, or Phase 3D2 work.
- Use channels and controlled clocks/writers for concurrency tests; do not use sleeps to establish correctness.
- Run `gofmt` on changed Go files. Do not commit certificates, private keys, profiles, raw access logs, or generated benchmark output.

## Fixed contracts and ownership

All paths are repository-relative. New Go packages require identifier-leading package and exported API documentation.

### Configuration contract

`internal/config/types.go` gains canonical runtime values:

```go
const (
    DefaultAccessLogQueueCapacity = 4096
    MaxAccessLogQueueCapacity     = 65536
)

type AccessLogConfig struct {
    Enabled       bool
    QueueCapacity int
}

type TelemetryConfig struct {
    RequestMetricsEnabled bool
    ProfilingEnabled      bool
    AccessLog             AccessLogConfig
}
```

`wire_v1alpha8.go` owns a v1alpha8-only telemetry wire type. Do not add `access_log` to the shared legacy `telemetryDocument`, because that would make older strict decoders accept the field.

### Request observation contract

`internal/requestctx/context.go` gains typed closed values and request-owned observation:

```go
type ResponseSource string

const (
    ResponseSourceGateway   ResponseSource = "gateway"
    ResponseSourcePlugin    ResponseSource = "plugin"
    ResponseSourceUpstream  ResponseSource = "upstream"
    ResponseSourceWebSocket ResponseSource = "websocket"
)

type Termination string

const (
    TerminationCompleted            Termination = "completed"
    TerminationClientCanceled       Termination = "client_canceled"
    TerminationDownstreamWriteError Termination = "downstream_write_error"
    TerminationPanic                Termination = "panic"
    TerminationWebSocketUpgraded    Termination = "websocket_upgraded"
)

type Observation struct {
    ResponseSource ResponseSource
    Termination    Termination
    UpstreamWait   time.Duration
    ResponseStream time.Duration
}
```

`Context` owns this value. Add saturating duration accumulation helpers rather than allowing proxy call sites to perform unchecked addition.

### Access event and sink contracts

`internal/accesslog` owns:

```go
const (
    Schema          = "gateway.access/v1"
    MaxStringBytes  = 256
    MaxEncodedBytes = 4096
)

type Event struct {
    Schema, Timestamp, RequestID string
    Revision uint64
    Method, Protocol string
    RouteID, ServiceID, UpstreamID, EndpointID string
    ResponseSource string
    Status int
    RequestBodyBytes, ResponseBodyBytes uint64
    Attempts int
    UpstreamOutcome, RetrySuppressed, ErrorCode, Termination string
    DurationUS, UpstreamWaitUS, ResponseStreamUS uint64
}

type DropReason string

const (
    DropQueueFull       DropReason = "queue_full"
    DropEventInvalid    DropReason = "event_invalid"
    DropEncodeError     DropReason = "encode_error"
    DropWriteError      DropReason = "write_error"
    DropShutdownTimeout DropReason = "shutdown_timeout"
)

type Observer interface {
    ObserveAccessLogWritten()
    ObserveAccessLogDropped(DropReason)
    SetAccessLogQueueDepth(int)
}

type Sink interface {
    Submit(Event)
    Close(context.Context)
}
```

The concrete sink copies an already immutable `Event` value into a channel. `Close` is idempotent and has no return value because writer failure is telemetry state, not a gateway shutdown failure.

### Gateway construction contract

Keep existing callers working:

```go
type Options struct {
    AccessLogWriter io.Writer
}

func New(bootstrap config.BootstrapConfig, resources model.ResourceSet, logger *slog.Logger) (*Gateway, error) {
    return NewWithOptions(bootstrap, resources, logger, Options{})
}

func NewWithOptions(bootstrap config.BootstrapConfig, resources model.ResourceSet, logger *slog.Logger, options Options) (*Gateway, error)
```

`cmd/gateway-dp` calls `NewWithOptions` with stdout. Enabled logging with a nil writer fails before listener binding. Tests and embedding callers using old versions remain on `New`.

### Traffic-chain contract

When enabled:

```text
trackTraffic(
  requestctx.Middleware(
    accesslog.Middleware(...).Wrap(
      recoverPanics(
        telemetry.Wrap(proxy),
      ),
    ),
  ),
)
```

When disabled, retain the pre-3D1 composition exactly. Panic recovery marks the request observation before producing/rethrowing the existing safe response. Access middleware uses a defer only to finalize its event; it never swallows panics.

---

## Task 1: Add strict v1alpha8 access-log configuration

**Files:** Modify `internal/config/types.go`, `internal/config/validate.go`, `internal/config/load.go`, `internal/config/load_test.go`. Create `internal/config/wire_v1alpha8.go`, `configs/phase3d1.yaml`.

- [ ] **Step 1: Add red tests for v1alpha8 and legacy rejection.**

Add table cases covering enabled with absent/zero capacity defaulting to 4096; explicit capacities 1 and 65536; rejection of -1, 65537, disabled plus nonzero capacity, unknown/duplicate nested fields, multiple YAML documents, invalid scalar types, and unsupported versions. Add a loop proving v1alpha1-v1alpha7 still load with canonical `AccessLogConfig{}`, and prove v1alpha7 rejects `telemetry.access_log`.

```go
func TestLoadV1Alpha8AccessLogDefaults(t *testing.T) {
    bootstrap, _, err := Load([]byte(validV1Alpha8("    enabled: true\n")))
    if err != nil {
        t.Fatal(err)
    }
    if !bootstrap.Telemetry.AccessLog.Enabled ||
        bootstrap.Telemetry.AccessLog.QueueCapacity != DefaultAccessLogQueueCapacity {
        t.Fatalf("access log = %+v", bootstrap.Telemetry.AccessLog)
    }
}
```

- [ ] **Step 2: Run the focused tests and retain the expected failure.**

```text
go test ./internal/config -run 'TestLoadV1Alpha8|TestLoadLegacy.*AccessLog|TestPhase3D1Example' -count=1
```

Expected: FAIL because v1alpha8 and the example do not exist.

- [ ] **Step 3: Implement the v1alpha8 wire conversion.**

Copy the v1alpha7 top-level wire shape and conversion without exposing legacy internals. Use an `*int` queue capacity field to preserve presence, then canonicalize:

```go
func convertAccessLogV8(w accessLogDocumentV8) (AccessLogConfig, error) {
    if !w.Enabled {
        if w.QueueCapacity != nil && *w.QueueCapacity != 0 {
            return AccessLogConfig{}, errors.New("telemetry.access_log.queue_capacity: must be zero when disabled")
        }
        return AccessLogConfig{}, nil
    }
    capacity := DefaultAccessLogQueueCapacity
    if w.QueueCapacity != nil && *w.QueueCapacity != 0 {
        capacity = *w.QueueCapacity
    }
    if capacity < 1 || capacity > MaxAccessLogQueueCapacity {
        return AccessLogConfig{}, fmt.Errorf("telemetry.access_log.queue_capacity: must be between 1 and %d", MaxAccessLogQueueCapacity)
    }
    return AccessLogConfig{Enabled: true, QueueCapacity: capacity}, nil
}
```

Add the explicit load switch and version validator. Do not change the legacy telemetry wire type.

- [ ] **Step 4: Add and load the runnable example.**

Base `configs/phase3d1.yaml` on the complete current Phase 3C example, set `api_version: gateway/v1alpha8`, and explicitly enable capacity 4096. Use only checked-in-safe paths and the existing documented material-path conventions.

- [ ] **Step 5: Run focused and package tests.**

```text
gofmt -w internal/config
go test ./internal/config -count=1
git diff --check
```

- [ ] **Step 6: Commit the task.**

```text
git add internal/config configs/phase3d1.yaml
git commit -m "feat: add phase 3d1 access log configuration"
```

## Task 2: Build the bounded event schema and canonical encoder

**Files:** Create `internal/accesslog/doc.go`, `event.go`, `encode.go`, `event_test.go`, `encode_test.go`.

- [ ] **Step 1: Write golden and boundary tests.**

The golden test must assert the complete one-line byte sequence and field order. Add named cases for valid UTF-8 at 256 bytes, 257 bytes, invalid UTF-8, an endpoint identity that embeds `http://`, unknown response source, unknown termination, negative status/attempts, duration/counter saturation, and encoded size exactly at/over 4096. Search decoded keys and raw bytes for every forbidden input class.

```go
func TestEndpointIDIsAlwaysHashed(t *testing.T) {
    got := EndpointID("users\x00http://10.0.0.1:8080")
    if !strings.HasPrefix(got, "sha256:") || strings.Contains(got, "http") {
        t.Fatalf("endpoint id = %q", got)
    }
}
```

- [ ] **Step 2: Run the red tests.**

```text
go test ./internal/accesslog -run 'TestEvent|TestEncode|TestEndpoint|TestBound' -count=1
```

Expected: FAIL because the package is missing.

- [ ] **Step 3: Implement bounded scalar helpers and event validation.**

Use `utf8.ValidString`, byte length, SHA-256, and saturating conversions. `Normalize` accepts an Event candidate, bounds every open string, validates the closed enum sets, converts timestamps to UTC RFC3339Nano, hashes canonical endpoint identity unconditionally, and returns a value without caller-owned aliases. Middleware creates only the candidate; `Sink.Submit` normalizes it synchronously before any enqueue.

```go
func boundedString(value string) string {
    if utf8.ValidString(value) && len(value) <= MaxStringBytes {
        return value
    }
    sum := sha256.Sum256([]byte(value))
    return "sha256:" + hex.EncodeToString(sum[:])
}
```

Use a private wire struct with fields declared in schema order and explicit JSON tags. Encode into a reusable worker-local buffer, append exactly one newline, and reject a result over `MaxEncodedBytes`.

- [ ] **Step 4: Prove the encoder cannot leak forbidden values.**

The Event API must have no fields for forbidden data. The tests should construct source inputs containing unique markers in path/query/headers/body/errors and assert those markers are absent from encoded bytes.

- [ ] **Step 5: Run package checks and commit.**

```text
gofmt -w internal/accesslog
go test ./internal/accesslog -count=1
git diff --check
git add internal/accesslog
git commit -m "feat: define bounded access log events"
```

## Task 3: Implement the nonblocking sink and failed-output state

**Files:** Create `internal/accesslog/sink.go`, `internal/accesslog/sink_test.go`.

- [ ] **Step 1: Write deterministic sink tests.**

Use channel-controlled writers for exact capacity, FIFO order, concurrent producers, nonblocking drop-new, partial writes, `io.ErrShortWrite`, ordinary error, panic, future-submit failure, close/drain, repeated close, submit racing close, shutdown timeout, and worker release after timeout. Assert no writer call occurs after failed-output transition and queue depth returns to zero.

```go
func TestSinkDropsNewestWhenQueueIsFull(t *testing.T) {
    writer := newBlockingWriter()
    observer := newRecordingObserver()
    sink := NewSink(writer, observer, slog.New(slog.NewTextHandler(io.Discard, nil)), 1)
    sink.Submit(event("first"))
    writer.waitUntilEntered(t)
    sink.Submit(event("second"))
    sink.Submit(event("third"))
    observer.requireDrop(t, DropQueueFull, 1)
    writer.release()
    closeWithDeadline(t, sink)
    requireIDs(t, writer.Bytes(), "first", "second")
}
```

- [ ] **Step 2: Run the red tests.**

```text
go test ./internal/accesslog -run 'TestSink' -count=1
```

- [ ] **Step 3: Implement one-owner sink state.**

Use one channel with exact capacity and one worker. `Submit` first normalizes the candidate; normalization failure records one `event_invalid` drop and never reaches the queue. Protect admission/close/failure state so send cannot race channel close; request goroutines must never wait for the writer. The worker alone writes and changes queue depth after receive. On first write error or panic, emit one stable operational warning, count the current and queued events as `write_error`, drain without writes, and make future submissions drop immediately with the same reason.

Handle partial writes with a full-write helper; zero progress is `io.ErrShortWrite`. Recover only at the writer call boundary. Do not include the writer error text or event in logs.

- [ ] **Step 4: Implement bounded close semantics.**

`Close(ctx)` atomically stops admission, signals the worker without closing a channel that submitters may still select on, and waits on a done channel. When the caller deadline expires, account all events still owned by the sink as `shutdown_timeout` exactly once and return. The worker may remain in a blocked external `Write`; once released it exits without double accounting.

- [ ] **Step 5: Run focused race tests and commit.**

```text
gofmt -w internal/accesslog
go test ./internal/accesslog -run 'TestSink' -race -count=20
git diff --check
git add internal/accesslog
git commit -m "feat: add bounded access log sink"
```

## Task 4: Add request observations and proxy timing/source instrumentation

**Files:** Modify `internal/requestctx/context.go`, `internal/requestctx/context_test.go`, `internal/proxy/handler.go`, `internal/proxy/attempt.go`, `internal/proxy/attempt_transport_test.go`, `internal/proxy/route_transport.go`, `internal/proxy/response_forward.go`, `internal/proxy/response_forward_test.go`, `internal/proxy/websocket.go`, `internal/proxy/websocket_test.go`, `internal/proxy/handler_test.go`, and relevant plugin/retry tests that exercise terminal branches.

- [ ] **Step 1: Add observation and saturating-duration tests.**

Test all typed values, zero state, positive accumulation, negative duration rejection/clamping, and saturation at `time.Duration` maximum.

- [ ] **Step 2: Add a deterministic proxy clock.**

Extend `proxy.RuntimeOptions` with `Now func() time.Time`; default it to `time.Now`. Store it on `handler` and `routeTransport`. Pass it to `executeAttempts`, including the WebSocket path. Update direct tests to use a sequence clock rather than elapsed-time tolerances.

```go
started := now()
response, err := roundTrip(selection, attemptRequest)
state.AddUpstreamWait(now().Sub(started))
```

Measure every selected attempt through headers or terminal error, including retried attempts. Do not include selection or backoff time.

- [ ] **Step 3: Run the attempt timing tests red, then implement.**

```text
go test ./internal/requestctx ./internal/proxy -run 'TestObservation|TestExecuteAttempts.*Timing' -count=1
```

- [ ] **Step 4: Instrument final-response streaming.**

Measure from immediately before response-plugin execution through header/body/trailer forwarding return. Set `ResponseSourceUpstream` before processing the final upstream response. A response-plugin failure/short circuit sets `ResponseSourcePlugin`. A downstream write/copy failure sets `TerminationDownstreamWriteError` without replacing an already committed status.

- [ ] **Step 5: Classify every proxy terminal path.**

Add a table-driven test matrix for invalid handler-level input, invalid query, gateway not ready, 404, 405, body limit, request-plugin failure, request-plugin short circuit, response-plugin failure, one-attempt success, retry success, exhausted attempts, unhealthy upstream, TLS failure, timeout, client cancellation, and response copy failure. Each case asserts response source, stable error code, status, attempts, outcome, suppression, and termination.

Use `ResponseSourceGateway` for routing/validation and gateway-generated upstream errors; `ResponseSourcePlugin` for plugin decisions; `ResponseSourceUpstream` for final upstream responses.

- [ ] **Step 6: Classify WebSocket handshakes only.**

Successful upgrade sets source WebSocket, status 101, and `TerminationWebSocketUpgraded` before tunnel activation. Rejected handshakes retain actual ordinary HTTP classification. Do not retain the request context in tunnel goroutines or add tunnel bytes/duration to the event.

- [ ] **Step 7: Run proxy packages and commit.**

```text
gofmt -w internal/requestctx internal/proxy
go test ./internal/requestctx ./internal/proxy -count=1
go test ./internal/proxy -run 'Test.*(Retry|Forward|WebSocket|Cancel)' -race -count=10
git diff --check
git add internal/requestctx internal/proxy
git commit -m "feat: observe proxy access outcomes"
```

## Task 5: Implement HTTP lifecycle middleware and exact byte accounting

**Files:** Create `internal/accesslog/middleware.go`, `internal/accesslog/middleware_test.go`, `internal/accesslog/response_writer.go`, `internal/accesslog/response_writer_test.go`.

- [ ] **Step 1: Write request identity and one-event tests.**

Use an injected `io.Reader` and sequence clock. Assert UUIDv4 version/variant bits, generation before route matching, plugin replacement through request context, no automatic response header, entropy failure producing one bounded 500 event, and exactly one submission for normal return and panic propagation.

- [ ] **Step 2: Write byte/status/termination tests.**

Cover request reads smaller/larger than `Content-Length`, partial read with error, implicit 200, informational status followed by final status, repeated `WriteHeader`, short underlying write, post-status cancellation, pre-status cancellation with status zero, body/trailer copy, and saturating counters.

- [ ] **Step 3: Write optional-interface conformance tests.**

Exercise all 16 capability masks for `http.Flusher`, `http.Hijacker`, `http.Pusher`, and `io.ReaderFrom`. The wrapper factory must expose exactly the interfaces supported by its underlying writer, and `Unwrap` must preserve `http.ResponseController`. Verify optimized `ReadFrom` bytes are counted.

- [ ] **Step 4: Run red middleware tests.**

```text
go test ./internal/accesslog -run 'TestMiddleware|TestResponseWriter|TestOptionalInterfaces' -count=1
```

- [ ] **Step 5: Implement request and response wrappers.**

The request wrapper counts bytes actually returned by `Read`. The base response wrapper records the first final status at least 200, implicit 200, and bytes reported by the underlying writer. Build finite wrapper variants selected by a four-bit capability mask; do not attach methods for capabilities the underlying writer lacks.

- [ ] **Step 6: Implement middleware finalization.**

At admission: generate UUID, set request-context ID, capture UTC timestamp and monotonic start, then wrap request/response. In a defer: derive missing termination from context cancellation or completed return, prefer the proxy's explicit context status when hijacking bypassed the wrapper, build the immutable Event, and submit once. Re-panic after submission if an inner panic escapes.

- [ ] **Step 7: Run package race tests and commit.**

```text
gofmt -w internal/accesslog
go test ./internal/accesslog -count=1
go test ./internal/accesslog -race -count=20
git diff --check
git add internal/accesslog
git commit -m "feat: observe complete http request lifecycles"
```

## Task 6: Register fixed-cardinality metrics

**Files:** Modify `internal/telemetry/telemetry.go`, `internal/telemetry/telemetry_test.go`. Add `internal/telemetry/accesslog.go`.

- [ ] **Step 1: Add metric-shape tests.**

Assert all access-log series exist at zero immediately after `telemetry.New`, even when access logging is disabled. Exercise every allowed result/reason, queue depth transitions, and assert exact series counts and absence of request/resource values.

- [ ] **Step 2: Run the red tests.**

```text
go test ./internal/telemetry -run 'TestAccessLogMetrics' -count=1
```

- [ ] **Step 3: Implement the accesslog.Observer methods.**

Register:

```text
gateway_access_log_events_total{result="written|dropped"}
gateway_access_log_dropped_total{reason="queue_full|event_invalid|encode_error|write_error|shutdown_timeout"}
gateway_access_log_queue_depth
```

Pre-bind all counter vectors in `New`; observer methods switch over typed constants and never accept arbitrary label text. Keep the existing isolated registry and Admin exposure.

- [ ] **Step 4: Run telemetry tests and commit.**

```text
gofmt -w internal/telemetry
go test ./internal/telemetry -count=1
git diff --check
git add internal/telemetry
git commit -m "feat: expose bounded access log metrics"
```

## Task 7: Wire sink ownership, panic observation, and bounded shutdown

**Files:** Modify `internal/gateway/gateway.go`, `internal/gateway/gateway_test.go`, `internal/gateway/response_state.go`, `cmd/gateway-dp/main.go`. Create `internal/gateway/phase3d1_lifecycle_test.go`.

- [ ] **Step 1: Add constructor and disabled-bypass tests.**

Assert old `New` succeeds for legacy/disabled configurations with no writer, enabled `New` fails before listeners, enabled `NewWithOptions` accepts a writer, and a failed later startup closes the sink. The disabled chain must prove no UUID, clock, wrapper, goroutine, or writer activity.

- [ ] **Step 2: Add panic and chain-order tests.**

An uncommitted panic yields status 500, source gateway, termination panic, stable error code, and one event. A committed panic preserves status/body bytes, records panic, then preserves the existing `http.ErrAbortHandler` behavior. Operational logs may retain the existing bounded panic record; access JSON must contain no panic value or stack.

- [ ] **Step 3: Implement compatible gateway construction.**

Move current `New` body into `NewWithOptions`; let `New` delegate. Create telemetry first, create the sink only when enabled, and install deferred bounded cleanup until ownership transfers to `Gateway`. Pass the injected proxy clock only in tests through package-private assembly helpers; production defaults to `time.Now`.

- [ ] **Step 4: Compose enabled and disabled traffic chains explicitly.**

Keep a separate disabled branch with the pre-3D1 composition. For the enabled branch use the approved chain. Update `recoverPanics` to set request-owned source/termination/error status before writing or rethrowing.

- [ ] **Step 5: Add lifecycle ordering tests.**

Prove new traffic is refused during drain, active HTTP handlers finish before sink admission closes, WebSocket tunnel shutdown does not retain handshake events, runtime leases close once, accepted events flush in FIFO order, blocked writer cannot hold shutdown past its context/five-second access drain bound, and repeated shutdown is idempotent.

Use a five-second access-drain child context capped by any shorter caller deadline:

```go
drainCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
defer cancel()
g.accessSink.Close(drainCtx)
```

Do not extend an already expired caller deadline and do not make access failure part of the returned readiness state.

- [ ] **Step 6: Route stdout and stderr at the process boundary.**

Change the command seam to `run(arguments []string, stdout, stderr io.Writer)`; `main` passes `os.Stdout, os.Stderr`. The gateway logger uses stderr and access output uses stdout. Update all direct command tests/callers.

- [ ] **Step 7: Run gateway lifecycle tests and commit.**

```text
gofmt -w internal/gateway cmd/gateway-dp
go test ./internal/gateway ./cmd/gateway-dp -count=1
go test ./internal/gateway -run 'TestPhase3D1|TestRecoverPanics|TestShutdown' -race -count=20
git diff --check
git add internal/gateway cmd/gateway-dp
git commit -m "feat: wire bounded access logging lifecycle"
```

## Task 8: Add end-to-end HTTP, TLS, WebSocket, and failure acceptance

**Files:** Create `test/integration/access_log_test.go`. Modify shared test helpers in `test/integration/gateway_test.go`, `test/integration/process_test.go`, and `test/integration/websocket_test.go` only where needed.

- [ ] **Step 1: Add a JSONL capture helper.**

Start a gateway with enabled v1alpha8 bootstrap and injected synchronized buffer. Decode lines strictly into maps/typed test records, reject duplicate/unknown schema keys, and provide a deadline-based channel notification instead of polling sleeps.

- [ ] **Step 2: Cover the required request matrix.**

One table must prove exactly one event per request for malformed handler-level request, invalid query, 404, 405, matched success, gateway not ready, oversized request, request/response plugin failure, plugin short circuit, retry success, exhausted retry, unhealthy upstream, TLS failure, timeout, cancellation, and streaming body/trailers. Assert the externally visible response and the matching bounded event together. Downstream-writer failure and panic remain deterministic gateway/package acceptance cases from Tasks 5 and 7 because production exposes no test-only endpoint that triggers them.

- [ ] **Step 3: Cover protocols and WebSocket.**

Run success through HTTP/1.1 plaintext and HTTP/2 over TLS. Prove a successful WebSocket handshake emits one 101 `websocket_upgraded` event immediately and no event on tunnel frames/close. Prove rejected handshake emits one ordinary event.

- [ ] **Step 4: Cover process stream separation.**

Build/run `cmd/gateway-dp` through the existing process harness with temporary TLS material. Send traffic, then assert stdout contains only valid access JSON lines and stderr contains operational logs with no access JSON. Do not commit captured output.

- [ ] **Step 5: Cover saturation and output failure under traffic.**

Use a controlled slow writer to saturate the exact queue, verify traffic continues, drop metrics rise with fixed labels, readiness remains healthy, and shutdown stays bounded. Repeat with failing and panicking writers.

- [ ] **Step 6: Run integration and race checks, then commit.**

```text
gofmt -w test/integration
go test ./test/integration -run 'TestAccessLog' -count=1
go test ./test/integration -run 'TestAccessLog' -race -count=10
git diff --check
git add test/integration
git commit -m "test: cover phase 3d1 access logging"
```

## Task 9: Add overhead and lifecycle evidence

**Files:** Create `internal/accesslog/benchmark_test.go`, `internal/gateway/phase3d1_benchmark_test.go`, `docs/benchmarks/phase-3d1-current-status.md`.

- [ ] **Step 1: Add reproducible benchmarks.**

Benchmark canonical encoding, disabled handler path, enabled path with a draining discard writer, and slow-writer saturation. Report allocations and drops. Do not add a pass/fail performance threshold or APISIX comparison.

```text
go test ./internal/accesslog ./internal/gateway -run '^$' -bench 'Benchmark(AccessLog|Phase3D1)' -benchmem -count=5
```

- [ ] **Step 2: Add repeated lifecycle evidence tests.**

Run at least 100 construct/request/shutdown cycles with controlled writers. After release/drain, require queue depth zero, no retained event buffers, stable owned worker count, and runtime/tunnel lifecycle observers at their steady-state values. Use explicit synchronization; goroutine counts are supporting evidence, not the only assertion.

```text
go test ./internal/gateway -run 'TestPhase3D1LifecycleReturnsToSteadyState' -race -count=20
```

- [ ] **Step 3: Record only observed evidence.**

Write the exact OS/architecture, Go version, commit, commands, benchmark aggregates, drop behavior, heap/allocation observations, and limitations. State that Phase 3D2 and APISIX parity remain outstanding.

- [ ] **Step 4: Commit evidence code and ledger.**

```text
git add internal/accesslog/benchmark_test.go internal/gateway/phase3d1_benchmark_test.go docs/benchmarks/phase-3d1-current-status.md
git commit -m "bench: record phase 3d1 access log overhead"
```

## Task 10: Complete operations documentation and repository status

**Files:** Create `docs/operations/phase-3d1-runbook.md`. Modify `README.md`, `docs/superpowers/specs/2026-07-21-go-native-api-gateway-phase-roadmap-design.md`, and this plan/spec status only after evidence exists.

- [ ] **Step 1: Write the operator runbook.**

Document `configs/phase3d1.yaml`, stdout JSONL versus stderr operational logs, schema fields, endpoint hashing, forbidden data, queue defaults/bounds, drop-new behavior, all drop metrics/reasons, readiness independence, writer-failure behavior, five-second drain, alert guidance, and exact local verification commands.

- [ ] **Step 2: Update delivered-scope statements from evidence.**

README and roadmap must distinguish implemented 3D1 behavior, locally observed evidence, CI evidence, and remaining 3D2 work. Do not mark Phase 3 complete and do not claim APISIX comparison. Mark the Phase 4A gate open only after all required CI checks below pass on the final tree.

- [ ] **Step 3: Validate documentation references and examples.**

```text
rg -n "phase3d1|gateway/v1alpha8|gateway_access_log|Phase 3D1" README.md configs docs
go test ./internal/config -run 'TestPhase3D1Example' -count=1
git diff --check
```

- [ ] **Step 4: Commit the documentation.**

```text
git add README.md docs/operations/phase-3d1-runbook.md docs/benchmarks/phase-3d1-current-status.md docs/superpowers/specs/2026-09-20-phase-3d1-bounded-access-logging-design.md docs/superpowers/specs/2026-07-21-go-native-api-gateway-phase-roadmap-design.md configs/phase3d1.yaml
git commit -m "docs: publish phase 3d1 operations guidance"
```

## Task 11: Run the completion gate and open Phase 4A

**Files:** Modify evidence/status documents only if commands produce new results. Modify `.github/workflows/ci.yml` only if inspection shows the required Linux race/build/image checks are absent; the current workflow is expected to supply them.

- [ ] **Step 1: Inspect the final diff and configuration compatibility.**

```text
git status --short
git diff --stat 6f93a92...HEAD
git diff --check
gofmt -l .
```

Any changed Go file listed by `gofmt -l` is a failure. Pre-existing unrelated files must be identified rather than silently reformatted.

- [ ] **Step 2: Run static analysis and all tests.**

```text
staticcheck -tests=false ./...
revive -set_exit_status -config revive.toml -formatter default ./...
go vet ./...
go test ./... -count=1
go test ./... -race -count=1
go build ./cmd/...
```

The race run required to open Phase 4A must pass on Linux CI. A local Windows result is additional evidence and does not substitute for that CI job.

- [ ] **Step 3: Build the production gateway image.**

```text
docker build --build-arg COMMAND=gateway-dp -t gateway-go:ci .
```

- [ ] **Step 4: Review the required CI workflow and result.**

Confirm CI runs formatting, staticcheck, revive, vet, normal tests, Linux race tests, command builds, and the gateway Docker build against the completed commit. If workflow coverage already exists, do not edit it. Record job URLs/identifiers and exact failures or skips in the status ledger.

- [ ] **Step 5: Perform the final security and cardinality audit.**

Search for accidental access to request URL/header/body/client address and for dynamic Prometheus labels:

```text
rg -n "RequestURI|URL\\.|Header\\.|RemoteAddr|Authorization|Cookie|WithLabelValues" internal/accesslog internal/gateway internal/telemetry
rg -n "os\\.Stdout|os\\.Stderr" internal cmd/gateway-dp
```

Every match must be explained by bounded construction, tests, or the process composition root. Decode representative test events and verify no raw upstream URL appears.

- [ ] **Step 6: Request code review and resolve findings.**

Use `superpowers:requesting-code-review`. Check every finding against the approved spec and actual code; use `superpowers:receiving-code-review` before applying review changes. Re-run the smallest affected test and then the completion gate affected by the change.

- [ ] **Step 7: Mark the gate from evidence and commit.**

Only after all completion criteria and Linux CI evidence pass:

- mark Phase 3D1 implementation complete in its spec/status ledger;
- record the accepted baseline commit and CI evidence in the Phase 4A plan execution gate;
- state that Phase 3D2 remains pending;
- allow Phase 4A executable-code work to begin.

```text
git add README.md docs/benchmarks/phase-3d1-current-status.md docs/superpowers/specs/2026-09-20-phase-3d1-bounded-access-logging-design.md docs/superpowers/specs/2026-07-21-go-native-api-gateway-phase-roadmap-design.md docs/superpowers/plans/2026-09-20-phase-3d1-bounded-access-logging.md docs/superpowers/plans/2026-09-20-phase-4a-configuration-management.md
git commit -m "docs: open phase 4a implementation gate"
git status --short
```

Expected final status: clean. If any required check cannot run or fails, keep the 4A gate closed and record the exact limitation without weakening the criterion.

## Spec coverage audit

| Approved requirement | Implemented/tested in |
| --- | --- |
| strict v1alpha8; legacy versions unchanged | Task 1 |
| disabled path has no logging overhead machinery | Tasks 1, 7 |
| fixed schema, bounds, hashing, forbidden fields | Task 2 |
| endpoint canonical identity never leaks URL | Task 2 |
| exact queue, nonblocking drop-new, writer failure/panic | Task 3 |
| request-owned proxy observations and timing | Task 4 |
| gateway/plugin/upstream/WebSocket source classification | Task 4 |
| actual request/response bytes and status | Task 5 |
| UUID before matching and plugin replacement | Task 5 |
| optional writer interfaces and controller behavior | Task 5 |
| fixed-cardinality metrics, readiness independence | Tasks 6, 8 |
| panic inside access observation | Task 7 |
| stdout/stderr separation and explicit writer injection | Tasks 7, 8 |
| every handler outcome emits exactly one event | Tasks 5, 8 |
| HTTP/1.1, HTTP/2 TLS, streaming, trailers, WebSocket | Task 8 |
| bounded lifecycle, repeatability, overhead evidence | Tasks 7, 9 |
| operational docs and honest 3D2 boundary | Tasks 9, 10 |
| Linux race/build/image completion gate before 4A | Task 11 |

No task contains a placeholder implementation, deferred core behavior, or alternate test-only production path. Exact implementation details that remain local choices—private struct layout, helper names, and test fixture organization—do not change these contracts.

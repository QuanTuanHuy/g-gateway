# Phase 3D1 Bounded Access Logging Design

**Date:** 2026-09-20

**Status:** Design approved in discussion; pending written-spec review. No implementation or acceptance evidence is claimed.

**Roadmap parent:** [Go-native API gateway phase roadmap](2026-07-21-go-native-api-gateway-phase-roadmap-design.md).

## 1. Outcome and phase boundary

Phase 3D is split into two independently reviewable slices:

1. **Phase 3D1** adds bounded structured access logging for every request that reaches a traffic listener.
2. **Phase 3D2** owns integrated resilience acceptance, reference-Linux canonical reproduction, and the integrated APISIX comparison.

Phase 3D1 is the implementation gate for Phase 4A. Phase 4A may begin only after 3D1 is implementation-complete with the CI evidence in section 13. Phase 3D2 remains required before claiming the whole of Phase 3 or the gateway's APISIX comparison accepted. This gate revision was approved by the user on 2026-09-20.

Phase 3D1 emits one access event for every HTTP request admitted by the plaintext or TLS traffic listener, including malformed requests that reach the handler, invalid queries, route misses, method mismatches, plugin failures and short circuits, upstream success/failure/retry, client cancellation, panic recovery, and WebSocket handshakes. Requests rejected by `net/http` before handler invocation cannot produce an application access event and remain server-level operational events.

A successful WebSocket upgrade emits one HTTP event at handshake completion with status 101. Tunnel duration and tunnel bytes remain in existing bounded WebSocket telemetry; the access logger does not retain request state for the tunnel lifetime.

Phase 3D1 does not add request/response body capture, header logging, client identity logging, tracing, file rotation, external collectors, sampling, per-route log policy, dynamic log configuration, or APISIX performance claims.

## 2. Existing seams

The current traffic chain is assembled in `internal/gateway/gateway.go`. `requestctx.Middleware` attaches one mutable request-owned context; `telemetry.Wrap` records bounded request metrics; `proxy.handler` owns matching, plugins, retry, streaming, and WebSocket handshake behavior; `recoverPanics` turns request panics into bounded responses; `Gateway.Shutdown` already drains traffic and tunnels.

`requestctx.Context` already contains revision, route/service/upstream metadata, selected endpoint, attempt counts, retry suppression, upstream outcome, request ID, response status, and stable response error. Phase 3D1 extends that observational seam instead of reconstructing proxy state from logs or HTTP headers.

Operational lifecycle logging continues through `slog` to stderr. Access events use a separate stdout JSON Lines stream so container operators can route traffic records independently. The gateway package receives an `io.Writer` from `cmd/gateway-dp`; reusable packages do not access `os.Stdout` directly.

## 3. Architecture and ownership

Create `internal/accesslog` with three focused responsibilities:

| Unit | Responsibility |
| --- | --- |
| `Event` | Immutable versioned access record with bounded, redacted fields |
| Middleware | Observe one complete HTTP handler lifecycle, count actual body bytes, classify termination, and submit one event |
| `Sink` | Own one bounded queue and worker, encode JSON Lines, write to the injected writer, expose bounded statistics, and drain on shutdown |

The request context remains the communication seam between the proxy and middleware. Add an observation value that tracks only data the proxy can measure accurately: cumulative upstream-header wait, final response-stream time, response source, and terminal classification. The proxy does not import JSON, stdout, queue, or access-log sink logic.

The enabled traffic chain is:

```text
track traffic
  -> attach request context
    -> access-log middleware
      -> panic recovery
        -> bounded request metrics
          -> proxy
```

Panic recovery is inside access observation so its final 500 response and stable panic classification are visible before the event is finalized. The access logger itself must not panic for input-dependent data. Its constructors validate required dependencies before listeners bind.

When access logging is disabled, the gateway uses the existing handler composition without access UUID generation, counting wrappers, timers, queue, or worker. The disabled path must preserve current allocations and behavior apart from the new configuration version decoder.

`Gateway` owns the sink. Construction failure closes any already-created runtime resources. Shutdown stops traffic admission, waits for active HTTP handlers through the existing traffic lifecycle, drains tunnels, closes sink admission, and gives the access worker at most five seconds to flush accepted events. A blocked writer cannot block request handlers or keep process shutdown waiting beyond that bound.

## 4. Configuration and compatibility

Add strict standalone configuration version `gateway/v1alpha8`. It inherits the complete v1alpha7 document and adds:

```yaml
telemetry:
  request_metrics_enabled: true
  profiling_enabled: false
  access_log:
    enabled: true
    queue_capacity: 4096
```

`enabled` defaults to false. `queue_capacity` defaults to 4096 when access logging is enabled and the field is absent or zero after presence-aware conversion. An explicit negative value or a value above 65536 fails validation. The canonical runtime value is always within 1 through 65536 for an enabled logger. When disabled, a nonzero queue capacity is rejected rather than silently ignored.

All configuration versions v1alpha1 through v1alpha7 continue to decode with access logging disabled. Unknown and duplicate YAML fields, multiple documents, invalid types, and unsupported versions retain strict rejection. The v1alpha8 example contains no credentials or generated artifacts and explicitly enables access logging.

The output destination, JSON schema, event-size limit, overflow policy, and five-second drain limit are fixed in 3D1. File paths, rotation, sampling ratios, blocking delivery, and alternate formats are deliberately absent.

## 5. Event schema

Every emitted line is one JSON object with `schema` equal to `gateway.access/v1`. Field order is stable for tests and operational readability, but consumers must use JSON names rather than positional parsing.

| Field | Type | Meaning |
| --- | --- | --- |
| `schema` | string | Always `gateway.access/v1` |
| `timestamp` | RFC3339Nano UTC string | Handler admission time |
| `request_id` | string | Ingress UUID or final validated request-ID plugin value |
| `revision` | uint64 | Retained runtime revision; zero before a snapshot/match is available |
| `method` | string | Bounded HTTP method |
| `protocol` | string | Bounded request protocol such as `HTTP/1.1` or `HTTP/2.0` |
| `route_id` | string | Matched route ID or empty |
| `service_id` | string | Resolved service ID or empty |
| `upstream_id` | string | Resolved upstream ID or empty |
| `endpoint_id` | string | Stable selected endpoint identity or empty; never an endpoint URL |
| `response_source` | string | `gateway`, `plugin`, `upstream`, or `websocket` |
| `status` | integer | Final status actually committed; zero if none was sent |
| `request_body_bytes` | uint64 | Body bytes actually read by the gateway |
| `response_body_bytes` | uint64 | Body bytes successfully written through the HTTP writer before return |
| `attempts` | integer | Total upstream attempts |
| `upstream_outcome` | string | Existing bounded outcome or empty |
| `retry_suppressed` | string | Existing bounded suppression reason or empty |
| `error_code` | string | Stable gateway error code or empty |
| `termination` | string | Closed terminal classification |
| `duration_us` | uint64 | Handler admission through handler return |
| `upstream_wait_us` | uint64 | Sum of attempt waits through upstream headers or terminal attempt error |
| `response_stream_us` | uint64 | Final upstream headers through body/trailer copy completion |

The terminal classifications are exactly `completed`, `client_canceled`, `downstream_write_error`, `panic`, and `websocket_upgraded`. Successful WebSocket handshake uses `websocket_upgraded`, status 101, response source `websocket`, and counts no tunnel bytes. A rejected handshake uses its actual HTTP status and ordinary completion/error classification.

`response_source=gateway` covers routing/validation failures and gateway-generated upstream errors. `plugin` covers short circuits and plugin failures. `upstream` covers a final upstream response. The source is independent of whether the status is successful.

Time values use integer microseconds and saturate rather than wrap. Total duration uses one injected monotonic-capable clock source in tests and `time.Now`/`time.Since` in production. `upstream_wait_us` sums each selected attempt's call through response headers or error, including attempts later retried. `response_stream_us` starts only for the final upstream response and includes response plugin work plus downstream body/trailer copy. The design does not publish a derived `gateway_duration` because subtracting overlapping or absent intervals would be misleading.

## 6. Correlation identity

When access logging is enabled, middleware generates a UUIDv4 at handler admission from `crypto/rand` and assigns it to the fresh request context. This guarantees an access identity for invalid, unmatched, and method-mismatched requests.

The existing `request-id` plugin retains its behavior. For a matched route it may replace the ingress UUID with one valid inbound value or its own generated UUID, and it remains solely responsible for adding the configured response header. The access event reads the final request-context value. Enabling access logging does not expose a request-ID response header on routes that do not configure the plugin.

Entropy failure is classified as an internal gateway failure for that request, produces a 500 if no status is committed, and records a bounded error code. No predictable fallback ID is generated.

## 7. Redaction and bounds

Access events never contain raw path, query, host, headers, cookies, authorization data, request/response bodies, client IP, upstream URL, certificate data, raw errors, stack traces, plugin configuration, or secret material.

Every string field is valid UTF-8 and at most 256 encoded bytes. Values within the bound are preserved. Longer or invalid values are represented as `sha256:` followed by the lowercase SHA-256 hex digest of the original bytes. Hashing is only a bounded correlation representation and is not presented as encryption. Closed enum values that are unknown due to a programming defect make the event invalid and therefore dropped as `event_invalid`; arbitrary values never become metric labels.

Each encoded event, including the newline, is at most 4096 bytes. Construction validates the complete bound before enqueue. A record that cannot satisfy the fixed schema bound is dropped as `event_invalid`; it is never truncated into malformed JSON. Integer counters use saturating addition.

Resource and endpoint IDs are configuration identities, not metric labels. The event stream may contain their bounded representation; Prometheus series never do.

## 8. HTTP observation semantics

The request-body wrapper counts bytes returned to gateway consumers, including bytes read before an oversized-body failure. It does not trust `Content-Length`. The response wrapper records the first final status at or above 200, treats an implicit first write as 200, and adds only the number of bytes successfully reported by the underlying writer.

The wrapper preserves `http.ResponseController` behavior through `Unwrap` and the optional interfaces needed by the repository's HTTP/1.1, HTTP/2, streaming, flushing, and WebSocket code. Tests must prove `Flush`, `Hijack`, `Push`, and optimized copy behavior where the underlying writer supports them. It must not advertise an optional interface that the underlying writer lacks.

Proxy attempt timing is measured around each `selection.RoundTrip` call. Final stream timing is measured around response-plugin execution plus copying the final body and trailers. The proxy writes response source and stable terminal metadata at the point it makes the decision; the middleware owns the fallback classification for unmatched and early failures.

If a client cancels before a final status, the event uses status zero and `client_canceled`. If cancellation occurs after a status is committed, the status remains and termination still explains the interrupted lifecycle. A downstream body write failure uses `downstream_write_error`. Panic recovery writes the existing safe response when possible, marks `panic`, logs the operational panic through the existing bounded channel, and never adds panic text to the access event.

Only requests reaching the traffic handler are in scope. TLS handshake failures, oversized headers rejected by `net/http`, malformed request lines, and listener accept errors remain outside application access logging because no safe request context exists.

## 9. Queue, worker, and output

`Sink` owns a buffered channel sized exactly to the configured queue capacity and one worker. Submission is a nonblocking select. If capacity is unavailable, the new event is dropped; an accepted older event is never evicted to admit it.

The worker encodes one immutable event to one JSON line and writes it to the injected writer. Encoding happens off the request path. Request-owned pointers, headers, URLs, contexts, bodies, and mutable slices never enter the queue. Queue slots therefore have an explicit bounded memory envelope derived from the Event struct and its bounded strings.

The first writer error moves the sink to a failed-output state. The worker stops calling the failed writer, accounts the failed event as `write_error`, drains queued events as dropped with the same fixed reason, and makes future submissions fail immediately without blocking. This avoids a hot retry loop. Readiness and traffic behavior do not change because access output is telemetry.

The worker guards its boundary so a panicking injected writer cannot crash the process. It records a bounded operational failure and enters the same failed-output state. Production uses `os.Stdout`; test writers exercise slow, failing, partial, and panicking behavior.

Closing the sink is idempotent: stop admission, let the worker drain accepted events, and wait up to five seconds. On timeout, account queued/in-flight events with `shutdown_timeout`, emit one bounded operational warning, and return control to gateway shutdown. An arbitrary blocked `io.Writer.Write` cannot be canceled; the process may leave that one worker blocked until process exit, but no request or graceful-shutdown step waits past the deadline. Tests use controllable writers and verify no leak when the writer returns.

## 10. Metrics and operational logging

Add these metrics to the existing isolated telemetry registry:

- `gateway_access_log_events_total{result="written|dropped"}`;
- `gateway_access_log_dropped_total{reason="queue_full|event_invalid|encode_error|write_error|shutdown_timeout"}`;
- `gateway_access_log_queue_depth`.

All label values are pre-bound closed sets. Do not label metrics with route, service, upstream, endpoint, request ID, revision, method, status, or configuration input. Metrics are registered regardless of access-log enablement so dashboards do not change shape; disabled mode leaves them at zero.

Operational warnings use existing stderr `slog` and include only stable code, queue counts, and fixed reason. They do not include the rejected Event, writer error text, request data, or hashes. Repeated output failures are coalesced into one state transition rather than logged per event.

## 11. Process wiring and lifecycle

Change gateway construction to accept an explicit access-log writer option. `cmd/gateway-dp` supplies stdout. Tests supply `bytes.Buffer`, controlled writers, or nil when logging is disabled. Enabling access logging with no writer is a construction error before listeners bind.

Startup order is telemetry, access sink, TLS/runtime dependencies, proxy, then listeners. Failure after sink creation closes it with a bounded cleanup context. Normal shutdown first prevents new traffic, waits for current HTTP handlers according to existing server shutdown semantics, drains WebSocket tunnels, closes runtime resources, and drains access output. The implementation must define one owner for each close and remain idempotent under partial startup and repeated shutdown calls.

Access-output failure does not make readiness false. Queue depth and drops show degraded observability. The runbook tells operators to alert on any sustained drop and to keep stdout consumers fast enough for the declared workload.

## 12. Verification strategy

Package-local tests cover:

- strict v1alpha8 decode, default/presence rules, queue limits, unknown/duplicate fields, and v1alpha1-v1alpha7 compatibility;
- golden JSON field names/order and one-line encoding;
- every redacted forbidden input class and the 256-byte/hash/4096-byte boundaries;
- UUID behavior before match and request-ID plugin replacement after match;
- response status, actual byte counters, saturating counters, and duration semantics;
- nonblocking enqueue, exact capacity, drop-new ordering, concurrent producers, writer partial/error/panic, failed-state transition, idempotent close, drain, and timeout;
- fixed metric labels and absence of user-controlled metric values;
- optional response-writer interfaces and disabled-mode bypass.

Proxy/gateway/integration tests cover:

- malformed handler-level request, invalid query, 404, 405, matched success, gateway not ready, request too large;
- plugin request/response failure and short circuit;
- one attempt, retry then success, exhausted attempts, unhealthy upstream, TLS failure, timeout, and cancellation;
- streaming bodies/trailers, downstream write failure, HTTP/1.1, HTTP/2 over TLS, and panic recovery;
- successful/rejected WebSocket handshakes and proof that tunnel bytes/duration are not held by access logging;
- startup failure cleanup, graceful shutdown, queue saturation during traffic, and repeated lifecycle return to steady state.

Concurrency tests use channels and controllable clocks/writers instead of sleeps. Race tests cover sink submission/close, gateway shutdown, cancellation, retry state, and writer failure. Repetition proves queue depth, goroutines, and owned buffers return to steady state after the writer is released and drain completes.

Benchmarks compare the existing disabled path, enabled path with a draining discard writer, slow-writer saturation, and JSON encoding. They report throughput, p99, allocations, queue drops, heap, and goroutine state. Phase 3D1 records these as local overhead evidence and does not set or claim an APISIX parity threshold.

## 13. Completion and Phase 4 gate

Phase 3D1 is implementation-complete only when:

1. v1alpha8 and the checked-in Phase 3D1 example strictly load;
2. every traffic-handler outcome produces exactly one bounded event when enabled;
3. redaction, byte/timing semantics, queue failure modes, and shutdown behavior pass their tests;
4. disabled logging preserves existing behavior and all standalone regression suites remain green;
5. formatting, staticcheck, revive, vet, normal tests, Linux race tests, command builds, and the gateway image build pass on the completed tree;
6. lifecycle repetition shows bounded queue/memory/goroutine ownership and documents local benchmark overhead;
7. the runbook and current-status ledger report the exact commands and observed limitations without claiming 3D2 evidence.

Only after those criteria are recorded may Phase 4A executable-code work begin. A design document, implementation plan, partial local tests, or a platform where the required Linux race job was skipped does not satisfy this gate.

Phase 3D2 remains responsible for reference-Linux integrated resilience, deferred canonical evidence consolidation, and APISIX comparison. Starting Phase 4A after 3D1 does not mark Phase 3, Phase 3C, or APISIX parity complete.

## 14. Documentation deliverables

Implementation adds:

- `configs/phase3d1.yaml` with v1alpha8 and enabled access logging;
- `docs/operations/phase-3d1-runbook.md` with JSON contract, stdout/stderr routing, queue/drop alerts, shutdown behavior, redaction boundary, and validation commands;
- `docs/benchmarks/phase-3d1-current-status.md` with exact local/CI evidence and remaining 3D2 work;
- README and roadmap updates describing actual delivered behavior and claims boundary.

No raw access-log corpus, credentials, certificates, profiles, or benchmark result directories are committed.

## 15. References

- [Accepted gateway architecture](../../architecture/apache-api-six-architecture-design.md)
- [Original Go-native architecture](2026-07-21-go-native-api-gateway-design.md)
- [Phase roadmap](2026-07-21-go-native-api-gateway-phase-roadmap-design.md)
- [Phase 3B resilience design](2026-07-27-phase-3b-health-timeout-retry-design.md)
- [Phase 3C1 protocol design](2026-07-30-phase-3c1-upstream-tls-protocol-design.md)
- [Phase 3C3 WebSocket design](2026-07-31-phase-3c3-websocket-lifecycle-design.md)
- [Phase 3C2 downstream TLS design](2026-09-13-phase-3c2-downstream-sni-certificate-rotation-design.md)
- [Phase 4 umbrella](2026-09-20-phase-4-control-plane-design.md)
- [Phase 4A specification](2026-09-20-phase-4a-configuration-management-design.md)
- [Phase 4A implementation plan](../plans/2026-09-20-phase-4a-configuration-management.md)

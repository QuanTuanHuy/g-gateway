# Phase 3C3 WebSocket Lifecycle Design

**Date:** 2026-07-31

**Status:** Approved design

**Parent:** [Go-native API Gateway phase roadmap](2026-07-21-go-native-api-gateway-phase-roadmap-design.md)

**Preceded by:** [Phase 3C1 upstream TLS and protocol](2026-07-30-phase-3c1-upstream-tls-protocol-design.md)

**Followed by:** Phase 3C2 dynamic downstream SNI certificates, then Phase 3D integrated acceptance

## 1. Decision summary

Phase 3C3 adds opt-in RFC 6455 WebSocket proxying over downstream HTTP/1.1 and upstream HTTP/1.1. It preserves the existing route, service, plugin, balancing, health, retry, timeout, TLS, and immutable-snapshot semantics until a valid upstream `101 Switching Protocols` response is ready. At that point it transfers the two network streams to a dedicated tunnel runtime with idle timeout, half-close, reload independence, bounded-cardinality telemetry, and graceful shutdown ownership.

The selected architecture is a dedicated Upgrade Executor beside the existing HTTP reverse proxy. It reuses a shared attempt executor and native Go transports but does not delegate upgraded-connection ownership to `httputil.ReverseProxy`. This is required so an established tunnel can release the full runtime snapshot, retain only its selected transport generation, and participate in explicit idle and shutdown drain behavior.

Phase 3C3 is intentionally implemented before Phase 3C2. To prevent later listener rework, it introduces a minimal downstream certificate-provider seam while retaining the current static startup certificate. Phase 3C2 will replace only that provider with an atomic exact/wildcard SNI selector.

## 2. Context

Phase 3C1 completed outbound TLS/mTLS, HTTP/2 over TLS, h2c, native gRPC pass-through, and transport-generation rotation. The current gateway still rejects every request containing `Upgrade` or `Connection: upgrade`, uses one static downstream certificate loaded during process construction, and lets one request lease retain the complete immutable runtime snapshot until its handler returns.

Those behaviors are unsuitable for WebSocket traffic:

- an upgraded connection may live for hours or days;
- `http.Server.Shutdown` does not own hijacked connections;
- holding a full snapshot for the tunnel lifetime prevents unrelated retired state from reaching steady state;
- HTTP request duration must measure the handshake rather than tunnel lifetime;
- classic WebSocket Upgrade requires HTTP/1.1 even when ordinary TLS traffic uses HTTP/2;
- reload and shutdown races must not orphan either side of a tunnel.

The design therefore separates the finite HTTP handshake transaction from the long-lived opaque byte tunnel.

## 3. Goals

Phase 3C3 must provide:

1. explicit Route/Service WebSocket enablement with deterministic inheritance;
2. RFC 6455 handshake validation for downstream HTTP/1.1;
3. upstream WebSocket over cleartext HTTP/1.1 and TLS HTTP/1.1;
4. reuse of request plugins, response plugins, balancing, passive health, replay-safe retry, retry budget, deadlines, and upstream TLS verification before `101`;
5. an established-tunnel lease that does not retain the full runtime snapshot;
6. bidirectional opaque copying with backpressure, idle timeout, and best-effort half-close;
7. config updates that affect new handshakes without terminating existing tunnels;
8. graceful shutdown that drains tunnels until the process deadline, then force-closes them;
9. bounded-cardinality telemetry without payload or secret exposure;
10. a static downstream certificate provider whose interface can receive Phase 3C2 SNI selection without listener changes;
11. correctness, concurrency, lifecycle, fuzz, and relative performance evidence.

## 4. Non-goals

Phase 3C3 does not add:

- RFC 8441 Extended CONNECT or WebSocket over HTTP/2;
- generic CONNECT, TCP, or UDP tunneling;
- WebSocket frame parsing, transformation, reconnect, or session migration;
- gateway-generated ping, pong, or close frames;
- tunnel admission quotas or per-route connection limiting;
- dynamic downstream exact/wildcard SNI certificate selection or rotation;
- a public configuration update API or file watcher;
- per-request or per-tunnel access logging;
- an integrated APISIX comparison or production certification.

The gateway forwards extensions negotiated by the endpoints but does not interpret them. Origin authorization remains the responsibility of request plugins or the upstream application.

## 5. Configuration contract

### 5.1. Strict version

Phase 3C3 introduces strict `gateway/v1alpha6`. Versions `gateway/v1alpha1` through `gateway/v1alpha5` remain accepted by their compatibility loaders and normalize to WebSocket disabled.

Both a Route and a Service may contain a presence-aware WebSocket policy:

```yaml
api_version: gateway/v1alpha6

services:
  - id: realtime
    upstream_ref: realtime-upstream
    websocket:
      enabled: true
      idle_timeout: 60s

routes:
  - id: realtime-events
    match:
      path: /events
      methods: [GET]
    service_ref: realtime
    websocket:
      idle_timeout: 5m
```

The public shape is:

```yaml
websocket:
  enabled: <optional boolean>
  idle_timeout: <optional Go duration string>
```

Wire fields must retain presence. An explicit `false` differs from an absent `enabled`, and an explicit `0` differs from an absent `idle_timeout`.

### 5.2. Inheritance

The compiler resolves each field independently:

```text
Route field -> Service field -> default
```

Defaults are:

- `enabled: false`;
- `idle_timeout: 60s`.

Therefore a Route can disable a Service-level enablement, enable a Service that is disabled by default, or override only the idle timeout. A Service may define an idle timeout while disabled so selected Routes can enable WebSocket and inherit it.

`idle_timeout: 0` explicitly disables idle expiration. Negative values, malformed durations, and duration overflow are rejected. No arbitrary upper bound is added; operators that need an unlimited tunnel must use the explicit zero value.

### 5.3. Upstream compatibility

Validation occurs on each effective compiled Route after Service and upstream references are resolved:

- an enabled WebSocket Route must resolve exactly one upstream using existing rules;
- upstream endpoints must use `http` or `https` as already required by Phase 3C1;
- `transport.protocol: http2` is invalid for an enabled WebSocket Route;
- `transport.protocol: auto` and `transport.protocol: http1` are valid;
- an upstream may be shared by ordinary HTTP Routes and WebSocket Routes;
- a Service-level policy does not make an unused Service invalid, and a Route-level explicit disable permits use of a strict HTTP/2 upstream.

For `protocol: auto`, ordinary traffic retains existing HTTP/2 negotiation while WebSocket attempts use an H1-only upgrade transport. This does not silently downgrade strict `protocol: http2`.

## 6. Architecture

### 6.1. Ownership flow

```text
downstream request
  -> runtime snapshot lease
  -> route match and effective WebSocket policy
  -> request plugins
  -> shared attempt executor
  -> upstream response
       non-101 -> ordinary HTTP response terminal
       valid 101
         -> response plugins and final validation
         -> acquire selected transport TunnelLease
         -> hijack downstream connection
         -> register pending tunnel
         -> commit downstream 101
         -> activate asynchronous tunnel
         -> release snapshot and finish HTTP telemetry
  -> bidirectional tunnel runtime
       -> unregister
       -> release TunnelLease
```

Every failure before the downstream `101` is committed follows a rollback path that closes the upstream upgraded body, closes a hijacked downstream connection when present, unregisters a pending tunnel, releases the tunnel lease, and preserves the active snapshot.

### 6.2. Package boundaries

#### `internal/model`

The model contains presence-aware Route and Service WebSocket overrides and a concrete effective `WebSocketPolicy`. It contains no header parsing or connection behavior.

#### `internal/runtime`

The snapshot compiler resolves Route-over-Service policy, applies defaults, validates the resolved upstream protocol, and stores the effective immutable policy on `CompiledRoute`. The request path performs no duration parsing or inheritance lookup.

#### `internal/proxy`

The proxy recognizes candidate handshakes, runs plugins, coordinates the shared attempt executor, forwards final non-`101` responses, and hands a valid upgrade to the tunnel runtime. Ordinary HTTP traffic continues to use `httputil.ReverseProxy`.

#### `internal/websocket`

This package contains pure RFC 6455 handshake functions:

- candidate recognition;
- strict request validation;
- safe upstream header reconstruction;
- upstream and final response validation;
- `Sec-WebSocket-Accept` calculation;
- subprotocol validation.

It does not depend on the router, runtime manager, upstream registry, tunnel registry, or telemetry.

#### `internal/tunnel`

This package owns the admission gate, active registry, opaque stream copier, activity clock, idle controller, half-close behavior, byte counters, drain, and force-close. It is protocol-agnostic and may be reused by a future CONNECT phase, but Phase 3C3 exposes only WebSocket.

#### `internal/upstream`

The upstream runtime provides:

- a shared attempt primitive used by ordinary HTTP and WebSocket orchestration;
- an H1-only upgrade transport for `auto` profiles;
- a `TunnelLease` that independently pins the selected transport generation;
- cleanup that waits for both plan ownership and tunnel ownership to reach zero.

The upstream package never owns the downstream connection.

#### `internal/downstreamtls`

The package exposes a minimal certificate-provider callback compatible with `tls.Config.GetCertificate`. Phase 3C3 provides an immutable static implementation loaded from the existing bootstrap certificate and key. It performs no SNI matching, file watching, or reload.

Phase 3C2 will replace the static implementation with an atomic exact/wildcard SNI index while retaining the same listener and server lifecycle.

#### `internal/gateway`

Gateway construction wires the certificate provider, tunnel registry, proxy, telemetry, and servers. Gateway shutdown controls readiness, admission, HTTP drain, tunnel drain, force-close, and final runtime cleanup. It contains neither RFC handshake parsing nor copy-loop implementation.

## 7. Shared attempt execution

The current `routeTransport` owns selection, replay checks, retry limits, retry-budget acquisition, response draining, passive observations, and attempt metadata. Phase 3C3 extracts those rules into a shared internal attempt executor with thin adapters for the ordinary HTTP reverse proxy and the WebSocket Upgrade Executor.

The extraction must not change Phase 3B or Phase 3C1 behavior. Existing retry, timeout, health, TLS error, and benchmark tests remain regression gates.

For a WebSocket candidate:

1. request plugins execute once before the first attempt;
2. a request-plugin short circuit returns its ordinary matched HTTP response and never selects an upstream or registers a tunnel;
3. the effective retry policy supplies the maximum attempts and total handshake timeout;
4. each attempt selects a distinct healthy or unknown endpoint;
5. the attempt uses the selected generation's H1-capable transport;
6. connect, TLS, response-header timeout, status, and transport failures are classified using existing policy;
7. retryable responses are drained and closed within existing bounds before another endpoint is selected;
8. response plugins execute only on the terminal response, matching existing behavior.

The handshake `GET` has no body and is replayable. It still requires the configured retry method, attempt count, retry-on condition, retry budget, an untried endpoint, and remaining handshake time.

### 7.1. Invalid upstream `101`

Before response plugins run, an upstream `101` is checked for RFC handshake validity. A malformed `101` is an upstream protocol failure. Before any downstream commitment it may retry another endpoint when `connection_failure` retry is enabled and all ordinary retry gates allow it. The passive observation is a stable protocol/connection failure and never includes header contents.

After a valid terminal `101`, response plugins run and the resulting response is validated again. A plugin error or plugin-caused invariant violation returns `500 PLUGIN_RESPONSE_FAILED`, closes the upstream connection, and does not mark the endpoint unhealthy.

### 7.2. Handshake deadline

The existing effective `total_timeout` applies only through successful handshake preparation. A normal context deadline cannot remain attached to an upgraded `http.Transport` response because it would later close the tunnel.

The Upgrade Executor therefore creates an independently cancelable tunnel context and a manual handshake timer:

- downstream request cancellation is linked into it while the handshake is in progress;
- the timer cancels it when the effective total timeout expires;
- both links are stopped after a valid downstream `101` is committed;
- the context then lives only until tunnel completion or gateway shutdown.

The ordinary dial, TLS, and response-header timeouts remain enforced by the selected transport.

## 8. RFC 6455 handshake contract

### 8.1. Candidate and disabled behavior

A request is a WebSocket candidate when it expresses an HTTP/1.1 Upgrade intent through the `Upgrade` or `Connection` tokens. Header token matching is case-insensitive and accepts comma-separated lists.

If effective WebSocket policy is disabled, the request follows the ordinary HTTP pipeline. Existing hop-by-hop removal strips the Upgrade intent, so the upstream cannot establish a tunnel through a disabled Route.

If policy is enabled but the request is not an Upgrade candidate, it also follows ordinary HTTP behavior. RFC 8441 Extended CONNECT is not recognized as a tunnel request.

### 8.2. Request validation

A candidate is accepted only when all conditions hold:

- protocol is HTTP/1.1;
- method is `GET`;
- `Connection` contains `Upgrade`;
- `Upgrade` contains `websocket`;
- `Sec-WebSocket-Version` is exactly `13` after valid header parsing;
- `Sec-WebSocket-Key` is a single valid base64 value decoding to exactly 16 bytes;
- the request has no body.

Malformed candidates return `400 INVALID_WEBSOCKET_HANDSHAKE` through the matched response-plugin path. Header-size protection remains the responsibility of the existing HTTP server `MaxHeaderBytes` setting.

### 8.3. Upstream request headers

The Upgrade Executor clones the plugin-mutated request, removes every standard hop-by-hop header and every header nominated by the inbound `Connection` tokens, then reconstructs canonical:

```text
Connection: Upgrade
Upgrade: websocket
```

Application headers, including `Origin`, cookies, authorization, forwarding headers, and custom metadata, follow the existing proxy rewrite and plugin results. The executor never forwards `Proxy-Connection`, `Keep-Alive`, transfer-coding, or unrelated client-nominated hop headers.

Handshake-control values remain end-to-end consistent. After request plugins, `Upgrade`, `Connection`, `Sec-WebSocket-Key`, `Sec-WebSocket-Version`, offered subprotocols, and offered extensions must be semantically unchanged from the validated inbound handshake. A plugin-caused change fails as `PLUGIN_REQUEST_FAILED`; otherwise the executor reconstructs their canonical representation. Application headers such as authorization, cookies, `Origin`, forwarding headers, and custom metadata remain plugin-mutable.

### 8.4. Response validation

An upstream response enters tunnel preparation only when:

- status is `101`;
- `Connection` contains `Upgrade`;
- `Upgrade` contains `websocket`;
- `Sec-WebSocket-Accept` exactly equals the RFC 6455 value derived from the request key;
- at most one selected subprotocol is present;
- any selected subprotocol was offered by the client.

These conditions are checked both before response plugins for upstream classification and after response plugins for downstream safety. Negotiated extensions are passed through without interpretation.

The final response must also preserve the upstream-negotiated `Sec-WebSocket-Accept`, subprotocol, and extensions. A response plugin may change application response headers, but changing handshake-control semantics fails as `PLUGIN_RESPONSE_FAILED`; the gateway never invents a negotiation that the upstream did not make.

Any terminal non-`101` response is handled as ordinary HTTP: response plugins run, hop-by-hop headers are removed, body and permitted trailers are streamed, request telemetry records its HTTP status, and no tunnel is registered.

## 9. Upstream transport and tunnel lease

### 9.1. H1 transport selection

Classic Upgrade is never sent through HTTP/2:

- an `http1` profile uses its existing H1 production transport;
- an `auto` profile owns an additional H1-only upgrade transport within the same transport generation;
- TLS upgrade transport advertises only `http/1.1` through ALPN;
- a strict `http2` profile is rejected during Route compilation.

The additional `auto` transport shares the generation's verified trust, client identity, SNI, dial, response-header, idle-connection, and pool policy. It is created, reused, retired, and observed with that generation rather than as request-owned state.

### 9.2. Minimal lease

The selected upstream generation remains protected by the snapshot lease during handshake. Before that snapshot is released, selection must acquire a `TunnelLease`. The lease increments independent tunnel ownership on exactly the selected transport generation and is idempotently released when tunnel cleanup completes.

Consequences:

- an unrelated config apply can retire the old snapshot immediately;
- old routes, plugin chains, endpoint sets, health schedules, and retry budgets are not retained by the tunnel;
- the selected transport generation cannot be finalized while its upgraded connection is active;
- after the final tunnel release, normal reaper cleanup reaches steady state.

Failure to acquire the lease fails closed before downstream commitment.

## 10. Upgrade commit and asynchronous handoff

The commit sequence is deliberately transactional:

1. validate the upstream `101`;
2. run response plugins and validate the final response;
3. acquire the selected `TunnelLease`;
4. hijack the downstream HTTP/1.1 connection using `http.ResponseController`;
5. preserve any bytes already buffered by the downstream server;
6. construct a pending tunnel with both connection handles;
7. register it while tunnel admission is still open;
8. write and flush the final `101` response directly to the hijacked downstream stream;
9. mark the tunnel active and start registry-owned copy goroutines;
10. set request state to HTTP status `101`;
11. release the snapshot lease and return from the HTTP handler.

Registering before response commitment closes the shutdown race. If admission has closed, the executor writes a non-`101` draining response over the hijacked HTTP stream when still possible, then closes both sides. If writing or flushing `101` fails, the pending registration is rolled back.

After successful handoff, the tunnel owns no `requestctx.Context`, response writer, compiled Route, plugin chain, or snapshot pointer.

## 11. Tunnel runtime

### 11.1. Opaque copying and backpressure

The tunnel is a pair of opaque streams:

```text
downstream net.Conn <==== raw bytes ====> upstream io.ReadWriteCloser
```

Two goroutines copy independently using fixed 32 KiB buffers obtained from a `sync.Pool`. Blocking writes provide backpressure. There is no payload queue, frame parser, message-size limit, or per-message allocation in the gateway.

Buffered downstream bytes returned by hijack and buffered upstream bytes returned by `http.Transport` must be consumed before direct socket reads. Text, binary, fragmented, extension-compressed, and close frames therefore pass unchanged.

### 11.2. Activity and idle timeout

Every successful read or write of one or more bytes updates a monotonic activity clock. One idle controller observes the combined activity of both directions:

- any traffic in either direction resets idle expiration;
- `idle_timeout: 0` disables the controller;
- on expiration, both streams are closed to unblock both copy goroutines;
- timer reset, completion, and shutdown races are idempotent.

The gateway does not synthesize pings. Applications needing long idle periods must send traffic or explicitly configure zero/a longer duration.

### 11.3. Half-close

When one copy direction reaches EOF, the tunnel invokes `CloseWrite` on the opposite stream when supported, then allows the reverse direction to drain. Go 1.26 `net.TCPConn`, `tls.Conn`, and the HTTP/1.1 upgraded response body expose the needed behavior. If a custom stream reports `ErrNotSupported`, the tunnel records no failure and continues reverse draining until completion, idle expiration, fatal I/O error, or shutdown.

### 11.4. Completion reason

Each tunnel records one bounded terminal reason:

- `client_eof`;
- `upstream_eof`;
- `idle_timeout`;
- `shutdown`;
- `io_error`.

Explicit controller actions (`shutdown`, then `idle_timeout`) take precedence over incidental close errors they cause. Otherwise the first causal EOF or fatal I/O error wins. Error strings and network identities are not retained.

Cleanup is idempotent and always:

1. closes both streams;
2. stops the idle controller and tunnel context;
3. waits for both copy goroutines;
4. returns buffers;
5. unregisters the tunnel;
6. observes duration, bytes, and reason;
7. releases the `TunnelLease`.

## 12. Apply and shutdown lifecycle

### 12.1. Configuration apply

An established tunnel is not terminated when a revision changes, its Route is deleted, its Service is changed, WebSocket is disabled, or its upstream membership rotates. New handshakes use the newly active snapshot. Existing tunnels retain only the selected old transport generation until natural completion.

Acceptance must prove that the old snapshot and unrelated resources retire while the tunnel remains active, and that the selected transport generation retires after the tunnel closes.

### 12.2. Graceful shutdown

Gateway shutdown uses the existing process shutdown deadline and this order:

1. set readiness false and mark the Gateway closing;
2. close tunnel admission before stopping traffic listeners;
3. reject/rollback handshakes that have not registered;
4. stop active-health scheduling;
5. shut down HTTP and HTTPS servers to drain ordinary requests and in-progress handshakes;
6. wait for registered tunnels to finish naturally for the remaining deadline;
7. when the deadline expires, force-close both streams of every remaining tunnel;
8. wait for copy goroutines to unregister and release tunnel leases;
9. close the runtime manager and upstream registry;
10. shut down the private admin server according to the existing lifecycle.

No WebSocket close frame is generated. The gateway remains a protocol-transparent byte tunnel. Repeated shutdown calls and simultaneous idle/client/upstream closure are safe and do not double-release a lease.

## 13. Downstream certificate-provider seam

The current Gateway stores the startup certificate directly in `tls.Config.Certificates`. Phase 3C3 replaces that direct ownership with a provider callback:

```text
tls.Config.GetCertificate -> DownstreamCertificateProvider.GetCertificate
```

The Phase 3C3 provider:

- loads and validates the same bootstrap certificate/key during Gateway construction;
- holds one immutable parsed certificate;
- returns it for every ClientHello regardless of SNI;
- performs no file access, locking, parsing, or allocation proportional to configured resources during handshake;
- preserves downstream HTTP/1.1 and HTTP/2 behavior.

The listener, server, and tunnel registry depend only on the provider interface. Phase 3C2 may atomically change the provider's immutable index without restarting or replacing listeners. Phase 3C3 does not expose dynamic provider activation through `Gateway.Apply`.

## 14. Telemetry and logging

### 14.1. HTTP telemetry boundary

The HTTP handler returns immediately after a successful asynchronous handoff. Existing request count and duration therefore describe the handshake, not tunnel lifetime. Because the final `101` is written directly after hijack, request state explicitly records status `101` so telemetry does not infer an implicit `200`.

Existing HTTP request metrics may retain their existing compiled Route labels. New WebSocket metric families do not add Route, Service, upstream, host, endpoint, client, certificate, or revision labels.

### 14.2. WebSocket metrics

Phase 3C3 adds exactly these bounded-cardinality families:

```text
gateway_websocket_handshakes_total{result}
gateway_websocket_active_tunnels
gateway_websocket_tunnels_closed_total{reason}
gateway_websocket_tunnel_duration_seconds
gateway_websocket_bytes_total{direction}
```

Allowed label values are fixed:

- handshake `result`: `success`, `invalid_request`, `upstream_rejected`, `upstream_failure`, `plugin_failure`, `draining`;
- close `reason`: `client_eof`, `upstream_eof`, `idle_timeout`, `shutdown`, `io_error`;
- byte `direction`: `downstream_to_upstream`, `upstream_to_downstream`.

Only requests that enter WebSocket handshake handling increment handshake metrics. A disabled Route or a non-candidate request remains ordinary HTTP. A terminal non-`101` from upstream increments `upstream_rejected` and its normal HTTP metrics.

The registry stores fixed-size metadata per active tunnel: an internal sequence, stream handles, start/activity time, effective idle timeout, counters, and terminal state. It never stores payloads or request headers.

### 14.3. Logs

Phase 3C3 does not emit a log per frame or per normal tunnel close. Rate-limited upstream handshake failures use stable bounded classes. Shutdown logs only aggregate drain counts and result. Logs and metrics must not include WebSocket keys, cookies, authorization, payload, hostnames, endpoint URLs, client addresses, raw certificate data, or arbitrary peer error text.

Per-request access logging remains Phase 3D work.

## 15. Error contract

Stable externally visible outcomes include:

| Condition | HTTP status | Code |
| --- | ---: | --- |
| malformed enabled WebSocket candidate | 400 | `INVALID_WEBSOCKET_HANDSHAKE` |
| no healthy endpoint | 503 | existing `UPSTREAM_UNHEALTHY` |
| handshake total/transport timeout | 504 | existing `UPSTREAM_TIMEOUT` |
| upstream TLS failure | 502 | existing `UPSTREAM_TLS_FAILED` |
| invalid terminal upstream `101` | 502 | `UPSTREAM_WEBSOCKET_HANDSHAKE_INVALID` |
| request plugin failure | 500 | existing `PLUGIN_REQUEST_FAILED` |
| response plugin failure/final invariant violation | 500 | `PLUGIN_RESPONSE_FAILED` |
| Gateway drain before commitment | 503 | `GATEWAY_DRAINING` |
| downstream hijack unavailable | 500 | `DOWNSTREAM_HIJACK_UNSUPPORTED` |

Non-`101` upstream responses are not rewritten into gateway errors unless existing retry/error policy already requires it. Once downstream `101` is committed, no HTTP error can be sent; subsequent failures only terminate the tunnel and update bounded telemetry.

## 16. Testing strategy

### 16.1. Configuration and model

Tests cover:

- strict v1alpha6 decoding and unknown-field rejection;
- Route-over-Service inheritance for every absent/present combination;
- explicit `false`, explicit zero, and default `60s`;
- malformed, negative, and overflowing durations;
- v1alpha1 through v1alpha5 compatibility normalization;
- enabled Route plus `auto`/`http1` success;
- enabled Route plus strict `http2` rejection;
- mixed ordinary/WebSocket Routes sharing one upstream;
- cloning and immutability of policy state.

### 16.2. Handshake unit and fuzz tests

Table and fuzz tests cover:

- case-insensitive and comma-separated tokens;
- duplicated, missing, malformed, or oversized logical header values;
- method, protocol, body, key, version, and subprotocol rules;
- deterministic `Sec-WebSocket-Accept` calculation;
- Connection-nominated hop-header stripping;
- canonical upstream header reconstruction;
- upstream and post-plugin response validation;
- request-plugin changes to handshake-control fields failing closed;
- response-plugin changes to negotiated handshake fields failing closed;
- no panic, secret-bearing error, or non-deterministic classification for arbitrary input.

### 16.3. Integration tests

End-to-end tests cover:

- `ws://` and `wss://` downstream;
- `http://` and verified `https://` upstream;
- `auto` and strict `http1` upstream policy;
- strict `http2` compile rejection;
- text, binary, fragmented, large, and eagerly buffered post-handshake data;
- extension and subprotocol pass-through;
- request and response plugin behavior;
- disabled policy following ordinary HTTP behavior;
- non-`101` response forwarding;
- endpoint retry, retry budget, passive observation, no-healthy behavior, TLS failure, and handshake timeout;
- invalid upstream `101` retry and final failure;
- no retry or health mutation after commitment.

### 16.4. Lifecycle and concurrency tests

Deterministic tests cover:

- traffic in either direction resetting idle expiration;
- explicit zero disabling idle expiration;
- client and upstream half-close with reverse drain;
- fatal copy error and double-close safety;
- apply changing/removing/disabling the Route while a tunnel remains active;
- old snapshot retirement while only the selected transport stays pinned;
- selected generation cleanup after final tunnel close;
- shutdown natural drain;
- shutdown deadline force-close;
- admission-close race at every pre-commit stage;
- concurrent Apply, handshake, idle expiration, peer close, and shutdown;
- repeated lifecycle with zero active tunnels, zero retired plan sets, and stable goroutine/heap state;
- Go race detector on a CGO-capable environment.

### 16.5. Performance acceptance

Performance comparison uses a deterministic direct Go H1 WebSocket/tunnel baseline and the Gateway path on the same host. Measurements alternate baseline and Gateway order and use the median of five warmed rounds.

Required relative gates are:

- Gateway handshake throughput at least 90% of direct baseline;
- Gateway handshake p99 no more than 125% of direct baseline;
- steady-state message/byte throughput at least 95% of direct baseline;
- steady-state message p99 no more than 110% of direct baseline;
- no heap allocation per established copy chunk;
- after close/drain, active tunnels are zero, tunnel leases are zero, retired runtime state reaches steady state, and heap/goroutine growth is not proportional to completed lifecycle rounds.

The mandatory normal developer profile uses hundreds of concurrent tunnels and remains suitable for routine verification. An opt-in full/reference profile exercises 10,000 concurrent idle and active tunnel lifecycle, large bidirectional transfer, repeated apply, and drain. It is isolated behind an environment gate so the ordinary unit suite remains bounded.

The 32 KiB buffer choice is evidence-driven rather than an immutable public contract. If the full memory or throughput gate fails, the implementation may tune buffer size using the same benchmark without changing configuration or lifecycle semantics.

## 17. Documentation and evidence

Implementation completion produces:

- `configs/phase3c3.yaml`;
- `docs/operations/phase-3c3-runbook.md`;
- `docs/benchmarks/phase-3c3-current-status.md`;
- README capability, exclusion, and command updates;
- roadmap status and reordered 3C2 handoff;
- exact verification commands, environment, measurements, and pending gates.

Phase 3C3 may be recorded as `implementation complete; canonical WebSocket evidence pending` when local correctness, lifecycle, fuzz, and relative performance pass but the current environment cannot run race or reference-Linux gates. That state is not APISIX parity, production certification, or umbrella Phase 3C completion.

## 18. Implementation-complete criteria

Phase 3C3 implementation is complete when:

1. strict v1alpha6 and all compatibility tests pass;
2. Route/Service inheritance and protocol validation match this design;
3. ws/wss through http/https correctness tests pass;
4. request/response plugins, retry, health, timeout, and TLS behavior remain correct through the handshake boundary;
5. established tunnels release snapshots and retain only selected transport generations;
6. idle, half-close, apply independence, drain, and force-close lifecycle tests pass;
7. bounded telemetry and safe logging tests pass;
8. normal relative performance gates and leak checks pass;
9. fuzz tests pass;
10. race and reference-Linux results are either passing or explicitly recorded as pending with no parity claim;
11. configuration, runbook, evidence, README, and roadmap documentation agree.

The umbrella Phase 3C remains incomplete until Phase 3C2 dynamic downstream certificate selection and all canonical Phase 3 gates are satisfied.

## 19. Handoff

Phase 3C2 receives:

- the `DownstreamCertificateProvider` callback already used by live listeners;
- unchanged HTTP/HTTPS server and tunnel ownership;
- generic immutable `Certificate` resources from Phase 3C1;
- the requirement to add atomic exact/wildcard SNI selection and rotation without listener restart.

Phase 3D receives:

- completed upstream TLS/protocol and downstream WebSocket behavior;
- bounded WebSocket aggregate telemetry;
- request-context and attempt metadata for structured access logging;
- the responsibility to run the integrated TLS, health, retry, protocol, WebSocket, and APISIX comparison.

Neither handoff may reinterpret developer-machine evidence as production parity.

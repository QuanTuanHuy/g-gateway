# Phase 3C1 Upstream TLS and Protocol Design

**Date:** 2026-07-30

**Status:** Approved design

**Depends on:** Phase 3A upstream runtime and balancing kernel; Phase 3B health, timeout, retry, and retry budget

**Followed by:** Phase 3C2 dynamic downstream SNI certificates; Phase 3C3 WebSocket

## 1. Context

Phase 3C is intentionally decomposed into three independently designed and accepted sub-phases:

1. **Phase 3C1:** upstream TLS/mTLS, HTTP protocol expansion, and gRPC pass-through;
2. **Phase 3C2:** dynamic downstream certificate selection and rotation by SNI;
3. **Phase 3C3:** WebSocket upgrade, tunnel lifecycle, timeout, and drain.

The decomposition follows runtime ownership and failure boundaries. Upstream TLS and HTTP/2 belong to shared outbound transport generations. Downstream certificates belong to listener handshake state. WebSocket belongs to long-lived upgraded request state. Combining all three in one plan would make rollback, performance attribution, and lifecycle verification unnecessarily coupled.

Phase 3C1 inherits:

- immutable, versioned snapshots activated atomically;
- registry-owned endpoint, health, retry-budget, selection, and transport runtimes;
- weighted round-robin and consistent hash;
- per-request snapshot leases and bounded retirement;
- active and passive health;
- replay-safe gateway retries across distinct endpoints;
- total request deadlines;
- HTTP/1.1 cleartext upstream transport;
- HTTP/1.1 cleartext and HTTP/1.1 or HTTP/2 over TLS downstream listeners.

The current transport profile is deliberately HTTP/1.1-only and accepts only `http://` endpoint URLs. Phase 3C1 expands that profile without moving parsing, certificate loading, protocol detection, or mutable policy onto the request hot path.

## 2. Goals

Phase 3C1 must provide:

- verified HTTPS upstream connections using system or explicitly referenced trust roots;
- upstream mutual TLS using a referenced client certificate;
- deterministic SNI and hostname-verification behavior;
- HTTP/1.1, HTTP/2 over TLS, and cleartext HTTP/2 prior knowledge;
- native gRPC pass-through, including streaming, trailers, status propagation, and cancellation;
- HTTPS-aware active health checks without sharing the production connection pool;
- atomic TLS material and protocol rotation through transport generations;
- bounded, low-cardinality operational telemetry;
- no regression outside the existing Phase 3B healthy-path performance budget.

## 3. Non-goals

Phase 3C1 does not include:

- dynamic downstream certificate or SNI selection;
- downstream h2c;
- gRPC transcoding, gRPC-Web, protobuf inspection, or retry based on `grpc-status`;
- WebSocket, CONNECT, or a generic TCP tunnel;
- configurable cipher suites, curves, or arbitrary TLS version ranges;
- insecure certificate-verification bypass;
- automatic filesystem watching;
- control-plane SecretRef distribution;
- access logging;
- canonical integrated APISIX comparison.

Downstream SNI belongs to Phase 3C2, WebSocket belongs to Phase 3C3, and access logging plus integrated APISIX evidence belongs to Phase 3D.

## 4. Chosen approach

Phase 3C1 uses one native Go 1.26 `http.Transport` per complete transport-profile fingerprint.

Go 1.26 `http.Protocols` natively supports:

- HTTP/1.0 and HTTP/1.1 over cleartext or TLS;
- HTTP/2 over TLS;
- unencrypted HTTP/2.

The design therefore does not introduce a separate `x/net/http2.Transport`, a protocol dispatcher with independent pools, or a custom HTTP networking stack. Existing `httputil.ReverseProxy`, route transport, retry loop, and registry lifecycle remain in place.

### 4.1. Rejected alternatives

**Separate HTTP/1 and HTTP/2 transports** would duplicate pool, timeout, telemetry, and cleanup semantics. It would also make `auto` negotiation and transport reuse harder to reason about.

**A custom dial, TLS, HTTP/2 session, and pool stack** would provide more control than the standard library but create a disproportionate correctness and security burden. Go 1.26 already exposes the protocol controls required by this phase.

## 5. Architecture and ownership

```text
gateway/v1alpha5 document
  |
  +-- trust_bundles: file-backed CA resources
  +-- certificates: file-backed certificate/key resources
  +-- upstream.transport:
        protocol: auto | http1 | http2
        tls: trust ref, client identity ref, server-name override
  |
  v
strict config loader
  |
  +-- read bounded files once
  +-- parse and validate public/private material
  +-- canonicalize and fingerprint public material
  +-- produce immutable logical resources
  |
  v
snapshot builder
  |
  +-- resolve references
  +-- validate endpoint scheme and protocol matrix
  +-- compile complete immutable transport profile
  |
  v
upstream registry
  |
  +-- reuse or create TransportRuntime by complete profile key
  +-- create a separate bounded health-probe transport
  +-- retain material and runtime ownership transactionally
  |
  v
request: route -> plan -> healthy endpoint -> retry loop -> native transport
```

File paths exist only in the configuration document. Request execution never reads files, parses certificates, resolves resource references, chooses protocol policy, or mutates a TLS configuration.

The runtime model gains immutable `Certificate` and `TrustBundle` resources. The configuration layer owns file reading and conversion. A focused TLS-material component owns PEM parsing, certificate/key validation, canonicalization, fingerprinting, and construction of runtime TLS inputs. It is reusable by Phase 3C2 without coupling the outbound transport registry to the downstream listener.

## 6. Configuration contract

Phase 3C1 introduces strict `gateway/v1alpha5`. Versions `gateway/v1alpha1` through `gateway/v1alpha4` remain accepted by their compatibility loaders and normalize to cleartext HTTP/1.1 upstream behavior.

The following excerpt shows the new fields:

```yaml
api_version: gateway/v1alpha5

trust_bundles:
  - id: internal-ca
    ca_file: /secrets/internal-ca.pem

certificates:
  - id: orders-client
    certificate_file: /secrets/orders-client.crt
    private_key_file: /secrets/orders-client.key

upstreams:
  - id: orders
    endpoints:
      - url: https://10.0.1.10:8443
        weight: 1
      - url: https://10.0.1.11:8443
        weight: 1
    balancer:
      type: weighted_round_robin
    transport:
      protocol: http2
      tls:
        trust_bundle_ref: internal-ca
        client_certificate_ref: orders-client
        server_name: orders.internal
      dial_timeout: 3s
      response_header_timeout: 10s
      idle_connection_timeout: 90s
      max_idle_connections: 1024
      max_idle_connections_per_host: 256
```

### 6.1. Material resources

A `Certificate` contains one ordered certificate chain and its matching private key. It is not intrinsically marked client or server. Phase 3C1 references it as a client identity; Phase 3C2 may reference the same resource type from a downstream SNI binding.

A `TrustBundle` contains one or more CA certificates. If `trust_bundle_ref` is absent, a transport generation uses system roots. If it is present, the referenced bundle replaces system roots. There is no implicit append mode. A bundle requiring public and private roots must contain the exact combined trust set.

Material limits are:

- CA bundle file: at most 1 MiB;
- certificate-chain file: at most 256 KiB;
- private-key file: at most 256 KiB;
- at most 10,000 certificate and trust-bundle resources combined;
- at most 64 MiB of certificate, key, and CA source bytes in one candidate resource set.

The loader rejects an empty CA set, non-certificate PEM blocks, malformed certificates, unsupported or malformed private keys, certificate/key mismatch, duplicate IDs, oversized files, and missing files.

Changing a material file on disk has no effect until the standalone configuration is loaded and applied again. There is no background watcher. A successful apply with changed public-material fingerprints creates new transport generations; a failed reload preserves the active revision.

### 6.2. Upstream TLS policy

The optional upstream `transport.tls` policy contains:

- `trust_bundle_ref`;
- `client_certificate_ref`;
- `server_name`.

TLS certificate-chain and hostname verification is always enabled. There is no `insecure_skip_verify` field.

If `server_name` is absent, TLS SNI and hostname verification use the selected endpoint URL hostname. If it is present, the same value applies to every endpoint in the upstream. This supports multiple IP endpoints serving one logical certificate identity.

TLS SNI is independent from the HTTP `Host` or HTTP/2 `:authority`. Phase 3C1 preserves the current behavior of forwarding the inbound host. APISIX-style `pass_host` or host rewrite is a separate request-policy concern.

### 6.3. Scheme and protocol validation

Every positive-weight endpoint in one upstream must use the same URL scheme. Supported schemes are `http` and `https`.

An `http` upstream must not declare a TLS policy. An `https` upstream may omit the TLS policy, in which case it uses system roots, endpoint-host SNI, and no client certificate.

The protocol matrix is:

| Endpoint scheme | `protocol` | Runtime behavior |
|---|---|---|
| `http` | `auto` | HTTP/1.1 |
| `http` | `http1` | HTTP/1.1 only |
| `http` | `http2` | h2c prior knowledge only |
| `https` | `auto` | ALPN prefers HTTP/2 and falls back to HTTP/1.1 |
| `https` | `http1` | HTTP/1.1 over TLS only |
| `https` | `http2` | HTTP/2 over TLS required; no HTTP/1.1 fallback |

There is no protocol probing. A strict mismatch fails the attempt.

## 7. TLS material and security behavior

Candidate preparation reads and validates material before any runtime activation:

1. read each referenced file once under its size limit;
2. decode PEM and parse X.509 certificates and private keys;
3. verify each certificate/private-key pair;
4. canonicalize public material;
5. calculate domain-separated SHA-256 fingerprints;
6. discard raw PEM when parsed runtime material is available;
7. resolve references and compile transport profiles;
8. activate only after the complete candidate succeeds.

Trust-bundle fingerprints are calculated from the sorted, de-duplicated certificate DER set because CA ordering has no semantic meaning. Certificate fingerprints preserve certificate-chain order. A matching private key is required, so changing to a different valid key also requires a matching certificate and changes the public certificate fingerprint.

Fingerprints are internal transport identity values. Private key bytes and PEM are never part of a log field, metric label, public error, or human-readable key.

Each TLS transport generation uses:

- minimum TLS version 1.2;
- Go default cipher suites and curves;
- system roots or the replacement trust bundle;
- zero or one client certificate;
- endpoint-host or fixed server-name verification;
- a private `tls.NewLRUClientSessionCache(64)`;
- an immutable `tls.Config`.

TLS session caches are never shared between transport profiles or generations. Material rotation cannot resume a session created with the previous trust or client identity.

Go does not guarantee secure memory zeroization. Phase 3C1 limits secret lifetime, avoids raw-PEM retention and copying, and releases parsed material with its generation, but does not claim cryptographic zeroization.

## 8. Complete transport identity

The transport key includes every value that can alter connection or protocol semantics:

- endpoint scheme;
- protocol mode;
- dial timeout;
- response-header timeout;
- idle-connection timeout;
- maximum idle connections;
- maximum idle connections per host;
- compression policy;
- TLS enabled state;
- TLS policy version;
- system-root sentinel or trust-bundle fingerprint;
- client-certificate fingerprint or no-client-identity sentinel;
- server-name override;
- fixed minimum TLS version.

Weights, endpoint order, balancer policy, health thresholds, retry policy, total timeout, route policy, and request host do not participate in the transport key.

One transport may continue to serve multiple upstream plans only when their complete keys are equal. Go's transport partitions connections by origin. Security profiles with different trust, client identity, SNI override, scheme, or protocol never share a transport runtime, session cache, or connection pool.

## 9. Request and protocol flow

The request flow remains:

1. acquire a snapshot lease;
2. match a route and run request plugins;
3. apply the compiled total deadline when non-zero;
4. choose a health-aware endpoint;
5. clone the request for the attempt;
6. replace only the outbound URL scheme and host with the selected endpoint;
7. execute the native transport;
8. stream response headers, body, and trailers;
9. release the lease after request completion.

The inbound HTTP host remains the outbound HTTP `Host` or HTTP/2 `:authority`. TLS verification identity follows Section 6.2.

### 9.1. gRPC pass-through

gRPC uses the same HTTP/2 transport. The gateway preserves:

- method and path;
- content type and metadata headers;
- binary message frames;
- response headers and trailers;
- `grpc-status` and `grpc-message`;
- client cancellation;
- unary, client-streaming, server-streaming, and bidirectional-streaming behavior.

The gateway does not parse protobuf messages or interpret gRPC status for retry. Long-lived streams must explicitly set `total_timeout: 0`. Phase 3C1 does not infer timeout policy from content type.

The cleartext downstream listener remains HTTP/1.1-only. Native gRPC clients enter through the existing HTTP/2 TLS listener. The selected upstream may use h2c or HTTP/2 over TLS.

## 10. Retry and failure observation

There are two intentionally distinct recovery layers.

### 10.1. Standard-library transport recovery

Go may replay a safe request on the same selected endpoint when recovering from a stale pooled connection or HTTP/2 events such as `GOAWAY` or `REFUSED_STREAM`. This is connection/session recovery inside one RoundTrip. It:

- does not increment the gateway attempt number;
- does not consume retry budget;
- does not select a different endpoint;
- remains bounded by the request context and Go's transport rules.

This behavior already exists for some HTTP/1 pooled-connection failures and is required for correct HTTP/2 connection lifecycle.

### 10.2. Gateway retry

The Phase 3B gateway retry starts only after the transport returns a final response or error. It:

- checks the existing replay-safety policy;
- consumes retry budget;
- increments attempt state;
- selects a healthy endpoint that has not been tried by the gateway;
- remains bounded by the total request deadline.

TLS verification, handshake, client-identity, and strict-protocol failures are connection failures for retry-policy purposes. No retry is triggered by a gRPC status trailer.

## 11. Health checking

HTTP active health checks use the same compiled trust, SNI, client identity, TLS version, and protocol policy as production traffic. They use a distinct bounded probe transport and connection pool so probes do not warm, consume, or perturb production connections.

TCP active health checks remain raw TCP reachability checks. They do not prove TLS, ALPN, HTTP, or application readiness.

Phase 3C1 does not implement the gRPC Health Checking Protocol. A gRPC upstream may expose an HTTP-compatible health path or use a TCP probe.

Passive observations classify:

- identified TLS trust, hostname, client-identity, handshake, or ALPN failures as transport failures;
- deadline failures as timeouts;
- HTTP responses under the existing Phase 3B status policy.

A successful standard-library recovery is not recorded as a failed gateway attempt.

## 12. Error contract

Candidate errors reject the complete transaction and preserve the active last-known-good revision. This includes invalid material, missing references, mixed schemes, and invalid scheme/protocol/TLS combinations.

Runtime errors map as follows:

| Condition | HTTP status | Stable code |
|---|---:|---|
| Identified final TLS or ALPN failure | 502 | `UPSTREAM_TLS_FAILED` |
| Other final connection failure | 502 | `UPSTREAM_CONNECTION_FAILED` |
| Total deadline exceeded | 504 | `UPSTREAM_TIMEOUT` |
| No healthy endpoint | 503 | `UPSTREAM_UNHEALTHY` |

Typed X.509, TLS alert, verification, and explicit strict-ALPN errors are wrapped in an internal TLS failure category without string matching where the standard library exposes a type. Unknown generic network errors remain connection failures.

Public responses never contain raw X.509 errors, certificate subjects, SANs, SNI, endpoint hostnames, file paths, PEM, private-key information, or remote TLS alert text.

## 13. Telemetry

Telemetry uses only bounded labels:

```text
gateway_upstream_tls_handshake_total{
  result="success|failure",
  mode="server_auth|mtls",
  protocol="auto|http1|http2"
}

gateway_upstream_tls_failure_total{
  class="trust|hostname|client_identity|protocol|handshake"
}

gateway_upstream_transport_generation_total{
  action="create|reuse|retire",
  tls="true|false",
  protocol="auto|http1|http2"
}
```

Handshake success is observed at TLS connection establishment rather than once per request. Failed RoundTrips increment one final classified failure category. Metrics do not use upstream ID, endpoint, hostname, certificate ID, fingerprint, error text, or revision as labels.

Lifecycle logs report bounded resource counts and stable failure categories. They do not log TLS material or certificate metadata. Fingerprints are not logged by default.

## 14. Reconcile and lifecycle

Candidate preparation transactionally:

1. resolves material references;
2. acquires immutable material handles;
3. creates the complete transport key;
4. reuses or creates the production transport;
5. prepares compatible health and probe runtimes;
6. builds the plan and route references;
7. commits ownership only after the candidate is complete.

Rollback releases every acquired reference, closes new unowned transports, unregisters new probe work, and leaves no goroutine, timer, session cache, material handle, or partial snapshot.

Route-only, weight-only, retry-only, and health-threshold-only changes reuse a TLS transport when its complete key is unchanged. A CA, client certificate, server name, protocol, scheme, or TLS policy change creates a new transport generation. Unrelated upstreams retain their existing pools.

An old generation remains usable while any request lease owns its plan. Retirement closes idle connections exactly once only after ownership is released. Active HTTP/2 and gRPC streams therefore survive a snapshot swap.

Shutdown order remains:

```text
readiness off
  -> stop new health work
  -> stop accepting and drain traffic
  -> wait request leases and retry permits
  -> close probe runtimes
  -> release plans and material handles
  -> close idle production transports exactly once
  -> close registry
```

## 15. Testing strategy

### 15.1. Unit and configuration tests

Tests cover:

- strict v1alpha5 decoding and v1alpha1-v1alpha4 compatibility;
- resource cloning and redaction;
- duplicate IDs, missing references, size limits, malformed PEM, empty CA sets, and key mismatch;
- endpoint scheme normalization and mixed-scheme rejection;
- every scheme/protocol/TLS matrix entry;
- complete transport-key identity;
- immutable TLS construction and minimum version;
- system-root versus replacement-root behavior;
- server-name override;
- session-cache isolation;
- idempotent transport cleanup.

### 15.2. Integration tests

TLS tests cover:

- system and custom CA success;
- unknown CA and hostname mismatch;
- IP endpoint with a valid server-name override;
- successful and failed mTLS;
- TLS 1.1 rejection;
- material rotation;
- HTTPS active probes using the production security policy but a separate pool.

Protocol tests cover:

- cleartext HTTP/1.1;
- HTTPS HTTP/1.1-only;
- HTTPS auto HTTP/2 negotiation and HTTP/1.1 fallback;
- strict HTTP/2 success and ALPN mismatch;
- h2c prior knowledge;
- HTTP/2 headers, streaming bodies, and trailers.

gRPC interoperability tests use a real Go gRPC client and server and cover:

- unary calls;
- client streaming;
- server streaming;
- bidirectional streaming;
- metadata and trailer propagation;
- gRPC status propagation;
- downstream cancellation.

Resilience tests cover:

- TLS failure followed by a gateway retry to another endpoint;
- non-replayable request suppression;
- retry-budget enforcement;
- total-deadline enforcement;
- active and passive health transitions;
- no gRPC-status-based retry.

Lifecycle tests cover:

- complete candidate rollback;
- material rotation creating a new pool;
- unrelated upstream update preserving its pool;
- active HTTP/2 and gRPC streams across a snapshot swap;
- repeated rotation returning to steady state;
- deterministic shutdown drain and cleanup.

### 15.3. Fuzz and race tests

Fuzz targets cover:

- certificate and CA document boundaries;
- protocol-policy normalization;
- scheme/protocol/TLS validation;
- transport-key construction;
- error redaction.

The race suite covers concurrent request, health, retry, apply, material rotation, retirement, and shutdown. The canonical race gate must run on a CGO-capable host. If the development host cannot run it, the evidence document records the exact environmental blocker without presenting the gate as passed.

## 16. Performance acceptance

Phase 3C1 preserves the Phase 3B healthy-path regression budget:

- cleartext HTTP/1.1 throughput is at least 95% of the Phase 3B baseline;
- cleartext HTTP/1.1 p99 latency is no more than 110% of the Phase 3B baseline;
- selector, request lease, and disabled-TLS path do not gain allocations.

Additional benchmarks cover:

- TLS full handshake;
- TLS session resumption;
- HTTPS HTTP/1.1;
- HTTP/2 multiplexing;
- h2c;
- unary and streaming gRPC;
- transport creation/reuse and material rotation.

The full-envelope compile/swap acceptance continues to enforce at most 10,000 upstreams, 100,000 total endpoints, and 1,000 endpoints per upstream. Its incremental active heap remains bounded by the Phase 3A target of 512 MiB. Repeated material rotation must return transport counts and memory to steady state after retirement.

Developer-machine evidence is recorded in `docs/benchmarks/phase-3c1-current-status.md`. Reference-Linux absolute performance gates and the integrated APISIX comparison remain Phase 3D responsibilities.

## 17. Exit criteria

Phase 3C1 is implementation-complete when:

1. strict v1alpha5 and compatibility tests pass;
2. HTTPS, custom trust, mTLS, SNI override, strict HTTP/2, h2c, and gRPC pass-through correctness tests pass;
3. active health, passive health, retry, deadline, and failure mapping behave according to this design;
4. rotation, rollback, retirement, and shutdown lifecycle tests pass without leaked runtime ownership;
5. normal performance acceptance stays within the Phase 3B regression budget;
6. fuzz tests pass;
7. verification evidence records every command, environment, result, and pending canonical gate.

Race and reference-Linux gates may remain explicitly pending when the current environment cannot run them. That status is not APISIX parity or production certification.

The umbrella Phase 3C is not implementation-complete until Phase 3C2 and Phase 3C3 pass their independent acceptance.

## 18. Handoff

Phase 3C2 reuses the generic `Certificate` material resource and adds an immutable exact/wildcard SNI index plus atomic downstream certificate activation. It does not change outbound transport ownership.

Phase 3C3 adds explicit route/service WebSocket enablement, HTTP/1.1 upgrade forwarding, upgraded-connection ownership, tunnel timeout semantics, and graceful drain. It reuses the listener and certificate runtime from Phase 3C2 but does not change certificate matching.

Phase 3D adds bounded structured access logging and runs the integrated TLS, health, retry, protocol, and WebSocket comparison against APISIX.

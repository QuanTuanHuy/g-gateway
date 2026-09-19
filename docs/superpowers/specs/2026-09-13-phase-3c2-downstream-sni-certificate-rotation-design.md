# Phase 3C2 Downstream SNI Certificate Rotation Design

**Date:** 2026-09-13

**Status:** Approved design; implementation plan pending

**Phase:** 3C2

**Depends on:** Phase 3C1 immutable certificate material and Phase 3C3 downstream certificate-provider seam

**Followed by:** Phase 3D bounded access logging and integrated APISIX evidence, then Phase 4 control plane

## 1. Summary

Phase 3C2 adds dynamic downstream certificate selection and rotation to the
existing HTTPS listener. One immutable runtime snapshot owns routing,
upstream, plugin, and downstream TLS state for a revision. Publishing that
snapshot changes all of those views with one atomic operation.

The phase supports an explicitly configured default certificate, exact SNI
bindings, and one-label wildcard bindings. A rejected revision leaves the
last-known-good selector and traffic snapshot active. Rotation affects new TLS
connections only; established HTTP, HTTP/2, gRPC, and WebSocket connections
are not interrupted.

Phase 3C2 deliberately retains one HTTPS listener. It does not introduce a
listener resource, a public update surface, certificate discovery, or general
TLS-policy management.

## 2. Context

Phase 3C1 introduced generic immutable `Certificate` resources and bounded
material loading. Phase 3C3 changed the HTTPS server to use a
`tls.Config.GetCertificate` provider while retaining the bootstrap certificate
as a static implementation. The runtime manager already serializes `Apply`,
builds an immutable candidate, atomically publishes a strictly newer revision,
and preserves the active revision on failure.

Keeping downstream TLS in a separately published atomic provider would split
one logical configuration transaction into two commits. A handshake could
then observe one revision while request routing observes another. Phase 3C2
instead makes the compiled selector part of the runtime snapshot and makes the
runtime manager the certificate callback used by the listener.

## 3. Goals

Phase 3C2 must provide:

1. a strict `gateway/v1alpha7` configuration for downstream TLS;
2. a required default certificate for every v1alpha7 snapshot;
3. explicit exact and one-label wildcard SNI bindings;
4. deterministic hostname normalization, validation, and precedence;
5. SAN and certificate-validity checks before activation;
6. one atomic publication for routing and downstream TLS state;
7. certificate rotation for new TLS connections without listener restart;
8. preservation of established HTTP/1.1, HTTP/2, gRPC, and WebSocket traffic;
9. last-known-good behavior for every rejected revision;
10. bounded-cardinality telemetry without hostname or secret disclosure;
11. compatibility for configuration versions v1alpha1 through v1alpha6;
12. correctness, concurrency, lifecycle, fuzz, and performance evidence.

## 4. Non-goals

Phase 3C2 does not add:

- multiple downstream HTTPS listeners or listener resources;
- per-listener TLS policy, cipher-suite, or minimum-version configuration;
- downstream client-certificate authentication;
- ACME, OCSP refresh, certificate discovery, or filesystem watching;
- `SecretRef` resolution or a public configuration update API;
- automatic publication of every DNS SAN in a certificate;
- TLS session-ticket key rotation or session resumption;
- forced closure of connections that used a retired certificate;
- access logging or an integrated APISIX comparison;
- cryptographic secure-memory zeroization guarantees.

Phase 3D continues to own bounded access logging and integrated APISIX
evidence. Phase 4 owns the external control-plane transaction and SecretRef
contract.

## 5. Configuration Contract

### 5.1. v1alpha7 shape

`gateway/v1alpha7` adds a required top-level `downstream_tls` document:

```yaml
api_version: gateway/v1alpha7

listeners:
  https:
    address: ":8443"
    certificate_file: /certs/bootstrap.crt
    private_key_file: /certs/bootstrap.key

certificates:
  - id: default-server
    certificate_file: /secrets/default.crt
    private_key_file: /secrets/default.key
  - id: public-wildcard
    certificate_file: /secrets/public-wildcard.crt
    private_key_file: /secrets/public-wildcard.key

downstream_tls:
  default_certificate_ref: default-server
  sni_bindings:
    - certificate_ref: public-wildcard
      hosts:
        - "*.example.com"
        - api.example.net
```

The strict decoder rejects unknown fields. `default_certificate_ref` must be
non-empty. `sni_bindings` may be empty because a default-only listener remains
valid. Each binding must contain a non-empty `certificate_ref` and at least one
host.

Certificate resources remain generic: the same material type may be referenced
by upstream mTLS and downstream TLS policy. Downstream policy never infers SNI
bindings from certificate SANs. Operators publish only the hostnames explicitly
listed in `sni_bindings`.

### 5.2. Legacy configuration

Versions v1alpha1 through v1alpha6 continue to use the certificate and private
key configured on the bootstrap HTTPS listener for every SNI value. They do not
gain dynamic SNI behavior implicitly.

The bootstrap certificate remains required for process bootstrap and legacy
compatibility. For v1alpha7, the initial runtime snapshot is successfully
compiled before listeners bind, so the snapshot default is authoritative once
the process starts accepting connections. Later successful `Apply` calls may
rotate that default. A failed initial v1alpha7 build prevents startup.

## 6. Canonical Model

`model.ResourceSet` gains an optional downstream policy so legacy internally
constructed resource sets remain representable:

```go
type DownstreamTLSPolicy struct {
    DefaultCertificateRef string
    SNIBindings           []SNIBinding
}

type SNIBinding struct {
    CertificateRef string
    Hosts          []string
}
```

The concrete implementation may use a pointer or an explicit presence marker;
it must preserve the distinction between a legacy absent policy and a v1alpha7
policy. `CloneResourceSet` deep-copies bindings and host slices. Parsed TLS
material handles remain immutable and may be retained by pointer, matching the
Phase 3C1 ownership rule.

The wire decoder is responsible for requiring the policy in v1alpha7. The
runtime compiler is still defensive and rejects a malformed present policy
regardless of its source.

## 7. Hostname Semantics

### 7.1. Normalization

The selector accepts ASCII DNS names only. Configuration and ClientHello names
are canonicalized by:

1. rejecting control characters, whitespace, embedded NUL, IP literals, empty
   labels, and DNS-invalid punctuation;
2. removing exactly one terminal dot when present;
3. converting ASCII letters to lowercase;
4. enforcing at most 253 canonical bytes and at most 63 bytes per label.

Unicode is rejected. A future control plane must convert internationalized
names to their intended A-label representation before distribution. Phase 3C2
does not silently apply IDNA transformations.

An empty or invalid ClientHello SNI is not a configuration error and selects
the default certificate. Configuration hostnames are strict and cause the
candidate revision to be rejected when invalid.

### 7.2. Exact and wildcard matching

An exact binding is a canonical DNS name such as `api.example.com`.

A wildcard binding has exactly one leading `*.` and at least two following
labels, such as `*.example.com`. It matches exactly one label:

- `api.example.com` matches `*.example.com`;
- `a.b.example.com` does not match;
- `example.com` does not match.

Exact lookup always precedes wildcard lookup. Wildcard lookup removes the first
label and looks up the remaining suffix in an immutable map. It does not walk a
suffix tree or attempt successively broader matches.

Every canonical exact name and wildcard suffix may occur only once across the
candidate policy. Duplicate declarations are rejected even when they reference
the same certificate. This avoids declaration-order semantics.

### 7.3. SAN coverage

Downstream bindings use DNS SANs only; Common Name fallback is prohibited.

- An exact binding may be covered by an exact DNS SAN or by a valid one-label
  wildcard DNS SAN according to Go hostname verification semantics.
- A wildcard binding requires the corresponding canonical wildcard DNS SAN.
  Testing one representative hostname is insufficient because an exact SAN for
  that representative would not cover the wildcard namespace.
- The default certificate is structurally and temporally validated but is not
  required to cover every unmatched or absent SNI value.

## 8. Selector Ownership and Hot Path

`internal/downstreamtls` owns a compiler and immutable selector. The selector
contains:

- one default `tls.Certificate`;
- one exact-name map;
- one wildcard-suffix map;
- bounded counts and the earliest referenced leaf expiration time.

All certificate references, hostname rules, SAN coverage, time validity, and
limits are resolved during compilation. The handshake path performs no file
I/O, PEM parsing, certificate parsing, hostname-sized allocation, or map
mutation.

The selector returns a pointer to immutable certificate state. A caller holding
that pointer keeps the selected certificate reachable even if a concurrent
`Apply` retires the snapshot that owned the selector.

The aggregate number of configured SNI hosts is limited to 10,000. This is
separate from and in addition to the existing limits of 10,000 TLS material
resources and 64 MiB aggregate TLS source bytes. The bounds are constants for
this phase rather than operator-tunable bootstrap settings.

## 9. Atomic Runtime Activation

### 9.1. Snapshot content

The immutable runtime snapshot gains its compiled downstream TLS selector and
bounded selector statistics. The runtime builder receives the bootstrap static
certificate dependency needed for legacy resource sets.

The HTTPS listener uses:

```go
tlsConfig.GetCertificate = manager.GetCertificate
tlsConfig.SessionTicketsDisabled = true
```

`Manager.GetCertificate` loads the active snapshot pointer exactly once and
delegates to that snapshot's selector. It returns a stable internal error when
there is no active snapshot or the manager is closed. It never includes the raw
SNI value or certificate data in that error.

### 9.2. Apply transaction

`Manager.Apply` retains its serialized transaction:

1. lock the apply mutex;
2. reject closed or stale revisions;
3. prepare upstream candidates;
4. clone and validate the complete resource set;
5. compile routes, plugins, upstream plans, and downstream SNI selector;
6. transfer prepared upstream ownership;
7. publish the complete snapshot with one atomic swap;
8. retire the prior plan set and report bounded lifecycle statistics.

No selector is externally visible before step 7. Every error before publication
rolls back the upstream candidate and preserves the complete active revision.
There is no second downstream-TLS commit and therefore no partial-publication
rollback path.

Concurrent `Apply` calls remain serialized. A stale lower revision cannot
replace either routing or certificate state after a higher revision wins.

### 9.3. Rotation behavior

A successful rotation affects only TLS connections whose certificate selection
starts after the new snapshot is published. Established HTTP keep-alive
connections, HTTP/2 and gRPC streams, and WebSocket tunnels continue without
drain or forced closure.

TLS session tickets are disabled in Phase 3C2. Consequently, a new connection
cannot resume a session that bypasses presentation of the active certificate.
Ticket-key lifecycle may be reconsidered with the security and distribution
contracts of Phase 4.

Retired certificate material requires no dedicated reaper. Active handshakes
retain selected certificate pointers; active request and tunnel lifecycle
continues to use the ownership already provided by snapshots and transport
leases.

## 10. Certificate Time Policy

Every certificate referenced by downstream TLS must have a parsed leaf whose
current time is within its inclusive `NotBefore`/`NotAfter` validity interval at
candidate compilation. The compiler uses an injected clock in tests.

A not-yet-valid or expired referenced certificate rejects the whole candidate.
An active certificate that expires after publication does not cause an
automatic rollback, selector mutation, or listener shutdown. Clients verifying
the certificate will reject it. Telemetry exposes the approaching expiration so
operators or the future control plane can rotate before that point.

The phase does not introduce a timer-driven apply path. Configuration remains
the only source of runtime revision changes.

## 11. Error Model

Downstream TLS build failures use `runtime.BuildError` with validate-stage
metadata, the candidate revision, a stable resource kind, and a bounded field.
Stable error codes include:

- `DOWNSTREAM_TLS_REQUIRED`;
- `DEFAULT_CERTIFICATE_NOT_FOUND`;
- `SNI_BINDING_INVALID`;
- `SNI_BINDING_CONFLICT`;
- `CERTIFICATE_HOSTNAME_MISMATCH`;
- `CERTIFICATE_NOT_YET_VALID`;
- `CERTIFICATE_EXPIRED`;
- `SNI_INDEX_LIMIT_EXCEEDED`.

Operator-facing configuration errors may identify a resource ID and field.
Lifecycle logs and metrics continue to expose only closed dimensions such as
code, stage, and resource kind. They never include ClientHello SNI, configured
hostnames, certificate DER/PEM, private keys, file contents, or raw parse-error
text.

An invalid ClientHello SNI is classified as a default selection rather than a
build error. A missing active selector is an internal handshake error.

## 12. Telemetry

Published snapshot statistics add:

- referenced downstream certificate count;
- exact binding count;
- wildcard binding count;
- duration until the earliest referenced certificate expiration.

Certificate selection increments a counter whose only selection labels are
`exact`, `wildcard`, `default`, and `error`. No hostname or certificate ID is a
metric label. Expiration values are aggregate gauges, not per-certificate
series.

Apply and rejection events reuse the existing lifecycle observer. No
per-request access log is added before Phase 3D.

## 13. Verification Strategy

### 13.1. Configuration and model

Tests cover strict v1alpha7 decoding, required downstream policy, default and
binding references, unknown fields, deep clone behavior, source limits, and
legacy v1alpha1-v1alpha6 behavior. The Phase 3C2 example must load through the
same public configuration entry point as the gateway executable.

### 13.2. Selector unit and fuzz tests

Table-driven tests cover:

- exact precedence over wildcard;
- one-label wildcard boundaries;
- case and trailing-dot normalization;
- empty and invalid ClientHello fallback;
- invalid configuration names and wildcard forms;
- conflicts after canonicalization;
- exact and wildcard SAN coverage;
- absent Common Name fallback;
- not-yet-valid and expired leaves;
- missing references and aggregate limits.

Fuzz targets exercise normalization, matching, and selector compilation. They
must not panic, create ambiguous aliases, escape one-label wildcard semantics,
or construct state beyond configured bounds.

### 13.3. Runtime and concurrency tests

Runtime tests prove that routing and certificate selection always report the
same active revision, rejected candidates retain the last-known-good revision,
concurrent applies preserve strict revision ordering, and shutdown/apply races
have stable outcomes.

Lifecycle tests repeat at least 100 rotations while concurrent handshakes run.
After clients release connections, runtime and upstream ownership counts must
return to their documented steady state.

### 13.4. Protocol integration

Process-level TLS tests cover default, exact, and wildcard certificates over
HTTP/1.1 and HTTP/2, plus native gRPC and classic WebSocket traffic. They prove:

- a successful rotation changes the certificate seen by new connections;
- an invalid rotation leaves traffic and certificates on the old revision;
- established HTTP/2/gRPC streams and WebSocket tunnels survive rotation;
- clients configured with a session cache do not resume TLS sessions;
- hostname matching remains correct during concurrent Apply operations.

Test certificates and private keys are generated in temporary test directories
and are never committed.

### 13.5. Performance and quality gates

Benchmarks compare static/default, exact, and wildcard lookup at a 10,000-name
index and measure selector build plus full snapshot rotation. The lookup target
is expected O(1) behavior with zero hot-path allocations. Results are recorded
as developer-machine evidence and repeated on the canonical Linux environment;
machine-specific absolute latency is not a portable pass threshold.

Submission gates are:

- `gofmt` on changed Go files;
- `go test ./... -count=1`;
- `go test ./... -race -count=1` on a CGO-capable host;
- `go vet ./...`;
- `staticcheck -tests=false ./...`;
- `revive -set_exit_status -config revive.toml -formatter default ./...`;
- `go build ./cmd/...`;
- focused fuzz, lifecycle, benchmark, and canonical Linux evidence.

Phase 3C2 makes no APISIX parity or production-certification claim.

## 14. Documentation and Operational Handoff

Implementation adds:

- `configs/phase3c2.yaml` with default, exact, and wildcard bindings;
- a Phase 3C2 runbook describing rotation, rollback, expiration, and safe
  inspection;
- a Phase 3C2 current-status ledger separating implementation completion from
  canonical evidence;
- README and roadmap updates that retain Phase 3D as the final umbrella Phase
  3C work.

The runbook must state that deleting a binding does not terminate established
connections, invalid updates preserve the active revision, expired active
certificates are not automatically replaced, and session tickets are disabled.

## 15. Exit Criteria

Phase 3C2 implementation is complete when:

1. v1alpha7 default, exact, and wildcard certificates work end to end;
2. one atomic snapshot revision controls routing and certificate selection;
3. invalid revisions preserve both active traffic and active certificates;
4. new connections observe a successfully rotated certificate;
5. established HTTP/2, gRPC, and WebSocket traffic survives rotation;
6. session-resumption tests prove new connections perform full handshakes;
7. selector lookup remains bounded and allocation-free at the 10,000-name
   scale;
8. correctness, concurrency, lifecycle, fuzz, static-analysis, and build gates
   pass;
9. canonical evidence is either complete or explicitly recorded as pending;
10. documentation does not claim APISIX parity or completion of umbrella Phase
    3C before Phase 3D.

After Phase 3C2, Phase 3D may add bounded access logging and integrated APISIX
comparison without changing selector ownership. Phase 4 may replace the local
file source with control-plane full snapshots and SecretRefs while retaining
the same validated canonical policy and atomic activation contract.

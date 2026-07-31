# Phase 3C1 Upstream TLS and Protocol Runbook

Phase 3C1 adds verified outbound TLS/mTLS, HTTP/2 over TLS, h2c prior knowledge, and native gRPC pass-through. The runnable configuration contract is [`configs/phase3c1.yaml`](../../configs/phase3c1.yaml). Mount all certificate, key, trust, and gateway configuration files read-only.

## Protocol contract

| Endpoint scheme | Protocol | Behavior |
|---|---|---|
| `http` | `auto` | HTTP/1.1 |
| `http` | `http1` | HTTP/1.1 only |
| `http` | `http2` | h2c prior knowledge; no probing or HTTP/1 fallback |
| `https` | `auto` | ALPN prefers HTTP/2 and falls back to HTTP/1.1 |
| `https` | `http1` | HTTP/1.1 over TLS only |
| `https` | `http2` | HTTP/2 over TLS is required |

All positive-weight endpoints in an upstream must use one scheme. TLS policy on `http` is invalid. A strict protocol mismatch fails the attempt.

## Trust, identity, and names

An HTTPS upstream without `trust_bundle_ref` uses system roots. A referenced trust bundle replaces system roots; it does not append to them. Put public and private roots in the same file when both must be trusted.

`server_name` controls both TLS SNI and hostname verification. When absent, the selected endpoint hostname is used. It does not rewrite HTTP `Host` or HTTP/2 `:authority`; inbound-host forwarding remains a separate request policy.

`client_certificate_ref` enables mTLS with one immutable certificate chain and matching private key. Every TLS generation enforces a TLS 1.2 minimum and Go's default cipher suites and curves. Verification cannot be disabled.

## Material loading and limits

The standalone loader reads material once during load/apply. It validates and parses the complete candidate before activation, retains parsed immutable values instead of source PEM, and has no filesystem watcher.

- CA bundle file: 1 MiB maximum.
- Certificate-chain file: 256 KiB maximum.
- Private-key file: 256 KiB maximum.
- Certificate and trust-bundle resources combined: 10,000 maximum.
- Aggregate candidate source bytes: 64 MiB maximum.

Changing a mounted file has no effect until the document is loaded and applied again. Missing, oversized, malformed, duplicate, mismatched, or unresolved material rejects the candidate and preserves the last-known-good revision.

## Rotation, rollback, and pool ownership

Public certificate-chain and canonical CA-set fingerprints participate in the complete transport key. A successful material, protocol, SNI, timeout, or pool-policy change creates a new production/probe transport generation. Unchanged profiles reuse their existing pools.

Apply rotations in this order:

1. Make the new CA/client material valid at the upstream.
2. Atomically replace mounted files.
3. Load and apply the complete `gateway/v1alpha5` document.
4. Confirm readiness, apply-success telemetry, new generation creation, and successful handshakes.
5. Wait for retired snapshot and transport gauges to return to steady state.
6. Remove old upstream trust only after the overlap window.

If apply fails, fix the candidate and reapply; the active revision and its pools remain in service. A committed old generation retires only after all request/snapshot leases release. Production requests and health probes use separate pools even when their security policy is identical.

## Health, retries, streaming, and gRPC

HTTP active health uses the same TLS profile, SNI, client identity, and protocol policy as production traffic through a separate probe transport. TCP health checks raw reachability and does not prove TLS or application health.

Typed TLS failures participate in the existing connection-failure retry policy. Retry still requires a replay-safe method/body, a distinct endpoint, remaining budget, and remaining deadline. The downstream stable response is:

```json
{"code":"UPSTREAM_TLS_FAILED","message":"upstream TLS failed"}
```

Native gRPC is passed through as HTTP/2 without protobuf inspection, transcoding, gRPC-status retry, or application-aware buffering. Headers, trailers, status, streaming, and cancellation are preserved. Configure `total_timeout: 0` for intentionally unbounded streams, then rely on client cancellation, listener drain, and upstream policy. `max_attempts: 1` is recommended for streaming RPCs.

## Metrics and cardinality

Inspect the private admin listener:

```powershell
curl.exe http://localhost:9090/readyz
curl.exe http://localhost:9090/metrics
```

Phase 3C1 adds exactly these bounded families:

- `gateway_upstream_tls_handshake_total{result,mode,protocol}`: 12 prebound series for `success|failure`, `server_auth|mtls`, and `auto|http1|http2`.
- `gateway_upstream_tls_failure_total{class}`: 5 prebound series for `trust|hostname|client_identity|protocol|handshake`.
- `gateway_upstream_transport_generation_total{action,tls,protocol}`: 18 prebound series for `create|reuse|retire`, `false|true`, and `auto|http1|http2`.

Never add endpoint URLs, server names, certificate IDs/fingerprints, file paths, peer certificate data, raw errors, client addresses, or private material as labels. Existing live-transport, retired-snapshot, apply, and cleanup metrics remain the primary leak/retirement signals.

## Troubleshooting

Use stable categories and counters; do not log PEM, private keys, peer certificates, raw fingerprints, or unredacted TLS errors.

- `trust`: confirm the replacement bundle contains the complete issuer chain; remember it does not include system roots.
- `hostname`: compare endpoint hostname/fixed `server_name` with certificate SANs.
- `client_identity`: confirm the upstream trusts the configured client chain and its usage.
- `protocol`: confirm ALPN for strict HTTPS HTTP/2 or prior-knowledge h2c support.
- `handshake`: check TLS 1.2+, upstream listener mode, and mTLS requirements.

A rising create count without matching retire count, growing `gateway_upstream_live_transports`, or nonzero retired snapshots after traffic drains indicates held leases or cleanup failure. A TCP health success does not rule out TLS failures.

## Readiness-first shutdown

Shutdown removes readiness first, stops new health work and configuration updates, drains HTTP/gRPC traffic and request leases, retires plan sets, closes production and probe transports, then stops the admin listener. Keep polling `/readyz` during rollout and stop sending new traffic as soon as it returns `503`.

## Verification

```powershell
go test ./internal/tlsmaterial ./internal/upstream ./internal/runtime ./internal/proxy ./internal/gateway ./test/integration -count=1
go test ./internal/upstream ./internal/proxy -run 'TestPhase3C1' -count=1 -v
$env:GATEWAY_PHASE3C1_ACCEPTANCE = '1'
go test ./internal/upstream ./internal/proxy -run 'TestPhase3C1' -count=1 -v
Remove-Item Env:GATEWAY_PHASE3C1_ACCEPTANCE
go test ./internal/upstream ./internal/proxy -run '^$' -bench 'BenchmarkTransportProfile|BenchmarkTLSHandshake|BenchmarkHTTPProtocol|BenchmarkGRPC|BenchmarkTransportGeneration' -benchmem -count=5
```

Canonical race, protocol, and comparative performance acceptance still require the reference Linux/CGO environment. Integrated APISIX comparison remains Phase 3D work.

# Phase 3C2 Downstream SNI and Certificate Rotation Runbook

Phase 3C2 adds strict `gateway/v1alpha7` downstream TLS policy, immutable certificate resources, default/exact/wildcard SNI selection, and atomic certificate rotation. The runnable configuration contract is [`configs/phase3c2.yaml`](../../configs/phase3c2.yaml). Mount configuration, certificate, key, and trust files read-only, and keep the admin listener private.

## Validate and start the example

The checked-in example declares three downstream identities:

- `default-server` is presented when SNI is absent, malformed, or unmatched;
- `exact-server` is presented for `api.example.com` and wins over a wildcard;
- `wildcard-server` is presented for one-label names such as `shop.example.com` through `*.example.com`.

First validate the checked-in v1alpha7 shape with the test that substitutes temporary valid material for the example paths:

```powershell
go test ./internal/config -run TestPhase3C2ExampleConfigurationHasDownstreamTLSShape -count=1 -v
```

There is no validate-only production command. For an operator-owned copy, place matching certificate/key files at every configured path, then start the process; startup strictly decodes the complete document, loads bounded material, validates references, SAN coverage, validity, conflicts, and the 10,000-name limit before binding traffic listeners:

```powershell
go run ./cmd/gateway-dp -config configs/phase3c2.yaml
```

A nonzero exit rejects the document. Do not send traffic until the private readiness endpoint returns `200`:

```powershell
curl.exe -i http://127.0.0.1:9090/readyz
curl.exe http://127.0.0.1:9090/metrics
```

## Verify the presented leaf

Open a new TLS connection for each selection class. These commands inspect the leaf actually presented by the listener; add a trusted `-CAfile` and `-verify_return_error` when chain verification is also required.

```bash
openssl s_client -connect 127.0.0.1:8443 -servername api.example.com -showcerts </dev/null 2>/dev/null \
  | openssl x509 -noout -subject -issuer -serial -dates -ext subjectAltName

openssl s_client -connect 127.0.0.1:8443 -servername shop.example.com -showcerts </dev/null 2>/dev/null \
  | openssl x509 -noout -subject -issuer -serial -dates -ext subjectAltName

openssl s_client -connect 127.0.0.1:8443 -servername unbound.invalid -showcerts </dev/null 2>/dev/null \
  | openssl x509 -noout -subject -issuer -serial -dates -ext subjectAltName
```

Expect the exact, wildcard, and default leaf respectively. Matching is ASCII case-insensitive, accepts one trailing dot, and permits only a single leftmost wildcard label. A wildcard for `*.example.com` matches `shop.example.com`, not `deep.shop.example.com`.

## Rotate through the internal Apply seam

Phase 3C2 has no public reload endpoint, file watcher, or signal reload. Changing a mounted PEM file alone has no effect. The only live-update seam is the internal Go method `Gateway.Apply(nextRevision, completeResourceSet)`, used by tests and reserved for the future control plane.

For a controlled internal integration:

1. Create and load the replacement certificate resource under a new ID, retaining the old resource during the overlap window.
2. Deep-clone the complete last-known-good `model.ResourceSet`.
3. Replace one reference in the candidate:
   - default rotation: set `downstream_tls.default_certificate_ref` to the new ID;
   - exact rotation: set the binding containing the exact host to the new ID;
   - wildcard rotation: set the binding containing the wildcard host to the new ID.
4. Call `Gateway.Apply` with a strictly greater revision. Never construct a partial resource set.
5. Require a nil result, confirm the active revision and apply metrics, then open a new `openssl s_client` connection for the affected SNI and verify the new leaf serial/SAN.
6. Verify an established HTTP/1.1, HTTP/2, gRPC, or WebSocket connection continues normally, and wait for retired ownership metrics to return to steady state.
7. Remove the old resource only in a later complete revision after the overlap and rollback window.

An invalid update preserves the complete active revision. Fix the candidate and retry with a newer revision; do not assume a rejected revision partially changed routing or TLS selection.

## Readiness and bounded metrics

Check these families on the private `/metrics` endpoint:

- `gateway_runtime_active_revision`: active immutable snapshot revision;
- `gateway_runtime_snapshot_apply_total{result,stage,code}`: applied or rejected candidates with bounded stable labels;
- `gateway_runtime_retired_snapshots`: old plan sets awaiting release;
- `gateway_downstream_tls_active_certificates`: distinct certificates in the active selector;
- `gateway_downstream_tls_exact_bindings`: active exact SNI names;
- `gateway_downstream_tls_wildcard_bindings`: active wildcard SNI suffixes;
- `gateway_downstream_tls_earliest_expiry_seconds`: seconds until the earliest active leaf expiry, decaying to zero;
- `gateway_downstream_tls_certificate_selections_total{selection}`: four pre-bound series for `exact`, `wildcard`, `default`, and `error`.

No SNI, certificate ID, fingerprint, file path, peer identity, or raw error is exposed as a metric label. A rejected apply should increment the rejected series while `gateway_runtime_active_revision` and the active selector gauges remain unchanged.

## Diagnose stable rejection codes

| Code | Meaning and action |
| --- | --- |
| `DOWNSTREAM_TLS_REQUIRED` | The dynamic selector was compiled without a policy. Supply the complete v1alpha7 `downstream_tls` object. |
| `DEFAULT_CERTIFICATE_NOT_FOUND` | `default_certificate_ref` is empty or unresolved. Add the certificate resource and use its exact ID. |
| `SNI_BINDING_INVALID` | A certificate resource or binding is malformed, empty, unresolved, duplicated by ID, or contains an invalid hostname/wildcard. Inspect the reported bounded `resource` and `field`. |
| `SNI_BINDING_CONFLICT` | Two entries canonicalize to the same exact name or wildcard suffix. Remove the duplicate; case and a trailing dot do not make names distinct. |
| `CERTIFICATE_HOSTNAME_MISMATCH` | The referenced leaf DNS SANs do not cover the binding. Common Name alone is not accepted; issue a certificate with the required exact or wildcard DNS SAN. |
| `CERTIFICATE_NOT_YET_VALID` | The leaf's `NotBefore` is later than candidate build time. Correct clock skew or wait/use currently valid material. |
| `CERTIFICATE_EXPIRED` | The leaf was already expired when the candidate was built. Replace it with currently valid material. |
| `SNI_INDEX_LIMIT_EXCEEDED` | The candidate contains more than 10,000 exact and wildcard names in total. Reduce or split the policy before applying it. |

All candidate errors are evaluated before publication. Do not log PEM, private keys, raw fingerprints, or unbounded underlying errors while diagnosing them.

## Operational warnings

- Deleting or changing a binding does not terminate established connections; it affects only new TLS handshakes after publication.
- An invalid update preserves the active revision and its complete routing/TLS state.
- An active certificate that later expires is not automatically replaced. Alert on the earliest-expiry gauge and apply valid replacement material before expiry.
- Session tickets are disabled, so every new TLS connection performs a full handshake. Capacity planning must include handshake CPU and latency.
- No public reload endpoint exists before Phase 4. Do not build operator automation around the internal `Apply` test seam.

## Verification and claims boundary

```powershell
go test ./internal/downstreamtls ./internal/config ./internal/runtime ./internal/gateway ./internal/telemetry ./test/integration -count=1
go test ./internal/gateway ./test/integration -run 'Test.*Phase3C2|TestDownstreamTLS' -count=20
go test ./internal/downstreamtls -run '^$' -bench 'Benchmark(Selector(Default|Exact|Wildcard)|CompileSelector)10K' -benchmem -count=5
```

Canonical race, lifecycle, resource, and performance acceptance still require the reference Linux/CGO environment. Phase 3D owns bounded access logging and integrated APISIX comparison. Local Phase 3C2 evidence is not APISIX parity or production certification, and the umbrella Phase 3C remains incomplete.

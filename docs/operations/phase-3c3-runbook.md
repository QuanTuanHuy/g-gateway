# Phase 3C3 WebSocket Operations Runbook

Phase 3C3 adds opt-in classic RFC 6455 WebSocket proxying to `gateway/v1alpha6`. The gateway validates the HTTP/1.1 handshake, applies existing routing/plugins/retry policy before commitment, then copies bytes opaquely until close, idle expiry, or shutdown.

## Configure and run

Start the deterministic cleartext upstream and the Phase 3C3 example:

```bash
go run ./cmd/test-upstream -listen :8081
go run ./cmd/gateway-dp -config configs/phase3c3.yaml
```

The checked-in file expects the downstream identity at `/certs/server.crt` and `/certs/server.key`, and the verified upstream CA at `/secrets/internal-ca.pem`. Mount certificate and configuration files read-only. The admin listener must remain private.

`services[].websocket` supplies inheritable defaults. In the example, `realtime` enables WebSocket with a `60s` idle timeout. `realtime-unbounded-idle` inherits enablement but overrides its timeout to `0`, which disables idle expiry. A Route can explicitly set `enabled: false` to force an ordinary HTTP request through hop-by-hop header stripping.

Use `transport.protocol: auto` or `http1` for enabled WebSocket Routes. `auto` retains HTTP/2 negotiation for ordinary TLS traffic but uses a separate HTTP/1.1 upgrade pool. Strict `http2` is rejected during snapshot compilation.

Example clients:

```bash
websocat ws://127.0.0.1:8080/local/websocket/echo
websocat --insecure wss://127.0.0.1:8443/local/websocket/echo
```

The `local-clear` upstream shows `http://` transport. The `realtime-tls` upstream shows verified `https://` transport with replacement trust and fixed SNI. Do not use insecure upstream TLS; install the correct CA bundle and `server_name` instead.

## Retry and handshake behavior

Retries happen only before a valid upstream `101` is committed. Connect failures, eligible connection failures, response-header timeouts, and invalid `101` responses follow the existing distinct-endpoint retry and budget rules. A terminal non-`101` response is forwarded as ordinary HTTP. Once committed, a tunnel is never replayed, reconnected, or migrated.

Malformed enabled candidates return `400 INVALID_WEBSOCKET_HANDSHAKE`. An invalid final upstream handshake returns `502 UPSTREAM_WEBSOCKET_HANDSHAKE_INVALID`. Requests on disabled Routes remain ordinary HTTP.

## Reload and shutdown

Applying a revision affects only new handshakes. Existing tunnels survive Route deletion, WebSocket disablement, and upstream rotation while retaining a lease on only their selected transport generation. Closing the tunnel releases that lease and permits retired transport cleanup.

Shutdown first removes readiness and closes tunnel admission. Established tunnels may finish naturally until the shutdown deadline. At the deadline, remaining sessions are force-closed and their leases are released. No WebSocket close frame is synthesized because the gateway is an opaque byte tunnel.

## Telemetry

The private `/metrics` endpoint exports exactly these bounded families:

- `gateway_websocket_handshakes_total{result=...}`
- `gateway_websocket_active_tunnels`
- `gateway_websocket_tunnels_closed_total{reason=...}`
- `gateway_websocket_tunnel_duration_seconds`
- `gateway_websocket_bytes_total{direction=...}`

They contain no Route, Service, upstream, host, endpoint, client, revision, key, authorization, cookie, payload, certificate, or peer-error labels.

## Troubleshooting

- A startup error mentioning strict HTTP/2 means an enabled WebSocket Route resolves to `transport.protocol: http2`; use `auto` or `http1`.
- `UPSTREAM_TLS_FAILED` means trust, SNI, certificate validity, or mTLS policy failed before upgrade. Verify mounted material and `server_name`.
- `UPSTREAM_UNHEALTHY` means no distinct endpoint is selectable under current health state.
- `GATEWAY_DRAINING` means shutdown closed admission between upstream selection and tunnel registration; retry against a ready instance.
- An idle close is controlled by the effective Service/Route `idle_timeout`; set `0` only when external lifecycle ownership is deliberate.

Phase 3C3 does not implement RFC 8441 Extended CONNECT, dynamic downstream exact/wildcard SNI, per-client tunnel quotas, access logs, frame parsing/transformation, or APISIX comparison. Phase 3C2 owns dynamic downstream SNI; Phase 3D owns bounded access logs and integrated comparison.

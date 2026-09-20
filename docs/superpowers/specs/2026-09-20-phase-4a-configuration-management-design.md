# Phase 4A Configuration Management Design

**Date:** 2026-09-20

**Status:** Written specification approved by the user on 2026-09-20, including the concrete choices identified in section 2. No implementation or acceptance evidence is claimed.

**Plan:** [Phase 4A implementation plan](../plans/2026-09-20-phase-4a-configuration-management.md).

**Parent:** [Phase 4 control plane design](2026-09-20-phase-4-control-plane-design.md).

**Implementation gate:** [Phase 3D1 bounded access logging](2026-09-20-phase-3d1-bounded-access-logging-design.md) must be implementation-complete with its required CI evidence before Phase 4 executable-code work begins. Phase 3D2 remains a separate acceptance obligation. This document neither implements a control plane nor claims benchmark, compatibility, or production acceptance.

## 1. Outcome and scope

Phase 4A adds one active `gateway-cp` process that accepts authenticated, revision-checked resource mutations and durably publishes immutable configuration artifacts in etcd. A CP restart retrieves exactly the committed resources and resolved material even if mounted secret files have changed or disappeared.

The output for 4B is an internal, bounded committed-artifact reader. Phase 4A does not expose material downloads through Admin API and does not add a production DP update surface. gRPC distribution, rollout endpoints, and managed DP startup/persistence belong to 4B/4C.

The complete publication unit is:

```text
declarative resource graph + resolved material + effective snapshot
  + global revision + committed idempotency result + success audit
```

All DPs ultimately receive one shared configuration. Single-resource CRUD uses the same transaction service as multi-resource transactions. No independent per-resource revision, delta, automatic rollback, namespace targeting, or multi-CP HA is introduced.

## 2. Decision provenance and review additions

The discussion approved global decimal-string revisions, final-state validation, put/delete transactions, mandatory idempotency, pinned reads/cursors, explicit SecretRef refresh, rollback of historical material, revision creation for no-op content, bounded sequential mutation processing, staged artifacts with atomic manifest publication, per-artifact envelope encryption, separate audit of successes/failures, retention, startup/outage policy, and the baseline limits in section 12. The Admin endpoint layout is also approved.

The written specification supplies concrete choices needed to make those decisions implementable:

- Managed resource and artifact field mapping, HTTP errors, and canonical request identity.
- Stable mTLS principal extraction and a prefix ownership guard preventing accidental overlapping CP writers.
- Artifact encoding, signing/encryption profile, chunk size, and staging/publication/cleanup state transitions.
- Pagination cursor encoding, audit retention, additional memory/storage budgets, and timeouts.
- Exact recovery behavior for uncertain commits and expired idempotency records.

These additions were approved together in the written specification review; they were not individually approved during the earlier discussion. No placeholder requires an implementer to invent a policy.

## 3. Existing code and package boundaries

Current `internal/config` decodes standalone YAML through v1alpha7 and resolves TLS material from files. `internal/model.ResourceSet` holds parsed immutable TLS handles rather than a directly portable Admin document. `runtime.Manager.Apply` publishes strictly newer snapshots; its builder requires an upstream candidate. CP must not call that builder with live transport preparation merely to validate a mutation.

Use these responsibilities:

| Package/component | Contract |
| --- | --- |
| `cmd/gateway-cp` | Bootstrap, listeners, dependency composition, signals, shutdown |
| `internal/controlplane` | Serialized mutation orchestration and CP readiness; depends on storage/resolver/compiler interfaces |
| `internal/adminapi` | mTLS authorization, bounded HTTP decode, DTO conversion, response/error mapping |
| `internal/resource` | Managed declarative DTOs, strict decode, reference expansion, shared pure validation |
| `internal/snapshot` | Versioned portable bundle, deterministic encoding, signatures and storage envelopes |
| `internal/secretref` | Root-confined file reads and explicit material refresh |
| `internal/cpstore` | Etcd layout, publication, idempotency, audit, artifact lifetime and recovery |
| Existing model/router/plugin/TLS packages | Reused semantic validation and immutable material constructors |

Extract only the validation needed by CP and DP. Preserve standalone conversions, routing precedence, defaulting and presence semantics. Shared validation may compile pure route/plugin/TLS structures for checking, but must not dial, start health probes, create live transport pools, or bind listeners. The request path continues to use existing runtime packages.

Nearby regression coverage includes config strict-decoding/version tests, `TestNewCertificateRejectsMismatchedPrivateKey`, `TestLoadMaterialRejectsOversizedAndNonRegularFiles`, and runtime builder/manager rollback tests. These are existing seams, not evidence that 4A works.

## 4. Bootstrap and authority

Bootstrap is strict YAML with `api_version: gateway-cp/v1alpha1`. It contains:

- Immutable configuration-domain ID and an exclusive etcd prefix.
- Private Admin HTTPS listener, telemetry listener, mTLS server identity, and Admin client trust.
- Explicit principal-to-Reader/Operator mapping.
- Etcd endpoints, CP client identity/trust, and dial/request limits.
- Secret mount names mapped to absolute root directories.
- Active signing key ID/private-key file plus trusted public-key files needed to verify retained artifacts.
- Active storage key ID/file plus old storage key files needed to decrypt retained artifacts.
- The configurable limits in section 12.

Paths and credentials are bootstrap inputs, not dynamic resources. Keys and secret mounts are read-only. Key IDs are immutable names: supplying new bytes under an existing ID is a startup error. Key fingerprints used to detect mismatch stay internal and are not metric labels.

Admin principal is the single URI SAN in a verified client certificate, bounded to 256 UTF-8 bytes. Match its exact URI string against bootstrap policy; do not fall back to Common Name, request headers, or a DP-supplied identity. Missing/ambiguous identity is refused. Reader may read declarative resources/revision metadata. Operator may also mutate/rollback. A DP certificate gains no Admin role without an explicit Admin policy entry. The eventual distribution listener has separate authorization.

Only one CP may own a domain prefix at a time. A lease-backed owner token rejects a second writer; it is an exclusion guard, not an HA election service. Every store mutation and GC batch compares the owner token. Loss of confirmed ownership stops mutation and GC and invalidates outstanding consistent Admin operations. A replacement process reconciles storage before serving; automatic rolling failover is outside 4A.

## 5. Managed resource schema

Admin version is selected by the `/v1` URL. A resource wrapper has exactly `kind`, `id`, and `spec`. Kinds are case-sensitive: Route, Service, Upstream, PluginConfig, Certificate, SecretRef, TrustBundle, DownstreamTLS. URLs use those same kind names.

Managed IDs are 1–128 ASCII characters matching `[A-Za-z0-9][A-Za-z0-9._-]*`. This restriction applies to the new managed API, not old YAML. DownstreamTLS has the fixed ID `default`; other IDs for that kind are invalid.

JSON uses the existing standalone snake_case field names and existing field types/defaults, with duration strings and explicit optional presence. ID lives in the wrapper, never duplicated inside spec. The exhaustive source of inherited fields is the v1alpha7 document graph in `internal/config/wire_v1alpha2.go` through `wire_v1alpha7.go`; bootstrap fields are excluded. The following overrides/additions are the managed schema:

| Kind | Spec contract |
| --- | --- |
| Route | Existing route fields excluding ID; add optional `plugin_config_ref` |
| Service | Existing service fields excluding ID; add optional `plugin_config_ref` |
| Upstream | Existing endpoint, balancer, transport, health and retry fields excluding ID |
| PluginConfig | Required `plugins` list with existing name/enabled/config attachment shape |
| SecretRef | Required `mount` and `path` strings; no material bytes or arbitrary URI |
| Certificate | Required `certificate_secret_ref` and `private_key_secret_ref`; no file path fields |
| TrustBundle | Required `ca_secret_ref`; no file path fields |
| DownstreamTLS | Existing default_certificate_ref and sni_bindings shape |

Reject unknown/duplicate JSON fields, trailing documents, invalid UTF-8, duplicate operations for a kind/ID, explicit null where no nullable field is defined, and unknown plugin names/config fields. Route/Service cannot contain both the `plugins` field (even empty) and `plugin_config_ref`. Keep existing per-scope plugin duplicate/disable rules.

Only existing built-in plugins are supported. CP expands PluginConfig into effective attachments while retaining declarations for reads/rollback. PluginConfig cannot reference another PluginConfig. Existing Route/Service inheritance stays unchanged.

A valid committed graph has at least one route, valid targets and all referenced resources, plus the DownstreamTLS singleton and its valid default certificate. Revision zero is an unconfigured store, not an activatable empty graph. Bootstrap never supplies a dynamic downstream certificate implicitly in managed mode.

Material bytes, parsed private-key objects, and the effective snapshot are never Admin read fields. SecretRef reads expose only logical mount/path. Existing plugin/header values remain operator configuration; clients must not treat that API as a secret-upload interface.

## 6. Admin API and reads

| Method/path | Request |
| --- | --- |
| POST /v1/transactions | `expected_revision`, nonempty `operations` |
| GET /v1/resources/{kind} | Optional `revision`, `limit`, `cursor` |
| GET /v1/resources/{kind}/{id} | Optional `revision` |
| PUT /v1/resources/{kind}/{id} | Spec object as body; required `expected_revision` query |
| DELETE /v1/resources/{kind}/{id} | No body; required `expected_revision` query |
| GET /v1/revisions | Optional `limit`, `cursor` |
| POST /v1/rollbacks | `expected_revision`, `source_revision` |

Mutation requires exactly one `Idempotency-Key` header. Body-bearing mutations require application/json; request compression is unsupported. Query names are strict, duplicates fail, and body/query revision disagreement cannot be silently resolved.

An operation is `{"op":"put","kind":"SecretRef","id":"orders-key","spec":{"mount":"tls-material","path":"orders/server.key"}}`, or a delete without spec. Rollback has no operations list. PUT wraps into one put and DELETE into one delete internally.

The full response envelope is bounded by record/page limits; methods outside this table return 405 with Allow. All JSON revisions are canonical decimal strings in the uint64 domain: no leading zero except `"0"`, signs, whitespace, fractions, exponent, or JSON number. Expected zero is legal only at empty head. Commit increments exactly once; overflow returns a stable conflict and never wraps.

All first successful commits, including content-identical PUT/rollback and unchanged refreshed files, return HTTP 200 with a small immutable body:

```json
{
  "committed_revision": "43",
  "committed_at": "2026-09-20T10:00:00Z",
  "idempotency_expires_at": "2026-09-21T10:00:00Z"
}
```

Times are server UTC RFC3339Nano values; they do not substitute for revision ordering. The same committed replay returns the same body/status with `Idempotency-Replayed: true`. It does not promise DP activation. There is no rollout URL until 4B actually implements one.

A read captures the head through a linearizable store read, then pins and loads that immutable artifact. If the captured default head retires before pin acquisition, retry head capture within the read deadline; never mix artifacts. Explicit historical reads return 410 if retirement wins. Return `revision` with resource/items. Explicit historical reads never silently upgrade to head. At revision zero, lists are empty and resource lookups return not found.

Resource lists sort by ID byte order. Revision lists sort newest first and freeze the first page's head as a ceiling; they return only retained revision metadata, so retention can remove entries between pages. They do not promise an immutable historical catalog. Resource pagination, in contrast, stays on one fixed artifact.

A bounded opaque cursor encodes version, kind, revision/ceiling, last key, and page limit as base64url JSON. It contains no secret, grants no authority, and is fully revalidated on each call; altered cursors are treated as explicit read positions and cannot bypass access control. Conflicting query/cursor values fail. Cursors do not pin history between requests. A removed resource-list revision returns 410; the client restarts pagination.

GET /v1/revisions returns revision, commit time, resource counts, and optional restored-from revision. It does not return material checksums, wrapped keys, chunks, or raw artifacts.

## 7. Mutation identity, execution and cancellation

Idempotency keys are 1–128 printable ASCII characters excluding spaces/control characters. Store their digest, not the raw key, under the authenticated principal's digest.

Request identity is the normalized logical action, expected revision, and typed body. CRUD normalizes to the corresponding single-operation transaction. Sort transaction operations by kind/ID; preserve ordered nested arrays and optional presence. Object key order and JSON whitespace do not affect identity. Numbers are normalized by their schema type without float64 conversion of integers. Defaults/presence follow the fixed v1 DTO profile, not mutable runtime state. Rollback identity includes source revision. Identity never includes reread file bytes, so retrying a committed refresh cannot read a different file.

Canonical encoding uses a versioned `cp-json-v1` profile: typed DTO field order, map keys sorted bytewise, UTF-8 strings encoded with the Go JSON string escaping rules, no insignificant whitespace or trailing newline, and canonical decimal integer values. Plugin configs use their registered typed schema. Golden tests fix this profile. A new incompatible profile requires a new schema version.

Processing order:

1. Authenticate/authorize; enforce header/body/concurrency bounds; strictly decode and normalize.
2. Enqueue at most 16 pending mutations. A single worker owns candidate preparation through commit/reconciliation.
3. Read a retained idempotency result before checking expected revision. Same request returns the prior result; different request returns 409.
4. Read current head, compare expected revision, and reserve bounded staging/auxiliary capacity.
5. Apply all operations to declarations, refresh only explicitly put SecretRefs, expand references, and validate the final graph.
6. Encode, sign, encrypt, stage the artifact and mark it complete.
7. Enter commit with an independent bounded context; atomically publish or reconcile the result.
8. Release reservations/pins; schedule abandoned staging for cleanup. Never run filesystem or crypto work under a read/distribution lifetime mutex.

Before commit starts, disconnect, queue timeout, or preparation timeout cancels work and cannot publish. After it starts, client cancellation does not define the commit outcome. If no definite answer is available by the internal deadline, return COMMIT_OUTCOME_UNKNOWN when a connection remains. Do not allow another mutation to overtake unresolved publication until store reconciliation or owner fencing establishes the old attempt's outcome.

Shutdown stops admissions, cancels queued/preparing mutations, and drains the bounded commit/reconciliation phase. A hard stop may leave an uncertain attempt; restart resolves it using durable stage state, head, and idempotency before new work.

Idempotency retention is at least 24 hours from commit by default, with a 100,000-record cap. Capacity cannot evict unexpired entries. Expiry is independent of snapshot retention: a replay can return revision 43 even after its historical artifact is gone. After expiry, the key may be used again, but expected_revision is still checked. Since every successful commit advances head, replaying an old request unchanged cannot silently repeat it at its old base revision.

Commit time is captured immediately before the final publication attempt; it is an administrative timestamp, not an exact etcd commit instant. Latency instrumentation must use separate operation timing. Only committed results are durably idempotent. Invalid or capacity-rejected requests have no committed result; retry revalidates them. Concurrent identical requests join or reach the same serialized lookup; they must not duplicate publication.

## 8. SecretRef refresh and rollback

Open only configured mount roots, using root-relative operations that enforce containment during lookup rather than a separate string-check followed by unrestricted open. Reject absolute paths, backslashes, drive/UNC syntax, empty/dot/dot-dot segments, NUL, and trailing slash. Internal relative symlinks may work only when the rooted open proves containment. Accept only regular files and bound reads on the opened handle. A type check after a potentially blocking FIFO/device open is insufficient: use a platform-specific safe opener where needed and test that special-file rejection itself cannot block the worker.

Go's rooted filesystem operations provide a relevant primitive but do not themselves prohibit device files or mount crossings. File-type checks and trusted read-only mount provisioning remain required. The implementation must verify behavior on Go 1.26.5 and supported operating systems; do not broaden filesystem access or alter the standalone resolver. [Go rooted filesystem API](https://go.dev/src/os/root.go)

Each SecretRef put reads its file once per transaction. A generic unreferenced SecretRef may retain up to 1 MiB; consumption as a chain/private key enforces the existing 256 KiB limits, and a CA bundle enforces 1 MiB. Reuse the captured bytes for every consumer. Distinct SecretRef IDs pointing to the same file are distinct reads; no cross-file atomicity is claimed.

A referenced chain/key pair must match before commit. Operator controls coordinated publication of multiple files; use atomic file replacement or versioned directories. CP cannot infer that two otherwise valid files represent the operator's intended rotation.

Unchanged SecretRefs reuse prior bytes even if files changed or disappeared. Putting a Certificate/TrustBundle does not refresh its SecretRefs. Every successful candidate revalidates consumed material at current time. Unreferenced SecretRefs are size/path-checked byte resources; type-specific validation occurs when consumed.

Rollback pins the retained source artifact, restores its complete declarations and captured material, and revalidates with current policy/time. It reads no secret files, including for SecretRefs whose logical mounts are no longer configured; the stored declarations remain historical references. A later explicit put requires the mount to exist again. Valid rollback gets a new revision, new artifact key, current signing key, audit, and idempotency result. A pruned source returns 410; invalid/expired source material returns 422. No reconstruction from current mount contents is permitted.

## 9. Artifact and cryptographic profile

Use separate immutable values for declarations and effective DP content:

- Declaration section: complete resource wrappers plus captured bytes for SecretRefs not otherwise represented, sufficient to reproduce read/refresh/rollback behavior.
- Snapshot section: normalized effective resources and deduplicated material table with stable references; includes bytes needed by DP without local file paths.
- Header: artifact format version, configuration-domain ID, logical revision, commit-intent timestamp, signing key ID, and hashes/lengths of both sections. Every material byte is inside one hashed section.

Deduplicate DP-required material in the snapshot section's material table. Declarations reference that table for consumed SecretRefs; otherwise-unreferenced captured bytes live only in the declaration section. The 16 MiB snapshot limit includes all serialized material needed by DP, including encoding expansion, and effective resources. The declaration-only portion, including otherwise unreferenced captured bytes, has a separate 16 MiB limit. The complete signed plaintext bundle is at most 33 MiB and stored ciphertext at most 34 MiB. Reject before publication when any limit is exceeded; do not compress around the bounds.

The portable envelope uses `gateway.snapshot/v1alpha1` and `cp-json-v1`. Sign the canonical header (which commits to section hashes/lengths and revision/domain) using Ed25519. The CP artifact reader rechecks both content hashes before accepting the full bundle. The internal 4B export contains only the signed header and snapshot section: DP checks the header signature and snapshot hash without receiving declaration-only material or CP file paths. The signed declaration hash may remain opaque to DP. Payloads may be identical at different revisions; revision is part of signed identity. 4B must preserve this identity rather than re-encode signed bytes.

Encrypt the full signed bundle once using a fresh 256-bit data key and AES-256-GCM with a random nonce. Split ciphertext after encryption, not plaintext, into 256 KiB chunks. The manifest contains artifact ID, ordered chunk lengths/SHA-256 hashes, total ciphertext length/hash, format/domain/revision, storage key ID, and wrapped data key. It has no plaintext secret fields.

Wrap each data key with the active CP AES-256-GCM storage key, using a separate nonce and domain-separated authenticated metadata binding artifact ID, domain, revision, format and key ID. Unwrapping and content decryption use exactly those bindings. Do not implement cryptographic primitives manually. Go's AEAD API documents nonce handling and per-key random-nonce usage limits. [Go cipher API](https://pkg.go.dev/crypto/cipher#NewGCMWithRandomNonce)

Wrapping attempts, including abandoned artifacts, reserve a persistent use count before encryption. Refuse new wrapping at 2^31 uses per storage key, below the API's 2^32 limit. A key belongs to one domain/counter lineage. Backup restoration that could roll back usage accounting requires a fresh active storage key before mutations; retain old keys for reads. No key bytes enter etcd, logs, or metrics.

Bootstrap rotation selects new signing/storage key IDs on restart. Retain old public/decryption keys while retained artifacts or backups need them. Key removal and re-encryption are not automated. Audit and declared identities remain inspectable metadata; storage encryption is not a claim to hide every access pattern or resource count.

## 10. Etcd layout, publication and recovery

Use an operator-selected exclusive prefix plus domain ID. Logical revisions are encoded as zero-padded 20-digit numbers in keys for lexicographic ordering; Admin keeps unpadded decimal strings.

| Relative key | Purpose |
| --- | --- |
| domain | Initialized format/domain identity; distinguishes a new store from a missing head |
| owner | Lease-backed process token; fence every store mutation |
| head | Current logical revision and committed manifest locator |
| revisions/{revision} | Immutable committed manifest and bounded public metadata |
| objects/{random-id}/state | Uploading, complete, published, or deleting state |
| objects/{random-id}/chunks/{index} | Immutable ciphertext chunks |
| idempotency/{principal-digest}/{key-digest} | Request digest, immutable response, expiry |
| audit/{revision} | Success audit independent of artifact retention |
| accounting | Bounded live-byte/count reservations and reconciliation state |
| key-uses/{key-id} | Monotonic wrapping reservations |

An unused prefix is initialized with domain metadata, head at revision zero, and empty accounting in one owner-checked transaction. Missing head under an already initialized domain is corruption, not a fresh start. Refuse to initialize a prefix containing unexplained existing data.

Artifact IDs are random attempt IDs, never the target revision alone; failed attempts for the same next revision cannot overwrite each other. Chunks are not attached to expiring leases: expiration must never remove a published artifact.

Stages transition `uploading -> complete -> published`, or `uploading/complete -> deleting`. Chunk writes compare owner, stage state/version and chunk absence. Marking complete records the fixed manifest digest and occurs only after all writes are acknowledged. No chunk may change afterward.

Final publication transaction compares owner, captured head version/value, the complete stage version/digest, absent retained idempotency result, and current capacity reservations. It atomically writes head, revision manifest, published stage state, idempotency response, success audit, and accounting transfer. The declarations live inside the immutable referenced artifact, so 1,000 Admin operations do not become 1,000 etcd writes in this transaction.

Chunk PUTs and final publication are individually bounded below the etcd request/operation limits; cap the encoded final transaction at 128 KiB and at 32 total comparisons/operations in the implementation contract. Manifest must fit 16 KiB. A chunk is 256 KiB and the artifact bound allows at most 136 chunks. These values avoid depending on raised default limits: etcd v3.6 documents a 1.5 MiB request maximum and 128 transaction operations by default. [etcd limits](https://etcd.io/docs/v3.6/dev-guide/limit/), [etcd configuration](https://etcd.io/docs/v3.6/op-guide/configuration/)

Pin an etcd 3.6 patch release and matching supported client in the implementation plan after checking its compatibility with the repository's Go/gRPC dependencies. This document does not upgrade dependencies or claim a selected patch was tested.

After an ambiguous final transaction, linearly read the idempotency record and stage/head state. A matching committed result is success. To establish non-commit, atomically change the still-complete stage to deleting while checking the current owner and idempotency absence. If publication won, that comparison fails; if abandonment won, a delayed final commit cannot pass its complete-stage comparison. A single read of an absent idempotency record is insufficient to rule out a delayed commit. Etcd documents transaction atomicity/consistency and the need for clients to handle failures. [etcd API guarantees](https://etcd.io/docs/v3.6/learning/api_guarantees/)

Restart obtains exclusive ownership, checks head/manifest/domain/key metadata, reconciles reservations and incomplete stages, then becomes mutation-ready. It never promotes an orphan stage to a new revision automatically. GC first claims a stage as deleting with owner/state comparisons; all subsequent bounded delete batches are fenced and resumable. Final publication cannot race past that claim.

Historical GC atomically claims retirement and removes discoverability before deleting chunks, comparing the current head so the current artifact cannot be retired. In-process artifact acquisition and GC claims share a short lifetime lock; readers pin a live revision before I/O and fail if already retiring. Do not hold that lock during network/decryption/file work. A historical pin prevents deletion until release; operation deadlines stop further I/O before release. On CP restart, old process pins are not reused. A deleting artifact cannot receive a new pin.

A backup-restore that makes head older than a live DP is not an ordinary rollback. Do not renumber silently or reuse conflicting revisions. Keep publication stopped until an operator performs a separately validated recovery that preserves revision/domain identity; distributed disaster recovery is a 4B/4C runbook dependency, not a reason to weaken monotonicity.

## 11. Retention, audit and CP health

History is at most 100 non-current revisions and 512 MiB of actual serialized manifests/chunks, with oldest-first retirement. Current artifact is always retained in its own one-artifact budget. Expired cursors do not pin it. Temporarily pinned old artifacts can move into the separate in-flight budget; that budget is not an unlimited retention exception.

Before commit, reserve enough capacity for the candidate and the former head. Cleanup may retire unpinned old history first. If capacity cannot be secured, reject the mutation before publication. No live artifact or unexpired idempotency/audit record is evicted to admit a write.

Stage accounting includes incomplete/deleting bytes until etcd deletion is confirmed. Scan/reconcile bounded pages after crash; do not admit based on an optimistic zero counter. Old unowned stages are eligible immediately after ownership recovery; owned stages have finite preparation/commit lifetimes. Etcd MVCC history, backend free pages, WALs and backup storage are outside logical byte counters; quota monitoring/compaction/defragmentation remain operational responsibilities. An etcd quota error is a mutation failure, not permission to delete head.

Success audit is committed with revision and includes principal, logical action, request ID, expected/committed revision, rollback source if any, commit time, operation count and per-kind counts. Keep it at most 4 KiB: do not embed 1,000 resource bodies or raw keys. Audit retention is 24 hours minimum by default and independent of artifact history. At capacity, refuse new mutations rather than drop a required success record. Log export may provide longer retention.

The server generates a bounded request ID; raw untrusted headers are not copied into audit identity. Failed-attempt log delivery uses a nonblocking queue of at most 1,000 events, each at most 4 KiB, dropping new events with a counter when full. Rejected attempts produce bounded structured operational log events with available identity, request ID and stable code. No request bodies, PEM, private keys, secret-content hashes, or raw underlying errors. A committed replay may log a replay event but creates no second success audit. If log output also fails, count dropped events; 4A does not promise durable records of every rejected attempt during combined outages.

A fresh, reachable empty store is CP-ready at revision zero for the first complete transaction. With committed state, CP verifies header, chunks, decryption, signature and schema before exposing it. Missing keys/corruption are not empty state and do not trigger file reconstruction or automatic historical fallback.

Startup checks integrity, decodability and structural schema, not whether old certificate dates are still current. Expired but intact current material must not prevent an Operator from committing a valid repair; candidate validation uses current time. Corruption or missing keys still requires storage/key repair before normal mutation.

On etcd unavailability, mutations and consistent Admin resource/revision reads return 503. Telemetry/health remains available. The internal distribution seam may retain an already verified cached artifact, marked freshness-unconfirmed; 4B decides how to report that status. On recovery, reconcile head before mutations resume. Historical missing keys affect those reads/rollback without falsely claiming historical data is usable.

CP readiness requires confirmed ownership, reconciled storage and usable current artifact (or a verified empty store). Capacity exhaustion reports degraded mutation availability and 503 for affected writes while valid reads remain possible. CP readiness never directly controls DP traffic readiness.

## 12. Limits and deadlines

The approved baseline defaults are retained exactly:

| Setting | Default | Meaning |
| --- | --- | --- |
| mutation_body_bytes | 1 MiB | Uncompressed HTTP body before decode |
| transaction_operations | 1,000 | Nonempty operations list |
| pending_mutations | 16 | Waiting after bounded decode; excludes one active worker |
| snapshot_bytes | 16 MiB | Serialized resolved DP section before signing/encryption |
| history_revisions | 100 | Non-current retained revision count |
| history_bytes | 512 MiB | Non-current manifests plus ciphertext |
| idempotency_retention | 24 hours | Minimum committed replay window |
| idempotency_records | 100,000 | Live records; do not evict early |

Additional review defaults make all exemptions bounded:

| Setting | Default / supported boundary |
| --- | --- |
| declaration-only bytes / signed bundle / ciphertext | 16 / 33 / 34 MiB; inclusive bounds |
| chunk / manifest / final transaction | 256 KiB / 16 KiB / 128 KiB; fixed format bounds |
| staging bytes / in-flight historical bytes | 128 MiB / 128 MiB |
| auxiliary records bytes | 512 MiB including idempotency, audit, indexes and accounting |
| audit retention / audit records | 24 hours / 100,000 |
| one idempotency response record / audit event | 1 KiB / 4 KiB |
| active HTTP body decoders / readers | 4 / 16; excess returns 429 |
| Admin header / cursor | 16 KiB / 2 KiB |
| page limit | Default 100, maximum 1,000 |
| queue wait / body read | 5 seconds / 5 seconds |
| candidate preparation | 30 seconds after dequeue |
| final commit and reconciliation | 10 seconds total; unresolved outcome blocks further mutation |
| read request / historical artifact pin | 10 seconds / maximum 60 seconds |
| CP shutdown drain | 20 seconds |
| etcd dial / ordinary request | 5 seconds / 5 seconds, bounded by operation deadline |
| ownership lease / keepalive interval | 30 seconds / 10 seconds |
| cleanup scan page / delete batch | 100 records / at most 32 operations |
| SecretRef path / mount name | 1,024 bytes / managed ID syntax |

All decoder/queue slots are acquired before allocating a maximum body. Raw bodies are discarded after bounded decoding. Reject excessive JSON nesting above 32 levels. The 16 queued objects remain bounded by body and schema limits; this is not a claim that process RSS equals 16 MiB. Candidate/snapshot buffers need explicit ownership and measured memory tests.

Baseline limit settings may be reduced within positive ranges, never zero-as-unlimited; retention reductions apply only to future records. Supporting increases beyond this initial envelope requires a reviewed envelope change and measurements, not merely an unchecked bootstrap number. Internal consistency checks reject settings unable to fit one maximum candidate or permitted page/record. CP filesystem material limits also retain stricter existing per-use bounds. Standalone limits are unchanged.

A deadline does not justify releasing pins while a worker still uses the artifact or abandoning an uncertain commit without reconciliation. Cancellation checkpoints and cleanup ownership must be testable. No unbounded retry goroutines, unpaginated whole-store scans, or counters that forget already retained objects after restart.

## 13. Error mapping

Use `{"error":{"code":"...","message":"...","request_id":"..."}}` with optional bounded `kind`, `id`, `field`, and `current_revision`. Return one deterministic validation error ordered by kind/ID/field; do not echo unbounded input.

| HTTP | Codes / meaning |
| --- | --- |
| 400 | INVALID_JSON, INVALID_REVISION, INVALID_QUERY, INVALID_CURSOR, IDEMPOTENCY_KEY_REQUIRED; malformed shape or syntax |
| 403 | ADMIN_FORBIDDEN for a verified principal without permission; invalid client certificates fail TLS before HTTP |
| 404 | RESOURCE_NOT_FOUND or REVISION_NOT_FOUND for a requested future revision |
| 409 | REVISION_CONFLICT, IDEMPOTENCY_KEY_REUSED, REVISION_EXHAUSTED |
| 410 | REVISION_GONE for a previously committed/pruned historical revision |
| 413 | REQUEST_TOO_LARGE, TRANSACTION_TOO_LARGE, SNAPSHOT_TOO_LARGE |
| 415 | UNSUPPORTED_MEDIA_TYPE or content encoding |
| 422 | Invalid resource graph, missing delete target, invalid TLS/material or SecretRef; stable specific code and field |
| 429 | MUTATION_QUEUE_FULL, QUEUE_WAIT_EXCEEDED, ADMIN_CONCURRENCY_LIMIT; Retry-After: 1 |
| 503 | STORE_UNAVAILABLE, CAPACITY_EXHAUSTED, SNAPSHOT_UNAVAILABLE, CP_NOT_READY |
| 504 | PREPARATION_TIMEOUT, COMMIT_OUTCOME_UNKNOWN |

Missing/corrupt current data returns 503, never historical-gone. Once commit began, generic timeout must not be represented as definite non-commit. Clients retry unknown outcomes with the identical request/key. Post-commit response failures do not erase success audit or committed result.

## 14. Verification and exit criteria

Use package-local tests for pure contracts and process/etcd integration tests for storage/lifecycle. Do not implement cryptographic substitutes in test-only production paths.

Required test matrices:

- Strict JSON, inherited DTO fields/defaults/presence, unknown/duplicate fields, kind/ID constraints, decimal uint64 boundaries and overflow.
- Multi-resource create/delete, final-state references independent of operation order, missing-delete rejection, plugin expansion and unchanged inheritance.
- Explicit SecretRef refresh, unchanged references with files removed, one read per ID, key mismatch, oversized/nonregular files, traversal/symlink races and cancellation cleanup.
- Rollback restores exact historical bytes without file reads; expired material and pruned sources fail; no-op mutation still advances revision.
- Same-base concurrent writes, normalized CRUD/transaction replay, changed-key content, expiry/capacity, lost response, and restart.
- Inject failure after every staging write and at complete/publication boundaries; no partial visibility, audit omission or early key expiration.
- Delayed commit versus abandonment/GC CAS, owner loss and replacement, resumed cleanup, no chunk writes after deleting claim, accounting reconstruction.
- Wrong domain/revision/key/nonce/tag/chunk order/hash; key rotation read compatibility; wrapping-use reservation on failed attempts.
- Pinned reads versus retention, resource cursor stability, explicit 410, revision catalog ceiling, separate history/idempotency/audit budgets.
- Empty CP bootstrap, corrupt current artifact, missing keys, etcd outage/recovery, queue saturation, cancellation before/after commit, shutdown drain.
- Reader/Operator separation, no DP implicit role, failed log output, redaction and bounded metric cardinality.
- Near-limit body, operation count, snapshot size, auxiliary capacity and repeated lifecycle tests to demonstrate bounded memory/storage.

A real etcd integration fixture must show a 1,000-operation Admin transaction that commits using the small fixed publication transaction under ordinary server request/operation limits. Include encrypted artifacts spanning many chunks, restart retrieval, deleted mount files, concurrent reads, and cleanup during failure recovery.

Build/CI checks when implementing: Go 1.26.5, formatting, staticcheck, revive, vet, normal/race tests, all command builds, existing gateway Docker build, and a CP image build using the repository's COMMAND composition convention. Add CP bootstrap examples and a 4A runbook with temporary test-generated keys only; commit no secrets, generated certificates, profiles or benchmark results.

4A completes when Admin-to-durable-artifact behavior and its failure matrix pass, existing standalone regressions remain green, and the bounded internal artifact seam is ready for 4B. The one-second activation gate and three-DP rollout/recovery evidence remain 4B/4C, not 4A claims.

## 15. Review handoff

The written spec is approved and the linked 4A implementation plan defines the task sequence. The plan preserves the Phase 3D1 implementation-and-CI gate, selects exact etcd dependency/image versions for mandatory compatibility testing, assigns meaningful validation to each change, and keeps process wiring thin. Approval of this document does not waive Phase 3D2 acceptance obligations or authorize publication/deployment.

## 16. Repository references

- [Approved umbrella](2026-09-20-phase-4-control-plane-design.md)
- [Phase roadmap](2026-07-21-go-native-api-gateway-phase-roadmap-design.md)
- [Original Go-native architecture](2026-07-21-go-native-api-gateway-design.md)
- [Accepted architecture reference](../../architecture/apache-api-six-architecture-design.md)
- [Phase 3C2 TLS design](2026-09-13-phase-3c2-downstream-sni-certificate-rotation-design.md)
- [Phase 3C2 operations](../../operations/phase-3c2-runbook.md)
- [TLS file limits](../../../internal/tlsmaterial/load.go)
- [Existing runtime validation](../../../internal/runtime/validate.go)
- [Existing snapshot builder](../../../internal/runtime/builder.go)

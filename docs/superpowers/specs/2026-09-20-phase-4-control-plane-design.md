# Phase 4 Control Plane End-to-End Design

**Date:** 2026-09-20

**Status:** Written umbrella specification approved by the user. No Phase 4 implementation or acceptance evidence is claimed.

**Scope:** Umbrella architecture for Phase 4A, 4B, and 4C. Each slice requires a detailed specification and implementation plan.

## 1. Objective and entry gate

Prove the complete flow:

```text
Admin mutation -> final-state validation -> resolve and seal snapshot
  -> conditional etcd commit -> full snapshot over gRPC/mTLS
  -> DP validate/build -> atomic activation and ACK
  -> encrypted local persistence and status update
```

Design may proceed now. Phase 4 implementation starts only after Phase 3D is complete. Phase 3D owns bounded access logging, integrated resilience acceptance, and canonical integrated APISIX comparison. Existing Phase 3 evidence requirements and deferred Phase 2 Task 16 remain in force.

Initial integration uses one active CP, one etcd cluster, three real DP processes, and deterministic upstreams. Every DP receives the same global configuration. There are no configuration groups or fleet-wide atomic activation.

## 2. Current foundation

The repository implements standalone resources through `gateway/v1alpha7`, snapshot compilation, and internal `Gateway.Apply`. `runtime.Manager.Apply` accepts strictly newer revisions, builds before publication, atomically swaps the active snapshot, and preserves the last-known-good state on rejection. Snapshot and transport leases protect in-flight traffic.

Existing tests include `TestManagerActivationKeepsLastKnownGood`, `TestManagerConcurrentApplyActivatesHighestRevision`, and `TestApplyPublishesNewRouteAndKeepsLastGoodSnapshot`. These describe the internal seam, not implemented remote configuration support.

CP, SecretRef resolution, remote distribution, and durable DP recovery are new work. Managed startup without a snapshot needs lifecycle changes: current gateway startup expects valid initial resources. Duplicate delivery must be handled before Apply, which rejects an already-active revision.

Preserve routing precedence, plugin inheritance, retry safety, TLS validation, streaming, trailers, cancellation, connection continuity, and bounded retirement. DP must not import etcd; CP must not enter the request hot path.

## 3. Delivery slices

| Slice | Responsibility | Completion boundary |
| --- | --- | --- |
| 4A: configuration management | CP, Admin resources, validation, transactions, SecretRefs, sealed storage, authentication/RBAC, audit | A committed revision can be retrieved after CP restart with identical resolved content |
| 4B: distribution and activation | gRPC/mTLS, compatibility, full snapshots, reconnect, ACK/NACK, rollout status | Three DPs converge without disrupting existing traffic |
| 4C: recovery and acceptance | Local encryption/persistence, managed startup, outages, integration evidence | Offline recovery and declared integration gates pass |

This umbrella defines shared contracts. Concrete API/wire schemas, storage layouts, numeric limits, and fixtures belong to the slice specifications. There must not be one implementation plan for the entire umbrella.

## 4. Component boundaries

| Component | Responsibility |
| --- | --- |
| `cmd/gateway-cp` | Thin bootstrap, dependency wiring, servers, shutdown |
| Admin API | Strict decoding, mTLS identity, role checks, resource and transaction responses |
| Transaction service | Apply mutations to one coherent base revision, validate final state, resolve material, request conditional commit |
| Shared validation | Deterministic CP/DP resource rules independent of listeners and live transports |
| Snapshot compiler/sealing | Produce serializable resolved content, sign it, encrypt storage; do not build DP connection pools |
| Etcd store | Durable publication, revision comparison, artifact retrieval, idempotency records |
| Distribution service | Send only committed artifacts; track authenticated DP sessions |
| DP configuration agent | Verify/decode snapshots, reconstruct canonical resources, call Apply, report activation and persistence |
| Local snapshot store | Bounded authenticated encryption, file replacement, recovery verification |
| Audit/observability | Redacted mutation outcomes, aggregate metrics, bounded per-DP status |

These are responsibility boundaries, not mandatory one-package-per-row assignments. Detailed designs preserve existing package ownership.

## 5. Bootstrap and resource contract

Bootstrap owns listeners, CP/etcd endpoints, control-channel identities/trust, signing/encryption keys, secret mount roots, and Admin identity-to-role mappings. These settings require restart in Phase 4. The credentials needed to connect to CP must not depend on a snapshot fetched through that same connection.

Dynamic resources are:

| Resource | Contract |
| --- | --- |
| Route, Service, Upstream | Preserve existing routing, resilience, TLS, and WebSocket behavior |
| PluginConfig | Reusable plugin configuration referenced by Route or Service |
| Certificate | References SecretRefs for chain and private key |
| SecretRef | Identifies files beneath configured CP secret roots; contains no uploaded secret bytes |
| TrustBundle | References SecretRef material for upstream verification |
| DownstreamTLS | Singleton defining default certificate and exact/wildcard SNI bindings for the existing HTTPS listener |

Each Route or Service selects either inline plugins or one PluginConfig reference. CP expands references in the snapshot. Existing inheritance, override, and disable behavior remains. An update affects all references in one revision; deleting a referenced resource fails.

TrustBundle and DownstreamTLS extend the original roadmap to cover existing DP capabilities. No dynamic listener, automatic renewal, secret upload, external secret manager, or file watching is added. Standalone version compatibility remains; managed wire schema versions are explicitly separate.

## 6. Transactions and Admin API

### 6.1. Revision and operations

One monotonically increasing logical configuration revision covers the complete resource graph. It is distinct from etcd's internal storage revision. Every mutation supplies `expected_revision`.

Multi-resource transactions are the foundation; CRUD is a one-resource transaction through the same service. Operations are `put` (create or replace the complete resource) and `delete`. Each resource may occur once per transaction. Validation examines the final graph, independent of operation ordering. Deleting a missing resource or leaving a dangling reference fails.

CP checks schema, references, routing/plugin rules, and resolved TLS material before publication. DP validates again and builds its runtime. CP success does not promise that every DP can activate.

Concurrent edits may conflict even on unrelated resources. A revision mismatch returns HTTP `409`; CP does not silently rebase. Rollback creates a newer revision restoring prior desired content, subject to current validation. It never activates a lower revision.

### 6.2. Commit and idempotency

Every mutation requires `Idempotency-Key`, scoped to authenticated identity and bound to normalized request content including the expected revision. Within a published retention window, replay of a committed request returns the original result; reuse with different content fails. Replay still requires current authorization.

The committed idempotency result and revision publication are atomic. If CP crashes or the response is lost after commit, retry must not commit again. An uncertain etcd outcome must be reconciled against durable state rather than treated as proof of failure.

Success returns `committed_revision`. Activation is observed through rollout status. Errors have stable codes and bounded resource/field locations, without secret bytes.

The 4A specification owns concrete endpoints/schema, remaining HTTP mappings, request normalization, key retention/capacity, read consistency/pagination, and no-op semantics.

### 6.3. Authentication and audit

Admin API uses mTLS and bootstrap identity-to-role mappings:

- Reader reads redacted resources and rollout status.
- Operator includes Reader permissions and may mutate or restore configuration.

DP identity does not grant Admin access. Separate listener trust/identity policies and authorization checks enforce this distinction.

Audit mutation attempts/outcomes with identity, operation, result, and revision when available. Do not log raw bodies, PEM, private keys, or unbounded underlying errors. Durable audit ordering, bounded buffering/retention, and audit failure policy are 4A contracts that must be settled before implementation.

## 7. Secret resolution and etcd publication

SecretRefs resolve only beneath configured CP mount roots, with bounded reads and protection against traversal/symlink escape. Admin API does not accept or return material bytes. Resolution and validation occur before commit; changing a mounted file alone does not activate anything.

Each committed revision fixes both the declarative resource state and exact resolved snapshot content. CP restart retrieves that artifact; it must not reread files to reconstruct an existing revision. The 4A contract must specify which references are re-resolved on a new transaction, coherent material reads, and rollback material semantics. This prevents accidental secret changes during unrelated updates.

CP signs snapshots and encrypts their persisted representation in etcd with a dedicated mounted storage key. That key is separate from signing and mTLS keys. CP recovery requires etcd data and the corresponding keys.

Publication exposes the revision, resource state, complete artifact, and committed idempotency result atomically. This is a logical visibility contract, not permission to put an arbitrarily large snapshot in one etcd value. 4A must choose and bound the physical representation. If immutable artifacts are staged before publication, readers cannot observe partial candidates; orphan cleanup must be bounded and crash-safe.

CP distributes only committed artifacts. Failed resolve, validate, seal, capacity checks, or revision comparisons cannot replace the committed head. Retention must protect artifacts needed by current publication and in-flight transfers.

## 8. Distribution and activation

DP initiates gRPC/mTLS. Handshake reports identity, supported protocol/schema versions, `active_revision`, and `persisted_revision`. CP authenticates identity and checks compatibility. Claimed identity must match the authenticated principal.

The signed envelope binds revision, payload identity/checksum, schema, and configuration-domain identity. A DP must not accept another domain's snapshot just because its signature verifies. Cryptographic format and key identifiers belong to the 4B wire contract.

Processing is sequential: bound input, verify envelope, decode/validate/build, atomically activate, ACK, persist, then report persistence.

| Condition | Behavior |
| --- | --- |
| Newer valid revision | Build off-path and atomically activate |
| Verification, compatibility, validation, or build failure | Reject/NACK with a bounded reason; keep active state |
| Same active revision and identical verified content | Re-ACK without calling Apply |
| Same revision and different content | Integrity rejection |
| Lower revision | Do not activate; report current state |
| Disconnect | Keep serving; bounded reconnect backoff |
| No valid snapshot | Remain not ready until delivery or valid recovery |

ACK confirms activation and identifies revision/content. Persistence success is reported separately. A local write failure does not retroactively NACK or roll back active traffic. Reconnect reconciles state even when an ACK was lost.

Per DP, allow one snapshot awaiting ACK/NACK with a configured deadline. Do not queue all subsequent snapshots. When a transfer completes, read the latest committed revision and send it if needed. Reconnect also targets the latest revision; intermediate revisions need not activate.

Slow or rejecting DPs do not block others. NACK does not roll back global configuration. Rejected revisions must not be resent indefinitely in one session; retry work is bounded.

4B owns exact message/assembly limits, session lifecycle, duplicate identity handling, heartbeat/expiry, deadlines, and retry rules. Basic latest-revision skipping is included in Phase 4. Delta delivery, high-rate coalescing scheduling, 1,000-DP scale, rolling compatibility guarantees, and multi-CP failover remain Phase 5.

## 9. Observational rollout status

Track desired revision, per-DP active/persisted revisions, last contact, and latest bounded NACK/persistence error. Distinguish synchronizing, active at desired revision, rejected, incompatible, and disconnected. Persistence health is independent of activation.

Responses include observation time, aggregate counts, and bounded DP records. There is no required fleet membership list, quorum, automatic rollback, or assertion that all intended nodes completed. For example, three connected DPs may be active while a previously known DP is offline.

Known-DP records have capacity and retention limits. Eviction or CP restart must not imply missing nodes succeeded. Arbitrary identities, resource IDs, revisions, checksums, and file paths are not metric labels. Detailed DP identity belongs in the access-controlled status API.

## 10. Managed startup and local recovery

Bootstrap explicitly selects:

- `standalone`: existing file-driven resources.
- `managed`: CP resources and verified local recovery, without silent fallback to bootstrap resource configuration.

A managed DP with valid local data builds/activates before waiting for CP, then reconnects to synchronize. Without usable local data, the process exposes health/status and retries CP with traffic readiness false. Managed listener startup and shutdown while no runtime exists require explicit lifecycle handling.

After activation, CP/etcd loss alone does not make traffic readiness false. Report configuration connectivity degradation separately. Missing usable state or runtime invariant failure still prevents readiness.

Each DP verifies CP signatures using trusted bootstrap public keys and encrypts its local copy with a node-specific externally mounted key stable across restarts. Keys are separate from mTLS identities.

Local writes use authenticated encryption, bounded I/O, and atomic replacement. Failure preserves the previous good file. Persistence is ordered so an older write cannot replace a newer durable revision. `persisted_revision` advances only after the promised durability step. Retry state is bounded.

Recovery decrypts, verifies signature/domain, checks compatibility, validates material at current time, and builds before serving. Corrupt, unverifiable, incompatible, or undecryptable data is refused while DP waits for CP. A valid signature does not waive certificate validity checks.

Restart before persistence succeeds may recover an older local revision when CP is unavailable; this is an accepted trade-off. Local recovery does not claim offline protection against replacement by an older correctly signed snapshot. The 4C specification defines key lifecycle, filesystem durability, retry ordering, and operational recovery procedures.

## 11. Acceptance and evidence

Implementation checks follow repository guidance: formatting, both documentation analyzers, vet, normal/race tests, command builds, and gateway Docker build, plus checks for new CP build inputs. No such results are claimed by this documentation change.

One CP and three real DPs must demonstrate:

| Area | Required evidence |
| --- | --- |
| Transaction | Multi-resource updates serve no intermediate resource graph |
| Concurrency | Only one transaction against a shared base revision commits |
| Idempotency | Lost responses, retries, and CP restart do not duplicate retained committed requests |
| Storage | Changed secret files and CP restart do not change committed content |
| Publication | Crash/capacity failures do not expose partial artifacts; cleanup is bounded |
| Activation | New requests use coherent snapshots; existing leases, pools, streams, and tunnels remain correct |
| Rejection | Invalid, incompatible, and tampered snapshots leave active traffic unchanged |
| Isolation | A slow/NACKing/disconnected DP does not block the others |
| Reconnect | Lost ACK and duplicate delivery reconcile; revision skipping preserves correctness |
| Outage | CP stop or etcd unavailability does not interrupt active traffic |
| Recovery | Offline restart uses valid local data; corrupt data or missing keys are not trusted |
| Persistence | Disk failure preserves the prior file; out-of-order writes cannot regress durable state |
| Access control | Reader/Operator/DP separation and secret redaction hold |
| Compatibility | Explicit supported/unsupported protocol and schema tests pass |

Connected healthy DP activation p99 must be at most one second at the declared integration scale. Measure commit to actual activation, including distribution and build. If ACK observation supplies a conservative bound, report it as such; do not subtract unsynchronized wall clocks.

Before measurement, 4C must freeze dataset counts and serialized size, update rate, warm-up, sample count, traffic load, host resources, toolchain, topology, and timing method. Report per-DP and aggregate distributions, failures, and skipped revisions separately. The baseline must include enough activations on each DP; burst tests cannot discard slow samples to pass it. This is a three-DP integration claim, not production certification or the Phase 5 scale claim.

## 12. Non-goals and review handoff

The [Phase 4A configuration management specification](2026-09-20-phase-4a-configuration-management-design.md) now details Admin contracts, explicit material refresh and rollback, bounded mutation processing, encrypted artifact publication, retention, audit, and recovery. Its written review is pending; 4B and 4C retain their separate design boundaries.

Excluded: multi-CP HA/failover, fleet targeting/quorum, delta delivery, 1,000-DP acceptance, automatic rollback, Admin OIDC, external secret managers/KMS, dynamic listeners, automatic certificate renewal, and new traffic authentication plugins.

The umbrella is intentionally decomposed. After written review, refine 4A's concrete API, material, publication, audit, retention, and size contracts first. Then refine 4B's signed wire/session contract and 4C's durability and frozen acceptance workload. Each slice receives its own reviewed specification before its implementation plan.

Phase 3D completion remains the implementation entry gate for every slice.

## 13. References

- [Accepted architecture](../../architecture/apache-api-six-architecture-design.md)
- [Go-native gateway design](2026-07-21-go-native-api-gateway-design.md)
- [Phase roadmap](2026-07-21-go-native-api-gateway-phase-roadmap-design.md)
- [Phase 2 runtime design](2026-07-23-phase-2-runtime-snapshot-router-kernel-design.md)
- [Phase 3C2 design](2026-09-13-phase-3c2-downstream-sni-certificate-rotation-design.md)
- [Phase 3C2 runbook](../../operations/phase-3c2-runbook.md)
- [Phase 3C2 evidence](../../benchmarks/phase-3c2-current-status.md)
- [Deferred Phase 2 evidence](../../benchmarks/phase-2-current-status.md#deferred-task-16)

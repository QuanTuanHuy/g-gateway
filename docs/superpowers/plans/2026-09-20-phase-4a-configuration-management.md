# Phase 4A Configuration Management Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an authenticated control plane that commits globally revisioned configuration and exact resolved material to etcd, with atomic audit/idempotency and a bounded internal artifact reader.

**Architecture:** Keep `cmd/gateway-cp` as composition only. A single mutation worker composes pure resource validation, explicit file resolution, signed/encrypted immutable artifacts, and owner-fenced etcd publication. Admin reads pin immutable revisions; DP distribution and local persistence remain separate Phase 4B/4C work.

**Tech Stack:** Go 1.26.5, standard library HTTP/TLS/crypto/testing, existing YAML and Prometheus libraries, etcd client/server v3.6.5.

**Spec:** [Approved Phase 4A specification](../specs/2026-09-20-phase-4a-configuration-management-design.md), read together with the [approved umbrella](../specs/2026-09-20-phase-4-control-plane-design.md).

**Status:** Written after user approval of the complete 4A spec on 2026-09-20. Implementation has not started. All execution checkboxes are intentionally unchecked.

## Global Constraints

- Phase 3D1 bounded access logging must be implementation-complete with its required CI evidence before Phase 4 executable-code work begins. Record the accepted 3D1 evidence and baseline commit before Task 1. Phase 3D2 remains a separate acceptance obligation and is not waived by this gate.
- Preserve all standalone YAML versions through v1alpha7, validation/default/presence behavior, snapshot lease ownership and last-known-good activation.
- Use Go 1.26.5. Do not upgrade existing direct dependencies or introduce an etcd server dependency into the production module.
- One configuration domain, one active CP, one global uint64 revision encoded as a canonical decimal string in Admin JSON; initial revision "0", first commit "1".
- Managed kinds: Route, Service, Upstream, PluginConfig, Certificate, SecretRef, TrustBundle, DownstreamTLS. Managed IDs: `[A-Za-z0-9][A-Za-z0-9._-]*`, 1–128 ASCII characters; DownstreamTLS ID `default`.
- Bootstrap `gateway-cp/v1alpha1`; portable artifact `gateway.snapshot/v1alpha1`; canonical encoding `cp-json-v1`.
- Admin URI SAN identity is exactly one verified URI, at most 256 UTF-8 bytes; explicit Reader/Operator mapping. Keep Admin private and credentials/mounts read-only.
- Ed25519 signatures; AES-256-GCM fresh per-artifact DEK plus separate KEK wrapping; persistent wrapping reservation capped at 2^31 attempts per key lineage.
- No live transport preparation, dial, health worker, listener binding or DP Apply in CP validation.
- No material upload/download endpoint, gRPC distribution, rollout API, managed DP startup, local DP persistence, automatic key rotation, HA or one-second activation claim in 4A.
- Commit no private keys, certificates, benchmark results or profiles. Test material is generated under temporary directories.

Copy these exact limits into one bootstrap defaults definition and table-driven boundary tests; all tasks inherit them:

| Setting | Default / fixed bound |
| --- | --- |
| mutation_body_bytes / transaction_operations / pending_mutations | 1 MiB / 1,000 / 16 plus one active worker |
| snapshot / declaration-only / signed bundle / ciphertext | 16 / 16 / 33 / 34 MiB |
| chunk / manifest / final transaction | 256 KiB / 16 KiB / 128 KiB |
| final transaction comparisons and operations / maximum chunks | 32 total / 136 |
| history_revisions / history_bytes | 100 non-current / 512 MiB |
| staging / in-flight historical / auxiliary bytes | 128 / 128 / 512 MiB |
| idempotency retention / records / record bytes | 24 hours minimum / 100,000 / 1 KiB |
| audit retention / records / event bytes | 24 hours minimum / 100,000 / 4 KiB |
| failed-event queue | 1,000 events, each at most 4 KiB; nonblocking drop-new |
| active HTTP body decoders / readers | 4 / 16 |
| Admin header / cursor / JSON nesting | 16 KiB / 2 KiB / 32 levels |
| page size | default 100, maximum 1,000 |
| queue wait / body read / preparation | 5 / 5 / 30 seconds |
| final commit plus reconciliation / read / pin lifetime | 10 / 10 / maximum 60 seconds |
| shutdown / etcd dial / ordinary etcd request | 20 / 5 / 5 seconds |
| owner lease / keepalive | 30 / 10 seconds |
| cleanup scan / delete batch | 100 records / at most 32 operations |
| SecretRef path / generic file | 1,024 bytes / 1 MiB |
| certificate chain / private key / CA | 256 KiB / 256 KiB / 1 MiB |

Reductions must remain positive and internally consistent. Retention reductions apply only to future records. Increased limits require reviewed measurements. Current artifact has its own one-artifact allowance; deleting/staged bytes remain charged until deletion is confirmed.

---

## Execution gate and dependency pin

Before changing executable code, read [README](../../../README.md), [repository instructions](../../../AGENTS.md), [roadmap](../specs/2026-07-21-go-native-api-gateway-phase-roadmap-design.md), the approved spec, the [Phase 3D1 design](../specs/2026-09-20-phase-3d1-bounded-access-logging-design.md), and its completed CI evidence. The planning baseline is commit `422434f`; it is not evidence that Phase 3D1 is complete. If the 3D1 gate is not met, stop execution, retain this plan, and report the missing evidence. Reconcile paths against the completed 3D1 baseline without broadening scope.

Pin `go.etcd.io/etcd/client/v3@v3.6.5`, matching `api/v3` and `client/pkg/v3`, and test image `gcr.io/etcd-development/etcd:v3.6.5`. This is a selected reproducible baseline, not a latest-release or tested-compatibility claim. The upstream client module declares Go 1.24 and gRPC v1.71.1; the repository has Go 1.26.5 and gRPC v1.82.1. Metadata therefore permits the selected dependency floors, but compilation, module selection and real-server behavior must pass Task 7 before accepting compatibility. [Upstream client module](https://raw.githubusercontent.com/etcd-io/etcd/v3.6.5/client/v3/go.mod), [official release and image instructions](https://github.com/etcd-io/etcd/releases/tag/v3.6.5).

No modules or images were installed while writing this plan. Keep the existing gRPC pin; inspect `go mod tidy` changes and do not accept unrelated upgrades.

## File structure and responsibility map

All paths in task file lists are repository-relative. Listed new files are planned outputs, not existing files. Add `doc.go` to each new package. Existing reference links here resolve at planning time.

| Files | Responsibility |
| --- | --- |
| `internal/resource/{revision,document,decode,canonical,mutate,expand,convert,validate,errors}.go` | Strict managed DTOs, logical actions, graph transformation and CP conversion |
| `internal/resource/{route,service,upstream,plugin,tls}_dto.go` | Focused typed wire structs copied from the accepted inherited field contract |
| `internal/model/validate.go`; existing config/runtime validators | Shared model-only validation; adapters preserve existing error surfaces |
| `internal/secretref/{resolver,path,open_linux,open_windows,open_other}.go` | Mount ownership and bounded rooted material capture |
| `internal/snapshot/{bundle,canonical,sign,envelope,manifest}.go` | Portable bytes, integrity, storage encryption and export projection |
| `internal/cpstore/{types,keys,owner,initialize,stage,publish,reconcile,retention,reader,accounting,keyuses}.go` | Etcd protocol, durable records, fencing, budgets and artifact lifetime |
| `internal/controlplane/{bootstrap,limits,service,worker,prepare,commit,reads,lifecycle,observability}.go` | Bootstrap and orchestration; no HTTP routing or direct etcd key construction |
| `internal/adminapi/{server,identity,decode,mutations,reads,cursor,errors}.go` | Private mTLS API, bounded admissions, strict requests and declarative output |
| `cmd/gateway-cp/main.go` | Compose dependencies, handle signals, run/shutdown |
| `internal/cptest/{etcd,material,process}.go` | Test-only helpers imported only by tests; real etcd/container and temporary identities |
| `test/integration/phase4a_*_test.go` | Process and failure acceptance; no Phase 4B server |
| `configs/phase4a-cp.yaml`; `docs/operations/phase-4a-runbook.md` | Bootstrap template, invocation, operational limits and recovery |
| `.github/workflows/ci.yml`; `README.md` | Mandatory etcd acceptance job, CP image build, accurate delivered scope |

Place tests beside each new behavior file (`*_test.go`). Do not serialize `model.ResourceSet` directly: it contains parsed TLS objects. Do not export standalone YAML internals merely to reuse a wire struct.

## Shared interfaces and ownership

The declarations below fix names used across tasks; implementations add exported Go documentation. Methods listed as signatures are API contracts, not stub bodies to commit. Use typed internal DTOs during decode and retain only their canonical JSON in `Document.Spec`; callers cannot bypass `DecodeDocument`/candidate validation by constructing a struct.

```go
// internal/resource
type Revision uint64
type Kind string
type Document struct {
    Kind Kind            `json:"kind"`
    ID   string          `json:"id"`
    Spec json.RawMessage `json:"spec"`
}
type Graph []Document
type Material map[string][]byte // SecretRef ID -> owned captured bytes
type Operation struct {
    Op   string          `json:"op"`
    Kind Kind            `json:"kind"`
    ID   string          `json:"id"`
    Spec json.RawMessage `json:"spec,omitempty"`
}
type Action struct {
    ExpectedRevision Revision    `json:"expected_revision"`
    Operations       []Operation `json:"operations,omitempty"`
    SourceRevision   *Revision   `json:"source_revision,omitempty"`
}
type Fault struct { Code, Kind, ID, Field string }
func (f *Fault) Error() string
func ParseRevision(s string) (Revision, error)
func (r Revision) Next() (Revision, error)
func (r Revision) MarshalJSON() ([]byte, error)
func (r *Revision) UnmarshalJSON(b []byte) error
func DecodeDocument(b []byte) (Document, error)
func DecodeAction(b []byte) (Action, error)
func Normalize(a Action) (Action, error)
func Digest(a Action) ([32]byte, error)
func Apply(base Graph, operations []Operation) (Graph, []string, error)
func Expand(g Graph) (Graph, error)
func Convert(g Graph, material Material, now time.Time) (model.ResourceSet, error)
func Validate(g Graph, material Material, now time.Time) error

// internal/secretref
type Resolver struct { /* private mount handles */ }
func Open(mounts map[string]string) (*Resolver, error)
func (r *Resolver) Read(ctx context.Context, mount, path string, limit int64) ([]byte, error)
func (r *Resolver) Close() error

// internal/snapshot
type Bundle struct {
    Domain string
    Revision resource.Revision
    CommittedAt time.Time
    Declarations resource.Graph
    Material resource.Material
    Effective resource.Graph // expanded portable DTOs, not parsed model handles
}
type Signed struct { Header, Signature, Declarations, Snapshot []byte }
type Export struct { Header, Signature, Snapshot []byte }
type Chunk struct { Length int; SHA256 [32]byte }
type Manifest struct {
    ArtifactID, Domain, Format, StorageKeyID string
    Revision resource.Revision
    WrappedDEK []byte
    Chunks []Chunk
    CiphertextLength int
    CiphertextSHA256 [32]byte
}
func Sign(b Bundle, keyID string, key ed25519.PrivateKey) (Signed, error)
func Verify(s Signed, domain string, revision resource.Revision,
    keys map[string]ed25519.PublicKey) (Bundle, error)
func Project(s Signed) Export
func VerifyExport(e Export, domain string, revision resource.Revision,
    keys map[string]ed25519.PublicKey) error
func Seal(s Signed, artifactID, storageKeyID string, key []byte) (Manifest, [][]byte, error)
func Open(m Manifest, chunks [][]byte, key []byte) (Signed, error)
```

`Material`, graphs and returned bytes are owned copies unless held through a read handle. Slice/map mutation by an input caller must never mutate committed state. `Sign` partitions captured material by consumption, deduplicates the consumed table, and excludes logical paths/unconsumed bytes from the DP section. `Project` copies the already signed header and exact snapshot bytes without re-encoding.

```go
// internal/cpstore
type Result struct {
    Revision resource.Revision `json:"committed_revision"`
    CommittedAt time.Time      `json:"committed_at"`
    ExpiresAt time.Time        `json:"idempotency_expires_at"`
}
type Identity struct { PrincipalHash, KeyHash, RequestHash [32]byte }
type Head struct { Revision resource.Revision; ModRevision int64; ArtifactID string }
type Metadata struct {
    Revision resource.Revision
    CommittedAt time.Time
    Counts map[resource.Kind]int
    RestoredFrom *resource.Revision
}
type Audit struct {
    Principal, Action, RequestID string
    ExpectedRevision, Revision resource.Revision
    SourceRevision *resource.Revision
    CommittedAt time.Time
    OperationCount int
    KindCounts map[resource.Kind]int
}
type Reservation struct { ID string } // opaque, owned and reconciled by Store
type Stage struct { ArtifactID string; Version int64; Digest [32]byte }
type Publication struct {
    Head Head
    Stage Stage
    Reservation Reservation
    Identity Identity
    Result Result
    Metadata Metadata
    Audit Audit
}
type Outcome uint8
const (
    Unknown Outcome = iota
    Committed
    Abandoned
)
type ReadHandle struct { /* private pin and verified bytes */ }
func (h *ReadHandle) Bundle() snapshot.Bundle
func (h *ReadHandle) Export() snapshot.Export
func (h *ReadHandle) Close() error
type Store struct { /* private client, fencing token, limits and keyring */ }
func (s *Store) Head(ctx context.Context) (Head, error)
func (s *Store) Lookup(ctx context.Context, id Identity) (Result, bool, error)
func (s *Store) Reserve(ctx context.Context, head Head) (Reservation, error)
func (s *Store) ReserveKeyUse(ctx context.Context, keyID string) error
func (s *Store) Stage(ctx context.Context, r Reservation,
    m snapshot.Manifest, chunks [][]byte) (Stage, error)
func (s *Store) Publish(ctx context.Context, p Publication) (Outcome, Result, error)
func (s *Store) Reconcile(ctx context.Context, p Publication) (Outcome, Result, error)
func (s *Store) Release(ctx context.Context, r Reservation) error
func (s *Store) Acquire(ctx context.Context, revision resource.Revision) (*ReadHandle, error)
func (s *Store) Revisions(ctx context.Context, ceiling resource.Revision,
    before resource.Revision, limit int) ([]Metadata, error)
func (s *Store) Collect(ctx context.Context) error
func (s *Store) Recover(ctx context.Context) error
func (s *Store) Close() error
```

Store construction is `cpstore.New(client *clientv3.Client, options Options) (*Store, error)`. Define `Options` in `cpstore/types.go` with `Prefix, Domain string`, `Limits Limits`, `SigningKeys map[string]ed25519.PublicKey`, `StorageKeys map[string][]byte`, and `Now func() time.Time`. Define store `Limits` there for the storage/count/retention/deadline settings in the global table; HTTP/worker settings stay in controlplane. Constructor performs no mutation; `Recover` acquires ownership and initializes/reconciles. Key fingerprints are persisted/checked with owner fencing. No caller-supplied raw error enters an Admin response.

```go
// internal/controlplane
type Request struct { Principal, Key, RequestID string; Action resource.Action }
type Reply struct { Result cpstore.Result; Replayed bool }
type Page struct {
    Revision resource.Revision
    Items []resource.Document
    LastID string
    More bool
}
type Service struct { /* store, resolver, keyring, bounded queue */ }
func (s *Service) Mutate(ctx context.Context, req Request) (Reply, error)
func (s *Service) Resources(ctx context.Context, kind resource.Kind,
    revision *resource.Revision, after string, limit int) (Page, error)
func (s *Service) Resource(ctx context.Context, kind resource.Kind,
    id string, revision *resource.Revision) (resource.Revision, resource.Document, error)
func (s *Service) Revisions(ctx context.Context, ceiling *resource.Revision,
    before resource.Revision, limit int) (resource.Revision, []cpstore.Metadata, error)
func (s *Service) Run(ctx context.Context) error
func (s *Service) Shutdown(ctx context.Context) error
func (s *Service) Ready() bool
```

Define `controlplane.New(store Store, resolver MaterialReader, options Options) (*Service, error)`. The service-local `Store` interface contains the concrete store methods consumed above; `MaterialReader` contains `Read`. `Options` holds `Domain, SigningKeyID, StorageKeyID string`, `SigningKey ed25519.PrivateKey`, `StorageKey []byte`, `Limits Limits`, `Now func() time.Time`, and a bounded event sink. Production always uses real snapshot crypto. Unit tests may fake I/O, clock and fault boundaries, never introduce an alternate production crypto path.

## Task sequence

Tasks are reviewable deliverables with a red/green test cycle. Within a task, execute one named scenario at a time before expanding its matrix; do not attempt the whole phase in one patch. Code blocks specify critical implementation kernels and executable test seeds; complete the listed matrices and full file responsibilities in the same task. Add required package/import declarations and identifier-leading Go documentation when placing snippets in files.

### Task 1: Strict revisions and managed resource decoding

**Files:** Create `internal/resource/doc.go`, `revision.go`, `document.go`, `decode.go`, `errors.go`, `route_dto.go`, `service_dto.go`, `upstream_dto.go`, `plugin_dto.go`, `tls_dto.go`, `revision_test.go`, `decode_test.go`.

**Interfaces:** Produces `Revision`, `Kind`, `Document`, `Graph`, `Material`, `Operation`, `Action`, `Fault`, `ParseRevision`, `DecodeDocument`, `DecodeAction` and revision JSON methods from the shared contracts. Inherited field source: `internal/config/wire_v1alpha2.go` through `wire_v1alpha7.go`; include the base transport fields in `internal/config/types.go`.

- [ ] **Step 1: Add the regression test seed and its named boundary cases.**

```go
func TestRevisionRejectsNonCanonical(t *testing.T) {
    for _, s := range []string{"", "00", "01", "+1", "-1", " 1", "1e2", "18446744073709551616"} {
        if _, err := ParseRevision(s); err == nil { t.Fatalf("accepted %q", s) }
    }
    r, err := ParseRevision("18446744073709551615")
    if err != nil { t.Fatal(err) }
    if _, err := r.Next(); err == nil { t.Fatal("revision wrapped") }
}
```

Add `TestDecodeStrictJSON` for unknown/duplicate nested keys, trailing document, invalid UTF-8, depth 32/33, null, unknown kinds and duplicate operations. Add `TestDecodeManagedFields` with every inherited field, omitted versus explicit zero/false/empty, ID boundaries and the DownstreamTLS singleton. Numeric JSON revisions must fail even when representable. Validate zero/nonzero revisions separately from graph completeness.

- [ ] **Step 2: Run the focused test before implementation.**

```text
go test ./internal/resource -run 'TestRevision|TestDecode' -count=1
```

Expected: FAIL for the missing contract or the asserted behavior. Investigate unrelated setup failures before using the failure as red-test evidence.

- [ ] **Step 3: Implement the contract in the listed files.**

Implement decimal parsing without float conversion:

```go
func ParseRevision(s string) (Revision, error) {
    if s == "" || (len(s) > 1 && s[0] == '0') {
        return 0, &Fault{Code: "INVALID_REVISION"}
    }
    for i := range s {
        if s[i] < '0' || s[i] > '9' { return 0, &Fault{Code: "INVALID_REVISION"} }
    }
    n, err := strconv.ParseUint(s, 10, 64)
    if err != nil { return 0, &Fault{Code: "INVALID_REVISION"} }
    return Revision(n), nil
}
```

Use a bounded token prepass for duplicate keys, UTF-8, depth and null rejection before typed decode with unknown-field rejection and EOF check. Define one private DTO per managed kind; preserve pointer presence and duration strings. Canonical stored spec comes from the typed DTO, never unchecked RawMessage. Keep plugin config typed by built-in name, including disabled/unreferenced attachments. Map all invalid input to bounded Fault fields.

- [ ] **Step 4: Format changed Go files and rerun the focused command.** Expected: PASS, including every listed matrix case. Inspect ownership, cancellation and error mappings in the diff; run neighboring regression packages when listed.

- [ ] **Step 5: Commit only this task's listed changed files after `git diff --check`.** Commit subject: `feat: define strict managed configuration documents`. Use explicit paths with `git add`; never stage unrelated work.

### Task 2: Canonical logical identity and atomic graph edits

**Files:** Create `internal/resource/canonical.go`, `mutate.go`, `canonical_test.go`, `mutate_test.go`; extend `decode_test.go`.

**Interfaces:** Consumes Task 1; produces `Normalize`, `Digest`, `Apply`. `Apply` returns an owned graph and the sorted IDs of explicitly put SecretRefs, without reading files or validating intermediate references.

- [ ] **Step 1: Add the regression test seed and its named boundary cases.**

```go
func TestDigestIgnoresObjectOrder(t *testing.T) {
    a, err := DecodeAction([]byte(`{"expected_revision":"0","operations":[{"op":"put","kind":"SecretRef","id":"key","spec":{"mount":"tls","path":"key.pem"}}]}`))
    if err != nil { t.Fatal(err) }
    b, err := DecodeAction([]byte(`{"operations":[{"spec":{"path":"key.pem","mount":"tls"},"id":"key","kind":"SecretRef","op":"put"}],"expected_revision":"0"}`))
    if err != nil { t.Fatal(err) }
    x, err := Digest(a); if err != nil { t.Fatal(err) }
    y, err := Digest(b); if err != nil { t.Fatal(err) }
    if x != y { t.Fatal("equivalent actions differ") }
}
```

Golden bytes/digests must cover operation reordering, map order, non-ASCII escaping, uint64 boundary, duration strings, plugin schema, optional defaults/presence and ordered arrays. Add `TestApplyFinalStateOrderIndependent`, `TestApplyMissingDelete`, `TestApplyDoesNotMutateInput`, and `TestApplyReturnsOnlyExplicitRefreshIDs`. Assert changed expected/source revision or body changes identity; material bytes are absent from the hash input.

- [ ] **Step 2: Run the focused test before implementation.**

```text
go test ./internal/resource -run 'TestDigest|TestNormalize|TestApply' -count=1
```

Expected: FAIL for the missing contract or the asserted behavior. Investigate unrelated setup failures before using the failure as red-test evidence.

- [ ] **Step 3: Implement the contract in the listed files.**

Normalize CRUD to Action before hashing. Sort operations by kind then ID, reject duplicate keys, retain nested array order and field presence. Marshal the typed canonical action with no trailing newline:

```go
func Digest(a Action) ([32]byte, error) {
    normalized, err := Normalize(a)
    if err != nil { return [32]byte{}, err }
    b, err := json.Marshal(normalized)
    if err != nil { return [32]byte{}, err }
    return sha256.Sum256(b), nil
}
```

Normalize each Spec using its typed profile first; plain RawMessage key order is not canonical. Apply all puts/deletes to a cloned keyed graph, then sort output; missing delete fails. Rollback actions contain source revision instead of operations. No-op content is not suppressed.

- [ ] **Step 4: Format changed Go files and rerun the focused command.** Expected: PASS, including every listed matrix case. Inspect ownership, cancellation and error mappings in the diff; run neighboring regression packages when listed.

- [ ] **Step 5: Commit only this task's listed changed files after `git diff --check`.** Commit subject: `feat: canonicalize configuration transactions`. Use explicit paths with `git add`; never stage unrelated work.

### Task 3: Pure shared validation and PluginConfig expansion

**Files:** Create `internal/resource/expand.go`, `convert.go`, `validate.go`, `validate_test.go`, `expand_test.go`; create `internal/model/validate.go`, `validate_test.go`; modify `internal/config/validate.go`, `internal/runtime/validate.go` and their existing tests only where extracting shared checks.

**Interfaces:** Consumes Graph/Material; produces `Expand`, `Convert`, `Validate`. Add `model.ValidateReferences(resources ResourceSet) error` for shared graph checks, preserving legacy ID rules. Managed-only rules remain in resource; map errors back to existing config/runtime envelopes.

- [ ] **Step 1: Add the regression test seed and its named boundary cases.**

```go
func TestExpandRejectsPluginsAndReference(t *testing.T) {
    _, err := DecodeDocument([]byte(`{"kind":"Service","id":"s","spec":{"upstream_ref":"u","plugins":[],"plugin_config_ref":"p"}}`))
    if err == nil { t.Fatal("accepted both plugin forms") }
}
```

Add `TestValidateFinalGraph` for forward references, missing targets, at least one route, required default certificate, route/service inheritance, unreferenced invalid resources, disabled plugin config validation, TLS references, upstream health/retry and WebSocket with strict HTTP/2. Reuse temporary certificate generation pattern from existing tests. Add parity tests for v1alpha7 defaults and old-version nil DownstreamTLS, and retain `TestManagerActivationKeepsLastKnownGood`. Assert pure validation opens no listeners, starts no probes and leaves no transport leases.

- [ ] **Step 2: Run the focused test before implementation.**

```text
go test ./internal/resource ./internal/model ./internal/config ./internal/runtime ./internal/plugin ./internal/downstreamtls ./internal/upstream -count=1
```

Expected: FAIL for the missing contract or the asserted behavior. Investigate unrelated setup failures before using the failure as red-test evidence.

- [ ] **Step 3: Implement the contract in the listed files.**

Expand referenced PluginConfig attachments onto Route/Service, then use existing built-in registry, router compilation, upstream pure policy checks and TLS constructors. Do not use runtime.Builder or upstream registry preparation. Keep a clear conversion sequence:

```text
canonical declarations
  -> expand PluginConfig into a cloned effective graph
  -> convert inherited fields/defaults into model values
  -> construct TLS handles from captured bytes
  -> validate model references, upstream policies and all plugin schemas
  -> check effective route retry/WebSocket policy, compile router and TLS selector
```

Extract only reusable pure checks. Preserve config version adapters and runtime BuildError codes/stages. Add a current-time material check for every consumed certificate, not just the downstream default. Startup structural decode in Task 6 must remain separate from this current-time validation.

- [ ] **Step 4: Format changed Go files and rerun the focused command.** Expected: PASS, including every listed matrix case. Inspect ownership, cancellation and error mappings in the diff; run neighboring regression packages when listed.

- [ ] **Step 5: Commit only this task's listed changed files after `git diff --check`.** Commit subject: `feat: validate managed resource graphs without runtime activation`. Use explicit paths with `git add`; never stage unrelated work.

### Task 4: Safe bounded SecretRef file reads

**Files:** Create `internal/secretref/doc.go`, `resolver.go`, `path.go`, `open_linux.go`, `open_windows.go`, `open_other.go`, `resolver_test.go`, `open_linux_test.go`, `open_windows_test.go`.

**Interfaces:** Produces `secretref.Open`, `Resolver.Read`, `Resolver.Close`; consumes logical mount-to-absolute-root mapping. No change to existing standalone TLS file loader.

- [ ] **Step 1: Add the regression test seed and its named boundary cases.**

```go
func TestReadRejectsTraversal(t *testing.T) {
    r, err := Open(map[string]string{"tls": t.TempDir()})
    if err != nil { t.Fatal(err) }
    defer r.Close()
    for _, path := range []string{"../key", "/key", "a/../key", "a//key", "a\\key", "C:/key", "key/"} {
        if _, err := r.Read(t.Context(), "tls", path, 1024); err == nil {
            t.Fatalf("accepted path %q", path)
        }
    }
}
```

Add regular file limit/limit+1, growing file, nonexistent mount, empty/dot/NUL/drive/UNC paths, directory, symlink escape/replacement and canceled-context cases. Linux FIFO test must finish without a writer; add device-file rejection where available. Run native Linux tests in CI and native Windows adapter tests before claiming Windows CP support; a skipped privilege-dependent test is not passing evidence.

- [ ] **Step 2: Run the focused test before implementation.**

```text
go test ./internal/secretref -count=1
```

Expected: FAIL for the missing contract or the asserted behavior. Investigate unrelated setup failures before using the failure as red-test evidence.

- [ ] **Step 3: Implement the contract in the listed files.**

Reject invalid lexical paths before rooted operations. Retain mount handles for the resolver lifetime. Linux: use directory-relative descriptor walking with O_NOFOLLOW on components and O_NONBLOCK|O_NOFOLLOW for the final open, then fstat regular-file checking; reject symlinks in this initial adapter, which is permitted by the spec's optional internal-symlink support. Windows: use rooted open with reparse-point/device rejection and root/handle containment; if the pinned APIs cannot prove a safe case, reject it. Other OS adapters return a stable unsupported-platform error instead of unrestricted fallback.

Bound reads and preserve cancellation between operations:

```go
data, err := io.ReadAll(io.LimitReader(file, limit+1))
if err != nil { return nil, err }
if int64(len(data)) > limit { return nil, &resource.Fault{Code: "SECRET_REF_TOO_LARGE"} }
if err := ctx.Err(); err != nil { return nil, err }
return data, nil
```

Run this only after a nonblocking safe regular-file open. Close all partial traversal handles on error. Never implement cancellation by leaking an open goroutine; supported mounts must be local regular-file storage, and any inability to bound platform operations is a failed adapter acceptance test.

- [ ] **Step 4: Format changed Go files and rerun the focused command.** Expected: PASS, including every listed matrix case. Inspect ownership, cancellation and error mappings in the diff; run neighboring regression packages when listed.

- [ ] **Step 5: Commit only this task's listed changed files after `git diff --check`.** Commit subject: `feat: resolve managed secret files within bounded mount roots`. Use explicit paths with `git add`; never stage unrelated work.

### Task 5: Deterministic signed portable artifacts

**Files:** Create `internal/snapshot/doc.go`, `bundle.go`, `canonical.go`, `sign.go`, `bundle_test.go`, `sign_test.go`.

**Interfaces:** Consumes canonical declarations, captured material and expanded graph; produces Bundle, Signed, Export, `Sign`, `Verify`, `Project`, `VerifyExport`. Define private typed header and section DTOs in bundle.go with fixed JSON order.

- [ ] **Step 1: Add the regression test seed and its named boundary cases.**

```go
func TestExportSignatureBindsRevision(t *testing.T) {
    public, private, err := ed25519.GenerateKey(rand.Reader)
    if err != nil { t.Fatal(err) }
    b := Bundle{Domain:"test", Revision:1, CommittedAt:time.Unix(1,0).UTC()}
    signed, err := Sign(b, "sign-1", private)
    if err != nil { t.Fatal(err) }
    if err := VerifyExport(Project(signed), "test", 2,
        map[string]ed25519.PublicKey{"sign-1":public}); err == nil {
        t.Fatal("accepted wrong revision")
    }
}
```

Fix golden header/section bytes using test-local deterministic public data; generate real private keys at runtime. Test all material bytes covered, same bytes at different revision identity, wrong domain/key/signature/hash/length, unsupported version, limit/limit+1 and caller mutation of returned buffers. `TestBundleRestoresPinnedMaterial` must show full CP round trip, while `TestExportOmitsDeclarationMaterial` checks exact snapshot bytes are preserved.

- [ ] **Step 2: Run the focused test before implementation.**

```text
go test ./internal/snapshot -run 'TestBundle|TestSign|TestExport' -count=1
```

Expected: FAIL for the missing contract or the asserted behavior. Investigate unrelated setup failures before using the failure as red-test evidence.

- [ ] **Step 3: Implement the contract in the listed files.**

Freeze header fields as format, encoding, domain, revision, committed_at, signing_key_id, declarations_sha256, declarations_length, snapshot_sha256, snapshot_length. Hash canonical sections and sign exact header bytes:

```go
signature := ed25519.Sign(privateKey, canonicalHeader)
if !ed25519.Verify(publicKey, signed.Header, signed.Signature) {
    return Bundle{}, &resource.Fault{Code: "SNAPSHOT_SIGNATURE_INVALID"}
}
```

Store secret ID-to-material references in declarations and a deduplicated consumed material table in Snapshot. Unused SecretRef bytes remain declaration-only. Verify lengths/hashes/schema/domain/revision as well as signature; enforce 16/16/33 MiB limits before allocating subsequent copies. Project must contain no declarations, logical mounts/paths or unconsumed material.

- [ ] **Step 4: Format changed Go files and rerun the focused command.** Expected: PASS, including every listed matrix case. Inspect ownership, cancellation and error mappings in the diff; run neighboring regression packages when listed.

- [ ] **Step 5: Commit only this task's listed changed files after `git diff --check`.** Commit subject: `feat: encode and sign portable configuration artifacts`. Use explicit paths with `git add`; never stage unrelated work.

### Task 6: Storage envelope encryption and verified structural loading

**Files:** Create `internal/snapshot/envelope.go`, `manifest.go`, `envelope_test.go`, `manifest_test.go`; extend `bundle_test.go`.

**Interfaces:** Produces Manifest/Chunk, `Seal`, `Open`. Seal is called only after Store.ReserveKeyUse in Task 11. Structural Verify checks integrity and DTO shape without rejecting an expired but intact certificate; candidate validation remains Task 3.

- [ ] **Step 1: Add the regression test seed and its named boundary cases.**

```go
func TestEnvelopeRejectsChangedArtifactIdentity(t *testing.T) {
    _, private, err := ed25519.GenerateKey(rand.Reader)
    if err != nil { t.Fatal(err) }
    signed, err := Sign(Bundle{Domain:"test",Revision:1}, "sign-1", private)
    if err != nil { t.Fatal(err) }
    key := make([]byte, 32)
    if _, err := rand.Read(key); err != nil { t.Fatal(err) }
    m, chunks, err := Seal(signed, "attempt-1", "storage-1", key)
    if err != nil { t.Fatal(err) }
    m.ArtifactID = "attempt-2"
    if _, err := Open(m, chunks, key); err == nil { t.Fatal("identity not authenticated") }
}
```

Test wrong KEK/tag/nonce/order/hash, missing/extra/duplicate chunks, truncated ciphertext, tampered wrapped DEK and every AAD binding. Test random attempts differ, old keys still read after rotation, and corrupted/unknown-key artifacts cannot become empty state. `TestVerifyAllowsIntactExpiredMaterial` followed by candidate Validate must distinguish structural acceptance from expired-candidate rejection.

- [ ] **Step 2: Run the focused test before implementation.**

```text
go test ./internal/snapshot -count=1
```

Expected: FAIL for the missing contract or the asserted behavior. Investigate unrelated setup failures before using the failure as red-test evidence.

- [ ] **Step 3: Implement the contract in the listed files.**

Use crypto/rand and standard AES/GCM. Generate fresh 32-byte DEK, use separate domain-separated AAD for content and wrapping, and nonce-prefixed ciphertext with NewGCMWithRandomNonce. Wrap the DEK with the mounted KEK; zero temporary owned key buffers when practical without claiming guaranteed memory erasure.

```go
block, err := aes.NewCipher(key)
if err != nil { return nil, err }
aead, err := cipher.NewGCMWithRandomNonce(block)
if err != nil { return nil, err }
sealed := aead.Seal(nil, nil, plaintext, aad)
```

The fragment is the AEAD helper kernel. Encrypt the full signed bundle before splitting ciphertext into 256 KiB chunks. Manifest has ordered lengths/hashes and whole ciphertext hash; cap 136 chunks/16 KiB manifest/34 MiB ciphertext. Validate manifest bounds before fetch/allocation; after decrypt, call Verify before exposing Bundle.

- [ ] **Step 4: Format changed Go files and rerun the focused command.** Expected: PASS, including every listed matrix case. Inspect ownership, cancellation and error mappings in the diff; run neighboring regression packages when listed.

- [ ] **Step 5: Commit only this task's listed changed files after `git diff --check`.** Commit subject: `feat: encrypt and verify immutable configuration storage`. Use explicit paths with `git add`; never stage unrelated work.

### Task 7: Real etcd fixture, domain initialization and ownership

**Files:** Modify `go.mod`, `go.sum`; create `internal/cptest/doc.go`, `etcd.go`, `material.go`; create `internal/cpstore/doc.go`, `types.go`, `keys.go`, `owner.go`, `initialize.go`, `fixture_test.go`, `owner_test.go`, `initialize_test.go`.

**Interfaces:** Produces cpstore types/Options/Limits/New/Head/Recover/Close and `cptest.StartEtcd(t *testing.T) *clientv3.Client`. Test helper `newTestStore(t *testing.T, client *clientv3.Client, prefix string) *Store` uses generated keys, domain `test` and fixed defaults, registers Close, and calls Recover. Add `DefaultLimits() Limits`. Also define `cptest.Graph(t *testing.T) (resource.Graph, resource.Material)` here: generate a self-signed localhost certificate/key in memory, return one Route first, one Upstream targeting http://127.0.0.1:8081, two SecretRefs, one Certificate and DownstreamTLS/default. Capture PEM under the corresponding SecretRef IDs. Use explicit valid inherited defaults and the same fixture from Tasks 9 and 11; do not commit PEM.

- [ ] **Step 1: Add the failing behavioral test and boundary matrix.**

```go
func TestOwnerRejectsSecondCP(t *testing.T) {
    client := cptest.StartEtcd(t)
    first := newTestStore(t, client, "/phase4a/owner")
    head, err := first.Head(t.Context())
    if err != nil || head.Revision != 0 { t.Fatalf("head=%+v err=%v", head, err) }
    second, err := New(client, Options{Prefix:"/phase4a/owner", Domain:"test", Limits:DefaultLimits()})
    if err != nil { t.Fatal(err) }
    defer second.Close()
    if err := second.Recover(t.Context()); err == nil { t.Fatal("second owner admitted") }
}
```

Assert empty initialization atomicity, existing domain mismatch, missing head, prefix garbage, lease loss and delayed writes under old owner. Test all key encoding boundaries and failure before/after initialization. Fixture helpers use standard testing, bounded process waits and no committed material. `go test` in normal skip mode does not satisfy this task's real-etcd acceptance.

- [ ] **Step 2: Run the focused test.**

```text
go test ./internal/cpstore -run 'TestOwner|TestInitialize' -count=1
```

Expected: FAIL for the new assertion/contract; an unavailable etcd fixture is an environment failure, not red-test evidence.

- [ ] **Step 3: Implement the specified behavior.**

After the Phase 3D1 implementation-and-CI gate, install only the selected client dependency:

```text
go get go.etcd.io/etcd/client/v3@v3.6.5
go mod tidy
go list -m go.etcd.io/etcd/client/v3 go.etcd.io/etcd/api/v3 go.etcd.io/etcd/client/pkg/v3 google.golang.org/grpc
```

Check exact etcd v3.6.5 selections and retained gRPC v1.82.1. Start a real pinned container with temporary volume, loopback random client port and default server request/txn limits. Generate server/client TLS under t.TempDir; enable client-cert authentication. Wait on authenticated endpoint status with a deadline; cleanup only that test's container and temp files. Record/assert server version 3.6.5. If GATEWAY_TEST_ETCD=1, any unavailable Docker/image/server is a hard test failure; otherwise tests explicitly skip with a reason. Never contact a user's configured etcd.

Acquire owner lease by comparing owner absence, use a random process token, and refresh with bounded keepalive. For initialization, validate that only the owned owner key exists before creating domain/head0/accounting atomically. Every write carries an owner comparison:

```go
ownerCmp := clientv3.Compare(clientv3.Value(ownerKey), "=", ownerToken)
```

Define keys using fixed kind constants, safe configured prefix and 20-digit revisions. Recover must refuse initialized domain without head, foreign domain and unexplained data. Losing confirmed ownership invalidates ongoing consistent work; no automatic competing election.

- [ ] **Step 4: Format changed Go files and rerun the focused command.** Expected: PASS for the entire matrix. Exercise race mode for shared store/worker/read ownership; preserve existing DP tests.

- [ ] **Step 5: Inspect `git diff --check`, stage explicit task paths and commit.** Subject: `feat: initialize and fence control plane storage`.

### Task 8: Durable reservations, key-use counts and immutable stages

**Files:** Create `internal/cpstore/accounting.go`, `keyuses.go`, `stage.go`, `accounting_test.go`, `keyuses_test.go`, `stage_test.go`; extend `types.go`.

**Interfaces:** Produces Reserve/ReserveKeyUse/Stage/Release and persistent counters. Define package-private `reserveUseCount(ctx context.Context, keyID string, cap uint64) error`; exported ReserveKeyUse fixes cap at 1<<31.

- [ ] **Step 1: Add the failing behavioral test and boundary matrix.**

```go
func TestKeyUseReservationDoesNotRefund(t *testing.T) {
    s := newTestStore(t, cptest.StartEtcd(t), "/phase4a/keyuses")
    if err := s.reserveUseCount(t.Context(), "k1", 1); err != nil { t.Fatal(err) }
    if err := s.reserveUseCount(t.Context(), "k1", 1); err == nil {
        t.Fatal("reused an exhausted wrapping allowance")
    }
}
```

Add stage failures after each chunk, immutable duplicate chunk rejection, late chunk after deleting claim, manifest/chunk bounds, exhausted key cap, key fingerprint mismatch, owner loss, quota error and retry without counter rollback. Verify account totals reflect actual bytes and outstanding reservations after restart; capacity refusal preserves head.

- [ ] **Step 2: Run the focused test.**

```text
go test ./internal/cpstore -run 'TestReservation|TestKeyUse|TestStage' -count=1
```

Expected: FAIL for the new assertion/contract; an unavailable etcd fixture is an environment failure, not red-test evidence.

- [ ] **Step 3: Implement the specified behavior.**

Reserve worst-case candidate/old-head/auxiliary space before expensive work. Refine reservations using actual encoded sizes without double charging. Persist monotonic wrapping count before Seal, including attempts that later fail; persist immutable key fingerprints and reject changed bytes under an existing ID.

Write random-attempt object state first, then immutable chunks. Each chunk write compares owner, uploading state/version and chunk absence. Completing compares uploading version and records fixed manifest digest only after every chunk acknowledgment.

```text
reserve -> reserve key use -> encrypt -> uploading
uploading + absent chunk + owner -> put one chunk
uploading + acknowledged complete manifest -> complete
uploading/complete + owner -> deleting
deleting -> bounded deletion acknowledgments -> release charged bytes
```

Represent all reservations durably so restart accounts for partial writes. Check protobuf-encoded requests against bounds; never depend on increasing server limits. No stage/chunk is attached to a lease that could expire after publication.

- [ ] **Step 4: Format changed Go files and rerun the focused command.** Expected: PASS for the entire matrix. Exercise race mode for shared store/worker/read ownership; preserve existing DP tests.

- [ ] **Step 5: Inspect `git diff --check`, stage explicit task paths and commit.** Subject: `feat: stage bounded encrypted configuration artifacts`.

### Task 9: Atomic publication, idempotency, audit and ambiguous commit reconciliation

**Files:** Create `internal/cpstore/publish.go`, `reconcile.go`, `publish_test.go`, `reconcile_test.go`; extend `fixture_test.go`.

**Interfaces:** Produces Lookup/Publish/Reconcile and durable Result/Audit. Define package-private `buildPublicationTxn(p Publication) (*etcdserverpb.TxnRequest, error)` for exact size/op checks. Test helper `stageTestPublication(t *testing.T, s *Store) Publication` builds a valid temporary graph/material, reserves, signs/encrypts and stages with unique identity; Task 7 material helper generates its TLS bytes.

- [ ] **Step 1: Add the failing behavioral test and boundary matrix.**

```go
func TestPublicationCannotPassAbandonmentFence(t *testing.T) {
    s := newTestStore(t, cptest.StartEtcd(t), "/phase4a/fence")
    p := stageTestPublication(t, s)
    outcome, _, err := s.Reconcile(t.Context(), p)
    if err != nil || outcome != Abandoned { t.Fatalf("outcome=%v err=%v", outcome, err) }
    outcome, _, _ = s.Publish(t.Context(), p)
    if outcome == Committed { t.Fatal("published a deleting artifact") }
    head, err := s.Head(t.Context())
    if err != nil || head.Revision != 0 { t.Fatalf("head=%+v err=%v", head, err) }
}
```

Use a test KV transport wrapper that can delay the actual Txn send, execute then drop the response, or fail before sending; no production fault flag. Test publication-wins and abandonment-wins orders, actual absent read preceding delayed commit, restart replay, same-base competitors, 1,000-operation counts, no audit omission, no duplicate replay audit, exact response bytes, independent expiry and capacity refusal. Advance a fake clock around retention boundaries; assert failed attempts create no committed replay.

- [ ] **Step 2: Run the focused test.**

```text
go test ./internal/cpstore -run 'TestPublication|TestReplay|TestReconcile|TestAudit' -race -count=1
```

Expected: FAIL for the new assertion/contract; an unavailable etcd fixture is an environment failure, not red-test evidence.

- [ ] **Step 3: Implement the specified behavior.**

Final transaction compares owner, captured head, complete stage/digest, idempotency absence and reservation/accounting version; atomically writes head, revision metadata/manifest, published state, immutable response, success audit and accounting. Build one fixed-size transaction independent of operation count:

```go
if proto.Size(txn) > 128<<10 ||
    len(txn.Compare)+len(txn.Success)+len(txn.Failure) > 32 {
    return nil, &resource.Fault{Code: "SNAPSHOT_TOO_LARGE"}
}
```

Lookup checks principal/key digests and request digest; same key with changed request is conflict, equal digest returns immutable response before expected-revision comparison. Successful audit is <=4 KiB and includes counts, not resource bodies.

On unknown final outcome, first read committed idempotency. Absence alone does not establish failure. Claim complete->deleting with owner and idempotency-absence CAS. If publication wins, re-read result; if abandonment wins, delayed publication cannot pass. Leave Unknown if store cannot resolve; no new worker mutation may overtake it.

Capture administrative commit time immediately before the bounded final attempt. To honor the minimum replay window despite that pre-commit timestamp, set immutable expiry to intent time + configured retention + the full commit/reconciliation deadline, and refuse to send after that deadline. Keep idempotency/audit records until at least this bound; do not use a lease started during staging. An expired record is removed with owner/version fencing before a reused key can satisfy absence.

- [ ] **Step 4: Format changed Go files and rerun the focused command.** Expected: PASS for the entire matrix. Exercise race mode for shared store/worker/read ownership; preserve existing DP tests.

- [ ] **Step 5: Inspect `git diff --check`, stage explicit task paths and commit.** Subject: `feat: atomically publish revisions with durable replay`.

### Task 10: Pinned readers, retention and restart recovery

**Files:** Create `internal/cpstore/reader.go`, `retention.go`, `reader_test.go`, `retention_test.go`; extend `reconcile.go`, `accounting.go`, `initialize_test.go`.

**Interfaces:** Produces ReadHandle/Acquire/Revisions/Collect and completed Recover. `ReadHandle.Bundle`/Export return owned copies or immutable views whose usage ends before Close; choose owned copies for public methods and test isolation. Current-head capture/retry is completed by controlplane reads in Task 14.

- [ ] **Step 1: Add the failing behavioral test and boundary matrix.**

```go
func TestCurrentArtifactSurvivesCollection(t *testing.T) {
    s := newTestStore(t, cptest.StartEtcd(t), "/phase4a/current")
    p := stageTestPublication(t, s)
    outcome, _, err := s.Publish(t.Context(), p)
    if err != nil || outcome != Committed { t.Fatalf("outcome=%v err=%v", outcome, err) }
    if err := s.Collect(t.Context()); err != nil { t.Fatal(err) }
    h, err := s.Acquire(t.Context(), p.Result.Revision)
    if err != nil { t.Fatal(err) }
    defer h.Close()
    if h.Bundle().Revision != p.Result.Revision { t.Fatal("wrong artifact") }
}
```

Test pin versus GC race, current-head transition, double Close, read cancellation, historical 410/future404/current503, no mixed revisions, orphan cleanup interruption, deleting bytes still charged, retained/audit/idempotency separate limits, old public/KEK keys and absent files after restart. Use fake time for 24h bounds, not sleeps. Assert bounded scans and counters match persisted objects after every injected restart.

- [ ] **Step 2: Run the focused test.**

```text
go test ./internal/cpstore -run 'TestRead|TestCurrent|TestRetention|TestRecovery' -race -count=1
```

Expected: FAIL for the new assertion/contract; an unavailable etcd fixture is an environment failure, not red-test evidence.

- [ ] **Step 3: Implement the specified behavior.**

Use a short process-local lifetime mutex for pin acquisition/retirement claim; no etcd I/O or crypto under it. Mark retirement locally before the fenced etcd CAS, then either restore availability on definite CAS failure or keep retirement pending while outcome is uncertain. A current-head comparison prevents deleting current data.

```text
pin live revision -> release lifetime mutex -> bounded fetch/decrypt/verify
retirement claim -> compare owner/head/state -> remove discoverability
wait for existing pins -> claim deleting -> delete batches -> reconcile accounting
```

Keep pinned retired bytes in the in-flight allowance and reject new reservations if full. Cursors hold no pin between requests. Enforce read/pin deadlines without releasing bytes still in use; cancellation joins work before releasing pin. Scan 100 records and delete <=32 operations per batch, owner-fenced and resumable.

Recover verifies current artifact structurally, rebuilds counters in bounded pages, retires orphan stages without promoting them and reconciles owner/key state. Missing historical keys fail affected reads; missing/corrupt current artifact prevents readiness. Empty verified head0 is allowed; expired intact current material is allowed for repair.

- [ ] **Step 4: Format changed Go files and rerun the focused command.** Expected: PASS for the entire matrix. Exercise race mode for shared store/worker/read ownership; preserve existing DP tests.

- [ ] **Step 5: Inspect `git diff --check`, stage explicit task paths and commit.** Subject: `feat: retain and recover pinned committed artifacts`.

### Task 11: Candidate preparation, pinned refresh and historical rollback

**Files:** Create `internal/controlplane/doc.go`, `service.go`, `prepare.go`, `prepare_test.go`; reuse `internal/cptest/material.go` from Task 7.

**Interfaces:** Define Request/Reply/Service/Store/MaterialReader/Options and `Prepared` with `Graph resource.Graph`, `Material resource.Material`, `Effective resource.Graph`, `Revision resource.Revision`, `SourceRevision *resource.Revision`. Add `Service.Prepare(ctx context.Context, base snapshot.Bundle, action resource.Action) (Prepared, error)`; the worker alone calls it. `cptest.Graph(t *testing.T) (resource.Graph, resource.Material)` returns the minimum valid graph (Route, Upstream, two SecretRefs, Certificate and DownstreamTLS) with temporary generated PEM.

- [ ] **Step 1: Add the failing test seed and the specified scenario matrix.**

```go
func TestPrepareUnchangedSecretDoesNotReadFiles(t *testing.T) {
    graph, material := cptest.Graph(t)
    s := &Service{resolver: forbiddenReader{}, now: time.Now}
    base := snapshot.Bundle{Domain:"test", Revision:1, Declarations:graph, Material:material}
    route := graph[0] // fixture guarantees the Route is first
    a := resource.Action{ExpectedRevision:1, Operations:[]resource.Operation{
        {Op:"put", Kind:route.Kind, ID:route.ID, Spec:route.Spec},
    }}
    p, err := s.Prepare(t.Context(), base, a)
    if err != nil { t.Fatal(err) }
    if p.Revision != 2 { t.Fatalf("revision=%v", p.Revision) }
}
type forbiddenReader struct{}
func (forbiddenReader) Read(context.Context, string, string, int64) ([]byte, error) {
    return nil, errors.New("unexpected file read")
}
```

Add counting reader tests: two consumers one SecretRef => one read; two IDs same path => two reads. Change/remove files then mutate Route/Certificate and prove exact prior bytes reused. Explicit unchanged SecretRef put refreshes and advances revision. Test coordinated key mismatch rejection, current-time expiry, deleted mount rollback, invalid final references and input ownership. Helpers are test-only; no private key fixtures committed.

- [ ] **Step 2: Run the focused command before implementing.**

```text
go test ./internal/controlplane -run 'TestPrepare|TestRefresh|TestRollback' -count=1
```

Expected: FAIL for the missing behavior, not an unrelated fixture failure.

- [ ] **Step 3: Implement the behavior below.**

For mutations, Apply clones declarations and identifies refreshed IDs; read each once at generic 1 MiB bound, then enforce tighter limits at every consumer. Reuse all other material bytes. Remove material for deleted SecretRefs; keep unreferenced bytes only if the declaration remains. Expand and validate the complete candidate after every operation has been applied.

```text
base + operations -> new declarations + explicit refresh IDs
refresh IDs -> one captured read per ID
reused + refreshed material -> full current-time validation
valid candidate -> next global revision, even if content is identical
```

For rollback, worker pins source with Store.Acquire and passes its stored Bundle as content source while retaining Action.ExpectedRevision as current base. Prepare copies historical declarations/material, performs no filesystem reads, validates at now, sets SourceRevision and assigns current head+1. Reject pruned source, invalid/expired historical content and uint64 overflow before staging.

- [ ] **Step 4: Format and rerun the focused command.** Expected: PASS for all named cases. Run the relevant existing regression packages when changing shared behavior.

- [ ] **Step 5: Review the diff, run `git diff --check`, stage explicit paths and commit.** Subject: `feat: prepare pinned configuration updates and rollback`.

### Task 12: Bounded mutation worker and cancellation semantics

**Files:** Create `internal/controlplane/worker.go`, `commit.go`, `worker_test.go`, `commit_test.go`, `fixture_test.go`; extend `service.go`.

**Interfaces:** Produces New/Mutate/Run/Shutdown, using Request/Reply and Store contract. Add unit helper `newWorkerFixture(t *testing.T) *workerFixture` with `Service *Service`, `BlockPreparation chan struct{}`, `CommitStarted chan struct{}`, `ReleaseCommit chan struct{}`, and `Published atomic.Int64`, and `Action resource.Action` containing the valid initial graph transaction; it uses a contract-enforcing in-memory Store fake and generated real signing/storage keys. Fake behavior is tested against real-store scenarios in Task 17.

- [ ] **Step 1: Add the failing test seed and the specified scenario matrix.**

```go
func TestWorkerCanceledQueuedMutationDoesNotPublish(t *testing.T) {
    f := newWorkerFixture(t)
    ctx, cancel := context.WithCancel(t.Context())
    cancel()
    _, err := f.Service.Mutate(ctx, Request{Principal:"urn:test:operator", Key:"cancel",
        Action:f.Action})
    if !errors.Is(err, context.Canceled) { t.Fatalf("want cancellation, got %v", err) }
    if f.Published.Load() != 0 { t.Fatal("published canceled work") }
}
```

Add `TestWorkerQueueBounds`, `TestWorkerChecksRevisionBeforeRead`, `TestReplayChecksKeyBeforeRevision`, `TestCommitSurvivesClientCancel`, `TestCommitUnknownBlocksNextMutation`, `TestWorkerShutdownDuringPreparation`, and `TestWorkerShutdownDuringCommit`. Use channels to order events rather than sleeps. Test 17th pending request, 5s queue deadline, 30s preparation deadline, post-commit response loss and same-key join/serialization. Count maximum active preparation=1 and prove no goroutine/handle leak after repeated cycles.

- [ ] **Step 2: Run the focused command before implementing.**

```text
go test ./internal/controlplane -run 'TestWorker|TestCommit|TestReplay' -race -count=1
```

Expected: FAIL for the missing behavior, not an unrelated fixture failure.

- [ ] **Step 3: Implement the behavior below.**

Acquire bounded admission, enqueue at most 16 waiting entries and run exactly one active preparation/publication owner. Return queue-full/wait-expired without spawning per-retry goroutines. Validate key syntax and hash principal/key separately.

```text
lookup retained idempotency -> compare expected head -> reserve
-> pin base/source -> prepare -> reserve key use -> sign/seal/stage
-> independent bounded commit context -> publish/reconcile -> release
```

Committed replay precedes expected-revision check and filesystem access. Use a buffered one-result channel so client disconnect cannot block worker completion. Before entering commit, check preparation context; once commit begins, use a service-owned context with 10-second total deadline, independent of request cancellation. Do not use an unbounded Background context.

When outcome stays Unknown, keep the worker in reconciliation state; use bounded attempts under service ownership and do not dequeue the next mutation. During outage return unavailable for new admissions. Stop on confirmed owner loss; restart recovery fences the old attempt. Release reservation only once commit or abandonment is proven; cleanup owns uncertain stage bytes.

- [ ] **Step 4: Format and rerun the focused command.** Expected: PASS for all named cases. Run the relevant existing regression packages when changing shared behavior.

- [ ] **Step 5: Review the diff, run `git diff --check`, stage explicit paths and commit.** Subject: `feat: serialize bounded control plane mutations`.

### Task 13: Strict bootstrap, keyrings and CP readiness lifecycle

**Files:** Create `internal/controlplane/bootstrap.go`, `limits.go`, `lifecycle.go`, `bootstrap_test.go`, `limits_test.go`, `lifecycle_test.go`; create `configs/phase4a-cp.yaml`.

**Interfaces:** Produces `LoadBootstrap(path string) (Bootstrap, error)`, `DefaultLimits() Limits`, `ValidateLimits(l Limits) error`, `Service.Ready`. Define Bootstrap with domain/prefix, Admin and telemetry listeners, Admin TLS/roles, etcd endpoints/TLS, mount roots, signing/storage keyring file maps and active IDs, Limits. Every field has a strict snake_case YAML tag; Options is runtime material loaded from Bootstrap.

- [ ] **Step 1: Add the failing test seed and the specified scenario matrix.**

```go
func TestLimitsRejectUnlimitedAndInconsistentBounds(t *testing.T) {
    limits := DefaultLimits()
    limits.PendingMutations = 0
    if err := ValidateLimits(limits); err == nil { t.Fatal("zero became unlimited") }
    limits = DefaultLimits()
    limits.StagingBytes = 1
    if err := ValidateLimits(limits); err == nil { t.Fatal("candidate cannot fit") }
}
```

Test every default from the spec, reductions and illegal increases, duplicate URI principal, missing/changed keys, empty store ready, corrupt current notready, expired intact current ready for repair, etcd outage/recovery, ownership loss, no mutation readiness from stale cache and capacity-degraded reads. Example contains paths only, no key/certificate bytes; test decoding with generated temporary files.

- [ ] **Step 2: Run the focused command before implementing.**

```text
go test ./internal/controlplane -run 'TestBootstrap|TestLimits|TestLifecycle' -race -count=1
```

Expected: FAIL for the missing behavior, not an unrelated fixture failure.

- [ ] **Step 3: Implement the behavior below.**

Populate a single defaults table from Global Constraints and check positive ranges, maximum supported values and cross-budget feasibility. Strictly reject unknown/duplicate YAML fields, multiple documents, bad listener/domain/prefix/mount/key IDs and ambiguous principal mappings. Load key material with regular-file/size checks; signing public/private match and AES key length must be validated. Enforce immutable ID fingerprints through Store.Recover.

```text
bootstrap validated -> listeners/telemetry available
store ownership + reconciliation + verified current or verified empty -> ready
ownership/store uncertainty -> not ready; no consistent Admin operations
capacity pressure -> writes unavailable, valid reads still available
shutdown -> stop admissions -> drain bounded commit -> close owned resources
```

Initialize logging/telemetry early enough to report corrupt-current state without leaking raw errors. Recoverable store outage remains alive/notready with bounded retries; invalid static bootstrap exits with sanitized structured error. Keep an already verified export only as freshness-unconfirmed internal cache; do not serve it as a consistent Admin read. Shutdown drain is 20 seconds, with queued/preparing work canceled first.

- [ ] **Step 4: Format and rerun the focused command.** Expected: PASS for all named cases. Run the relevant existing regression packages when changing shared behavior.

- [ ] **Step 5: Review the diff, run `git diff --check`, stage explicit paths and commit.** Subject: `feat: configure and supervise the control plane lifecycle`.

### Task 14: Consistent declarative reads and bounded pagination

**Files:** Create `internal/controlplane/reads.go`, `reads_test.go`; create `internal/adminapi/doc.go`, `cursor.go`, `cursor_test.go`.

**Interfaces:** Produces Service.Resources/Resource/Revisions. Define adminapi `Cursor` with `Version int`, `Kind resource.Kind`, `Revision resource.Revision`, `Ceiling resource.Revision`, `LastID string`, `Before resource.Revision`, `Limit int`; `EncodeCursor(c Cursor) (string,error)` and `DecodeCursor(s string) (Cursor,error)`. Validate allowed fields per resource-list/revision-list mode; no implicit authorization.

- [ ] **Step 1: Add the failing test seed and the specified scenario matrix.**

```go
func TestCursorRejectsOversize(t *testing.T) {
    if _, err := DecodeCursor(strings.Repeat("a", 2049)); err == nil {
        t.Fatal("accepted oversized cursor")
    }
    c := Cursor{Version:1,Kind:"Route",Revision:7,LastID:"r1",Limit:100}
    encoded, err := EncodeCursor(c); if err != nil { t.Fatal(err) }
    decoded, err := DecodeCursor(encoded); if err != nil { t.Fatal(err) }
    if decoded != c { t.Fatalf("cursor changed: %+v", decoded) }
}
```

Test fixed-resource pagination concurrent with mutations, removed artifact410, future404, corrupt current503, default-read recapture, literal revision0, no material/key/hash fields, resource count/page cap and byte budget. Cursor tests cover invalid base64, duplicate/unknown/null fields, future version, altered positions and query conflicts. Verify unauthorized caller still cannot use a valid cursor in Task 15.

- [ ] **Step 2: Run the focused command before implementing.**

```text
go test ./internal/controlplane ./internal/adminapi -run 'TestRead|TestCursor|TestPage' -race -count=1
```

Expected: FAIL for the missing behavior, not an unrelated fixture failure.

- [ ] **Step 3: Implement the behavior below.**

For default reads, capture linearizable head, pin its immutable artifact, and retry head capture if retirement won; every retry shares the 10-second deadline. Explicit revision reads never upgrade. At head0, lists are empty and single-resource reads fail. Sort resource IDs bytewise; decode/query limits are bounded before allocations.

```text
resource page: pinned revision + ID > last_id, limit + one lookahead
revision page: revision <= captured ceiling and revision < before, newest first
```

Return only declaration wrappers/metadata. Copy page results before releasing the pin. Revision catalog may lose retained entries between pages but never exceed the captured ceiling. Base64url cursor is strict versioned JSON <=2 KiB, not an authorization token; revalidate kind/ID/revision/limit and conflicting query fields. Cursor does not extend retention.

- [ ] **Step 4: Format and rerun the focused command.** Expected: PASS for all named cases. Run the relevant existing regression packages when changing shared behavior.

- [ ] **Step 5: Review the diff, run `git diff --check`, stage explicit paths and commit.** Subject: `feat: read pinned configuration revisions with bounded cursors`.

### Task 15: Private mTLS Admin API and mutation adapters

**Files:** Create `internal/adminapi/server.go`, `identity.go`, `decode.go`, `mutations.go`, `reads.go`, `errors.go`, `server_test.go`, `identity_test.go`, `decode_test.go`; extend `internal/cptest/material.go` with temporary URI-SAN certificate helpers.

**Interfaces:** Define `adminapi.New(service *controlplane.Service, roles map[string]string, limits controlplane.Limits) (http.Handler,error)` and `Principal(state tls.ConnectionState) (string,error)`. TLS server configuration is composed in Task 16 and requires verified client certificates.

- [ ] **Step 1: Add the failing test seed and the specified scenario matrix.**

```go
func TestPrincipalDoesNotTrustPeerCertificateAlone(t *testing.T) {
    uri, err := url.Parse("urn:test:operator"); if err != nil { t.Fatal(err) }
    state := tls.ConnectionState{PeerCertificates:[]*x509.Certificate{{URIs:[]*url.URL{uri}}}}
    if _, err := Principal(state); err == nil { t.Fatal("accepted unverified certificate") }
}
```

Use real TLS handshakes for Reader, Operator, unmapped URI, DP URI without role, wrong CA, absent certificate, multiple URIs and oversized URI. Full HTTP matrix: JSON revisions, normalized CRUD replay, absent/duplicate idempotency header, query/body shape, unsupported methods/media/compression, depth/body/header bounds, decoder/reader saturation, pagination and all status families. Verify unauthorized requests invoke no resolver/store mutation. Header limit tests allow net/http framing overhead and document actual enforcement, rather than claiming MaxHeaderBytes alone is a byte-exact transport cap.

- [ ] **Step 2: Run the focused command before implementing.**

```text
go test ./internal/adminapi -count=1
```

Expected: FAIL for the missing behavior, not an unrelated fixture failure.

- [ ] **Step 3: Implement the behavior below.**

Register exactly the spec's seven method/path combinations, including GET and PUT/DELETE resource paths. Reader gets declarative reads; Operator gets reads/mutations/rollback. Require VerifiedChains and exactly one <=256-byte URI SAN; never trust headers, CN or implicit DP role.

```go
mux.HandleFunc("POST /v1/transactions", transactionHandler)
mux.HandleFunc("POST /v1/rollbacks", rollbackHandler)
mux.HandleFunc("PUT /v1/resources/{kind}/{id}", putHandler)
mux.HandleFunc("DELETE /v1/resources/{kind}/{id}", deleteHandler)
mux.HandleFunc("GET /v1/resources/{kind}", listHandler)
mux.HandleFunc("GET /v1/resources/{kind}/{id}", getHandler)
mux.HandleFunc("GET /v1/revisions", revisionsHandler)
```

These handler variables are local closures over the service/limits. Enforce 4 decoder/16 reader slots before maximum body allocation; discard raw body after decode. Apply 1 MiB MaxBytesReader plus five-second body deadline, strict Content-Type/no Content-Encoding, query names/duplicates, one valid Idempotency-Key and no DELETE body. PUT body is only spec; expected revision belongs in query. Normalize adapters to Action.

Return immutable success200 and replay header. Map every spec section13 code, deterministic bounded validation field, server-generated request ID, 405 Allow and 429 Retry-After:1. Do not echo underlying storage/file/crypto errors. Bound response size as well as count; no artifact/material endpoint.

- [ ] **Step 4: Format and rerun the focused command.** Expected: PASS for all named cases. Run the relevant existing regression packages when changing shared behavior.

- [ ] **Step 5: Review the diff, run `git diff --check`, stage explicit paths and commit.** Subject: `feat: expose authenticated configuration administration`.

### Task 16: Thin CP command, bounded observability and operations

**Files:** Create `cmd/gateway-cp/main.go`, `main_test.go`; create `internal/controlplane/observability.go`, `observability_test.go`; create `internal/cptest/process.go`, `test/integration/phase4a_process_test.go`, `docs/operations/phase-4a-runbook.md`; modify `README.md` and `.github/workflows/ci.yml`.

**Interfaces:** Produces the `gateway-cp -config` entry point. Define `Event` with bounded `Code, Principal, RequestID, Action string` and `Revision resource.Revision`; `EventSink` interface `TryEmit(Event) bool`, `Close(context.Context) error`. `NewEventSink(writer io.Writer, capacity int) EventSink` wraps a bounded nonblocking queue. Define `cptest.StartCP(t *testing.T, configPath string) *Process`, `Process.Stop(t *testing.T)` and `Process.Wait(t *testing.T) error` with bounded cleanup.

- [ ] **Step 1: Add the failing test seed and the specified scenario matrix.**

```go
func TestEventSinkDropsWithoutBlocking(t *testing.T) {
    sink := NewEventSink(io.Discard, 1)
    defer sink.Close(t.Context())
    event := Event{Code:"REVISION_CONFLICT",RequestID:"server-id"}
    for i := 0; i < 10000; i++ { sink.TryEmit(event) }
}
```

Extend seed with a deliberately blocked writer and failing writer, bounded join, event-size truncation/redaction, bounded label sets and counted drops. Process tests cover structured bad-bootstrap failure, empty ready CP, SIGTERM/drain, etcd outage alive/notready, read-only mounts and no secret strings in logs. A slow log writer must not leak unbounded goroutines; if an arbitrary writer cannot be interrupted, isolate a single bounded writer and define process shutdown behavior explicitly.

- [ ] **Step 2: Run the focused command before implementing.**

```text
go test ./internal/controlplane ./cmd/gateway-cp ./test/integration -run 'TestEvent|TestCP' -race -count=1
```

Expected: FAIL for the missing behavior, not an unrelated fixture failure.

- [ ] **Step 3: Implement the behavior below.**

Compose validated bootstrap, safe resolver, real etcd client/store, Service, Admin HTTPS and telemetry in main; keep behavior in internal packages. Main owns closure order and joins goroutines. Set Admin TLS ClientAuth RequireAndVerifyClientCert, separate trusted CA/identity from etcd credentials, bounded headers/timeouts and private listener defaults.

```text
signal -> reject new requests -> cancel queued/preparing work
-> drain service commit/reconcile within 20s
-> stop HTTP/listeners -> release pins/resolver/store
-> flush bounded event sink within remaining deadline
```

Add bounded counters/gauges for CP readiness, active/queued work, storage reservations, commit result codes and dropped logs. Use only fixed enums as labels; never principal, request ID, resource ID, revision or key fingerprint. Failed events <=4 KiB, queue<=1,000, drop-new and count failed writer output. A blocked sink cannot block mutations or shutdown indefinitely.

Runbook documents generated temporary keys, bootstrap fields, initial complete transaction, expected_revision/idempotency retries, explicit refresh, rollback, read cursors, rotation retaining old keys, quota/compaction/defrag, missing-key/corrupt-current recovery and backup-lineage fresh write key requirement. Explain that restoring an older head while live DPs have higher revisions is outside ordinary rollback; keep publication stopped pending 4B/4C disaster recovery.

Add a CP image build using the existing Dockerfile COMMAND argument. Add a separate mandatory Linux real-etcd CI step/job with GATEWAY_TEST_ETCD=1; preserve every existing CI gate. Add native Windows safe-opener test coverage if Windows CP support is declared; otherwise document unsupported platform and fail startup there until acceptance passes.

- [ ] **Step 4: Format and rerun the focused command.** Expected: PASS for all named cases. Run the relevant existing regression packages when changing shared behavior.

- [ ] **Step 5: Review the diff, run `git diff --check`, stage explicit paths and commit.** Subject: `feat: run and operate the configuration control plane`.

### Task 17: Integrated durability and bounded-resource acceptance

**Files:** Create `test/integration/phase4a_acceptance_test.go`, `phase4a_failure_test.go`, `phase4a_limits_test.go`; extend `internal/cpstore/reconcile_test.go`, `internal/cptest/etcd.go`, `process.go`; complete `docs/operations/phase-4a-runbook.md`.

**Interfaces:** Consumes real Admin/CP/store and test-only fault seams. Add local test helper `runCPAcceptance(t *testing.T, operations int)` that starts real etcd+CP with temporary keys, sends a complete graph transaction, restarts CP and compares read declarations and internal artifact identity; it must assert normal etcd server limits and final transaction serialized bounds.

- [ ] **Step 1: Add the failing test seed and the specified scenario matrix.**

```go
func TestCPThousandOperationsRemainAtomic(t *testing.T) {
    runCPAcceptance(t, 1000)
}
```

Required acceptance cases: Reader/Operator isolation; competing expected revisions; same-key changed content; before/after-commit cancellation; unknown commit blocks next; lease loss; stage failure after every chunk; late transaction versus abandonment CAS; repeated cleanup crash; pinned read/retention; replay/audit capacity; key-use count survives abandoned attempts; corrupt current/missing keys; expired current repair; startup with no state; failed logs; 1 MiB/1,000/16 MiB boundaries and repeated queue saturation. Limit workload sizes so all tests finish within CI deadlines; do not run the benchmark harness.

- [ ] **Step 2: Run the focused command before implementing.**

```text
go test ./internal/cpstore ./test/integration -run 'TestPublication|TestReconcile|TestCP' -race -count=1
```

Expected: FAIL for the missing behavior, not an unrelated fixture failure.

- [ ] **Step 3: Implement the behavior below.**

Build one 1,000-operation initial transaction within the 1 MiB HTTP body limit: minimum TLS/Route/Upstream graph plus additional distinct Route declarations. Resolve shared material once. Separately construct a valid near-16 MiB resolved snapshot using generated material/resources while staying within body and operation caps. Ensure encrypted artifact spans many chunks.

```text
commit revision1 -> change/remove mount files -> restart CP
-> identical committed declarations/material, no file reread
-> explicit refresh revision2 -> rollback source1 commits revision3
-> old request replay returns original result, even after source artifact GC
```

Capture success audit, head, manifest, identity record and actual chunk set after every injected failure. Use deterministic barriers for after-write/complete/publish/response-loss/owner-change/GC races. Real-etcd tests verify the protocol; memory fakes alone cannot satisfy acceptance. Test quota errors and network loss without deleting head or shortening replay windows.

Measure peak/steady heap, goroutines, open handles and storage counters across repeated bounded workloads; assert the known maximum active bodies/readers/preparations and that steady resource use returns to an agreed baseline tolerance. Record workload, platform and measurements in the runbook acceptance record; do not claim an RSS bound equal to summed payload limits or a throughput/latency benchmark.

- [ ] **Step 4: Format and rerun the focused command.** Expected: PASS for all named cases. Run the relevant existing regression packages when changing shared behavior.

- [ ] **Step 5: Review the diff, run `git diff --check`, stage explicit paths and commit.** Subject: `test: verify control plane durability and failure recovery`.

### Task 18: Full verification and Phase 4B handoff

**Files:** Update `docs/operations/phase-4a-runbook.md`, `README.md`, this plan's execution checkboxes, and the Phase 4 roadmap only after evidence exists. Preserve uncompleted 4B/4C status.

**Interfaces:** The deliverable for 4B is `Store.Acquire` -> verified `ReadHandle.Export` -> exact signed header/signature/snapshot bytes, with bounded pin lifetime and freshness handling. It is internal; no Admin artifact endpoint or DP network activation surface is added.

- [ ] Confirm the Phase 3D prerequisite record and every task's red/green evidence. Review imports to ensure production commands do not import `internal/cptest`, and CP validation never prepares a live upstream registry.
- [ ] Run the repository-required formatting, analyzers, tests and builds from the root on Go 1.26.5:

```text
gofmt -l .
staticcheck -tests=false ./...
revive -set_exit_status -config revive.toml -formatter default ./...
go vet ./...
go test ./... -count=1
go test ./... -race -count=1
go build ./cmd/...
docker build --build-arg COMMAND=gateway-dp -t gateway-go:ci .
docker build --build-arg COMMAND=gateway-cp -t gateway-cp:ci .
git diff --check
```

Expected: no formatting output, no diagnostics and every command exits zero. Analyzer versions remain Staticcheck 2026.1 and Revive v1.15.0 as in AGENTS.md.

- [ ] Run mandatory real-etcd checks with explicit enablement. PowerShell:

```powershell
$env:GATEWAY_TEST_ETCD = '1'
go test ./internal/cpstore ./test/integration -count=1
go test ./internal/cpstore ./test/integration -race -count=1
Remove-Item Env:GATEWAY_TEST_ETCD
```

CI uses a step-local `GATEWAY_TEST_ETCD: "1"` environment entry. Record the exact server/client versions, selected gRPC version, default server request/operation limits and image identity actually tested. Any skip in mandatory etcd scenarios fails acceptance. Never use an existing production etcd endpoint.

- [ ] Review spec coverage against the matrix below, inspect sanitized traces from ambiguous commit tests, and check material never appears in HTTP/log/audit/metric output. Compare normal/race standalone regression results with the Phase 3D baseline.
- [ ] Record actual checks/results, platform limitations and resource measurements. If Docker/race tooling/platform is unavailable, record that exact missing check and leave 4A acceptance incomplete; do not replace real-etcd evidence with fakes.
- [ ] Commit the verified documentation/status with `docs: record phase 4a acceptance and handoff`. Do not mark 4B/4C or fleet activation acceptance complete.

## Spec coverage and review checkpoints

| Approved spec section | Implementation tasks | Required review evidence |
| --- | --- | --- |
| 1–3: scope and package boundaries | 1–3, 16, 18 | Pure CP validation, unchanged DP behavior and explicit 4B seam |
| 4: bootstrap, mTLS authority and ownership | 7, 13, 15–16 | Strict bootstrap, URI policy, owner fencing on all writes and GC |
| 5: all managed kinds and inherited fields | 1–3, 11 | Exhaustive DTO/default/presence parity, final graph/TLS/plugin validation |
| 6: API/revisions/read consistency | 1–2, 14–15 | Full route/status matrix, canonical uint64, pinned pages and no material reads |
| 7: identity, cancellation, replay | 2, 9, 12 | Normalized identity, replay before revision, uncertainty fence, bounded shutdown |
| 8: refresh and rollback | 4, 11, 17 | Exact material reuse, one read per put ID, expired/pruned rollback rejection |
| 9: artifact format and cryptography | 5–6, 8, 10 | Golden bytes, binding/corruption matrix, key-use reservations and rotation reads |
| 10: etcd publication/recovery | 7–10, 17 | Fixed-size final Txn, real-etcd delayed-commit races, no orphan promotion |
| 11: retention/audit/health | 8–10, 13, 16–17 | Separate budgets, pin/GC lifetime, audit atomicity, startup/outage repair |
| 12: every limit/deadline | 1, 4–10, 12–17 | Boundary tables, admission bounds, timed cancellation and measured lifecycle |
| 13: stable error mappings | 1, 9, 12, 14–16 | Status/code table, deterministic safe fields and no raw error disclosure |
| 14: verification and exit criteria | 17–18 | Full CI + real etcd + resource bounds, no activation-latency claim |
| 15–16: handoff/references | 18 | Phase 3D1 gate, exact pins, usable bounded internal export |

Review milestones are: Tasks 1–6 (portable pure contracts), Tasks 7–10 (durability protocol), Tasks 11–15 (application/API behavior), Tasks 16–18 (process/acceptance). A milestone is not permission to skip its individual tests. Do not parallelize store state-machine changes that depend on unreviewed contracts.

## Operational details that must remain explicit

- File resolution safety is an acceptance condition, not a string-prefix check. Internal symlink support is optional and must fail closed where containment cannot be proved.
- Memory/storage limits include copied and encoded bytes. Current artifact, history, pins, stages and auxiliary metadata are separate charged categories; a deadline does not free still-used memory.
- Wall-clock timestamps do not order revisions. Tests must include clock movement around retention checks; never expire records earlier because of an administratively reduced future retention setting. Use monotonic time for in-process deadlines.
- Ambiguous publication cannot be declared failed from an absent read. Only committed replay or a successful fenced abandonment establishes the outcome.
- Prefix lease exclusion is not multi-CP HA. All consistent reads also require confirmed ownership and reconciled head.
- CP storage encryption does not hide every metadata/access pattern. Existing keys needed by retained artifacts/backups stay mounted; no automatic re-encryption is delivered.
- Bootstrap key changes and backup restore must not reuse a rolled-back wrapping counter lineage. The runbook requires a fresh active KEK for that restoration case and old keys for decryption.
- An intact expired current artifact can be loaded for repair, while every new candidate must pass current-time material validation.
- A committed Admin response proves durability, not any DP's activation or persistence.

## Execution handoff

The plan is ready for task-by-task execution after the Phase 3D1 implementation-and-CI gate is met. At that point the execution options are (1) subagent-driven work with review between tasks, or (2) inline execution using executing-plans and milestone checkpoints. Select an execution mode when starting implementation; writing this plan does not start it.

Planning verification is documentation-only. Code blocks and future test commands are not claims that code exists or tests have passed.

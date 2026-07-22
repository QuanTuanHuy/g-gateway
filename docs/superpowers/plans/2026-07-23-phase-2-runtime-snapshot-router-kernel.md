# Phase 2 Runtime Snapshot and Router Kernel Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the Phase 1 single-route handler with a versioned immutable runtime snapshot, deterministic 100,000-route router, compiled plugin pipeline, and reproducible Phase 2 scalability benchmark while preserving Phase 1 proxy correctness.

**Architecture:** Decode `gateway/v1alpha1` or strict `gateway/v1alpha2` into canonical resources, create a fixed bootstrap upstream table, then compile routes, predicates, resolved references, and plugin chains into a complete shadow `RuntimeSnapshot`. A serialized `Manager.Apply` publishes the snapshot with `atomic.Pointer`; each request loads it once and carries the selected compiled route through request plugins, the existing `httputil.ReverseProxy`, and response plugins.

**Tech Stack:** Go 1.26.0 with toolchain Go 1.26.5, `net/http`, `net/http/httputil`, `sync/atomic`, `go.yaml.in/yaml/v3`, Prometheus client, Go unit/property/fuzz/race/benchmark tooling, PowerShell, Docker Compose, wrk, h2load, and the pinned local APISIX source checkout.

## Global Constraints

- Work on the current `codex/phase2-runtime-snapshot-router-kernel-design` branch; do not create a Git worktree.
- Use TDD for every behavior change: observe the focused test fail, implement the minimum behavior, observe it pass, then run the package regression suite.
- Keep `net/http` and `httputil.ReverseProxy`; do not introduce a new HTTP engine or a new Go module dependency.
- `RuntimeSnapshot` is immutable after publication; the request path may load one atomic pointer but must not take a global mutex.
- No config parsing, resource reference resolution, plugin merge, or plugin sorting is allowed on the request path.
- `Apply` accepts full resources and monotonically increasing revisions. A failed or stale apply leaves the last-known-good snapshot active.
- Upstream IDs, endpoints, and transport settings are fixed at bootstrap. A later resource set that changes them fails with `UPSTREAM_SET_IMMUTABLE`.
- Phase 2 routing supports exact, prefix, parameter, and catch-all paths; exact, one-label wildcard, and hostless hosts; it does not support regex.
- Preserve Phase 1 streaming, trailers, cancellation, forwarding headers, body limits, TLS/ALPN, stable errors, graceful shutdown, and raw-query forwarding.
- Preserve strict `gateway/v1alpha1` compatibility and add strict `gateway/v1alpha2`; unknown fields remain errors in both schemas.
- The standard 100,000-route dataset and benchmark metadata must be deterministic and checksummed.
- Absolute compile/memory reference profile: Linux x86_64, 8 dedicated logical CPUs, 16 GiB memory limit, Go 1.26.5, swap disabled, with exact environment metadata recorded.
- Do not change the Phase 1 smoke evidence. New Phase 2 results go to a distinct ignored result directory and report.

## File and Dependency Map

The dependency direction is intentionally one-way:

    model
      -> config
      -> requestctx
      -> router
      -> plugin
      -> upstream
      -> runtime
      -> proxy
      -> telemetry/gateway/cmd

Files to create or materially extend:

- `internal/model/resources.go`: canonical Route, Service, predicates, plugins, and deep-copy helpers.
- `internal/config/wire_v1alpha2.go`: strict v1alpha2 wire structs and conversion.
- `internal/config/load.go`, `types.go`, `validate.go`: version dispatch, v1 compatibility, and common validation.
- `internal/upstream/table.go`: fixed bootstrap runtime table and immutable-set validation.
- `internal/requestctx/context.go`: typed request metadata, path parameters, request ID, and private context bridge.
- `internal/router/pattern.go`: host/path parsing and specificity metadata.
- `internal/router/predicate.go`, `query.go`: compiled predicate instructions and lazy query scanning.
- `internal/router/router.go`: immutable indexes, compilation, precedence, matching, and method handling.
- `internal/plugin/registry.go`, `chain.go`: plugin registration, inheritance, compile order, and hook contracts.
- `internal/plugin/request_id.go`, `header_rewrite.go`: two built-in plugins.
- `internal/runtime/errors.go`, `snapshot.go`, `builder.go`, `manager.go`: resolved compiled state and atomic activation.
- `internal/proxy/handler.go`, `errors.go`: snapshot-driven route selection and plugin-aware proxy flow.
- `internal/telemetry/telemetry.go`: dynamic route labels and snapshot build metrics.
- `internal/gateway/gateway.go`: bootstrap table/snapshot wiring and internal `Apply` entry point.
- `internal/benchdataset/generator.go`: deterministic standard dataset and Go/APISIX renderers.
- `cmd/bench-dataset/main.go`: dataset CLI used by the benchmark harness.
- `bench/run-phase2.ps1`, `bench/phase2-scenarios.yaml`, `bench/compose.phase2.yaml`: isolated Phase 2 runner/profile.
- `internal/phase2benchreport/report.go`, `cmd/phase2-bench-report/main.go`: relative scalability and comparative report.
- `docs/operations/phase-2-runbook.md`, `docs/benchmarks/phase-2-current-status.md`: operator workflow and evidence snapshot.

---

### Task 1: Expand the Canonical Resource Model Without Breaking Phase 1

**Files:**
- Modify: `internal/model/resources.go`
- Create: `internal/model/resources_test.go`

**Interfaces:**
- Produces: `model.ResourceSet`, `model.Route`, `model.Service`, `model.Predicate`, `model.PluginAttachment`, and `model.CloneResourceSet`.
- Compatibility: existing `Route.Match.Path`, `Route.Match.Methods`, `Route.UpstreamRef`, `Upstream`, and `TransportConfig` field names remain valid.

- [ ] **Step 1: Write failing deep-copy and canonical-shape tests**

```go
func TestCloneResourceSetDoesNotAliasInput(t *testing.T) {
	raw := json.RawMessage(`{"header_name":"X-Trace-ID"}`)
	in := ResourceSet{
		Routes: []Route{{
			ID: "users", Priority: 10,
			Match: RouteMatch{
				Hosts: []string{"api.example.com"}, Path: "/users/{id}", Methods: []string{"GET"},
				Headers: []Predicate{{Name: "X-Tenant", Operator: PredicateEquals, Values: []string{"acme"}}},
			},
			ServiceRef: "users-service",
			Plugins: []PluginAttachment{{Name: "request-id", Enabled: true, RawConfig: raw}},
		}},
		Services: []Service{{ID: "users-service", UpstreamRef: "users-upstream"}},
		Upstreams: []Upstream{{ID: "users-upstream", Endpoints: []string{"http://upstream:8080"}}},
	}

	got := CloneResourceSet(in)
	in.Routes[0].Match.Hosts[0] = "mutated.example.com"
	in.Routes[0].Match.Headers[0].Values[0] = "mutated"
	in.Routes[0].Plugins[0].RawConfig[0] = 'x'
	in.Upstreams[0].Endpoints[0] = "http://mutated:8080"

	if got.Routes[0].Match.Hosts[0] != "api.example.com" || got.Routes[0].Match.Headers[0].Values[0] != "acme" {
		t.Fatalf("CloneResourceSet() aliases route input: %+v", got.Routes[0])
	}
	if string(got.Routes[0].Plugins[0].RawConfig) != `{"header_name":"X-Trace-ID"}` {
		t.Fatalf("plugin config = %q", got.Routes[0].Plugins[0].RawConfig)
	}
	if got.Upstreams[0].Endpoints[0] != "http://upstream:8080" {
		t.Fatalf("upstream endpoints = %v", got.Upstreams[0].Endpoints)
	}
}
```

- [ ] **Step 2: Run the focused test and verify the model is incomplete**

Run: `go test ./internal/model -run TestCloneResourceSetDoesNotAliasInput -count=1`

Expected: FAIL to compile because `Service`, `Predicate`, `PluginAttachment`, or `CloneResourceSet` is undefined.

- [ ] **Step 3: Add canonical types and an explicit deep-copy helper**

```go
type PredicateOperator string

const (
	PredicateExists    PredicateOperator = "exists"
	PredicateNotExists PredicateOperator = "not_exists"
	PredicateEquals    PredicateOperator = "equals"
	PredicateNotEquals PredicateOperator = "not_equals"
	PredicateOneOf     PredicateOperator = "one_of"
)

type ResourceSet struct {
	Routes    []Route
	Services  []Service
	Upstreams []Upstream
}

type Route struct {
	ID          string
	Priority    int
	Match       RouteMatch
	ServiceRef  string
	UpstreamRef string
	Plugins     []PluginAttachment
}

type RouteMatch struct {
	Hosts   []string
	Path    string
	Methods []string
	Headers []Predicate
	Queries []Predicate
}

type Predicate struct {
	Name     string
	Operator PredicateOperator
	Values   []string
}

type Service struct {
	ID          string
	UpstreamRef string
	Plugins     []PluginAttachment
}

type PluginAttachment struct {
	Name      string
	Enabled   bool
	RawConfig json.RawMessage
}
```

Implement `CloneResourceSet` by allocating every route/service/upstream slice, every nested string slice, every predicate value slice, every plugin slice, and every `RawConfig` byte slice. Keep `TransportConfig` copied by value.

- [ ] **Step 4: Run model and full regression tests**

Run: `gofmt -w internal/model/resources.go internal/model/resources_test.go`

Run: `go test ./internal/model -count=1`

Expected: PASS.

Run: `go test ./... -count=1`

Expected: PASS; Phase 1 call sites still compile because their existing fields were preserved.

- [ ] **Step 5: Commit the canonical model**

```powershell
git add internal/model/resources.go internal/model/resources_test.go
git commit -m "feat: add canonical phase 2 resource model"
```

### Task 2: Add Strict v1alpha2 Decoding and Preserve v1alpha1

**Files:**
- Modify: `internal/config/load.go`
- Modify: `internal/config/types.go`
- Modify: `internal/config/validate.go`
- Modify: `internal/config/load_test.go`
- Create: `internal/config/wire_v1alpha2.go`
- Create: `configs/phase2.yaml`

**Interfaces:**
- Consumes: canonical model from Task 1.
- Produces: unchanged `config.Load(path) (BootstrapConfig, model.ResourceSet, error)` and `config.Decode(io.Reader)` with version dispatch.
- Schema: `gateway/v1alpha1` remains exact Phase 1 behavior; `gateway/v1alpha2` adds services, hosts, priority, predicates, and plugin attachments.

- [ ] **Step 1: Add failing version compatibility and strict v1alpha2 tests**

Add tests that decode the existing `validDocument` unchanged, then decode this v1alpha2 resource fragment using generated TLS paths:

```yaml
api_version: gateway/v1alpha2
routes:
  - id: users
    priority: 100
    match:
      hosts: [api.example.com, "*.example.net"]
      path: /users/{id}
      methods: [GET]
      headers:
        - name: X-Tenant
          operator: one_of
          values: [acme, globex]
      queries:
        - name: verbose
          operator: equals
          values: ["true"]
    service_ref: users-service
    plugins:
      - name: request-id
        enabled: true
        config:
          header_name: X-Request-ID
services:
  - id: users-service
    upstream_ref: baseline
    plugins:
      - name: header-rewrite
        enabled: true
        config:
          request:
            set:
              X-Service: users
```

Assert priority, hosts, predicates, ServiceRef, service reference, plugin enabled default, and JSON `RawConfig`. Add table cases proving:

- a v1alpha1 document containing `services` fails as unknown-field input;
- a v1alpha2 document containing an unknown route field fails;
- duplicate route/service/upstream IDs fail;
- a route with both `service_ref` and `upstream_ref` fails;
- a route with neither target fails;
- unresolved Service and Upstream references fail;
- invalid predicate operators/value arity fail.

- [ ] **Step 2: Run config tests and observe v1alpha2 rejection**

Run: `go test ./internal/config -run 'TestDecodeValidV1Alpha2|TestDecodeV1Alpha2Rejects' -count=1`

Expected: FAIL because the decoder currently accepts only `gateway/v1alpha1`.

- [ ] **Step 3: Dispatch strict decoding by API version**

Read the input once, decode only the version header, then strict-decode the selected wire type:

```go
type versionHeader struct {
	APIVersion string `yaml:"api_version"`
}

func Decode(r io.Reader) (BootstrapConfig, model.ResourceSet, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return BootstrapConfig{}, model.ResourceSet{}, fmt.Errorf("read config: %w", err)
	}
	var header versionHeader
	if err := yaml.Unmarshal(data, &header); err != nil {
		return BootstrapConfig{}, model.ResourceSet{}, fmt.Errorf("decode config header: %w", err)
	}
	switch header.APIVersion {
	case apiVersionV1Alpha1:
		var wire document
		if err := decodeStrict(data, &wire); err != nil { return BootstrapConfig{}, model.ResourceSet{}, err }
		return convertAndValidateV1(wire)
	case apiVersionV1Alpha2:
		var wire documentV2
		if err := decodeStrict(data, &wire); err != nil { return BootstrapConfig{}, model.ResourceSet{}, err }
		return convertAndValidateV2(wire)
	default:
		return BootstrapConfig{}, model.ResourceSet{}, fmt.Errorf("api_version: unsupported %q", header.APIVersion)
	}
}
```

`decodeStrict` must use `yaml.Decoder.KnownFields(true)` and perform the existing second-decode EOF check so multiple YAML documents remain forbidden.

- [ ] **Step 4: Define the v1alpha2 wire structs and conversion**

Use explicit wire structs rather than embedding the v1 type. Represent plugin `config` as `map[string]any`, marshal it with `encoding/json`, and deep-copy the bytes into `model.PluginAttachment.RawConfig`. Use `*bool` in the wire type so omitted `enabled` becomes `true` while explicit `false` remains false.

```go
type documentV2 struct {
	APIVersion string               `yaml:"api_version"`
	Listeners  listenersDocument    `yaml:"listeners"`
	Server     serverDocument       `yaml:"server"`
	Telemetry  telemetryDocument    `yaml:"telemetry"`
	Routes     []routeDocumentV2    `yaml:"routes"`
	Services   []serviceDocumentV2  `yaml:"services"`
	Upstreams  []upstreamDocument   `yaml:"upstreams"`
}

type routeDocumentV2 struct {
	ID          string             `yaml:"id"`
	Priority    int                `yaml:"priority"`
	Match       routeMatchDocumentV2 `yaml:"match"`
	ServiceRef  string             `yaml:"service_ref"`
	UpstreamRef string             `yaml:"upstream_ref"`
	Plugins     []pluginDocumentV2 `yaml:"plugins"`
}

type routeMatchDocumentV2 struct {
	Hosts   []string              `yaml:"hosts"`
	Path    string                `yaml:"path"`
	Methods []string              `yaml:"methods"`
	Headers []predicateDocumentV2 `yaml:"headers"`
	Queries []predicateDocumentV2 `yaml:"queries"`
}

type predicateDocumentV2 struct {
	Name     string   `yaml:"name"`
	Operator string   `yaml:"operator"`
	Values   []string `yaml:"values"`
}

type serviceDocumentV2 struct {
	ID          string             `yaml:"id"`
	UpstreamRef string             `yaml:"upstream_ref"`
	Plugins     []pluginDocumentV2 `yaml:"plugins"`
}

type pluginDocumentV2 struct {
	Name    string         `yaml:"name"`
	Enabled *bool          `yaml:"enabled"`
	Config  map[string]any `yaml:"config"`
}

func convertPlugin(raw pluginDocumentV2) (model.PluginAttachment, error) {
	enabled := true
	if raw.Enabled != nil { enabled = *raw.Enabled }
	encoded, err := json.Marshal(raw.Config)
	if err != nil { return model.PluginAttachment{}, fmt.Errorf("plugin %q config: %w", raw.Name, err) }
	return model.PluginAttachment{Name: raw.Name, Enabled: enabled, RawConfig: encoded}, nil
}
```

Keep listener/server/telemetry/upstream conversion shared. Keep Phase 1's exactly-one-route/exactly-one-upstream restrictions only in `validateV1Resources`; v1alpha2 allows multiple routes/services/upstreams but still requires at least one route and upstream.

- [ ] **Step 5: Add a runnable strict v1alpha2 example**

Create `configs/phase2.yaml` with the same listener/server/upstream settings as `configs/phase1.yaml`, one direct route, one Service route, request-id, and request/response header rewrite. Use only valid static example values and `/certs/server.crt` paths so the container command can consume it.

- [ ] **Step 6: Run config and full regression suites**

Run: `gofmt -w internal/config/*.go`

Run: `go test ./internal/config -count=1`

Expected: PASS for v1alpha1 and v1alpha2 cases.

Run: `go test ./... -count=1`

Expected: PASS.

- [ ] **Step 7: Commit versioned configuration support**

```powershell
git add internal/config configs/phase2.yaml
git commit -m "feat: add strict phase 2 configuration"
```

### Task 3: Introduce the Fixed Upstream Runtime Table

**Files:**
- Create: `internal/upstream/table.go`
- Create: `internal/upstream/table_test.go`
- Modify: `internal/upstream/runtime.go`

**Interfaces:**
- Consumes: `[]model.Upstream` from Task 1 and existing `upstream.New`.
- Produces: `upstream.NewTable`, `(*Table).Get`, `(*Table).ValidateResources`, and `(*Table).CloseIdleConnections`.
- Runtime builder in Task 12 relies on exact `Get(id string) (*Runtime, bool)` and `ValidateResources([]model.Upstream) error` signatures.

- [ ] **Step 1: Write failing construction, cleanup, lookup, and immutability tests**

```go
func TestTableRejectsChangedUpstreamSet(t *testing.T) {
	resources := []model.Upstream{testResource("http://one:8080")}
	table, err := NewTable(resources)
	if err != nil { t.Fatal(err) }
	defer table.CloseIdleConnections()

	if runtime, ok := table.Get("baseline"); !ok || runtime.Target().String() != "http://one:8080" {
		t.Fatalf("Get(baseline) = %v, %v", runtime, ok)
	}
	changed := model.CloneResourceSet(model.ResourceSet{Upstreams: resources}).Upstreams
	changed[0].Endpoints[0] = "http://two:8080"
	err = table.ValidateResources(changed)
	if err == nil || !strings.Contains(err.Error(), "UPSTREAM_SET_IMMUTABLE") {
		t.Fatalf("ValidateResources() error = %v", err)
	}
}
```

Also test duplicate IDs, missing IDs, reordered-but-equal input, and partial construction failure. Inject a package-private `newRuntime` function variable in the test only if needed to prove already-created runtimes are closed when a later creation fails.

- [ ] **Step 2: Run the focused test and verify the table is absent**

Run: `go test ./internal/upstream -run TestTable -count=1`

Expected: FAIL to compile because `NewTable` is undefined.

- [ ] **Step 3: Implement canonical fixed-table ownership**

```go
type Table struct {
	byID      map[string]*Runtime
	resources []model.Upstream
}

func NewTable(resources []model.Upstream) (*Table, error)
func (t *Table) Get(id string) (*Runtime, bool)
func (t *Table) ValidateResources(resources []model.Upstream) error
func (t *Table) CloseIdleConnections()
```

Sort cloned canonical resources by ID before storing/comparing so input order is irrelevant. Compare all endpoint and transport fields. `ValidateResources` returns an error whose text starts with `UPSTREAM_SET_IMMUTABLE:` for additions, removals, or changes. `NewTable` closes every runtime it already created if any later resource fails.

- [ ] **Step 4: Run upstream package and race tests**

Run: `gofmt -w internal/upstream/table.go internal/upstream/table_test.go`

Run: `go test ./internal/upstream -count=1`

Expected: PASS.

Run: `go test ./internal/upstream -race -count=1`

Expected: PASS.

- [ ] **Step 5: Commit fixed upstream ownership**

```powershell
git add internal/upstream
git commit -m "feat: add fixed upstream runtime table"
```

### Task 4: Add the Typed Request Context Bridge

**Files:**
- Create: `internal/requestctx/context.go`
- Create: `internal/requestctx/context_test.go`

**Interfaces:**
- Produces: `requestctx.Attach`, `requestctx.Middleware`, `requestctx.From`, `requestctx.Context`, typed metadata pointers, parameter spans, response state, and indexed scratch slots.
- Plugin and proxy tasks rely on one private `context.Context` value carrying one mutable request-lifetime object.

- [ ] **Step 1: Write failing one-value bridge and reset-isolation tests**

```go
func TestAttachCarriesOneTypedContext(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "http://gateway/users/42", nil)
	request, state := Attach(request, 2)
	state.Revision = 7
	state.Route = &RouteMeta{ID: "users"}
	state.Scratch[1] = "plugin-value"

	got, ok := From(request.Context())
	if !ok || got != state || got.Revision != 7 || got.Route.ID != "users" {
		t.Fatalf("From() = %+v, %v", got, ok)
	}
	if got.Scratch[1] != "plugin-value" || len(got.Scratch) != 2 {
		t.Fatalf("scratch = %#v", got.Scratch)
	}
}
```

Add a test proving `Attach` returns a distinct Context and scratch backing array for two requests. Add test implementations of `SnapshotRef` and `RuntimeRoute` and prove both typed references survive the bridge.

- [ ] **Step 2: Run the package test and verify the package is absent**

Run: `go test ./internal/requestctx -count=1`

Expected: FAIL because the package or `Attach` is undefined.

- [ ] **Step 3: Implement the typed request-lifetime state**

```go
type RouteMeta struct{ ID string }
type ServiceMeta struct{ ID string }
type UpstreamMeta struct{ ID string }

type ParamSpan struct {
	Name  string
	Start int
	End   int
}

type SnapshotRef interface {
	Revision() uint64
}

type RuntimeRoute interface {
	Target() *url.URL
	RoundTrip(*http.Request) (*http.Response, error)
	RunResponse(*Context, *http.Response) error
}

type Context struct {
	Snapshot      SnapshotRef
	Runtime       RuntimeRoute
	Revision      uint64
	Route         *RouteMeta
	Service       *ServiceMeta
	Upstream      *UpstreamMeta
	RequestID     string
	Path          string
	Params        []ParamSpan
	Scratch       []any
	ResponseCode  int
	ResponseError string
}

type contextKey struct{}

func Attach(request *http.Request, scratchSlots int) (*http.Request, *Context) {
	state := &Context{}
	if scratchSlots > 0 { state.Scratch = make([]any, scratchSlots) }
	return request.WithContext(context.WithValue(request.Context(), contextKey{}, state)), state
}

func From(ctx context.Context) (*Context, bool) {
	state, ok := ctx.Value(contextKey{}).(*Context)
	return state, ok
}

func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		request, _ = Attach(request, 0)
		next.ServeHTTP(response, request)
	})
}

func (c *Context) AllocateScratch(slots int) {
	if slots > 0 { c.Scratch = make([]any, slots) }
}
```

Do not expose the key and do not add per-field context helpers. Scratch uses numeric indexes assigned by the plugin compiler; it is not a string-keyed communication map. Add a middleware test proving the same Context pointer is visible to a wrapper both during and after `next.ServeHTTP`, which is required by dynamic telemetry.

- [ ] **Step 4: Format and run package tests**

Run: `gofmt -w internal/requestctx/context.go internal/requestctx/context_test.go`

Run: `go test ./internal/requestctx -count=1`

Expected: PASS.

- [ ] **Step 5: Commit request context primitives**

```powershell
git add internal/requestctx
git commit -m "feat: add typed gateway request context"
```

### Task 5: Compile Host and Path Patterns

**Files:**
- Create: `internal/router/pattern.go`
- Create: `internal/router/pattern_test.go`

**Interfaces:**
- Consumes: route host/path strings from `model.RouteMatch`.
- Produces: `NormalizeRequestHost`, `compileHostPattern`, `compilePathPattern`, and immutable specificity metadata used by Task 7.

- [ ] **Step 1: Write failing host and path semantics tests**

Use table tests covering:

```go
func TestCompilePathPatternSemantics(t *testing.T) {
	tests := []struct {
		pattern string
		path    string
		match   bool
		params  map[string]string
	}{
		{pattern: "/users", path: "/users", match: true},
		{pattern: "/users", path: "/users/", match: false},
		{pattern: "/api/*", path: "/api", match: false},
		{pattern: "/api/*", path: "/api/", match: true},
		{pattern: "/api/*", path: "/api/v1/users", match: true},
		{pattern: "/users/{id}", path: "/users/42", match: true, params: map[string]string{"id": "42"}},
		{pattern: "/users/{id}", path: "/users/", match: false},
		{pattern: "/assets/{*path}", path: "/assets", match: false},
		{pattern: "/assets/{*path}", path: "/assets/", match: true, params: map[string]string{"path": ""}},
		{pattern: "/assets/{*path}", path: "/assets/a/b", match: true, params: map[string]string{"path": "a/b"}},
	}
	for _, tt := range tests {
		t.Run(tt.pattern+"_"+tt.path, func(t *testing.T) {
			compiled, err := compilePathPattern(tt.pattern)
			if err != nil { t.Fatal(err) }
			got, params := compiled.match(tt.path)
			if got != tt.match { t.Fatalf("match = %v, want %v", got, tt.match) }
			if !reflect.DeepEqual(materialize(tt.path, params), tt.params) { t.Fatalf("params mismatch") }
		})
	}
}
```

Add rejection cases for relative path, query/fragment, empty or duplicate parameter name, non-final catch-all, braces in static segments, and mixed `*` syntax. Add host tests for lowercase, port removal, one trailing dot, exact match, `*.example.com`, wildcard apex rejection, and multi-label rejection.

- [ ] **Step 2: Run pattern tests and observe missing compiler failures**

Run: `go test ./internal/router -run 'TestCompilePathPattern|TestHost' -count=1`

Expected: FAIL to compile because pattern functions are absent.

- [ ] **Step 3: Implement parsed pattern types and specificity**

```go
type segmentKind uint8

const (
	segmentStatic segmentKind = iota
	segmentParameter
	segmentPrefix
	segmentCatchAll
)

type pathSegment struct {
	kind segmentKind
	text string
}

type pathSpecificity struct {
	staticSegments int
	kindRank       int
	segmentCount   int
	patternBytes   int
}

type compiledPathPattern struct {
	raw         string
	segments    []pathSegment
	specificity pathSpecificity
}
```

Parse once at compile time. Preserve path bytes and trailing slash semantics; do not call `path.Clean`. Parameter matching consumes exactly one non-empty slash-delimited segment. Prefix consumes the trailing slash and any remainder without capture. Catch-all captures the remainder after its required slash and permits an empty capture.

`NormalizeRequestHost` must handle DNS host with optional port through `net.SplitHostPort`, lowercase DNS names, and remove one trailing dot. Return an error for malformed bracketed authority or empty host. Route wildcard compilation stores only the lowercase suffix and verifies it has at least two DNS labels.

- [ ] **Step 4: Run pattern tests and package race tests**

Run: `gofmt -w internal/router/pattern.go internal/router/pattern_test.go`

Run: `go test ./internal/router -run 'TestCompilePathPattern|TestHost' -count=1`

Expected: PASS.

Run: `go test ./internal/router -race -count=1`

Expected: PASS.

- [ ] **Step 5: Commit pattern compilation**

```powershell
git add internal/router/pattern.go internal/router/pattern_test.go
git commit -m "feat: compile route host and path patterns"
```

### Task 6: Compile Methods and Header/Query Predicates

**Files:**
- Create: `internal/router/predicate.go`
- Create: `internal/router/query.go`
- Create: `internal/router/predicate_test.go`
- Create: `internal/router/query_test.go`

**Interfaces:**
- Consumes: `model.Predicate` and route method strings.
- Produces: immutable `methodSet`, `compiledPredicateSet`, `newEvaluation`, and sentinel `ErrInvalidQuery`.
- Task 7 calls `predicates.evaluate(*evaluation) (bool, error)` and reuses one lazy query parse across all candidates.

- [ ] **Step 1: Write failing method and predicate truth-table tests**

```go
func TestPredicateSemantics(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "http://gateway/path?tag=a&tag=b&empty=&plus=a+b", nil)
	request.Header["X-Role"] = []string{"reader", "writer"}
	tests := []struct {
		field string
		input model.Predicate
		want  bool
	}{
		{field: "header equals any", input: model.Predicate{Name: "x-role", Operator: model.PredicateEquals, Values: []string{"writer"}}, want: true},
		{field: "header not equals requires none", input: model.Predicate{Name: "X-Role", Operator: model.PredicateNotEquals, Values: []string{"admin"}}, want: true},
		{field: "header not equals rejects one match", input: model.Predicate{Name: "X-Role", Operator: model.PredicateNotEquals, Values: []string{"reader"}}, want: false},
		{field: "query one of", input: model.Predicate{Name: "tag", Operator: model.PredicateOneOf, Values: []string{"z", "b"}}, want: true},
		{field: "query empty exists", input: model.Predicate{Name: "empty", Operator: model.PredicateExists}, want: true},
		{field: "query plus decoded", input: model.Predicate{Name: "plus", Operator: model.PredicateEquals, Values: []string{"a b"}}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.field, func(t *testing.T) {
			var headers, queries []model.Predicate
			if strings.HasPrefix(tt.field, "header") {
				headers = []model.Predicate{tt.input}
			} else {
				queries = []model.Predicate{tt.input}
			}
			compiled, err := compilePredicates(headers, queries)
			if err != nil { t.Fatal(err) }
			got, err := compiled.evaluate(newEvaluation(request))
			if err != nil { t.Fatal(err) }
			if got != tt.want { t.Fatalf("evaluate() = %v, want %v", got, tt.want) }
		})
	}
}
```

Implement the test as two tables, one compiling header predicates and one compiling query predicates. Add missing-field cases for every operator, duplicate query keys, case-sensitive query keys/values, byte-sensitive header values, invalid `%` escape returning `ErrInvalidQuery`, `one_of` with no values failing compile, and `exists/not_exists` with values failing compile.

Add method tests proving standard methods use a mask, custom tokens match case-sensitively after configured uppercase canonicalization, and undeclared HEAD does not match GET.

- [ ] **Step 2: Run focused tests and verify compiler types are missing**

Run: `go test ./internal/router -run 'TestPredicate|TestQuery|TestMethod' -count=1`

Expected: FAIL to compile because predicate and method compilers are undefined.

- [ ] **Step 3: Implement flat predicate instructions and lazy query state**

```go
var ErrInvalidQuery = errors.New("invalid encoded query")

type fieldKind uint8
const (
	fieldHeader fieldKind = iota
	fieldQuery
)

type compiledPredicate struct {
	kind     fieldKind
	name     string
	operator model.PredicateOperator
	values   map[string]struct{}
}

type compiledPredicateSet []compiledPredicate

type evaluation struct {
	request    *http.Request
	queryOnce  sync.Once
	query      map[string][]string
	queryError error
}

func compilePredicates(headers, queries []model.Predicate) (compiledPredicateSet, error)
func (p compiledPredicateSet) evaluate(e *evaluation) (bool, error)
func newEvaluation(request *http.Request) *evaluation
func (e *evaluation) queryValues(name string) ([]string, bool, error)
```

The query parser scans `RawQuery` only on first query access, splits on `&` and the first `=`, calls `url.QueryUnescape` for key/value, preserves duplicates and empty values, and converts any decode error to `ErrInvalidQuery`. Header lookup uses canonicalized names and the request's existing header map.

Compile `equals`, `not_equals`, and `one_of` values into immutable lookup maps. Implement `not_equals` as false for missing fields and true only when no present value is in the compiled set.

- [ ] **Step 4: Implement standard method bits and custom fallback**

```go
type methodSet struct {
	standard uint16
	custom   map[string]struct{}
}

func compileMethods(methods []string) (methodSet, error)
func (m methodSet) contains(method string) bool
func (m methodSet) sortedValues() []string
```

Reserve stable bits for CONNECT, DELETE, GET, HEAD, OPTIONS, PATCH, POST, PUT, and TRACE. Validate every configured method with the existing HTTP-token rule moved to a shared package-private helper in `router`; canonicalize configured values to uppercase and reject duplicates after canonicalization.

- [ ] **Step 5: Run predicate, router, and race tests**

Run: `gofmt -w internal/router/predicate.go internal/router/query.go internal/router/predicate_test.go internal/router/query_test.go`

Run: `go test ./internal/router -count=1`

Expected: PASS.

Run: `go test ./internal/router -race -count=1`

Expected: PASS.

- [ ] **Step 6: Commit compiled match conditions**

```powershell
git add internal/router
git commit -m "feat: compile route methods and predicates"
```

### Task 7: Build the Immutable Router and Deterministic Precedence

**Files:**
- Create: `internal/router/router.go`
- Create: `internal/router/router_test.go`
- Create: `internal/router/precedence_test.go`

**Interfaces:**
- Consumes: model matches, path/host compilers from Task 5, predicates/methods from Task 6.
- Produces: `router.Compile([]RouteSpec) (*Router, error)` and `(*Router).Match(*http.Request) (Result, error)`.
- Runtime snapshot Task 12 stores the returned immutable Router and resolves `Result.RouteIndex` into its compiled route slice.

- [ ] **Step 1: Write failing multi-route and full precedence tests**

```go
type RouteSpec struct {
	Index    int
	ID       string
	Priority int
	Match    model.RouteMatch
}

func TestPrecedenceMatrix(t *testing.T) {
	routes := []RouteSpec{
		{Index: 0, ID: "hostless", Match: model.RouteMatch{Path: "/users/{id}", Methods: []string{"GET"}}},
		{Index: 1, ID: "wild", Match: model.RouteMatch{Hosts: []string{"*.example.com"}, Path: "/users/{id}", Methods: []string{"GET"}}},
		{Index: 2, ID: "exact-param", Match: model.RouteMatch{Hosts: []string{"api.example.com"}, Path: "/users/{id}", Methods: []string{"GET"}}},
		{Index: 3, ID: "exact-static", Match: model.RouteMatch{Hosts: []string{"api.example.com"}, Path: "/users/me", Methods: []string{"GET"}}},
		{Index: 4, ID: "priority-wins", Priority: 100, Match: model.RouteMatch{Path: "/users/{id}", Methods: []string{"GET"}}},
	}
	router, err := Compile(routes)
	if err != nil { t.Fatal(err) }
	result, err := router.Match(httptest.NewRequest(http.MethodGet, "http://api.example.com/users/me", nil))
	if err != nil { t.Fatal(err) }
	if !result.Found || result.RouteIndex != 4 { t.Fatalf("result = %+v", result) }
}
```

Add isolated table cases for each precedence layer: priority, host rank, static over parameter, parameter over prefix/catch-all, static segment count, pattern length, predicate count, and byte-order route ID. Add tests for multiple hosts, wildcard one-label behavior, declaration-order shuffle, duplicate priority+match-expression rejection, 404 result, 405 sorted/deduplicated Allow union, and malformed query propagation.

- [ ] **Step 2: Run router tests and observe missing `Compile`**

Run: `go test ./internal/router -run 'TestPrecedence|TestRouter' -count=1`

Expected: FAIL to compile because Router is not implemented.

- [ ] **Step 3: Implement immutable host indexes and segment radix nodes**

```go
type Router struct {
	exactHosts    map[string]*pathNode
	wildcardHosts map[string]*pathNode
	hostless      *pathNode
}

type pathNode struct {
	static   map[string]*pathNode
	parameter *pathNode
	terminal []candidate
	prefix   []candidate
	catchAll []candidate
	upper    precedenceKey
}

type candidate struct {
	routeIndex int
	routeID    string
	priority   int
	hostRank   int
	path       compiledPathPattern
	methods    methodSet
	predicates compiledPredicateSet
}

type Result struct {
	Found            bool
	MethodNotAllowed bool
	RouteIndex       int
	Params           []requestctx.ParamSpan
	Allow            []string
}
```

Create one path tree for every exact host, wildcard suffix, and the hostless bucket. Insert one candidate per declared host; hostless routes insert once. Sort every terminal/prefix/catch-all candidate slice by the total precedence key and compute the maximum subtree key bottom-up.

Reject exact duplicate normalized match expressions with equal priority. The canonical duplicate signature sorts hosts, methods, predicates, and predicate values before hashing/serializing, so source declaration order cannot bypass the check.

- [ ] **Step 4: Implement allocation-free static matching and bounded branch exploration**

`Match` normalizes request host, creates one lazy predicate evaluation, and considers matching exact, wildcard, and hostless trees. Traverse static and parameter edges, then prefix/catch-all terminals. Maintain the best fully matching candidate and prune a subtree only when its upper-bound precedence cannot beat the current best.

For every host/path/predicate candidate:

1. evaluate predicates using the shared lazy evaluation;
2. if method matches, compare total precedence and retain route/parameter spans;
3. if only method fails, union its configured methods into the 405 set;
4. after all candidates, return a found result, else 405 if the Allow set is non-empty, else a not-found result.

Do not allocate for a static route hit with no query predicates. Parameter/catch-all results may allocate their span slice; materialized strings are deferred to `requestctx` consumers.

- [ ] **Step 5: Run precedence, allocation, and full router tests**

Add:

```go
func TestStaticMatchAllocations(t *testing.T) {
	router := mustCompile(t, []RouteSpec{{Index: 0, ID: "static", Match: model.RouteMatch{Path: "/v1/users", Methods: []string{"GET"}}}})
	request := httptest.NewRequest(http.MethodGet, "http://gateway/v1/users", nil)
	if got := testing.AllocsPerRun(1000, func() {
		result, err := router.Match(request)
		if err != nil || !result.Found { panic("static route did not match") }
	}); got != 0 {
		t.Fatalf("allocations = %f, want 0", got)
	}
}
```

Run: `gofmt -w internal/router/router.go internal/router/router_test.go internal/router/precedence_test.go`

Run: `go test ./internal/router -count=1`

Expected: PASS, including zero allocations for the static/no-query case.

Run: `go test ./internal/router -race -count=1`

Expected: PASS.

- [ ] **Step 6: Commit the router kernel**

```powershell
git add internal/router
git commit -m "feat: add immutable deterministic router kernel"
```

### Task 8: Add the Reference Matcher, Property Tests, and Router Fuzzing

**Files:**
- Create: `internal/router/reference_test.go`
- Create: `internal/router/property_test.go`
- Create: `internal/router/fuzz_test.go`

**Interfaces:**
- Consumes: public router interfaces from Task 7.
- Produces: a test-only slow oracle, deterministic randomized test generator, committed fuzz seeds, and regression coverage for precedence/order invariants.

- [ ] **Step 1: Write a deliberately simple reference matcher**

The reference implementation must not call `Router.Match`. For each RouteSpec it:

1. compiles and directly tests every host/path expression;
2. evaluates method and predicates with straightforward `net/url` parsing;
3. constructs the same explicit precedence tuple;
4. stable-sorts all full matches and selects the first;
5. independently constructs the 405 Allow union when no method matches.

Expose only this test helper:

```go
func referenceMatch(t *testing.T, specs []RouteSpec, request *http.Request) Result
```

Keep it intentionally O(number of routes) so it is structurally independent from radix-tree behavior.

- [ ] **Step 2: Add a randomized property test that initially finds edge differences**

```go
func TestCompiledRouterMatchesReference(t *testing.T) {
	const cases = 10_000
	random := rand.New(rand.NewSource(20260723))
	for caseIndex := 0; caseIndex < cases; caseIndex++ {
		specs, request := generatedCase(t, random, caseIndex)
		compiled, err := Compile(specs)
		if err != nil { t.Fatalf("case %d compile: %v", caseIndex, err) }
		got, err := compiled.Match(request.Clone(request.Context()))
		if err != nil { t.Fatalf("case %d match: %v", caseIndex, err) }
		want := referenceMatch(t, specs, request.Clone(request.Context()))
		if !equivalentResult(got, want) {
			t.Fatalf("seed=20260723 case=%d got=%+v want=%+v specs=%+v request=%s", caseIndex, got, want, specs, request.URL)
		}
	}
}
```

Generate 1–50 unique routes per case across every host/path/method/predicate form. Include a second assertion that shuffles a cloned `specs` slice and obtains the same route ID, params, and Allow set.

- [ ] **Step 3: Run the property test and resolve every mismatch in production code**

Run: `go test ./internal/router -run TestCompiledRouterMatchesReference -count=1`

Expected before fixes: FAIL if the optimized router differs from the oracle on an overlap edge. For each failure, add the minimized case to `precedence_test.go`, fix `router.go`, and rerun until PASS. Do not weaken the oracle or skip a seed.

- [ ] **Step 4: Add focused fuzz targets with seed corpora**

```go
func FuzzPathPattern(f *testing.F) {
	for _, seed := range []string{"/", "/users/{id}", "/api/*", "/assets/{*path}", "/bad/{*x}/tail"} { f.Add(seed) }
	f.Fuzz(func(t *testing.T, pattern string) {
		compiled, err := compilePathPattern(pattern)
		if err != nil { return }
		if compiled.raw != pattern { t.Fatalf("raw pattern changed: %q", compiled.raw) }
	})
}

func FuzzQueryEvaluation(f *testing.F) {
	for _, seed := range []string{"a=1", "a=%2F&a=", "bad=%", "plus=a+b"} { f.Add(seed) }
	f.Fuzz(func(t *testing.T, rawQuery string) {
		request := httptest.NewRequest(http.MethodGet, "http://gateway/path", nil)
		request.URL.RawQuery = rawQuery
		evaluation := newEvaluation(request)
		_, _, err := evaluation.queryValues("a")
		if err != nil && !errors.Is(err, ErrInvalidQuery) { t.Fatalf("unexpected error: %v", err) }
	})
}
```

Also add `FuzzHostNormalization`, `FuzzPredicateCompile`, and `FuzzRouterCompileAndMatch`; the last target caps generated routes at 64 and asserts deterministic repeat results without panic.

- [ ] **Step 5: Run deterministic tests and short fuzz verification**

Run: `gofmt -w internal/router/reference_test.go internal/router/property_test.go internal/router/fuzz_test.go internal/router/precedence_test.go internal/router/router.go`

Run: `go test ./internal/router -count=1`

Expected: PASS and print any failure with the fixed seed/case index.

Run: `go test ./internal/router -run=^$ -fuzz=FuzzPathPattern -fuzztime=10s`

Expected: PASS with no crash.

Run: `go test ./internal/router -run=^$ -fuzz=FuzzRouterCompileAndMatch -fuzztime=10s`

Expected: PASS with no crash.

- [ ] **Step 6: Commit oracle, property, and fuzz coverage**

```powershell
git add internal/router
git commit -m "test: verify router with property and fuzz oracles"
```

### Task 9: Compile Plugin Inheritance into Stable Hook Chains

**Files:**
- Create: `internal/plugin/registry.go`
- Create: `internal/plugin/chain.go`
- Create: `internal/plugin/registry_test.go`
- Create: `internal/plugin/chain_test.go`

**Interfaces:**
- Consumes: `model.PluginAttachment` and `requestctx.Context`.
- Produces: `plugin.Registry`, `plugin.Definition`, `plugin.CompileChain`, `plugin.Chain`, `plugin.RequestResult`, and `plugin.ShortCircuitResponse`.
- Runtime builder Task 12 compiles one immutable Chain per resolved route.

- [ ] **Step 1: Write failing inheritance, order, disable, and short-circuit tests**

Define test plugins that append names to `requestctx.Context.Scratch[0]`. Prove:

- Service plugins are inherited;
- a Route attachment with the same name fully replaces Service raw config;
- `enabled:false` on Route removes an inherited plugin;
- declaration order does not affect registry request/response order;
- unknown and duplicate-scope plugin names fail compile;
- a short-circuit stops later request hooks but runs applicable response hooks;
- a runtime hook error identifies the plugin name and phase.

```go
func TestCompileChainUsesRegistryOrder(t *testing.T) {
	registry := mustRegistry(t,
		testDefinition("second", 20, 20),
		testDefinition("first", 10, 30),
	)
	attachments := []model.PluginAttachment{
		{Name: "second", Enabled: true, RawConfig: json.RawMessage(`{"value":"route-second"}`)},
		{Name: "first", Enabled: true, RawConfig: json.RawMessage(`{"value":"route-first"}`)},
	}
	chain, err := registry.CompileChain(nil, attachments)
	if err != nil { t.Fatal(err) }
	if got := chain.Names(); !reflect.DeepEqual(got, []string{"first", "second"}) {
		t.Fatalf("Names() = %v", got)
	}
}
```

- [ ] **Step 2: Run plugin tests and observe missing package/API failures**

Run: `go test ./internal/plugin -run 'TestCompileChain|TestPlugin' -count=1`

Expected: FAIL because plugin registry and chain do not exist.

- [ ] **Step 3: Implement registration and immutable compiled-hook contracts**

```go
type Action uint8
const (
	Continue Action = iota
	ShortCircuit
)

type ShortCircuitResponse struct {
	Status  int
	Headers http.Header
	Body    []byte
	Code    string
}

type RequestResult struct {
	Action   Action
	Response *ShortCircuitResponse
	Err      error
}

type RequestHook interface {
	OnRequest(*requestctx.Context, *http.Request) RequestResult
}

type ResponseHook interface {
	OnResponse(*requestctx.Context, *http.Response) error
}

type CompiledPlugin struct {
	Request      RequestHook
	Response     ResponseHook
	ScratchSlots int
}

type Definition struct {
	Name          string
	Version       string
	RequestOrder  int
	ResponseOrder int
	Compile       func(json.RawMessage) (CompiledPlugin, error)
}
```

`NewRegistry` rejects empty/duplicate names, versions, orders that collide within a phase, and nil compilers. Store copied definitions by name.

- [ ] **Step 4: Implement scope merge and pre-sorted hook arrays**

`CompileChain(service, route []model.PluginAttachment)` must:

1. reject duplicate names inside either input scope;
2. copy enabled Service attachments into a name map;
3. replace or delete by every Route attachment;
4. resolve every remaining name in the registry;
5. call each config compiler once;
6. sort request hooks by `RequestOrder` ascending and response hooks by `ResponseOrder` ascending;
7. assign contiguous scratch slot ranges and return total slot count.

`Chain` exposes read-only `Names`, `ScratchSlots`, `RunRequest`, and `RunResponse`. Bound short-circuit body to 64 KiB during compile/execution validation and deep-copy response headers/body before use.

- [ ] **Step 5: Run plugin package tests and race tests**

Run: `gofmt -w internal/plugin/*.go`

Run: `go test ./internal/plugin -count=1`

Expected: PASS.

Run: `go test ./internal/plugin -race -count=1`

Expected: PASS.

- [ ] **Step 6: Commit plugin contracts**

```powershell
git add internal/plugin
git commit -m "feat: compile deterministic plugin chains"
```

### Task 10: Implement the request-id Plugin

**Files:**
- Create: `internal/plugin/request_id.go`
- Create: `internal/plugin/request_id_test.go`
- Modify: `internal/plugin/registry.go`

**Interfaces:**
- Consumes: plugin definition contracts from Task 9.
- Produces: built-in definition named `request-id` version `1.0.0` and `plugin.NewBuiltinRegistry()`.
- Ordering: request-id request hook precedes header rewrite; request-id response hook runs after header rewrite.

- [ ] **Step 1: Write failing preservation, replacement, forwarding, and response tests**

Use an injected deterministic random reader to assert the exact UUID:

```go
func TestRequestIDReplacesInvalidInputAndReturnsUUID(t *testing.T) {
	definition := requestIDDefinition(bytes.NewReader([]byte{
		0x00, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77,
		0x88, 0x99, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff,
	}))
	compiled, err := definition.Compile(json.RawMessage(`{"header_name":"X-Request-ID","max_input_length":128}`))
	if err != nil { t.Fatal(err) }
	request := httptest.NewRequest(http.MethodGet, "http://gateway/", nil)
	request.Header.Set("X-Request-ID", "contains newline\n")
	state := &requestctx.Context{}
	if result := compiled.Request.OnRequest(state, request); result.Err != nil { t.Fatal(result.Err) }
	const want = "00112233-4455-4677-8899-aabbccddeeff"
	if state.RequestID != want || request.Header.Get("X-Request-ID") != want { t.Fatalf("state/header = %q/%q", state.RequestID, request.Header.Get("X-Request-ID")) }
	response := &http.Response{Header: make(http.Header)}
	if err := compiled.Response.OnResponse(state, response); err != nil { t.Fatal(err) }
	if response.Header.Get("X-Request-ID") != want { t.Fatalf("response header = %q", response.Header.Get("X-Request-ID")) }
}
```

Add tests for preserving every allowed character, missing input, zero/multiple inbound values, lengths 1/128/129, custom valid header name, invalid header name, invalid max length, malformed JSON, secure-random read failure, and response overwrite after header rewrite.

- [ ] **Step 2: Run focused tests and observe missing definition failure**

Run: `go test ./internal/plugin -run TestRequestID -count=1`

Expected: FAIL because `requestIDDefinition` is undefined.

- [ ] **Step 3: Compile immutable request-id configuration**

```go
type requestIDConfig struct {
	HeaderName     string `json:"header_name"`
	MaxInputLength int    `json:"max_input_length"`
}

type requestIDPlugin struct {
	headerName string
	maxLength int
	random    io.Reader
}
```

Decode with `json.Decoder.DisallowUnknownFields`, require exactly one JSON value, default header to `X-Request-ID` and max length to 128, validate max in 1–1024, and reject invalid/reserved header names.

Valid inbound IDs contain only ASCII letters, digits, `.`, `_`, `:`, or `-`. Require exactly one header value; otherwise generate a new ID.

- [ ] **Step 4: Generate RFC 4122 UUIDv4 without a dependency**

Read exactly 16 bytes with `io.ReadFull`, set version bits `(b[6]&0x0f)|0x40`, set variant bits `(b[8]&0x3f)|0x80`, and format lowercase hexadecimal as `8-4-4-4-12`. Return a hook error if secure random fails; never fall back to timestamp or weak random.

- [ ] **Step 5: Register the builtin with stable phase orders**

`NewBuiltinRegistry` registers request-id with request order `100` and response order `900`. Task 11 registers header-rewrite with request order `200` and response order `800`, ensuring request ID is established first and restored last.

- [ ] **Step 6: Run plugin tests and race tests**

Run: `gofmt -w internal/plugin/request_id.go internal/plugin/request_id_test.go internal/plugin/registry.go`

Run: `go test ./internal/plugin -count=1`

Expected: PASS.

Run: `go test ./internal/plugin -race -count=1`

Expected: PASS.

- [ ] **Step 7: Commit request ID behavior**

```powershell
git add internal/plugin
git commit -m "feat: add compiled request id plugin"
```

### Task 11: Implement the Request/Response header-rewrite Plugin

**Files:**
- Create: `internal/plugin/header_rewrite.go`
- Create: `internal/plugin/header_rewrite_test.go`
- Modify: `internal/plugin/registry.go`

**Interfaces:**
- Consumes: plugin contracts and builtin registry from Tasks 9–10.
- Produces: built-in definition `header-rewrite` version `1.0.0`, request order `200`, response order `800`.

- [ ] **Step 1: Write failing compile-validation and exact mutation tests**

```go
func TestHeaderRewriteMutatesRequestAndResponse(t *testing.T) {
	registry, err := NewBuiltinRegistry()
	if err != nil { t.Fatal(err) }
	chain, err := registry.CompileChain(nil, []model.PluginAttachment{{
		Name: "header-rewrite", Enabled: true,
		RawConfig: json.RawMessage(`{
			"request":{"remove":["X-Remove"],"set":{"X-Set":"request"},"add":{"X-Add":["a","b"]}},
			"response":{"remove":["X-Hide"],"set":{"X-Set":"response"},"add":{"Set-Cookie":["a=1","b=2"]}}
		}`),
	}})
	if err != nil { t.Fatal(err) }
	request := httptest.NewRequest(http.MethodGet, "http://gateway/", nil)
	request.Header.Set("X-Remove", "old")
	request.Header.Set("X-Set", "old")
	state := &requestctx.Context{}
	if result := chain.RunRequest(state, request); result.Err != nil { t.Fatal(result.Err) }
	if request.Header.Get("X-Remove") != "" || request.Header.Get("X-Set") != "request" || !reflect.DeepEqual(request.Header.Values("X-Add"), []string{"a", "b"}) { t.Fatalf("request headers = %#v", request.Header) }
	response := &http.Response{Header: http.Header{"X-Hide": {"secret"}}}
	if err := chain.RunResponse(state, response); err != nil { t.Fatal(err) }
	if response.Header.Get("X-Hide") != "" || response.Header.Get("X-Set") != "response" || len(response.Header.Values("Set-Cookie")) != 2 { t.Fatalf("response headers = %#v", response.Header) }
}
```

Add compile rejection cases for unknown JSON fields, invalid header name/value, the same normalized header in multiple operation groups, Host, Content-Length, Connection, Keep-Alive, Proxy-Connection, TE, Trailer, Transfer-Encoding, Upgrade, and names beginning with `:`. Prove a string and a string array are both accepted as static values.

- [ ] **Step 2: Run focused tests and observe the missing plugin**

Run: `go test ./internal/plugin -run TestHeaderRewrite -count=1`

Expected: FAIL because header-rewrite is not registered.

- [ ] **Step 3: Strict-decode and normalize operations during compile**

```go
type rewriteConfig struct {
	Request  rewriteDirection `json:"request"`
	Response rewriteDirection `json:"response"`
}

type rewriteDirection struct {
	Remove []string                `json:"remove"`
	Set    map[string]headerValues `json:"set"`
	Add    map[string]headerValues `json:"add"`
}

type compiledRewrite struct {
	request  compiledDirection
	response compiledDirection
}

type compiledDirection struct {
	remove []string
	set    []headerOperation
	add    []headerOperation
}
```

Implement `headerValues.UnmarshalJSON` to accept either one JSON string or a non-empty JSON string array. Canonicalize names with `http.CanonicalHeaderKey`, sort operation slices by normalized name for deterministic compiled state, deep-copy values, and reject cross-group collisions.

- [ ] **Step 4: Apply precompiled operations without parsing or sorting**

For request and response headers, run remove, set, then add. Use `Header.Del`, direct copied slices for set, and `Header.Add` for every add value. Never mutate the compiled value slices. Register the definition in `NewBuiltinRegistry` with the exact orders specified above.

- [ ] **Step 5: Run all plugin tests and full regression tests**

Run: `gofmt -w internal/plugin/header_rewrite.go internal/plugin/header_rewrite_test.go internal/plugin/registry.go`

Run: `go test ./internal/plugin -count=1`

Expected: PASS.

Run: `go test ./... -count=1`

Expected: PASS.

- [ ] **Step 6: Commit header rewrite behavior**

```powershell
git add internal/plugin
git commit -m "feat: add compiled header rewrite plugin"
```

### Task 12: Build and Atomically Activate Complete Runtime Snapshots

**Files:**
- Create: `internal/runtime/errors.go`
- Create: `internal/runtime/validate.go`
- Create: `internal/runtime/snapshot.go`
- Create: `internal/runtime/builder.go`
- Create: `internal/runtime/manager.go`
- Create: `internal/runtime/builder_test.go`
- Create: `internal/runtime/manager_test.go`

**Interfaces:**
- Consumes: model, router, plugin registry, request context metadata, and fixed upstream table.
- Produces: `runtime.NewBuilder`, `(*Builder).Build`, `runtime.NewManager`, `(*Manager).Apply`, `(*Manager).Load`, immutable `Snapshot`, and `CompiledRoute`.
- Gateway Task 14 constructs one Builder/Manager; proxy Task 13 consumes `Snapshot.Match` and `CompiledRoute` methods.

- [ ] **Step 1: Write failing build, resolution, inheritance, and immutability tests**

```go
func TestBuilderResolvesServiceAndCompilesRoute(t *testing.T) {
	resources := testResources()
	upstreams := mustUpstreamTable(t, resources.Upstreams)
	registry, err := plugin.NewBuiltinRegistry()
	if err != nil { t.Fatal(err) }
	builder, err := NewBuilder(upstreams, registry)
	if err != nil { t.Fatal(err) }

	snapshot, err := builder.Build(7, resources)
	if err != nil { t.Fatal(err) }
	request := httptest.NewRequest(http.MethodGet, "http://api.example.com/users/42", nil)
	match, err := snapshot.Match(request)
	if err != nil { t.Fatal(err) }
	if !match.Found || match.Route.Meta().ID != "users" || match.Route.UpstreamMeta().ID != "users-upstream" {
		t.Fatalf("match = %+v", match)
	}
	resources.Routes[0].ID = "mutated"
	if match.Route.Meta().ID != "users" { t.Fatal("published snapshot aliases input") }
}
```

Add failures for duplicate IDs, target XOR violation, unresolved Service/Upstream, plugin compile failure, immutable upstream change, revision zero, duplicate normalized match, and empty route set. Prove Route config replaces Service config and `enabled:false` removes inheritance.

- [ ] **Step 2: Write failing manager activation tests**

Test these transitions:

```text
no snapshot -> Apply(1) succeeds -> Load revision 1
Apply(1) again -> STALE_REVISION -> Load remains revision 1
Apply(2) invalid -> typed build error -> Load remains revision 1
Apply(3) valid -> Load revision 3
concurrent Apply(4..20) -> serialized monotonic result -> highest successfully entered revision active
```

Use a controllable test builder hook to prove a slow lower revision cannot publish after a higher revision.

- [ ] **Step 3: Run runtime tests and observe missing package/API failures**

Run: `go test ./internal/runtime -count=1`

Expected: FAIL because runtime builder and manager are undefined.

- [ ] **Step 4: Implement structured build errors and authoritative resource validation**

```go
type BuildStage string
const (
	StageValidate BuildStage = "validate"
	StageResolve  BuildStage = "resolve"
	StagePlugin   BuildStage = "plugin_compile"
	StageRouter   BuildStage = "router_compile"
)

type BuildError struct {
	Code         string
	Stage        BuildStage
	Revision     uint64
	ResourceKind string
	ResourceID   string
	Field        string
	Cause        error
}

func (e *BuildError) Error() string
func (e *BuildError) Unwrap() error
```

`validateResources` is the authoritative validator for programmatic `Apply`; config validation may fail earlier but cannot replace it. Normalize only cloned resources. Validate IDs, target XOR, references, hosts/path/method/predicate rules, duplicate plugin names, plugin names/configs, and upstream equality.

`Builder.Build` must call `model.CloneResourceSet` before validation and never retain caller-owned slices or `RawConfig` bytes. Every normalization step mutates only this private clone.

- [ ] **Step 5: Compile resolved routes and immutable snapshot state**

```go
type CompiledRoute struct {
	meta         *requestctx.RouteMeta
	service      *requestctx.ServiceMeta
	upstreamMeta *requestctx.UpstreamMeta
	upstream     *upstream.Runtime
	plugins      plugin.Chain
}

type Snapshot struct {
	revision uint64
	router   *router.Router
	routes   []CompiledRoute
	stats    Stats
}

type Match struct {
	Found            bool
	MethodNotAllowed bool
	Route            *CompiledRoute
	Params           []requestctx.ParamSpan
	Allow            []string
}

func (s *Snapshot) Revision() uint64
func (s *Snapshot) Match(*http.Request) (Match, error)
func (r *CompiledRoute) Meta() *requestctx.RouteMeta
func (r *CompiledRoute) ServiceMeta() *requestctx.ServiceMeta
func (r *CompiledRoute) UpstreamMeta() *requestctx.UpstreamMeta
func (r *CompiledRoute) ScratchSlots() int
func (r *CompiledRoute) RunRequest(*requestctx.Context, *http.Request) plugin.RequestResult
func (r *CompiledRoute) RunResponse(*requestctx.Context, *http.Response) error
func (r *CompiledRoute) Target() *url.URL
func (r *CompiledRoute) RoundTrip(*http.Request) (*http.Response, error)
```

Resolve direct and Service routes into the same CompiledRoute shape. Allocate metadata once per compiled route. Build `router.RouteSpec` with the exact compiled-route slice index, then construct the Snapshot only after every step succeeds.

- [ ] **Step 6: Implement serialized atomic activation and observer hooks**

```go
type Observer interface {
	SnapshotApplied(Stats)
	SnapshotRejected(*BuildError, time.Duration)
}

type Manager struct {
	applyMu  sync.Mutex
	active   atomic.Pointer[Snapshot]
	builder  *Builder
	observer Observer
}

func NewManager(builder *Builder, observer Observer) *Manager
func (m *Manager) Apply(revision uint64, resources model.ResourceSet) error
func (m *Manager) Load() *Snapshot
```

Hold `applyMu` across revision check, full build, invariant check, and `active.Store`. The request path only calls `Load`. Observer calls occur after build result is known, use bounded codes/counts, and must not change activation outcome.

- [ ] **Step 7: Run runtime tests, race tests, and all package tests**

Run: `gofmt -w internal/runtime/*.go`

Run: `go test ./internal/runtime -count=1`

Expected: PASS.

Run: `go test ./internal/runtime -race -count=1`

Expected: PASS.

Run: `go test ./... -count=1`

Expected: PASS.

- [ ] **Step 8: Commit snapshot construction and activation**

```powershell
git add internal/runtime
git commit -m "feat: add immutable runtime snapshot activation"
```

### Task 13: Replace Single-Route Proxy Matching with the Snapshot Pipeline

**Files:**
- Create: `internal/proxy/runtime_handler.go`
- Create: `internal/proxy/runtime_handler_test.go`
- Modify: `internal/proxy/errors.go`
- Create: `internal/proxy/route_transport.go`

**Interfaces:**
- Consumes: `runtime.Manager`, `runtime.Snapshot`, `runtime.CompiledRoute`, plugin results, and existing header/proxy helpers.
- Produces: `proxy.NewRuntime(RuntimeOptions)` where RuntimeOptions contains `Snapshots *runtime.Manager`, body limit, and logger; dynamic routing errors; plugin-aware request/response flow.
- Compatibility: keep the Phase 1 `proxy.New(Options)` entry point unchanged until Gateway migrates in Task 14, so every intermediate commit builds and passes.

- [ ] **Step 1: Verify runtime types satisfy the typed requestctx interfaces**

Task 4 already defines low-level interfaces so requestctx does not import internal/runtime. Add compile-time assertions:

```go
var _ requestctx.SnapshotRef = (*runtime.Snapshot)(nil)
var _ requestctx.RuntimeRoute = (*runtime.CompiledRoute)(nil)
```

Put these assertions in `runtime_handler_test.go`; any signature drift must fail compilation before proxy tests run.

- [ ] **Step 2: Rewrite handler tests against a real Manager and add dynamic cases**

Create a runtime fixture that builds two fixed upstream runtimes through `upstream.Table`, a builtin registry, runtime Builder/Manager, applies revision 1, wraps `NewRuntime` with `requestctx.Middleware`, and returns the wrapped handler plus Manager. Keep the existing `newTestHandler(methods, transport)` Phase 1 tests running against the legacy constructor until Task 14 ports them.

Add focused tests for:

- no active snapshot -> `503 GATEWAY_NOT_READY`;
- host/path/parameter/predicate hit;
- 404 and 405 union;
- invalid query -> `400 INVALID_QUERY`;
- revision swap changes route without recreating handler;
- request-id reaches upstream and response;
- request/response header rewrite;
- plugin compile failure leaves previous behavior active;
- Phase 1 streaming/body/upgrade/timeout/error tests remain unchanged in meaning.

- [ ] **Step 3: Run proxy tests and observe the old Options mismatch**

Run: `go test ./internal/proxy -count=1`

Expected: FAIL because `NewRuntime` and `RuntimeOptions` do not exist.

- [ ] **Step 4: Add one request context and a per-route transport dispatcher**

```go
type routeTransport struct{}

func (routeTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	state, ok := requestctx.From(request.Context())
	if !ok || state.Runtime == nil {
		return nil, errors.New("proxy request missing compiled runtime route")
	}
	return state.Runtime.RoundTrip(request)
}
```

Use one shared `httputil.ReverseProxy`. Its Rewrite obtains the request state, calls `SetURL(state.Runtime.Target())`, preserves original Host, removes hop-by-hop headers, and rebuilds forwarding headers. Its ModifyResponse removes hop-by-hop response headers then calls `state.Runtime.RunResponse`.

- [ ] **Step 5: Implement snapshot match and typed request initialization**

Runtime handler order must be exact:

1. validate URL/method;
2. load active snapshot or return 503;
3. call snapshot.Match;
4. map invalid query, 404, and 405;
5. require the Context installed by `requestctx.Middleware` and allocate the route's scratch slots;
6. populate Snapshot, Runtime, revision, metadata pointers, path, and parameter spans on that same Context;
7. reject Upgrade/body limits;
8. execute request plugin chain;
9. write a plugin short-circuit or plugin error through synthetic response handling;
10. enable full duplex, enforce MaxBytesReader, and call ReverseProxy.

Pass the same request pointer through the rest of the pipeline. Gateway installs exactly one private context value before telemetry/proxy; the runtime handler never layers additional context values.

- [ ] **Step 6: Centralize matched-route synthetic responses**

Add:

```go
func (h *handler) writeMatchedResponse(w http.ResponseWriter, request *http.Request, state *requestctx.Context, status int, code, message string, headers http.Header)
```

Create an in-memory `http.Response` with bounded JSON error body or short-circuit body, run the matched route's response hooks, then copy status/headers/body to the downstream writer. Use it for body limit, unsupported Upgrade, request-plugin failure, upstream timeout/connection errors, and plugin response errors before commit. Route-not-found, method-not-allowed, invalid request/query, and gateway-not-ready have no matched chain and use the existing direct error writer.

If response headers/body are already committed, log a bounded error and panic with `http.ErrAbortHandler` rather than writing a second JSON response.

- [ ] **Step 7: Run proxy tests, full tests, and race tests**

Run: `gofmt -w internal/proxy`

Run: `go test ./internal/proxy -count=1`

Expected: PASS including all preserved streaming tests.

Run: `go test ./... -count=1`

Expected: PASS because the legacy constructor remains intact while the new runtime handler is tested independently.

Run: `go test ./internal/proxy -race -count=1`

Expected: PASS.

- [ ] **Step 8: Commit snapshot-driven proxy routing**

```powershell
git add internal/proxy
git commit -m "feat: route proxy requests through runtime snapshots"
```

### Task 14: Wire Gateway Lifecycle, Dynamic Telemetry, and Concurrent Swaps

**Files:**
- Modify: `internal/gateway/gateway.go`
- Modify: `internal/gateway/gateway_test.go`
- Modify: `internal/telemetry/telemetry.go`
- Modify: `internal/telemetry/telemetry_test.go`
- Modify: `cmd/gateway-dp/main.go`
- Modify: `test/integration/gateway_test.go`
- Modify: `test/integration/process_test.go`
- Create: `test/integration/snapshot_test.go`

**Interfaces:**
- Consumes: upstream Table, builtin plugin registry, Builder/Manager, and `proxy.NewRuntime`.
- Produces: `(*Gateway).Apply(revision uint64, resources model.ResourceSet) error`, dynamic per-request telemetry, snapshot observer metrics, and initial revision 1 activation.

- [ ] **Step 1: Add failing startup and Apply lifecycle tests**

Prove:

- `gateway.New` creates all upstream runtimes and activates revision 1 before Start;
- invalid initial resources fail before listeners bind;
- `Gateway.Apply(2, resources)` changes a route target/plugin marker for new requests;
- stale/invalid Apply returns an error and readiness remains true;
- shutdown closes every fixed upstream table runtime;
- request metrics use the matched dynamic route ID and `__unmatched__` for 404, without pre-creating 100,000 label series.

- [ ] **Step 2: Add a failing in-flight revision consistency integration test**

Create two fixed upstream servers A/B. Revision 1 selects A and sets `X-Revision: 1`; hold a request open in A. Apply revision 2 selecting B and setting `X-Revision: 2`, then issue a new request. Assert:

```text
held request -> upstream A + response X-Revision 1
new request  -> upstream B + response X-Revision 2
no response  -> A/2 or B/1 mixed state
```

Repeat the same invariant under 32 request goroutines and at least 1,000 swaps in `snapshot_test.go`; keep individual upstream responses small and bound the test with a 30-second context.

- [ ] **Step 3: Run Gateway/integration tests and observe old single-runtime wiring failure**

Run: `go test ./internal/gateway ./internal/telemetry ./test/integration -count=1`

Expected: FAIL because Gateway still constructs one upstream and one static proxy route.

- [ ] **Step 4: Construct table, registry, builder, manager, and initial snapshot**

In `gateway.New`:

```go
upstreamTable, err := upstream.NewTable(resources.Upstreams)
registry, err := plugin.NewBuiltinRegistry()
builder, err := runtime.NewBuilder(upstreamTable, registry)
telemetryRuntime, err := telemetry.New(bootstrap.Telemetry.RequestMetricsEnabled, bootstrap.Telemetry.ProfilingEnabled)
manager := runtime.NewManager(builder, telemetryRuntime)
if err := manager.Apply(1, resources); err != nil { close table and return a wrapped error }
proxyHandler, err := proxy.NewRuntime(proxy.RuntimeOptions{
	Snapshots: manager,
	MaxRequestBodyBytes: bootstrap.Server.MaxRequestBodyBytes,
	Logger: logger,
})
```

Store table and Manager on Gateway. Add an atomic closing flag: `Gateway.Apply` delegates to Manager before shutdown and returns `GATEWAY_SHUTTING_DOWN` once Shutdown begins. Shutdown sets the flag before draining listeners and closes idle connections through the table exactly once.

Compose the traffic handler in this order so telemetry observes the same populated request state after proxy return:

```go
trafficHandler := recoverPanics(
	requestctx.Middleware(telemetryRuntime.Wrap(proxyHandler)),
	logger,
)
```

- [ ] **Step 5: Make telemetry route-aware and implement runtime.Observer**

Change `Telemetry.Wrap(next, fixedRouteID)` to `Telemetry.Wrap(next)`. After `next.ServeHTTP`, read requestctx; use the matched route ID or `__unmatched__`. Do not initialize route label values at snapshot build.

Register bounded snapshot metrics:

```text
gateway_runtime_active_revision gauge
gateway_runtime_snapshot_apply_duration_seconds histogram
gateway_runtime_snapshot_apply_total{result,stage,code} counter
gateway_runtime_compiled_routes gauge
gateway_runtime_compiled_services gauge
gateway_runtime_compiled_plugins gauge
```

Implement `SnapshotApplied(Stats)` and `SnapshotRejected(*BuildError, duration)` without resource IDs or raw messages as labels.

- [ ] **Step 6: Update integration fixtures, remove the legacy constructor, and keep command behavior**

Keep `gateway-dp -config` unchanged. Port every Phase 1 proxy test from `proxy.New(Options)` to the runtime fixture, then delete the legacy single-route `Options`, constructor, matcher fields, and compatibility-only code. Rename/consolidate `runtime_handler.go` into `handler.go` after the old implementation is removed. Update test resources to use the expanded model and keep v1alpha1 process fixtures unchanged. Add one process test booting `configs/phase2.yaml` equivalent content from a temp file and verify exact, parameter, request-id, and header-rewrite behavior over HTTP/1.1 and HTTP/2 TLS.

- [ ] **Step 7: Run focused, integration, race, and full suites**

Run: `gofmt -w internal/gateway internal/telemetry cmd/gateway-dp test/integration`

Run: `go test ./internal/gateway ./internal/telemetry ./test/integration -count=1`

Expected: PASS.

Run: `go test ./internal/runtime ./internal/router ./internal/plugin ./internal/proxy ./internal/gateway ./test/integration -race -count=1`

Expected: PASS, including 32-worker/1,000-swap consistency.

Run: `go test ./... -count=1`

Expected: PASS.

- [ ] **Step 8: Commit end-to-end runtime activation**

```powershell
git add internal/gateway internal/telemetry cmd/gateway-dp test/integration
git commit -m "feat: activate dynamic gateway runtime snapshots"
```

### Task 15: Generate the Standard Dataset and Enforce Router/Snapshot Resource Gates

**Files:**
- Create: `internal/benchdataset/generator.go`
- Create: `internal/benchdataset/generator_test.go`
- Create: `internal/benchdataset/render.go`
- Create: `internal/benchdataset/render_test.go`
- Create: `cmd/bench-dataset/main.go`
- Create: `internal/router/router_benchmark_test.go`
- Create: `internal/runtime/acceptance_test.go`

**Interfaces:**
- Consumes: canonical model, runtime Builder, builtin plugins, and fixed upstream table.
- Produces: `benchdataset.Generate(Options)`, canonical checksum/metadata, v1alpha2 and APISIX renderers, dataset CLI, microbenchmarks, and opt-in 100,000-route acceptance tests.

- [ ] **Step 1: Write failing deterministic distribution and checksum tests**

```go
func TestGenerateStandardDatasetIsDeterministic(t *testing.T) {
	options := Options{RouteCount: 100_000, Seed: 20260723, Endpoint: "http://upstream-performance:8080"}
	first, firstMeta, err := Generate(options)
	if err != nil { t.Fatal(err) }
	second, secondMeta, err := Generate(options)
	if err != nil { t.Fatal(err) }
	if firstMeta.Checksum != secondMeta.Checksum || !reflect.DeepEqual(first, second) {
		t.Fatalf("dataset is not deterministic: %s != %s", firstMeta.Checksum, secondMeta.Checksum)
	}
	if firstMeta.HostCounts != (HostCounts{Exact: 60_000, Wildcard: 20_000, Hostless: 20_000}) {
		t.Fatalf("host counts = %+v", firstMeta.HostCounts)
	}
	if firstMeta.PathCounts != (PathCounts{Static: 50_000, Parameter: 20_000, Prefix: 15_000, CatchAll: 15_000}) {
		t.Fatalf("path counts = %+v", firstMeta.PathCounts)
	}
}
```

Also assert 90/10 standard/custom methods, 20% predicates, 50/50 Service/direct targets, 10% request-id, 10% header-rewrite, unique match signatures, and three known sentinel route IDs/URLs for first/middle/last.

- [ ] **Step 2: Run dataset tests and observe missing package failures**

Run: `go test ./internal/benchdataset -count=1`

Expected: FAIL because dataset generation is absent.

- [ ] **Step 3: Implement deterministic generation without global randomness**

```go
type Options struct {
	RouteCount       int
	Seed             int64
	Endpoint         string
	BaselineSentinel string
}

type Metadata struct {
	SchemaVersion int
	Seed          int64
	RouteCount    int
	Checksum      string
	HostCounts    HostCounts
	PathCounts    PathCounts
	MethodCounts  MethodCounts
	PredicateRoutes int
	ServiceRoutes   int
	PluginCounts    map[string]int
	Sentinels        map[string]Sentinel
}

func Generate(options Options) (model.ResourceSet, Metadata, error)
```

Use `rand.New(rand.NewSource(seed))`, but assign category counts by deterministic index ranges before a seeded final shuffle. Generate collision-free route IDs, hosts, paths, and predicates from the index. A 100,000-route dataset reserves three plugin-free static exact sentinel routes named `sentinel-first`, `sentinel-middle`, and `sentinel-last` with known URLs returning the 1 KiB upstream payload. A one-route dataset requires `BaselineSentinel` equal to `first`, `middle`, or `last` and generates the exact corresponding sentinel route, so every 100,000-route measurement has a semantically equivalent one-route baseline.

Compute checksum by JSON-encoding a canonical copy with routes/services/upstreams sorted by ID and hashing with SHA-256. Record every distribution counter in Metadata.

- [ ] **Step 4: Render strict Go and equivalent APISIX standalone configs**

`RenderGateway` emits complete `gateway/v1alpha2` YAML including supplied listener/certificate paths. `RenderAPISIX` emits standalone routes/services/upstreams and maps:

```text
Go {id}             -> APISIX :id
Go trailing /*      -> APISIX /*
Go {*path}          -> APISIX /*
header name         -> APISIX var http_<lowercase_with_underscores>
query name          -> APISIX var arg_<name>
exists/not_exists   -> ~= nil / == nil
equals/not_equals   -> == / ~=
one_of              -> in
request-id          -> request-id {header_name, include_in_response:true, algorithm:uuid}
request rewrite     -> proxy-rewrite.headers
response rewrite    -> response-rewrite.headers
```

Set APISIX HTTP router mode to `radixtree_uri_with_parameter`. Because APISIX does not expose a named catch-all value to these benchmark plugins, map Go named catch-all to the same `/*` match set; path-parameter values are not consumed by the Phase 2 plugins.

Renderer tests parse both YAML outputs, assert exact resource counts/checksum metadata, and verify representative translations for every category. Add a CLI:

```text
bench-dataset -routes 100000 -seed 20260723 -baseline-sentinel "" -gateway-out <path> -apisix-out <path> -metadata-out <path> -endpoint http://upstream-performance:8080
```

- [ ] **Step 5: Add router scale microbenchmarks**

```go
func BenchmarkRouterScale(b *testing.B) {
	for _, routeCount := range []int{1, 1_000, 10_000, 100_000} {
		for _, position := range []string{"first", "middle", "last"} {
			options := benchdataset.Options{RouteCount: routeCount, Seed: 20260723, Endpoint: "http://upstream:8080"}
			if routeCount == 1 { options.BaselineSentinel = position }
			resources, metadata, err := benchdataset.Generate(options)
			if err != nil { b.Fatal(err) }
			compiled := compileDatasetRouter(b, resources)
			sentinel := metadata.Sentinels[position]
			request := httptest.NewRequest(http.MethodGet, sentinel.URL, nil)
			b.Run(fmt.Sprintf("routes=%d/%s", routeCount, position), func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					result, err := compiled.Match(request)
					if err != nil || !result.Found { b.Fatal("sentinel did not match") }
				}
			})
		}
	}
}
```

Add wildcard, parameter, catch-all, predicate hit/miss, not-found, and 10,000-candidate collision-stress sub-benchmarks. Static sentinel cases must report 0 allocs/op.

- [ ] **Step 6: Add opt-in compile, heap, and 20-swap acceptance tests**

Guard only the expensive 100,000-route checks with `GATEWAY_PHASE2_ACCEPTANCE=1`; normal CI still runs a 10,000-route structural version.

The acceptance test must:

1. generate 100,000 routes;
2. capture Go heap baseline after GC;
3. build revision 1 and require elapsed time <=5 seconds;
4. force GC and require active incremental heap <=512 MiB after subtracting retained input/table baselines;
5. apply 20 cloned revisions with deterministic route plugin changes;
6. release old inputs, force two GCs at test boundary, and require retained snapshot heap <=115% of the measured one-snapshot heap;
7. verify all sentinel routes after the final swap.

Use `runtime.ReadMemStats` imported as `goruntime` to avoid collision with `internal/runtime` package naming. Log elapsed time, heap measurements, Go version, CPU count, dataset checksum, and seed.

- [ ] **Step 7: Run normal tests, opt-in acceptance, and benchmarks**

Run: `gofmt -w internal/benchdataset cmd/bench-dataset internal/router/router_benchmark_test.go internal/runtime/acceptance_test.go`

Run: `go test ./internal/benchdataset ./internal/router ./internal/runtime -count=1`

Expected: PASS.

Run on the reference profile: `$env:GATEWAY_PHASE2_ACCEPTANCE='1'; go test ./internal/runtime -run TestPhase2Acceptance -count=1 -v`

Expected: PASS with compile <=5s, active snapshot <=512 MiB, and steady-state <=115% logged.

Run: `go test ./internal/router -run=^$ -bench BenchmarkRouterScale -benchmem -count=5`

Expected: every static sentinel sub-benchmark reports `0 B/op` and `0 allocs/op`; retain raw output for the benchmark report.

- [ ] **Step 8: Commit deterministic dataset and resource gates**

```powershell
git add internal/benchdataset cmd/bench-dataset internal/router/router_benchmark_test.go internal/runtime/acceptance_test.go
git commit -m "bench: add phase 2 route scale acceptance suite"
```

### Task 16: Add the Isolated Phase 2 End-to-End Benchmark and Report

**Files:**
- Create: `bench/lib/common.ps1`
- Modify: `bench/run.ps1`
- Create: `bench/run-phase2.ps1`
- Create: `bench/phase2-scenarios.yaml`
- Create: `bench/compose.phase2.yaml`
- Create: `bench/apisix/config.phase2.yaml`
- Create: `bench/schema/phase2-raw-run.schema.json`
- Create: `bench/schema/phase2-summary.schema.json`
- Create: `internal/phase2benchreport/report.go`
- Create: `internal/phase2benchreport/report_test.go`
- Create: `cmd/phase2-bench-report/main.go`
- Modify: `Dockerfile`
- Modify: `bench/README.md`

**Interfaces:**
- Consumes: deterministic dataset CLI, existing pinned load generators, APISIX source guard, payloads, certificates, and raw-evidence conventions.
- Produces: isolated `run-phase2.ps1`, raw Phase 2 schema, relative scalability verdict, APISIX comparative tables, resource traces, and a report command/image.

- [ ] **Step 1: Extract shared PowerShell helpers without changing Phase 1 behavior**

Move only pure/shared functions from `bench/run.ps1` into `bench/lib/common.ps1`: native command execution, Docker invocation, path normalization, APISIX source guard, environment metadata, certificate/payload generation, readiness probes, image IDs, generator invocation/parsing, cleanup, and JSON writing.

Both runners dot-source the library by resolved script root. Keep Phase 1 parameters, scenario semantics, output schema, target ordering, and paths unchanged.

Run: `pwsh -NoProfile -Command "[void][scriptblock]::Create((Get-Content -Raw bench/lib/common.ps1)); [void][scriptblock]::Create((Get-Content -Raw bench/run.ps1))"`

Expected: exit 0.

Run: `pwsh bench/run.ps1 -Mode smoke -Target go -Scenario h1-clear -ApisixSource D:\User2\open_source\apisix -ResultsDir bench/results/phase1-refactor-smoke`

Expected: Phase 1 Go smoke completes with zero request errors and cleanup leaves no benchmark containers/network.

- [ ] **Step 2: Write failing report aggregation tests from fixed raw fixtures**

Create test fixtures in Go values for five repetitions of:

```text
protocol h1-clear and h2-tls
route_count 1 and 100000
sentinel first/middle/last
target go and apisix
rps, p99_us, errors, container_cpu, container_memory
dataset checksum and environment class
```

Assert the report computes medians and these exact verdict rules:

```text
Go 100k throughput / Go 1-route throughput >= 0.90
Go 100k p99 / Go 1-route p99 <= 1.10
100k first/middle/last median-throughput spread <= 0.05
100k first/middle/last median-p99 spread <= 0.10
unexpected errors == 0
APISIX values displayed but excluded from blocking verdict
```

Include boundary tests at exactly 0.90/1.10/0.05/0.10, missing repetition rejection, mixed checksum rejection, provisional environment labeling, and Markdown/CSV/JSON stable ordering.

- [ ] **Step 3: Run report tests and observe missing package failure**

Run: `go test ./internal/phase2benchreport -count=1`

Expected: FAIL because the report package does not exist.

- [ ] **Step 4: Implement raw/summary schemas and deterministic report output**

Raw run fields must include:

```text
schema_version, timestamp, repository_commit, apisix_commit
environment_class and full environment metadata
dataset seed/checksum/distribution counters
target name/version/image ID
protocol, TLS, payload_bytes, route_count, sentinel, shuffled
warmup_seconds, duration_seconds, repetition
requests, requests_per_second, p50_us, p95_us, p99_us
timeouts, socket_errors, non_2xx, unexpected_errors
direct_requests_per_second and headroom_factor
container_cpu_samples and container_memory_samples
```

`phase2-bench-report` reads the result root, validates group completeness and metadata consistency, then writes `summary.json`, `summary.csv`, and `summary.md`. JSON contains a blocking-gates array with measured value, threshold, pass flag, and evidence paths.

- [ ] **Step 5: Define isolated Phase 2 profiles and scenario catalog**

`phase2-scenarios.yaml` defines:

```json
{
  "schema_version": 1,
  "seed": 20260723,
  "route_counts": [1, 100000],
  "sentinels": ["first", "middle", "last"],
  "payload_bytes": 1024,
  "modes": {
    "smoke": {"warmup_seconds": 3, "duration_seconds": 10, "repetitions": 1},
    "compare": {"warmup_seconds": 15, "duration_seconds": 60, "repetitions": 5}
  },
  "scenarios": {
    "h1-clear": {"generator": "wrk", "protocol": "http/1.1", "tls": false, "threads": 8, "connections": 64},
    "h2-tls": {"generator": "h2load", "protocol": "http/2", "tls": true, "threads": 8, "clients": 64, "streams_per_client": 16}
  }
}
```

`compose.phase2.yaml` extends the existing images but assigns Go/APISIX/load targets 8 CPUs and a 16 GiB limit where applicable, mounts distinct generated/results directories, and uses a distinct `g-gateway-phase2-benchmark` network/project name. `config.phase2.yaml` enables `radixtree_uri_with_parameter`, keeps standalone mode, and preserves the Phase 1 downstream keepalive fix.

- [ ] **Step 6: Implement runner orchestration and semantic preflight**

`run-phase2.ps1` parameters:

```powershell
param(
  [ValidateSet('smoke','compare')] [string] $Mode = 'smoke',
  [ValidateSet('go','apisix','all')] [string] $Target = 'all',
  [ValidateSet('h1-clear','h2-tls','all')] [string] $Scenario = 'all',
  [string] $ApisixSource = 'D:\User2\open_source\apisix',
  [string] $ResultsDir = '.\bench\results\phase2'
)
```

Generate one 100,000-route Go/APISIX config and three one-route baseline configs, one for each sentinel. Before measurement, start each target and send representative exact, wildcard, parameter, prefix, catch-all, header predicate, query predicate, request-id, request rewrite, and response rewrite probes. Compare status, selected upstream marker, and expected headers across Go/APISIX. Abort before performance measurement on semantic mismatch.

Measure direct control once per protocol, alternate target order by repetition, keep warmup on measurement connections, write request-level artifacts, and sample `docker stats --no-stream` once per second into raw resource traces. For each sentinel, measure its matching one-route config and the same 100,000-route process; reuse the 100,000-route process across first/middle/last within a target/repetition. Always clean targets/load generators/network in `finally`.

- [ ] **Step 7: Run syntax, unit, schema, and smoke verification**

Run: `gofmt -w internal/phase2benchreport cmd/phase2-bench-report`

Run: `go test ./internal/phase2benchreport -count=1`

Expected: PASS.

Run: `pwsh -NoProfile -Command "[void][scriptblock]::Create((Get-Content -Raw bench/lib/common.ps1)); [void][scriptblock]::Create((Get-Content -Raw bench/run.ps1)); [void][scriptblock]::Create((Get-Content -Raw bench/run-phase2.ps1))"`

Expected: exit 0.

Run: `Get-Content -Raw bench/phase2-scenarios.yaml | ConvertFrom-Json | Out-Null`

Expected: exit 0.

Run: `pwsh bench/run-phase2.ps1 -Mode smoke -Target all -Scenario all -ApisixSource D:\User2\open_source\apisix -ResultsDir bench/results/phase2-smoke`

Expected: semantic preflight passes, every run has zero unexpected errors, raw/resource artifacts exist, the report is marked provisional, and Docker cleanup reports no Phase 2 containers/network.

- [ ] **Step 8: Commit the Phase 2 benchmark harness**

```powershell
git add bench internal/phase2benchreport cmd/phase2-bench-report Dockerfile
git commit -m "bench: add phase 2 routing scale comparison"
```

### Task 17: Document Operations, Run Final Gates, and Record Honest Acceptance

**Files:**
- Create: `docs/operations/phase-2-runbook.md`
- Create: `docs/benchmarks/phase-2-current-status.md`
- Modify: `README.md`
- Modify: `docs/superpowers/specs/2026-07-23-phase-2-runtime-snapshot-router-kernel-design.md`
- Modify: `docs/superpowers/specs/2026-07-21-go-native-api-gateway-phase-roadmap-design.md`

**Interfaces:**
- Consumes: all implementation and evidence tasks.
- Produces: reproducible operator commands, known limitations, evidence links, and an acceptance status based only on commands that actually passed.

- [ ] **Step 1: Write the Phase 2 runbook before the final long run**

Document exact commands for:

- v1alpha1/v1alpha2 startup;
- focused unit/property/race tests;
- five-minute-per-target fuzz acceptance;
- 100,000-route compile/memory acceptance;
- router microbench;
- Phase 2 smoke and canonical compare;
- inspecting raw/summary/resource traces;
- interpreting relative gates versus non-blocking APISIX comparison;
- cleanup and rerun after interruption;
- dedicated Linux reference requirements;
- known exclusions: upstream mutation, regex, control plane, retries/health checks, auth/rate-limit plugins.

State that `Apply` is internal-only and show no unsupported external reload endpoint.

- [ ] **Step 2: Run formatting, static analysis, unit, integration, race, and command builds**

Run in the pinned Go 1.26.5 environment:

```powershell
gofmt -l .
go vet ./...
go test ./... -count=1
go test ./... -race -count=1
go build ./cmd/...
```

Expected: `gofmt -l .` prints nothing; every other command exits 0. If any command fails, fix the failure in the owning task, rerun the focused test, then rerun this complete gate.

- [ ] **Step 3: Run the full property, fuzz, resource, and microbenchmark acceptance gates**

Run property tests with the fixed 10,000-case seed:

`go test ./internal/router -run TestCompiledRouterMatchesReference -count=1 -v`

Expected: PASS.

Run every fuzz target for at least five minutes:

```powershell
go test ./internal/router -run=^$ -fuzz=FuzzPathPattern -fuzztime=5m
go test ./internal/router -run=^$ -fuzz=FuzzQueryEvaluation -fuzztime=5m
go test ./internal/router -run=^$ -fuzz=FuzzHostNormalization -fuzztime=5m
go test ./internal/router -run=^$ -fuzz=FuzzPredicateCompile -fuzztime=5m
go test ./internal/router -run=^$ -fuzz=FuzzRouterCompileAndMatch -fuzztime=5m
```

Expected: all exit 0; commit any minimized new crash corpus only after fixing the defect and rerunning the target.

On the reference profile:

```powershell
$env:GATEWAY_PHASE2_ACCEPTANCE='1'
go test ./internal/runtime -run TestPhase2Acceptance -count=1 -v
go test ./internal/router -run=^$ -bench BenchmarkRouterScale -benchmem -count=5
Remove-Item Env:GATEWAY_PHASE2_ACCEPTANCE
```

Expected: compile, active-heap, steady-state, sentinel correctness, and zero-allocation static gates pass. Save raw console output beneath the ignored Phase 2 result root.

- [ ] **Step 4: Build all production/test/report containers**

Run:

```powershell
docker build --build-arg COMMAND=gateway-dp -t g-gateway:phase2 .
docker build --build-arg COMMAND=test-upstream -t g-gateway-test-upstream:phase2 .
docker build --build-arg COMMAND=bench-dataset -t g-gateway-bench-dataset:phase2 .
docker build --build-arg COMMAND=phase2-bench-report -t g-gateway-phase2-bench-report:phase2 .
```

Expected: all four builds exit 0.

- [ ] **Step 5: Run Phase 2 smoke and inspect the report**

Run:

`pwsh bench/run-phase2.ps1 -Mode smoke -Target all -Scenario all -ApisixSource D:\User2\open_source\apisix -ResultsDir bench/results/phase2-final-smoke`

Expected: semantic preflight, direct headroom, artifact completeness, zero-error checks, report generation, and cleanup pass. Inspect `summary.json` and `summary.md`; do not claim a relative performance pass if a threshold is missed.

- [ ] **Step 6: Run the canonical compare only with explicit long-run authorization**

Run after the user approves the multi-hour Docker workload and available disk/memory has been checked:

`pwsh bench/run-phase2.ps1 -Mode compare -Target all -Scenario all -ApisixSource D:\User2\open_source\apisix -ResultsDir bench/results/phase2-final-compare`

Expected: five complete repetitions for every target/protocol/route-count/sentinel group, consistent checksums, zero unexpected errors, complete resource traces, and a generated relative-gate verdict. If authorization is not granted, record canonical compare as pending rather than substituting smoke data.

- [ ] **Step 7: Write the evidence status without overstating results**

`phase-2-current-status.md` must include:

- branch/commit and exact environment class;
- dataset seed/checksum/distribution;
- verification commands and outcomes;
- compile time, active heap, steady-state ratio, microbench allocations;
- 1-route versus 100,000-route median throughput/p99 ratios;
- first/middle/last spreads;
- APISIX comparative throughput/p99/memory;
- missing or rejected gates;
- raw evidence paths;
- final status chosen from `implemented; relative gates passed`, `implemented; provisional miss`, or `implementation complete; canonical evidence pending` based solely on evidence.

Update the design audit and roadmap only to the status supported by that file.

- [ ] **Step 8: Run documentation checks and final verification-before-completion**

Run:

```powershell
$unfinished = @('T'+'BD', 'T'+'ODO', 'FIX'+'ME', 'place'+'holder', 'chưa quyết định') -join '|'
rg -n $unfinished README.md docs bench/README.md --glob '!docs/superpowers/plans/2026-07-23-phase-2-runtime-snapshot-router-kernel.md'
git diff --check
git status --short
```

Expected: no unfinished-marker hits in Phase 2 documents, no whitespace errors, and only intended Phase 2 files modified. Re-run the complete commands from Steps 2–5 after the final documentation edit if any code/config changed.

- [ ] **Step 9: Commit the verified Phase 2 handoff**

```powershell
git add README.md docs bench/README.md
git commit -m "docs: record phase 2 verification workflow"
```

Do not invoke `superpowers:finishing-a-development-branch` until every blocking implementation/correctness gate is complete and the status document accurately records any pending canonical performance evidence.

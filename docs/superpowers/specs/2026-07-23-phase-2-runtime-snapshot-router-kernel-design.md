# Phase 2 — Runtime snapshot và router kernel

- Ngày: 2026-07-23
- Trạng thái: Thiết kế đã duyệt; chờ user review văn bản
- Parent roadmap: [Go-native API Gateway phase roadmap](2026-07-21-go-native-api-gateway-phase-roadmap-design.md)
- North-star: [Go-native API Gateway design](2026-07-21-go-native-api-gateway-design.md)
- Phase trước: [Phase 1 — Proxy baseline và benchmark harness](2026-07-22-phase-1-proxy-baseline-benchmark-design.md)

## 1. Quyết định chuyển phase

Phase 1 được chấp nhận để tiếp tục roadmap dựa trên proxy correctness, test/race/build gates, benchmark reproducibility và clean smoke evidence. Kết quả hiệu năng hiện tại vẫn là `provisional_miss`; quyết định này không được diễn giải thành APISIX parity.

Canonical compare, profiling sau parity miss và chứng nhận trên dedicated Linux tiếp tục là performance debt. Chúng không chặn việc thiết kế và triển khai Phase 2, nhưng phải hoàn tất trước production certification ở Phase 7.

## 2. Mục tiêu

Phase 2 chứng minh hai invariant chính:

1. request hot path chỉ đọc một immutable runtime revision trong toàn bộ lifecycle;
2. route matching giữ được correctness và chi phí ổn định khi scale đến 100.000 route.

Phase này thay single-route matcher của Phase 1 bằng canonical resources, shadow snapshot build, atomic activation, compiled router và một plugin pipeline tối thiểu. HTTP proxy semantics đã được kiểm chứng ở Phase 1 phải được giữ nguyên.

## 3. Phạm vi

Bao gồm:

- canonical Route, Service và Upstream model cần cho data plane;
- config schema `gateway/v1alpha2` và backward adapter cho `gateway/v1alpha1`;
- immutable `RuntimeSnapshot`;
- full shadow rebuild và atomic pointer swap;
- internal `Apply(revision, ResourceSet)` API;
- exact/wildcard/hostless host indexes;
- segment radix tree cho exact, prefix, parameter và catch-all paths;
- method bitmask và compiled header/query predicates;
- deterministic route precedence;
- typed lightweight request context và compiled plugin chains;
- plugin `request-id` và `header-rewrite`;
- correctness, property, fuzz, concurrent-swap, race, memory và performance tests;
- benchmark 1 đến 100.000 route và comparative APISIX evidence.

Không bao gồm:

- dynamic upstream endpoint hoặc transport update;
- load balancing, retry, active/passive health checks;
- Admin API, etcd, gRPC distribution hoặc file watcher;
- regex routing hoặc expression language;
- authentication, authorization, rate limiting hoặc third-party plugins;
- reusable PluginConfig resource;
- incremental hoặc copy-on-write snapshot build.

## 4. Kiến trúc runtime

Request flow:

    net/http Server
      -> validate và normalize request view
      -> atomic load RuntimeSnapshot một lần
      -> compiled router match
      -> typed RequestContext
      -> compiled request plugin chain
      -> fixed UpstreamRuntime
      -> httputil.ReverseProxy
      -> compiled response plugin chain
      -> bounded telemetry

Module boundaries:

- `internal/model`: canonical resources và match/plugin configuration; không chứa runtime state;
- `internal/runtime`: snapshot manager, builder và immutable compiled snapshot;
- `internal/router`: route compiler, matcher và reference matcher dùng cho tests;
- `internal/plugin`: registry, compiler/execute contracts và built-in plugins;
- `internal/requestctx`: typed per-request state và scratch slots;
- `internal/upstream`: fixed runtime table được tạo lúc bootstrap;
- `internal/proxy`: HTTP forwarding/correctness, không còn sở hữu single-route matching;
- `internal/gateway`: process lifecycle, listener wiring và initial snapshot activation.

`RuntimeSnapshot` chứa:

- revision;
- compiled router;
- compiled routes;
- Route/Service/Upstream references đã resolve;
- compiled request/response plugin chains;
- build metadata cần cho telemetry và diagnostics.

Mọi field runtime đều unexported. Snapshot chỉ expose read methods và không có mutating method sau khi publish.

## 5. Canonical resource model

Model khái niệm:

    ResourceSet
      Routes[]
      Services[]
      Upstreams[]

    Route
      ID
      Priority
      Match
      ServiceRef XOR UpstreamRef
      Plugins[]

    RouteMatch
      Hosts[]
      PathPattern
      Methods[]
      HeaderPredicates[]
      QueryPredicates[]

    Service
      ID
      UpstreamRef
      Plugins[]

    PluginAttachment
      Name
      Enabled
      RawConfig

`RawConfig` là immutable byte payload. Config decoder tạo payload riêng cho từng attachment; snapshot builder deep-copy trước khi gọi plugin compiler. Không JSON/YAML parsing trên request path.

Upstream giữ transport fields của Phase 1. Phase 2 cho phép nhiều upstream runtime được tạo lúc bootstrap, nhưng tập ID, endpoints và transport configuration không đổi sau khi Gateway start. Các revision mới chỉ được tham chiếu đến runtime đã tồn tại.

Vì `Apply` luôn nhận full `ResourceSet`, mỗi revision phải gửi lại cùng canonical Upstream set đã dùng ở bootstrap. Thiếu upstream, thêm upstream hoặc thay bất kỳ endpoint/transport field nào đều bị từ chối bằng `UPSTREAM_SET_IMMUTABLE`.

## 6. Configuration compatibility

Phase 2 thêm schema strict `gateway/v1alpha2`. Unknown fields, duplicate IDs và invalid enum/operator đều bị từ chối.

Decoder tiếp tục hỗ trợ `gateway/v1alpha1` và chuyển sang canonical model như sau:

- priority bằng `0`;
- route không có host constraint;
- path dùng exact match;
- methods và upstream reference giữ nguyên;
- không có Service hoặc plugin attachment.

Nhờ backward adapter, Phase 1 configs, integration tests và benchmark scenarios tiếp tục chạy nguyên trạng.

Bootstrap listener, server, TLS và telemetry configuration tiếp tục tách khỏi `ResourceSet`. Phase 4 có thể thay nguồn resource bằng remote snapshots mà không đổi process lifecycle.

## 7. Validation và reference resolution

`Apply` từ chối toàn bộ revision khi có bất kỳ lỗi nào:

- revision không lớn hơn active revision;
- ID rỗng hoặc trùng trong cùng resource type;
- Route không có đúng một trong `service_ref` hoặc `upstream_ref`;
- Service/Upstream reference không resolve;
- Upstream set khác canonical bootstrap set hoặc reference không thuộc fixed runtime table;
- host/path/method/predicate không hợp lệ;
- cùng scope khai báo trùng plugin name;
- plugin name không đăng ký hoặc raw config compile lỗi;
- hai route có cùng priority và match expression hoàn toàn giống nhau.

Host validation chỉ chấp nhận:

- exact DNS host;
- wildcard một label bên trái, ví dụ `*.example.com`;
- không khai báo host.

Host được lowercase, bỏ port và một trailing dot trước khi match. Wildcard không match apex và không match nhiều hơn một label.

Path pattern phải bắt đầu bằng `/`. Parameter name phải hợp lệ và duy nhất trong một pattern. Catch-all chỉ được đứng ở cuối. Query và fragment không được xuất hiện trong path pattern.

Methods là HTTP tokens hợp lệ, được uppercase khi compile và không trùng. `HEAD`, `OPTIONS` và custom methods không được suy diễn từ method khác.

Builder resolve Route -> Service -> Upstream và plugin inheritance trước khi tạo compiled route. Hot path giữ direct pointers; không lookup resource reference.

## 8. Snapshot build và activation

Chọn full shadow rebuild thay vì incremental mutation hoặc persistent copy-on-write.

`runtime.Manager` có:

    applyMu sync.Mutex
    active  atomic.Pointer[RuntimeSnapshot]

`applyMu` serialize toàn bộ `Apply` để revision cũ không thể hoàn thành sau và ghi đè revision mới. Mutex này không xuất hiện trên request path.

Apply flow:

    lock applyMu
      -> verify monotonic revision
      -> deep-copy resources
      -> validate resources và references
      -> merge và compile plugin chains
      -> compile router
      -> verify runtime invariants
      -> active.Store(newSnapshot)
    unlock

Nếu một bước lỗi, snapshot mới bị bỏ và active pointer giữ nguyên. Failed apply không làm mất readiness khi snapshot cũ còn hợp lệ.

Gateway startup decode config, tạo fixed upstream runtime table, build revision `1`, rồi mới mở readiness. Nếu initial build lỗi, process startup fail. Khi chưa từng activate snapshot, traffic trả `503 GATEWAY_NOT_READY`.

## 9. Snapshot lifetime và request consistency

Mỗi request gọi `manager.Load()` đúng một lần trước route match. Pointer đó được giữ trong `RequestContext` cho đến khi response hooks kết thúc.

Không reload active pointer sau route match. Route, plugin chain, service/upstream reference và snapshot revision của một request luôn đến từ cùng snapshot.

Snapshot cũ không dùng manual reference counting hoặc finalizer. Local request pointer giữ snapshot sống tự nhiên; GC chỉ reclaim khi request cuối cùng không còn tham chiếu. Upstream connection pools nằm trong fixed Gateway-owned runtime table và không phụ thuộc snapshot lifetime.

Production không gọi `runtime.GC()`. Tests được phép force GC tại measurement boundary để kiểm tra retained heap sau nhiều swaps.

## 10. Router indexes

Snapshot chứa ba host indexes:

    exactHosts[normalizedHost] -> PathTree
    wildcardHosts[suffix]      -> PathTree
    hostless                   -> PathTree

Mỗi `PathTree` là segment radix tree với:

- static edges;
- một parameter edge tại mỗi node;
- trailing prefix edge;
- trailing catch-all edge.

Supported patterns:

- exact: `/users`;
- prefix: `/api/*`;
- parameter: `/users/{id}`;
- catch-all: `/assets/{*path}`.

Parameter match đúng một non-empty segment. `/api/*` match `/api/` và các descendants nhưng không match `/api`. `/assets/{*path}` match `/assets/` với empty capture và mọi descendant, nhưng không match `/assets`. Trailing slash luôn có ý nghĩa; muốn match cả base path và descendants phải khai báo hai routes.

Router không rewrite outbound path. Parameter ban đầu được trả dưới dạng spans vào normalized path và chỉ materialize khi consumer cần.

Method match dùng bitmask cho HTTP methods chuẩn và immutable fallback lookup cho custom methods.

Header/query predicates được compile thành flat instructions:

    field-kind | normalized-name | operator | compiled-values

Không dùng reflection, closure sorting hoặc expression parsing trên hot path.

## 11. Route precedence

Khi nhiều route match cùng request, tiêu chí trước thắng tiêu chí sau:

1. explicit priority lớn hơn;
2. exact host, rồi wildcard host, rồi hostless;
3. static path segment, rồi parameter, rồi prefix/catch-all;
4. nhiều static segments hơn;
5. path cụ thể/dài hơn;
6. nhiều header/query predicates hơn;
7. route ID nhỏ hơn theo byte-order.

Prefix và named catch-all có cùng routing specificity; nếu các tiêu chí trước vẫn hòa thì predicate count và route ID quyết định.

Route declaration order không ảnh hưởng kết quả.

Do priority thắng host/path specificity, matcher không dừng ở exact-host/static-path đầu tiên. Radix nodes giữ precedence upper-bound của subtree. Matcher duyệt nhánh có khả năng thắng trước và prune subtree không thể vượt best valid match.

Expected complexity:

- host lookup gần O(1);
- path traversal phụ thuộc số path segments;
- predicate work phụ thuộc số candidate routes cạnh tranh;
- không scan tuyến tính toàn bộ 100.000 route trong standard dataset.

Nhiều route cố tình dùng cùng host/path và chỉ khác predicates vẫn có worst case O(k). Collision-stress benchmark ghi nhận giới hạn này riêng, không trộn với standard scalability gate.

## 12. Predicate semantics

Operators Phase 2:

- `exists`;
- `not_exists`;
- `equals`;
- `not_equals`;
- `one_of`.

Semantics:

- header name không phân biệt hoa thường;
- header value so sánh byte-sensitive;
- query key/value phân biệt hoa thường;
- duplicate header/query values được giữ;
- `equals` và `one_of` thành công nếu ít nhất một value khớp;
- `not_equals` yêu cầu field tồn tại và không value nào khớp;
- `exists` thành công khi field có mặt, kể cả value rỗng;
- `not_exists` thành công khi field hoàn toàn không có.

Query scanner chỉ chạy nếu ít nhất một relevant candidate có query predicate. Scanner percent-decode theo URL query rules, bao gồm `+` thành space. Malformed percent encoding trả `400 INVALID_QUERY`.

## 13. Request context

Typed `RequestContext` chứa:

- snapshot revision và snapshot pointer;
- compiled route, service và upstream pointers;
- request ID;
- path parameter spans;
- response commit/error state;
- timing fields;
- plugin scratch slots.

Không dùng `map[string]any` làm kênh trao đổi. Plugin registry đăng ký scratch slot index khi compile. Scratch slice chỉ được tạo khi compiled chain thực sự yêu cầu; hai built-in Phase 2 dùng typed fields nên không cần scratch allocation.

Một private `context.Context` key được phép dùng để chuyển cùng `RequestContext` qua `httputil.ReverseProxy` hooks. Không tạo context value riêng cho từng field.

Plugin không được giữ request context hoặc request/response pointer sau khi hook trả về.

## 14. Plugin registry và precedence

Plugin registry được tạo lúc bootstrap và bất biến. Mỗi registration định nghĩa:

- stable name và version;
- execution phase và stable order;
- raw-config compiler;
- request/response runtime hooks;
- scratch-slot requirements.

Service plugin config được kế thừa bởi Route. Route attachment cùng tên thay thế toàn bộ Service config; không deep-merge. Route attachment `enabled: false` loại inherited plugin. Declaration order không ảnh hưởng execution order.

Compiler tạo flat request/response hook arrays. Hot path không resolve references, merge config hoặc sort plugins.

Contract khái niệm:

    Compile(rawConfig) -> immutable compiled plugin | error
    OnRequest(ctx, request) -> continue | short-circuit | error
    OnResponse(ctx, response) -> continue | error

Short-circuit result là typed value chứa status, headers, bounded body và stable error code. Plugin không tự ghi trực tiếp vào `ResponseWriter`.

Short-circuit và gateway-generated error responses vẫn chạy response hooks đã được thiết lập cho matched route. Vì vậy request ID và response header policy không bị mất trên error path. Hook gây ra lỗi không được chạy lại.

## 15. Built-in plugin: request-id

Mặc định dùng header `X-Request-ID`.

- Giữ inbound ID dài 1 đến 128 ký tự nếu chỉ chứa ký tự an toàn `[A-Za-z0-9._:-]`.
- Thiếu hoặc không hợp lệ thì sinh UUIDv4 từ cryptographically secure random source.
- Lưu ID trong typed request context.
- Forward ID lên upstream và trả ID trong response.
- Request hook chạy trước header rewrite.
- Response hook chạy cuối để correlation header luôn tồn tại.

Header name và maximum input length có thể cấu hình trong giới hạn compiler quy định.

## 16. Built-in plugin: header-rewrite

Plugin có operation groups riêng cho request và response:

- `remove` xóa toàn bộ values;
- `set` thay toàn bộ values;
- `add` giữ values hiện có rồi thêm values mới.

Header names và static values được validate/canonicalize khi compile. Cùng một normalized header không được xuất hiện trong nhiều groups của cùng direction.

Plugin không được sửa:

- `Host`;
- `Content-Length`;
- hop-by-hop headers;
- HTTP/2 pseudo headers.

Phase 2 không hỗ trợ variables, templates, regex hoặc runtime expression evaluation.

## 17. Request and response semantics

Phase 1 contracts được giữ:

- request/response streaming, trailers và cancellation;
- max body/header limits;
- Upgrade rejection;
- forwarding-header policy;
- stable upstream connection/timeout errors;
- outbound raw-query preservation;
- original downstream Host forwarding;
- graceful shutdown và drain.

Router dùng normalized `URL.Path`, nhưng proxy không gọi `path.Clean` hoặc tự ý rewrite path. Prefix, parameter và catch-all chỉ ảnh hưởng matching/capture.

Routing error rules:

- invalid request target: `400 INVALID_REQUEST`;
- invalid encoded query khi query predicates cần evaluate: `400 INVALID_QUERY`;
- no active snapshot: `503 GATEWAY_NOT_READY`;
- không route match host/path/predicates/method: `404 ROUTE_NOT_FOUND`;
- có route match host/path/predicates nhưng không method phù hợp: `405 METHOD_NOT_ALLOWED` với sorted/deduplicated `Allow` union;
- plugin failure trước response commit: `500 PLUGIN_EXECUTION_FAILED`;
- impossible runtime invariant failure: `500 INTERNAL_GATEWAY_ERROR`.

Nếu response đã commit, gateway không cố ghi JSON error lần hai. Nó ghi bounded log/metric và đóng stream khi protocol semantics yêu cầu.

## 18. Build errors và observability

`Apply` trả typed build error gồm:

- stable error code;
- build stage;
- target revision;
- resource kind và ID khi có;
- field path;
- human-readable cause.

Telemetry ghi:

- active snapshot revision;
- build/apply duration;
- successful và failed apply counters;
- failed build stage/error code;
- compiled route/service/plugin counts;
- estimated snapshot bytes khi measurement bật.

Metric labels không chứa arbitrary resource ID hoặc raw error text.

## 19. Test strategy

### 19.1. Unit và integration

- model validation và backward config adapter;
- host/path/method/predicate matching;
- complete precedence matrix;
- Service/Route plugin override và disable inheritance;
- request-id generation/preservation;
- request/response header rewrite;
- legacy Phase 1 proxy integration suite;
- HTTP/1.1 cleartext, HTTP/1.1 TLS và HTTP/2 TLS.

### 19.2. Reference and property tests

Một slow reference matcher độc lập được dùng làm oracle. Property tests sinh route/request sets ngẫu nhiên và yêu cầu compiled matcher trả cùng kết quả.

Acceptance run dùng ít nhất 10.000 generated cases với seed được ghi trong test output. CI luôn replay mọi regression seed đã từng phát hiện lỗi.

Các invariants:

- shuffle resource declaration order không đổi result;
- cùng request và snapshot luôn trả cùng route;
- matched route luôn thuộc candidate set;
- precedence result là total và deterministic;
- invalid config không publish snapshot.

### 19.3. Fuzz

Fuzz targets:

- host normalization;
- path-pattern compiler và matcher;
- query scanner;
- predicate compiler/evaluator;
- plugin raw-config compilers;
- v1alpha1/v1alpha2 config decoding.

Fuzz inputs không được panic, leak unbounded memory hoặc tạo compiled state vi phạm invariants.

Mỗi fuzz target replay corpus trong CI. Trước Phase 2 acceptance, mỗi target phải chạy tối thiểu năm phút trên reference build mà không sinh crash mới; crash inputs được commit vào regression corpus sau khi thu nhỏ.

### 19.4. Concurrent swap và race

Nhiều request goroutines chạy đồng thời với repeated `Apply`. Mỗi revision dùng route target và header marker khác nhau. Không response nào được chứa tổ hợp route/plugin/revision từ nhiều snapshots.

Acceptance stress run dùng ít nhất 32 request goroutines và 1.000 successful snapshot swaps, xen kẽ failed/stale revision attempts.

Long-lived request bắt đầu ở revision N phải hoàn tất bằng revision N dù revision N+1 đã active. Request mới sau swap dùng revision mới.

Snapshot/router/plugin core và integration swap suite phải pass Go race detector.

### 19.5. Memory steady state

Test build và activate ít nhất 20 full snapshots, bỏ references cũ, force GC tại test boundary rồi đo retained heap. Retained heap phải trở về tolerance của một active snapshot. Test cũng xác nhận fixed upstream runtimes không nhân bản theo revision.

## 20. Benchmark design

Standard dataset chứa 100.000 routes với documented deterministic seed và phân bố:

- host: 60% exact, 20% wildcard và 20% hostless;
- path: 50% static exact, 20% parameter, 15% prefix và 15% catch-all;
- 90% standard methods và 10% custom methods;
- 20% routes có một đến ba header/query predicates;
- 50% routes đi qua shared Services và 50% trỏ direct upstream;
- 10% routes bật request-id, 10% bật header-rewrite và phần còn lại plugin-free.

Generator dùng stable seed, loại duplicate match expressions và ghi seed, distribution counters, config checksum vào raw metadata. Ba sentinel routes đại diện first/middle/last trong deterministic source order; benchmark có thêm shuffled-input variant để chứng minh declaration order không ảnh hưởng result hoặc cost.

Microbench chạy ở 1, 1.000, 10.000 và 100.000 route, gồm first/middle/last lookup, miss, wildcard, parameter, catch-all, predicate hit/miss và collision-stress.

End-to-end matrix tối thiểu:

- HTTP/1.1 cleartext;
- HTTP/2 TLS;
- payload 1 KiB;
- 1-route baseline;
- 100.000-route first/middle/last;
- năm measurement repetitions;
- Go, APISIX và direct-upstream controls.

APISIX dùng cùng logical dataset. Translator và resulting config metadata phải được lưu cùng raw evidence để review semantic equivalence.

## 21. Performance gates

Reference profile cho absolute compile/memory gates là Linux x86_64 container với 8 dedicated logical CPUs, 16 GiB memory limit, Go 1.26.5, swap disabled và không chạy workload cạnh tranh. Exact CPU model, kernel, container runtime và image digest phải được ghi trong raw metadata. Docker Desktop có thể dùng cùng CPU/memory limits nhưng chỉ tạo provisional evidence.

Blocking gates:

- compile 100.000 standard routes trong không quá 5 giây trên reference Linux;
- router-only match không heap allocation khi không materialize path parameters;
- active standard snapshot không quá 512 MiB incremental heap, không tính canonical input retained bởi benchmark driver, fixed upstream runtimes hoặc load generator;
- sau 20 swaps và test-boundary GC, retained heap không quá 115% một active snapshot;
- Go 100.000-route end-to-end median throughput ít nhất 90% Go 1-route baseline trên cùng protocol/environment;
- Go 100.000-route median p99 không quá 110% Go 1-route baseline trên cùng protocol/environment;
- first/middle/last median-throughput spread không quá 5%;
- first/middle/last median-p99 spread không quá 10%;
- zero unexpected request errors trong valid benchmark runs.

Docker Desktop được phép tạo provisional Phase 2 evidence cho relative scalability gates khi reproducibility checks pass. Dedicated Linux vẫn là nguồn chứng nhận tuyệt đối.

APISIX throughput, p99 và memory được ghi trong comparative report. Absolute APISIX parity không phải Phase 2 blocking gate vì Phase 1 đã xác nhận có HTTP-engine baseline gap; Phase 2 không được dùng gap đó để đánh giá sai router scalability.

## 22. Exit criteria

Phase 2 hoàn tất khi:

- `gateway/v1alpha1` compatibility và `gateway/v1alpha2` strict decoding pass;
- 100.000 routes compile và match đúng precedence;
- request không thấy partial revision trong concurrent update tests;
- không global mutex, config parsing, reference resolution hoặc plugin sorting trên hot path;
- unit, integration, property, fuzz regression corpus và race suites pass;
- request-id và header-rewrite hoạt động ở request/response phases;
- memory steady-state gate pass;
- relative scalability gates pass;
- APISIX comparative report và raw evidence được lưu;
- operations/benchmark documentation được cập nhật.

## 23. Delivery slices

1. Canonical model, v1alpha2 decoder và v1alpha1 backward adapter.
2. Immutable snapshot builder và manager.
3. Router compiler, matcher và reference/property tests.
4. Plugin registry, request context và built-in plugins.
5. Gateway/proxy integration giữ nguyên Phase 1 semantics.
6. Concurrent-swap, race và memory suites.
7. 100.000-route benchmark harness và APISIX comparative report.
8. Operational documentation và Phase 2 acceptance record.

Mỗi slice phải giữ main test suite xanh và không tạo external configuration update surface trước Phase 4.

## 24. Handoff sang Phase 3

Phase 3 nhận các contracts đã khóa:

- canonical resource IDs và resolved upstream references;
- immutable snapshot/request-consistency rules;
- compiled router/plugin pipeline;
- fixed upstream runtime boundary;
- benchmark raw-result/report conventions.

Phase 3 thay fixed upstream table bằng lifecycle-aware `UpstreamRuntimeRegistry`, thêm balancing, health checks và retries. Nó không được đưa mutable upstream state vào `RuntimeSnapshot` hoặc request route-matching path.

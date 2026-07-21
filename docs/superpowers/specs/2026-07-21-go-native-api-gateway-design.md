# Thiết kế API Gateway Go-native

- Ngày: 2026-07-21
- Trạng thái: North-star architecture đã được duyệt
- Phạm vi: Kiến trúc mục tiêu qua nhiều delivery phase

Roadmap triển khai được quản lý tại [go-native-api-gateway-phase-roadmap-design.md](2026-07-21-go-native-api-gateway-phase-roadmap-design.md). Tài liệu này định nghĩa các invariant và trạng thái đích; không được dùng trực tiếp như một implementation plan duy nhất.

## 1. Bối cảnh

Dự án xây dựng một API gateway bằng Go có năng lực và đặc tính vận hành tương đương Apache APISIX, nhưng không phải drop-in replacement. Thiết kế kế thừa các nguyên tắc tốt của APISIX:

- control plane không nằm trên request hot path;
- request chỉ dùng cấu hình đã được chuẩn bị trong memory;
- route, plugin chain và upstream được cập nhật động mà không restart;
- cấu hình được version hóa và mỗi data plane kích hoạt nguyên tử;
- data plane tiếp tục phục vụ bằng last-known-good state khi configuration plane hỏng.

Phân tích APISIX làm nền cho thiết kế nằm tại [apache-api-six-architecture-design.md](../../architecture/apache-api-six-architecture-design.md). Source APISIX được đối chiếu tại commit 0f35b154824ed51d0d4bdadc78d1d5fa9ed1ec62.

Thiết kế không sao chép Lua runtime, Nginx phase model hoặc Admin API schema của APISIX. Các contract và runtime được thiết kế thuần Go.

## 2. Mục tiêu và tiêu chí

### 2.1. Mục tiêu sản phẩm

- Workload chính là REST/JSON với request ngắn và payload nhỏ đến vừa.
- Hỗ trợ HTTP/1.1 và HTTP/2; WebSocket phải proxy đúng nhưng chưa cần tối ưu riêng.
- Chạy dưới dạng Linux container, Kubernetes-first nhưng không phụ thuộc Kubernetes.
- REST Admin API là giao diện cấu hình chính.
- Plugin v1 là built-in Go code compile cùng data-plane binary.
- Data plane không truy cập trực tiếp etcd.
- Configuration convergence lag của các data plane connected và healthy có p99 không quá một giây.
- Design envelope là 100.000 route, 1.000 data-plane instance và 100 configuration update mỗi giây.

### 2.2. Mục tiêu hiệu năng

Gateway Go được so sánh trực tiếp với APISIX trên cùng:

- host và CPU allocation;
- upstream;
- route, TLS và plugin configuration;
- payload, concurrency và keepalive;
- kernel và network tuning.

Trong từng workload acceptance:

- median throughput của gateway Go không thấp hơn APISIX;
- p99 latency không lớn hơn 110% p99 của APISIX;
- error rate không cao hơn APISIX;
- snapshot activation không tạo request error.

### 2.3. Nguyên tắc thiết kế

- Correctness trước, tối ưu theo profiling.
- Không có configuration I/O, JSON parsing, sorting hoặc reference resolution trên request path.
- State cấu hình là immutable; state vận hành dài hạn được quản lý riêng.
- Không có unbounded queue hoặc unbounded buffering.
- Failure ngoài hot path làm đóng băng thay đổi, không làm gián đoạn traffic đang dùng snapshot hợp lệ.
- Semantics cấu hình phải xác định, quan sát được và dễ rollback.

## 3. Các phương án HTTP runtime

Ba phương án đã được xem xét:

1. Go net/http và tối ưu dựa trên benchmark.
2. fasthttp từ đầu.
3. Tự xây HTTP parser và event-loop engine.

Thiết kế chọn phương án 1 với Go 1.26:

- net/http cung cấp HTTP/1.1, HTTP/2, streaming, cancellation và protocol correctness;
- httputil.ReverseProxy có thể làm implementation và correctness baseline ban đầu;
- routing, plugin pipeline, load balancing, retry và observability thuộc gateway core riêng;
- chỉ xem xét đổi HTTP engine khi profiling chứng minh bottleneck nằm trong net/http.

Không chọn fasthttp cho v1 vì thiếu HTTP/2 native, streaming semantics hạn chế hơn và API lifecycle làm tăng rủi ro đối với reverse proxy tổng quát. Không tự xây HTTP stack vì chi phí correctness, security và maintenance không phù hợp vertical slice.

## 4. Topology

Hệ thống có hai binary độc lập:

- gateway-cp: control plane;
- gateway-dp: data plane.

Hai binary dùng chung model và API contracts nhưng không import implementation nội bộ của nhau.

Luồng cấu hình:

    Operator
      -> REST Admin API
      -> authentication và RBAC
      -> schema/reference validation
      -> etcd transaction
      -> configuration event
      -> versioned gRPC distribution
      -> DP builds shadow runtime snapshot
      -> atomic activation

Luồng traffic:

    net/http Server
      -> load atomic runtime snapshot
      -> route match
      -> precompiled plugin chain
      -> upstream selection
      -> pooled http.Transport
      -> response phases
      -> bounded telemetry

### 4.1. Control plane

Control plane:

- cung cấp REST Admin API;
- xác thực, phân quyền và audit mutation;
- validate schema, plugin config và resource references;
- ghi etcd bằng transaction;
- theo dõi revision của etcd;
- phát full snapshot hoặc delta qua gRPC stream dùng mTLS;
- nhận ACK/NACK và tổng hợp rollout status;
- không tham gia xử lý traffic.

CP instance là stateless ngoài các stream đang giữ. Mọi instance có thể phục vụ bất kỳ DP nào. Mỗi CP theo dõi etcd và phân phối state tới các DP đang kết nối với nó.

### 4.2. Data plane

Data plane:

- không có etcd credential;
- nhận full snapshot lúc bootstrap/recovery và delta ở steady state;
- build route tree, plugin chain và upstream tables ngoài hot path;
- kích hoạt revision bằng một atomic pointer swap;
- lưu last-known-good snapshot đã mã hóa xuống local disk;
- tiếp tục phục vụ khi CP hoặc etcd unavailable.

Request đang chạy giữ snapshot đã load lúc bắt đầu. Request mới dùng snapshot mới. Không có global mutex trên request path.

### 4.3. Network boundaries

- Admin listener, distribution listener và traffic listener tách port.
- CP và DP có network policy độc lập.
- External load balancer đặt trước CP và DP.
- CP đến etcd dùng TLS và RBAC.
- CP đến DP dùng mutual TLS và certificate rotation.

## 5. Resource model

REST API v1:

    /admin/v1/routes
    /admin/v1/services
    /admin/v1/upstreams
    /admin/v1/consumers
    /admin/v1/plugin-configs
    /admin/v1/certificates
    /admin/v1/transactions
    /admin/v1/rollouts/{revision}

### 5.1. Resources

Route:

- host, path, method, header và query conditions;
- explicit priority;
- plugin attachment;
- chính xác một trong hai: service_ref hoặc upstream_ref.

Service:

- cấu hình dùng chung cho nhiều route;
- plugin attachment;
- upstream_ref.

Upstream:

- endpoints và weight;
- balancing policy;
- connect, response và total timeout;
- retry policy;
- active và passive health-check policy;
- upstream TLS policy.

Consumer:

- identity;
- credential references;
- consumer-specific policies.

PluginConfig:

- tập cấu hình plugin tái sử dụng.

Certificate:

- SNI patterns;
- certificate chain;
- private-key SecretRef;
- downstream mTLS policy.

Mỗi resource có id, generation, created_at, updated_at, labels và spec. ID là string ổn định do client cung cấp hoặc CP tạo.

### 5.2. Optimistic concurrency

Update và delete yêu cầu If-Match theo generation. Generation mismatch trả 409 để tránh operator ghi đè lẫn nhau.

Một thay đổi đơn-resource dùng một etcd transaction. Thay đổi nhiều resource liên quan dùng /transactions và có chung một logical revision. DP kích hoạt toàn bộ revision hoặc giữ nguyên snapshot cũ.

Admin API trả thành công khi etcd transaction đã commit, không chờ 1.000 DP. Response chứa revision và rollout URL.

### 5.3. Plugin configuration precedence

Thứ tự cấu hình:

    system defaults
      < Service
      < referenced PluginConfig
      < Route
      < Consumer policy after authentication

Nếu cùng một plugin xuất hiện ở nhiều scope, scope cụ thể hơn thay thế toàn bộ config. Không deep-merge từng field.

Plugin chain được compile sẵn theo phase:

    request-normalize
      -> pre-auth
      -> authentication
      -> post-auth policy
      -> upstream selection
      -> response headers
      -> logging

Authentication có thể gắn Consumer vào request context; consumer policy chỉ chạy sau authentication.

## 6. Configuration distribution

### 6.1. Handshake

Khi mở stream, DP gửi:

- node ID và binary version;
- distribution protocol versions;
- supported resource schema versions;
- built-in plugin names và versions;
- zone và labels;
- current active revision.

CP chỉ phân phối state tương thích. CP và DP hỗ trợ protocol/schema version hiện tại và một version liền trước để rolling upgrade.

### 6.2. Snapshot và delta

Full snapshot dùng cho:

- bootstrap;
- reconnect khi DP không có revision phù hợp;
- checksum mismatch;
- delta gap;
- slow consumer recovery.

Delta message chứa:

    base_revision
    target_revision
    upserts[]
    deletes[]
    schema_version
    state_checksum

DP chỉ nhận delta khi base_revision trùng revision hiện tại. Delta được áp dụng vào shadow configuration. DP build toàn bộ runtime indexes cần thay đổi, chạy invariants, rồi atomic-swap.

### 6.3. Backpressure

- Mỗi DP có bounded send queue.
- CP có thể coalesce các revision trung gian thành target state mới hơn.
- Khi delta gap lớn, CP bỏ queue cũ và gửi full snapshot.
- Revision luôn tăng; không rollback ngầm.
- Ở 100 update/giây, latest correct state quan trọng hơn việc DP kích hoạt mọi revision trung gian.

### 6.4. ACK/NACK

ACK/NACK chứa:

- node ID;
- target revision;
- active revision;
- build/apply duration;
- structured error hoặc warning.

Rollout API tổng hợp pending, building, active, nacked và offline. SLO p99 không quá một giây chỉ áp dụng cho DP connected và healthy; DP offline không làm Admin API treo.

SLO đo convergence lag, không yêu cầu mọi revision trung gian đều được kích hoạt. Khi update liên tục và CP coalesce revision, phép đo dùng thời gian từ latest desired revision tại CP đến lúc DP kích hoạt target revision tương ứng. Revision trung gian bị coalesce không được tính là activation failure.

## 7. Data-plane request pipeline

### 7.1. Listener và request normalization

gateway-dp dùng net/http.Server:

- HTTP/1.1 và HTTP/2;
- dynamic TLS certificate selection theo SNI;
- giới hạn request-line, header count, header size và body size;
- graceful shutdown và connection draining.

Trước route match, DP:

- canonicalize authority và host;
- validate và normalize path;
- phân loại hop-by-hop headers;
- chỉ tin Forwarded hoặc X-Forwarded headers từ trusted_proxies;
- giữ cả raw path và normalized path để router và plugin không diễn giải khác nhau.

### 7.2. Routing

Router được compile ngoài hot path:

    exact host index
      -> wildcard host index
      -> path radix tree
      -> method bitmask
      -> compiled header/query predicates

Thứ tự route, tiêu chí trước thắng tiêu chí sau:

1. explicit priority, giá trị số lớn hơn thắng;
2. host specificity;
3. path specificity;
4. predicate count;
5. route ID.

CP từ chối hai route có match expression và priority giống hệt nhau.

### 7.3. Request context

RequestContext nhẹ chứa:

- snapshot revision;
- route, service và upstream ID;
- client identity và selected consumer;
- retry state;
- timestamps;
- plugin scratch slots.

Không dùng map string-to-any làm kênh trao đổi chính. Built-in plugin đăng ký slot index khi compile; request truy cập slice theo index.

### 7.4. Plugin contract

Plugin tách compile và execute:

    Compile(raw config) -> immutable compiled config
    Execute(compiled config, request context) -> continue hoặc short-circuit

Snapshot giữ sẵn ordered function/config slices theo phase. Hot path không parse JSON, resolve reference hoặc sort plugin.

Plugin có thể short-circuit với response hoàn chỉnh. Response streaming là mặc định. Plugin cần đọc hoặc sửa toàn body phải khai báo RequiresBufferedResponse và byte limit. Không buffer vô hạn.

## 8. Upstream runtime

### 8.1. Config state và runtime state

Immutable snapshot chứa upstream configuration. UpstreamRuntimeRegistry chứa state sống lâu:

- HTTP transports và connection pools;
- active/passive health;
- balancer counters;
- endpoint latency;
- concurrency state.

Registry keyed theo stable upstream và endpoint identity để config update không phá keepalive pool không liên quan.

Transport được chia sẻ theo transport profile, gồm protocol, TLS policy và connection limits. Không tạo transport theo request hoặc route.

### 8.2. V1 load balancing

- weighted round-robin;
- consistent hash;
- active HTTP/TCP health check;
- passive failure tracking.

Mỗi attempt chọn trong eligible healthy endpoints. Tất cả endpoint unhealthy trả 503; không fail-open tới endpoint unhealthy nếu operator không bật rõ.

### 8.3. Retry

Retry yêu cầu:

- method replayable hoặc operator cho phép rõ;
- chưa gửi response bytes xuống client;
- request body rỗng hoặc có replay source;
- còn retry budget và deadline;
- còn endpoint phù hợp.

GET, HEAD và OPTIONS có thể retry mặc định. POST và PUT không tự retry trừ khi body nằm trong replay buffer limit và policy bật rõ.

Gateway phân biệt:

- connect failure;
- upstream reset;
- response timeout;
- retry exhaustion;
- all endpoints unhealthy.

Các trường hợp được ánh xạ nhất quán sang 502, 503 hoặc 504 và structured telemetry.

## 9. Built-in plugins v1

- request-id;
- key-auth;
- jwt-auth;
- local token-bucket rate limit;
- Redis-backed cluster-wide rate limit;
- request/response header rewrite;
- CORS;
- Prometheus metrics;
- structured access log.

Local rate limit là per-DP và không hứa global exact count. Redis mode cung cấp cluster-wide semantics, có timeout, concurrency limit và fail-open/fail-closed policy. Benchmark hot-path chính dùng local mode; Redis mode có benchmark riêng vì phụ thuộc external store.

Plugin có external I/O phải có timeout, concurrency cap và failure policy. Panic được recover tại request boundary và ghi plugin/route/revision. Gateway trả 500 nếu response chưa commit; nếu response đã commit thì kết thúc stream/connection theo protocol. Panic không làm process chết.

## 10. Observability

Mỗi request có request ID và ghi:

- active revision;
- route, service, upstream và consumer IDs;
- selected endpoint và attempt count;
- status, bytes và latency breakdown;
- plugin short-circuit hoặc error code.

Metrics tối thiểu:

- request count và latency;
- gateway-only và upstream latency;
- status theo route/service với cardinality budget;
- active connections;
- retry count;
- healthy/unhealthy endpoints;
- config revision lag;
- snapshot build/apply duration;
- distribution reconnect và ACK/NACK;
- queue drops;
- Go runtime, GC và memory.

Metrics cập nhật đồng bộ bằng primitives phù hợp. Access-log exporter dùng bounded queue. Trace exporter chưa thuộc phạm vi v1, nhưng khi được bổ sung phải dùng cùng nguyên tắc bounded queue. Exporter chậm dẫn đến drop/sample theo policy, không block request hoặc tăng memory vô hạn.

## 11. Failure semantics

### 11.1. Admin API

- 400: request hoặc schema không hợp lệ.
- 401/403: authentication hoặc authorization.
- 404: resource/reference không tồn tại.
- 409: generation conflict hoặc referenced resource.
- 422: cấu hình không compile được.
- 503: etcd không thể commit.

Error response có stable code, message, resource, field và request_id.

### 11.2. Snapshot apply

Apply pipeline:

    receive
      -> verify signature/checksum
      -> validate protocol/schema/plugin capabilities
      -> build shadow snapshot
      -> run invariants
      -> atomic swap
      -> persist
      -> ACK

Lỗi trước atomic swap gửi NACK và giữ last-known-good snapshot. Persist lỗi sau activation không rollback traffic; DP chuyển ready_degraded và ACK kèm warning.

### 11.3. Component failures

- CP hỏng: DP reconnect CP khác; traffic tiếp tục; config update tạm dừng.
- etcd hỏng: Admin mutation trả 503; state đã active tiếp tục chạy.
- DP restart khi CP hỏng: dùng encrypted local snapshot.
- Upstream hỏng: health state và safe retry xử lý; hết endpoint trả 503.
- Telemetry hỏng: drop/sample trên bounded queue.
- Plugin external dependency hỏng: áp dụng configured fail-open/fail-closed policy.

Readiness:

- ready: snapshot hợp lệ và CP connection bình thường;
- ready_degraded: snapshot hợp lệ nhưng CP hoặc persistence lỗi;
- not_ready: chưa có snapshot hợp lệ hoặc runtime invariant lỗi.

## 12. Security

- Admin API dùng TLS, JWT/OIDC, RBAC và audit log.
- CP-DP dùng mutual TLS và certificate rotation.
- Etcd chỉ mở cho CP bằng TLS/RBAC.
- Configuration message và persisted snapshot được CP ký; DP tin các public key được cấu hình và hỗ trợ key rotation.
- Credential, private key và plugin secret dùng SecretRef.
- Secret trong etcd dùng envelope encryption qua provider interface.
- Local snapshot chỉ bật khi có node-local encryption key từ mounted secret hoặc KMS.
- Secret không xuất hiện trong Admin read response, log, metric hoặc NACK payload.
- Hop-by-hop headers bị loại bỏ đúng protocol.
- Trusted forwarding headers được dựng lại từ trusted data.
- Router và plugin dùng chung normalized representation.
- Listener có timeout, size limit, connection limit và backpressure.

## 13. Shutdown và draining

Khi shutdown:

1. chuyển not_ready;
2. ngừng nhận connection mới;
3. drain request đang chạy;
4. gửi HTTP/2 GOAWAY;
5. đóng WebSocket sau drain timeout;
6. flush bounded telemetry trong giới hạn;
7. thoát khi hết deadline.

## 14. Module map và seams

    cmd/
      gateway-cp
      gateway-dp

    internal/
      config
      configcodec
      model
      gateway
      snapshot
      router
      plugin
      upstream
      proxy
      telemetry
      controlplane/
        admin
        auth
        store
        validation
        distribution

    api/
      admin
      distribution

Dependency rules:

- internal/model và versioned wire contracts không phụ thuộc CP/DP implementations;
- CP không import data-plane implementation;
- DP không import etcd client;
- router không biết Admin API hoặc distribution transport;
- plugin không truy cập global mutable configuration;
- proxy nhận runtime objects đã resolve;
- telemetry nhận structured events và không sở hữu request flow.
- Không tạo package shared, common hoặc utils; mỗi Module phải có domain owner rõ.

Seam chỉ đặt tại nơi behavior thực sự thay đổi: config store, distribution transport, secret provider, load-balancing policy, plugin và telemetry exporter. Không tạo Go interface cho mọi Module hoặc khi chỉ có một Adapter.

## 15. Testing

### 15.1. Unit, property và fuzz

- Route precedence, wildcard, normalization và predicates.
- Plugin phase order, override và short-circuit.
- Load-balancer distribution, health state và retry.
- Revision ordering, delta gap, checksum và snapshot swap.
- Optimistic concurrency và resource transaction.
- Property: cùng request và snapshot luôn cho cùng route.
- Fuzz HTTP normalization, config decoding, snapshot/delta decoding, SNI và malformed upstream response.

### 15.2. Integration

Topology test gồm etcd, hai CP, nhiều DP và programmable upstream.

Các luồng bắt buộc:

- Admin write đến ACK;
- bootstrap, delta và gap recovery;
- invalid revision NACK;
- CP failover;
- local snapshot restart;
- upstream failure và recovery;
- TLS/mTLS rotation;
- rolling upgrade lệch một version.

### 15.3. Concurrency và chaos

- Go race detector cho core packages.
- Kill CP hoặc etcd leader trong lúc có traffic.
- Packet loss, slow DP và reconnect storm.
- Corrupt snapshot và disk persistence failure.
- Làm đầy telemetry queue.
- 100 update/giây dưới continuous request load.
- Không partial config, data race hoặc unbounded memory.

## 16. Comparative benchmark

Workloads:

1. Một route, không plugin, HTTP/1.1 keepalive.
2. Một route qua TLS.
3. 100.000 route, match đầu/giữa/cuối.
4. Authentication, local rate limit và metrics.
5. Weighted upstream với health check và retry.
6. HTTP/2 concurrent streams.
7. Stable traffic cùng 100 config update/giây.

Payload: 0 B, 1 KiB, 16 KiB và 64 KiB.

Mỗi scenario có warm-up và ít nhất năm measurement run. Báo cáo throughput, p50/p95/p99/p99.9, error rate, CPU/core, RSS, allocation/request, GC pause, connection reuse, config propagation và latency spike lúc swap.

Nếu không đạt:

1. profile CPU, allocation, block và mutex;
2. tối ưu router, header copy, buffer và transport pool;
3. benchmark lại sau từng thay đổi;
4. chỉ thay HTTP engine sau một ADR có số liệu chứng minh net/http là bottleneck.

## 17. Phạm vi v1

### 17.1. Bao gồm

- REST Admin API, RBAC và audit.
- etcd persistence và resource transactions.
- Full/delta gRPC distribution, ACK/NACK và rollout status.
- HTTP/1.1, HTTP/2, TLS/SNI và WebSocket proxying.
- Dynamic routing và built-in plugin chain.
- Weighted round-robin, consistent hash, health check và retry.
- Prometheus metrics, access logs và encrypted last-known-good snapshot.

### 17.2. Không bao gồm

- Lua compatibility.
- WASM hoặc external plugin runner.
- TCP/UDP stream proxy.
- HTTP/3/QUIC.
- Kubernetes Gateway API/CRD.
- Service-registry discovery.
- WAF, response cache và traffic mirroring.
- Admin UI.
- Multi-region active-active control plane.
- APISIX Admin API/schema compatibility.
- User-supplied Go code hoặc dynamic scripts.

## 18. Definition of done

V1 hoàn thành khi chứng minh end-to-end:

    Admin transaction
      -> etcd commit
      -> CP distribution
      -> 1.000 simulated DP activation
      -> HTTP route match
      -> authentication và rate limit
      -> healthy upstream selection
      -> proxied response
      -> metrics và access log

Đồng thời:

- tất cả correctness, integration, race và chaos gates đạt;
- comparative benchmark gates đạt;
- configuration convergence lag p99 không quá một giây với 1.000 connected DP và 100 update/giây;
- CP/etcd outage không làm gián đoạn traffic đang dùng snapshot hợp lệ;
- memory đạt steady state và không tăng theo số revision.

## 19. Các quyết định đã khóa

1. Go-native capability equivalence, không drop-in compatibility.
2. Vertical slice production tối thiểu.
3. REST/JSON là workload chính.
4. Benchmark tương đối trực tiếp với APISIX.
5. Chỉ CP truy cập etcd.
6. Built-in Go plugins trong v1.
7. Linux container, Kubernetes-first nhưng portable.
8. REST Admin API là configuration interface chính.
9. Configuration convergence lag p99 không quá một giây; revision trung gian có thể được coalesce.
10. Design envelope 100.000 route, 1.000 DP và 100 update/giây.
11. Go net/http là HTTP foundation; thay engine chỉ dựa trên benchmark và ADR.
12. Delivery dùng bảy risk-first vertical phase; mỗi phase có spec, plan, implementation và verification riêng.

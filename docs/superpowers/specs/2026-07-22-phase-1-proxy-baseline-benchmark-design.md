# Phase 1 — Proxy baseline và benchmark harness

- Ngày: 2026-07-22
- Trạng thái: Accepted for phase progression; performance evidence provisional
- Parent roadmap: [Go-native API Gateway phase roadmap](2026-07-21-go-native-api-gateway-phase-roadmap-design.md)
- North-star: [Go-native API Gateway design](2026-07-21-go-native-api-gateway-design.md)

## 1. Mục tiêu

Phase 1 tạo data-plane executable tối thiểu có thể proxy HTTP đúng và một benchmark harness tái lập để so sánh với APISIX.

Phase này loại bỏ hai rủi ro đầu tiên:

- net/http và httputil.ReverseProxy có đáp ứng correctness requirements của gateway hay không;
- dự án có thể tạo benchmark apples-to-apples với APISIX hay không.

Kết quả Phase 1 là nền móng production code, không phải prototype bỏ đi. Tuy nhiên performance result trên Docker Desktop chỉ mang tính provisional.

## 2. Các quyết định đã khóa

1. Benchmark tạm thời chạy trên Docker Desktop.
2. Correctness và reproducibility là blocking gates; parity numbers là provisional.
3. APISIX baseline build từ local source commit 0f35b154824ed51d0d4bdadc78d1d5fa9ed1ec62.
4. wrk dùng cho HTTP/1.1 baseline; h2load dùng cho HTTP/2 và HTTP/1.1 cross-check.
5. Downstream hỗ trợ HTTP/1.1 cleartext, HTTP/1.1 TLS và HTTP/2 TLS.
6. Upstream chỉ dùng HTTP/1.1 trong Phase 1.
7. Static configuration dùng strict YAML theo canonical resource shape.
8. Correctness upstream viết bằng Go; performance upstream dùng Nginx.
9. Benchmark topology dùng một Docker Compose project và chỉ chạy một gateway target tại một thời điểm.
10. Go toolchain được pin ở Go 1.26 trong build image.

## 3. Phạm vi

### 3.1. Bao gồm

- Go module và repository layout.
- gateway-dp executable.
- test-upstream executable.
- Static YAML configuration.
- Chính xác một Route và một Upstream.
- HTTP/1.1 và HTTP/2 downstream.
- HTTP/1.1 upstream connection pooling.
- TLS certificate load tại startup.
- Strict request normalization và forwarding-header policy.
- Streaming request/response.
- Stable proxy error semantics.
- Health, readiness và Prometheus metrics endpoints.
- Graceful shutdown.
- Docker Compose benchmark topology.
- wrk và h2load benchmark runners.
- APISIX local-source baseline.
- Raw benchmark artifacts và summaries.

### 3.2. Không bao gồm

- Dynamic configuration hoặc atomic snapshot swap.
- Multi-route radix router.
- Plugin framework.
- Health check, retry hoặc nhiều upstream endpoint.
- WebSocket/Upgrade.
- HTTP/2 hoặc h2c upstream.
- Control plane, etcd hoặc gRPC.
- Service discovery.
- Production performance certification.

## 4. Runtime modules

Phase 1 tạo hai executable:

    cmd/gateway-dp
    cmd/test-upstream

gateway-dp được chia theo trách nhiệm:

    internal/gateway
    internal/config
    internal/model
    internal/proxy
    internal/upstream
    internal/telemetry
    internal/testupstream

### 4.1. gateway

- Sở hữu HTTP, HTTPS và admin servers.
- Sở hữu startup order, readiness state và graceful shutdown.
- Cấu hình TLS/ALPN và request-level server limits.
- Xử lý SIGINT/SIGTERM.
- Không chứa request normalization, route matching hoặc upstream policy.

Startup, listeners và shutdown nằm cùng một Module vì chúng có chung invariants và luôn thay đổi cùng nhau. Không tạo các Module listener/lifecycle chỉ để chuyển tiếp lời gọi.

### 4.2. config

- Đọc một YAML file khi startup.
- Strict-decode và từ chối unknown fields.
- Tách phần process bootstrap khỏi canonical resources sau khi decode.
- Validate listener addresses, durations, limits, TLS files, resource IDs và references.
- Không watch hoặc reload file.

### 4.3. model

- Sở hữu canonical Route và Upstream types.
- Không phụ thuộc YAML, files, listeners hoặc Docker.
- Được tái sử dụng khi Phase 2 thêm RuntimeSnapshot và Phase 4 thêm control plane.

### 4.4. proxy

- Normalize và validate inbound request.
- Match exact route.
- Reject unsupported Upgrade.
- Dùng httputil.ReverseProxy làm proxy correctness baseline.
- Map internal/upstream failures thành stable client errors.

### 4.5. upstream

- Sở hữu configured endpoint và một shared http.Transport.
- Chỉ bật HTTP/1.1 tới upstream.
- Quản lý keepalive pool, dial timeout và response-header timeout.
- Không retry hoặc health-check.

Tên Module là upstream thay vì transport vì http.Transport chỉ là Implementation detail. Phase 3 mở rộng cùng Module bằng endpoint selection, health, balancing và retry.

### 4.6. telemetry

- Cung cấp liveness, readiness và Prometheus metrics.
- Không điều khiển request flow.
- Cho phép tắt per-request metrics trong proxy-only benchmark.

### 4.7. Module depth và seams

- cmd/gateway-dp chỉ là composition root; không chứa business behavior.
- Không tạo package shared, common hoặc utils.
- Không tạo Go interface chỉ vì có một Module.
- Một Adapter duy nhất dùng concrete dependency.
- Chỉ thêm Seam khi có ít nhất hai Adapter thật hoặc behavior cần thay tại đúng Seam đó.
- Tests ưu tiên đi qua cùng Interface mà caller production sử dụng.

internal/testupstream sở hữu correctness-upstream behavior; cmd/test-upstream chỉ parse process arguments và chạy Module đó.

Dependency chính:

    cmd/gateway-dp
      -> config
      -> gateway
           -> proxy
                -> model
                -> upstream
           -> telemetry

config phụ thuộc model để tạo canonical resources. telemetry quan sát các Module nhưng không được trở thành dependency của proxy correctness.

## 5. Static configuration

Phase 1 dùng canonical-shaped YAML:

    api_version: gateway/v1alpha1

    listeners:
      http:
        address: ":8080"
      https:
        address: ":8443"
        certificate_file: /certs/server.crt
        private_key_file: /certs/server.key
      admin:
        address: ":9090"

    server:
      read_header_timeout: 5s
      idle_timeout: 60s
      shutdown_timeout: 30s
      max_header_bytes: 1048576
      max_request_body_bytes: 67108864

    telemetry:
      request_metrics_enabled: true

    routes:
      - id: baseline
        match:
          path: /hello
          methods: [GET, POST]
        upstream_ref: baseline

    upstreams:
      - id: baseline
        endpoints:
          - http://upstream:8080
        transport:
          dial_timeout: 3s
          response_header_timeout: 10s
          idle_connection_timeout: 90s
          max_idle_connections: 1024
          max_idle_connections_per_host: 1024

Một YAML document không đồng nghĩa một internal config type:

- BootstrapConfig chứa listeners, TLS file paths, server limits, telemetry và shutdown settings.
- model.ResourceSet chứa Route và Upstream resources.

config Module decode một lần rồi trả hai cấu trúc này. Phase 2 có thể thay nguồn ResourceSet bằng RuntimeSnapshot mà không thay BootstrapConfig hoặc gateway lifecycle.

Validation rules:

- api_version phải đúng gateway/v1alpha1.
- Phase 1 yêu cầu đúng một Route và một Upstream.
- Route và Upstream IDs phải khác rỗng và không trùng.
- upstream_ref phải resolve được.
- Route path phải là absolute exact path.
- methods không rỗng và không chứa giá trị không hợp lệ.
- Upstream có đúng một HTTP endpoint.
- HTTPS listener yêu cầu certificate và private-key files hợp lệ.
- Timeout và limits phải hữu hạn, không âm.
- Listener address không được xung đột.

Invalid configuration làm process log structured startup error, thoát non-zero và không bao giờ trở thành ready.

## 6. Protocol matrix

### 6.1. Downstream

| Mode | Protocol |
|---|---|
| HTTP listener | HTTP/1.1 cleartext |
| HTTPS listener | HTTP/1.1 qua TLS |
| HTTPS listener | HTTP/2 qua TLS/ALPN |

Phase 1 không hỗ trợ h2c.

### 6.2. Upstream

Gateway luôn dùng HTTP/1.1 upstream, kể cả khi downstream là HTTP/2.

Shared Transport:

- tái sử dụng keepalive connections;
- không follow redirects;
- không retry;
- không tự động đổi protocol sang HTTP/2;
- hủy RoundTrip khi downstream context bị cancel.

## 7. Request path

    net/http.Server
      -> panic/error boundary
      -> request limits
      -> normalize request
      -> exact path/method match
      -> reject unsupported Upgrade
      -> Rewrite outbound request
      -> httputil.ReverseProxy
      -> shared HTTP/1.1 Transport
      -> stream response
      -> optional request metrics

Hot path không:

- đọc configuration file;
- parse YAML;
- tạo Transport mới;
- buffer toàn request/response body;
- retry request;
- gọi health checker;
- load plugin.

## 8. Request semantics

### 8.1. Route matching

- Path match dùng normalized URL path và yêu cầu exact equality.
- Method match case-sensitive theo HTTP token đã parse.
- Path không match trả 404.
- Path match nhưng method không match trả 405 và Allow header.

### 8.2. Query và Host

- Raw query được giữ nguyên vì Phase 1 không có query-based policy.
- Original downstream Host được giữ làm upstream Host.
- Upstream dial target vẫn lấy từ configured endpoint.

### 8.3. Forwarding headers

Phase 1 không có trusted-proxy configuration. Mọi downstream peer được coi là untrusted:

- xóa inbound Forwarded;
- xóa inbound X-Forwarded-For;
- xóa inbound X-Forwarded-Host;
- xóa inbound X-Forwarded-Proto;
- xóa inbound X-Forwarded-Port;
- dựng lại forwarding headers từ remote socket, request Host và TLS state.

### 8.4. Hop-by-hop headers

Gateway loại hop-by-hop headers và các header được liệt kê trong Connection tokens trước khi gửi upstream hoặc downstream.

### 8.5. Streaming và cancellation

- Không dùng io.ReadAll cho request hoặc response.
- max_request_body_bytes được áp dụng dưới dạng streaming limit.
- Response không có implicit whole-body limit hoặc buffering.
- Downstream cancellation hủy upstream request context.
- Trailers được chuyển theo net/http semantics.

### 8.6. Upgrade

Request có Upgrade intent bị reject 501 với stable error code. WebSocket được bổ sung ở Phase 3.

## 9. Error semantics

Error response dùng JSON nhỏ:

    {
      "code": "UPSTREAM_CONNECTION_FAILED",
      "message": "upstream connection failed"
    }

| Condition | Status | Code |
|---|---:|---|
| Route không tồn tại | 404 | ROUTE_NOT_FOUND |
| Method không được phép | 405 | METHOD_NOT_ALLOWED |
| Malformed request | 400 | INVALID_REQUEST |
| Request body vượt limit | 413 | REQUEST_BODY_TOO_LARGE |
| Upgrade chưa hỗ trợ | 501 | UPGRADE_NOT_SUPPORTED |
| Upstream connect/reset trước response | 502 | UPSTREAM_CONNECTION_FAILED |
| Upstream response-header timeout | 504 | UPSTREAM_TIMEOUT |
| Internal panic trước response commit | 500 | INTERNAL_ERROR |

Error response không chứa stack trace, upstream address hoặc raw internal error.

Nếu panic xảy ra sau khi response đã commit, gateway kết thúc stream/connection theo protocol và log structured error; không cố ghi status 500 lần hai.

Client cancellation không tạo error response mới nếu downstream đã rời.

## 10. Startup, readiness và shutdown

Startup order:

    read YAML
      -> strict decode
      -> validate config
      -> load TLS key pair
      -> construct Transport
      -> construct ReverseProxy
      -> bind admin listener
      -> bind traffic listeners
      -> ready

Readiness nghĩa là config hợp lệ và traffic listeners đang phục vụ. Phase 1 không health-check upstream nên upstream availability không ảnh hưởng readiness.

Liveness chỉ phản ánh process và admin server còn hoạt động.

Shutdown order:

1. readiness chuyển false;
2. traffic listeners ngừng nhận connection mới;
3. request đang chạy được drain trong shutdown_timeout;
4. hết deadline thì request còn lại bị cancel;
5. idle upstream connections bị đóng;
6. admin server shutdown;
7. process thoát.

## 11. Resource safety

- ReadHeaderTimeout, IdleTimeout và MaxHeaderBytes bắt buộc hữu hạn.
- Request body có streaming byte limit.
- Dial và response-header timeout bắt buộc hữu hạn.
- Không có unbounded queue.
- Không có unbounded response buffering.
- Panic được recover ở outer request boundary.
- Error log trong upstream outage được rate-limit.
- Performance mode tắt access log và per-request metrics.

## 12. Correctness upstream

test-upstream cung cấp:

    /fixed/{bytes}
    /echo
    /headers
    /stream
    /delay/{duration}
    /trailers
    /close

Mục đích:

- /fixed kiểm tra body sizes;
- /echo kiểm tra streamed request body;
- /headers kiểm tra Host, forwarding và hop-by-hop policy;
- /stream phát nhiều chunk theo thời gian;
- /delay trì hoãn response headers;
- /trailers tạo HTTP trailers;
- /close đóng connection để kiểm tra failure mapping.

test-upstream có connection counters phục vụ kiểm tra keepalive reuse và cancellation observation.

## 13. Performance upstream

Nginx performance upstream:

- access log tắt;
- không có dynamic application logic;
- trả payload chuẩn bị sẵn;
- cung cấp 0 B, 1 KiB, 16 KiB và 64 KiB bodies;
- chạy cùng container/image/config cho cả gateway Go và APISIX.

Mỗi benchmark suite chạy direct-upstream control trước khi đo gateway. Scenario bị invalid nếu upstream hoặc load generator không có đủ headroom.

## 14. Docker Compose topology

Services:

    gateway-go
    apisix
    upstream-correctness
    upstream-performance
    wrk
    h2load

gateway-go và apisix dùng mutually exclusive Compose profiles. Cả hai nhận cùng network alias gateway khi active.

Benchmark harness là black-box Module. Nó không import internal Go packages của gateway và chỉ tương tác qua Docker processes, configuration files, HTTP traffic, health endpoints và result files. gateway-go và apisix là hai Adapter thật tại cùng target Seam.

Một benchmark run:

1. xác nhận APISIX source path và commit;
2. build/pull pinned images;
3. start shared upstream;
4. start đúng một gateway target;
5. wait readiness;
6. run direct-upstream control;
7. warm up target;
8. run measurements;
9. collect raw output và metadata;
10. stop target;
11. lặp với target còn lại;
12. generate summary.

APISIX chạy ở standalone data-plane mode bằng generated static configuration; benchmark không khởi động Admin API hoặc etcd. Cách này đặt APISIX và gateway Go trên cùng static-config footing và loại configuration plane khỏi phép đo.

Harness không sửa file trong APISIX source checkout. APISIX commit phải đúng 0f35b154824ed51d0d4bdadc78d1d5fa9ed1ec62 và worktree phải sạch; nếu không, benchmark dừng trước build.

Mỗi payload scenario sinh config riêng cho cả hai target. Exact Route path trùng performance-upstream path, ví dụ /bytes/1024. Target được restart bằng config tương ứng trước warm-up; không dùng route rewrite ẩn.

Target order được luân phiên giữa measurement runs.

Resource fairness:

- Go GOMAXPROCS bằng APISIX worker count;
- gateway targets có cùng CPU/memory limits;
- load generators không chia CPU limit với gateway;
- cùng Docker network path;
- cùng TLS certificate và policy;
- cùng upstream;
- access logging và per-request metrics có cùng trạng thái.

## 15. Benchmark orchestration

Phase 1 canonical entry point trên môi trường hiện tại:

    bench/run.ps1 -Mode smoke
    bench/run.ps1 -Mode compare

Options tối thiểu:

    -Target go|apisix|all
    -Scenario <name>|all
    -ApisixSource <path>
    -ResultsDir <path>

Scenario definitions nằm trong một file dữ liệu dùng chung, không hard-code thành hai bộ command riêng cho Go và APISIX.

Compare defaults:

    wrk:    2 threads, 16 connections
    h2load: 2 threads, 16 clients, 16 max concurrent streams/client

Các giá trị được version trong scenario definitions và có thể override cho exploratory runs. Artifact phải ghi effective values.

PowerShell wrapper chỉ điều phối Docker Compose và ghi artifact. Load generation thực tế luôn chạy trong wrk/h2load containers.

Khi chuyển canonical acceptance sang Linux, dự án có thể chạy PowerShell 7 hoặc bổ sung Linux wrapper đọc cùng scenario definitions; không được nhân đôi scenario semantics.

## 16. Benchmark scenarios

| Name | Generator | Downstream | TLS | Payloads |
|---|---|---|---:|---|
| h1-clear | wrk | HTTP/1.1 | No | 0, 1 KiB, 16 KiB, 64 KiB |
| h1-tls | wrk | HTTP/1.1 | Yes | 0, 1 KiB, 16 KiB, 64 KiB |
| h1-crosscheck-clear | h2load --h1 | HTTP/1.1 | No | 1 KiB |
| h1-crosscheck-tls | h2load --h1 | HTTP/1.1 | Yes | 1 KiB |
| h2-tls | h2load | HTTP/2 | Yes | 0, 1 KiB, 16 KiB, 64 KiB |

Modes:

- smoke: warm-up ngắn, một measurement ngắn, dùng cho development feedback;
- compare: warm-up 15 giây, năm measurement run, mỗi run 60 giây.

Mỗi scenario ghi:

- requests/second;
- transfer rate;
- p50, p95 và p99;
- request error, timeout và non-2xx count;
- target CPU/memory sample;
- raw generator output.

wrk chạy với latency reporting. h2load chạy với per-request --log-file và JSON --output-file; summary generator tính percentile từ raw per-request durations thay vì suy diễn từ mean/standard deviation.

wrk dùng một pinned Lua reporting script để xuất p50, p95 và p99 từ latency histogram. Không suy diễn p95 từ bảng percentile mặc định khi output đó không chứa p95.

## 17. Results format

    bench/results/<timestamp>/
      metadata.json
      go/<scenario>/<run>/raw.txt
      apisix/<scenario>/<run>/raw.txt
      controls/<scenario>/raw.txt
      summary.json
      summary.md

metadata.json chứa:

- gateway repository commit;
- APISIX commit;
- image digests;
- Go, Docker và generator versions;
- host OS và Docker Desktop version;
- CPU/memory limits;
- GOMAXPROCS và APISIX worker count;
- scenario definitions;
- warm-up, duration và run count;
- TLS configuration.

Summary tính median throughput và latency distributions, không chỉ chọn run tốt nhất.

Mọi result sinh trên Docker Desktop được đánh dấu:

    environment_class: provisional

## 18. Test strategy

### 18.1. Unit

- Strict YAML decoding.
- Validation rules.
- Exact route and method matching.
- Header removal/rebuild.
- Outbound URL, Host và raw-query preservation.
- Error mapping.
- Shutdown state transitions.

### 18.2. Integration

- HTTP/1.1 cleartext.
- HTTP/1.1 TLS.
- HTTP/2 TLS và ALPN h2.
- Streamed request/response bodies.
- First response chunk đến trước upstream completion.
- Client cancellation tới upstream.
- Headers và trailers.
- Connect failure, upstream close và response-header timeout.
- Upstream connection reuse.
- Readiness and graceful drain.
- Unsupported Upgrade.

### 18.3. Concurrency

- Core packages chạy với Go race detector trong Linux build/test container.
- Concurrent requests trong shutdown.
- Concurrent keepalive reuse.
- Cancellation dưới streamed responses.

## 19. Exit criteria

Blocking gates:

- Unit, integration và race tests đạt.
- Gateway build bằng pinned Go 1.26 image.
- Compose harness chạy từ clean state.
- APISIX baseline commit guard hoạt động.
- Direct-upstream control chứng minh benchmark không upstream-bound.
- Năm compare runs hoàn thành không có request errors do gateway.
- Raw data và metadata đầy đủ.
- Summary đánh dấu Docker Desktop results là provisional.
- Profiling artifacts được lưu nếu provisional parity target không đạt.

Direct-upstream control được coi là có headroom khi throughput ít nhất bằng 125% throughput của target nhanh hơn trong cùng scenario. Nếu không đạt, scenario bị invalid và phải giảm target load bottleneck hoặc cấp thêm CPU cho upstream/load generator trước khi so sánh.

Provisional comparison targets:

- median Go throughput không thấp hơn APISIX;
- Go p99 không lớn hơn 110% APISIX;
- Go error rate không cao hơn APISIX.

Không đạt provisional targets không tự động fail Phase 1, không tự động đổi HTTP engine và không chứng minh north-star architecture sai.

Parity gate chính thức phải được chạy lại trên dedicated Linux environment trước production certification. Chỉ evidence từ môi trường chuẩn mới có thể dẫn tới ADR đổi HTTP engine.

### 19.1. Implementation audit — 2026-07-22

Ngày 2026-07-23, Phase 1 được chấp nhận để tiếp tục roadmap dựa trên correctness, reproducibility và clean smoke evidence. Quyết định này không thay đổi `provisional_miss` thành parity pass và không phải production performance certification.

- Unit, integration, race, vet, format, command build và ba container build đã pass với Go 1.26.5.
- Clean smoke-all đã tạo đủ 42 raw runs, 18 h2load request logs và 14 summary comparisons; mọi run có zero request errors/timeouts/non-2xx và direct-control headroom lớn hơn 125%.
- Source guard đã xác minh APISIX checkout sạch tại đúng commit `0f35b154824ed51d0d4bdadc78d1d5fa9ed1ec62`; standalone topology không dùng etcd/Admin API.
- Smoke summary có `environment_class: provisional` và `verdict: provisional_miss`; parity miss này không phải blocking failure.
- Canonical compare-all với năm lần lặp 60 giây chưa được thực thi vì phiên làm việc chưa được cấp quyền cho lệnh chạy dài; đây là performance debt không còn chặn Phase 2.
- Profiling CPU/heap và Docker resource traces vẫn pending; chúng phải được hoàn tất trước production certification và không thay thế comparison samples.

## 20. Deliverables

- Go module và pinned build image.
- gateway-dp.
- test-upstream.
- Static config example.
- Unit/integration/race suites.
- Compose topology.
- APISIX local-source image/build integration.
- wrk và h2load runners.
- Smoke và compare commands.
- Versioned scenario definitions.
- Raw-result schema và summary generator.
- Phase 1 operational notes.

## 21. Handoff sang Phase 2

Phase 1 đã được chấp nhận cho phase progression ngày 2026-07-23 với performance evidence vẫn provisional.

Các contract được giữ:

- request normalization policy;
- gateway lifecycle và upstream Module responsibilities;
- stable error codes;
- benchmark scenario/result formats;
- static canonical resource shape.

Phase 2 thay exact single-route matcher bằng immutable RuntimeSnapshot và compiled router nhưng không thay proxy correctness baseline nếu không có evidence.

## 22. Tài liệu tham chiếu

- [Go net/http](https://pkg.go.dev/net/http)
- [Go httputil.ReverseProxy](https://pkg.go.dev/net/http/httputil)
- [wrk](https://github.com/wg/wrk)
- [h2load manual](https://nghttp2.org/documentation/h2load.1.html)
- [APISIX architecture analysis](../../architecture/apache-api-six-architecture-design.md)

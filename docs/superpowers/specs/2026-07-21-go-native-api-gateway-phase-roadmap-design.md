# Roadmap triển khai API Gateway Go-native

- Ngày: 2026-07-21
- Trạng thái: Đã được duyệt
- Chiến lược: Risk-first vertical slices

## 1. Mục đích

Roadmap này phân rã [north-star architecture](2026-07-21-go-native-api-gateway-design.md) thành bảy phase tuần tự. Mỗi phase phải tạo ra một hệ thống chạy được, kiểm chứng được và giảm một nhóm rủi ro cụ thể.

North-star architecture định nghĩa trạng thái đích và các invariant xuyên suốt. Roadmap định nghĩa thứ tự delivery. Mỗi phase sau đó có design spec và implementation plan riêng.

Không tạo một implementation plan duy nhất cho toàn bộ gateway.

## 2. Vì sao chọn risk-first vertical slices

Ba chiến lược đã được xem xét:

1. Chia theo technical layer như model, control plane, data plane và plugin. Cách này tạo nhiều component riêng lẻ nhưng rất muộn mới có hệ thống end-to-end.
2. Chia theo feature hoàn chỉnh. Cách này tạo giá trị sớm nhưng có thể phát hiện rủi ro HTTP runtime và configuration distribution quá muộn.
3. Risk-first vertical slices. Mỗi phase chạy xuyên qua các layer tối thiểu cần thiết, có benchmark và exit gate riêng.

Roadmap chọn phương án 3. Hai rủi ro lớn nhất được xử lý sớm:

- net/http có đạt correctness và performance target hay không;
- immutable snapshot, route matcher và configuration distribution có scale đến design envelope hay không.

## 3. Dependency và thứ tự

Các phase thực hiện tuần tự:

    Phase 1: Proxy baseline và benchmark harness
      -> Phase 2: Runtime snapshot và router kernel
      -> Phase 3: Upstream resilience
      -> Phase 4: Control plane end-to-end
      -> Phase 5: Configuration distribution ở quy mô lớn
      -> Phase 6: Policy và security plugins
      -> Phase 7: Production hardening và parity gate

Phase tiếp theo chỉ bắt đầu khi exit criteria của phase hiện tại đã được kiểm chứng và chấp nhận.

Không được kéo một feature từ phase tương lai vào phase hiện tại chỉ để làm API có vẻ hoàn chỉnh. Chỉ tạo extension point khi phase hiện tại có ít nhất một implementation thật sử dụng nó.

## 4. Contract chung cho mọi phase

Mỗi phase có vòng đời:

    brainstorm
      -> design spec
      -> user review
      -> implementation plan
      -> implementation
      -> verification
      -> phase acceptance

Design spec của mỗi phase phải có:

- mục tiêu và rủi ro cần loại bỏ;
- phạm vi bao gồm và không bao gồm;
- component boundaries và contracts;
- data flow và failure semantics;
- migration hoặc compatibility impact;
- test strategy;
- benchmark scenarios;
- measurable exit criteria.

Implementation plan chỉ được tạo sau khi design spec của phase đó được duyệt.

Mỗi phase phải:

- giữ repository buildable và testable;
- có executable hoặc integration path chạy được;
- thêm regression tests cho behavior mới;
- chạy benchmark liên quan và lưu kết quả;
- không vi phạm invariant của north-star architecture;
- cập nhật tài liệu khi contract thay đổi.

## 5. Phase 1 — Proxy baseline và benchmark harness

### 5.1. Mục tiêu

Chứng minh net/http là correctness foundation khả thi và thiết lập benchmark đối chiếu APISIX có thể tái lập.

### 5.2. Phạm vi

- Go module, repository layout, build và CI tối thiểu.
- Một gateway-dp executable.
- Static configuration cho một Route và một Upstream.
- HTTP/1.1 và HTTP/2 reverse proxy.
- Request normalization và hop-by-hop header handling tối thiểu.
- Connection reuse qua configured http.Transport.
- Graceful shutdown.
- Health/readiness endpoint tối thiểu.
- Prometheus process và request metrics tối thiểu.
- Programmable upstream test server.
- Benchmark harness chạy gateway Go và APISIX trong cùng điều kiện.

### 5.3. Không bao gồm

- Dynamic snapshot swap.
- Route tree nhiều route.
- Plugin framework.
- Health check, retry hoặc load-balancing nhiều endpoint.
- Control plane, etcd hoặc gRPC distribution.

### 5.4. Exit criteria

- HTTP/1.1 và HTTP/2 proxy correctness tests đạt.
- Request/response streaming không buffer toàn body.
- Graceful shutdown không nhận request mới và drain request đang chạy.
- Benchmark một route, không plugin, có và không TLS chạy tái lập được.
- Median throughput không thấp hơn APISIX và p99 không lớn hơn 110% APISIX trong baseline workload.
- Nếu không đạt, profiling xác định bottleneck và tạo ADR trước khi tiếp tục.

## 6. Phase 2 — Runtime snapshot và router kernel

### 6.1. Mục tiêu

Chứng minh request hot path chỉ đọc immutable state và route matching scale đến 100.000 route.

### 6.2. Phạm vi

- Canonical Route, Service và Upstream model cần cho DP.
- RuntimeSnapshot bất biến.
- Shadow build và atomic pointer swap.
- Request giữ một snapshot trong toàn bộ lifecycle.
- Host index, path radix tree, method bitmask và compiled predicates.
- Deterministic route precedence.
- Lightweight request context.
- Plugin compile/execute contract tối thiểu.
- request-id và header-rewrite làm hai plugin thật đầu tiên.
- Unit, property, fuzz và concurrent-swap tests.

### 6.3. Không bao gồm

- Full policy plugin catalog.
- etcd hoặc remote distribution.
- Active/passive health check.
- Distributed rate limiting.

### 6.4. Exit criteria

- 100.000 route compile và match đúng theo precedence đã định.
- Request không thấy partial revision trong concurrent update test.
- Không có global mutex, JSON parsing, reference resolution hoặc plugin sorting trên hot path.
- Go race detector đạt cho snapshot/router/plugin core.
- Route-first/middle/last benchmarks đạt comparative target với APISIX.
- Memory sau nhiều snapshot swap trở về steady state.

## 7. Phase 3 — Upstream resilience

### 7.1. Mục tiêu

Tạo standalone data plane đủ tin cậy để proxy traffic thật trước khi thêm control plane.

### 7.2. Phạm vi

- UpstreamRuntimeRegistry tách khỏi immutable config.
- Shared transport profiles và connection pools.
- Weighted round-robin và consistent hash.
- Active HTTP/TCP health check.
- Passive failure tracking.
- Connect, response và total timeout.
- Replay-safe retry với budget và deadline.
- Dynamic TLS/SNI certificate selection.
- Upstream TLS policy.
- WebSocket proxy correctness.
- Bounded structured access log.

### 7.3. Không bao gồm

- Remote configuration distribution.
- Redis rate limit.
- OIDC/RBAC Admin API.
- Service-registry discovery.

### 7.4. Exit criteria

- Config update không phá connection pool không liên quan.
- Retry không lặp request không replayable.
- Health transition và all-unhealthy behavior có deterministic tests.
- TLS certificate thay đổi không restart listener.
- Upstream failure/recovery integration tests đạt.
- Load balancing, TLS, health và retry benchmark scenarios không vi phạm comparative gate liên quan.

## 8. Phase 4 — Control plane end-to-end

### 8.1. Mục tiêu

Chứng minh flow quản trị hoàn chỉnh từ Admin API đến request dùng revision mới.

### 8.2. Phạm vi

- gateway-cp executable.
- REST resources: Route, Service, Upstream, PluginConfig, Certificate và SecretRef tối thiểu.
- Schema/reference validation.
- Optimistic concurrency và multi-resource transaction.
- Etcd persistence.
- gRPC distribution handshake dùng mTLS.
- Full snapshot distribution.
- DP shadow build, atomic activate và ACK/NACK.
- Rollout status cơ bản.
- Signed và encrypted last-known-good snapshot.
- Admin authentication/RBAC tối thiểu và audit mutation.

### 8.3. Không bao gồm

- Delta distribution và coalescing.
- 1.000 DP scale gate.
- Consumer và full authentication plugin set.
- Multi-region control plane.

### 8.4. Exit criteria

- Admin transaction đến active DP revision chạy end-to-end.
- Invalid snapshot bị NACK và traffic giữ revision cũ.
- DP cold-start được bằng local snapshot khi CP unavailable.
- CP hoặc etcd outage không làm gián đoạn active traffic.
- Connected healthy DP activation p99 không quá một giây ở integration scale của phase.
- CP và DP có protocol/schema compatibility tests.

## 9. Phase 5 — Configuration distribution ở quy mô lớn

### 9.1. Mục tiêu

Đạt design envelope của configuration plane và hỗ trợ rolling upgrade/HA.

### 9.2. Phạm vi

- Delta messages theo base và target revision.
- Logical state checksum.
- Per-DP bounded queues.
- Revision coalescing.
- Gap detection và full resync.
- Slow-consumer handling.
- Detailed rollout states.
- CP failover và reconnect storm handling.
- Current/N-1 protocol và schema support.
- Capability negotiation theo plugin/version.
- 1.000 simulated connected DP load harness.

### 9.3. Không bao gồm

- Multi-region active-active.
- Kubernetes Gateway API.
- Service-registry discovery.

### 9.4. Exit criteria

- 1.000 DP và 100 update/giây đạt configuration convergence lag p99 không quá một giây. Revision trung gian bị coalesce không bắt buộc phải được kích hoạt.
- Không có unbounded queue hoặc memory growth.
- Slow DP không làm chậm DP khác.
- Lost delta luôn dẫn đến deterministic full recovery.
- CP failover và reconnect storm không làm mất active traffic.
- Rolling upgrade current/N-1 đạt compatibility tests.

## 10. Phase 6 — Policy và security plugins

### 10.1. Mục tiêu

Hoàn thiện policy surface cần cho vertical slice production.

### 10.2. Phạm vi

- Consumer resource.
- Hoàn thiện SecretRef, envelope-encryption provider integrations và rotation lifecycle.
- Key authentication.
- JWT authentication.
- Local token-bucket rate limit.
- Redis-backed cluster-wide rate limit.
- CORS.
- Request/response header rewrite hoàn chỉnh.
- Prometheus metrics với cardinality budget.
- Structured access log pipeline.
- Admin JWT/OIDC integration và RBAC hoàn chỉnh.
- Secret redaction và key/certificate rotation.

### 10.3. Không bao gồm

- WASM, Lua hoặc external plugin runner.
- WAF, response cache hoặc traffic mirroring.
- User-supplied scripts.

### 10.4. Exit criteria

- Plugin precedence và Consumer post-auth policy đúng contract.
- External I/O plugin có timeout, concurrency cap và fail policy.
- Secret không xuất hiện trong API response, log, metric hoặc NACK.
- Redis unavailable behavior đúng fail-open/fail-closed configuration.
- Auth, local rate-limit và metrics comparative workload đạt target.
- Race/fuzz tests đạt cho plugin configuration và execution.

## 11. Phase 7 — Production hardening và parity gate

### 11.1. Mục tiêu

Kiểm chứng toàn hệ thống đạt north-star Definition of Done và sẵn sàng cho production evaluation.

### 11.2. Phạm vi

- Full failure matrix.
- Race, fuzz, soak và chaos suites.
- CP/etcd leader loss drills.
- Packet loss, slow DP, corrupt snapshot và telemetry saturation.
- TLS/mTLS rotation drills.
- Memory/GC profiling và optimization.
- Tất cả comparative benchmark workloads.
- Operational runbooks và upgrade/rollback documentation.
- Security review đối với HTTP normalization, forwarding headers, secrets và Admin API.

### 11.3. Exit criteria

- Tất cả correctness, integration, race, fuzz, chaos và soak gates đạt.
- Tất cả comparative workloads đạt throughput và p99 target.
- Configuration convergence lag đạt target tại full design envelope.
- CP/etcd outage không làm gián đoạn traffic đang dùng snapshot hợp lệ.
- Memory đạt steady state và không tăng theo revision.
- Không còn production-blocking security finding.
- Runbook chứng minh deploy, upgrade, rollback, backup và recovery.

## 12. Benchmark policy xuyên phase

Benchmark không bị dồn sang Phase 7.

- Phase 1 khóa baseline proxy.
- Phase 2 khóa router/snapshot cost.
- Phase 3 khóa upstream resilience cost.
- Phase 4 khóa end-to-end configuration activation.
- Phase 5 khóa distribution scale.
- Phase 6 khóa real plugin stack.
- Phase 7 chạy full parity matrix và soak.

Mỗi benchmark result phải lưu:

- source commit;
- Go và APISIX version/commit;
- hardware/container limits;
- kernel/network settings;
- command và workload config;
- raw output;
- summarized comparison.

Performance regression ở phase hiện tại phải được giải thích hoặc sửa trước khi bắt đầu phase tiếp theo.

## 13. Change control

Nếu một phase phát hiện north-star assumption sai:

1. ghi evidence;
2. brainstorm lựa chọn;
3. tạo hoặc cập nhật ADR;
4. cập nhật north-star và roadmap;
5. re-evaluate các phase sau.

Không âm thầm thay contract xuyên phase.

Thay đổi chỉ ảnh hưởng implementation nội bộ của phase hiện tại không yêu cầu sửa roadmap nếu invariant, public contract và exit criteria không đổi.

## 14. Bước tiếp theo

Bước tiếp theo duy nhất sau khi roadmap được review là brainstorm Phase 1 — Proxy baseline và benchmark harness.

Không lập implementation plan cho Phase 1 hoặc phase khác trước khi Phase 1 design spec được duyệt.

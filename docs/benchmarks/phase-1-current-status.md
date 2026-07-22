# Phase 1 benchmark — trạng thái hiện tại

- Cập nhật: 2026-07-22
- Branch: `codex/phase1-proxy-baseline`
- Clean smoke evidence: `bench/results/task12-smoke-all-final`
- Môi trường: Docker Desktop, Linux containers, x86_64, 20 logical CPUs
- Phân loại evidence: `provisional`

## Kết luận điều hành

Phase 1 đã có một benchmark harness hợp lệ và một clean smoke-all hoàn chỉnh. Correctness, reproducibility, source guard, raw evidence và direct-control headroom đều đạt. Không có request error, timeout hoặc non-2xx response trong 42 raw runs.

Kết quả hiệu năng hiện tại là `provisional_miss`: Go đạt ngưỡng parity ở 2/14 scenario/payload combinations. Hai trường hợp đạt là HTTP/1.1 với payload 64 KiB, cả cleartext và TLS. Khoảng cách lớn nhất nằm ở HTTP/2/TLS và một số payload nhỏ/trung bình.

Đây chưa phải kết luận hiệu năng chính thức và Phase 1 chưa được acceptance hoàn toàn. Evidence hiện tại chỉ là smoke run trên Docker Desktop. Canonical compare-all gồm năm lần lặp 60 giây và profiling sau parity miss vẫn chưa được thực thi.

## Tổng quan evidence

| Chỉ số | Kết quả |
|---|---:|
| Benchmark mode | `smoke` |
| Warm-up / measurement / repetitions | 3 giây / 10 giây / 1 |
| Scenario/payload comparisons | 14 |
| Raw runs | 42 |
| h2load request logs | 18 |
| Request errors | 0 |
| Timeouts | 0 |
| Non-2xx responses | 0 |
| Direct headroom | 4.68×–10.99× |
| `provisional_pass` | 2/14 |
| `provisional_miss` | 12/14 |
| Overall verdict | `provisional_miss` |

Parity của mỗi comparison yêu cầu đồng thời:

- Go median throughput không thấp hơn APISIX;
- Go median p99 không lớn hơn 110% APISIX;
- Go error rate không cao hơn APISIX.

Direct control phải đạt ít nhất 125% throughput của gateway target nhanh hơn. Tất cả smoke comparisons đều vượt yêu cầu này với biên đáng kể.

## Ma trận kết quả hiện tại

RPS và p99 dưới đây là số liệu của một smoke repetition, không phải median của canonical five-run comparison.

| Scenario | Payload | Go RPS | APISIX RPS | Go/APISIX | Go p99 | APISIX p99 | Headroom | Verdict |
|---|---:|---:|---:|---:|---:|---:|---:|---|
| h1-clear | 0 B | 30,681.6 | 37,263.9 | 82.3% | 1,442 µs | 1,027 µs | 9.07× | miss |
| h1-clear | 1 KiB | 28,250.3 | 36,249.1 | 77.9% | 1,573 µs | 1,221 µs | 6.28× | miss |
| h1-clear | 16 KiB | 16,052.0 | 30,684.2 | 52.3% | 3,855 µs | 1,302 µs | 7.27× | miss |
| h1-clear | 64 KiB | 21,974.1 | 20,566.6 | 106.8% | 1,865 µs | 2,169 µs | 6.01× | **pass** |
| h1-crosscheck-clear | 1 KiB | 27,500.9 | 37,865.4 | 72.6% | 1,580 µs | 1,090 µs | 4.68× | miss |
| h1-crosscheck-tls | 1 KiB | 26,937.8 | 29,512.2 | 91.3% | 1,638 µs | 1,336 µs | 6.45× | miss |
| h1-tls | 0 B | 28,976.7 | 31,922.8 | 90.8% | 1,572 µs | 1,206 µs | 10.99× | miss |
| h1-tls | 1 KiB | 27,329.2 | 30,843.9 | 88.6% | 1,636 µs | 1,451 µs | 7.50× | miss |
| h1-tls | 16 KiB | 20,012.7 | 23,668.1 | 84.6% | 2,739 µs | 1,534 µs | 9.67× | miss |
| h1-tls | 64 KiB | 17,575.1 | 15,769.2 | 111.5% | 2,231 µs | 2,301 µs | 7.38× | **pass** |
| h2-tls | 0 B | 22,267.9 | 42,932.1 | 51.9% | 23,242 µs | 11,442 µs | 8.68× | miss |
| h2-tls | 1 KiB | 18,867.2 | 38,420.4 | 49.1% | 26,778 µs | 17,243 µs | 6.67× | miss |
| h2-tls | 16 KiB | 13,918.1 | 22,980.2 | 60.6% | 39,592 µs | 25,333 µs | 9.51× | miss |
| h2-tls | 64 KiB | 11,471.4 | 16,806.9 | 68.3% | 40,899 µs | 25,764 µs | 9.14× | miss |

## Nhận định kỹ thuật

### HTTP/1.1

- Go đạt parity ở cả hai trường hợp payload 64 KiB. Throughput cao hơn APISIX khoảng 6.8% với cleartext và 11.5% với TLS; p99 cũng thấp hơn.
- Với payload 0–1 KiB, Go đạt khoảng 78%–91% throughput của APISIX và p99 cao hơn 12.7%–40.4%.
- `h1-clear/16 KiB` là outlier đáng chú ý: throughput chỉ bằng 52.3% APISIX và p99 gần gấp ba. Cần canonical repetitions và profiling trước khi kết luận nguyên nhân.
- Hai h2load HTTP/1.1 cross-check thống nhất với xu hướng wrk: Go vẫn thấp hơn APISIX, nên khoảng cách không chỉ do một load generator.

### HTTP/2/TLS

- Đây là khoảng cách lớn nhất: Go đạt 49.1%–68.3% throughput APISIX.
- Go p99 cao hơn khoảng 1.55×–2.03× APISIX.
- Cả hai target vẫn trả về zero errors. Vấn đề hiện tại là efficiency/latency dưới multiplexed load, không phải protocol correctness.
- Cần CPU profile, heap profile và Docker CPU/memory traces để xác định chi phí nằm ở Go HTTP/2 server, TLS, reverse proxy, buffer/copy path hay scheduling.

### Độ tin cậy của harness

Direct headroom từ 4.68× đến 10.99× cho thấy các smoke measurements không bị upstream Nginx giới hạn. Trong quá trình ổn định harness, các sai lệch sau đã được phát hiện và sửa:

- wrk được patch dùng monotonic clock để tránh Docker Desktop wall-clock corrections tạo timeout giả;
- h2load ghi request-level log vào filesystem Linux trong container rồi mới copy ra bind mount, tránh Windows I/O bóp nghẹt generator;
- h2load warm-up cùng connection pool được dùng cho measurement;
- direct control luôn dùng clear HTTP/1.1, đúng với Phase 1 upstream contract;
- APISIX downstream `keepalive_requests` được nâng lên 10,000,000 để connections không bị đóng hết trong warm-up;
- TLS readiness probe dùng hostname `localhost` để cung cấp đúng SNI;
- performance upstream dùng static payload trong image và Nginx `sendfile`.

## Verification đã đạt

- Strict config, routing, proxy semantics, streaming, trailers, cancellation, body limits và stable errors.
- HTTP/1.1 cleartext, HTTP/1.1 TLS, HTTP/2 TLS và ALPN integration tests.
- Readiness, telemetry và graceful shutdown/drain.
- `gofmt`, `go vet`, toàn bộ unit/integration tests, race tests và `go build ./cmd/...` trong `golang:1.26.5-bookworm`.
- Container builds cho `gateway-dp`, `test-upstream` và `bench-report`.
- APISIX clean-source guard tại commit `0f35b154824ed51d0d4bdadc78d1d5fa9ed1ec62`.
- Standalone APISIX topology không dùng etcd hoặc Admin API.
- Benchmark cleanup không để lại target/upstream/load-generator containers hoặc network.

## Gate còn thiếu

1. Chạy canonical compare-all: 15 giây warm-up, năm repetitions × 60 giây cho mỗi scenario/payload.
2. Nếu kết quả vẫn là `provisional_miss`, chạy lại scenario miss nặng nhất ngoài comparison sample với profiling bật.
3. Thu CPU profile 30 giây, heap profile và Docker CPU/memory traces cho Go; thu resource traces tương ứng cho APISIX.
4. Chỉ khi các blocking gates hoàn tất mới đổi design status thành `Implemented; provisional benchmark captured`.
5. Parity chính thức vẫn phải được xác nhận lại trên dedicated Linux trong Phase 7.

Lệnh compare tiếp theo:

```powershell
pwsh bench/run.ps1 -Mode compare -Target all -Scenario all -ApisixSource D:\User2\open_source\apisix
```

Full compare dự kiến kéo dài khoảng 3–4 giờ và cần tối thiểu 15 GiB dung lượng trống để lưu request-level evidence.

## Nguồn evidence

- Human-readable smoke report: `bench/results/task12-smoke-all-final/summary/summary.md`
- Machine-readable summary: `bench/results/task12-smoke-all-final/summary/summary.json`
- Raw runs: `bench/results/task12-smoke-all-final/{controls,go,apisix}/`
- Benchmark contract và cách vận hành: [`bench/README.md`](../../bench/README.md)
- Operational/profiling procedure: [`docs/operations/phase-1-runbook.md`](../operations/phase-1-runbook.md)
- Phase 1 design audit: [`docs/superpowers/specs/2026-07-22-phase-1-proxy-baseline-benchmark-design.md`](../superpowers/specs/2026-07-22-phase-1-proxy-baseline-benchmark-design.md)

Raw result directory bị Git ignore có chủ đích và không được commit. File này là snapshot tóm tắt; `summary.json` vẫn là nguồn số liệu chuẩn của run hiện tại.

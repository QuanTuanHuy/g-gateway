# Phase 1 benchmark harness

This harness compares G-Gateway with APISIX through the same Docker network, deterministic Nginx upstream, certificates, resource limits, and load-generator images. Results produced on Docker Desktop are always `provisional`; official parity must be rerun on dedicated Linux during Phase 7.

## Prerequisites

- Docker Desktop using Linux containers and Compose v2. Allocate at least 8 logical CPUs and 12 GiB RAM, close unrelated workloads, and keep the power profile fixed for a comparison run.
- PowerShell 7 (`pwsh`) is canonical. Windows PowerShell 5.1 is compatible with the current scripts but is not the certification shell.
- A clean APISIX checkout at `D:\User2\open_source\apisix`, exactly commit `0f35b154824ed51d0d4bdadc78d1d5fa9ed1ec62`.
- Enough free disk for APISIX, wrk, h2load, three Go command images, and raw results. Reserve at least 15 GiB for a full compare run because h2load request-level evidence is intentionally retained.

The runner refuses a wrong or dirty APISIX checkout before invoking Docker. Its Git trust override is scoped to that resolved checkout and does not mutate global Git configuration.

Verify the source guard without building images:

```powershell
pwsh bench/run.ps1 -PreflightOnly -ApisixSource D:\User2\open_source\apisix
```

## Reproducibility controls

- Go toolchain: `golang:1.26.5-bookworm`.
- APISIX source: pinned commit above, built by `bench/apisix/Dockerfile` in standalone YAML mode without etcd or Admin API.
- wrk: commit `a211dd5a7050b1f9e8a9870b95513060e72ac4a0` (4.2.0), plus the checked-in monotonic-clock patch required to avoid Docker Desktop wall-clock corrections corrupting latency samples.
- h2load: nghttp2 tag `v1.69.0`.
- Performance upstream: image derived from `nginx:1.31.3-alpine`, with generated payloads copied into the image so Windows bind-mount I/O is not measured.
- Go and APISIX: two workers/`GOMAXPROCS=2`, 2 CPU and 1 GiB container limits, access logging and per-request metrics disabled.
- APISIX downstream keepalive requests are raised to 10,000,000 so the fixed connection set is not exhausted during h2load warm-up; its upstream keepalive pool remains configured separately.
- h2load warms the same connections used for measurement. Per-request logs are written to the container filesystem during the sample and copied to the result mount afterward, preventing Windows bind-mount I/O from throttling the load generator.

`bench/certs/generate.ps1` creates an ignored self-signed certificate with SANs `gateway` and `localhost`. Payloads, certificates, generated target configuration, and bulk results are ignored by Git.

## Run flows

Quick protocol/correctness and artifact smoke:

```powershell
pwsh bench/run.ps1 -Mode smoke -Target all -Scenario all -ApisixSource D:\User2\open_source\apisix
```

Canonical Docker Desktop comparison (five 60-second repetitions after 15-second warm-ups):

```powershell
pwsh bench/run.ps1 -Mode compare -Target all -Scenario all -ApisixSource D:\User2\open_source\apisix
```

Limit a diagnostic run with `-Target go|apisix` and `-Scenario h1-clear|h1-tls|h1-crosscheck-clear|h1-crosscheck-tls|h2-tls`. A comparison summary is generated only for `-Target all`, because parity requires both targets.

The order for each scenario/payload is direct Nginx control, then alternating Go/APISIX first target by repetition. Only one service owns the `gateway` network alias at a time. The direct control always uses clear HTTP/1.1 because Phase 1's real upstream contract is HTTP/1.1; target measurements use the scenario's declared downstream protocol/TLS. Direct raw artifacts record that effective HTTP/1.1/non-TLS protocol. Any generator error, timeout, non-2xx response, or direct-control throughput below 125% of the faster gateway invalidates the run.

## Result layout

By default the runner creates `bench/results/<UTC timestamp>/`:

```text
metadata.json
controls/<scenario>/<payload>/run-1/
go/<scenario>/<payload>/run-<n>/
apisix/<scenario>/<payload>/run-<n>/
  raw-run.json
  generator.json
  stdout.log
  stderr.log
  requests.tsv          h2load only
summary/
  summary.json
  summary.csv
  summary.md
```

Every raw run records source/image pins, effective settings, container limits, environment metadata, direct-control measurement, percentiles, errors, and artifact paths. `bench-report` re-parses wrk output and calculates exact nearest-rank h2load percentiles from `requests.tsv`; it does not import gateway runtime code.

Verdicts:

- `invalid`: corrupt/missing evidence, any request failure, or insufficient direct headroom. The report command exits non-zero.
- `provisional_pass`: Go median throughput is at least APISIX, Go median p99 is at most 110% of APISIX, and Go error rate is no higher.
- `provisional_miss`: one or more parity targets missed. The report command remains successful; preserve evidence and profile instead of changing HTTP engines from Docker Desktop data.

## Cleanup and common failures

Normal and error exits remove target, upstream, load-generator containers, and `g-gateway-benchmark`. If interrupted outside the script's `finally`, set the required interpolation variables and clean every profile:

```powershell
$env:APISIX_SOURCE = 'D:\User2\open_source\apisix'
$env:GATEWAY_SOURCE = (Resolve-Path '.').ProviderPath
$env:BENCH_RESULTS_DIR = (New-Item -ItemType Directory -Force 'bench/results/cleanup').FullName
docker compose -f bench/compose.yaml --profile go --profile apisix --profile load down --remove-orphans
```

- Commit mismatch/dirty checkout: restore the pinned clean APISIX source; never bypass the guard.
- Port 18080, 18443, or 19090 busy: stop the conflicting process/container.
- Direct headroom invalid: check Docker CPU pressure and upstream/load-generator saturation. Do not lower the 1.25 factor.
- Generator errors: retain the failed directory and inspect sibling stdout/stderr plus container logs.
- Certificate/TLS failure: regenerate `bench/certs/generated` and verify Docker Desktop file sharing.
- Docker Desktop comparison variance: rerun the full comparison from a clean, idle state; never merge selected repetitions.

For a `provisional_miss`, follow the profiling procedure in [the Phase 1 runbook](../docs/operations/phase-1-runbook.md). Profiles and Docker resource traces are diagnostic evidence outside the comparison sample and never replace measured runs.

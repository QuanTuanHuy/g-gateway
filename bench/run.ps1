[CmdletBinding()]
param(
    [ValidateSet('smoke', 'compare')]
    [string] $Mode = 'smoke',

    [ValidateSet('go', 'apisix', 'all')]
    [string] $Target = 'all',

    [string] $Scenario = 'all',

    [string] $ApisixSource = 'D:\User2\open_source\apisix',

    [string] $ResultsDir = '',

    [switch] $PreflightOnly
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$ExpectedApisixCommit = '0f35b154824ed51d0d4bdadc78d1d5fa9ed1ec62'

function Invoke-Git {
    param(
        [Parameter(Mandatory = $true)]
        [string] $Repository,

        [Parameter(Mandatory = $true)]
        [string[]] $Arguments
    )

    # The benchmark may run from a containerized/sandboxed shell whose account
    # differs from the Windows checkout owner. Trust only this explicitly
    # resolved source path and do not mutate global Git configuration.
    $output = @(& git -c "safe.directory=$Repository" -c 'core.excludesFile=' -C $Repository @Arguments 2>&1)
    if ($LASTEXITCODE -ne 0) {
        throw "git $($Arguments -join ' ') failed for '$Repository': $($output -join [Environment]::NewLine)"
    }
    return $output
}

function Assert-ApisixSource {
    param(
        [Parameter(Mandatory = $true)]
        [string] $Source
    )

    $resolved = Resolve-Path -LiteralPath $Source -ErrorAction Stop
    $sourcePath = $resolved.ProviderPath
    $actualCommit = (Invoke-Git -Repository $sourcePath -Arguments @('rev-parse', 'HEAD') | Select-Object -First 1).Trim()
    if ($actualCommit -ne $ExpectedApisixCommit) {
        throw "APISIX commit mismatch. Expected '$ExpectedApisixCommit' but found '$actualCommit' at '$sourcePath'. Checkout the pinned commit before running the benchmark."
    }

    $status = @(Invoke-Git -Repository $sourcePath -Arguments @('status', '--porcelain'))
    if ($status.Count -gt 0) {
        $preview = ($status | Select-Object -First 20) -join [Environment]::NewLine
        throw "APISIX checkout is dirty at '$sourcePath'. Commit, stash, or remove these changes before running the benchmark:$([Environment]::NewLine)$preview"
    }

    [PSCustomObject]@{
        Path   = $sourcePath
        Commit = $actualCommit
    }
}

function Invoke-Docker {
    param(
        [Parameter(Mandatory = $true)]
        [string[]] $Arguments
    )

    & docker @Arguments | Out-Host
    if ($LASTEXITCODE -ne 0) {
        throw "docker $($Arguments -join ' ') exited with code $LASTEXITCODE"
    }
}

function Write-Utf8File {
    param(
        [Parameter(Mandatory = $true)]
        [string] $Path,

        [Parameter(Mandatory = $true)]
        [string] $Content
    )

    $utf8WithoutBom = New-Object System.Text.UTF8Encoding($false)
    [IO.File]::WriteAllText($Path, $Content, $utf8WithoutBom)
}

function New-BenchmarkPayloads {
    param(
        [Parameter(Mandatory = $true)]
        [string] $PayloadDirectory
    )

    $directory = New-Item -ItemType Directory -Force -Path $PayloadDirectory
    Invoke-Docker -Arguments @(
        'run', '--rm',
        '-v', "$($directory.FullName):/payloads",
        'alpine:3.21.3',
        'sh', '-c',
        'for n in 0 1024 16384 65536; do head -c "$n" /dev/zero > "/payloads/$n.bin"; done'
    )

    foreach ($size in @(0, 1024, 16384, 65536)) {
        $file = Get-Item -LiteralPath (Join-Path $directory.FullName "$size.bin")
        if ($file.Length -ne $size) {
            throw "generated payload '$($file.FullName)' contains $($file.Length) bytes; expected $size"
        }
    }
}

function New-GoGatewayConfig {
    param(
        [Parameter(Mandatory = $true)]
        [string] $Path,

        [Parameter(Mandatory = $true)]
        [int] $PayloadBytes
    )

    $routePath = "/bytes/$PayloadBytes"
    $document = @"
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
  request_metrics_enabled: false
  profiling_enabled: false

routes:
  - id: benchmark
    match:
      path: $routePath
      methods: [GET]
    upstream_ref: benchmark

upstreams:
  - id: benchmark
    endpoints:
      - http://upstream-performance:8080
    transport:
      dial_timeout: 3s
      response_header_timeout: 10s
      idle_connection_timeout: 90s
      max_idle_connections: 1024
      max_idle_connections_per_host: 1024
"@
    Write-Utf8File -Path $Path -Content $document
}

function ConvertTo-YamlBlock {
    param(
        [Parameter(Mandatory = $true)]
        [string] $Content
    )

    return (($Content.Trim() -split "`r?`n") | ForEach-Object { "      $_" }) -join "`n"
}

function New-ApisixRouteConfig {
    param(
        [Parameter(Mandatory = $true)]
        [string] $Path,

        [Parameter(Mandatory = $true)]
        [int] $PayloadBytes,

        [Parameter(Mandatory = $true)]
        [string] $CertificateFile,

        [Parameter(Mandatory = $true)]
        [string] $PrivateKeyFile
    )

    $certificate = ConvertTo-YamlBlock -Content ([IO.File]::ReadAllText($CertificateFile))
    $privateKey = ConvertTo-YamlBlock -Content ([IO.File]::ReadAllText($PrivateKeyFile))
    $routePath = "/bytes/$PayloadBytes"
    $document = @"
routes:
  - id: benchmark
    uri: $routePath
    methods: [GET]
    upstream:
      type: roundrobin
      nodes:
        "upstream-performance:8080": 1

ssls:
  - id: benchmark-tls
    cert: |
$certificate
    key: |
$privateKey
    snis:
      - gateway
      - localhost
#END
"@
    Write-Utf8File -Path $Path -Content $document
}

function Get-ScenarioDefinition {
    param(
        [Parameter(Mandatory = $true)]
        [object] $Catalog,

        [Parameter(Mandatory = $true)]
        [string] $Name
    )

    $property = $Catalog.scenarios.PSObject.Properties | Where-Object { $_.Name -eq $Name } | Select-Object -First 1
    if ($null -eq $property) {
        $available = ($Catalog.scenarios.PSObject.Properties.Name | Sort-Object) -join ', '
        throw "unknown scenario '$Name'; expected one of: $available, all"
    }
    return $property.Value
}

function Stop-GatewayTarget {
    param(
        [Parameter(Mandatory = $true)]
        [string] $ComposeFile,

        [Parameter(Mandatory = $true)]
        [ValidateSet('go', 'apisix')]
        [string] $TargetName
    )

    $service = if ($TargetName -eq 'go') { 'gateway-go' } else { 'apisix' }
    Invoke-Docker -Arguments @('compose', '-f', $ComposeFile, '--profile', $TargetName, 'stop', '--timeout', '35', $service)
    Invoke-Docker -Arguments @('compose', '-f', $ComposeFile, '--profile', $TargetName, 'rm', '--force', $service)
}

function Start-GatewayTarget {
    param(
        [Parameter(Mandatory = $true)]
        [string] $ComposeFile,

        [Parameter(Mandatory = $true)]
        [ValidateSet('go', 'apisix')]
        [string] $TargetName
    )

    $service = if ($TargetName -eq 'go') { 'gateway-go' } else { 'apisix' }
    Invoke-Docker -Arguments @('compose', '-f', $ComposeFile, '--profile', $TargetName, 'up', '--detach', '--build', '--force-recreate', $service)
}

function Wait-ForHttpResponse {
    param(
        [Parameter(Mandatory = $true)]
        [string] $Url,

        [Parameter(Mandatory = $true)]
        [string] $OutputFile,

        [Parameter(Mandatory = $true)]
        [long] $ExpectedBytes,

        [switch] $Insecure
    )

    $deadline = [DateTime]::UtcNow.AddMinutes(2)
    while ([DateTime]::UtcNow -lt $deadline) {
        $curlArguments = @('--silent', '--output', $OutputFile, '--write-out', '%{http_code}')
        if ($Insecure) {
            $curlArguments += '--insecure'
        }
        $curlArguments += $Url
        $httpCode = (& curl.exe @curlArguments 2>$null | Select-Object -Last 1)
        $curlExitCode = $LASTEXITCODE
        if ($curlExitCode -eq 0 -and $httpCode -eq '200' -and (Test-Path -LiteralPath $OutputFile)) {
            $length = (Get-Item -LiteralPath $OutputFile).Length
            if ($length -eq $ExpectedBytes) {
                return
            }
        }
        Start-Sleep -Milliseconds 250
    }
    throw "'$Url' did not return HTTP 200 with $ExpectedBytes bytes before the readiness deadline"
}

function Get-OptionalProperty {
    param(
        [Parameter(Mandatory = $true)]
        [object] $Object,

        [Parameter(Mandatory = $true)]
        [string] $Name,

        [Parameter(Mandatory = $true)]
        [object] $DefaultValue
    )

    $property = $Object.PSObject.Properties | Where-Object { $_.Name -eq $Name } | Select-Object -First 1
    if ($null -eq $property) {
        return $DefaultValue
    }
    return $property.Value
}

function Invoke-DockerCapture {
    param(
        [Parameter(Mandatory = $true)]
        [string[]] $Arguments
    )

    $output = @(& docker @Arguments)
    if ($LASTEXITCODE -ne 0) {
        throw "docker $($Arguments -join ' ') exited with code $LASTEXITCODE"
    }
    return ($output -join "`n").Trim()
}

function Get-DockerImageID {
    param(
        [Parameter(Mandatory = $true)]
        [string] $Reference
    )

    return (Invoke-DockerCapture -Arguments @('image', 'inspect', $Reference, '--format', '{{.Id}}'))
}

function Invoke-GeneratorShell {
    param(
        [Parameter(Mandatory = $true)]
        [string] $ComposeFile,

        [Parameter(Mandatory = $true)]
        [ValidateSet('wrk', 'h2load')]
        [string] $Generator,

        [Parameter(Mandatory = $true)]
        [string] $Command,

        [string[]] $Environment = @()
    )

    $arguments = @('compose', '-f', $ComposeFile, '--profile', 'load', 'run', '--rm', '--no-deps', '--entrypoint', '/bin/sh')
    foreach ($variable in $Environment) {
        $arguments += @('-e', $variable)
    }
    $arguments += @($Generator, '-c', $Command)
    Invoke-Docker -Arguments $arguments
}

function Invoke-GeneratorWarmup {
    param(
        [Parameter(Mandatory = $true)]
        [string] $ComposeFile,

        [Parameter(Mandatory = $true)]
        [object] $ScenarioDefinition,

        [Parameter(Mandatory = $true)]
        [string] $Url,

        [Parameter(Mandatory = $true)]
        [int] $DurationSeconds,

        [switch] $ForceHTTP1
    )

    $threads = [int]$ScenarioDefinition.settings.threads
    if ($ScenarioDefinition.generator -eq 'wrk') {
        $connections = [int]$ScenarioDefinition.settings.connections
        $command = "wrk -t$threads -c$connections -d${DurationSeconds}s --timeout 30s '$Url' >/dev/null 2>&1"
        Invoke-GeneratorShell -ComposeFile $ComposeFile -Generator wrk -Command $command
        return
    }

    $clients = [int]$ScenarioDefinition.settings.clients
    $streams = [int]$ScenarioDefinition.settings.streams_per_client
    $h1 = if ($ForceHTTP1) { '--h1 ' } else { '' }
    $command = "h2load ${h1}-t$threads -c$clients -m$streams -D${DurationSeconds}s --connection-inactivity-timeout=30s '$Url' >/dev/null 2>&1"
    Invoke-GeneratorShell -ComposeFile $ComposeFile -Generator h2load -Command $command
}

function Invoke-BenchmarkMeasurement {
    param(
        [Parameter(Mandatory = $true)]
        [string] $ComposeFile,

        [Parameter(Mandatory = $true)]
        [string] $ResultsDirectory,

        [Parameter(Mandatory = $true)]
        [string] $RelativeDirectory,

        [Parameter(Mandatory = $true)]
        [object] $ScenarioDefinition,

        [Parameter(Mandatory = $true)]
        [string] $Url,

        [Parameter(Mandatory = $true)]
        [int] $DurationSeconds,

        [Parameter(Mandatory = $true)]
        [int] $WarmupSeconds,

        [switch] $ForceHTTP1
    )

    $hostDirectory = Join-Path $ResultsDirectory ($RelativeDirectory -replace '/', '\')
    $null = New-Item -ItemType Directory -Force -Path $hostDirectory
    $stdoutRelative = "$RelativeDirectory/stdout.log"
    $stderrRelative = "$RelativeDirectory/stderr.log"
    $structuredRelative = "$RelativeDirectory/generator.json"
    $requestLogRelative = ''
    $threads = [int]$ScenarioDefinition.settings.threads

    if ($ScenarioDefinition.generator -eq 'wrk') {
        $connections = [int]$ScenarioDefinition.settings.connections
        $command = "wrk -t$threads -c$connections -d${DurationSeconds}s --latency --timeout 30s -s /scripts/report.lua '$Url' >'/results/$stdoutRelative' 2>'/results/$stderrRelative'"
        Invoke-GeneratorShell -ComposeFile $ComposeFile -Generator wrk -Command $command -Environment @("WRK_JSON_PATH=/results/$structuredRelative")
        $structured = Get-Content -LiteralPath (Join-Path $hostDirectory 'generator.json') -Raw | ConvertFrom-Json
        $requestErrors = [int64]$structured.errors.connect + [int64]$structured.errors.read + [int64]$structured.errors.write
        $timeouts = [int64]$structured.errors.timeout
        $non2xx = [int64]$structured.non_2xx
        $metrics = [ordered]@{
            requests_per_second      = [double]$structured.requests_per_second
            transfer_bytes_per_second = [double]$structured.transfer_bytes_per_second
            p50_us                   = [double]$structured.p50_us
            p95_us                   = [double]$structured.p95_us
            p99_us                   = [double]$structured.p99_us
        }
    }
    else {
        $clients = [int]$ScenarioDefinition.settings.clients
        $streams = [int]$ScenarioDefinition.settings.streams_per_client
        $requestLogRelative = "$RelativeDirectory/requests.tsv"
        $h1 = if ($ForceHTTP1) { '--h1 ' } else { '' }
        # h2load writes one record per request. Writing that stream directly to a
        # Docker Desktop bind mount throttles the generator and invalidates the
        # direct-control headroom check. Measure on the container filesystem,
        # then copy the completed evidence to the mounted results directory.
        $command = "h2load ${h1}-t$threads -c$clients -m$streams -D${DurationSeconds}s --warm-up-time=${WarmupSeconds}s --connection-inactivity-timeout=30s --log-file='/tmp/requests.tsv' --output-file='/tmp/generator.json' '$Url' >'/tmp/stdout.log' 2>'/tmp/stderr.log'; benchmark_status=`$?; chmod 0644 /tmp/requests.tsv /tmp/generator.json /tmp/stdout.log /tmp/stderr.log; cp /tmp/requests.tsv '/results/$requestLogRelative' && cp /tmp/generator.json '/results/$structuredRelative' && cp /tmp/stdout.log '/results/$stdoutRelative' && cp /tmp/stderr.log '/results/$stderrRelative'; copy_status=`$?; if [ `$benchmark_status -ne 0 ]; then exit `$benchmark_status; fi; exit `$copy_status"
        Invoke-GeneratorShell -ComposeFile $ComposeFile -Generator h2load -Command $command
        $structured = Get-Content -LiteralPath (Join-Path $hostDirectory 'generator.json') -Raw | ConvertFrom-Json
        $measurement = $structured.measurements
        $requestErrors = [int64]$measurement.requests.failed + [int64]$measurement.requests.errored
        $timeouts = [int64]$measurement.requests.timeout
        $non2xx = [int64]$measurement.status_codes.'3xx' + [int64]$measurement.status_codes.'4xx' + [int64]$measurement.status_codes.'5xx'
        $metrics = [ordered]@{
            requests_per_second      = [double]$measurement.request_per_second
            transfer_bytes_per_second = [double]$measurement.bytes_per_second
            p50_us                   = [double]$measurement.performance.request.median * 1000000
            p95_us                   = [double]$measurement.performance.request.p95 * 1000000
            p99_us                   = [double]$measurement.performance.request.p99 * 1000000
        }
    }

    if ($requestErrors -ne 0 -or $timeouts -ne 0 -or $non2xx -ne 0) {
        throw "generator reported errors for '$RelativeDirectory': request_errors=$requestErrors timeouts=$timeouts non_2xx=$non2xx"
    }

    return [PSCustomObject]@{
        Metrics = $metrics
        Errors = [ordered]@{
            request_errors = $requestErrors
            timeouts       = $timeouts
            non_2xx        = $non2xx
        }
        Artifacts = [ordered]@{
            stdout      = $stdoutRelative
            stderr      = $stderrRelative
            structured  = $structuredRelative
            request_log = $requestLogRelative
        }
        Directory = $hostDirectory
    }
}

function Write-RawRun {
    param(
        [Parameter(Mandatory = $true)]
        [string] $RepositoryCommit,

        [Parameter(Mandatory = $true)]
        [string] $ApisixCommit,

        [Parameter(Mandatory = $true)]
        [ValidateSet('direct', 'go', 'apisix')]
        [string] $TargetName,

        [Parameter(Mandatory = $true)]
        [string] $TargetVersion,

        [Parameter(Mandatory = $true)]
        [string] $TargetImageID,

        [Parameter(Mandatory = $true)]
        [string] $UpstreamImageID,

        [Parameter(Mandatory = $true)]
        [string] $GeneratorImageID,

        [Parameter(Mandatory = $true)]
        [string] $ScenarioName,

        [Parameter(Mandatory = $true)]
        [object] $ScenarioDefinition,

        [Parameter(Mandatory = $true)]
        [int] $PayloadBytes,

        [Parameter(Mandatory = $true)]
        [object] $ModeDefinition,

        [Parameter(Mandatory = $true)]
        [int] $Repetition,

        [Parameter(Mandatory = $true)]
        [double] $DirectRequestsPerSecond,

        [Parameter(Mandatory = $true)]
        [double] $HeadroomFactor,

        [Parameter(Mandatory = $true)]
        [object] $EnvironmentMetadata,

        [Parameter(Mandatory = $true)]
        [object] $Measurement
    )

    $threads = [int]$ScenarioDefinition.settings.threads
    $connections = [int](Get-OptionalProperty -Object $ScenarioDefinition.settings -Name connections -DefaultValue 0)
    $clients = [int](Get-OptionalProperty -Object $ScenarioDefinition.settings -Name clients -DefaultValue 0)
    $streams = [int](Get-OptionalProperty -Object $ScenarioDefinition.settings -Name streams_per_client -DefaultValue 0)
    $generatorVersion = if ($ScenarioDefinition.generator -eq 'wrk') { 'wrk-4.2.0+monotonic-clock' } else { 'h2load-1.69.0' }
    $generatorRevision = if ($ScenarioDefinition.generator -eq 'wrk') { 'a211dd5a7050b1f9e8a9870b95513060e72ac4a0' } else { 'v1.69.0' }
    $document = [ordered]@{
        schema_version     = '1.0.0'
        timestamp_utc      = [DateTime]::UtcNow.ToString('o')
        environment_class  = 'provisional'
        gateway_git_commit = $RepositoryCommit
        target             = [ordered]@{
            name          = $TargetName
            version       = $TargetVersion
            apisix_commit = $ApisixCommit
        }
        image_ids          = [ordered]@{
            target    = $TargetImageID
            upstream  = $UpstreamImageID
            generator = $GeneratorImageID
        }
        scenario           = [ordered]@{
            name      = $ScenarioName
            generator = [string]$ScenarioDefinition.generator
            protocol  = [string]$ScenarioDefinition.protocol
            tls       = [bool]$ScenarioDefinition.tls
        }
        payload_bytes       = $PayloadBytes
        generator_settings  = [ordered]@{
            generator_version = $generatorVersion
            generator_revision = $generatorRevision
            threads           = $threads
            connections       = $connections
            clients           = $clients
            streams_per_client = $streams
            warmup_seconds     = [int]$ModeDefinition.warmup_seconds
            duration_seconds   = [int]$ModeDefinition.duration_seconds
            repetition         = $Repetition
        }
        target_limits       = [ordered]@{
            cpus         = 2.0
            memory_bytes = 1073741824
            workers      = 2
        }
        environment         = $EnvironmentMetadata
        direct_control      = [ordered]@{
            requests_per_second     = $DirectRequestsPerSecond
            required_headroom_factor = $HeadroomFactor
        }
        artifacts           = $Measurement.Artifacts
        metrics             = $Measurement.Metrics
        errors              = $Measurement.Errors
    }
    Write-Utf8File -Path (Join-Path $Measurement.Directory 'raw-run.json') -Content ($document | ConvertTo-Json -Depth 10)
}

function Write-BenchmarkMetadata {
    param(
        [Parameter(Mandatory = $true)]
        [string] $Path,

        [Parameter(Mandatory = $true)]
        [string] $ModeName,

        [Parameter(Mandatory = $true)]
        [string] $RepositoryCommit,

        [Parameter(Mandatory = $true)]
        [string] $ApisixCommit,

        [Parameter(Mandatory = $true)]
        [object] $EnvironmentMetadata,

        [Parameter(Mandatory = $true)]
        [object] $Catalog
    )

    $metadata = [ordered]@{
        schema_version     = 'metadata-1'
        timestamp_utc      = [DateTime]::UtcNow.ToString('o')
        environment_class  = 'provisional'
        mode                = $ModeName
        gateway_git_commit = $RepositoryCommit
        apisix_commit       = $ApisixCommit
        environment         = $EnvironmentMetadata
        target_limits       = [ordered]@{ cpus = 2.0; memory_bytes = 1073741824; workers = 2 }
        scenarios           = $Catalog
    }
    Write-Utf8File -Path $Path -Content ($metadata | ConvertTo-Json -Depth 12)
}

try {
    $apisix = Assert-ApisixSource -Source $ApisixSource
    Write-Output "APISIX source verified: $($apisix.Path)"
    Write-Output "APISIX pinned commit: $($apisix.Commit)"
}
catch {
    [Console]::Error.WriteLine("Benchmark preflight failed: $($_.Exception.Message)")
    exit 1
}

if ($PreflightOnly) {
    exit 0
}

$composeStarted = $false
$previousApisixSource = $env:APISIX_SOURCE
$previousGatewaySource = $env:GATEWAY_SOURCE
$previousBenchResultsDirectory = $env:BENCH_RESULTS_DIR
try {
    $benchRoot = $PSScriptRoot
    $composeFile = Join-Path $benchRoot 'compose.yaml'
    $generatedDirectory = (New-Item -ItemType Directory -Force -Path (Join-Path $benchRoot 'generated')).FullName
    $payloadDirectory = (New-Item -ItemType Directory -Force -Path (Join-Path $benchRoot 'payloads')).FullName
    $certificateDirectory = (New-Item -ItemType Directory -Force -Path (Join-Path $benchRoot 'certs\generated')).FullName
    if ([string]::IsNullOrWhiteSpace($ResultsDir)) {
        $ResultsDir = Join-Path $benchRoot ("results\" + [DateTime]::UtcNow.ToString('yyyyMMddTHHmmssZ'))
    }
    $ResultsDir = (New-Item -ItemType Directory -Force -Path $ResultsDir).FullName

    $catalog = Get-Content -LiteralPath (Join-Path $benchRoot 'scenarios.yaml') -Raw | ConvertFrom-Json
    $scenarioNames = if ($Scenario -eq 'all') {
        @($catalog.scenarios.PSObject.Properties.Name)
    }
    else {
        $null = Get-ScenarioDefinition -Catalog $catalog -Name $Scenario
        @($Scenario)
    }
    $targetNames = if ($Target -eq 'all') { @('go', 'apisix') } else { @($Target) }
    $modeProperty = $catalog.modes.PSObject.Properties | Where-Object { $_.Name -eq $Mode } | Select-Object -First 1
    if ($null -eq $modeProperty) {
        throw "benchmark catalog does not define mode '$Mode'"
    }
    $modeDefinition = $modeProperty.Value
    $headroomFactor = [double]$catalog.direct_upstream_headroom_factor

    $env:APISIX_SOURCE = $apisix.Path
    $env:GATEWAY_SOURCE = (Resolve-Path -LiteralPath (Join-Path $benchRoot '..')).ProviderPath
    $env:BENCH_RESULTS_DIR = $ResultsDir
    $repositoryCommit = (Invoke-Git -Repository $env:GATEWAY_SOURCE -Arguments @('rev-parse', 'HEAD') | Select-Object -First 1).Trim()
    $environmentMetadata = [ordered]@{
        docker_version   = Invoke-DockerCapture -Arguments @('version', '--format', '{{.Server.Version}}')
        operating_system = Invoke-DockerCapture -Arguments @('info', '--format', '{{.OperatingSystem}} ({{.OSType}}/{{.Architecture}})')
        cpu              = Invoke-DockerCapture -Arguments @('info', '--format', '{{.NCPU}} logical CPUs ({{.Architecture}})')
    }
    New-BenchmarkPayloads -PayloadDirectory $payloadDirectory
    & (Join-Path $benchRoot 'certs\generate.ps1') -OutputDirectory $certificateDirectory
    $certificateFile = Join-Path $certificateDirectory 'server.crt'
    $privateKeyFile = Join-Path $certificateDirectory 'server.key'

    Invoke-Docker -Arguments @('compose', '-f', $composeFile, 'up', '--detach', '--build', 'upstream-correctness', 'upstream-performance')
    $composeStarted = $true
    Invoke-Docker -Arguments @('compose', '-f', $composeFile, '--profile', 'load', 'build', 'wrk', 'h2load')

    $upstreamImageID = Get-DockerImageID -Reference 'g-gateway-bench-upstream-performance'
    $generatorImageIDs = @{
        wrk    = Get-DockerImageID -Reference 'g-gateway-bench-wrk'
        h2load = Get-DockerImageID -Reference 'g-gateway-bench-h2load'
    }
    Write-BenchmarkMetadata -Path (Join-Path $ResultsDir 'metadata.json') -ModeName $Mode -RepositoryCommit $repositoryCommit -ApisixCommit $apisix.Commit -EnvironmentMetadata $environmentMetadata -Catalog $catalog

    foreach ($scenarioName in $scenarioNames) {
        $definition = Get-ScenarioDefinition -Catalog $catalog -Name $scenarioName
        foreach ($payloadBytes in @($definition.payload_bytes)) {
            New-GoGatewayConfig -Path (Join-Path $generatedDirectory 'gateway.yaml') -PayloadBytes $payloadBytes
            New-ApisixRouteConfig -Path (Join-Path $generatedDirectory 'apisix.yaml') -PayloadBytes $payloadBytes -CertificateFile $certificateFile -PrivateKeyFile $privateKeyFile

            foreach ($candidate in @('go', 'apisix')) {
                Stop-GatewayTarget -ComposeFile $composeFile -TargetName $candidate
            }

            $forceHTTP1 = $definition.protocol -eq 'http/1.1'
            $directDefinition = [PSCustomObject]@{
                generator = [string]$definition.generator
                protocol  = 'http/1.1'
                tls       = $false
                settings  = $definition.settings
            }
            $directUrl = "http://upstream-performance:8080/bytes/$payloadBytes"
            Write-Output "Direct control: scenario=$scenarioName payload_bytes=$payloadBytes"
            if ($definition.generator -eq 'wrk') {
                Invoke-GeneratorWarmup -ComposeFile $composeFile -ScenarioDefinition $directDefinition -Url $directUrl -DurationSeconds ([int]$modeDefinition.warmup_seconds) -ForceHTTP1
            }
            $directMeasurement = Invoke-BenchmarkMeasurement -ComposeFile $composeFile -ResultsDirectory $ResultsDir -RelativeDirectory "controls/$scenarioName/$payloadBytes/run-1" -ScenarioDefinition $directDefinition -Url $directUrl -DurationSeconds ([int]$modeDefinition.duration_seconds) -WarmupSeconds ([int]$modeDefinition.warmup_seconds) -ForceHTTP1
            $directRequestsPerSecond = [double]$directMeasurement.Metrics.requests_per_second
            Write-RawRun -RepositoryCommit $repositoryCommit -ApisixCommit $apisix.Commit -TargetName direct -TargetVersion 'nginx-1.31.3-alpine' -TargetImageID $upstreamImageID -UpstreamImageID $upstreamImageID -GeneratorImageID $generatorImageIDs[[string]$definition.generator] -ScenarioName $scenarioName -ScenarioDefinition $directDefinition -PayloadBytes $payloadBytes -ModeDefinition $modeDefinition -Repetition 1 -DirectRequestsPerSecond $directRequestsPerSecond -HeadroomFactor $headroomFactor -EnvironmentMetadata $environmentMetadata -Measurement $directMeasurement

            $targetRequestsPerSecond = @()
            for ($repetition = 1; $repetition -le [int]$modeDefinition.repetitions; $repetition++) {
                $orderedTargets = if (($repetition % 2) -eq 1) { @('go', 'apisix') } else { @('apisix', 'go') }
                foreach ($targetName in @($orderedTargets | Where-Object { $targetNames -contains $_ })) {
                    foreach ($candidate in @('go', 'apisix')) {
                        Stop-GatewayTarget -ComposeFile $composeFile -TargetName $candidate
                    }
                    Start-GatewayTarget -ComposeFile $composeFile -TargetName $targetName

                    if ($targetName -eq 'go') {
                        Wait-ForHttpResponse -Url 'http://127.0.0.1:19090/readyz' -OutputFile (Join-Path $generatedDirectory 'ready.txt') -ExpectedBytes 6
                    }
                    $scheme = if ($definition.tls) { 'https' } else { 'http' }
                    $hostName = if ($definition.tls) { 'localhost' } else { '127.0.0.1' }
                    $hostPort = if ($definition.tls) { 18443 } else { 18080 }
                    $containerPort = if ($definition.tls) { 8443 } else { 8080 }
                    $responseFile = Join-Path $generatedDirectory 'smoke-response.bin'
                    Wait-ForHttpResponse -Url "${scheme}://${hostName}:${hostPort}/bytes/$payloadBytes" -OutputFile $responseFile -ExpectedBytes $payloadBytes -Insecure:$definition.tls

                    $targetUrl = "${scheme}://gateway:${containerPort}/bytes/$payloadBytes"
                    Write-Output "Measurement: target=$targetName scenario=$scenarioName payload_bytes=$payloadBytes repetition=$repetition"
                    if ($definition.generator -eq 'wrk') {
                        Invoke-GeneratorWarmup -ComposeFile $composeFile -ScenarioDefinition $definition -Url $targetUrl -DurationSeconds ([int]$modeDefinition.warmup_seconds) -ForceHTTP1:$forceHTTP1
                    }
                    $measurement = Invoke-BenchmarkMeasurement -ComposeFile $composeFile -ResultsDirectory $ResultsDir -RelativeDirectory "$targetName/$scenarioName/$payloadBytes/run-$repetition" -ScenarioDefinition $definition -Url $targetUrl -DurationSeconds ([int]$modeDefinition.duration_seconds) -WarmupSeconds ([int]$modeDefinition.warmup_seconds) -ForceHTTP1:$forceHTTP1
                    $targetImageReference = if ($targetName -eq 'go') { 'g-gateway-bench-gateway-go' } else { 'g-gateway-bench-apisix' }
                    $targetImageID = Get-DockerImageID -Reference $targetImageReference
                    $targetVersion = if ($targetName -eq 'go') { $repositoryCommit } else { $apisix.Commit }
                    Write-RawRun -RepositoryCommit $repositoryCommit -ApisixCommit $apisix.Commit -TargetName $targetName -TargetVersion $targetVersion -TargetImageID $targetImageID -UpstreamImageID $upstreamImageID -GeneratorImageID $generatorImageIDs[[string]$definition.generator] -ScenarioName $scenarioName -ScenarioDefinition $definition -PayloadBytes $payloadBytes -ModeDefinition $modeDefinition -Repetition $repetition -DirectRequestsPerSecond $directRequestsPerSecond -HeadroomFactor $headroomFactor -EnvironmentMetadata $environmentMetadata -Measurement $measurement
                    $targetRequestsPerSecond += [double]$measurement.Metrics.requests_per_second
                    Stop-GatewayTarget -ComposeFile $composeFile -TargetName $targetName
                }
            }

            $fastestTarget = [double](($targetRequestsPerSecond | Measure-Object -Maximum).Maximum)
            $requiredDirect = $headroomFactor * $fastestTarget
            if ($directRequestsPerSecond -lt $requiredDirect) {
                throw "invalid benchmark scenario '$scenarioName' payload=${payloadBytes}: direct control $([Math]::Round($directRequestsPerSecond, 2)) req/s is below required $([Math]::Round($requiredDirect, 2)) req/s (headroom factor $headroomFactor over fastest target $([Math]::Round($fastestTarget, 2)) req/s)"
            }
            Write-Output "Headroom passed: scenario=$scenarioName payload_bytes=$payloadBytes direct_rps=$([Math]::Round($directRequestsPerSecond, 2)) fastest_target_rps=$([Math]::Round($fastestTarget, 2))"
        }
    }

    if ($Target -eq 'all') {
        $reportImage = 'g-gateway-bench-report'
        Invoke-Docker -Arguments @('build', '--file', (Join-Path $env:GATEWAY_SOURCE 'Dockerfile'), '--build-arg', 'COMMAND=bench-report', '--tag', $reportImage, $env:GATEWAY_SOURCE)
        Invoke-Docker -Arguments @('run', '--rm', '--volume', "${ResultsDir}:/results", $reportImage, '-input', '/results', '-output', '/results/summary')
    }
    else {
        Write-Output "Comparison report skipped because target '$Target' does not include both Go and APISIX."
    }

    Write-Output "Benchmark run completed. Results directory: $ResultsDir"
}
catch {
    [Console]::Error.WriteLine("Benchmark harness failed: $($_.Exception.Message)")
    exit 1
}
finally {
    if ($composeStarted) {
        $cleanupErrorPreference = $ErrorActionPreference
        $ErrorActionPreference = 'Continue'
        & docker compose -f $composeFile --profile go --profile apisix --profile load down --remove-orphans
        $cleanupExitCode = $LASTEXITCODE
        $ErrorActionPreference = $cleanupErrorPreference
        if ($cleanupExitCode -ne 0) {
            [Console]::Error.WriteLine("Benchmark cleanup warning: docker compose down exited with code $cleanupExitCode")
        }
    }
    if ($null -eq $previousApisixSource) {
        Remove-Item Env:APISIX_SOURCE -ErrorAction SilentlyContinue
    }
    else {
        $env:APISIX_SOURCE = $previousApisixSource
    }
    if ($null -eq $previousGatewaySource) {
        Remove-Item Env:GATEWAY_SOURCE -ErrorAction SilentlyContinue
    }
    else {
        $env:GATEWAY_SOURCE = $previousGatewaySource
    }
    if ($null -eq $previousBenchResultsDirectory) {
        Remove-Item Env:BENCH_RESULTS_DIR -ErrorAction SilentlyContinue
    }
    else {
        $env:BENCH_RESULTS_DIR = $previousBenchResultsDirectory
    }
}

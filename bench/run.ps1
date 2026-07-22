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

    & docker @Arguments
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
try {
    if ($Mode -ne 'smoke') {
        throw "mode '$Mode' requires the measurement runners added in Task 10; Task 9 supports topology smoke only"
    }

    $benchRoot = $PSScriptRoot
    $composeFile = Join-Path $benchRoot 'compose.yaml'
    $generatedDirectory = (New-Item -ItemType Directory -Force -Path (Join-Path $benchRoot 'generated')).FullName
    $payloadDirectory = (New-Item -ItemType Directory -Force -Path (Join-Path $benchRoot 'payloads')).FullName
    $certificateDirectory = (New-Item -ItemType Directory -Force -Path (Join-Path $benchRoot 'certs\generated')).FullName
    if ([string]::IsNullOrWhiteSpace($ResultsDir)) {
        $ResultsDir = Join-Path $benchRoot ("results\" + [DateTime]::UtcNow.ToString('yyyyMMddTHHmmssZ'))
    }
    $null = New-Item -ItemType Directory -Force -Path $ResultsDir

    $catalog = Get-Content -LiteralPath (Join-Path $benchRoot 'scenarios.yaml') -Raw | ConvertFrom-Json
    $scenarioNames = if ($Scenario -eq 'all') {
        @($catalog.scenarios.PSObject.Properties.Name)
    }
    else {
        $null = Get-ScenarioDefinition -Catalog $catalog -Name $Scenario
        @($Scenario)
    }
    $targetNames = if ($Target -eq 'all') { @('go', 'apisix') } else { @($Target) }

    $env:APISIX_SOURCE = $apisix.Path
    $env:GATEWAY_SOURCE = (Resolve-Path -LiteralPath (Join-Path $benchRoot '..')).ProviderPath
    Invoke-Docker -Arguments @('version')
    New-BenchmarkPayloads -PayloadDirectory $payloadDirectory
    & (Join-Path $benchRoot 'certs\generate.ps1') -OutputDirectory $certificateDirectory
    $certificateFile = Join-Path $certificateDirectory 'server.crt'
    $privateKeyFile = Join-Path $certificateDirectory 'server.key'

    Invoke-Docker -Arguments @('compose', '-f', $composeFile, 'up', '--detach', '--build', 'upstream-correctness', 'upstream-performance')
    $composeStarted = $true

    foreach ($scenarioName in $scenarioNames) {
        $definition = Get-ScenarioDefinition -Catalog $catalog -Name $scenarioName
        foreach ($payloadBytes in @($definition.payload_bytes)) {
            New-GoGatewayConfig -Path (Join-Path $generatedDirectory 'gateway.yaml') -PayloadBytes $payloadBytes
            New-ApisixRouteConfig -Path (Join-Path $generatedDirectory 'apisix.yaml') -PayloadBytes $payloadBytes -CertificateFile $certificateFile -PrivateKeyFile $privateKeyFile

            foreach ($targetName in $targetNames) {
                foreach ($candidate in @('go', 'apisix')) {
                    Stop-GatewayTarget -ComposeFile $composeFile -TargetName $candidate
                }
                Start-GatewayTarget -ComposeFile $composeFile -TargetName $targetName

                if ($targetName -eq 'go') {
                    Wait-ForHttpResponse -Url 'http://127.0.0.1:19090/readyz' -OutputFile (Join-Path $generatedDirectory 'ready.txt') -ExpectedBytes 6
                }
                $scheme = if ($definition.tls) { 'https' } else { 'http' }
                $port = if ($definition.tls) { 18443 } else { 18080 }
                $responseFile = Join-Path $generatedDirectory 'smoke-response.bin'
                Wait-ForHttpResponse -Url "${scheme}://127.0.0.1:${port}/bytes/$payloadBytes" -OutputFile $responseFile -ExpectedBytes $payloadBytes -Insecure:$definition.tls
                Write-Output "Smoke passed: target=$targetName scenario=$scenarioName payload_bytes=$payloadBytes"
                Stop-GatewayTarget -ComposeFile $composeFile -TargetName $targetName
            }
        }
    }

    Write-Output "Benchmark topology smoke completed. Results directory: $ResultsDir"
}
catch {
    [Console]::Error.WriteLine("Benchmark harness failed: $($_.Exception.Message)")
    exit 1
}
finally {
    if ($composeStarted) {
        $cleanupErrorPreference = $ErrorActionPreference
        $ErrorActionPreference = 'Continue'
        & docker compose -f $composeFile down --remove-orphans
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
}

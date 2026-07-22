[CmdletBinding()]
param(
    [string] $OutputDirectory = (Join-Path $PSScriptRoot 'generated')
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$output = New-Item -ItemType Directory -Force -Path $OutputDirectory
$resolvedOutput = $output.FullName
$arguments = @(
    'run', '--rm',
    '-v', "${resolvedOutput}:/certs",
    'alpine:3.21.3',
    'sh', '-c',
    "apk add --no-cache openssl >/dev/null && openssl req -x509 -newkey rsa:2048 -nodes -sha256 -days 30 -subj '/CN=gateway' -addext 'subjectAltName=DNS:gateway,DNS:localhost' -keyout /certs/server.key -out /certs/server.crt >/dev/null 2>&1 && chmod 0644 /certs/server.key /certs/server.crt"
)

& docker @arguments
if ($LASTEXITCODE -ne 0) {
    throw "certificate generation container exited with code $LASTEXITCODE"
}

$certificate = Join-Path $resolvedOutput 'server.crt'
$privateKey = Join-Path $resolvedOutput 'server.key'
if (-not (Test-Path -LiteralPath $certificate) -or -not (Test-Path -LiteralPath $privateKey)) {
    throw 'certificate generation completed without server.crt and server.key'
}

Write-Output "Generated benchmark certificate: $certificate"

param(
    [string]$Image = '',
    [ValidatePattern('^(?:|sha256:[0-9a-f]{64})$')]
    [string]$ExpectedImageID = ''
)

$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot
$webNginxCompatibilityFixtureImage = 'nginx:1.30-alpine@sha256:97d490c12ba55b4946b01546d1c3ed324e8d41ab1c9fcb2a616aa470620e5b46'
if ([string]::IsNullOrWhiteSpace($Image)) { $Image = $webNginxCompatibilityFixtureImage }
$releaseBound = -not [string]::IsNullOrWhiteSpace($ExpectedImageID)
if ($releaseBound) {
    $observedImageID = (& docker image inspect $Image --format '{{.Id}}' 2>$null | Out-String).Trim()
    if ($LASTEXITCODE -ne 0 -or $observedImageID -cne $ExpectedImageID) {
        throw "web security-header verifier image is unavailable or stale: $Image"
    }
}

function Get-ReleaseBoundAssetRequestPath {
    param([Parameter(Mandatory)][string]$Container)

    $assetPath = (& docker exec $Container sh -ec "find /usr/share/nginx/html/assets -maxdepth 1 -type f \( -name '*.js' -o -name '*.css' \) -print -quit" 2>$null | Out-String).Trim()
    if ($LASTEXITCODE -ne 0 -or
        $assetPath -notmatch '^/usr/share/nginx/html/assets/[A-Za-z0-9][A-Za-z0-9._-]*\.(?:js|css)$') {
        throw 'release-bound web image has no safely addressable built JS/CSS asset'
    }
    return $assetPath.Substring('/usr/share/nginx/html'.Length)
}

$mountArguments = @()
$assetRequestPath = ''
if (-not $releaseBound) {
    $configPath = (Resolve-Path -LiteralPath (Join-Path $projectRoot 'web\nginx.conf')).Path
    $distPath = (Resolve-Path -LiteralPath (Join-Path $projectRoot 'web\dist')).Path
    $asset = Get-ChildItem -LiteralPath (Join-Path $distPath 'assets') -File |
        Where-Object { $_.Extension -in @('.js', '.css') } |
        Select-Object -First 1
    if ($null -eq $asset) { throw 'frontend build has no testable static asset' }
    $mountArguments = @(
        '--mount', "type=bind,source=$configPath,target=/etc/nginx/conf.d/default.conf,readonly",
        '--mount', "type=bind,source=$distPath,target=/usr/share/nginx/html,readonly"
    )
    $assetRequestPath = "/assets/$($asset.Name)"
}

$container = 'invoice-web-headers-' + ([Guid]::NewGuid().ToString('N').Substring(0, 12))
$started = $false
try {
    $dockerRunArguments = @('run', '--detach', '--rm', '--name', $container, '--publish', '127.0.0.1::8080')
    $dockerRunArguments += $mountArguments
    $dockerRunArguments += $Image
    & docker @dockerRunArguments | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'failed to start web header verifier' }
    $started = $true
    $binding = (docker port $container '8080/tcp').Trim()
    if ($LASTEXITCODE -ne 0 -or $binding -notmatch ':(\d+)$') {
        throw "unable to resolve web verifier port: $binding"
    }
    $port = [int]$Matches[1]
    if ($releaseBound) {
        $assetRequestPath = Get-ReleaseBoundAssetRequestPath -Container $container
    }
    $responses = @()
    foreach ($path in @('/', $assetRequestPath)) {
        $response = $null
        $deadline = (Get-Date).AddSeconds(20)
        do {
            try {
                $response = Invoke-WebRequest -Uri "http://127.0.0.1:$port$path" -Method Head -TimeoutSec 2
            } catch {
                Start-Sleep -Milliseconds 250
            }
        } while ($null -eq $response -and (Get-Date) -lt $deadline)
        if ($null -eq $response -or $response.StatusCode -ne 200) {
            throw "web verifier could not load $path"
        }
        $responses += $response
    }
    foreach ($response in $responses) {
        $csp = [string]$response.Headers['Content-Security-Policy']
        if ($csp -notmatch "default-src 'self'" -or
            $csp -notmatch "object-src 'none'" -or
            $csp -notmatch 'https://api\.solov\.cc' -or
            $csp -notmatch 'https://xm\.solov\.cc') {
            throw 'web response is missing the approved CSP'
        }
        if ([string]$response.Headers['X-Content-Type-Options'] -ne 'nosniff' -or
            [string]$response.Headers['Referrer-Policy'] -ne 'no-referrer') {
            throw 'web response is missing mandatory security headers'
        }
    }
} finally {
    if ($started) { docker rm --force $container *> $null }
}

Write-Host 'Web root and immutable asset security headers verified with Nginx.'

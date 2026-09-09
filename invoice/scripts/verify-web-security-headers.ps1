param(
    [string]$Image = '',
    [ValidatePattern('^(?:|sha256:[0-9a-f]{64})$')]
    [string]$ExpectedImageID = ''
)

$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot
$webNginxCompatibilityFixtureImage = 'nginx:1.30-alpine@sha256:97d490c12ba55b4946b01546d1c3ed324e8d41ab1c9fcb2a616aa470620e5b46'
$approvedContentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors https://api.solov.cc https://api2.solov.cc https://xm.solov.cc https://xm2.solov.cc"
# XM-INV-ADMIN-EMBED (CR-0005): /admin is framed only by the sole approved
# first-party console origin, not by the Sub2API/New API user-embed origins
# above -- every other directive is identical to the rest of the SPA.
$approvedAdminContentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors https://console.solov.cc"
if ([string]::IsNullOrWhiteSpace($Image)) { $Image = $webNginxCompatibilityFixtureImage }
$releaseBound = -not [string]::IsNullOrWhiteSpace($ExpectedImageID)
if ($releaseBound) {
    $observedImageID = (& docker image inspect $Image --format '{{.Id}}' 2>$null | Out-String).Trim()
    if ($LASTEXITCODE -ne 0 -or $observedImageID -cne $ExpectedImageID) {
        throw "web security-header verifier image is unavailable or stale: $Image"
    }
}

function Get-ContainerDiagnostics {
    param([Parameter(Mandatory)][string]$Container)

    $inspectLines = @(& docker inspect $Container --format '{{json .State}}' 2>&1)
    $inspectExit = $LASTEXITCODE
    $inspectState = ($inspectLines | Out-String).Trim()
    if ($inspectExit -ne 0 -or [string]::IsNullOrWhiteSpace($inspectState)) {
        $inspectState = "unavailable (docker inspect exit $inspectExit): $inspectState"
    }

    $logLines = @(& docker logs $Container 2>&1)
    $logsExit = $LASTEXITCODE
    $logs = ($logLines | Out-String).TrimEnd()
    if ($logsExit -ne 0 -or [string]::IsNullOrWhiteSpace($logs)) {
        $logs = "unavailable (docker logs exit $logsExit): $logs"
    }

    return [pscustomobject]@{
        InspectState = $inspectState
        Logs = $logs
    }
}

function Assert-ExactWebHeaderResponse {
    param(
        [Parameter(Mandatory)][string]$Path,
        [Parameter(Mandatory)][string]$RawResponse,
        [string]$ExpectedContentSecurityPolicy = $approvedContentSecurityPolicy
    )

    $statusMatches = [regex]::Matches(
        $RawResponse,
        '(?m)^[ \t]*HTTP/\d+(?:\.\d+)?[ \t]+(?<status>\d{3})(?:[ \t]+[^\r\n]*)?[ \t]*\r?$'
    )
    if ($statusMatches.Count -ne 1 -or
        $statusMatches[0].Groups['status'].Value -cne '200') {
        $observedStatuses = @($statusMatches | ForEach-Object { $_.Groups['status'].Value }) -join ','
        throw "expected exactly one HTTP 200 status for $Path; observed: $observedStatuses"
    }

    foreach ($requiredHeaderLine in @(
        "Content-Security-Policy: $ExpectedContentSecurityPolicy",
        'X-Content-Type-Options: nosniff',
        'Referrer-Policy: no-referrer'
    )) {
        $headerParts = $requiredHeaderLine.Split(':', 2)
        $headerMatches = [regex]::Matches(
            $RawResponse,
            '(?im)^[ \t]*' + [regex]::Escape($headerParts[0]) + ':[ \t]*(?<value>[^\r\n]*?)[ \t]*\r?$'
        )
        if ($headerMatches.Count -ne 1) {
            throw "expected exactly one $($headerParts[0]) header for $Path; observed: $($headerMatches.Count)"
        }
        $observedValue = $headerMatches[0].Groups['value'].Value.Trim()
        if (-not [string]::Equals($observedValue, $headerParts[1].Trim(), [StringComparison]::Ordinal)) {
            throw "unexpected $($headerParts[0]) header for $Path; observed: $observedValue"
        }
    }
}

function Invoke-ContainerWebHeaderProbe {
    param(
        [Parameter(Mandatory)][string]$Container,
        [Parameter(Mandatory)][string]$Path,
        [string]$ExpectedContentSecurityPolicy = $approvedContentSecurityPolicy
    )

    $uri = "http://127.0.0.1:8080$Path"
    $deadline = (Get-Date).AddSeconds(20)
    $lastError = 'probe was not attempted'
    do {
        $probeLines = @(& docker exec $Container wget --spider -S -T 2 $uri 2>&1)
        $probeExit = $LASTEXITCODE
        $rawResponse = ($probeLines | Out-String).TrimEnd()
        if ($probeExit -eq 0) {
            try {
                Assert-ExactWebHeaderResponse -Path $Path -RawResponse $rawResponse -ExpectedContentSecurityPolicy $ExpectedContentSecurityPolicy
                return
            } catch {
                $lastError = "$($_.Exception.Message); raw response: $rawResponse"
            }
        } else {
            $lastError = "docker exec exit $probeExit; output: $rawResponse"
        }
        if ((Get-Date) -lt $deadline) { Start-Sleep -Milliseconds 250 }
    } while ((Get-Date) -lt $deadline)

    $diagnostics = Get-ContainerDiagnostics -Container $Container
    throw @"
web verifier could not load $Path within 20 seconds
last HTTP/exec error: $lastError
docker inspect state: $($diagnostics.InspectState)
docker logs:
$($diagnostics.Logs)
"@
}

function Get-ReleaseBoundAssetRequestPath {
    param([Parameter(Mandatory)][string]$Container)

    $assetPath = (& docker exec $Container sh -ec "find /usr/share/nginx/html/assets -maxdepth 1 -type f \( -name '*.js' -o -name '*.css' \) -print -quit" 2>&1 | Out-String).Trim()
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
    $dockerRunArguments = @('run', '--detach', '--name', $container)
    $dockerRunArguments += $mountArguments
    $dockerRunArguments += $Image
    & docker @dockerRunArguments | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'failed to start web header verifier' }
    $started = $true

    Invoke-ContainerWebHeaderProbe -Container $container -Path '/'
    if ($releaseBound) {
        $assetRequestPath = Get-ReleaseBoundAssetRequestPath -Container $container
    }
    Invoke-ContainerWebHeaderProbe -Container $container -Path $assetRequestPath
    # XM-INV-ADMIN-EMBED: /admin must carry the console-only frame-ancestors,
    # not the user-embed one every other path (including the asset above)
    # carries -- verified against this image's actual nginx.conf (mounted
    # locally, or baked into the release-bound image) rather than only
    # against the deploy/ template, since the two must not disagree either.
    Invoke-ContainerWebHeaderProbe -Container $container -Path '/admin' -ExpectedContentSecurityPolicy $approvedAdminContentSecurityPolicy
} finally {
    if ($started) { docker rm --force $container *> $null }
}

Write-Host 'Web root, immutable asset and admin security headers verified with Nginx.'

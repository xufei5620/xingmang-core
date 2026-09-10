$ErrorActionPreference = 'Stop'

function Resolve-InvoiceUpstreamRoot {
    param([Parameter(Mandatory)][string]$ProjectRoot)

    if (-not [string]::IsNullOrWhiteSpace($env:INVOICE_UPSTREAM_ROOT)) {
        if (-not [IO.Path]::IsPathFullyQualified($env:INVOICE_UPSTREAM_ROOT)) {
            throw 'INVOICE_UPSTREAM_ROOT must be an absolute path to the pinned upstream directory'
        }
        $pinnedRoot = [IO.Path]::GetFullPath($env:INVOICE_UPSTREAM_ROOT)
    } else {
        # The common Git directory belongs to 01-core even from a linked tree
        # under 09-wt. Never substitute the moving 06-upstream mirrors.
        $commonLines = @(& git -C $ProjectRoot rev-parse --path-format=absolute --git-common-dir 2>$null)
        $commonExit = $LASTEXITCODE
        $commonDirectory = ($commonLines | Out-String).Trim()
        if ($commonExit -ne 0 -or $commonLines.Count -ne 1 -or
            -not [IO.Path]::IsPathFullyQualified($commonDirectory)) {
            throw "git common directory resolution failed with exit $commonExit"
        }
        $repositoryRoot = Split-Path -Parent $commonDirectory
        $workspaceRoot = Split-Path -Parent $repositoryRoot
        $pinnedRoot = Join-Path $workspaceRoot '06-upstream/pinned'
    }

    if (-not (Test-Path -LiteralPath $pinnedRoot -PathType Container)) {
        throw "Pinned upstream directory does not exist or is not a directory: $pinnedRoot"
    }
    return (Resolve-Path -LiteralPath $pinnedRoot).Path
}

$upstreamRoot = Resolve-InvoiceUpstreamRoot -ProjectRoot (Split-Path -Parent $PSScriptRoot)

$upstreams = @(
    @{
        Name = 'Sub2API'
        Path = Join-Path $upstreamRoot 'sub2api-upstream-v0.1.157'
        Head = 'a2779cd5f30d6d3904a9d59088aed09507678dfe'
    },
    @{
        Name = 'Sub2API v0.1.179 contract snapshot'
        Path = Join-Path $upstreamRoot '_research\sub2api-contract-v0.1.179'
        Head = '75f88be5f75c27771836b586f7de1503afa0e3bc'
    },
    @{
        Name = 'New API agent snapshot'
        Path = Join-Path $upstreamRoot '_research\new-api-agent'
        Head = 'f116414284162ad15d8925f7bca494c109b83e93'
    },
    @{
        Name = 'New API local snapshot'
        Path = Join-Path $upstreamRoot '_research\new-api-local'
        Head = 'f116414284162ad15d8925f7bca494c109b83e93'
    }
)

foreach ($upstream in $upstreams) {
    if (-not (Test-Path -LiteralPath $upstream.Path -PathType Container)) {
        throw "$($upstream.Name): pinned upstream tree directory is missing"
    }
    # Command-scoped safe.directory handles CI/sandbox ownership without
    # mutating the user's global Git configuration.
    $safePath = $upstream.Path.Replace('\', '/')
    $headLines = @(& git -c "safe.directory=$safePath" -C $upstream.Path rev-parse --verify HEAD 2>$null)
    $headExit = $LASTEXITCODE
    $actualHead = ($headLines | Out-String).Trim()
    if ($headExit -ne 0 -or $actualHead -cnotmatch '^[0-9a-f]{40}$') {
        throw "$($upstream.Name): git HEAD failed with exit $headExit"
    }
    $changes = @(& git -c "safe.directory=$safePath" -C $upstream.Path status --porcelain=v1 2>$null)
    $statusExit = $LASTEXITCODE
    if ($statusExit -ne 0) {
        throw "$($upstream.Name): git status failed with exit $statusExit"
    }

    if ($actualHead -ne $upstream.Head) {
        throw "$($upstream.Name): HEAD changed (expected $($upstream.Head), got $actualHead)"
    }

    if ($changes.Count -ne 0) {
        throw "$($upstream.Name): working tree is not clean"
    }

    if ($actualHead -eq $upstream.Head -and $changes.Count -eq 0) {
        Write-Host "OK  $($upstream.Name)  $actualHead"
    }
}

$global:LASTEXITCODE = 0

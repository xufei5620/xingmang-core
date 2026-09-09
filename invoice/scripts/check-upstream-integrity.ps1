$ErrorActionPreference = 'Stop'

$workspaceRoot = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)

$upstreams = @(
    @{
        Name = 'Sub2API'
        Path = Join-Path $workspaceRoot 'sub2api-upstream-v0.1.157'
        Head = 'a2779cd5f30d6d3904a9d59088aed09507678dfe'
    },
    @{
        Name = 'Sub2API v0.1.179 contract snapshot'
        Path = Join-Path $workspaceRoot '_research\sub2api-contract-v0.1.179'
        Head = '75f88be5f75c27771836b586f7de1503afa0e3bc'
    },
    @{
        Name = 'New API agent snapshot'
        Path = Join-Path $workspaceRoot '_research\new-api-agent'
        Head = 'f116414284162ad15d8925f7bca494c109b83e93'
    },
    @{
        Name = 'New API local snapshot'
        Path = Join-Path $workspaceRoot '_research\new-api-local'
        Head = 'f116414284162ad15d8925f7bca494c109b83e93'
    }
)

$failed = $false
foreach ($upstream in $upstreams) {
    # Command-scoped safe.directory handles CI/sandbox ownership without
    # mutating the user's global Git configuration.
    $safePath = $upstream.Path.Replace('\', '/')
    $actualHead = (git -c "safe.directory=$safePath" -C $upstream.Path rev-parse HEAD).Trim()
    $changes = @(git -c "safe.directory=$safePath" -C $upstream.Path status --porcelain=v1)

    if ($LASTEXITCODE -ne 0) {
        Write-Error "$($upstream.Name): unable to read git state"
        $failed = $true
        continue
    }

    if ($actualHead -ne $upstream.Head) {
        Write-Error "$($upstream.Name): HEAD changed (expected $($upstream.Head), got $actualHead)"
        $failed = $true
    }

    if ($changes.Count -ne 0) {
        Write-Error "$($upstream.Name): working tree is not clean"
        $changes | ForEach-Object { Write-Error "  $_" }
        $failed = $true
    }

    if ($actualHead -eq $upstream.Head -and $changes.Count -eq 0) {
        Write-Host "OK  $($upstream.Name)  $actualHead"
    }
}

if ($failed) {
    exit 1
}

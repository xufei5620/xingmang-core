Set-StrictMode -Version Latest

class TrivyCacheMutexLease : System.IDisposable {
    [Threading.Mutex] $Mutex
    [bool] $Released = $false
    TrivyCacheMutexLease([Threading.Mutex] $mutex) { $this.Mutex = $mutex }
    [void] Dispose() {
        if (-not $this.Released) {
            $this.Mutex.ReleaseMutex()
            $this.Mutex.Dispose()
            $this.Released = $true
        }
    }
}

# Docker daemon ID is independent of context aliases, checkout/output paths,
# and user TEMP overrides. Global named mutexes serialize local processes
# accessing the same daemon and volume; an inaccessible mutex fails closed.
function Get-TrivyReleaseGateLockPath {
    param(
        [string]$ProjectRoot = '', # compatibility only; never part of the lock identity
        [ValidatePattern('^[0-9A-Za-z][0-9A-Za-z_.-]{0,127}$')]
        [string]$Volume = 'invoice-release-gate-trivy-0-74-0'
    )
    $identityOutput = & docker info --format '{{.ID}}' 2>&1
    $identityExit = $LASTEXITCODE
    $daemonId = ($identityOutput -join "`n").Trim()
    if ($identityExit -ne 0 -or $daemonId -cnotmatch '^[0-9A-Za-z][0-9A-Za-z:_.-]{0,255}$') {
        throw 'cannot establish Docker daemon identity for the shared Trivy cache lock'
    }
    $identity = "$daemonId`n$Volume"
    $hex = [Convert]::ToHexString([Security.Cryptography.SHA256]::HashData([Text.Encoding]::UTF8.GetBytes($identity))).ToLowerInvariant()
    return "Global\InvoiceTrivyCache-$hex"
}

function Enter-TrivyReleaseGateLock {
    param([Parameter(Mandatory)][string]$LockPath)
    if ($LockPath.StartsWith('Global\InvoiceTrivyCache-', [StringComparison]::Ordinal)) {
        $mutex = [Threading.Mutex]::new($false, $LockPath)
        try {
            $acquired = $false
            try { $acquired = $mutex.WaitOne(0) } catch [Threading.AbandonedMutexException] { $acquired = $true }
            if (-not $acquired) {
                $contention = [IO.IOException]::new('another process is already using the shared Trivy cache lock')
                $contention.Data['TrivyCacheLockContention'] = $true
                throw $contention
            }
            return [TrivyCacheMutexLease]::new($mutex)
        } catch { $mutex.Dispose(); throw }
    }
    # Retain the file-lock API for callers with an explicit legacy lock path.
    New-Item -ItemType Directory -Path (Split-Path -Parent $LockPath) -Force | Out-Null
    try {
        return [IO.File]::Open($LockPath, [IO.FileMode]::OpenOrCreate, [IO.FileAccess]::ReadWrite, [IO.FileShare]::None)
    } catch {
        $cause = $_.Exception
        while ($null -ne $cause.InnerException) { $cause = $cause.InnerException }
        $nativeCode = $cause.HResult -band 0xffff
        # Only sharing/locking violations mean another process owns the lock.
        # Preserve access-denied, directory, disk and other IO failures.
        if ($cause -is [IO.IOException] -and
            (($IsWindows -and $nativeCode -in @(32, 33)) -or (-not $IsWindows -and $nativeCode -eq 11))) {
            $contention = [IO.IOException]::new('another process (a release image gate run, or a concurrent Trivy cache refresh) is already using the shared Trivy cache lock', $cause)
            $contention.Data['TrivyCacheLockContention'] = $true
            throw $contention
        }
        throw
    }
}

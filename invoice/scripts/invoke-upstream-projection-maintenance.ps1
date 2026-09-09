[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [ValidateSet('newapi', 'sub2api')]
    [string]$Source,

    [Parameter(Mandatory = $true)]
    [string]$DsnFile,

    [ValidateSet('Audit', 'Apply', 'InstallSource', 'InstallEconomic', 'RollbackBridge', 'UpgradePreflight', 'CutoverQuiescencePreflight')]
    [string]$Mode = 'Audit',

    [ValidateSet('Detached', 'BridgeV4')]
    [string]$BoundaryState = 'Detached',

    [switch]$AcknowledgeSourceAgentsStopped,
    [switch]$AcknowledgeBackupVerified,
    [switch]$AcknowledgeUpstreamAppStopped,

    [string]$PsqlPath = 'psql'
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Resolve-RegularSecretFile {
    param([Parameter(Mandatory = $true)][string]$Path)

    $resolved = (Resolve-Path -LiteralPath $Path -ErrorAction Stop).ProviderPath
    $item = Get-Item -LiteralPath $resolved -Force -ErrorAction Stop
    if ($item.PSIsContainer) {
        throw 'DsnFile must be a regular file, not a directory.'
    }
    if (($item.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
        throw 'DsnFile must not be a symlink or reparse point.'
    }
    if ($item.Length -lt 1 -or $item.Length -gt 16384) {
        throw 'DsnFile has an invalid size.'
    }
    return $resolved
}

function Read-ConnectionString {
    param([Parameter(Mandatory = $true)][string]$Path)

    $value = [System.IO.File]::ReadAllText($Path).Trim()
    if ([string]::IsNullOrWhiteSpace($value) -or $value.Contains([char]0)) {
        throw 'DsnFile is empty or contains a NUL byte.'
    }
    if ($value.Contains("`r") -or $value.Contains("`n")) {
        throw 'DsnFile must contain exactly one connection-string line.'
    }
    if (
        -not $value.StartsWith('postgresql://', [System.StringComparison]::OrdinalIgnoreCase) -and
        -not $value.StartsWith('postgres://', [System.StringComparison]::OrdinalIgnoreCase) -and
        $value -notmatch '(^|\s)dbname\s*='
    ) {
        throw 'DsnFile must contain a PostgreSQL URI or libpq conninfo string.'
    }
    return $value
}

if ($Mode -in @('Apply', 'InstallSource', 'InstallEconomic', 'RollbackBridge')) {
    if (-not $AcknowledgeSourceAgentsStopped) {
        throw "$Mode requires -AcknowledgeSourceAgentsStopped."
    }
    if (-not $AcknowledgeBackupVerified) {
        throw "$Mode requires -AcknowledgeBackupVerified."
    }
}
if ($Mode -eq 'CutoverQuiescencePreflight' -and -not $AcknowledgeUpstreamAppStopped) {
    throw 'CutoverQuiescencePreflight requires -AcknowledgeUpstreamAppStopped.'
}

$repositoryRoot = Split-Path -Parent $PSScriptRoot
$sqlPath = switch ($Mode) {
    'UpgradePreflight' { Join-Path $repositoryRoot 'contracts/upstream-upgrade-preflight.postgresql.sql' }
    'CutoverQuiescencePreflight' { Join-Path $repositoryRoot 'contracts/source-cutover-quiescence-preflight.postgresql.sql' }
    'InstallSource' { Join-Path $repositoryRoot "contracts/$Source-source-projection-grants.postgresql.sql" }
    'InstallEconomic' { Join-Path $repositoryRoot "contracts/$Source-economic-projection-grants.postgresql.sql" }
    'RollbackBridge' { Join-Path $repositoryRoot "contracts/$Source-bridge-v4-rollback.postgresql.sql" }
    default { Join-Path $repositoryRoot 'contracts/projection-reconcile.postgresql.sql' }
}
if (-not (Test-Path -LiteralPath $sqlPath -PathType Leaf)) {
    throw "Required SQL contract is missing: $sqlPath"
}

$resolvedDsnFile = Resolve-RegularSecretFile -Path $DsnFile
$connectionString = Read-ConnectionString -Path $resolvedDsnFile
$psqlCommand = Get-Command -Name $PsqlPath -CommandType Application -ErrorAction Stop |
    Select-Object -First 1

$managedEnvironment = @(
    'PGAPPNAME',
    'PGCONNECT_TIMEOUT',
    'PGDATABASE',
    'PGHOST',
    'PGHOSTADDR',
    'PGOPTIONS',
    'PGPASSFILE',
    'PGPASSWORD',
    'PGPORT',
    'PGSERVICE',
    'PGSERVICEFILE',
    'PGSSLMODE',
    'PGUSER'
)
$savedEnvironment = @{}
foreach ($name in $managedEnvironment) {
    $savedEnvironment[$name] = [Environment]::GetEnvironmentVariable($name, 'Process')
    [Environment]::SetEnvironmentVariable($name, $null, 'Process')
}

try {
    # PGDATABASE keeps credentials out of the process argument list. psql is
    # also isolated from ambient PG* variables and per-user startup commands.
    [Environment]::SetEnvironmentVariable('PGDATABASE', $connectionString, 'Process')
    [Environment]::SetEnvironmentVariable('PGCONNECT_TIMEOUT', '5', 'Process')
    [Environment]::SetEnvironmentVariable(
        'PGAPPNAME',
        "invoice-$Source-$($Mode.ToLowerInvariant())",
        'Process'
    )

    $arguments = @(
        '-X',
        '--no-password',
        '--set=ON_ERROR_STOP=1',
        '--set=VERBOSITY=terse',
        "--set=invoice_source=$Source"
    )

    if ($Mode -eq 'UpgradePreflight') {
        $sqlBoundaryState = if ($BoundaryState -eq 'BridgeV4') { 'bridge-v4' } else { 'detached' }
        $arguments += "--set=boundary_state=$sqlBoundaryState"
    } elseif ($Mode -in @('Audit', 'Apply')) {
        $applyValue = if ($Mode -eq 'Apply') { '1' } else { '0' }
        $arguments += "--set=reconcile_apply=$applyValue"
    }
    $arguments += @('--file', $sqlPath)

    Write-Host "Running $Mode for $Source (connection string is intentionally not displayed)."
    & $psqlCommand.Source @arguments
    if ($LASTEXITCODE -ne 0) {
        throw "psql rejected $Mode for $Source (exit code $LASTEXITCODE). No success is recorded."
    }
    Write-Host "$Mode passed for $Source."
} finally {
    $connectionString = $null
    foreach ($name in $managedEnvironment) {
        [Environment]::SetEnvironmentVariable($name, $savedEnvironment[$name], 'Process')
    }
}

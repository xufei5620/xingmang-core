$ErrorActionPreference = 'Stop'

$projectRoot = Split-Path -Parent $PSScriptRoot
$restore = Get-Content -Raw (Join-Path $projectRoot 'deploy\backup\restore-drill.sh')
$validator = Get-Content -Raw (Join-Path $projectRoot 'deploy\backup\validate-restore-postgres-capacity.sh')
$cleanupHelper = Get-Content -Raw (Join-Path $projectRoot 'deploy\backup\docker-cleanup-state.sh')

$requiredRestoreContracts = @(
    'restore_postgres_tmpfs_size=${RESTORE_POSTGRES_TMPFS_SIZE-16g}',
    'host_available_bytes=$(awk',
    "docker info --format '{{.MemTotal}}'",
    'bash "$capacity_validator" "$restore_postgres_tmpfs_size"',
    'network_created=true',
    'started=true',
    'restore drill Docker resource name collision',
    'docker run --pull never --detach --rm --name "$container"',
    '--tmpfs "/var/lib/postgresql:rw,nosuid,nodev,size=$restore_postgres_tmpfs_size"',
    'remove_docker_resource_strict container "$container"',
    'remove_docker_resource_strict network "$network"',
    '[[ -e "$temporary" ]] && cleanup_failed=true',
    'CRITICAL: restore drill cleanup left a container, network, or temporary plaintext behind'
)
foreach ($contract in $requiredRestoreContracts) {
    if (-not $restore.Contains($contract, [StringComparison]::Ordinal)) {
        throw "restore capacity/cleanup contract is missing: $contract"
    }
}

foreach ($contract in @(
    'docker "$kind" inspect "$name"',
    'docker info >/dev/null 2>&1 || return 2',
    'docker rm --force "$name"',
    'docker network rm "$name"',
    '(( state == 2 )) && return 2'
)) {
    if (-not $cleanupHelper.Contains($contract, [StringComparison]::Ordinal)) {
        throw "restore cleanup helper lost three-state contract: $contract"
    }
}
if ($restore.Contains('size=1g', [StringComparison]::Ordinal) -or
    $restore.Contains('type=volume', [StringComparison]::Ordinal)) {
    throw 'restore PostgreSQL still uses the undersized tmpfs or a persistent volume'
}
if ($restore.Contains('docker run --detach --rm --name "$container"', [StringComparison]::Ordinal)) {
    throw 'restore PostgreSQL container can still pull outside the release gate'
}
$capacityIndex = $restore.IndexOf('restore_postgres_tmpfs_bytes=$(bash', [StringComparison]::Ordinal)
$networkIndex = $restore.IndexOf('docker network create "$network"', [StringComparison]::Ordinal)
$containerIndex = $restore.IndexOf('docker run --pull never --detach --rm --name "$container"', [StringComparison]::Ordinal)
if ($capacityIndex -lt 0 -or $networkIndex -lt 0 -or $containerIndex -lt 0 -or
    $capacityIndex -ge $networkIndex -or $networkIndex -ge $containerIndex) {
    throw 'restore capacity validation does not fail before creating Docker resources'
}

foreach ($contract in @(
    '^([89]|[12][0-9]|3[0-2])g$',
    'reserve_bytes=$((4*gib))',
    'host_available_bytes >= required_bytes',
    'docker_total_bytes >= required_bytes'
)) {
    if (-not $validator.Contains($contract, [StringComparison]::Ordinal)) {
        throw "restore capacity validator lost strict contract: $contract"
    }
}

if (-not (Get-Command bash -ErrorAction SilentlyContinue)) {
    throw 'bash is required for restore capacity boundary tests'
}
function Invoke-CapacityValidator([string]$Size, [Int64]$HostBytes, [Int64]$DockerBytes) {
    Push-Location $projectRoot
    try {
        $output = @(& bash deploy/backup/validate-restore-postgres-capacity.sh $Size $HostBytes $DockerBytes 2>&1)
        return [pscustomobject]@{ Code = $LASTEXITCODE; Output = (($output -join "`n") -replace "`0", '').Trim() }
    } finally {
        Pop-Location
    }
}

$gib = [Int64]1024 * 1024 * 1024
foreach ($fixture in @(
    @{ Size = '8g'; Required = 12 * $gib; Output = 8 * $gib },
    @{ Size = '16g'; Required = 20 * $gib; Output = 16 * $gib },
    @{ Size = '32g'; Required = 36 * $gib; Output = 32 * $gib }
)) {
    $result = Invoke-CapacityValidator $fixture.Size $fixture.Required $fixture.Required
    $numericLines = [regex]::Matches($result.Output, '(?m)^\s*([0-9]+)\s*$')
    $actualOutput = if ($numericLines.Count -gt 0) { $numericLines[$numericLines.Count - 1].Groups[1].Value } else { '' }
    if ($result.Code -ne 0 -or $actualOutput -ne [string]$fixture.Output) {
        throw "valid restore capacity rejected: size=$($fixture.Size) code=$($result.Code) output=$($result.Output)"
    }
}
foreach ($invalid in @('1g','7g','16G','16gb','33g','0g','')) {
    $result = Invoke-CapacityValidator $invalid (64 * $gib) (64 * $gib)
    if ($result.Code -eq 0) { throw "invalid restore tmpfs size accepted: '$invalid'" }
}
$required16 = 20 * $gib
if ((Invoke-CapacityValidator '16g' ($required16 - 1) (64 * $gib)).Code -eq 0) {
    throw 'restore capacity accepted insufficient host MemAvailable'
}
if ((Invoke-CapacityValidator '16g' (64 * $gib) ($required16 - 1)).Code -eq 0) {
    throw 'restore capacity accepted insufficient Docker memory'
}

Push-Location $projectRoot
try {
    & bash scripts/test-restore-cleanup-state.sh
    if ($LASTEXITCODE -ne 0) { throw 'restore Docker cleanup three-state tests failed' }
} finally {
    Pop-Location
}

$global:LASTEXITCODE = 0
Write-Host 'Restore PostgreSQL tmpfs capacity/cleanup contract passed.'

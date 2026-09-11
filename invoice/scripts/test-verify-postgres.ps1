$ErrorActionPreference = 'Stop'

$projectRoot = Split-Path -Parent $PSScriptRoot
$verifyPostgresPath = Join-Path $PSScriptRoot 'verify-postgres.ps1'
$verifyPath = Join-Path $PSScriptRoot 'verify.ps1'
$bridgeContractPath = Join-Path $projectRoot 'contracts\SOURCE-BRIDGE-V4.md'
$retiredBridgeMatrixPath = Join-Path $projectRoot 'agents\scripts\verify-bridge-postgres-matrix.ps1'
$retiredBridgeRetryPath = Join-Path $projectRoot 'agents\scripts\bridge-matrix-retry-policy.ps1'
$backendDockerfilePath = Join-Path $projectRoot 'backend\Dockerfile'
$agentDockerfilePath = Join-Path $projectRoot 'agents\Dockerfile.production'

function Assert-PostgresContainerNetworkContract {
    param([Parameter(Mandatory = $true)][string]$Source)

    foreach ($forbidden in @(
        '--publish',
        'docker port',
        '[System.Net.Sockets.TcpClient]',
        '${hostPort}',
        'host-port retries'
    )) {
        if ($Source.Contains($forbidden, [StringComparison]::OrdinalIgnoreCase)) {
            throw "verify-postgres still depends on the Docker Desktop host/NAT path: $forbidden"
        }
    }
    if ($Source -match '(?m)^\s*(?:go test -race|sh -c [''"](?:umask 077; )?go test -race)') {
        throw 'verify-postgres database-version runners duplicate the host race suite'
    }

    foreach ($required in @(
        '$backendRunnerImage = "invoice-system-postgres-backend-runner:$suffix"',
        '$agentRunnerImage = "invoice-system-postgres-agent-runner:$suffix"',
        '$contractsPath = Join-Path $projectRoot ''contracts''',
        '$monorepoRoot = Split-Path -Parent $projectRoot',
        '$target = ''/src/invoice/'' +',
        "--platform 'linux/amd64'",
        '--provenance=false',
        '--target build',
        '@(''run'', ''--rm'', ''--network'', "container:$DatabaseContainer")',
        'type=bind,source=$ContractsPath,target=/contracts,readonly',
        '@127.0.0.1:5432/${databaseName}?sslmode=disable',
        '@127.0.0.1:5432/source_contract_test?sslmode=disable',
        'go run ./cmd/migrate',
        './internal/postgresstore',
        './internal/adminsettings',
        './internal/auth',
        './service',
        './internal/application',
        './internal/migrate',
        './internal/backupverify',
        'umask 077; go test ./cmd/source-agent-prod -count=1',
        '^TestSourceReadinessActiveIndexMigrationCatalogContract$',
        'Test(BridgeV4|Sub2APIBridge|NewAPIBridge)',
        'scripts/verify.ps1 runs the full backend and agent suites with -race',
        'foreach ($runnerImage in @($backendRunnerImage, $agentRunnerImage))',
        'docker image rm --force $runnerImage'
    )) {
        if (-not $Source.Contains($required, [StringComparison]::Ordinal)) {
            throw "verify-postgres is missing the all-version container-runner contract: $required"
        }
    }

    if ([regex]::Matches($Source, '(?m)^\s*docker build\s').Count -ne 2) {
        throw 'verify-postgres must build exactly two shared temporary runner images'
    }
    if ([regex]::Matches($Source, '(?m)^\s*\$monorepoRoot\s*$').Count -ne 2) {
        throw 'both PostgreSQL runners must use the monorepo build context'
    }
    if ([regex]::Matches($Source, '(?m)^\s*--pull\s').Count -ne 2 -or
        [regex]::Matches($Source, '(?m)^\s*--target build\s').Count -ne 2) {
        throw 'both shared runner builds must refresh pinned metadata and select the build target'
    }
}

$verifyPostgresSource = Get-Content -Raw -LiteralPath $verifyPostgresPath
Assert-PostgresContainerNetworkContract -Source $verifyPostgresSource

$verifySource = Get-Content -Raw -LiteralPath $verifyPath
if ([regex]::Matches($verifySource, '(?m)^\s*go test -race -p 1 \./\.\.\.\s*$').Count -lt 2 -or
    [regex]::Matches($verifySource, '(?m)^\s*go vet \./\.\.\.\s*$').Count -lt 2) {
    throw 'host verify.ps1 no longer supplies full backend and agent race/vet coverage before database compatibility'
}
if ($verifySource.Contains('verify-bridge-postgres-matrix.ps1', [StringComparison]::Ordinal) -or
    [regex]::Matches($verifySource, '(?m)^\s*& \(Join-Path \$PSScriptRoot ''verify-postgres\.ps1''\)').Count -ne 1) {
    throw 'active verify.ps1 must invoke the consolidated PostgreSQL container-network gate exactly once'
}
if ((Test-Path -LiteralPath $retiredBridgeMatrixPath) -or (Test-Path -LiteralPath $retiredBridgeRetryPath)) {
    throw 'retired host-port bridge matrix or retry policy still exists'
}
$bridgeContract = Get-Content -Raw -LiteralPath $bridgeContractPath
foreach ($requiredContractText in @(
    'Run `scripts/verify-postgres.ps1`',
    'PostgreSQL 15 and PostgreSQL 18',
    'container network namespace'
)) {
    if (-not $bridgeContract.Contains($requiredContractText, [StringComparison]::Ordinal)) {
        throw "Bridge V4 contract does not point to the consolidated container-network gate: $requiredContractText"
    }
}
foreach ($activeGateSource in @($verifySource, $verifyPostgresSource, $bridgeContract)) {
    foreach ($forbiddenActivePath in @('--publish', 'docker port', 'TcpClient', 'verify-bridge-postgres-matrix.ps1')) {
        if ($activeGateSource.Contains($forbiddenActivePath, [StringComparison]::OrdinalIgnoreCase)) {
            throw "active PostgreSQL gate surface retains host/NAT path: $forbiddenActivePath"
        }
    }
}

$backendDockerfile = Get-Content -Raw -LiteralPath $backendDockerfilePath
if ($backendDockerfile -notmatch '(?m)^FROM golang:[^\s]+@sha256:[0-9a-f]{64} AS build$') {
    throw 'backend PostgreSQL test runner build stage is not digest pinned'
}
$agentDockerfile = Get-Content -Raw -LiteralPath $agentDockerfilePath
if ($agentDockerfile -notmatch '(?m)^ARG GO_IMAGE=golang:[^\s]+@sha256:[0-9a-f]{64}$' -or
    $agentDockerfile -notmatch '(?m)^FROM \$\{GO_IMAGE\} AS build$') {
    throw 'source-agent PostgreSQL test runner build stage is not digest pinned'
}

# Mutation checks prove the guard rejects any reintroduction of a host/NAT
# dependency rather than merely accepting the current source by accident.
foreach ($forbiddenMutation in @('--publish', 'docker port', '[System.Net.Sockets.TcpClient]')) {
    $mutationRejected = $false
    try {
        Assert-PostgresContainerNetworkContract -Source ($forbiddenMutation + "`n" + $verifyPostgresSource)
    } catch {
        $mutationRejected = $_.Exception.Message.Contains('host/NAT path', [StringComparison]::Ordinal)
    }
    if (-not $mutationRejected) {
        throw "PostgreSQL static contract did not reject forbidden mutation: $forbiddenMutation"
    }
}

Write-Host 'PostgreSQL all-version container-network source-contract fixtures passed.'

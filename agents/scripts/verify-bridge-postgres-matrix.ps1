[CmdletBinding()]
param(
    [string]$Postgres15Image = 'postgres:15-alpine@sha256:fe0737ba566a2c5b2a28f34433c0a423261900ec17b9bf7ad115e1aae7e57f1b',
    [string]$Postgres18Image = 'postgres:18-alpine@sha256:d3e1620b530c944afa6e887d22eb899824da68e19c52024bf98f5220c88a65b2'
)

$ErrorActionPreference = 'Stop'
$agentRoot = Split-Path -Parent $PSScriptRoot
$password = 'bridge_matrix_ephemeral'
. (Join-Path $PSScriptRoot 'bridge-matrix-retry-policy.ps1')

function Invoke-BridgeMatrixCase {
    param([Parameter(Mandatory)][string]$Image, [Parameter(Mandatory)][string]$Label)

    for ($attempt = 1; $attempt -le 3; $attempt++) {
        $name = 'invoice-bridge-matrix-' + $Label + '-' + [Guid]::NewGuid().ToString('N').Substring(0, 8)
        $attemptError = $null
        $cleanupExitCode = -1
        $passed = $false
        $retry = $false
        try {
            docker run --detach --name $name --publish '127.0.0.1::5432' `
                --env "POSTGRES_PASSWORD=$password" --env 'POSTGRES_DB=bridge_test' $Image | Out-Null
            if ($LASTEXITCODE -ne 0) { throw "Could not start PostgreSQL $Label matrix container" }

            $ready = $false
            for ($readyAttempt = 0; $readyAttempt -lt 60; $readyAttempt++) {
                docker exec $name pg_isready --username postgres --dbname bridge_test *> $null
                if ($LASTEXITCODE -eq 0) {
                    $ready = $true
                    break
                }
                Start-Sleep -Milliseconds 250
            }
            if (-not $ready) {
                throw "PostgreSQL $Label did not become ready"
            }

            $portLine = docker port $name '5432/tcp'
            if ($LASTEXITCODE -ne 0 -or $portLine -notmatch ':(\d+)\s*$') {
                throw "Could not resolve PostgreSQL $Label host port"
            }
            $prior = $env:SOURCE_AGENT_TEST_DATABASE_URL
            try {
                $env:SOURCE_AGENT_TEST_DATABASE_URL = "postgres://postgres:$password@127.0.0.1:$($Matches[1])/bridge_test?sslmode=disable"
                Push-Location $agentRoot
                try {
                    $testOutput = @(& go test ./cmd/source-agent-prod -run 'Test(BridgeV4|Sub2APIBridge|NewAPIBridge)' -count=1 2>&1)
                    $testExitCode = $LASTEXITCODE
                    $testOutput | ForEach-Object { Write-Host $_ }
                    if ($testExitCode -eq 0) {
                        $passed = $true
                    } else {
                        $testText = $testOutput | Out-String
                        $isTransientHostPortFailure = Test-BridgeMatrixTransientHostPortFailure -Text $testText
                        if (-not $isTransientHostPortFailure -or $attempt -eq 3) {
                            throw "Bridge V4 integration failed on PostgreSQL $Label"
                        }
                        $retry = $true
                        Write-Warning "Docker host-port connectivity failed during PostgreSQL $Label matrix attempt $attempt; retrying the unchanged test suite in a fresh container."
                    }
                }
                finally {
                    Pop-Location
                }
            }
            finally {
                $env:SOURCE_AGENT_TEST_DATABASE_URL = $prior
            }
        }
        catch {
            $attemptError = $_
        }
        finally {
            docker rm --force $name *> $null
            $cleanupExitCode = $LASTEXITCODE
        }
        Complete-BridgeMatrixAttempt -ContainerName $name -CleanupExitCode $cleanupExitCode -AttemptError $attemptError
        if ($passed) { return }
        if ($retry) { continue }
        throw "PostgreSQL $Label matrix attempt ended without a result"
    }
}

Invoke-BridgeMatrixCase -Image $Postgres15Image -Label '15'
Invoke-BridgeMatrixCase -Image $Postgres18Image -Label '18'

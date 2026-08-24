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

    $maxAttempts = 5
    for ($attempt = 1; $attempt -le $maxAttempts; $attempt++) {
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
                docker exec $name pg_isready --username postgres --dbname bridge_test 2>&1 | Out-Null
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
            $hostPort = [int]$Matches[1]
            $hostDeadline = [DateTime]::UtcNow.AddSeconds(30)
            $hostReady = $false
            do {
                $client = [Net.Sockets.TcpClient]::new()
                try {
                    $pendingConnect = $client.ConnectAsync('127.0.0.1', $hostPort)
                    $hostReady = $pendingConnect.Wait(500) -and $client.Connected
                } catch {
                    $hostReady = $false
                } finally {
                    $client.Dispose()
                }
                if (-not $hostReady) { Start-Sleep -Milliseconds 250 }
            } while (-not $hostReady -and [DateTime]::UtcNow -lt $hostDeadline)
            if (-not $hostReady) { throw "PostgreSQL $Label host port did not become reachable" }
            $prior = $env:SOURCE_AGENT_TEST_DATABASE_URL
            try {
                $env:SOURCE_AGENT_TEST_DATABASE_URL = "postgres://postgres:$password@127.0.0.1:${hostPort}/bridge_test?sslmode=disable"
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
                        if (-not $isTransientHostPortFailure -or $attempt -eq $maxAttempts) {
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
            docker rm --force $name 2>&1 | Out-Null
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

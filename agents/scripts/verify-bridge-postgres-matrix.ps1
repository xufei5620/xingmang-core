[CmdletBinding()]
param(
    [string]$Postgres15Image = 'postgres:15-alpine@sha256:fe0737ba566a2c5b2a28f34433c0a423261900ec17b9bf7ad115e1aae7e57f1b',
    [string]$Postgres18Image = 'postgres:18-alpine@sha256:d3e1620b530c944afa6e887d22eb899824da68e19c52024bf98f5220c88a65b2'
)

$ErrorActionPreference = 'Stop'
$agentRoot = Split-Path -Parent $PSScriptRoot
$password = 'bridge_matrix_ephemeral'
$containers = [System.Collections.Generic.List[string]]::new()

function Invoke-BridgeMatrixCase {
    param([Parameter(Mandatory)][string]$Image, [Parameter(Mandatory)][string]$Label)

    $name = 'invoice-bridge-matrix-' + $Label + '-' + [Guid]::NewGuid().ToString('N').Substring(0, 8)
    $containers.Add($name)
    docker run --detach --name $name --publish '127.0.0.1::5432' `
        --env "POSTGRES_PASSWORD=$password" --env 'POSTGRES_DB=bridge_test' $Image | Out-Null

    $ready = $false
    for ($attempt = 0; $attempt -lt 60; $attempt++) {
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
            go test ./cmd/source-agent-prod -run 'Test(BridgeV4|Sub2APIBridge|NewAPIBridge)' -count=1
            if ($LASTEXITCODE -ne 0) {
                throw "Bridge V4 integration failed on PostgreSQL $Label"
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

try {
    Invoke-BridgeMatrixCase -Image $Postgres15Image -Label '15'
    Invoke-BridgeMatrixCase -Image $Postgres18Image -Label '18'
}
finally {
    foreach ($container in $containers) {
        docker rm --force $container *> $null
    }
}

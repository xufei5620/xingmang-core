param(
    [Parameter(Mandatory)]
    [string]$Image,
    [Parameter(Mandatory)]
    [ValidatePattern('^sha256:[0-9a-f]{64}$')]
    [string]$ExpectedImageID,
    [Parameter(Mandatory)]
    [string]$PostgresImage,
    [Parameter(Mandatory)]
    [ValidatePattern('^sha256:[0-9a-f]{64}$')]
    [string]$ExpectedPostgresImageID,
    [Parameter(Mandatory)]
    [string]$ProbeImage,
    [Parameter(Mandatory)]
    [ValidatePattern('^sha256:[0-9a-f]{64}$')]
    [string]$ExpectedProbeImageID
)

$ErrorActionPreference = 'Stop'
$suffix = ([Guid]::NewGuid().ToString('N')).Substring(0, 12)
$network = "invoice-kc-smoke-$suffix"
$database = "invoice-kc-smoke-db-$suffix"
$keycloak = "invoice-kc-smoke-app-$suffix"
$networkCreated = $false
$databaseCreated = $false
$keycloakCreated = $false

function Assert-ImageID {
    param(
        [Parameter(Mandatory)][string]$Reference,
        [Parameter(Mandatory)][string]$ExpectedID
    )

    $observedID = (& docker image inspect $Reference --format '{{.Id}}' 2>$null | Out-String).Trim()
    if ($LASTEXITCODE -ne 0 -or $observedID -cne $ExpectedID) {
        throw "runtime image $Reference does not equal expected image ID $ExpectedID"
    }
}

try {
    Assert-ImageID -Reference $Image -ExpectedID $ExpectedImageID
    Assert-ImageID -Reference $PostgresImage -ExpectedID $ExpectedPostgresImageID
    Assert-ImageID -Reference $ProbeImage -ExpectedID $ExpectedProbeImageID

    # These artifacts are not used by the server and must stay outside its
    # runtime/SBOM. This is also a negative test for Docker COPY overlay drift.
    docker run --rm --network none --entrypoint /bin/bash $Image -ec @'
test ! -e /opt/keycloak/bin/client
! find /opt/keycloak -type f -name 'com.microsoft.sqlserver.mssql-jdbc-*.jar' -print -quit | grep -q .
'@
    if ($LASTEXITCODE -ne 0) { throw 'Keycloak runtime pruning assertion failed' }

    docker network create --internal $network | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'failed to create isolated Keycloak smoke network' }
    $networkCreated = $true

    # This database is a network-isolated, disposable fixture. Trust auth is
    # deliberately limited to this one internal Docker network and avoids
    # placing even fake passwords in process arguments or test artifacts.
    docker run --detach --name $database --network $network --network-alias postgres `
        --tmpfs '/var/lib/postgresql:rw,nosuid,nodev,size=1g' `
        --env POSTGRES_HOST_AUTH_METHOD=trust `
        --env POSTGRES_DB=keycloak `
        --env POSTGRES_USER=keycloak_app `
        $PostgresImage | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'failed to start disposable Keycloak PostgreSQL' }
    $databaseCreated = $true

    $databaseDeadline = (Get-Date).AddSeconds(90)
    do {
        docker exec $database pg_isready -U keycloak_app -d keycloak *> $null
        if ($LASTEXITCODE -eq 0) { break }
        Start-Sleep -Milliseconds 500
    } while ((Get-Date) -lt $databaseDeadline)
    if ($LASTEXITCODE -ne 0) { throw 'disposable Keycloak PostgreSQL did not become ready' }

    docker run --detach --name $keycloak --network $network --network-alias keycloak `
        --entrypoint /opt/keycloak/bin/kc.sh `
        --env 'KC_DB_URL=jdbc:postgresql://postgres:5432/keycloak' `
        --env KC_DB_USERNAME=keycloak_app `
        --env 'KC_HOSTNAME=http://keycloak:8080' `
        --env KC_HOSTNAME_STRICT=true `
        --env KC_HTTP_ENABLED=true `
        --env KC_HEALTH_ENABLED=true `
        --env 'JAVA_OPTS_KC_HEAP=-XX:MinHeapFreeRatio=10 -XX:MaxHeapFreeRatio=30 -XX:MaxRAMPercentage=65' `
        $Image start --optimized | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'failed to start hardened Keycloak image' }
    $keycloakCreated = $true

    $ready = $false
    $keycloakDeadline = (Get-Date).AddSeconds(150)
    do {
        $healthBody = docker run --rm --network $network $ProbeImage `
            wget -qO- 'http://keycloak:9000/health/ready' 2>$null
        if ($LASTEXITCODE -eq 0) {
            try {
                $health = $healthBody | ConvertFrom-Json
                if ($health.status -eq 'UP') { $ready = $true; break }
            } catch {
                # Startup may still be returning an incomplete response.
            }
        }
        Start-Sleep -Milliseconds 750
    } while ((Get-Date) -lt $keycloakDeadline)
    if (-not $ready) { throw 'hardened Keycloak did not become ready' }

    $body = docker run --rm --network $network $ProbeImage `
        wget -qO- 'http://keycloak:8080/realms/master/.well-known/openid-configuration'
    if ($LASTEXITCODE -ne 0) { throw 'Keycloak discovery endpoint failed' }
    $discovery = $body | ConvertFrom-Json
    if ($discovery.issuer -ne 'http://keycloak:8080/realms/master' -or
        -not $discovery.authorization_endpoint -or
        -not $discovery.token_endpoint -or
        -not $discovery.userinfo_endpoint -or
        -not $discovery.jwks_uri -or
        -not $discovery.end_session_endpoint -or
        $discovery.backchannel_logout_supported -ne $true -or
        $discovery.backchannel_logout_session_supported -ne $true -or
        @($discovery.id_token_signing_alg_values_supported) -notcontains 'RS256') {
        throw 'Keycloak discovery/logout/algorithm contract is incomplete'
    }

    $logs = docker logs $keycloak 2>&1 | Out-String
    if ($logs -match 'ClassNotFoundException|NoSuchMethodError|LinkageError|NoClassDefFoundError') {
        throw 'Keycloak runtime linkage failure detected after pruning'
    }
    if ($logs -notmatch 'Keycloak 26\.7\.2 .* started') {
        throw 'expected Keycloak 26.7.2 startup evidence is missing'
    }

    Write-Host 'Hardened Keycloak runtime, PostgreSQL schema, health and OIDC discovery smoke passed.'
} finally {
    if ($keycloakCreated) { docker rm --force $keycloak *> $null }
    if ($databaseCreated) { docker rm --force $database *> $null }
    if ($networkCreated) { docker network rm $network *> $null }
}

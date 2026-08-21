param(
    [string]$PostgresImage = 'postgres:18-alpine@sha256:d3e1620b530c944afa6e887d22eb899824da68e19c52024bf98f5220c88a65b2'
)

$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot
$suffix = ([Guid]::NewGuid().ToString('N')).Substring(0, 12)
$containerName = "invoice-system-pgtest-$suffix"
$databaseName = 'invoice_test'
$databaseUser = 'invoice_test'
$databasePassword = "invoice_test_$suffix"
$containerStarted = $false

try {
    docker run --detach --rm `
        --name $containerName `
        --env "POSTGRES_DB=$databaseName" `
        --env "POSTGRES_USER=$databaseUser" `
        --env "POSTGRES_PASSWORD=$databasePassword" `
        --publish '127.0.0.1::5432' `
		--tmpfs '/var/lib/postgresql:rw,nosuid,nodev,size=512m' `
        $PostgresImage | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'failed to start isolated PostgreSQL test container' }
    $containerStarted = $true

    $deadline = (Get-Date).AddSeconds(60)
    do {
        docker exec $containerName pg_isready -U $databaseUser -d $databaseName *> $null
        if ($LASTEXITCODE -eq 0) { break }
        Start-Sleep -Milliseconds 500
    } while ((Get-Date) -lt $deadline)
    if ($LASTEXITCODE -ne 0) { throw 'isolated PostgreSQL did not become ready' }

    $binding = (docker port $containerName '5432/tcp').Trim()
    if ($LASTEXITCODE -ne 0 -or $binding -notmatch ':(\d+)$') {
        throw "unable to resolve isolated PostgreSQL port: $binding"
    }
    $hostPort = [int]$Matches[1]

    # Docker Desktop can report the container ready slightly before its NAT
    # publication accepts host connections. Wait on the exact random port so
    # that an environment race is not misreported as an application failure.
    $hostDeadline = (Get-Date).AddSeconds(30)
    $hostReady = $false
    do {
        $client = [System.Net.Sockets.TcpClient]::new()
        try {
            $connect = $client.ConnectAsync('127.0.0.1', $hostPort)
            $hostReady = $connect.Wait(500) -and $client.Connected
        } catch {
            $hostReady = $false
        } finally {
            $client.Dispose()
        }
        if (-not $hostReady) { Start-Sleep -Milliseconds 250 }
    } while (-not $hostReady -and (Get-Date) -lt $hostDeadline)
    if (-not $hostReady) { throw 'isolated PostgreSQL host port did not become reachable' }

    $databaseUrl = "postgres://${databaseUser}:${databasePassword}@127.0.0.1:${hostPort}/${databaseName}?sslmode=disable"

    Push-Location (Join-Path $projectRoot 'backend')
    try {
        $env:DATABASE_URL = $databaseUrl
        $env:MIGRATIONS_DIR = 'migrations'
        # Docker Desktop on Windows can briefly withdraw a published random
        # port even after an earlier TCP probe succeeded. Migration execution
        # is transactional and checksum-idempotent, so retry this first host
        # PostgreSQL operation instead of turning a NAT race into a false gate.
        $migrationSucceeded = $false
        for ($attempt = 1; $attempt -le 15; $attempt++) {
            go run ./cmd/migrate
            if ($LASTEXITCODE -eq 0) {
                $migrationSucceeded = $true
                break
            }
            Start-Sleep -Seconds 1
        }
        if (-not $migrationSucceeded) { throw 'migration command failed after host-port retries' }

        $env:INVOICE_TEST_DATABASE_URL = $databaseUrl
        go test -race ./internal/postgresstore -count=1
        if ($LASTEXITCODE -ne 0) { throw 'PostgreSQL integration tests failed' }

        go test -race ./internal/adminsettings -count=1
        if ($LASTEXITCODE -ne 0) { throw 'admin settings PostgreSQL integration tests failed' }

        go test -race ./internal/auth -count=1
        if ($LASTEXITCODE -ne 0) { throw 'authentication PostgreSQL integration tests failed' }

        go test -race ./internal/oidcretention -count=1
        if ($LASTEXITCODE -ne 0) { throw 'OIDC logout retention PostgreSQL integration tests failed' }

        go test -race ./internal/application -count=1
        if ($LASTEXITCODE -ne 0) { throw 'application PostgreSQL integration tests failed' }

        go test -race ./internal/migrate -count=1
        if ($LASTEXITCODE -ne 0) { throw 'migration exact-set/atomicity PostgreSQL integration tests failed' }

        go test -race ./internal/backupverify -count=1
        if ($LASTEXITCODE -ne 0) { throw 'backup document restore verification PostgreSQL integration tests failed' }

        docker exec $containerName psql -U $databaseUser -d $databaseName -v ON_ERROR_STOP=1 -c "CREATE ROLE invoice_app LOGIN PASSWORD 'invoice_app_test_only'" *> $null
        if ($LASTEXITCODE -ne 0) { throw 'failed to create isolated runtime role' }
        $permissionsFile = Join-Path $projectRoot 'deploy\postgres\harden-runtime-role.sql'
        docker cp $permissionsFile "${containerName}:/tmp/harden-runtime-role.sql" *> $null
        if ($LASTEXITCODE -ne 0) { throw 'failed to copy runtime permission policy' }
        docker exec $containerName psql -U $databaseUser -d $databaseName -v ON_ERROR_STOP=1 -f /tmp/harden-runtime-role.sql *> $null
        if ($LASTEXITCODE -ne 0) { throw 'runtime permission policy failed' }
        docker exec -e PGPASSWORD=invoice_app_test_only $containerName psql -U invoice_app -d $databaseName -v ON_ERROR_STOP=1 -c "INSERT INTO audit_events(id,actor_type,actor_id,action,object_type,object_id,request_id) VALUES('00000000-0000-4000-8000-000000000099','system','permission-test','permission.test','permission_test','1','permission-test')" *> $null
        if ($LASTEXITCODE -ne 0) { throw 'runtime role cannot append audit events' }
        docker exec -e PGPASSWORD=invoice_app_test_only $containerName psql -U invoice_app -d $databaseName -v ON_ERROR_STOP=1 -c "INSERT INTO oidc_backchannel_logout_events(id,issuer_hash,jti_hash,sid_hash,token_issued_at,token_expires_at,received_at,revoked_session_count,request_id) VALUES('00000000-0000-4000-8000-000000000098',repeat('a',64),repeat('b',64),repeat('c',64),now()-interval '1 minute',now()+interval '1 minute',now(),0,'permission-test')" *> $null
        if ($LASTEXITCODE -ne 0) { throw 'runtime role cannot append OIDC logout replay records' }
        $privileges = (docker exec -e PGPASSWORD=invoice_app_test_only $containerName psql -U invoice_app -d $databaseName -At -F '|' -c "SELECT has_table_privilege(current_user,'audit_events','INSERT'),has_table_privilege(current_user,'audit_events','UPDATE'),has_table_privilege(current_user,'audit_events','DELETE'),has_table_privilege(current_user,'oidc_backchannel_logout_events','UPDATE'),has_table_privilege(current_user,'oidc_backchannel_logout_events','DELETE'),has_table_privilege(current_user,'source_events','UPDATE'),has_table_privilege(current_user,'source_ingest_batches','UPDATE'),has_table_privilege(current_user,'payment_candidate_reviews','UPDATE'),has_table_privilege(current_user,'source_usage_events','UPDATE'),has_table_privilege(current_user,'source_credit_events','UPDATE'),has_table_privilege(current_user,'balance_reconciliation_checkpoints','UPDATE'),has_schema_privilege(current_user,'public','CREATE'),has_table_privilege(current_user,'schema_migrations','SELECT'),has_database_privilege(current_user,current_database(),'TEMP'),r.rolsuper,r.rolcreatedb,r.rolcreaterole,r.rolreplication,r.rolbypassrls,r.rolinherit,r.rolconnlimit,EXISTS(SELECT 1 FROM pg_auth_members m WHERE m.member=r.oid) FROM pg_roles r WHERE r.rolname=current_user").Trim()
        if ($LASTEXITCODE -ne 0 -or $privileges -ne 't|f|f|f|f|f|f|f|f|f|f|f|t|f|f|f|f|f|f|f|20|f') { throw "unexpected runtime role privileges: $privileges" }
        docker exec -e PGPASSWORD=invoice_app_test_only $containerName psql -U invoice_app -d $databaseName -v ON_ERROR_STOP=1 -c "UPDATE audit_events SET action='tampered' WHERE id='00000000-0000-4000-8000-000000000099'" *> $null
        if ($LASTEXITCODE -eq 0) { throw 'runtime role can mutate immutable audit events' }
        docker exec -e PGPASSWORD=invoice_app_test_only $containerName psql -U invoice_app -d $databaseName -v ON_ERROR_STOP=1 -c "UPDATE oidc_backchannel_logout_events SET revoked_session_count=99 WHERE id='00000000-0000-4000-8000-000000000098'" *> $null
        if ($LASTEXITCODE -eq 0) { throw 'runtime role can mutate immutable OIDC logout replay records' }
        docker exec -e PGPASSWORD=invoice_app_test_only $containerName psql -U invoice_app -d $databaseName -v ON_ERROR_STOP=1 -c "DELETE FROM oidc_backchannel_logout_events WHERE id='00000000-0000-4000-8000-000000000098'" *> $null
        if ($LASTEXITCODE -eq 0) { throw 'runtime role can delete OIDC logout replay records' }
    } finally {
        Remove-Item Env:DATABASE_URL, Env:MIGRATIONS_DIR, Env:INVOICE_TEST_DATABASE_URL -ErrorAction SilentlyContinue
        Pop-Location
    }

    Push-Location (Join-Path $projectRoot 'agents')
    try {
        $env:SOURCE_AGENT_TEST_DATABASE_URL = $databaseUrl
        go test -race ./cmd/source-agent-prod -count=1
        if ($LASTEXITCODE -ne 0) { throw 'source projection privilege integration tests failed' }
    } finally {
        Remove-Item Env:SOURCE_AGENT_TEST_DATABASE_URL -ErrorAction SilentlyContinue
        Pop-Location
    }
} finally {
    if ($containerStarted) {
        docker rm --force $containerName *> $null
    }
}

Write-Host "PostgreSQL migration and integration verification passed in isolated container $containerName."

param(
    [string]$PostgresImage = 'postgres:18-alpine@sha256:d3e1620b530c944afa6e887d22eb899824da68e19c52024bf98f5220c88a65b2',
    [string]$Postgres15Image = 'postgres:15-alpine@sha256:fe0737ba566a2c5b2a28f34433c0a423261900ec17b9bf7ad115e1aae7e57f1b',
    [string]$Postgres15FallbackImageID = 'sha256:fe0737ba566a2c5b2a28f34433c0a423261900ec17b9bf7ad115e1aae7e57f1b'
)

$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot
$consumptionStoreSource = Get-Content -Raw (Join-Path $projectRoot 'backend\internal\postgresstore\consumption.go')
$normalizedConsumptionStore = [regex]::Replace($consumptionStoreSource, '\s+', ' ')
$forbiddenImmutableRowLocks = @(
    'FROM source_cutover_manifests WHERE source_instance_id=$1 FOR UPDATE',
    'FROM source_cutover_manifests WHERE source_instance_id=$1 FOR SHARE',
    "FROM source_credit_events WHERE funding_lot_id=`$1 AND credit_kind='PRE_POLICY_NON_INVOICEABLE' FOR UPDATE",
    'ORDER BY b.as_of,b.id FOR SHARE OF b',
    "AND b.schema_version='3.0' FOR SHARE OF b"
)
foreach ($forbiddenLock in $forbiddenImmutableRowLocks) {
    if ($normalizedConsumptionStore.Contains($forbiddenLock, [StringComparison]::Ordinal)) {
        throw "runtime store attempts a row lock on an immutable projection table: $forbiddenLock"
    }
}
function Invoke-PostgresContainerCommand {
    param(
        [Parameter(Mandatory = $true)][string]$DatabaseContainer,
        [Parameter(Mandatory = $true)][string]$RunnerImage,
        [Parameter(Mandatory = $true)][string[]]$Environment,
        [Parameter(Mandatory = $true)][string]$Command,
        [string]$ContractsPath,
        [string[]]$RepoRootMounts = @(),
        [Parameter(Mandatory = $true)][string]$FailureMessage
    )

    $arguments = @('run', '--rm', '--network', "container:$DatabaseContainer")
    if (-not [string]::IsNullOrWhiteSpace($ContractsPath)) {
        $arguments += @('--mount', "type=bind,source=$ContractsPath,target=/contracts,readonly")
    }
    # 后端测试有一批按「源文件往上三级 = 仓库根」去读仓库其它目录：eligibilitywire 读
    # contracts/ 与 backend/{migrations,internal}，又按全仓发现扫 agents/；就绪原因对拍闸读
    # web/src/App.tsx；验证脚本同步闸读 deploy/postgres/…。运行器镜像只带 /src，仓库根解析成
    # 容器根，所以把这些目录按同名只读挂到 / 下（与 /src 是同一份源码）。RC106 第一次门禁死在
    # contracts/，第二次死在 backend/，RC107 第一次死在 web/ 与 deploy/——手列一个补一个的
    # 形状，这里一次把仓库根下会被读的目录全部挂上，调用方传清单。
    foreach ($relative in $RepoRootMounts) {
        $sourcePath = Join-Path $projectRoot $relative
        if (-not (Test-Path -LiteralPath $sourcePath)) { throw "repo mount source missing: $relative" }
        $target = '/' + ($relative -replace '\\', '/')
        $arguments += @('--mount', "type=bind,source=$sourcePath,target=$target,readonly")
    }
    foreach ($environmentEntry in $Environment) {
        $arguments += @('--env', $environmentEntry)
    }
    $arguments += @($RunnerImage, 'sh', '-c', $Command)
    & docker @arguments
    $commandExit = $LASTEXITCODE
    if ($commandExit -ne 0) { throw "$FailureMessage (container exit $commandExit)" }
}

$suffix = ([Guid]::NewGuid().ToString('N')).Substring(0, 12)
$containerName = "invoice-system-pgtest-$suffix"
$source15Container = "invoice-source-pg15-$suffix"
$backendRunnerImage = "invoice-system-postgres-backend-runner:$suffix"
$agentRunnerImage = "invoice-system-postgres-agent-runner:$suffix"
$contractsPath = Join-Path $projectRoot 'contracts'
$databaseName = 'invoice_test'
$databaseUser = 'invoice_test'
$databasePassword = "invoice_test_$suffix"
$containerStarted = $false
$source15Started = $false

# scripts/verify.ps1 runs the full backend and agent suites with -race and vet
# before this script. These Linux runners add real PostgreSQL 15/18 SQL and
# migration compatibility without duplicating race coverage or crossing the
# Docker Desktop host/NAT boundary. BuildKit's normal cache remains reusable.
try {
    docker build `
        --pull `
        --platform 'linux/amd64' `
        --provenance=false `
        --target build `
        --tag $backendRunnerImage `
        --file (Join-Path $projectRoot 'backend\Dockerfile') `
        (Join-Path $projectRoot 'backend')
    if ($LASTEXITCODE -ne 0) { throw 'failed to build shared PostgreSQL backend test runner image' }

    docker build `
        --pull `
        --platform 'linux/amd64' `
        --provenance=false `
        --target build `
        --tag $agentRunnerImage `
        --file (Join-Path $projectRoot 'agents\Dockerfile.production') `
        (Join-Path $projectRoot 'agents')
    if ($LASTEXITCODE -ne 0) { throw 'failed to build shared PostgreSQL source-agent test runner image' }

    try {
    # XM-INV-GATE-TMPFS: the whole backend suite shares this sidecar; its WAL alone reached
    # ~320 MiB before the RC73 gate's PostgreSQL aborted on a full 512 MiB tmpfs (SIGABRT, then
    # 'the database system is in recovery mode'). The 2g tmpfs and the WAL bounds passed as
    # server arguments below are test-sidecar-only settings.
    docker run --detach --rm `
        --name $containerName `
        --env "POSTGRES_DB=$databaseName" `
        --env "POSTGRES_USER=$databaseUser" `
        --env "POSTGRES_PASSWORD=$databasePassword" `
		--tmpfs '/var/lib/postgresql:rw,nosuid,nodev,size=2g' `
        $PostgresImage `
        postgres -c max_wal_size=256MB -c min_wal_size=64MB -c checkpoint_timeout=60s | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'failed to start isolated PostgreSQL test container' }
    $containerStarted = $true

    $deadline = (Get-Date).AddSeconds(60)
    do {
        docker exec $containerName pg_isready -U $databaseUser -d $databaseName *> $null
        if ($LASTEXITCODE -eq 0) { break }
        Start-Sleep -Milliseconds 500
    } while ((Get-Date) -lt $deadline)
    if ($LASTEXITCODE -ne 0) { throw 'isolated PostgreSQL did not become ready' }

    $databaseUrl = "postgres://${databaseUser}:${databasePassword}@127.0.0.1:5432/${databaseName}?sslmode=disable"
    Invoke-PostgresContainerCommand `
        -DatabaseContainer $containerName `
        -RunnerImage $backendRunnerImage `
        -Environment @("DATABASE_URL=$databaseUrl", 'MIGRATIONS_DIR=migrations') `
        -Command 'go run ./cmd/migrate' `
        -FailureMessage 'migration command failed against PostgreSQL 18'

    # Exercise the production cleanup plan under PostgreSQL's real read-only
    # enforcement. An empty fixture must fail only at the exact tuple gate.
    $cleanupSQL = Get-Content -Raw (Join-Path $projectRoot 'deploy\postgres\balance-history-cleanup.sql')
        $cleanupProbeOutput = $cleanupSQL | docker exec -i `
            --env 'PGOPTIONS=-c default_transaction_read_only=on -c statement_timeout=2min -c lock_timeout=5s' `
            $containerName psql -X -q -v ON_ERROR_STOP=1 -U $databaseUser -d $databaseName `
            -v apply_cleanup=false `
            -v keep_rows_path=/tmp/solov-cleanup-probe-keep.rows `
            -v purge_rows_path=/tmp/solov-cleanup-probe-purge.rows -f - 2>&1
        $cleanupProbeExit = $LASTEXITCODE
        $cleanupProbeText = @($cleanupProbeOutput) -join "`n"
        if ($cleanupProbeExit -eq 0 -or
            -not $cleanupProbeText.Contains('balance history cleanup tuple mismatch', [StringComparison]::Ordinal) -or
            $cleanupProbeText.Contains('cannot execute CREATE TABLE', [StringComparison]::OrdinalIgnoreCase) -or
            $cleanupProbeText.Contains('cannot execute ALTER TABLE', [StringComparison]::OrdinalIgnoreCase)) {
            throw "balance cleanup read-only transaction probe failed at the wrong gate: $cleanupProbeText"
        }
        $persistentCleanupRelations = docker exec $containerName psql -X -q -At -v ON_ERROR_STOP=1 `
            -U $databaseUser -d $databaseName `
            -c "SELECT count(*) FROM pg_catalog.pg_class WHERE relname LIKE 'cleanup\_%' ESCAPE '\';"
        if ($LASTEXITCODE -ne 0 -or [int64]$persistentCleanupRelations -ne 0) {
            throw 'balance cleanup read-only transaction probe left a persistent relation'
        }

        $backendDatabaseTests = @(
            [pscustomobject]@{ Package = './internal/postgresstore'; Failure = 'PostgreSQL integration tests failed' },
            [pscustomobject]@{ Package = './internal/adminsettings'; Failure = 'admin settings PostgreSQL integration tests failed' },
            [pscustomobject]@{ Package = './internal/auth'; Failure = 'authentication PostgreSQL integration tests failed' },
            [pscustomobject]@{ Package = './internal/oidcretention'; Failure = 'OIDC logout retention PostgreSQL integration tests failed' },
            [pscustomobject]@{ Package = './internal/application'; Failure = 'application PostgreSQL integration tests failed' },
            [pscustomobject]@{ Package = './internal/migrate'; Failure = 'migration exact-set/atomicity PostgreSQL integration tests failed' },
            [pscustomobject]@{ Package = './internal/backupverify'; Failure = 'backup document restore verification PostgreSQL integration tests failed' }
        )
        foreach ($databaseTest in $backendDatabaseTests) {
            # XM-INV-LOT-REASON-CONTRACT: application 里的资格摘要契约测试经 eligibilitywire.Load()
            # 读仓库根下的 contracts/invoice-eligibility-wire.v1.json（repoRoot = 源文件往上三级）。
            # 运行器镜像只带 backend/，根就是容器根，所以要把 contracts/ 只读挂到 /contracts，
            # 否则那条测试在会话里绿、在这里红（RC106 门禁第一次跑就是这样死的）。
            Invoke-PostgresContainerCommand `
                -DatabaseContainer $containerName `
                -RunnerImage $backendRunnerImage `
                -Environment @("INVOICE_TEST_DATABASE_URL=$databaseUrl") `
                -Command "go test $($databaseTest.Package) -count=1" `
                -ContractsPath $contractsPath `
                -RepoRootMounts @('backend', 'agents', 'web\src', 'deploy', 'docs') `
                -FailureMessage $databaseTest.Failure
        }

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
        $privileges = (docker exec -e PGPASSWORD=invoice_app_test_only $containerName psql -U invoice_app -d $databaseName -At -F '|' -c "SELECT has_table_privilege(current_user,'audit_events','INSERT'),has_table_privilege(current_user,'audit_events','UPDATE'),has_table_privilege(current_user,'audit_events','DELETE'),has_table_privilege(current_user,'oidc_backchannel_logout_events','UPDATE'),has_table_privilege(current_user,'oidc_backchannel_logout_events','DELETE'),has_table_privilege(current_user,'source_events','UPDATE'),has_table_privilege(current_user,'source_ingest_batches','UPDATE'),has_table_privilege(current_user,'payment_candidate_reviews','UPDATE'),has_table_privilege(current_user,'source_usage_events','UPDATE'),has_table_privilege(current_user,'source_credit_events','UPDATE'),has_table_privilege(current_user,'balance_reconciliation_checkpoints','UPDATE'),has_table_privilege(current_user,'balance_carry_forward_proofs','UPDATE'),has_table_privilege(current_user,'balance_carry_forward_evaluations','UPDATE'),has_table_privilege(current_user,'consumption_allocations','UPDATE'),has_table_privilege(current_user,'invoice_eligibility_policy','SELECT'),has_table_privilege(current_user,'invoice_eligibility_policy','UPDATE'),has_schema_privilege(current_user,'public','CREATE'),has_table_privilege(current_user,'schema_migrations','SELECT'),has_database_privilege(current_user,current_database(),'TEMP'),r.rolsuper,r.rolcreatedb,r.rolcreaterole,r.rolreplication,r.rolbypassrls,r.rolinherit,r.rolconnlimit,EXISTS(SELECT 1 FROM pg_auth_members m WHERE m.member=r.oid) FROM pg_roles r WHERE r.rolname=current_user").Trim()
        if ($LASTEXITCODE -ne 0 -or $privileges -ne 't|f|f|f|f|f|f|f|f|f|f|f|f|f|t|f|f|t|f|f|f|f|f|f|f|20|f') { throw "unexpected runtime role privileges: $privileges" }
        docker exec -e PGPASSWORD=invoice_app_test_only $containerName psql -U invoice_app -d $databaseName -v ON_ERROR_STOP=1 -c "SELECT eligibility_start_at,policy_version FROM invoice_eligibility_policy WHERE singleton_id=1" *> $null
        if ($LASTEXITCODE -ne 0) { throw 'runtime role cannot read immutable invoice eligibility policy' }
        docker exec $containerName psql -U $databaseUser -d $databaseName -v ON_ERROR_STOP=1 -c "INSERT INTO source_instances(id,source_type,name,runtime_version) VALUES('f1000000-0000-4000-8000-000000000001','sub2api','runtime-policy-negative','fixture-runtime'),('f1000000-0000-4000-8000-000000000002','sub2api','runtime-policy-positive','runtime-test') ON CONFLICT(id) DO NOTHING" *> $null
        if ($LASTEXITCODE -ne 0) { throw 'failed to seed runtime manifest policy checks' }
        docker exec $containerName psql -U $databaseUser -d $databaseName -v ON_ERROR_STOP=1 -c "INSERT INTO source_instances(id,source_type,name,runtime_version) VALUES('f1000000-0000-4000-8000-000000000003','sub2api','runtime-batch-lock-check','runtime-test'); INSERT INTO source_ingest_state(source_instance_id,stream_id) VALUES('f1000000-0000-4000-8000-000000000003','balances'); INSERT INTO source_ingest_batches(source_instance_id,stream_id,batch_id,sequence,body_hash,signing_key_id,record_count,schema_version,stream_watermark_at,source_cursor,scan_ceiling_at,scan_ceiling_cursor,scan_cycle_id,scan_complete,scan_snapshot_id,scan_snapshot_row_count) VALUES('f1000000-0000-4000-8000-000000000003','balances','f3000000-0000-4000-8000-000000000002',1,repeat('1',64),'runtime-key',1,'3.0',now(),'balance_snapshot:0',now(),'balance_snapshot:0','f3000000-0000-4000-8000-000000000001',false,repeat('3',64),0); INSERT INTO source_ingest_events(source_instance_id,stream_id,event_id,first_batch_id,entity_type,operation,payload_hash,payload_ciphertext,observed_at) VALUES('f1000000-0000-4000-8000-000000000003','balances','f3000000-0000-4000-8000-000000000003','f3000000-0000-4000-8000-000000000002','cutover_manifest','upsert',repeat('2',64),decode(repeat('00',16),'hex'),now()); INSERT INTO source_economic_scan_cycles(source_instance_id,stream_id,scan_cycle_id,stream_watermark_at,source_cursor,scan_ceiling_at,scan_ceiling_cursor,scan_snapshot_id,scan_snapshot_row_count,first_sequence,last_sequence,cycle_status) VALUES('f1000000-0000-4000-8000-000000000003','balances','f3000000-0000-4000-8000-000000000001',now(),'balance_snapshot:0',now(),'balance_snapshot:0',repeat('3',64),0,1,1,'receiving'); INSERT INTO source_economic_scan_cycle_events(source_instance_id,stream_id,scan_cycle_id,event_id,batch_id,payload_hash) VALUES('f1000000-0000-4000-8000-000000000003','balances','f3000000-0000-4000-8000-000000000001','f3000000-0000-4000-8000-000000000003','f3000000-0000-4000-8000-000000000002',repeat('2',64))" *> $null
        if ($LASTEXITCODE -ne 0) { throw 'failed to seed immutable-batch runtime lock check' }
        docker exec -e PGPASSWORD=invoice_app_test_only $containerName psql -U invoice_app -d $databaseName -v ON_ERROR_STOP=1 -c "BEGIN; SELECT m.payload_hash,b.signing_key_id,b.scan_ceiling_at,c.cycle_status FROM source_economic_scan_cycle_events m JOIN source_ingest_batches b ON b.source_instance_id=m.source_instance_id AND b.stream_id=m.stream_id AND b.batch_id=m.batch_id JOIN source_economic_scan_cycles c ON c.source_instance_id=m.source_instance_id AND c.stream_id=m.stream_id AND c.scan_cycle_id=m.scan_cycle_id WHERE m.source_instance_id='f1000000-0000-4000-8000-000000000003' AND m.stream_id='balances' AND m.event_id='f3000000-0000-4000-8000-000000000003'::uuid AND m.batch_id='f3000000-0000-4000-8000-000000000002'::uuid AND m.scan_cycle_id='f3000000-0000-4000-8000-000000000001'::uuid AND b.schema_version='3.0' FOR SHARE OF c; ROLLBACK" *> $null
        if ($LASTEXITCODE -ne 0) { throw 'runtime role cannot lock the mutable scan cycle without UPDATE on immutable batches' }
        docker exec -e PGPASSWORD=invoice_app_test_only $containerName psql -U invoice_app -d $databaseName -v ON_ERROR_STOP=1 -c "INSERT INTO source_cutover_manifests(source_instance_id,manifest_hash,cutover_at,database_clock,source_runtime_version,projection_contract,configuration_hash,unit_code,payments_ceiling,usage_ceiling,credits_ceiling,balances_ceiling,baseline_snapshot_id,baseline_snapshot_hash,baseline_row_count,signing_key_id) VALUES('f1000000-0000-4000-8000-000000000001',repeat('a',64),'2026-08-31T15:59:59.999999Z','2026-08-31T15:59:59.999999Z','fixture-runtime','fixture-v3',repeat('b',64),'SUB2_BALANCE_1E8','p','u','c','b',repeat('c',64),repeat('c',64),0,'fixture')" *> $null
        if ($LASTEXITCODE -eq 0) { throw 'runtime role inserted owner-only fixture manifest' }
        docker exec -e PGPASSWORD=invoice_app_test_only $containerName psql -U invoice_app -d $databaseName -v ON_ERROR_STOP=1 -c "INSERT INTO source_cutover_manifests(source_instance_id,manifest_hash,cutover_at,database_clock,source_runtime_version,projection_contract,configuration_hash,unit_code,payments_ceiling,usage_ceiling,credits_ceiling,balances_ceiling,baseline_snapshot_id,baseline_snapshot_hash,baseline_row_count,signing_key_id) VALUES('f1000000-0000-4000-8000-000000000001',repeat('a',64),'2026-08-31T15:59:59.999999Z','2026-08-31T15:59:59.999999Z','fixture-runtime','wrong-contract',repeat('b',64),'SUB2_BALANCE_1E8','p','u','c','b',repeat('c',64),repeat('c',64),0,'runtime-key')" *> $null
        if ($LASTEXITCODE -eq 0) { throw 'runtime role inserted wrong source projection contract' }
        docker exec -e PGPASSWORD=invoice_app_test_only $containerName psql -U invoice_app -d $databaseName -v ON_ERROR_STOP=1 -c "INSERT INTO source_cutover_manifests(source_instance_id,manifest_hash,cutover_at,database_clock,source_runtime_version,projection_contract,configuration_hash,unit_code,payments_ceiling,usage_ceiling,credits_ceiling,balances_ceiling,baseline_snapshot_id,baseline_snapshot_hash,baseline_row_count,signing_key_id) VALUES('f1000000-0000-4000-8000-000000000001',repeat('a',64),'2026-08-31T16:00:00Z','2026-08-31T16:00:00Z','fixture-runtime','sub2api-economic-v4',repeat('b',64),'SUB2_BALANCE_1E8','p','u','c','b',repeat('c',64),repeat('c',64),0,'runtime-key')" *> $null
        if ($LASTEXITCODE -eq 0) { throw 'runtime role inserted a source cutover at the eligibility boundary' }
        docker exec -e PGPASSWORD=invoice_app_test_only $containerName psql -U invoice_app -d $databaseName -v ON_ERROR_STOP=1 -c "INSERT INTO source_cutover_manifests(source_instance_id,manifest_hash,cutover_at,database_clock,source_runtime_version,projection_contract,configuration_hash,unit_code,payments_ceiling,usage_ceiling,credits_ceiling,balances_ceiling,baseline_snapshot_id,baseline_snapshot_hash,baseline_row_count,signing_key_id) VALUES('f1000000-0000-4000-8000-000000000002',repeat('d',64),'2026-08-31T15:59:59.999999Z','2026-08-31T15:59:59.999999Z','runtime-test','sub2api-economic-v4',repeat('e',64),'SUB2_BALANCE_1E8','p','u','c','b',repeat('f',64),repeat('f',64),0,'runtime-key')" *> $null
        if ($LASTEXITCODE -ne 0) { throw 'runtime role cannot insert an exact pre-policy production manifest' }
        docker exec $containerName psql -U $databaseUser -d $databaseName -v ON_ERROR_STOP=1 -c "INSERT INTO invoice_users(id,oidc_issuer,oidc_subject) VALUES('f2000000-0000-4000-8000-000000000001','test','runtime-lock-user'); INSERT INTO invoice_profiles(id,invoice_user_id,profile_type,title_ciphertext,email_ciphertext,email_verified) VALUES('f4000000-0000-4000-8000-000000000001','f2000000-0000-4000-8000-000000000001','personal',decode(repeat('11',16),'hex'),decode(repeat('22',16),'hex'),TRUE); INSERT INTO invoice_requests(id,request_no,invoice_user_id,source_instance_id,profile_id,profile_snapshot_ciphertext,currency,amount_minor,status,idempotency_key,eligibility_policy_start_at,eligibility_policy_version) SELECT 'f6000000-0000-4000-8000-000000000001','RUNTIME-POLICY-LOCK','f2000000-0000-4000-8000-000000000001','f1000000-0000-4000-8000-000000000002','f4000000-0000-4000-8000-000000000001',decode(repeat('33',16),'hex'),'CNY',20000,'pending_review','runtime-policy-lock',eligibility_start_at,policy_version FROM invoice_eligibility_policy WHERE singleton_id=1" *> $null
        if ($LASTEXITCODE -ne 0) { throw 'failed to seed runtime request policy lock check' }
        docker exec -e PGPASSWORD=invoice_app_test_only $containerName psql -U invoice_app -d $databaseName -v ON_ERROR_STOP=1 -c "BEGIN; SELECT ir.eligibility_policy_start_at,ir.eligibility_policy_version,policy.eligibility_start_at,policy.policy_version FROM invoice_requests ir CROSS JOIN invoice_eligibility_policy policy WHERE ir.id='f6000000-0000-4000-8000-000000000001' FOR SHARE OF ir; ROLLBACK" *> $null
        if ($LASTEXITCODE -ne 0) { throw 'runtime role cannot execute request issue policy lock path' }
        docker exec -e PGPASSWORD=invoice_app_test_only $containerName psql -U invoice_app -d $databaseName -v ON_ERROR_STOP=1 -c "UPDATE audit_events SET action='tampered' WHERE id='00000000-0000-4000-8000-000000000099'" *> $null
        if ($LASTEXITCODE -eq 0) { throw 'runtime role can mutate immutable audit events' }
        docker exec -e PGPASSWORD=invoice_app_test_only $containerName psql -U invoice_app -d $databaseName -v ON_ERROR_STOP=1 -c "UPDATE oidc_backchannel_logout_events SET revoked_session_count=99 WHERE id='00000000-0000-4000-8000-000000000098'" *> $null
        if ($LASTEXITCODE -eq 0) { throw 'runtime role can mutate immutable OIDC logout replay records' }
        docker exec -e PGPASSWORD=invoice_app_test_only $containerName psql -U invoice_app -d $databaseName -v ON_ERROR_STOP=1 -c "DELETE FROM oidc_backchannel_logout_events WHERE id='00000000-0000-4000-8000-000000000098'" *> $null
        if ($LASTEXITCODE -eq 0) { throw 'runtime role can delete OIDC logout replay records' }
        docker exec -e PGPASSWORD=invoice_app_test_only $containerName psql -U invoice_app -d $databaseName -v ON_ERROR_STOP=1 -c "UPDATE invoice_eligibility_policy SET updated_by='tampered' WHERE singleton_id=1" *> $null
        if ($LASTEXITCODE -eq 0) { throw 'runtime role can mutate immutable invoice eligibility policy' }
    Invoke-PostgresContainerCommand `
        -DatabaseContainer $containerName `
        -RunnerImage $agentRunnerImage `
        -Environment @("SOURCE_AGENT_TEST_DATABASE_URL=$databaseUrl") `
        -ContractsPath $contractsPath `
        -Command 'umask 077; go test ./cmd/source-agent-prod -count=1' `
        -FailureMessage 'source bridge privilege integration tests failed against PostgreSQL 18'
    } finally {
        if ($containerStarted) {
            docker rm --force $containerName *> $null
            if ($LASTEXITCODE -ne 0) { throw "failed to remove PostgreSQL 18 test container: $containerName" }
        }
    }

    try {
    $source15RunImage = $Postgres15Image
    docker image inspect $Postgres15Image *> $null
    if ($LASTEXITCODE -ne 0) {
        docker image inspect $Postgres15FallbackImageID *> $null
        if ($LASTEXITCODE -eq 0) {
            $source15RunImage = $Postgres15FallbackImageID
        } else {
            docker pull $Postgres15Image *> $null
            if ($LASTEXITCODE -ne 0) { throw 'exact PostgreSQL 15 source-contract image is unavailable' }
        }
    }

    docker run --detach --rm `
        --name $source15Container `
        --env 'POSTGRES_DB=source_contract_test' `
        --env 'POSTGRES_USER=source_contract_test' `
        --env 'POSTGRES_PASSWORD=source_contract_test_only' `
        --tmpfs '/var/lib/postgresql:rw,nosuid,nodev,size=512m' `
        $source15RunImage | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'failed to start exact PostgreSQL 15 source-contract container' }
    $source15Started = $true

    $deadline = (Get-Date).AddSeconds(60)
    do {
        docker exec $source15Container pg_isready -U source_contract_test -d source_contract_test *> $null
        if ($LASTEXITCODE -eq 0) { break }
        Start-Sleep -Milliseconds 500
    } while ((Get-Date) -lt $deadline)
    if ($LASTEXITCODE -ne 0) { throw 'isolated PostgreSQL 15 did not become ready' }

    $source15DatabaseUrl = 'postgres://source_contract_test:source_contract_test_only@127.0.0.1:5432/source_contract_test?sslmode=disable'
    # Migration 0013 intentionally uses PostgreSQL catalogs as a fail-closed
    # structural contract. Exercise the exact same migration on the oldest
    # supported PostgreSQL family as well as the PostgreSQL 18 invoice test above.
    Invoke-PostgresContainerCommand `
        -DatabaseContainer $source15Container `
        -RunnerImage $backendRunnerImage `
        -Environment @("INVOICE_TEST_DATABASE_URL=$source15DatabaseUrl") `
        -Command 'go test ./internal/migrate -run ''^TestSourceReadinessActiveIndexMigrationCatalogContract$'' -count=1' `
        -FailureMessage 'PostgreSQL 15 source readiness index catalog contract failed'

    docker exec $source15Container psql -U source_contract_test -d source_contract_test -v ON_ERROR_STOP=1 -c "DROP SCHEMA public CASCADE; CREATE SCHEMA public" *> $null
    if ($LASTEXITCODE -ne 0) { throw 'failed to reset PostgreSQL 15 after readiness index catalog contract' }

    Invoke-PostgresContainerCommand `
        -DatabaseContainer $source15Container `
        -RunnerImage $agentRunnerImage `
        -Environment @("SOURCE_AGENT_TEST_DATABASE_URL=$source15DatabaseUrl") `
        -ContractsPath $contractsPath `
        -Command 'umask 077; go test ./cmd/source-agent-prod -run ''^Test(BridgeV4|Sub2APIBridge|NewAPIBridge)'' -count=1' `
        -FailureMessage 'PostgreSQL 15 source bridge/apply/rollback contracts failed'
    } finally {
        if ($source15Started) {
            docker rm --force $source15Container *> $null
            if ($LASTEXITCODE -ne 0) { throw "failed to remove PostgreSQL 15 test container: $source15Container" }
        }
    }
} finally {
    $runnerCleanupFailures = [Collections.Generic.List[string]]::new()
    foreach ($runnerImage in @($backendRunnerImage, $agentRunnerImage)) {
        docker image inspect $runnerImage *> $null
        if ($LASTEXITCODE -eq 0) {
            docker image rm --force $runnerImage *> $null
            if ($LASTEXITCODE -ne 0) { $runnerCleanupFailures.Add("image:$runnerImage") }
        }
    }
    if ($runnerCleanupFailures.Count -ne 0) {
        throw "PostgreSQL test-runner cleanup failed: $($runnerCleanupFailures -join ', ')"
    }
}

Write-Host "PostgreSQL migration/integration and PG15/PG18 source-contract verification passed in isolated containers."

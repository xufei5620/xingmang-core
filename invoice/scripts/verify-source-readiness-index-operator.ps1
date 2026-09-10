param([switch]$PlanFixturesOnly)
$ErrorActionPreference = 'Stop'

& python (Join-Path $PSScriptRoot 'tests/test_readiness_plan.py')
if ($LASTEXITCODE -ne 0) { throw 'readiness plan behavioral fixtures failed' }
if ($PlanFixturesOnly) { $global:LASTEXITCODE = 0; return }

$projectRoot = Split-Path -Parent $PSScriptRoot
$operatorPath = Join-Path $projectRoot 'deploy\postgres\apply-source-readiness-index-concurrently.sh'
$verifierPath = Join-Path $projectRoot 'deploy\postgres\verify-source-readiness-index.sh'
$runtimeRolePolicyPath = Join-Path $projectRoot 'deploy\postgres\harden-runtime-role.sql'
$runbookPath = Join-Path $projectRoot 'docs\PRODUCTION-RUNBOOK.md'
$operator = Get-Content -Raw -LiteralPath $operatorPath
$verifier = Get-Content -Raw -LiteralPath $verifierPath
$runtimeRolePolicy = Get-Content -Raw -LiteralPath $runtimeRolePolicyPath
$runbook = Get-Content -Raw -LiteralPath $runbookPath
$runbookNormalized = [regex]::Replace($runbook, '\s+', ' ')

$exactCreate = "CREATE INDEX CONCURRENTLY source_ingest_events_readiness_active_idx ON public.source_ingest_events USING btree (source_instance_id, stream_id, processing_status, created_at) WHERE processing_status IN ('queued','failed','processing','dead')"
if ([regex]::Matches($operator, [regex]::Escape($exactCreate)).Count -ne 1) {
    throw 'operator must contain exactly one reviewed CREATE INDEX CONCURRENTLY definition'
}

foreach ($required in @(
    '[[ "$(id -u)" == ''0'' ]]',
    'SOURCE_READINESS_INDEX_CONFIRMED',
    'EXPECTED_OPERATOR_SHA256',
    'EXPECTED_VERIFIER_SHA256',
    "readonly LOCK_PARENT='/run/lock'",
    'lock_parent_mode_is_safe',
    '(8#$mode & 8#002) == 0',
    '(8#$mode & 8#1000) != 0',
    '/run/lock must not be world-writable unless the sticky bit is set',
    'solov-invoice-source-readiness-index',
    'dedicated lock directory must be root:root mode 0700',
    'lock file must be a regular non-symlink file',
    'lock file must be root:root mode 0600',
    'LOCK_PATH_DEVICE_INODE',
    'LOCK_FD_DEVICE_INODE',
    'opened lock descriptor does not match the reviewed lock path',
    'public.source_ingest_events',
    "c.relkind='r'",
    "c.relpersistence='p'",
    "am.amname='btree'",
    'i.indnatts=4',
    'i.indnkeyatts=4',
    'i.indexprs IS NULL',
    "pg_catalog.pg_get_indexdef(idx.oid,1,true)='source_instance_id'",
    "pg_catalog.pg_get_indexdef(idx.oid,2,true)='stream_id'",
    "pg_catalog.pg_get_indexdef(idx.oid,3,true)='processing_status'",
    "pg_catalog.pg_get_indexdef(idx.oid,4,true)='created_at'",
    'i.indisvalid',
    'i.indisready',
    'i.indislive',
    "INDEX_ACTION='create-concurrently'",
    "INDEX_ACTION='already-exact'",
    'operator will not drop or repair it automatically',
    "statement_timeout='20min'",
    'ANALYZE public.source_ingest_events',
    'schema-migrations-before.tsv',
    'schema-migrations-after.tsv',
    "name='0013_source_readiness_active_index.sql'",
    'post-create-readonly-verification',
    'api-before.env',
    'api-after.env',
    'RC39-SOURCE-READINESS-INDEX.sha256',
    'sub2api_newapi_touched=false'
)) {
    if (-not $operator.Contains($required, [StringComparison]::Ordinal)) {
        throw "operator contract is missing: $required"
    }
}

if ($operator.Contains("statement_timeout='20m'", [StringComparison]::Ordinal)) {
    throw 'operator uses PostgreSQL-invalid minute abbreviation 20m instead of 20min'
}

if ($operator.Contains("die '/run/lock must not be world-writable'", [StringComparison]::Ordinal)) {
    throw 'operator still rejects the standard sticky /run/lock mode'
}
$modeFunction = [regex]::Match($operator, '(?ms)^lock_parent_mode_is_safe\(\) \{.*?^\}').Value
if ([string]::IsNullOrWhiteSpace($modeFunction)) {
    throw 'cannot extract lock parent mode predicate for dynamic tests'
}
$modeFixtureName = '.tmp-lock-parent-mode-' + [Guid]::NewGuid().ToString('N') + '.sh'
$modeFixturePath = Join-Path $projectRoot $modeFixtureName
try {
    [IO.File]::WriteAllText($modeFixturePath,
        $modeFunction + "`nlock_parent_mode_is_safe `"`$1`"`n",
        [Text.UTF8Encoding]::new($false))
    Push-Location $projectRoot
    try {
        foreach ($mode in @('755','775','1777')) {
            & bash $modeFixtureName $mode 2>$null
            if ($LASTEXITCODE -ne 0) { throw "safe root-owned lock parent mode was rejected: $mode" }
        }
        foreach ($mode in @('0777','0002')) {
            & bash $modeFixtureName $mode 2>$null
            if ($LASTEXITCODE -eq 0) { throw "unsafe non-sticky world-writable lock parent mode was accepted: $mode" }
        }
    } finally {
        Pop-Location
    }
} finally {
    Remove-Item -LiteralPath $modeFixturePath -Force -ErrorAction SilentlyContinue
}

foreach ($required in @(
    '[[ "$(id -u)" == ''0'' ]]',
    'default_transaction_read_only=on',
    'EXPLAIN (COSTS OFF, VERBOSE, SETTINGS, FORMAT TEXT)',
    'EXPLAIN (ANALYZE, BUFFERS, COSTS OFF, VERBOSE, SETTINGS, SUMMARY, FORMAT TEXT)',
    "statement_timeout=2500ms",
    "timeout 10s",
    'READINESS_EXECUTION_TIME_MS',
    'milliseconds <= 2000.0',
    'readiness query execution exceeded the 2000ms hard limit',
    'postgres_execution_time_ms=',
    'source_ingest_events sequential scan',
    'source_ingest_events_readiness_active_idx',
    'schema_migrations changed during read-only verification',
    'RC39-SOURCE-READINESS-INDEX-VERIFY.sha256',
    'database_read_only=true',
    'old_api_liveness=passed'
)) {
    if (-not $verifier.Contains($required, [StringComparison]::Ordinal)) {
        throw "read-only verifier contract is missing: $required"
    }
}

$combined = $operator + "`n" + $verifier
foreach ($forbidden in @(
    'DROP INDEX',
    'REINDEX',
    'CREATE INDEX CONCURRENTLY IF NOT EXISTS',
    'INSERT INTO public.schema_migrations',
    'UPDATE public.schema_migrations',
    'DELETE FROM public.schema_migrations',
    'TRUNCATE public.schema_migrations',
    'docker restart',
    'docker compose restart',
    'docker compose stop',
    'docker compose up',
    'sub2api-payments',
    'newapi-payments'
)) {
    if ($combined.Contains($forbidden, [StringComparison]::OrdinalIgnoreCase)) {
        throw "operator/verifier contains a forbidden production action: $forbidden"
    }
}

if ($operator.Contains('exec 9>"$LOCK_FILE"', [StringComparison]::Ordinal) -or
    -not $operator.Contains('exec 9<>"$LOCK_FILE"', [StringComparison]::Ordinal)) {
    throw 'operator lock descriptor can follow an unverified pre-existing path'
}
$lockDirectoryCheckOffset = $operator.IndexOf('dedicated lock directory must be root:root mode 0700', [StringComparison]::Ordinal)
$lockParentModeOffset = $operator.IndexOf('lock_parent_mode_is_safe "$lock_parent_mode"', [StringComparison]::Ordinal)
$lockDirectoryCreateOffset = $operator.IndexOf('mkdir -m 0700 -- "$LOCK_DIRECTORY"', [StringComparison]::Ordinal)
$lockFileCheckOffset = $operator.IndexOf('lock file must be a regular non-symlink file', [StringComparison]::Ordinal)
$lockOpenOffset = $operator.IndexOf('exec 9<>"$LOCK_FILE"', [StringComparison]::Ordinal)
$lockDescriptorCheckOffset = $operator.IndexOf('opened lock descriptor does not match the reviewed lock path', [StringComparison]::Ordinal)
$flockOffset = $operator.IndexOf("flock -n 9 || die 'another source readiness index operation is running'", [StringComparison]::Ordinal)
$lockPostCheckOffset = $operator.IndexOf("die 'lock path inode changed after flock'", [StringComparison]::Ordinal)
if ($lockParentModeOffset -lt 0 -or $lockDirectoryCreateOffset -lt 0 -or
    $lockDirectoryCheckOffset -lt 0 -or $lockFileCheckOffset -lt 0 -or $lockOpenOffset -lt 0 -or
    $lockDescriptorCheckOffset -lt 0 -or $flockOffset -lt 0 -or $lockPostCheckOffset -lt 0 -or
    $lockParentModeOffset -ge $lockDirectoryCreateOffset -or
    $lockDirectoryCreateOffset -ge $lockDirectoryCheckOffset -or
    $lockDirectoryCheckOffset -ge $lockFileCheckOffset -or $lockFileCheckOffset -ge $lockOpenOffset -or
    $lockOpenOffset -ge $lockDescriptorCheckOffset -or $lockDescriptorCheckOffset -ge $flockOffset -or
    $flockOffset -ge $lockPostCheckOffset) {
    throw 'lock directory/file/open-descriptor/flock/post-check ordering drifted'
}

$explainAnalyzeOffset = $verifier.IndexOf('EXPLAIN (ANALYZE, BUFFERS, COSTS OFF, VERBOSE, SETTINGS, SUMMARY, FORMAT TEXT)', [StringComparison]::Ordinal)
$executionTimeParseOffset = $verifier.IndexOf('$1=="Execution" && $2=="Time:"', [StringComparison]::Ordinal)
$executionTimeLimitOffset = $verifier.IndexOf('milliseconds <= 2000.0', [StringComparison]::Ordinal)
$executionTimeEvidenceOffset = $verifier.IndexOf('postgres_execution_time_ms=%s', [StringComparison]::Ordinal)
if ($explainAnalyzeOffset -lt 0 -or $executionTimeParseOffset -lt 0 -or $executionTimeLimitOffset -lt 0 -or
    $executionTimeEvidenceOffset -lt 0 -or $explainAnalyzeOffset -ge $executionTimeParseOffset -or
    $executionTimeParseOffset -ge $executionTimeLimitOffset -or $executionTimeLimitOffset -ge $executionTimeEvidenceOffset) {
    throw 'EXPLAIN ANALYZE execution-time parse/limit/evidence ordering drifted'
}

if ($operator -match '(?im)^\s*(?:BEGIN|START\s+TRANSACTION)\b') {
    throw 'CREATE INDEX CONCURRENTLY operator must not open an explicit transaction'
}
if ($verifier -match '(?im)^\s*(?:CREATE|ALTER|DROP|REINDEX|VACUUM|ANALYZE|INSERT|UPDATE|DELETE|TRUNCATE|GRANT|REVOKE)\b') {
    throw 'read-only verifier contains a database mutation statement'
}

foreach ($required in @(
    'Stage 0: backlog freeze, recoverability and capacity hard gate',
    '`sub2api-balances` and `newapi-balances` remain stopped',
    'at least five minutes apart',
    'Freeze account linking for the whole window',
    'do not invoke any dependency batch wakeup',
    '`external_accounts`, `external_account_binding_proofs`',
    '`BACKUP_SCHEMA_MODE=post-0011`',
    '`BACKUP_LOCAL_KEYCLOAK=true`',
    '`schema_migrations` set must end at 0012',
    '`0013_source_readiness_active_index.sql`',
    '`0014_balance_carry_forward_proof.sql`',
    'both new carry-forward tables must be absent in the restored copy',
    '`deploy/backup/restore-drill.sh` into an isolated',
    'off-host copy/ACK must be no older than two hours',
    '`pg_database_size(current_database())`',
    '`pg_total_relation_size`',
    '`pg_ls_waldir()`',
    '`pg_stat_archiver`',
    '`pg_replication_slots`',
    '`pg_stat_replication`',
    'the greater of 20 GiB',
    '`/tmp` and `RECORD_ROOT` must each have at least 5 GiB free',
    'at least 10 percent free inodes',
    'replay lag over 60 seconds',
    'WAL lag over 512 MiB',
    'owner-applied runtime-role hardening and privilege proof',
    'schema-migrations-expected.tsv',
    'schema-migrations-after-permissions.tsv',
    '<deploy/postgres/harden-runtime-role.sql',
    "balance_carry_forward_evaluations|t|t|f|f|f|f|f",
    "balance_carry_forward_proofs|t|t|f|f|f|f|f",
    "balance_carry_forward_evaluations|INSERT,SELECT",
    "balance_carry_forward_proofs|INSERT,SELECT",
    "public.schema_migrations','TRUNCATE'",
    "grep -Fx 't|f|f|f|f|f|f'",
    'Require exactly two lines in each carry-forward privilege evidence file',
    'prior invoice API image is operationally forbidden'
)) {
    if (-not $runbookNormalized.Contains($required, [StringComparison]::Ordinal)) {
        throw "RC39 production Stage 0/runbook contract is missing: $required"
    }
}
$stageZeroOffset = $runbook.IndexOf('Stage 0: backlog freeze, recoverability and capacity hard gate', [StringComparison]::Ordinal)
$immutableOrderOffset = $runbook.IndexOf('The order is immutable: concurrent index operator', [StringComparison]::Ordinal)
$operatorRunOffset = $runbook.IndexOf('deploy/postgres/apply-source-readiness-index-concurrently.sh', $immutableOrderOffset, [StringComparison]::Ordinal)
$evidenceSignOffset = $runbook.IndexOf('ssh-keygen -Y sign', $operatorRunOffset, [StringComparison]::Ordinal)
$migrationRunOffset = $runbook.IndexOf('run --rm --pull never migrate', $evidenceSignOffset, [StringComparison]::Ordinal)
$migrationSetProofOffset = $runbook.IndexOf('schema-migrations-expected.tsv', $migrationRunOffset, [StringComparison]::Ordinal)
$runtimeRoleReplayOffset = $runbook.IndexOf('<deploy/postgres/harden-runtime-role.sql', $migrationSetProofOffset, [StringComparison]::Ordinal)
$carryForwardPrivilegeOffset = $runbook.IndexOf('carry-forward-effective-privileges.tsv', $runtimeRoleReplayOffset, [StringComparison]::Ordinal)
$migrationAfterPermissionsOffset = $runbook.IndexOf('schema-migrations-after-permissions.tsv', $carryForwardPrivilegeOffset, [StringComparison]::Ordinal)
$apiRollForwardOffset = $runbook.IndexOf('up -d --no-build --no-deps api', $migrationAfterPermissionsOffset, [StringComparison]::Ordinal)
if ($stageZeroOffset -lt 0 -or $immutableOrderOffset -lt 0 -or $operatorRunOffset -lt 0 -or
    $evidenceSignOffset -lt 0 -or $migrationRunOffset -lt 0 -or $migrationSetProofOffset -lt 0 -or
    $runtimeRoleReplayOffset -lt 0 -or $carryForwardPrivilegeOffset -lt 0 -or
    $migrationAfterPermissionsOffset -lt 0 -or $apiRollForwardOffset -lt 0 -or
    $stageZeroOffset -ge $immutableOrderOffset -or $immutableOrderOffset -ge $operatorRunOffset -or
    $operatorRunOffset -ge $evidenceSignOffset -or $evidenceSignOffset -ge $migrationRunOffset -or
    $migrationRunOffset -ge $migrationSetProofOffset -or $migrationSetProofOffset -ge $runtimeRoleReplayOffset -or
    $runtimeRoleReplayOffset -ge $carryForwardPrivilegeOffset -or
    $carryForwardPrivilegeOffset -ge $migrationAfterPermissionsOffset -or
    $migrationAfterPermissionsOffset -ge $apiRollForwardOffset) {
    throw 'RC39 Stage0/operator/signature/0013+0014/role-hardening/new-API roll-forward order drifted'
}

foreach ($table in @('balance_carry_forward_proofs','balance_carry_forward_evaluations')) {
    $escapedTable = [regex]::Escape($table)
    if ($runtimeRolePolicy -notmatch "(?s)REVOKE UPDATE, DELETE, TRUNCATE ON TABLE.*?$escapedTable.*?FROM invoice_app;" -or
        $runtimeRolePolicy -notmatch "(?s)GRANT SELECT, INSERT ON TABLE.*?$escapedTable.*?TO invoice_app;") {
        throw "runtime role policy does not reduce $table to SELECT/INSERT"
    }
}

$preflightOffset = $operator.IndexOf('INDEX_STATE_BEFORE="$(index_contract_state)"', [StringComparison]::Ordinal)
$createOffset = $operator.IndexOf('printf ''%s;\n'' "$CREATE_INDEX_SQL"', [StringComparison]::Ordinal)
$verifyOffset = $operator.IndexOf('INDEX_STATE_AFTER="$(index_contract_state)"', [StringComparison]::Ordinal)
$analyzeOffset = $operator.IndexOf('ANALYZE public.source_ingest_events', [StringComparison]::Ordinal)
$readonlyVerifierOffset = $operator.IndexOf('VERIFICATION_RECORD_DIR="$RECORD_DIR/post-create-readonly-verification"', [StringComparison]::Ordinal)
$migrationAfterOffset = $operator.IndexOf('capture_schema_migrations "$RECORD_DIR/schema-migrations-after.tsv"', [StringComparison]::Ordinal)
if ($preflightOffset -lt 0 -or $createOffset -lt 0 -or $verifyOffset -lt 0 -or
    $preflightOffset -ge $createOffset -or $createOffset -ge $verifyOffset -or
    $verifyOffset -ge $analyzeOffset -or $analyzeOffset -ge $readonlyVerifierOffset -or
    $readonlyVerifierOffset -ge $migrationAfterOffset) {
    throw 'operator fail-closed preflight/create/catalog/analyze/verifier/migration-check order drifted'
}

foreach ($path in @($operatorPath, $verifierPath)) {
    $bytes = [IO.File]::ReadAllBytes($path)
    if ($bytes -contains 13) { throw "$path contains CRLF bytes" }
}

$bash = Get-Command bash -ErrorAction Stop
Push-Location $projectRoot
try {
    foreach ($shellScript in @('deploy/postgres/apply-source-readiness-index-concurrently.sh', 'deploy/postgres/verify-source-readiness-index.sh')) {
        & $bash.Source -n $shellScript
        if ($LASTEXITCODE -ne 0) { throw "RC39 readiness index shell syntax failed: $shellScript" }
    }
} finally {
    Pop-Location
}

Write-Host 'RC39 concurrent index operator and read-only verifier static contracts passed.'
$global:LASTEXITCODE = 0

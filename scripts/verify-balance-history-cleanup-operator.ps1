$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot
$operator = Get-Content -Raw (Join-Path $projectRoot 'deploy\postgres\apply-balance-history-cleanup.sh')
$sql = Get-Content -Raw (Join-Path $projectRoot 'deploy\postgres\balance-history-cleanup.sql')
$rehearsal = Get-Content -Raw (Join-Path $projectRoot 'deploy\postgres\rehearse-balance-history-cleanup.sh')
$plan = Get-Content -Raw (Join-Path $projectRoot 'deploy\postgres\plan-balance-history-cleanup.sh')
$restore = Get-Content -Raw (Join-Path $projectRoot 'deploy\backup\restore-drill.sh')
$runbook = Get-Content -Raw (Join-Path $projectRoot 'docs\PRODUCTION-RUNBOOK.md')

foreach ($required in @(
    "event.stream_id='balances'",
    "event.entity_type='balance_checkpoint'",
    "event.operation='upsert'",
    "event.processing_status='parked_identity'",
    "event.dependency_kind='source_external_account'",
    'event.dependency_key_hmac IS NOT NULL',
    'mapped.payload_hash=event.payload_hash',
    "cycle.cycle_status='published'",
    "batch.schema_version='3.0'",
    "cycle.scan_ceiling_at<'2026-09-01T00:00:00+08:00'::timestamptz",
    'PARTITION BY source_instance_id,dependency_key_hmac',
    'ORDER BY scan_ceiling_at,batch_sequence,created_at,event_id',
    'ORDER BY scan_ceiling_at DESC,batch_sequence DESC,created_at DESC,event_id DESC',
    'first_rank=1 OR last_rank=1',
    'target_count<>1879297', 'group_count<>2747', 'keep_count<>5494', 'purge_count<>1873803',
    'minimum_group_count<>79', 'maximum_group_count<>771',
    'cutover_anchor_count<>2738', 'post_cutover_anchor_count<>9',
    'baseline_time_mismatch_count<>0', 'unexpected_first_count<>0',
    'latest_actual_anchor_count<>2747', 'unexpected_last_count<>0',
    "SELECT source_instance_id::text||'|'||event_id::text",
    'FROM cleanup_keep ORDER BY source_instance_id,event_id',
    'FROM cleanup_purge ORDER BY source_instance_id,event_id',
    'BEGIN ISOLATION LEVEL SERIALIZABLE',
    'SET LOCAL synchronous_commit=on',
    "SET LOCAL transaction_timeout='2h'",
    'source_economic_scan_cycle_events_event_fk_idx',
    'DELETE FROM public.source_economic_scan_cycle_events',
    'DELETE FROM public.source_ingest_events',
    'mapping_deleted<>1873803', 'event_deleted<>1873803',
    'did not retain exactly two rows per group',
    'changed a guarded non-target table',
    'FK/delete-trigger contract mismatch'
)) {
    if (-not $sql.Contains($required, [StringComparison]::Ordinal)) {
        throw "cleanup SQL contract missing: $required"
    }
}
$mappingDelete = $sql.IndexOf('DELETE FROM public.source_economic_scan_cycle_events', [StringComparison]::Ordinal)
$eventDelete = $sql.IndexOf('DELETE FROM public.source_ingest_events', [StringComparison]::Ordinal)
if ($mappingDelete -lt 0 -or $eventDelete -lt 0 -or $mappingDelete -ge $eventDelete) {
    throw 'cleanup must delete child mappings before parent events'
}
foreach ($marker in @('\o :keep_rows_path','\o :purge_rows_path')) {
    $start = $sql.IndexOf($marker, [StringComparison]::Ordinal)
    $end = $sql.IndexOf('\o', $start + $marker.Length, [StringComparison]::Ordinal)
    if ($start -lt 0 -or $end -lt 0) { throw "cleanup digest block missing: $marker" }
    $digestBlock = $sql.Substring($start, $end - $start)
    if ($digestBlock.Contains('dependency_key_hmac', [StringComparison]::Ordinal) -or
        $digestBlock.Contains('payload_hash', [StringComparison]::Ordinal)) {
        throw 'cleanup digest output can expose HMAC or payload metadata'
    }
}

foreach ($required in @(
    '-v apply_cleanup=false', 'balance-history-cleanup.sql',
    'default_transaction_read_only=on',
    'plan requires every source agent stopped', 'no other invoice database sessions',
    'T=1879297 G=2747 K=5494 D=1873803',
    'min_group=79|max_group=771',
    'cutover_anchors=2738|post_cutover_anchors=9|baseline_time_mismatch=0',
    'latest_actual_anchors=2747|unexpected_last=0',
    'BALANCE-HISTORY-PLAN.sha256', 'keep.sha256', 'purge.sha256'
)) {
    if (-not $plan.Contains($required, [StringComparison]::Ordinal)) {
        throw "cleanup read-only plan contract missing: $required"
    }
}
if ($plan.Contains('CREATE INDEX', [StringComparison]::OrdinalIgnoreCase) -or
    $plan.Contains('-v apply_cleanup=true', [StringComparison]::Ordinal)) {
    throw 'cleanup plan contains a persistent database mutation path'
}
if ($sql.Contains('/tmp/solov-balance-history-cleanup-', [StringComparison]::Ordinal)) {
    throw 'shared cleanup SQL chooses a fixed scratch path'
}

foreach ($required in @(
    'BALANCE_HISTORY_CLEANUP_CONFIRMED', 'WRITE_FREEZE_CONFIRMED',
    'EXPECTED_OPERATOR_SHA256', 'EXPECTED_SQL_SHA256', 'EXPECTED_SCHEMA_MIGRATIONS_SHA256',
    'EXPECTED_KEEP_SHA256', 'EXPECTED_PURGE_SHA256',
    'BACKUP_MANIFEST', 'RESTORE_REHEARSAL_MANIFEST', 'OFFSITE_ACK_FILE',
    'write freeze requires postgres to be the only running production service',
    'all source agents must be stopped', 'unexpected invoice database sessions',
    "'0|0|0|0|0|0|0|0'", '1903871', '1879297', '30068', '5494',
    'CREATE INDEX CONCURRENTLY', 'i.indisvalid', 'i.indisready', 'i.indislive',
    'timeout --foreground --kill-after=30s 7500s',
    'mktemp -d /tmp/solov-balance-history-cleanup.XXXXXXXX',
    "stat -c '%u:%g:%a'", "stat -Lc '%d:%i'",
    'pg_wal_lsn_diff', 'required_pgdata_bytes', 'pg_ls_waldir()',
    "VACUUM (ANALYZE) public.source_economic_scan_cycle_events",
    "VACUUM (ANALYZE) public.source_ingest_events",
    'BALANCE-HISTORY-CLEANUP.sha256', 'schema_migrations_written_by_operator=false'
)) {
    if (-not $operator.Contains($required, [StringComparison]::Ordinal)) {
        throw "cleanup operator contract missing: $required"
    }
}
if ($operator.Contains('timeout 7500s psql_owner', [StringComparison]::Ordinal) -or
    $operator -match "SET statement_timeout='2h'.*VACUUM") {
    throw 'cleanup operator retains an invalid function timeout or transactional VACUUM path'
}

foreach ($required in @(
    'EXPECTED_BALANCE_HISTORY_SQL_SHA256', 'EXPECTED_BALANCE_HISTORY_REHEARSAL_SHA256',
    "name='0014_balance_carry_forward_proof.sql'", 'CREATE INDEX CONCURRENTLY',
    'timeout --foreground --kill-after=30s 7500s', 'pg_wal_lsn_diff',
    'BALANCE-HISTORY-REHEARSAL.sha256', 'backup_manifest_sha256=', 'backup_signature_sha256='
)) {
    if (-not $rehearsal.Contains($required, [StringComparison]::Ordinal)) {
        throw "cleanup rehearsal contract missing: $required"
    }
}
foreach ($required in @(
    'RESTORE_BALANCE_HISTORY_CLEANUP_REHEARSAL',
    'balance history cleanup rehearsal requires RESTORE_POSTGRES_TMPFS_SIZE=16g',
    'BALANCE_HISTORY_REHEARSAL_RECORD_ROOT',
    'rehearse-balance-history-cleanup.sh'
)) {
    if (-not $restore.Contains($required, [StringComparison]::Ordinal)) {
        throw "restore drill cleanup rehearsal contract missing: $required"
    }
}
if ($restore.Contains('RESTORE_POSTGRES_HOOK', [StringComparison]::OrdinalIgnoreCase) -or
    $restore.Contains('eval "$RESTORE', [StringComparison]::OrdinalIgnoreCase)) {
    throw 'restore drill accepts an arbitrary restore hook'
}
foreach ($required in @(
    'Balance-history cleanup maintenance exception',
    'T=1879297', 'G=2747', 'K=5494', 'D=1873803',
    'RESTORE_BALANCE_HISTORY_CLEANUP_REHEARSAL=YES',
    '2026-09-01T00:00:00+08:00',
    'first technical anchor', 'latest pre-policy actual anchor',
    '2738 rows match both the manifest baseline',
    '9 are the earliest post-cutover'
)) {
    if (-not $runbook.Contains($required, [StringComparison]::Ordinal)) {
        throw "cleanup runbook contract missing: $required"
    }
}

Push-Location $projectRoot
try {
    bash -n deploy/postgres/apply-balance-history-cleanup.sh `
        deploy/postgres/plan-balance-history-cleanup.sh `
        deploy/postgres/rehearse-balance-history-cleanup.sh `
        deploy/backup/restore-drill.sh
    if ($LASTEXITCODE -ne 0) { throw 'cleanup operator/rehearsal shell syntax failed' }
} finally {
    Pop-Location
}
$global:LASTEXITCODE = 0
Write-Host 'Balance history cleanup operator/rehearsal contracts passed.'

$ErrorActionPreference = 'Stop'

$projectRoot = Split-Path -Parent $PSScriptRoot
$operatorPath = Join-Path $projectRoot 'deploy\keycloak\invite-permanent-master-admin.sh'
$wrapperPath = Join-Path $projectRoot 'deploy\keycloak\run-permanent-master-admin-maintenance.sh'
$stateTestPath = Join-Path $projectRoot 'scripts\test-keycloak-permanent-master-admin-state.ps1'
$operator = Get-Content -Raw -LiteralPath $operatorPath
$wrapper = Get-Content -Raw -LiteralPath $wrapperPath

$required = @(
    "readonly EXPECTED_SOURCE_TAG='v0.1.0-rc38-signed'",
    'RELEASE-TREE.sha256', 'RELEASE-TREE.sha256.sig', 'solov-invoice-release-v1',
    'SOURCE_COMMIT', 'SOURCE_TAG', 'KEYCLOAK_IMAGE', 'SMTP_TRANSPORT',
    'source/deploy/keycloak/run-permanent-master-admin-maintenance.sh',
    'sha256sum -c RELEASE-TREE.sha256',
    'installed release-tree signature verification failed',
    'fixed Keycloak backup root must be root-owned mode 0700',
    'findmnt -n -T "$BACKUP_ROOT" -o FSTYPE', 'tmpfs|ramfs|overlay',
    'age identity and backup signing key must not be stored under the fixed backup root',
    'check_root_config_file "$AGE_RECIPIENT_FILE" ''age recipients file''',
    'check_root_config_file "$BACKUP_ALLOWED_SIGNERS_FILE" ''backup allowed_signers file''',
    '/run/lock/solov-keycloak-permanent-master-admin.lock',
    'openssl rand -hex 32', 'solov.permanent-master-admin.operation-nonce',
    'KEYCLOAK_ADMIN_WRITE_FREEZE_CONFIRMED',
    'allow 127.0.0.1;', 'allow ::1;', 'deny all;',
    '--network none', 'sync_persistent_evidence',
    'roles/invoice-admin/users?first=0&max=2',
    '.port == "465" and .ssl == "true" and .starttls == "false"',
    'source realm SMTP host differs from signed release binding',
    'KC_TLS_HOSTNAME_VERIFIER=DEFAULT', 'KC_TRUSTSTORE_KUBERNETES_ENABLED=false',
    'canonicalize_master_realm', 'verify_master_state_soft',
    '.adminEventsDetailsEnabled == false',
    'master-pre-temporary-put', 'master-post-temporary-put', 'master-pre-restore',
    'jq ''.smtpServer={}'' "$current_realm"',
    'USER_MUTATION_ATTEMPTED=true', 'SMTP_CHANGED=true',
    'execute-actions-email?lifespan=$ACTION_LIFESPAN',
    'bootstrap_retirement=still_blocked_pending_browser_enrollment_and_fresh_admin_canary'
)
foreach ($item in $required) {
    if (-not $operator.Contains($item, [StringComparison]::Ordinal)) { throw "operator contract is missing: $item" }
}
foreach ($stale in @('EXPECTED_SOURCE_COMMIT','EXPECTED_OPERATOR_SHA256','KEYCLOAK_BACKUP_DIR:?set','STARTTLS or implicit TLS','source-user-id')) {
    if ($operator.Contains($stale, [StringComparison]::Ordinal)) { throw "stale operator contract remains: $stale" }
}
if ($operator -match 'admin_request(?:_soft)? PUT ''/admin/realms/master'' "\$TMP_DIR/master-realm-original' -or
    $operator -match 'check_private_file "\$KEYCLOAK_BOOTSTRAP_PASSWORD_FILE"') {
    throw 'operator can replay a stale realm baseline or rejects the production bootstrap owner contract'
}

$createArm = $operator.IndexOf('USER_MUTATION_ATTEMPTED=true', [StringComparison]::Ordinal)
$createCall = $operator.IndexOf('admin_request POST ''/admin/realms/master/users''', [StringComparison]::Ordinal)
$smtpArm = $operator.IndexOf('SMTP_CHANGED=true', [StringComparison]::Ordinal)
$smtpCall = $operator.IndexOf('admin_request PUT ''/admin/realms/master'' "$TMP_DIR/master-realm-temporary.json"', [StringComparison]::Ordinal)
$backupSync = $operator.IndexOf('signed pre-invitation backup did not reach persistent storage', [StringComparison]::Ordinal)
$token = $operator.IndexOf('refresh_admin_header_soft || die ''bootstrap token acquisition failed''', [StringComparison]::Ordinal)
if ($createArm -lt 0 -or $smtpArm -lt 0 -or $createArm -ge $createCall -or $smtpArm -ge $smtpCall -or $backupSync -ge $token) {
    throw 'mutation rollback arming or persistent-backup-before-token ordering drifted'
}

# Parse complete persistent printf blocks, not merely their redirection lines.
$lines = $operator -split "`r?`n"
$persistentFlow = [Collections.Generic.List[string]]::new()
for ($index = 0; $index -lt $lines.Count; $index++) {
    if ($lines[$index] -match '^printf ''%s\\n'' \\$') {
        for ($cursor = $index; $cursor -lt $lines.Count; $cursor++) {
            $persistentFlow.Add($lines[$cursor])
            if ($lines[$cursor] -match '>"\$RECORD_DIR/') { break }
        }
    }
}
if ($persistentFlow.Count -eq 0) { throw 'persistent result dataflow was not found' }
$persistentText = $persistentFlow -join "`n"
foreach ($forbidden in @('$TMP_DIR','$HTTP_','$AUTH_','$USER_ID','$OPERATION_NONCE','$PASSWORD','$TOKEN','$TARGET','smtp_from=','smtp_host=','sourceUserId','email_address=')) {
    if ($persistentText.Contains($forbidden, [StringComparison]::OrdinalIgnoreCase)) { throw "sensitive data can flow into persistent evidence: $forbidden" }
}
$dynamicVariables = [regex]::Matches($persistentText, '\$[A-Z][A-Z0-9_]*') | ForEach-Object Value | Sort-Object -Unique
foreach ($variable in $dynamicVariables) {
    if ($variable -notin @('$SOURCE_COMMIT','$SOURCE_TAG','$ACTUAL_OPERATOR_SHA256','$RECORD_DIR')) { throw "unapproved persistent evidence variable: $variable" }
}

$wrapperRequired = @(
    '/run/lock/solov-keycloak-master-admin-maintenance.lock',
    'original-allowlist.conf.age', 'ALLOWLIST-ORIGINAL.sha256',
    'trap cleanup EXIT', 'nginx_test_reload',
    'allow 127.0.0.1;', 'allow ::1;', 'deny all;',
    'KEYCLOAK_ADMIN_WRITE_FREEZE_CONFIRMED=YES',
    'cmp -s -- "$ALLOWLIST" "$ORIGINAL_FROM_RECORD"',
    'maintenance_gate;stage=preflight-approved-source',
    'maintenance_gate;stage=frozen-approved-source',
    'maintenance_gate;stage=restored-approved-source'
)
foreach ($item in $wrapperRequired) {
    if (-not $wrapper.Contains($item, [StringComparison]::Ordinal)) { throw "maintenance wrapper contract is missing: $item" }
}

$bash = Get-Command bash -ErrorAction Stop
Push-Location $projectRoot
try {
    & $bash.Source -n 'deploy/keycloak/invite-permanent-master-admin.sh' 'deploy/keycloak/run-permanent-master-admin-maintenance.sh'
    if ($LASTEXITCODE -ne 0) { throw 'Keycloak permanent administrator shell syntax failed' }
    & $bash.Source 'deploy/keycloak/verify-permanent-master-admin-maintenance.sh'
    if ($LASTEXITCODE -ne 0) { throw 'Keycloak maintenance wrapper dynamic negative fixtures failed' }
} finally { Pop-Location }

& $stateTestPath
if ($LASTEXITCODE -ne 0) { throw 'Keycloak permanent administrator negative state fixtures failed' }
& (Join-Path $PSScriptRoot 'test-keycloak-offsite-ack.ps1')
if ($LASTEXITCODE -ne 0) { throw 'Keycloak off-site ACK valid/tampered fixtures failed' }

Write-Host 'Permanent master administrator release binding, persistence, CAS, evidence and maintenance contracts passed.'
$global:LASTEXITCODE = 0

param([switch]$ConsumerOnly)
$ErrorActionPreference = 'Stop'
$consumerTest = Join-Path $PSScriptRoot 'tests/test_offsite_ack_content.py'
& python $consumerTest
if ($LASTEXITCODE -ne 0) { throw 'Keycloak off-site ACK consumer content fixtures failed' }
if ($ConsumerOnly) { $global:LASTEXITCODE = 0; return }
$projectRoot = Split-Path -Parent $PSScriptRoot
$helper = Join-Path $PSScriptRoot 'create-keycloak-offsite-ack.ps1'
$ssh = (Get-Command ssh-keygen -ErrorAction Stop).Source
$temp = Join-Path ([IO.Path]::GetTempPath()) ('keycloak-offsite-ack-' + [Guid]::NewGuid().ToString('N'))
$record = Join-Path $temp 'keycloak-master-admin-invite-20260825T120000Z'
[IO.Directory]::CreateDirectory($record) | Out-Null
try {
    $backupKey = Join-Path $temp 'backup-key'
    $offsiteKey = Join-Path $temp 'offsite-key'
    & $ssh -q -t ed25519 -N '' -f $backupKey
    & $ssh -q -t ed25519 -N '' -f $offsiteKey
    $backupPublic = (Get-Content -Raw "$backupKey.pub").Trim()
    $allowed = Join-Path $temp 'backup-allowed-signers'
    [IO.File]::WriteAllText($allowed,"invoice-backup namespaces=`"solov-invoice-backup-v1`" $backupPublic`n",[Text.UTF8Encoding]::new($false))
    [IO.File]::WriteAllText((Join-Path $record 'keycloak.postgres.dump.age'),'encrypted-fixture',[Text.UTF8Encoding]::new($false))
    [IO.File]::WriteAllText((Join-Path $record 'restore-proof.txt'),"restore_status=passed`n",[Text.UTF8Encoding]::new($false))
    $dumpHash = (Get-FileHash -Algorithm SHA256 (Join-Path $record 'keycloak.postgres.dump.age')).Hash.ToLowerInvariant()
    $proofHash = (Get-FileHash -Algorithm SHA256 (Join-Path $record 'restore-proof.txt')).Hash.ToLowerInvariant()
    $manifest = Join-Path $record 'KEYCLOAK-PREINVITE-BACKUP.sha256'
    [IO.File]::WriteAllText($manifest,"$dumpHash  keycloak.postgres.dump.age`n$proofHash  restore-proof.txt`n",[Text.UTF8Encoding]::new($false))
    & $ssh -Y sign -q -f $backupKey -n solov-invoice-backup-v1 $manifest 2>$null
    & $helper -RecordDirectory $record -BackupAllowedSignersFile $allowed -OffsiteSigningKeyFile $offsiteKey | Out-Null
    if (-not (Test-Path (Join-Path $record 'OFFSITE-ACK')) -or -not (Test-Path (Join-Path $record 'OFFSITE-ACK.sig'))) {
        throw 'valid off-site fixture did not produce the ACK pair'
    }
    [IO.File]::AppendAllText((Join-Path $record 'keycloak.postgres.dump.age'),'tamper',[Text.UTF8Encoding]::new($false))
    $failed = $false
    try { & $helper -RecordDirectory $record -BackupAllowedSignersFile $allowed -OffsiteSigningKeyFile $offsiteKey | Out-Null } catch { $failed = $true }
    if (-not $failed) { throw 'tampered off-site backup was accepted' }
} finally {
    Remove-Item -LiteralPath $temp -Recurse -Force -ErrorAction SilentlyContinue
}
Write-Host 'Keycloak off-site ACK valid/tampered fixtures passed.'
$global:LASTEXITCODE = 0

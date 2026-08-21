$ErrorActionPreference = 'Stop'

$projectRoot = Split-Path -Parent $PSScriptRoot
$backupScript = Get-Content -Raw (Join-Path $projectRoot 'deploy\backup\backup.sh')
$restoreScript = Get-Content -Raw (Join-Path $projectRoot 'deploy\backup\restore-drill.sh')
if ($backupScript -notmatch 'BACKUP_SIGNING_KEY_FILE:\?temporarily mount' -or
    $backupScript -notmatch 'ssh-keygen -Y sign' -or
    $restoreScript -notmatch 'BACKUP_SIGNATURE:\?set BACKUP_SIGNATURE' -or
    $restoreScript -notmatch 'ssh-keygen -Y verify') {
    throw 'backup scripts do not require the signing key/signature chain'
}
$verifyIndex = $restoreScript.IndexOf('ssh-keygen -Y verify', [StringComparison]::Ordinal)
$decryptIndex = $restoreScript.IndexOf('age --decrypt', [StringComparison]::Ordinal)
if ($verifyIndex -lt 0 -or $decryptIndex -lt 0 -or $verifyIndex -ge $decryptIndex) {
    throw 'restore does not verify the signed manifest before the first decryption'
}
$archiveVerifyIndex = $restoreScript.IndexOf('/usr/local/bin/invoice-archive-verify', [StringComparison]::Ordinal)
$archiveExtractIndex = $restoreScript.IndexOf('tar --no-same-owner', [StringComparison]::Ordinal)
if ($archiveVerifyIndex -lt 0 -or $archiveExtractIndex -lt 0 -or $archiveVerifyIndex -ge $archiveExtractIndex) {
    throw 'restore does not run the strict archive parser before extraction'
}

$sshKeygen = Get-Command ssh-keygen -ErrorAction Stop
$temporary = Join-Path ([IO.Path]::GetTempPath()) ("invoice-backup-signature-" + [Guid]::NewGuid().ToString('N'))
[IO.Directory]::CreateDirectory($temporary) | Out-Null
try {
    $key = Join-Path $temporary 'backup-signing-ed25519'
    & $sshKeygen.Source -q -t ed25519 -N '' -f $key
    if ($LASTEXITCODE -ne 0) { throw 'cannot generate the test Ed25519 key' }
    $public = (Get-Content -Raw "$key.pub").Trim()
    if (-not $public.StartsWith('ssh-ed25519 ', [StringComparison]::Ordinal)) {
        throw 'test key is not Ed25519'
    }
    $allowed = Join-Path $temporary 'allowed_signers'
    [IO.File]::WriteAllText($allowed, "invoice-backup namespaces=`"solov-invoice-backup-v1`" $public`n", [Text.UTF8Encoding]::new($false))
    $manifest = Join-Path $temporary 'invoice.sha256'
    [IO.File]::WriteAllText($manifest, (('a' * 64) + "  invoice.postgres.dump.age`n"), [Text.UTF8Encoding]::new($false))
    & $sshKeygen.Source -Y sign -f $key -n solov-invoice-backup-v1 $manifest 2>$null
    if ($LASTEXITCODE -ne 0 -or -not (Test-Path "$manifest.sig")) {
        throw 'OpenSSH manifest signing failed'
    }

    function Invoke-ManifestVerify([string]$Namespace) {
        $start = [Diagnostics.ProcessStartInfo]::new()
        $start.FileName = $sshKeygen.Source
        $start.UseShellExecute = $false
        $start.RedirectStandardInput = $true
        $start.RedirectStandardOutput = $true
        $start.RedirectStandardError = $true
        foreach ($argument in @('-Y','verify','-f',$allowed,'-I','invoice-backup','-n',$Namespace,'-s',"$manifest.sig")) {
            $start.ArgumentList.Add($argument)
        }
        $process = [Diagnostics.Process]::new()
        $process.StartInfo = $start
        $process.Start() | Out-Null
        $bytes = [IO.File]::ReadAllBytes($manifest)
        $process.StandardInput.BaseStream.Write($bytes, 0, $bytes.Length)
        $process.StandardInput.Close()
        $process.WaitForExit()
        return $process.ExitCode
    }

    if ((Invoke-ManifestVerify 'solov-invoice-backup-v1') -ne 0) {
        throw 'valid signed manifest was not accepted'
    }
    if ((Invoke-ManifestVerify 'wrong-namespace') -eq 0) {
        throw 'signature was accepted under the wrong namespace'
    }
    [IO.File]::AppendAllText($manifest, "tamper`n", [Text.UTF8Encoding]::new($false))
    if ((Invoke-ManifestVerify 'solov-invoice-backup-v1') -eq 0) {
        throw 'tampered manifest was accepted'
    }
} finally {
    Remove-Item -LiteralPath $temporary -Recurse -Force -ErrorAction SilentlyContinue
}

Write-Host 'Backup Ed25519 signature/tamper gate passed.'

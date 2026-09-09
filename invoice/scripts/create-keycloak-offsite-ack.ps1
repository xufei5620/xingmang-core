param(
    [Parameter(Mandatory)][string]$RecordDirectory,
    [Parameter(Mandatory)][string]$BackupAllowedSignersFile,
    [Parameter(Mandatory)][string]$OffsiteSigningKeyFile
)
$ErrorActionPreference = 'Stop'
$record = (Resolve-Path -LiteralPath $RecordDirectory).Path
$recordId = Split-Path -Leaf $record
if ($recordId -notmatch '^keycloak-master-admin-invite-[0-9]{8}T[0-9]{6}Z$') { throw 'unsafe backup record identifier' }
$manifest = Join-Path $record 'KEYCLOAK-PREINVITE-BACKUP.sha256'
$signature = "$manifest.sig"
foreach ($file in @($manifest,$signature,$BackupAllowedSignersFile,$OffsiteSigningKeyFile)) {
    if (-not (Test-Path -LiteralPath $file -PathType Leaf)) { throw 'required off-site verification input is missing' }
}

$sshKeygen = (Get-Command ssh-keygen -ErrorAction Stop).Source
$start = [Diagnostics.ProcessStartInfo]::new()
$start.FileName = $sshKeygen
$start.UseShellExecute = $false
$start.RedirectStandardInput = $true
$start.RedirectStandardOutput = $true
$start.RedirectStandardError = $true
foreach ($argument in @('-Y','verify','-f',$BackupAllowedSignersFile,'-I','invoice-backup','-n','solov-invoice-backup-v1','-s',$signature)) {
    $start.ArgumentList.Add($argument)
}
$process = [Diagnostics.Process]::new()
$process.StartInfo = $start
$process.Start() | Out-Null
$bytes = [IO.File]::ReadAllBytes($manifest)
$process.StandardInput.BaseStream.Write($bytes,0,$bytes.Length)
$process.StandardInput.Close()
$process.WaitForExit()
if ($process.ExitCode -ne 0) { throw 'downloaded backup manifest signature is invalid' }

$expected = @{}
foreach ($line in Get-Content -LiteralPath $manifest) {
    if ($line -notmatch '^([0-9a-f]{64})  ([A-Za-z0-9][A-Za-z0-9._-]{0,254})$') { throw 'downloaded backup manifest syntax is invalid' }
    if ($expected.ContainsKey($Matches[2])) { throw 'downloaded backup manifest contains a duplicate' }
    $expected[$Matches[2]] = $Matches[1]
}
if (($expected.Keys | Sort-Object) -join ',' -ne 'keycloak.postgres.dump.age,restore-proof.txt') { throw 'downloaded backup component set is not exact' }
foreach ($entry in $expected.GetEnumerator()) {
    $path = Join-Path $record $entry.Key
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { throw 'downloaded backup component is missing' }
    $actual = (Get-FileHash -Algorithm SHA256 -LiteralPath $path).Hash.ToLowerInvariant()
    if ($actual -cne $entry.Value) { throw 'downloaded backup component checksum mismatch' }
}

$manifestHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $manifest).Hash.ToLowerInvariant()
$ack = Join-Path $record 'OFFSITE-ACK'
$content = "record_id=$recordId`nbackup_manifest_sha256=$manifestHash`nverified_at_utc=$([DateTime]::UtcNow.ToString('yyyy-MM-ddTHH:mm:ssZ'))`n"
[IO.File]::WriteAllText($ack,$content,[Text.UTF8Encoding]::new($false))
& $sshKeygen -Y sign -q -f $OffsiteSigningKeyFile -n solov-invoice-offsite-v1 $ack
if ($LASTEXITCODE -ne 0 -or -not (Test-Path "$ack.sig")) { throw 'cannot sign off-site acknowledgement' }
Write-Host "Off-site backup verified; upload OFFSITE-ACK and OFFSITE-ACK.sig to the root-only record, then send exact stdin word: continue"
$global:LASTEXITCODE = 0

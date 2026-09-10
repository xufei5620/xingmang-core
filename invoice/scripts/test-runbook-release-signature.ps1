param(
    [string]$RunbookPath = (Join-Path $PSScriptRoot '../docs/PRODUCTION-RUNBOOK.md'),
    [Parameter(Mandatory)][string]$FixtureRoot
)
$ErrorActionPreference = 'Stop'
$fixture = [IO.Path]::GetFullPath($FixtureRoot)
[IO.Directory]::CreateDirectory($fixture) | Out-Null
$runbook = [IO.File]::ReadAllText($RunbookPath)
$block = [regex]::Match($runbook, '(?ms)^# 2026-09-06.*?(?=^```)').Value
if (-not $block) { throw 'signature verification procedure is absent' }
$fakeSource = @'
package main
import("crypto/sha256";"fmt";"io";"os";"reflect";"strconv")
func main(){
 want:=[]string{"-Y","verify","-f",os.Getenv("RUNBOOK_ALLOWED"),"-I","invoice-release@solov.cc","-n","solov-invoice-release-v1","-s",os.Getenv("RUNBOOK_SIGNATURE")}
 data,err:=io.ReadAll(os.Stdin)
 if err!=nil || !reflect.DeepEqual(os.Args[1:],want) || fmt.Sprintf("%x",sha256.Sum256(data))!=os.Getenv("RUNBOOK_HASH") {fmt.Fprintln(os.Stderr,"ARGV_OR_RAW_STDIN_MISMATCH");os.Exit(91)}
 fmt.Println("ARGV_AND_RAW_STDIN_OK")
 code,_:=strconv.Atoi(os.Getenv("RUNBOOK_FAKE_EXIT"));os.Exit(code)
}
'@
$fakeGo = Join-Path $fixture 'fake-verifier.go'
[IO.File]::WriteAllText($fakeGo, $fakeSource, [Text.UTF8Encoding]::new($false))
$fakeName = if ($IsWindows) { 'ssh-keygen.exe' } else { 'ssh-keygen' }
$fakeExe = Join-Path $fixture $fakeName
$envNames = @('PATH','GOWORK','GOPROXY','GOTOOLCHAIN','RUNBOOK_ALLOWED','RUNBOOK_SIGNATURE','RUNBOOK_HASH','RUNBOOK_FAKE_EXIT')
$saved = @{}
foreach ($name in $envNames) { $saved[$name] = [Environment]::GetEnvironmentVariable($name) }
try {
    $env:GOWORK = 'off'; $env:GOPROXY = 'off'; $env:GOTOOLCHAIN = 'local'
    & go build -o $fakeExe $fakeGo
    if ($LASTEXITCODE -ne 0) { throw 'cannot compile inert verifier fixture' }
    $manifest = Join-Path $fixture "manifest [甲]&'.SHA256SUMS"
    $signature = "$manifest.sig"
    $allowed = Join-Path $fixture "allowed signers [乙]&'"
    # Deliberately includes a BOM, CRLF, non-ASCII and a missing final newline.
    # These are inert bytes, not signing keys or credentials.
    [byte[]]$bytes = @(239,187,191) + [Text.Encoding]::UTF8.GetBytes("inert checksum 甲`r`nlast line")
    [IO.File]::WriteAllBytes($manifest,$bytes)
    $env:RUNBOOK_HASH = [Convert]::ToHexStringLower([Security.Cryptography.SHA256]::HashData($bytes))
    $env:RUNBOOK_ALLOWED = $allowed; $env:RUNBOOK_SIGNATURE = $signature
    $env:PATH = $fixture + [IO.Path]::PathSeparator + $saved['PATH']
    function Quote-PS([string]$value) { return "'" + $value.Replace("'","''") + "'" }
    $preamble = "`$ErrorActionPreference = 'Stop'`nfunction cmd { throw 'LEGACY_CMD_DOES_NOT_RECEIVE_POWERSHELL_PATHS' }`n"
    $preamble += '`$checksumManifest = '.Replace('`$','$') + (Quote-PS $manifest) + "`n"
    $preamble += '`$releaseSignature = '.Replace('`$','$') + (Quote-PS $signature) + "`n"
    $preamble += '`$releaseAllowedSigners = '.Replace('`$','$') + (Quote-PS $allowed) + "`n"
    $probe = Join-Path $fixture 'documented-verification.ps1'
    [IO.File]::WriteAllText($probe,$preamble+$block,[Text.UTF8Encoding]::new($false))
    foreach ($case in @(@{Code=0;Success=$true},@{Code=23;Success=$false})) {
        $env:RUNBOOK_FAKE_EXIT = [string]$case.Code
        $output = & pwsh -NoProfile -File $probe 2>&1 | Out-String
        $exit = $LASTEXITCODE
        if ($case.Success -and ($exit -ne 0 -or $output -notmatch 'ARGV_AND_RAW_STDIN_OK')) {
            throw "documented signature command failed exact argv/raw-input control: $output"
        }
        if (-not $case.Success -and ($exit -eq 0 -or $output -notmatch 'signature verification failed')) {
            throw "documented signature command ignored verifier rejection: $output"
        }
    }
    Write-Host 'RUNEARLY-01 exact document command: argv/raw bytes and verifier rejection passed; no key operation.'
} finally {
    foreach ($name in $envNames) { [Environment]::SetEnvironmentVariable($name,$saved[$name]) }
}

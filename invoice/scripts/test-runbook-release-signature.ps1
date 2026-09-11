param(
    [string]$SourceRoot = (Join-Path $PSScriptRoot '../..'),
    [string]$RunbookPath,
    [Parameter(Mandatory)][string]$FixtureRoot
)
$ErrorActionPreference = 'Stop'
$fixture = [IO.Path]::GetFullPath($FixtureRoot)
[IO.Directory]::CreateDirectory($fixture) | Out-Null
$root = [IO.Path]::GetFullPath($SourceRoot)
if (-not $RunbookPath) { $RunbookPath = Join-Path $root 'docs/runbooks/UNIFIED-CUTOVER.md' }
if ([IO.File]::ReadAllText($RunbookPath) -notmatch 'deploy/rehearsal-unified/rehearse.sh') { throw 'current unified D/E runbook is absent' }
$fakeSource = @'
package main
import("crypto/sha256";"fmt";"io";"os";"reflect";"strconv")
func main(){
 want:=[]string{"-Y","verify","-f",os.Getenv("RUNBOOK_ALLOWED"),"-I",os.Getenv("RUNBOOK_PRINCIPAL"),"-n",os.Getenv("RUNBOOK_NAMESPACE"),"-s",os.Getenv("RUNBOOK_SIGNATURE")}
 data,err:=io.ReadAll(os.Stdin)
 if err!=nil || !reflect.DeepEqual(os.Args[1:],want) || fmt.Sprintf("%x",sha256.Sum256(data))!=os.Getenv("RUNBOOK_HASH") {fmt.Fprintln(os.Stderr,"ARGV_OR_RAW_STDIN_MISMATCH");os.Exit(91)}
 fmt.Println("ARGV_AND_RAW_STDIN_OK")
 code,_:=strconv.Atoi(os.Getenv("RUNBOOK_FAKE_EXIT"));os.Exit(code)
}
'@
$fakeGo = Join-Path $fixture 'fake-verifier.go'
[IO.File]::WriteAllText($fakeGo, $fakeSource, [Text.UTF8Encoding]::new($false))
$fakeName = if ($IsWindows) { 'ssh-keygen-fixture.exe' } else { 'ssh-keygen-fixture' }
$fakeExe = Join-Path $fixture $fakeName
$saved = @{}
foreach ($name in @('GOWORK','GOPROXY','GOTOOLCHAIN')) { $saved[$name] = [Environment]::GetEnvironmentVariable($name) }
try {
    $env:GOWORK = 'off'; $env:GOPROXY = 'off'; $env:GOTOOLCHAIN = 'local'
    & go build -o $fakeExe $fakeGo
    if ($LASTEXITCODE -ne 0) { throw 'cannot compile inert verifier fixture' }
    $probe = @'
import datetime as dt
import hashlib
import os
from pathlib import Path
import sys
from unittest.mock import patch

root, fixture, fake = map(Path, sys.argv[1:])
sys.path.insert(0,str(root/'deploy/rehearsal-unified'))
import lifecycle as m
import restore as r

def scenario(label, *, failure=None, malformed=False, ending=b'\r\n'):
    work=fixture/label;work.mkdir()
    descriptors={}; raw={}
    stamp=dt.datetime.now(dt.timezone.utc).strftime('%Y%m%dT%H%M%SZ')
    for domain,kinds in [('platform',('database','metadata')),('invoice',('database','metadata','documents','source_state'))]:
        folder=work/domain;folder.mkdir()
        values={}
        for key in ('signature','identity_file','age_binary'):
            path=folder/(key+" [乙]&'");path.write_bytes(b'inert fixture, no keys')
            values[key]=str(path)
        values['ssh_keygen_binary']=str(fake)
        signers=folder/"allowed signers [乙]&'"
        principal,namespace=(domain+'-backup','solov-'+domain+'-backup-v1')
        signers.write_text(f'{principal} namespaces="{namespace}" ssh-ed25519 YWJj\n')
        values['allowed_signers']=str(signers)
        components={};lines=[]
        for kind in kinds:
            path=folder/(domain+'-'+stamp+r.SUFFIXES[kind]);path.write_bytes(b'inert ciphertext '+kind.encode())
            components[kind]=str(path);lines.append(hashlib.sha256(path.read_bytes()).hexdigest().encode()+b'  '+path.name.encode())
        # Valid CRLF/no-final-newline must retain exact bytes. The deliberately
        # malformed BOM/non-ASCII case reaches verifier unchanged then fails parsing.
        body=ending.join(lines)
        if malformed and domain=='platform':body=b'\xef\xbb\xbf'+body+b'\r\nnon-ascii \xe7\x94\xb2'
        manifest=folder/"manifest [甲]&'.SHA256SUMS";manifest.write_bytes(body)
        values['manifest']=str(manifest);values['components']=components
        descriptors[domain]=values;raw[domain]=body
    calls=[];digests=[]
    class Driver(m.DockerDriver):
        def command(self,name,args,**kwargs):
            domain=name.split('-')[1];calls.append(domain)
            row=descriptors[domain]
            self.env.update(RUNBOOK_ALLOWED=row['allowed_signers'],RUNBOOK_SIGNATURE=row['signature'],
                RUNBOOK_PRINCIPAL=domain+'-backup',RUNBOOK_NAMESPACE='solov-'+domain+'-backup-v1',
                RUNBOOK_HASH=hashlib.sha256(raw[domain]).hexdigest(),RUNBOOK_FAKE_EXIT='23' if domain==failure else '0')
            reply=super().command(name,args,**kwargs)
            assert reply.stdout.strip()==b'ARGV_AND_RAW_STDIN_OK','raw stdin/argv was not accepted by actual fixture process'
            return reply
        def pipeline(self,*args):raise AssertionError('age must never run during signature contract checks')
    driver=object.__new__(Driver);driver.env=dict(os.environ);driver.output=work/'events';driver.sequence=0
    actual_digest=r.digest
    def checked_digest(path):
        domain=Path(path).parent.name
        assert domain in calls,'ciphertext hashing preceded signature verification'
        assert domain != failure,'rejected signature reached ciphertext hashing'
        digests.append(str(path));return actual_digest(path)
    actual_read=Path.read_bytes
    def safe_read(path):
        assert str(path) not in [d['identity_file'] for d in descriptors.values()], 'private identity was read'
        return actual_read(path)
    with patch.object(r,'digest',checked_digest),patch.object(Path,'read_bytes',safe_read):
        try:proof=r.verify_backups(driver,descriptors,24)
        except (m.OperatorError,UnicodeError):
            assert failure is not None or malformed,'valid backup unexpectedly rejected'
        else:
            assert failure is None and not malformed,'invalid signature/manifest was accepted'
            assert set(proof)=={'platform','invoice'}
            assert all(proof[d]['manifest_sha256']==hashlib.sha256(raw[d]).hexdigest() for d in raw)
    assert calls==(['platform'] if failure=='platform' or malformed else ['platform','invoice'])
    if failure=='platform' or malformed:assert not digests,'rejected signature/invalid manifest reached component reads'

scenario('valid-crlf-no-final-newline')
scenario('valid-lf',ending=b'\n')
scenario('malformed-bom-unicode',malformed=True)
scenario('platform-verifier-exit23',failure='platform')
scenario('invoice-verifier-exit23',failure='invoice')
print('RUNEARLY-01 actual two-domain verify_backups: exact argv/raw bytes, bad syntax and both verifier failures rejected; no signing/key/age operation.')
'@
    $probePath = Join-Path $fixture 'verify-current-backups.py'
    [IO.File]::WriteAllText($probePath,$probe,[Text.UTF8Encoding]::new($false))
    & python -X utf8 $probePath $root $fixture $fakeExe
    if ($LASTEXITCODE -ne 0) { throw 'current backup signature consumer contract failed' }
} finally {
    foreach ($name in $saved.Keys) { [Environment]::SetEnvironmentVariable($name,$saved[$name]) }
}

"""Exercise the audit dispatcher using inert child programs, never real gates."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile

SCRIPTS = Path(__file__).resolve().parents[1]
# This is the expected coverage contract, independent of the dispatcher's data.
EXPECTED = [
    ('tests/test_full_audit_wiring.py', []),
    ('test-git-root-boundaries.ps1', []),
    ('test-release-path-boundaries.ps1', []),
    ('test-unified-operations.py', []),
    *[('test-trivy-cache-safety.ps1', ['-Case', case]) for case in
      ('image-parameters', 'lock-errors', 'resume-identity', 'unchanged-cache', 'shared-cache-lock')],
    *[('test-register-trivy-refresh-behavior.ps1', ['-Case', case]) for case in ('legacy-suite', 'meta')],
    *[('tests/' + name + '.py', []) for name in
      ('test_projection_attachment', 'test_capture_failures', 'test_collection_producers', 'test_index_finalizer', 'test_shell_syntax_coverage')],
    ('test-runbook-release-signature.ps1', ['-FixtureRoot', '{fixture}/signature']),
    ('test-runbook-command-contracts.py', ['--case', 'release-env', '--fixture-root', '{fixture}/release-env']),
    ('test-runbook-command-contracts.py', ['--case', 'migration-gate', '--fixture-root', '{fixture}/migration-gate']),
    ('test-runbook-repair-contracts.py', ['--case', 'kinds', '--fixture-root', '{fixture}/repair-kinds']),
    ('test-runbook-repair-contracts.py', ['--case', 'exit-codes', '--fixture-root', '{fixture}/repair-exits']),
    *[('test-runbook-maintenance-contracts.py', ['--case', case]) for case in
      ('blocked-event', 'shadow-order')],
]
PS_STUB = r'''param([string]$Case, [string]$FixtureRoot)
$arguments = @()
if ($PSBoundParameters.ContainsKey('Case')) { $arguments += @('-Case', $Case) }
if ($PSBoundParameters.ContainsKey('FixtureRoot')) { $arguments += @('-FixtureRoot', $FixtureRoot) }
$record = @{Script='__SCRIPT__'; Arguments=$arguments; Pid=$PID; Cwd=(Get-Location).Path; Bash=$env:TEST_BASH; RunbookBash=$env:RUNBOOK_TEST_BASH}
[IO.File]::AppendAllText($env:INVOICE_WIRING_TRACE, ($record | ConvertTo-Json -Compress) + "`n")
if ($record.Script -ceq $env:INVOICE_WIRING_FAIL_SCRIPT) { exit ([int]$env:INVOICE_WIRING_FAIL_EXIT) }
exit 0
'''
PY_STUB = '''import json,os,sys
from pathlib import Path
record=dict(Script=__SCRIPT__,Arguments=sys.argv[1:],Pid=os.getpid(),Cwd=os.getcwd(),Bash=os.environ.get('TEST_BASH'),RunbookBash=os.environ.get('RUNBOOK_TEST_BASH'),Optimize=sys.flags.optimize)
with open(os.environ['INVOICE_WIRING_TRACE'],'a',encoding='utf-8') as f:f.write(json.dumps(record)+'\\n')
sys.exit(int(os.environ['INVOICE_WIRING_FAIL_EXIT']) if record['Script']==os.environ.get('INVOICE_WIRING_FAIL_SCRIPT') else 0)
'''

def main():
    root = Path(tempfile.mkdtemp(prefix='invoice-audit-wiring-回归 '))
    scripts = root / 'invoice/scripts'
    scripts.mkdir(parents=True)
    shutil.copyfile(SCRIPTS / 'test-full-audit.ps1', scripts / 'test-full-audit.ps1')
    for path, _ in EXPECTED:
        target = scripts / path
        target.parent.mkdir(parents=True, exist_ok=True)
        content = PS_STUB.replace('__SCRIPT__', path) if path.endswith('.ps1') else PY_STUB.replace('__SCRIPT__', repr(path))
        target.write_text(content, encoding='utf-8', newline='\n')
    pwsh = shutil.which('pwsh')
    assert pwsh, 'audit-wiring: PowerShell 7 is required'
    scenarios = [('all', '', 0), ('powershell-failure', 'test-git-root-boundaries.ps1', 23),
                 ('python-failure', 'tests/test_projection_attachment.py', 19),
                 ('native-negative', 'test-git-root-boundaries.ps1', -9)]
    for name, failed_script, fail_exit in scenarios:
        trace = root / (name + '.jsonl')
        fixture = root / (name + '-fixtures')
        env = dict(os.environ, INVOICE_WIRING_TRACE=str(trace), INVOICE_WIRING_FAIL_SCRIPT=failed_script,
                   INVOICE_WIRING_FAIL_EXIT=str(fail_exit), PYTHONOPTIMIZE='1')
        result = subprocess.run([pwsh, '-NoProfile', '-NonInteractive', '-File', str(scripts/'test-full-audit.ps1'),
                                 '-FixtureRoot', str(fixture)], cwd=root, env=env, capture_output=True, text=True, encoding='utf-8', errors='replace')
        (root/(name+'.log')).write_text(result.stdout+result.stderr, encoding='utf-8')
        if not failed_script:
            assert result.returncode == 0, 'audit-wiring: successful child was rejected: ' + result.stderr
            expected = EXPECTED
        else:
            assert result.returncode != 0, 'audit-wiring: child failure was swallowed: ' + name
            assert 'full-audit child failed:' in result.stderr, 'audit-wiring: failure did not come from child-exit propagation'
            expected = EXPECTED[:next(i for i, item in enumerate(EXPECTED) if item[0] == failed_script)+1]
        records = [json.loads(line) for line in trace.read_text(encoding='utf-8-sig').splitlines()] if trace.exists() else []
        actual = [(r['Script'], [str(a).replace('\\','/') for a in r['Arguments']]) for r in records]
        expanded = [(path, [a.replace('{fixture}',fixture.as_posix()) for a in args]) for path,args in expected]
        assert actual == expanded, 'audit-wiring: exact child coverage/arguments or stop-on-failure changed: ' + name
        assert all(Path(r['Cwd']).resolve() == (root/'invoice').resolve() for r in records), 'audit-wiring: child working directory changed'
        assert all(r['Bash'] and r['Bash'] == r['RunbookBash'] and Path(r['Bash']).is_file() for r in records), 'audit-wiring: child Bash environment missing'
        assert all(r['Optimize'] == 0 for r in records if r['Script'].endswith('.py')), 'audit-wiring: Python assertions were disabled'
    print('AUDIT-WIRING-PASS: exact coverage, arguments, isolated children and native failures; retained fixture:', root)

if __name__ == '__main__':
    main()

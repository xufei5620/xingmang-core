"""Exercise secret capture and documented DB capture without starting services."""
import argparse, json, os, pathlib, re, shutil, subprocess, tempfile

parser = argparse.ArgumentParser()
parser.add_argument('--root', type=pathlib.Path, default=pathlib.Path(__file__).resolve().parents[3])
parser.add_argument('--case')
args = parser.parse_args()
bash = os.environ.get('TEST_BASH') or shutil.which('bash')
fixture_root = pathlib.Path(__file__).resolve().parents[2] / 'release/auxiliary-test-fixtures'
fixture_root.mkdir(parents=True, exist_ok=True)
fixture = pathlib.Path(tempfile.mkdtemp(prefix='capture-', dir=fixture_root))
failures = []

def check(case, script, expected, output, env=None):
    if args.case and args.case != case: return
    path = fixture / (case+'.sh')
    path.write_text('set -euo pipefail\n'+script, encoding='utf-8', newline='\n')
    result = subprocess.run([bash, path.as_posix()], capture_output=True, env=os.environ | (env or {}))
    ok = (result.returncode == expected if expected == 0 else result.returncode != 0) and result.stdout == output
    if not ok: failures.append(f'{case}: expected exit {expected} and bounded sentinel, got exit {result.returncode}')
    else: print('PASS '+case)

# Exercise the installed module's consumer, never the removed IdP wrapper.
assert not (args.root/'invoice/deploy/keycloak/entrypoint.sh').exists(), 'retired Keycloak entrypoint must remain absent'
if not args.case or args.case == 'service-secret-capture':
    go = shutil.which('go')
    assert go, 'Go is required for the active secret-file capture contract'
    result = subprocess.run(
        [go, 'test', '-json', '-count=1', '-p', '1', './service', '-run', '^TestReadSecretLineCaptureContract$'],
        cwd=args.root/'invoice/backend', capture_output=True,
    )
    # These tests use inert data and intentionally never print captured values.
    print(result.stdout.decode('utf-8', errors='replace'), end='')
    if result.returncode != 0:
        print(result.stderr.decode('utf-8', errors='replace'), end='')
        raise AssertionError('active secret capture Go test failed: exit '+str(result.returncode))
    events=[]
    for line in result.stdout.splitlines():
        try: events.append(json.loads(line))
        except (ValueError, TypeError): pass
    assert any(e.get('Test') == 'TestReadSecretLineCaptureContract' and e.get('Action') == 'pass' for e in events), 'active secret capture test did not run'
    assert not any(e.get('Action') in ('fail', 'skip') for e in events), 'active secret capture test failed or skipped'
    print('PASS service-secret-capture')

for origin, relative in [('runbook','platform/docs/runbooks/GIT-WORKFLOW.md'),('header','platform/scripts/dev/worktree-testdb.sh')]:
    text=(args.root/relative).read_text(encoding='utf-8-sig')
    if origin=='header': text='\n'.join(re.sub(r'^#\s{0,3}','',line) for line in text.splitlines() if line.startswith('#'))
    for mode, variable in [('eval','testdb_exports'),('export','testdb_url')]:
        if re.search(r'(?m)^'+variable+r'=\$\(',text):
            tail=text[re.search(r'(?m)^'+variable+r'=\$\(',text).start():]
            end = re.search(r'(?m)^eval "\$testdb_exports".*$' if mode=='eval' else r'(?m)^export XM_TEST_DATABASE_URL="\$testdb_url".*$',tail)
            assert end, origin+' documented capture completion missing'
            block=tail[:end.end()]
        else:
            pattern = r'(?m)^eval "\$\(.*worktree-testdb\.sh.*$' if mode=='eval' else r'(?m)^export XM_TEST_DATABASE_URL=\$\(.*worktree-testdb\.sh.*$'
            match=re.search(pattern,text)
            assert match, origin+' documented original capture missing'
            block=match.group()
        for status in ['failed','empty','valid']:
            fake = 'return 17' if status=='failed' else (':' if status=='empty' else ('printf "export XM_TEST_DATABASE_URL=fixture-db\\n"' if mode=='eval' else 'printf fixture-db'))
            # Intercept both documented invocation forms without provisioning.
            prefix='unset XM_TEST_DATABASE_URL\nbash(){ '+fake+'; }\nfunction scripts/dev/worktree-testdb.sh { '+fake+'; }\n'
            suffix='\n[[ "${XM_TEST_DATABASE_URL:-}" == fixture-db ]] || exit 94\n' if status=='valid' else '\n'
            check(f'{origin}-{mode}-{status}', prefix+block+suffix+'printf reached\n',0 if status=='valid' else 1,b'reached' if status=='valid' else b'')
if failures: raise AssertionError('\n'.join(failures))

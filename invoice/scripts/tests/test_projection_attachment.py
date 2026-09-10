"""Execute only ensure_attachment against typed Docker-inspect projections."""
import argparse, os, pathlib, re, shutil, subprocess, tempfile

parser = argparse.ArgumentParser()
parser.add_argument('--source', type=pathlib.Path)
parser.add_argument('--case', choices=['valid-alias', 'missing-alias', 'query-error'])
args = parser.parse_args()
invoice = pathlib.Path(__file__).resolve().parents[2]
source = args.source or invoice / 'deploy/provision-projection-networks.sh'
text = source.read_text(encoding='utf-8-sig')
match = re.search(r'(?ms)^ensure_attachment\(\) \{.*?^\}', text)
assert match, 'production ensure_attachment function missing'
root = invoice / 'release/auxiliary-test-fixtures'
root.mkdir(parents=True, exist_ok=True)
fixture = pathlib.Path(tempfile.mkdtemp(prefix='attachment-', dir=root))
binary = fixture / ('docker-fixture.exe' if os.name == 'nt' else 'docker-fixture')
subprocess.run(['go', 'build', '-o', str(binary), str(pathlib.Path(__file__).with_name('projection-inspect-fixture.go'))], check=True)
script = fixture / 'probe.sh'
script.write_text('set -Eeuo pipefail\ndocker(){ "$FAKE_DOCKER" "$@"; }\n' + match.group() + '\nensure_attachment fixture-network fixture-db required-alias\n', encoding='utf-8', newline='\n')
bash = os.environ.get('TEST_BASH') or shutil.which('bash')
for case, expected in [('valid-alias', 0), ('missing-alias', 1), ('query-error', 74)]:
    if args.case and args.case != case: continue
    env = os.environ | {'FAKE_DOCKER':binary.as_posix(), 'ATTACHMENT_CASE':case}
    result = subprocess.run([bash, script.as_posix()], env=env, capture_output=True)
    assert result.returncode == expected, f'{case}: expected exit {expected}, got {result.returncode}; {result.stderr.decode(errors="replace")}'
    print(f'PASS {case}')

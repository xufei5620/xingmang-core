"""Execute the installer's bounded Git configuration block with a command recorder."""
from pathlib import Path
import os
import subprocess

root = Path(__file__).resolve().parents[2]
source = (root/'deploy/scripts/install-git-server.sh').read_text(encoding='utf-8')
start = source.index('git --git-dir="$repo_path" config receive.denyNonFastForwards true')
end = source.index('# 不对整个 ci-dir', start)
block = source[start:end]
# This test is restricted to Git config; fail rather than execute a new lifecycle operation.
for line in block.splitlines():
    if line.strip() and not line.lstrip().startswith('#'):
        assert line.startswith('git --git-dir="$repo_path" config '), 'unrecognized install operation'
prefix = '''set -euo pipefail
git() { printf '%s\\n' "$*"; }
repo_path=/synthetic/repo.git
ci_dir=/synthetic/ci
ci_script_target=/synthetic/ci/ci-local.sh
ci_script_sha256=synthetic
'''
result = subprocess.run(['bash', '-p', '-c', prefix+block], env={'PATH':os.environ['PATH']},
                        capture_output=True, text=True, check=True)
assert '--git-dir=/synthetic/repo.git config receive.denyDeletes true' in result.stdout.splitlines(), 'missing denyDeletes call'
assert '--git-dir=/synthetic/repo.git config xm.ci.script /synthetic/ci/ci-local.sh' in result.stdout.splitlines(), 'missing trusted CI registration call'
print('installer Git configuration calls verified')

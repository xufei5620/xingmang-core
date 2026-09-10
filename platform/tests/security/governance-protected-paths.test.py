"""Exercise the guard against actual standalone and monorepo Git diffs, offline."""
import os
from pathlib import Path
import shutil
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[2]
git = shutil.which('git')
bash = str(Path(git).parents[1] / 'bin/bash.exe') if os.name == 'nt' else 'bash'
env = os.environ.copy()
env.update(GIT_CONFIG_GLOBAL=os.devnull, GIT_CONFIG_NOSYSTEM='1', GIT_OPTIONAL_LOCKS='0')
with tempfile.TemporaryDirectory(prefix='guard-paths-') as tmp:
    repo = Path(tmp)
    def command(*args):
        return subprocess.check_output(args, cwd=repo, env=env, text=True).strip()
    command(git, 'init', '-q')
    paths = ['PROJECT-CONSTITUTION.md', 'platform/PROJECT-CONSTITUTION.md',
             'platform/scripts/check-compose-env.py', '.github/workflows/ci.yml', 'platform/docs/note.md']
    for name in paths:
        p = repo / name
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_text('baseline\n', encoding='utf-8')
    command(git, 'add', '.')
    command(git, '-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.invalid',
            '-c', 'commit.gpgsign=false', 'commit', '-qm', 'baseline')
    base = command(git, 'rev-parse', 'HEAD')
    bindir = repo / 'bin'
    bindir.mkdir()
    # Fetch is the only network boundary. Merge/diff use exact local fixture refs.
    shim = bindir / 'git'
    shim.write_text('#!/usr/bin/env bash\ncase "$1" in fetch) exit 0;; '
                    'merge-base) printf "%s\\n" "$FIXTURE_BASE"; exit 0;; esac\n'
                    'exec "$FIXTURE_GIT" "$@"\n', encoding='utf-8')
    shim.chmod(0o755)
    for name in paths:
        (repo / name).write_text('changed\n', encoding='utf-8')
        command(git, 'add', name)
        command(git, '-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.invalid',
                '-c', 'commit.gpgsign=false', 'commit', '-qm', name)
        head = command(git, 'rev-parse', 'HEAD')
        for labels in ('', 'governance-change'):
            testenv = env | {'PATH':str(bindir)+os.pathsep+env['PATH'], 'FIXTURE_BASE':base,
                            'FIXTURE_GIT':git, 'BASE_REF':'main', 'HEAD_SHA':head, 'PR_LABELS':labels,
                            'GITHUB_STEP_SUMMARY':str(repo/'summary.md')}
            result = subprocess.run([bash, '-c', 'export PATH="$(cygpath -u "$1" 2>/dev/null || printf "%s" "$1"):$PATH"; bash "$2"',
                                     '_', str(bindir), str(ROOT/'scripts/guard-governance-files.sh')],
                                    cwd=repo, env=testenv, capture_output=True, text=True, encoding='utf-8')
            expected = 1 if labels == '' and name != 'platform/docs/note.md' else 0
            assert result.returncode == expected, f'{name} labels={labels!r}: expected {expected}, got {result.returncode}'
        base = head
print('governance protected paths passed')

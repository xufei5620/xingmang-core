"""Run only the production confirmation predicate, never a production invocation."""
from pathlib import Path
import os
import subprocess

source = (Path(__file__).resolve().parents[2]/'deploy/scripts/deploy.sh').read_text(encoding='utf-8')
start = source.index('if [ "$env_name" = "prod" ] && [ "$dry_run" -eq 0 ]; then')
end = source.index('\nfi', start) + len('\nfi')
predicate = source[start:end]
for token, expected in (('', 1), ('wrong', 1), ('DEPLOY-PRODUCTION', 0)):
    prefix = 'env_name=prod\ndry_run=0\nconfirm_token="$1"\ndie() { printf "%s\\n" "$*" >&2; }\n'
    result = subprocess.run(['bash', '-p', '-c', prefix+predicate, '_', token],
                            env={'PATH':os.environ['PATH']}, capture_output=True, text=True)
    assert result.returncode == expected, f'production confirmation predicate returned {result.returncode}, expected {expected}'
    if expected == 1:
        assert 'production 必须提供 --confirm DEPLOY-PRODUCTION' in result.stderr, 'wrong rejection boundary'
print('production confirmation predicate verified without deployment')

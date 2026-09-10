"""Bounded command/document checks. Never invokes Docker, SSH or a production tool."""
from pathlib import Path
import argparse, os, re, shlex, subprocess

PROJECT = Path(__file__).resolve().parents[1]
BASH = os.environ.get('RUNBOOK_TEST_BASH', 'D:/Git/bin/bash.exe' if os.name == 'nt' else 'bash')

def blocks(text):
    return re.findall(r'(?ms)^\s*```(?:bash|powershell)?\s*\n(.*?)^\s*```\s*$', text)

def release_env(runbook, fixture):
    release = fixture / 'release with spaces'
    project = release / 'source' / 'invoice'
    (project / 'deploy').mkdir(parents=True, exist_ok=True)
    env_file = release / '.env.production'
    env_file.write_text('ELIGIBILITY_START_AT=2026-09-01T00:00:00+08:00\n', encoding='utf-8')
    (project/'relative.env').write_text('inert relative-path control\n',encoding='utf-8')
    for name in ['prod','sources','idp','idp.bootstrap']:
        (project/'deploy'/('docker-compose.'+name+'.yml')).write_text('# inert compose fixture\n')
    source = runbook.split('## 4. Host directories and secrets',1)[1].split('## 8. Configure upstream OIDC',1)[0]
    logical = '\n'.join(blocks(source)).replace('\\\n',' ')
    prefixes = re.findall(r'docker compose\b(.*?)(?=\s+(?:up|run|exec|config|stop|ps)\b)',logical)
    assert prefixes, 'release environment command inventory is empty'
    fake = '''docker() {
      [[ "$1" == compose ]] || return 91
      shift
      local used_env='' file
      while (( $# )); do
        case "$1" in
          --env-file) used_env=$2; shift 2 ;;
          -f) file=$2; [[ -f "$file" ]] || { echo COMPOSE_PATH_MISSING >&2; return 92; }; shift 2 ;;
          *) shift ;;
        esac
      done
      [[ "$used_env" == "$EXPECTED_RELEASE_ENV" && -s "$used_env" ]] || { echo WRONG_RELEASE_ENV >&2; return 93; }
    }
    '''
    env=dict(os.environ,PRODUCTION_ENV_FILE=env_file.as_posix(),EXPECTED_RELEASE_ENV=env_file.as_posix())
    env.pop('BASH_ENV',None);env.pop('ENV',None)
    setup=next((b for b in blocks(source) if b.startswith('export PRODUCTION_ENV_FILE=')),None)
    assert setup is not None, 'release environment selection prerequisite is absent'
    value=env_file.as_posix()
    if os.name=='nt': value='/'+value[0].lower()+value[2:]
    for supplied,expected in [(value,0),(value+'.absent',1),('relative.env',1)]:
        case=re.sub(r"(?m)^export PRODUCTION_ENV_FILE=.*$",'export PRODUCTION_ENV_FILE='+shlex.quote(supplied),setup)
        p=subprocess.run([BASH,'--noprofile','--norc','-c',case],cwd=project,env=env,capture_output=True,text=True)
        assert p.returncode==expected, f'release env setup did not enforce absolute existing file: {supplied}'
    for i,prefix in enumerate(prefixes):
        # Only the inspected command's Compose/global-options prefix is evaluated;
        # no lifecycle arguments, substitutions, redirections or pipelines run.
        assert not any(x in prefix for x in ['$', '`',';','|','<','>']) or '$PRODUCTION_ENV_FILE' in prefix, prefix
        command='docker compose'+prefix+' config\n'
        p=subprocess.run([BASH,'--noprofile','--norc','-c',fake+command],cwd=project,env=env,capture_output=True,text=True)
        assert p.returncode==0, f'Compose invocation {i} does not select the release environment: {p.stderr}'
    # Inspect assignments passed to two actual shell entrypoints. External bash
    # is replaced, so neither provisioning nor permissions code can execute.
    for entry in ['preflight-secret-permissions.sh','provision-projection-networks.sh']:
        for match in re.finditer(r'(?m)^([^\n]*?)bash deploy/'+re.escape(entry)+r'\b',logical):
            assignments=match.group(1)
            p=subprocess.run([BASH,'--noprofile','--norc','-c',
                'bash() { [[ "$PRODUCTION_ENV_FILE" == "$EXPECTED_RELEASE_ENV" ]] || { echo WRONG_RELEASE_ENV >&2; return 94; }; };\n'+assignments+'bash fixture-entrypoint'],cwd=project,env=env,capture_output=True,text=True)
            assert p.returncode==0, f'{entry} environment assignment is stale: {p.stderr}'
    # Actual stat/grep path arguments in the policy-start check must address the
    # same file selected by Compose; tokenize without executing any file reader.
    policy=next(b for b in blocks(source) if "ELIGIBILITY_START_AT=2026-09-01" in b and 'stat -c' in b)
    for pattern in [r"stat -c '%a' (.*?)\)",r"grep -Fxc 'ELIGIBILITY_START_AT=[^']+' (.*?)\)"]:
        arg=re.search(pattern,policy).group(1).replace('$PRODUCTION_ENV_FILE',env_file.as_posix())
        tokens=shlex.split(arg)
        assert len(tokens)==1 and Path(tokens[0])==env_file, 'policy-start check reads a different environment file'
    print(f'RUNEARLY-02: {len(prefixes)} actual Compose prefixes and wrapper/policy paths select one release env; external calls trapped.')

if __name__=='__main__':
    parser=argparse.ArgumentParser()
    parser.add_argument('--case',choices=['release-env'],required=True)
    parser.add_argument('--fixture-root',type=Path,required=True)
    parser.add_argument('--runbook',type=Path,default=PROJECT/'docs/PRODUCTION-RUNBOOK.md')
    args=parser.parse_args();args.fixture_root.mkdir(parents=True,exist_ok=True)
    release_env(args.runbook.read_text(encoding='utf-8-sig'),args.fixture_root)

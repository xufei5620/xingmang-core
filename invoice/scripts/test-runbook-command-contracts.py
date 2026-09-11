"""Bounded command/document checks. Never invokes Docker, SSH or a production tool."""
from pathlib import Path
import argparse, hashlib, os, re, shlex, subprocess

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

def migration_gate(runbook,fixture):
    migration=next(b for b in blocks(runbook) if 'schema-migrations-expected.tsv' in b and 'run --rm --pull never migrate' in b)
    privileges=next(b for b in blocks(runbook) if 'carry-forward-effective-privileges.tsv' in b and '<deploy/postgres/harden-runtime-role.sql' in b)
    (fixture/'backend/migrations').mkdir(parents=True,exist_ok=True)
    (fixture/'deploy/postgres').mkdir(parents=True,exist_ok=True)
    (fixture/'deploy/postgres/harden-runtime-role.sql').write_text('-- inert stdin fixture\n')
    names=['0013_source_readiness_active_index.sql','0014_balance_carry_forward_proof.sql']
    schema=[]
    for n in names:
        data=('inert migration '+n+'\n').encode();(fixture/'backend/migrations'/n).write_bytes(data)
        schema.append(n+'|'+hashlib.sha256(data).hexdigest())
    fake='''
install() { command mkdir -p -- "${@: -1}"; }
sha256sum() { [[ "$CASE" != hash-fail ]] || { echo FIXTURE_HASH_FAILURE >&2; return 95; }; command sha256sum "$@"; }
docker() {
  local command="$*"
  case "$command" in
    *' run '*) [[ "$CASE" != migrate-fail ]] || return 21 ;;
    *'SELECT name,checksum'*) command cat "$FIXTURE_SCHEMA"; [[ "$CASE" != schema-query-fail ]] || return 22 ;;
    *"SELECT has_table_privilege"*)
      if [[ "$CASE" == schema-privilege ]]; then echo 't|t|f|f|f|f|f'; else echo 't|f|f|f|f|f|f'; fi
      [[ "$CASE" != privilege-query-fail ]] || return 23 ;;
    *'has_table_privilege'*) command cat "$FIXTURE_EFFECTIVE" ;;
    *'role_table_grants'*) command cat "$FIXTURE_DIRECT" ;;
    *) command cat >/dev/null; [[ "$CASE" != permissions-fail ]] || return 24 ;;
  esac
}
'''
    cases=[('matching',False),('extra-migration',False),('migrate-fail',False),('hash-fail',False),('schema-query-fail',False),
           ('matching',True),('effective-first',True),('effective-second',True),('effective-extra',True),('direct-first',True),('direct-second',True),('direct-extra',True),('schema-privilege',True),('privilege-query-fail',True),('permissions-fail',True),('extra-migration',True)]
    for case,is_privilege in cases:
        case_dir=fixture/(case+('-privilege' if is_privilege else '-migration'));case_dir.mkdir(exist_ok=True)
        schema_lines=schema+(['9999_unapproved.sql|'+'a'*64] if case=='extra-migration' else [])
        (case_dir/'actual.tsv').write_text('\n'.join(schema_lines)+'\n',newline='\n')
        eff=['balance_carry_forward_evaluations|t|t|f|f|f|f|f','balance_carry_forward_proofs|t|t|f|f|f|f|f']
        direct=['balance_carry_forward_evaluations|INSERT,SELECT','balance_carry_forward_proofs|INSERT,SELECT']
        if case.startswith('effective-'):
            if case.endswith('extra'):eff.append('extra|t|t|f|f|f|f|f')
            else:eff[0 if case.endswith('first') else 1]=eff[0 if case.endswith('first') else 1].replace('|t|t|f','|t|t|t',1)
        if case.startswith('direct-'):
            if case.endswith('extra'):direct.append('extra|INSERT,SELECT')
            else:direct[0 if case.endswith('first') else 1]+=',UPDATE'
        (case_dir/'effective.tsv').write_text('\n'.join(eff)+'\n',newline='\n');(case_dir/'direct.tsv').write_text('\n'.join(direct)+'\n',newline='\n')
        env=dict(os.environ,CASE=case,RECORD_ROOT=case_dir.as_posix(),PRODUCTION_ENV_FILE='/inert/env',RC39_POST_MIGRATION_EVIDENCE_DIR=case_dir.as_posix(),FIXTURE_SCHEMA=(case_dir/'actual.tsv').as_posix(),FIXTURE_EFFECTIVE=(case_dir/'effective.tsv').as_posix(),FIXTURE_DIRECT=(case_dir/'direct.tsv').as_posix())
        env.pop('BASH_ENV',None);env.pop('ENV',None)
        (case_dir/'schema-migrations-expected.tsv').write_text('\n'.join(schema)+'\n',newline='\n')
        command=privileges if is_privilege else migration.replace('<exact-rc39-post-migration-record>','post')
        p=subprocess.run([BASH,'--noprofile','--norc','-c',fake+command],cwd=fixture,env=env,capture_output=True,text=True)
        assert (p.returncode==0)==(case=='matching'),f'{case} {"privilege" if is_privilege else "migration"} gate exit={p.returncode}; must stop on failed proof; stderr={p.stderr} stdout={p.stdout}'
    print(f'RUNEARLY-04: {len(cases)} actual migration/privilege command-block controls passed; Docker and permissions execution trapped.')

if __name__=='__main__':
    parser=argparse.ArgumentParser()
    parser.add_argument('--case',choices=['release-env','migration-gate'],required=True)
    parser.add_argument('--fixture-root',type=Path,required=True)
    parser.add_argument('--runbook',type=Path,default=PROJECT/'docs/PRODUCTION-RUNBOOK.md')
    args=parser.parse_args();args.fixture_root.mkdir(parents=True,exist_ok=True)
    {'release-env':release_env,'migration-gate':migration_gate}[args.case](args.runbook.read_text(encoding='utf-8-sig'),args.fixture_root)

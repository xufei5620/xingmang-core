"""Current unified command consumers; all external operations use inert fixtures."""
from pathlib import Path
import argparse, copy, hashlib, importlib, json, os, re, shlex, subprocess, sys
from unittest.mock import patch

PROJECT = Path(__file__).resolve().parents[1]
BASH = os.environ.get('RUNBOOK_TEST_BASH', 'D:/Git/bin/bash.exe' if os.name == 'nt' else 'bash')

def blocks(text):
    return re.findall(r'(?ms)^\s*```(?:bash|powershell)?\s*\n(.*?)^\s*```\s*$', text)

def load_operator(root):
    scripts = root / 'deploy/rehearsal-unified'
    assert (scripts / 'lifecycle.py').is_file(), 'current unified lifecycle is absent'
    sys.path.insert(0, str(scripts))
    return importlib.import_module('lifecycle')

def config_fixture(m, fixture):
    folder = fixture / "release with spaces [甲]&'"
    folder.mkdir(parents=True, exist_ok=True)
    paths = {}
    for name in ('manifest.json', 'smoke.json'):
        path = folder / name; path.write_text('{}\n', encoding='utf-8'); paths[name] = str(path)
    def project(kind, roles, prefix):
        directory = folder/prefix/kind; directory.mkdir(parents=True)
        services = {role: dict(role=role, image_id='sha256:'+'a'*64) for role in roles}
        base, overlay, env = (directory/name for name in ('compose.one.json', 'compose.two.json', 'reviewed.env'))
        base.write_text(json.dumps({'services': {role: {'image': row['image_id']} for role, row in services.items()}})+'\n', encoding='utf-8')
        overlay.write_text('{"services":{}}\n', encoding='utf-8')
        env.write_text('PUBLIC_FIXTURE=reviewed\n', encoding='utf-8')
        return dict(kind=kind, name=prefix+'-'+kind, compose_files=[str(base), str(overlay)],
                    env_file=str(env), services=services)
    old = [project(kind, sorted(roles), 'old') for kind, roles in m.OLD_ROLES.items()]
    candidate = [project('unified', ['platform-api','web'], 'new'), project('sources', sorted(m.STREAM_ROLES), 'new')]
    # These are public inputs, not a mock of HostNginx or the preservation guard.
    # Command-only tests do not invoke nginx or certify TLS/runtime readiness.
    nginx = folder/'nginx'; nginx.mkdir()
    live, staged = nginx/'live.conf', nginx/'candidate.conf'
    def routes(admin, user):
        return ''.join(f'server {{ listen {port} ssl; server_name localhost; location / {{ proxy_pass http://127.0.0.1:{upstream}; }} }}\n'
                       for port, upstream in ((8444, admin), (8443, user)))
    live.write_text(routes(8089,58092), encoding='utf-8')
    staged.write_text(routes(8088,58090), encoding='utf-8')
    main, staged_main = nginx/'nginx.conf', nginx/'staged.conf'
    for target, vhost in ((main,live), (staged_main,staged)):
        target.write_text('events {}\nhttp { include '+json.dumps(vhost.as_posix(),ensure_ascii=False)+'; }\n', encoding='utf-8')
    Path(paths['smoke.json']).write_text(json.dumps({'origins': {'admin':'https://localhost:8444','user':'https://localhost:8443'}})+'\n', encoding='utf-8')
    host_nginx = dict(main_config=str(main), staged_main_config=str(staged_main),
        vhosts=[dict(live=str(live), candidate=str(staged), candidate_sha256=m.digest(staged))],
        execution=dict(container='fixture-nginx', image_id='sha256:'+'a'*64, project='fixture-nginx', host_root=str(nginx), container_root='/config'),
        web_ports=dict(admin=8088,user=58090))
    config = dict(schema='xingmang.unified.operator/v1', mode='local-synthetic', state_root=str(folder/'state'),
        docker=dict(binary=sys.executable, context='inert-local', config_dir=str(folder)),
        candidate=dict(head='a'*40, manifest=paths['manifest.json'], manifest_sha256='b'*64, migration_digest='c'*64,
            projects=candidate, jobs=[dict(project='new-unified',service=x) for x in ('migrate','invoice-migrate','invoice-permissions')], ready_url='http://127.0.0.1:1/readyz', databases={}),
        previous=dict(projects=old, migration_digest='c'*64, ready_urls={}, permission_jobs=[], databases={}),
        backups=dict(invoice={},platform={}), approvals={}, smoke_config=paths['smoke.json'], rehearsal={}, host_preflight={}, host_nginx=host_nginx)
    config['previous']['input_snapshot'] = m.capture_old_inputs(config, [str(folder/'old')],
        str(Path(m.__file__).absolute().parents[2]), str(folder/'old-inputs.json'))
    return config

def release_env(root, fixture):
    m = load_operator(root)
    config = config_fixture(m, fixture)
    fake = fixture/'inert-compose.py'
    fake.write_text('import json,os,sys\nassert sys.argv[1:]==json.loads(os.environ["EXPECTED_ARGV"])\nassert "COMPOSE_FILE" not in os.environ and "DOCKER_HOST" not in os.environ\nprint("EXACT_ARGV_ENV_OK")\nraise SystemExit(int(os.environ["FIXTURE_EXIT"]))\n', encoding='utf-8')
    with patch.dict(os.environ, {'COMPOSE_FILE':'must-not-leak','DOCKER_HOST':'tcp://inert.invalid:2375'}):
        driver = m.DockerDriver(config, record_root=fixture/'commands')
    # Replace only the external executable with Python; the actual compose argv,
    # reviewed context/env/files, sanitized env and command exit handling execute.
    driver.docker = [sys.executable, str(fake), '--context', 'inert-local']
    count = 0
    for project in config['candidate']['projects']:
        for command in (['config','--quiet'], ['run','--rm','--no-deps','--pull','never','invoice-permissions'], ['up','-d','--no-deps','--pull','never','platform-api']):
            expected = ['--context','inert-local','compose','--project-name',project['name'],'--env-file',project['env_file']]
            for path in project['compose_files']: expected += ['-f',path]
            expected += command
            driver.env.update(EXPECTED_ARGV=json.dumps(expected), FIXTURE_EXIT='0')
            assert driver.compose(project, 'valid-'+str(count), command).stdout.strip() == b'EXACT_ARGV_ENV_OK'
            driver.env['FIXTURE_EXIT']='23'
            try: driver.compose(project, 'reject-'+str(count), command)
            except m.OperatorError: pass
            else: raise AssertionError('actual Compose consumer swallowed exit 23')
            count += 1
    for field in ('env_file','compose_files'):
        for bad in ('relative.env', str(fixture/'absent.env')):
            value = copy.deepcopy(config)
            value['candidate']['projects'][0][field] = [bad] if field == 'compose_files' else bad
            try: m.validate_config(value)
            except m.OperatorError: pass
            else: raise AssertionError('current config accepted a relative/missing '+field)
    for missing in ('host_nginx','input_snapshot'):
        value = copy.deepcopy(config)
        del (value if missing == 'host_nginx' else value['previous'])[missing]
        try: m.validate_config(value)
        except m.OperatorError: pass
        else: raise AssertionError('current config accepted missing '+missing)
    overlapping = copy.deepcopy(config)
    overlapping['candidate']['projects'][0]['compose_files'] = config['previous']['projects'][0]['compose_files']
    try: m.validate_config(overlapping)
    except m.OperatorError: pass
    else: raise AssertionError('candidate reused retained old Compose input')
    # The real old-input consumer must reject changed bytes, then accept the
    # original restoration. No capture or validation routine is patched.
    env = Path(config['previous']['projects'][0]['env_file']); original = env.read_bytes()
    try:
        env.write_bytes(original+b'PUBLIC_FIXTURE_DRIFT=true\n')
        try: m.validate_config(config)
        except m.OperatorError: pass
        else: raise AssertionError('changed retained old environment was accepted')
    finally: env.write_bytes(original)
    m.validate_config(config)
    print(f'RUNEARLY-02 unified actual compose consumer: {count} exact argv/env successes, {count} real child exit rejections, four path refusals; missing nginx/snapshot, retained-input overlap and byte drift rejected; no Docker.')

def current_migration_gate(root, fixture):
    m = load_operator(root)
    config = config_fixture(m, fixture)
    state = Path(config['state_root']); state.mkdir(parents=True,exist_ok=True)
    ledgers = {'platform':['p','r'],'invoice':['i']}; permissions=['r','m','t','s','f']
    (state/'deployment-record.json').write_text(json.dumps({'status':'PREPARED','snapshot':{'ledger_hashes':ledgers,'platform_permissions':permissions}}))
    count = 0
    for failure in (None,'migrate','invoice-migrate','invoice-permissions','ledger-query','extra-ledger','permissions-query','permissions-changed'):
        class Driver(m.DockerDriver):
            def compose(self, project, name, args, **kwargs):
                self.calls.append(args[-1])
                assert args == ['run','--rm','--no-deps','--pull','never',args[-1]]
                if args[-1] == failure: raise m.OperatorError('inert failed prerequisite')
            def ledger_snapshot(self, side):
                self.calls.append('ledger')
                if failure == 'ledger-query': raise m.OperatorError('inert failed ledger query')
                return {**ledgers,'invoice':['i','unapproved']} if failure=='extra-ledger' else ledgers
            def platform_permissions_snapshot(self, side):
                self.calls.append('permissions')
                if failure == 'permissions-query': raise m.OperatorError('inert failed permission query')
                return permissions+['extra-grant'] if failure=='permissions-changed' else permissions
        driver=Driver(config);driver.calls=[]
        try: driver.migrate_and_permissions()
        except m.OperatorError:
            assert failure is not None
        else: assert failure is None, 'current prerequisite/ledger/permission failure accepted: '+str(failure)
        order=['migrate','invoice-migrate','invoice-permissions','ledger','permissions']
        assert driver.calls == order[:len(driver.calls)], 'current prerequisite order changed'
        if failure is None: assert driver.calls == order
        if failure in order: assert driver.calls[-1] == failure, 'work continued after failed prerequisite'
        count += 1
    for changed in (['invoice-migrate','migrate','invoice-permissions'], ['migrate','invoice-migrate']):
        bad=copy.deepcopy(config);bad['candidate']['jobs']=[dict(project='new-unified',service=x) for x in changed]
        driver=Driver(bad);driver.calls=[]
        try: driver.migrate_and_permissions()
        except m.OperatorError: pass
        else: raise AssertionError('missing/reordered mandatory jobs accepted')
        assert driver.calls == [], 'invalid plan ran a prerequisite'
    print(f'RUNEARLY-04 unified real migrate_and_permissions: {count} success/failure scenarios plus two invalid-order refusals.')

def historical_financial_invariants(runbook,fixture):
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
    p=argparse.ArgumentParser()
    p.add_argument('--case',choices=['release-env','migration-gate'],required=True)
    p.add_argument('--fixture-root',type=Path,required=True)
    p.add_argument('--source-root',type=Path,default=PROJECT.parent)
    p.add_argument('--runbook',type=Path)
    a=p.parse_args();a.fixture_root.mkdir(parents=True,exist_ok=True)
    current=a.runbook or a.source_root/'docs/runbooks/UNIFIED-CUTOVER.md'
    assert 'deploy/rehearsal-unified/rehearse.sh' in current.read_text(encoding='utf-8'), 'active runbook must address unified D/E'
    if a.case=='release-env':release_env(a.source_root,a.fixture_root)
    else:
        current_migration_gate(a.source_root,a.fixture_root/'current')
        # Historical financial command blocks stay byte-for-byte controls only;
        # their mocked Docker prefixes are not a current deployment procedure.
        historical_financial_invariants((a.source_root/'invoice/docs/PRODUCTION-RUNBOOK.md').read_text(encoding='utf-8'),a.fixture_root/'financial')

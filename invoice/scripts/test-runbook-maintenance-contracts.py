"""Check documented operation sequences against their current consumer contracts.

Current unified orchestrators run against inert external boundaries; no Docker,
database, cryptographic operation or network call is made.
"""
from pathlib import Path
import argparse,re,shlex,subprocess,os
PROJECT=Path(__file__).resolve().parents[1]

def blocked_event(project):
    text=(project/'docs/PRODUCTION-RUNBOOK.md').read_text(encoding='utf-8')
    section=text.split('> **`blocked` does not mean',1)[1].split('\n\n',1)[0]
    source=(project/'backend/internal/postgresstore/eligibility_operations.go').read_text(encoding='utf-8')
    statuses=re.search(r'eligibilityFreezeBlockingEventStatuses = \[\]string\{([^}]+)\}',source).group(1)
    blocked=set(re.findall(r'"([a-z_]+)"',statuses))
    ack=(project/'backend/internal/postgresstore/ingest_unreplayable_acknowledge.go').read_text(encoding='utf-8')
    terminal=re.search(r"UPDATE source_ingest_events SET processing_status='([^']+)'",ack).group(1)
    state='dead';resolved=False;preview=False
    for command in re.findall(r'`([^`]+)`',section.replace('\n> ',' ')):
        if command.startswith('invoice_eligibility_repair '):
            args=shlex.split(command)
            if '--kind=ingest-acknowledge-unreplayable' in args:
                assert any(a.startswith('--event=') for a in args), 'acknowledgement command omitted required event filter'
                if '--apply' in args:
                    assert preview and any(a.startswith('--operator-id=') for a in args), 'acknowledgement apply skipped preview/operator prerequisite'
                    state=terminal
                else:preview=True
        # The historical internal route is mounted externally under /invoice-api/v1.
        if command.startswith(('POST /invoice-api/v1/admin/eligibility-freezes/','POST /api/v1/admin/eligibility-freezes/')):
            assert state not in blocked, 'documented resolve reaches current dead-event guard before acknowledgement'
            resolved=True
    assert resolved,'blocked-event procedure has no final guarded resolution step'
    print('INV-DOC-03 documented dry-run/apply/resolve order satisfies extracted CLI and dead-event prerequisites; no production execution.')

def unified_order(project, source_root):
    import copy, importlib, json, sys, tempfile
    from types import SimpleNamespace
    from unittest.mock import patch
    scripts=source_root/'deploy/rehearsal-unified'
    sys.path.insert(0,str(scripts))
    m=importlib.import_module('lifecycle');r=importlib.import_module('restore');pre=importlib.import_module('preflight')
    book=(source_root/'docs/runbooks/UNIFIED-CUTOVER.md').read_text(encoding='utf-8')
    for entry in ('deploy/rehearsal-unified/rehearse.sh','deploy/unified/cutover.sh','deploy/unified/rollback.sh'):
        assert entry in book,'active runbook omitted unified entry '+entry
    # Operate actual current orchestrators; only Docker/HTTP/filesystem side-effect
    # boundaries are inert. RC110 is historical and is never read as a procedure.
    source_roles=sorted(kind+'-'+stream for kind in ('sub2api','newapi')
                        for stream in ('payments','identities','usage','credits','balances'))
    source_checks=['verify-restored-state-'+role for role in source_roles]
    def restore_case(failure=None):
        with tempfile.TemporaryDirectory(prefix='unified-order-') as temp:
            base=Path(temp);calls=[];records=[]
            def step(name):
                calls.append(name)
                if name==failure:raise m.OperatorError('inert failed '+name)
            value=dict(tools_image='sha256:'+'a'*64,archive_tmpfs_bytes=134217728,
                       verification_jobs=[dict(project='inert',service='verify-invoice-restore')])
            source_project=dict(kind='sources',name='inert-frozen-sources',
                                compose_files=[str(base/'sources.compose.json')],env_file=str(base/'sources.env'),
                                services={'service-'+role:dict(role=role,image_id='sha256:'+'c'*64) for role in source_roles})
            main_project=dict(kind='unified',name='inert-frozen-unified',
                              compose_files=[str(base/'unified.compose.json')],env_file=str(base/'unified.env'),
                              services={'platform-api':dict(role='platform-api',image_id='sha256:'+'d'*64),
                                        'postgres':dict(role='platform-postgres',image_id='sha256:'+'e'*64)})
            class Trial:
                def __init__(self):
                    self.state=base;self.output=base/'output';self.docker=['INERT-DOCKER']
                    self.config=dict(mode='local-synthetic',candidate=dict(head='a'*40,manifest_sha256='b'*64,manifest=str(base/'manifest.json')),backups={},approvals=dict(max_age_hours=24))
                def projects(self,which):
                    assert which=='candidate'
                    return [main_project,source_project]
                def compose(self,project,name,args):
                    if name=='prepare-restored-source-networks':
                        assert project is main_project,'network creation escaped the owning main project'
                        assert args==['create','--no-build','--no-recreate','--pull','never','platform-api'],('API must be created without starting',args)
                        calls.append(name)
                        return SimpleNamespace(returncode=23 if name==failure else 0,stdout=b'')
                    assert project is source_project,'state check escaped the frozen source project'
                    assert name in source_checks,'unexpected external Compose operation'
                    role=name.removeprefix('verify-restored-state-')
                    assert args==['run','--rm','--no-deps','--pull','never','--entrypoint','/source-agent-prod','service-'+role,'check-state'],('state checker command changed',args)
                    calls.append(name)
                    # Return a genuine nonzero boundary result so the real
                    # verify_frozen_source_state consumer must reject it.
                    return SimpleNamespace(returncode=23 if name==failure else 0,stdout=b'')
                def artifact_preflight(self):
                    step('artifact')
                    self.operator_source={'fixture':'inert artifact-preflight boundary'}
                def command(self,*args):return SimpleNamespace(stdout=b'8589934592')
                def start_new_databases(self):step('start-databases')
                def jobs(self,jobs,prefix):
                    assert [j['service'] for j in jobs]==['verify-invoice-restore']
                    step('verify-document-source-job')
                def migrate_and_permissions(self):step('permissions')
                def start_new(self):step('start-new')
                def check_new(self):step('readiness')
                def smoke(self):step('smoke')
                def inventory(self,*args):step('inventory');return []
            trial=Trial()
            proof=dict(status='PASS',exit_code=0,mode='local-synthetic',qualification_scope='local-synthetic-host-preflight',
                       checks=[dict(name=n,status='PASS') for n in ('source-versions','host-mode-guards','network-addressing','compose-environment')],inherited_server_checks_requires_server=['inert server boundary'])
            def captured_json(path,data):records.append((Path(path).name,copy.deepcopy(data)))
            with patch.object(r,'rehearsal_driver',return_value=(trial,value)),patch.object(r,'invalidate_receipt',side_effect=lambda *_:step('invalidate-prior-pass')),\
                 patch.object(pre,'run',return_value=proof),patch.object(r,'read_public_json',return_value={'images':[{'name':'invoice-tools','imageId':value['tools_image']}]}),\
                 patch.object(r,'verify_backups',side_effect=lambda *_:step('signatures')),patch.object(r,'validate_frozen_mounts',side_effect=lambda *_:step('mounts')),\
                 patch.object(r,'create_frozen_volumes',side_effect=lambda *_:step('create-volumes')),\
                 patch.object(r,'extract_archive',side_effect=lambda d,v,domain,kind:step('extract-'+domain+'-'+kind)),\
                 patch.object(r,'restore_database',side_effect=lambda d,v,domain:step('restore-'+domain)),\
                 patch.object(r,'verify_snapshot_metadata',side_effect=lambda *_:step('metadata-match')),\
                 patch.object(r,'cleanup',side_effect=lambda *_:step('cleanup')),patch.object(r,'atomic_json',side_effect=captured_json):
                try:result=r.rehearse(trial)
                except m.OperatorError:assert failure is not None,'valid current D sequence unexpectedly rejected'
                else:
                    assert failure is None,'failed restore stage returned success: '+str(failure)
                    assert result['status']=='PASS' and result['cleanup_complete'] is True and result['exit_code']==0
                    assert result['actual_operator_source']==trial.operator_source
                    assert result['restored_source_state']==[dict(role=role,exit_code=0) for role in source_roles]
            expected=['invalidate-prior-pass','artifact','signatures','mounts','create-volumes','extract-invoice-documents','extract-invoice-source_state','extract-invoice-metadata','extract-platform-metadata','start-databases','restore-platform','restore-invoice','metadata-match','verify-document-source-job','prepare-restored-source-networks',*source_checks,'permissions','start-new','readiness','smoke','inventory','cleanup']
            if failure is None:assert calls==expected,('D current sequence changed',calls)
            else:
                assert failure in calls
                reached=expected[:expected.index(failure)+1]
                # A resource-free early failure does not trigger resource deletion;
                # once create succeeded, only mandatory cleanup may follow failure.
                if expected.index(failure)>expected.index('create-volumes') and failure!='cleanup':reached+=['cleanup']
                assert calls==reached,('work continued after failed D prerequisite',failure,calls)
                assert not any(name=='rehearsal-pass.json' and row.get('status')=='PASS' for name,row in records)
                assert records[-1][1]['status']=='FAIL' and records[-1][1]['exit_code']==1
    failures=['artifact','signatures','create-volumes','extract-invoice-documents','restore-platform','metadata-match','verify-document-source-job','prepare-restored-source-networks','permissions','readiness','smoke','cleanup']
    restore_case()
    for failure in failures+source_checks:restore_case(failure)

    class Cutover:
        def __init__(self,failure=None):self.failure=failure;self.calls=[];self.records=[]
        def step(self,name):
            self.calls.append(name)
            if name==self.failure:raise m.OperatorError('inert '+name)
        def preflight(self):self.step('signed-preflight')
        def precheck_new(self):self.step('isolated-candidate-preview')
        def snapshot(self):self.step('snapshot');return {'actual_invoice_containers':18}
        def stop_old(self):self.step('stop-old')
        def start_new_databases(self):self.step('start-databases')
        def migrate_and_permissions(self):self.step('permissions')
        def start_new(self):self.step('start-new')
        def check_new(self):self.step('readiness')
        def switch_nginx(self,snapshot):
            assert snapshot=={'actual_invoice_containers':18}
            self.step('switch-nginx')
        def smoke(self):self.step('smoke')
        def stop_new(self):self.step('stop-new')
        def restore_permissions(self):self.step('restore-permissions')
        def start_old(self):self.step('start-old')
        def check_old(self,*args):self.step('check-old')
        def restore_nginx(self,snapshot):
            assert snapshot=={'actual_invoice_containers':18}
            self.step('restore-nginx')
        def record(self,row):self.records.append(copy.deepcopy(row))
    expected=['signed-preflight','isolated-candidate-preview','snapshot','stop-old','start-databases','permissions','start-new','readiness','switch-nginx','smoke']
    for failure in (None,'signed-preflight','isolated-candidate-preview','permissions','readiness','switch-nginx','smoke'):
        trial=Cutover(failure)
        try:result=m.cutover(trial)
        except m.OperatorError:assert failure is not None
        else:assert failure is None and result['status']=='COMMITTED'
        if failure is None:assert trial.calls==expected
        elif failure in ('signed-preflight','isolated-candidate-preview'):
            assert trial.calls==expected[:expected.index(failure)+1] and trial.records[-1]['status']=='PREFLIGHT_FAILED'
        else:
            assert trial.calls==expected[:expected.index(failure)+1]+['stop-new','restore-permissions','start-old','check-old','restore-nginx']
            assert trial.records[-1]['status']=='ROLLED_BACK' and trial.records[-1]['exit_code']==1
    print('INV-DOC-04 actual unified D 23 cases and E 7 cases: signatures/restore/metadata/document verification/stopped API create/ten native check-state commands/permissions/readiness/smoke/cleanup ordering and per-stream nonzero propagation; isolated preview before old stop and nginx switch/restore ordering; no Docker/HTTP/key operation.')

if __name__=='__main__':
    p=argparse.ArgumentParser();p.add_argument('--case',choices=['blocked-event','shadow-order','unified-order'],required=True)
    p.add_argument('--project',type=Path,default=PROJECT);p.add_argument('--source-root',type=Path)
    a=p.parse_args()
    if a.case=='blocked-event':blocked_event(a.project)
    else:unified_order(a.project,a.source_root or a.project.parent)

"""Check documented operation sequences against their current consumer contracts.

This models only prerequisite states read from source; it never executes a
maintenance tool, database query, signature operation, or network call.
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
        if command.startswith('POST /api/v1/admin/eligibility-freezes/'):
            assert state not in blocked, 'documented resolve reaches current dead-event guard before acknowledgement'
            resolved=True
    assert resolved,'blocked-event procedure has no final guarded resolution step'
    print('INV-DOC-03 documented dry-run/apply/resolve order satisfies extracted CLI and dead-event prerequisites; no production execution.')

def shadow_order(project):
    text=(project/'docs/PRODUCTION-RUNBOOK.md').read_text(encoding='utf-8')
    template=text.split('**RC plan template step:**',1)[1].split('## 12. Rollback',1)[0]
    # The prose's explicit phase placement becomes the input to a dependency
    # check, rather than accepting the presence of a "shadow" keyword.
    if 'before the tag is created' in template:
        order=['source gate','shadow evaluation','signed tag','image gate','verified image load','roll-forward']
    else:
        declaration=re.search(r'(?m)^Placement: (.+)\.$',template)
        assert declaration, 'RC template must declare its operation placement'
        order=[x.strip() for x in declaration.group(1).split(' → ')]
    historical=(project/'docs/handoffs/RELEASE-RC110.md').read_text(encoding='utf-8')
    history=historical.split('## 顺序',1)[1].split('## 补记',1)[0]
    assert history.index('tag')<history.index('镜像门禁')<history.index('stage2')<history.index('影子评估')<history.index('roll-forward.sh'),'historical release order source changed; review explicit scope'
    script=(project/'deploy/rehearsal/shadow-eval.sh').read_text(encoding='utf-8')
    inspect=re.search(r'(?m)^docker image inspect "\$tools_image" "\$postgres_image" >/dev/null$',script)
    assert inspect, 'shadow image-availability consumer changed; review test scope'
    # Execute only that side-effect-free entry guard with a Docker function that
    # refuses images until the documented load step. No shadow script executes.
    bash='D:/Git/bin/bash.exe' if os.name=='nt' else 'bash'
    state='';seen=[]
    for step in order:
        if step=='image gate':assert 'signed tag' in seen,'image gate precedes signed source identity'
        if step=='verified image load':assert 'image gate' in seen,'load precedes reviewed image generation';state='yes'
        if step=='shadow evaluation':
            guard='docker() { [[ "$AVAILABLE" == yes ]]; };\ntools_image=inert-tools; postgres_image=inert-pg;\n'+inspect.group(0)
            p=subprocess.run([bash,'--noprofile','--norc','-c',guard],env=dict(os.environ,AVAILABLE=state),capture_output=True,text=True)
            assert p.returncode==0,'template runs shadow before its required candidate images are loaded'
        if step=='roll-forward':assert 'shadow evaluation' in seen,'template allows deployment before shadow gate'
        seen.append(step)
    assert set(seen)=={'source gate','signed tag','image gate','verified image load','shadow evaluation','roll-forward'},'RC template omits a required existing stage'
    print('INV-DOC-04 placement matches historical release dependencies and extracted image-availability gate; no production command executed.')

def keycloak_identity(project):
    doc=(project/'docs/PRODUCTION-RUNBOOK.md').read_text(encoding='utf-8')
    section=doc.split('The optimized image uses a 2 GiB memory limit.',1)[1].split('The maintenance wrapper encrypts',1)[0]
    row=re.search(r'\| `SOURCE_TAG` \| `([^`]+)` \|',section)
    if row:
        tag=row.group(1);image=re.search(r'\| `KEYCLOAK_IMAGE.config_image` \| `([^`]+)` \|',section).group(1)
    else:
        rc=re.search(r'The (RC\d+) installation must',section).group(1).lower()
        tag='v0.1.0-'+rc+'-signed';image='invoice-keycloak:0.1.0-'+rc
    src=(project/'deploy/keycloak/invite-permanent-master-admin.sh').read_text(encoding='utf-8')
    declaration=re.search(r"(?m)^readonly EXPECTED_SOURCE_TAG=.*$",src).group(0)
    tag_guard=re.search(r'(?m)^\[\[ "\$SOURCE_TAG" == "\$EXPECTED_SOURCE_TAG" \]\].*$',src).group(0)
    image_guard=re.search(r'(?m)^\[\[ "\$KEYCLOAK_CONFIG_IMAGE" == .*$',src).group(0)
    bash='D:/Git/bin/bash.exe' if os.name=='nt' else 'bash'
    script='set -eu\ndie() { echo "$*" >&2; exit 1; };\n'+declaration+'\n'+tag_guard+'\n'+image_guard
    env=dict(os.environ,SOURCE_TAG=tag,KEYCLOAK_CONFIG_IMAGE=image);env.pop('BASH_ENV',None);env.pop('ENV',None)
    p=subprocess.run([bash,'--noprofile','--norc','-c',script],env=env,capture_output=True,text=True)
    assert p.returncode==0,'documented Keycloak identity is rejected by the unchanged operator: '+p.stderr
    print('INV-AUX-007 documented historical tag/image accepted by extracted unchanged RC38 identity predicates; no operator/key operation.')

if __name__=='__main__':
    p=argparse.ArgumentParser();p.add_argument('--case',choices=['blocked-event','shadow-order','keycloak-identity'],required=True);p.add_argument('--project',type=Path,default=PROJECT)
    a=p.parse_args();{'blocked-event':blocked_event,'shadow-order':shadow_order,'keycloak-identity':keycloak_identity}[a.case](a.project)

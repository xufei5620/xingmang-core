"""Check documented operation sequences against their current consumer contracts.

This models only prerequisite states read from source; it never executes a
maintenance tool, database query, signature operation, or network call.
"""
from pathlib import Path
import argparse,re,shlex
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

if __name__=='__main__':
    p=argparse.ArgumentParser();p.add_argument('--case',choices=['blocked-event'],required=True);p.add_argument('--project',type=Path,default=PROJECT)
    a=p.parse_args();blocked_event(a.project)

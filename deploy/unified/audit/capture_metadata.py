"""Operator-invoked read-only captures. No credentials are accepted as CLI values."""
import argparse
import datetime as dt
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
from identity_audit import assess,exact_https,read_json,role_map


def main():
    parser=argparse.ArgumentParser()
    for name in ['platform-service','invoice-service','role-map','staff-origin','crosswalk','output-directory','psql']:
        parser.add_argument('--'+name,required=True)
    args=parser.parse_args()
    output=None
    try:
        if any(not re.fullmatch('[A-Za-z0-9_-]{1,64}',s) for s in [args.platform_service,args.invoice_service]) or args.platform_service==args.invoice_service:
            raise ValueError('SERVICE_REFERENCES_REQUIRED')
        exact_https(args.staff_origin,True)
        roles=role_map(read_json(args.role_map));mapping=read_json(args.crosswalk)
        output=Path(args.output_directory);output.mkdir(parents=True,exist_ok=False)
        root=Path(__file__).resolve().parent
        env=dict(os.environ)
        # psql/libpq reads caller-owned PGSERVICEFILE/PGPASSFILE; this program never reads their bytes.
        env['PGOPTIONS']='-c default_transaction_read_only=on -c statement_timeout=30000'
        env['PGCONNECT_TIMEOUT']='10'
        snapshots={};records=[]
        for name,service,sqlfile in [('platform',args.platform_service,'staff-mfa.sql'),('invoice',args.invoice_service,'invoice-actors.sql')]:
            command=[args.psql,'-X','-qAt','--no-password','--dbname','service='+service,'-v','ON_ERROR_STOP=1','-f',str(root/sqlfile)]
            if name=='platform':command+=['-v','role_scope_map_json='+json.dumps(roles,separators=(',',':'))]
            start=dt.datetime.now(dt.timezone.utc).isoformat()
            p=subprocess.run(command,capture_output=True,env=env)
            record={'component':name,'serviceReference':service,'startUtc':start,'endUtc':dt.datetime.now(dt.timezone.utc).isoformat(),'exitCode':p.returncode,'sqlSha256':hashlib.sha256((root/sqlfile).read_bytes()).hexdigest()}
            records.append(record)
            (output/'capture-execution.json').write_text(json.dumps(records,indent=2))
            if p.returncode:
                # Connection errors can echo service data. Do not archive or print stderr.
                raise ValueError('READONLY_CAPTURE_FAILED')
            raw=p.stdout
            value=json.loads(raw)
            snapshots[name]=value
            (output/(name+'.json')).write_bytes(raw)
            record['snapshotSha256']=hashlib.sha256(raw).hexdigest()
        result=assess(snapshots['platform'],snapshots['invoice'],mapping,args.staff_origin)
        result['input_sha256']={name:hashlib.sha256((output/(name+'.json')).read_bytes()).hexdigest() for name in ['platform','invoice']}
        result['input_sha256'].update(role_map=hashlib.sha256(Path(args.role_map).read_bytes()).hexdigest(),crosswalk=hashlib.sha256(Path(args.crosswalk).read_bytes()).hexdigest())
        (output/'result.json').write_text(json.dumps(result,indent=2))
        (output/'capture-execution.json').write_text(json.dumps(records,indent=2))
        print('Read-only capture complete; result.json contains metadata and explicit unresolved conditions.')
        return 2 if result['blockers'] else 0
    except (ValueError,OSError,TypeError,KeyError):
        print('Identity audit rejected input or capture; no identity changes were attempted. No connection details are printed.')
        return 1


if __name__=='__main__':raise SystemExit(main())

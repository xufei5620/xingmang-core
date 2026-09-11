"""Only synthetic spool paths and extracted network membership predicates run."""
import argparse, os, pathlib, re, shutil, subprocess, tempfile
parser=argparse.ArgumentParser()
parser.add_argument('--root',type=pathlib.Path,default=pathlib.Path(__file__).resolve().parents[3])
parser.add_argument('--case')
args=parser.parse_args()
bash=os.environ.get('TEST_BASH') or shutil.which('bash')
root=pathlib.Path(__file__).resolve().parents[2]/'release/auxiliary-test-fixtures'
root.mkdir(parents=True,exist_ok=True)
fixture=pathlib.Path(tempfile.mkdtemp(prefix='collections-',dir=root))
spool=(args.root/'invoice/deploy/check-pending-spools.sh').read_text(encoding='utf-8-sig')
network=(args.root/'invoice/deploy/provision-projection-networks.sh').read_text(encoding='utf-8-sig')
match=re.search(r'(?ms)^validate_members\(\) \{.*?^\}',network)
assert match,'production validate_members missing'
cases=[
 ('spool-empty', 'find(){ :; }\n'+spool,0,b'PENDING-SPOOLS-EMPTY'),
 ('spool-present','find(){ printf "fixture/pending.enc\\t10\\n"; }\n'+spool,1,b'PENDING-SPOOLS-PRESENT'),
 ('spool-find-error','find(){ return 73; }\n'+spool,2,b'unable to scan'),
 ('members-allowed','docker(){ printf "fixture-db\\n"; }\n'+match.group()+'\nvalidate_members fixture-net fixture fixture-db\nprintf accepted\n',0,b'accepted'),
 ('members-unexpected','docker(){ printf "unexpected-peer\\n"; }\n'+match.group()+'\nvalidate_members fixture-net fixture fixture-db\nprintf accepted\n',1,b'unexpected container'),
 ('members-query-error','docker(){ return 74; }\n'+match.group()+'\nvalidate_members fixture-net fixture fixture-db\nprintf accepted\n',74,b''),
]
failures=[]
for case,source,expected,marker in cases:
 if args.case and args.case!=case: continue
 path=fixture/(case+'.sh'); path.write_text('set -Eeuo pipefail\n'+source,encoding='utf-8',newline='\n')
 result=subprocess.run([bash,path.as_posix(),'--state-root',fixture.as_posix()],capture_output=True)
 combined=result.stdout+result.stderr
 if result.returncode!=expected or marker not in combined or (case.endswith('error') and (b'accepted' in result.stdout or b'PENDING-SPOOLS-EMPTY' in result.stdout)):
  failures.append(f'{case}: expected exit {expected} and predicate marker, got {result.returncode}')
 else: print('PASS '+case)
if failures: raise AssertionError('\n'.join(failures))

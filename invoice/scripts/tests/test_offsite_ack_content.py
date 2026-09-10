"""Run only the invitation consumer's jq binding predicate, never maintenance."""
import argparse,os,pathlib,re,shutil,subprocess,tempfile
parser=argparse.ArgumentParser()
parser.add_argument('--root',type=pathlib.Path,default=pathlib.Path(__file__).resolve().parents[3])
parser.add_argument('--case')
args=parser.parse_args()
source=(args.root/'invoice/deploy/keycloak/invite-permanent-master-admin.sh').read_text(encoding='utf-8-sig')
match=re.search(r'(?ms)^jq -\w+ --arg record "\$record_id".*?\|\| die \'off-site acknowledgement content is invalid\'',source)
assert match,'off-site consumer content gate missing'
home=pathlib.Path(__file__).resolve().parents[2]/'release/auxiliary-test-fixtures'
home.mkdir(parents=True,exist_ok=True)
fixture=pathlib.Path(tempfile.mkdtemp(prefix='ack-content-',dir=home))
record='fixture-record'; digest='a'*64
valid=[f'record_id={record}',f'backup_manifest_sha256={digest}','verified_at_utc=2026-09-10T00:00:00Z']
cases=[('valid',valid,0),('wrong-record',['record_id=other',*valid[1:]],1),('wrong-hash',[valid[0],'backup_manifest_sha256='+'b'*64,valid[2]],1),('wrong-lines',[*valid,'extra=value'],1),('wrong-time',[*valid[:2],'verified_at_utc=invalid'],1)]
use_wsl=os.name=='nt' and not shutil.which('jq')
def shellpath(path):
 return '/mnt/'+path.drive[0].lower()+path.as_posix()[2:] if use_wsl else path.as_posix()
failures=[]
for case,lines,expected in cases:
 if args.case and args.case!=case: continue
 ack=fixture/(case+'.txt'); ack.write_text('\n'.join(lines)+'\n',encoding='utf-8',newline='\n')
 script=fixture/(case+'.sh')
 script.write_text('set -euo pipefail\ndie(){ echo rejected >&2; exit 1; }\nrecord_id='+record+'\nbackup_manifest_hash='+digest+'\noffsite_ack="'+shellpath(ack)+'"\n'+match.group()+'\nprintf accepted\n',encoding='utf-8',newline='\n')
 command=['wsl.exe','--','bash',shellpath(script)] if use_wsl else [os.environ.get('TEST_BASH') or shutil.which('bash'),shellpath(script)]
 result=subprocess.run(command,capture_output=True)
 if result.returncode!=expected or result.stdout!=(b'accepted' if expected==0 else b''): failures.append(f'{case}: expected exit {expected} and consumer decision, got {result.returncode}')
 else: print('PASS '+case)
if failures: raise AssertionError('\n'.join(failures))

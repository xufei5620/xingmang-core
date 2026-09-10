"""Exercise the existing plan predicate on text only; no SQL or operator runs."""
import argparse,os,pathlib,re,shutil,subprocess,tempfile
parser=argparse.ArgumentParser()
parser.add_argument('--root',type=pathlib.Path,default=pathlib.Path(__file__).resolve().parents[3])
parser.add_argument('--case')
args=parser.parse_args()
source=(args.root/'invoice/deploy/postgres/verify-source-readiness-index.sh').read_text(encoding='utf-8-sig')
match=re.search(r'(?ms)^assert_plan\(\) \{.*?^\}',source)
assert match,'production assert_plan missing'
home=pathlib.Path(__file__).resolve().parents[2]/'release/auxiliary-test-fixtures'; home.mkdir(parents=True,exist_ok=True)
fixture=pathlib.Path(tempfile.mkdtemp(prefix='readiness-plan-',dir=home))
index='source_ingest_events_readiness_active_idx'
good='Index Scan using '+index+' on public.source_ingest_events\n'
cases=[('allowed-index',good,0),('missing-index','Result\n',1),('sequential-scan',good+'Seq Scan on public.source_ingest_events\n',1),('parallel-sequential-scan',good+'Parallel Seq Scan on source_ingest_events\n',1)]
script=fixture/'assert-plan.sh'; script.write_text('set -euo pipefail\nINDEX_NAME='+index+'\ndie(){ printf rejected >&2; exit 1; }\n'+match.group()+'\nassert_plan "$1"\n',encoding='utf-8',newline='\n')
failures=[]
for case,plan,expected in cases:
 if args.case and args.case!=case: continue
 path=fixture/(case+'.txt'); path.write_text(plan,encoding='utf-8',newline='\n')
 # Relative fixture paths work with native Bash and Windows' WSL bash launcher.
 result=subprocess.run([os.environ.get('TEST_BASH') or shutil.which('bash'),script.name,path.name],cwd=fixture,capture_output=True)
 if result.returncode!=expected: failures.append(f'{case}: expected plan exit {expected}, got {result.returncode}')
 else: print('PASS '+case)
if failures: raise AssertionError('\n'.join(failures))

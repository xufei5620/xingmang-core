"""Extract the EXIT finalizer; all inputs are owned synthetic local evidence."""
import argparse,hashlib,os,pathlib,re,shutil,subprocess,tempfile
parser=argparse.ArgumentParser()
parser.add_argument('--root',type=pathlib.Path,default=pathlib.Path(__file__).resolve().parents[3])
parser.add_argument('--case')
args=parser.parse_args()
source=(args.root/'invoice/deploy/postgres/apply-source-readiness-index-concurrently.sh').read_text(encoding='utf-8-sig')
function=re.search(r'(?ms)^finalize_record\(\) \{.*?^\}',source)
assert function,'production finalizer missing'
home=pathlib.Path(__file__).resolve().parents[2]/'release/auxiliary-test-fixtures'
home.mkdir(parents=True,exist_ok=True)
fixture=pathlib.Path(tempfile.mkdtemp(prefix='finalizer-',dir=home))
bash=os.environ.get('TEST_BASH') or shutil.which('bash')
failures=[]
for case in ['healthy','compose-hash','env-hash','migration-hash','result-write','manifest-write','permission','sync','incoming-failure']:
 if args.case and args.case!=case: continue
 record=fixture/case; record.mkdir()
 for name in ['compose.synthetic','env.synthetic','schema-migrations-after.tsv']:
  (record/name).write_text('synthetic evidence\n',encoding='utf-8')
 setup='''set -Eeuo pipefail
RECORD_DIR="$1"; RECORD_ROOT="$1"; COMPOSE_FILE="$1/compose.synthetic"; PRODUCTION_ENV_FILE="$1/env.synthetic"
OPERATION_STATUS=passed; START_UTC=2026-09-10T00:00:00Z; START_SECONDS=$SECONDS
FAILURE_LINE=0; INDEX_ACTION=fixture; ACTUAL_OPERATOR_SHA256=fixture; ACTUAL_VERIFIER_SHA256=fixture
sha256sum(){
  case "$FAULT:$1" in compose-hash:*/compose.synthetic|env-hash:*/env.synthetic|migration-hash:*/schema-migrations-after.tsv) return 72;; esac
  command sha256sum "$@"
}
cat(){
  if [[ "$FAULT" == result-write && ! -e "$RECORD_DIR/result-fault-used" ]]; then touch "$RECORD_DIR/result-fault-used"; return 73; fi
  command cat "$@"
}
xargs(){
  if [[ "$FAULT" == manifest-write && ! -e "$RECORD_DIR/manifest-fault-used" ]]; then touch "$RECORD_DIR/manifest-fault-used"; return 74; fi
  command xargs "$@"
}
chmod(){ if [[ "$FAULT" == permission ]]; then return 75; fi; }
sync(){ if [[ "$FAULT" == sync ]]; then return 76; fi; }
'''
 script=record/'probe.sh'; script.write_text(setup+function.group()+'\ntrap finalize_record EXIT\nexit '+('37' if case=='incoming-failure' else '0')+'\n',encoding='utf-8',newline='\n')
 result=subprocess.run([bash,script.as_posix(),record.as_posix()],env=os.environ|{'FAULT':case},capture_output=True)
 expected=0 if case=='healthy' else (37 if case=='incoming-failure' else 1)
 evidence=(record/'result.env').read_text(encoding='utf-8') if (record/'result.env').exists() else ''
 status='status=passed' if case=='healthy' else 'status=failed'
 if result.returncode!=expected or status not in evidence.splitlines(): failures.append(f'{case}: expected exit {expected} and {status}, got {result.returncode}')
 else: print('PASS '+case)
if failures: raise AssertionError('\n'.join(failures))

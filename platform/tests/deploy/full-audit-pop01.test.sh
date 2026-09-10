#!/usr/bin/env bash
# POP-01: monorepo/standalone fixtures; no network, server, real Docker or database.
set -Eeuo pipefail
root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd -P)"
python3 - "$root" <<'PY'
from pathlib import Path
import subprocess, os, tempfile, shutil, hashlib, re
source=Path(os.environ.get('AUDIT_SOURCE',__import__('sys').argv[1]))
work=Path(tempfile.mkdtemp());fail=[]
def write(p,s):
 p.parent.mkdir(parents=True,exist_ok=True);p.write_text(s);p.chmod(0o755)
def cmd(args,cwd,env=None):
 e=os.environ.copy();e.update({'GIT_CONFIG_NOSYSTEM':'1','GIT_CONFIG_GLOBAL':'/dev/null','GIT_ALLOW_PROTOCOL':'file','GIT_OPTIONAL_LOCKS':'0','GIT_TERMINAL_PROMPT':'0'});e.update(env or {})
 return subprocess.run(args,cwd=cwd,env=e,text=True,capture_output=True,timeout=30)
def setup(args,cwd):
 r=cmd(args,cwd)
 if r.returncode:raise RuntimeError('fixture setup failed: '+r.stderr)
 return r.stdout.strip()
def check(name,r,predicate=lambda r:True):
 if r.returncode or not predicate(r):
  fail.append(name);print('FAIL POP-01: '+name);print(r.stderr[-400:])
files=['deploy/scripts/deploy.sh','deploy/scripts/promote.sh','deploy/scripts/deploy-local.sh','deploy/git-hooks/pre-receive','deploy/git-hooks/post-receive','scripts/ci-local.sh','scripts/verify-real-mode.sh','scripts/dev/worktree-testdb.sh']
for layout in ['monorepo','sparse','standalone']:
 case=work/layout;repo=case/'checkout';project=repo if layout=='standalone' else repo/'platform'
 repo.mkdir(parents=True);bindir=case/'bin';bindir.mkdir();trace=case/'trace'
 for f in files:write(project/f,(source/f).read_text())
 write(project/'go.mod','module fixture\n');write(project/'cmd/migrate/main.go','package main\n');write(project/'db/migrations/001.sql','-- fixture\n')
 for f in ['launch.yaml','server-staging.yaml','server-prod.yaml']:write(project/'deploy/compose'/f,'services: {}\n')
 write(project/'deploy/compose/.env','ENVIRONMENT=staging\n');write(repo/'.gitignore','**/.env\n')
 setup(['git','init','-q','-b','release/v0.1-launch',str(repo)],case)
 setup(['git','config','user.name','fixture'],repo);setup(['git','config','user.email','fixture@example.invalid'],repo)
 setup(['git','add','.'],repo);setup(['git','commit','-qm','fixture'],repo);sha=setup(['git','rev-parse','HEAD'],repo)
 if layout=='sparse':
  setup(['git','sparse-checkout','init','--cone'],repo)
  setup(['git','sparse-checkout','set','platform'],repo)
  # Initialize the untracked test-only configuration after the layout is selected.
  write(project/'deploy/compose/.env','ENVIRONMENT=staging\n')
 setup(['git','branch','main'],repo);bare=case/'repo.git';setup(['git','clone','--bare','-q',str(repo),str(bare)],case)
 setup(['git','remote','add','origin',str(bare)],repo)
 for key,val in [('receive.denyNonFastForwards','true'),('receive.denyDeletes','true')]:setup(['git','--git-dir='+str(bare),'config',key,val],case)
 write(bare/'hooks/pre-receive',(project/'deploy/git-hooks/pre-receive').read_text())
 status=case/'status';status.mkdir();write(status/(sha+'.status'),'green\n')
 env={'PATH':str(bindir)+':/usr/bin:/bin','FIXTURE_PROJECT':str(project),'FIXTURE_ROOT':str(work),'TRACE':str(trace),'XM_DEPLOY_TEST_MODE':'1'}
 write(bindir/'docker',r'''#!/bin/bash
printf '%s\n' "$*" >> "${TRACE:?}"
if [ "$1" = compose ]; then
 prev=''; directory=''; first_file=''
 for arg in "$@"; do
  [ "$prev" != --project-directory ] || directory="$arg"
  if [ "$prev" = --file ] && [ -z "$first_file" ]; then first_file="$arg"; fi
  prev="$arg"
 done
 [ "$(dirname "$first_file")" = "${FIXTURE_PROJECT:?}/deploy/compose" ] || exit 91
 [ "$directory" = "${FIXTURE_PROJECT:?}/deploy/compose" ] || exit 92
 [ "$(cd "$directory/../.." && pwd)" = "$FIXTURE_PROJECT" ] || exit 93
fi
exit 0
''')
 write(bindir/'curl','#!/bin/bash\nexit 0\n')
 write(bindir/'stat','#!/bin/bash\nif [ "$1" = -c ] && [ "$2" = %a ]; then echo 600; else /usr/bin/stat "$@"; fi\n')
 write(bindir/'rm','#!/bin/bash\nfor arg in "$@"; do case "$arg" in -*) ;; "${FIXTURE_ROOT:?}"/*) ;; *) exit 94;; esac; done\nexec /bin/rm "$@"\n')
 r=cmd(['bash',str(project/'deploy/scripts/deploy.sh'),'staging','--test-mode','--repo',str(repo),'--status-dir',str(status),'--audit-log',str(case/'audit'),'--docker-bin',str(bindir/'docker'),'--curl-bin',str(bindir/'curl'),'--reason','fixture'],case,env)
 check(layout+' deploy resolves project and Compose context',r)
 # Production selection and the existing three file whitelists, without executing deployment.
 deploy=(project/'deploy/scripts/deploy.sh').read_text()
 begin=deploy.find('project_path="$repo_path"')
 if begin < 0:begin=deploy.index('case "$env_name" in\n  staging)')
 end=deploy.index('validate_file_path() {',begin)
 write(case/'production-paths.sh',deploy[begin:end])
 write(case/'check-production-paths.sh',r'''#!/bin/bash
set -euo pipefail
die() { echo "$*" >&2; return 1; }
repo_path="$2"; env_name=prod; test_mode=0; compose_file="${4:-}"; override_file="${5:-}"; env_file="${6:-}";health_url='';ready_url=''
source "$1"
[ "$compose_file" = "$3/deploy/compose/launch.yaml" ] && [ "$override_file" = "$3/deploy/compose/server-prod.yaml" ] && [ "$env_file" = "$3/deploy/compose/.env" ] || exit 98
[ "$health_url" = http://127.0.0.1:18089/healthz ] && [ "$ready_url" = http://127.0.0.1:18089/readyz ] || exit 98
''')
 args=['bash',str(case/'check-production-paths.sh'),str(case/'production-paths.sh'),str(repo),str(project)]
 check(layout+' production default paths and existing port',cmd(args,case,env))
 for flag,message in [('compose','compose-file'),('override','override-file'),('env','env-file')]:
  values=['','',''];values[['compose','override','env'].index(flag)]=str(case/'other')
  r=cmd(args+values,case,env)
  if r.returncode==0 or message not in r.stderr:
   fail.append(flag);print('FAIL POP-01: '+layout+' production '+flag+' whitelist does not reject alternate path')
 r=cmd(['bash',str(project/'deploy/scripts/promote.sh'),'--test-mode','--repo',str(bare),'--checkout',str(repo),'--status-dir',str(status),'--audit-log',str(case/'promote.audit'),'--reason','fixture','--dry-run'],case,env)
 check(layout+' promote resolves versioned hook',r)
 local_env=dict(env,XM_DEPLOY_LOCAL_TEST_MODE='1',XM_DEPLOY_LOCAL_DOCKER_BIN=str(bindir/'docker'),XM_DEPLOY_LOCAL_CURL_BIN=str(bindir/'curl'))
 # deploy detached HEAD as part of its existing flow; synthetic branch restored by symbolic-ref only.
 setup(['git','symbolic-ref','HEAD','refs/heads/release/v0.1-launch'],repo)
 r=cmd(['bash',str(project/'deploy/scripts/deploy-local.sh'),'--test-mode','--no-fetch','--dry-run','--repo',str(repo),'--sha',sha],case,local_env)
 check(layout+' local deploy resolves project',r)
 verify=project/'scripts/verify-real-mode.sh';text=verify.read_text();prefix=text[:text.index('tmp="$(mktemp -d)"')]
 write(case/'verify-prefix.sh',prefix+'\nprintf "verified-root=%s compose=%s\\n" "$repo_path" "$compose_file"\n')
 # Keep script-relative default inference by placing extracted prefix alongside the copied original.
 write(project/'scripts/verify-prefix.sh',(case/'verify-prefix.sh').read_text())
 r=cmd(['bash',str(project/'scripts/verify-prefix.sh'),'--test-mode'],case,dict(env,XM_VERIFY_REAL_MODE_TEST_MODE='1',XM_VERIFY_REAL_MODE_DOCKER_BIN=str(bindir/'docker'),XM_VERIFY_REAL_MODE_CURL_BIN=str(bindir/'curl')))
 check(layout+' verify-real-mode default root',r,lambda r:'verified-root='+str(repo)+' compose='+str(project/'deploy/compose/launch.yaml') in r.stdout)
 write(bindir/'gate','#!/bin/bash\n[ "$PWD" = "${FIXTURE_PROJECT:?}" ] && [ -f go.mod ] || exit 95\n')
 ci_env=dict(env,CI_LOCAL_ALLOW_OVERRIDES='1')
 for gate in ['GOVERNANCE','SECRET_SCAN','BACKEND','FRONTEND']:ci_env['CI_LOCAL_'+gate+'_CMD']=str(bindir/'gate')
 r=cmd(['bash',str(project/'scripts/ci-local.sh')],case,ci_env)
 check(layout+' ci default gates use project cwd',r)
 r=cmd(['bash',str(project/'scripts/ci-local.sh')],case,dict(ci_env,CI_LOCAL_ROOT=str(repo)))
 check(layout+' ci explicit Git root uses project cwd',r)
 write(bindir/'psql','#!/bin/bash\nprintf "1\\n"\n')
 write(bindir/'go','#!/bin/bash\n[ "$PWD" = "${FIXTURE_PROJECT:?}" ] && [ -f go.mod ] && [ -d db/migrations ] || exit 96\n')
 r=cmd(['bash',str(project/'scripts/dev/worktree-testdb.sh'),'--pg-url','postgres://fixture@127.0.0.1:65432/postgres','--print-url'],project,dict(env,XM_WORKTREE_TESTDB_GO_BIN=str(bindir/'go'),XM_WORKTREE_TESTDB_PSQL_BIN=str(bindir/'psql')))
 check(layout+' testdb migration uses project cwd',r,lambda r:'/xm_test_checkout' in r.stdout)
 # run_ci only: Docker is a recorder, and the trusted script is a fixture copy.
 post=(project/'deploy/git-hooks/post-receive').read_text();a=post.index('run_ci() {');b=post.index('\n}\n',a)+3
 write(case/'run-ci.sh',post[a:b])
 write(case/'post-check.sh',r'''#!/bin/bash
set -euo pipefail
source "$1"
ci_command='';docker_bin="$2";configured_ci_script="$3";configured_ci_script_sha256="$(sha256sum "$3" | cut -d' ' -f1)"
configured_base_ref=origin/main;configured_require_base=1;ci_image=fixture:fixed;zero=0000000000000000000000000000000000000000
run_ci "$4" aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa "$zero"
''')
 trace.write_text('')
 r=cmd(['bash',str(case/'post-check.sh'),str(case/'run-ci.sh'),str(bindir/'docker'),str(project/'scripts/ci-local.sh'),str(repo)],case,env)
 target='/workspace' if layout=='standalone' else '/workspace/platform'
 check(layout+' post-receive CI project root',r,lambda r:'CI_LOCAL_ROOT='+target+' ' in trace.read_text() and '-w '+target+' ' in trace.read_text())
 # Execute only the actual Compose wrapper with a recorder; no runtime verification is invoked.
 a=text.index('compose() {');b=text.index('\n}\n',a)+3
 write(case/'compose-function.sh',text[a:b])
 write(bindir/'compose-recorder','#!/bin/bash\n[ "$PWD" = "${FIXTURE_PROJECT:?}" ] || exit 97\n')
 r=cmd(['bash','-c','set -euo pipefail; source "$1"; repo_path="$2"; project_path="$3"; compose_file="$3/deploy/compose/launch.yaml"; env_file="$3/deploy/compose/.env"; project=fixture; docker_bin="$4"; compose config','fixture',str(case/'compose-function.sh'),str(repo),str(project),str(bindir/'compose-recorder')],case,env)
 check(layout+' verify-real-mode Compose project cwd',r)
# Active monorepo command paths are verified against real checked-in assets, not executed.
for doc in ['DEPLOY-SERVER-QUICKSTART.md','GO-LIVE-CHECKLIST.md','REQLOG-RECORDER.md']:
 text=(source/'docs/runbooks'/doc).read_text()
 controlled_repo=re.search(r'^repo_path="([^"]+)"$',(source/'deploy/scripts/deploy.sh').read_text(),re.M).group(1)
 for path in re.findall(r'/srv/(?:[A-Za-z0-9_.-]+/)*xingmang-platform(?=[/\s`])',text):
  if path != controlled_repo:
   fail.append(doc);print('FAIL POP-01: '+doc+' changes the controlled checkout root')
 if '/xingmang-platform/deploy/' in text or ('platform/deploy/scripts/' not in text):fail.append(doc);print('FAIL POP-01: '+doc+' keeps old deployment command paths')
# Resolve actual executable/override tokens in the documented Git-root commands.
 for token in re.findall(r'(?:platform/)?(?:deploy/scripts/[A-Za-z0-9.-]+\.sh|deploy/compose/server-[a-z]+\.yaml)',text):
  if not token.startswith('platform/') or not (source/token.removeprefix('platform/')).is_file():
   fail.append(doc);print('FAIL POP-01: '+doc+' command asset does not resolve: '+token)
text=(source/'docs/runbooks/GIT-WORKFLOW.md').read_text()
if 'platform/scripts/dev/worktree-testdb.sh' not in text or not (source/'scripts/dev/worktree-testdb.sh').is_file():fail.append('GIT-WORKFLOW');print('FAIL POP-01: Git workflow command root is not monorepo-aware')
print('fixture='+str(work))
if fail:raise SystemExit(1)
print('PASS POP-01: both layouts and command roots verified with external fakes')
PY

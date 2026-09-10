from pathlib import Path
import json,os,re,shutil,subprocess,tempfile
ROOT=Path(__file__).resolve().parents[3]
def read(path):return (ROOT/path).read_text(encoding="utf-8")
BASH=os.environ.get("BASH_EXE") or ("D:/Git/bin/bash.exe" if os.name=="nt" else "bash")
doc=read("platform/docs/runbooks/DEPLOY.md")
snippet=next(b for b in re.findall(r"```bash\s*\n(.*?)```",doc,re.S) if "PROMOTE-PRODUCTION" in b)
# External actions are trapped; no deploy script, Docker daemon, env or server is accessed.
snippet=snippet.replace("deploy/scripts/promote.sh","approved_promote").replace("deploy/scripts/deploy.sh","approved_deploy")
shell='docker() { printf "%s" "$DOC_MODEL"; return "${DOC_CONFIG_EXIT:-0}"; }\napproved_promote() { echo PROMOTED; }\napproved_deploy() { echo DEPLOYED; }\n'
if not shutil.which("jq"): shell+='jq() { wsl.exe --exec jq "$@"; }\n'
shell+=snippet
def run(model,config_exit=0):
 env=os.environ.copy();env.update(DOC_MODEL=json.dumps(model),DOC_CONFIG_EXIT=str(config_exit))
 return subprocess.run([BASH,"--noprofile","--norc","-c",shell],env=env,capture_output=True,text=True,encoding="utf-8",errors="replace")
# Bind the two preserved conventions to actual consumers, not a redefined production port.
deploy=read("platform/deploy/scripts/deploy.sh")
port=re.search(r'prod\)\s+branch_name="main".*?default_port=(\d+)',deploy,re.S).group(1)
prod=read("platform/deploy/compose/server-prod.yaml")
fallback=re.search(r'127\.0\.0\.1:\$\{WEB_PORT:-(\d+)\}:80',prod).group(1)
if (port,fallback)!=("18089","8088"):raise RuntimeError("existing port conventions changed; review docs")
good={"services":{"web":{"ports":[{"host_ip":"127.0.0.1","published":port,"target":80}]}}}
cases=[]
for label,published,host,target in [('default-8088',fallback,'127.0.0.1',80),('public-bind',port,'0.0.0.0',80),('wrong-target',port,'127.0.0.1',81)]:
 cases.append((label,{"services":{"web":{"ports":[{"host_ip":host,"published":published,"target":target}]}}},0))
cases.append(('missing-web',{"services":{}},0))
cases.append(('extra-port',{"services":{"web":{"ports":good['services']['web']['ports']*2}}},0))
cases.append(('config-failed',good,17))
for label,model,status in cases:
 r=run(model,status)
 assert r.returncode!=0 and "PROMOTED" not in r.stdout and "DEPLOYED" not in r.stdout, "port preflight permitted "+label
r=run(good)
assert r.returncode==0 and r.stdout.splitlines()==['PROMOTED','DEPLOYED'], "port preflight rejected the existing DEPLOY0 convention: "+r.stderr
print("PASS: actual doc block rejects all mismatched/failed models before actions; approved18089 succeeds, defaults unchanged")

from pathlib import Path
import os,re,shlex,subprocess,tempfile
ROOT=Path(__file__).resolve().parents[3]
def read(path): return (ROOT/path).read_text(encoding="utf-8")
def blocks(text): return re.findall(r"```bash\s*\n(.*?)```",text,re.S)
BASH=os.environ.get("BASH_EXE") or ("D:/Git/bin/bash.exe" if os.name=="nt" else "bash")
# Actual verification snippet must address either existing deployment project explicitly.
doc=read("platform/docs/runbooks/REQLOG-RECORDER.md")
snippet=next(b for b in blocks(doc) if "platform-api" in b and "sh -c" in b and ("docker exec" in b or "docker compose" in b))
for project in ("xingmang-launch","xingmang-prod"):
 shell='docker() { printf "%s\n" "$@"; }\n'+snippet
 env=os.environ.copy();env["XM_DEPLOY_PROJECT"]=project
 p=subprocess.run([BASH,"--noprofile","--norc","-c",shell],env=env,capture_output=True,text=True)
 args=p.stdout.splitlines()
 assert p.returncode==0 and args[:2]==["exec",project+"-platform-api-1"], "verification targets wrong project: "+project
print("PASS: verification selects both real deployment naming contracts without Compose discovery")

from pathlib import Path
import os,re,shlex,subprocess,tempfile
ROOT=Path(__file__).resolve().parents[3]
def read(path): return (ROOT/path).read_text(encoding="utf-8")
def blocks(text): return re.findall(r"```bash\s*\n(.*?)```",text,re.S)
BASH=os.environ.get("BASH_EXE") or ("D:/Git/bin/bash.exe" if os.name=="nt" else "bash")
# Consume actual worker restart argv; only inspect tracked YAML, never run Docker.
prod=read("platform/deploy/compose/server-prod.yaml")
worker=prod.split("  platform-worker:",1)[1].split("  web:",1)[0]
required=re.search(r":(/var/lib/xm/reqlog):ro",worker).group(1)
for name in ("NEWAPI","SUB2API"):
 doc=read("platform/docs/runbooks/SWITCH-"+name+"-REAL.md")
 snippet=next(b for b in blocks(doc) if "up -d platform-worker" in b)
 args=shlex.split(snippet.replace("\\\n"," "))
 configs=[args[i+1] for i,v in enumerate(args[:-1]) if v in ("-f","--file")]
 mounted=[]
 for config in configs:
  model=read("platform/"+config)
  section=model.split("  platform-worker:",1)[1].split("  web:",1)[0]
  mounted.extend(re.findall(r":(/var/lib/xm/reqlog):ro",section))
 assert required in mounted, name+" production worker restart loses the required reqlog mount"
print("PASS: both production restart snippets retain the actual override mount")

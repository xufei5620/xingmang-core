from pathlib import Path
import os,re,shlex,subprocess,tempfile
ROOT=Path(__file__).resolve().parents[3]
def read(path): return (ROOT/path).read_text(encoding="utf-8")
def blocks(text): return re.findall(r"```bash\s*\n(.*?)```",text,re.S)
BASH=os.environ.get("BASH_EXE") or ("D:/Git/bin/bash.exe" if os.name=="nt" else "bash")
# The real writer keeps existing mode; the doc's approved manual repair must expose errors.
writer=read("platform/cmd/reqlog-recorder/tokenmap.go")
if "os.WriteFile(r.cfg.TokenMapPath" not in writer: raise RuntimeError("writer contract changed")
snippet=next(b for b in blocks(read("platform/docs/runbooks/REQLOG-RECORDER.md")) if "sudo chgrp 10001 /root/reqlog/tokenmap.json" in b)
shell='sudo() { if [ "$1" = chgrp ]; then return 23; fi; printf "unexpected-chmod\n"; }\n'+snippet
p=subprocess.run([BASH,"--noprofile","--norc","-c",shell],capture_output=True,text=True)
assert p.returncode==23 and "unexpected-chmod" not in p.stdout, "permission repair swallowed failure or continued to chmod"
print("PASS: documented permission repair preserves failure without touching a file")

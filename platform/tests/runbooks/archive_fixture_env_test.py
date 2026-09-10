from pathlib import Path
import os,re,shutil,subprocess,tempfile
ROOT=Path(__file__).resolve().parents[3]
def read(path): return (ROOT/path).read_text(encoding="utf-8")
def blocks(text): return re.findall(r"```bash\s*\n(.*?)```",text,re.S)
BASH=os.environ.get("BASH_EXE") or ("D:/Git/bin/bash.exe" if os.name=="nt" else "bash")
# Regression consumes the actual up/down snippets; Docker is an isolated boundary fake.
model=read("platform/deploy/compose/archive.yaml")
required=re.search(r"\$\{(XM_ARCHIVE_CREDENTIAL_ENV_FILE):\?",model).group(1)
commands=blocks(read("platform/docs/runbooks/AUDIT-ARCHIVE.md"))
shell="unset "+required+"\ndocker() { test -n \"${"+required+"-}\" || return 19; }\nset -e\n"+"\n".join(commands)
p=subprocess.run([BASH,"--noprofile","--norc","-c",shell],capture_output=True,text=True)
assert p.returncode==0, "archive teardown lost required env variable; exit="+str(p.returncode)
print("PASS: both actual fixture commands satisfy the Compose required-variable contract")

from pathlib import Path
import json,os,re,shlex,shutil,subprocess,tempfile
ROOT=Path(__file__).resolve().parents[3]
def read(path): return (ROOT/path).read_text(encoding="utf-8")
def blocks(text): return re.findall(r"```bash\s*\n(.*?)```",text,re.S)
BASH=os.environ.get("BASH_EXE") or ("D:/Git/bin/bash.exe" if os.name=="nt" else "bash")
# Meta-regression: the document's command must not label a perfect map as proof of a broken reader.
doc=read("platform/docs/runbooks/REQLOG-RECORDER.md")
query=next(q for q in re.findall(r"sudo jq '(.+?)' /root/reqlog/tokenmap.v2.json",doc,re.S) if "with_id" in q)
fixture=Path(os.environ.get("RUNBOOK_FIXTURE_ROOT") or tempfile.mkdtemp(prefix="reqlog-doc-"));fixture.mkdir(parents=True,exist_ok=True)
p=fixture/"synthetic-map.json";p.write_text(json.dumps({"schema_version":2,"entries":{"synthetic-prefix":{"source":"sub2api","user_id":"123"}}}),encoding="utf-8")
if shutil.which("jq"): cmd=[shutil.which("jq"),query,str(p)]
else:
 text=p.resolve().as_posix();posix="/mnt/"+text[0].lower()+text[2:]
 cmd=["wsl.exe","--exec","jq",query,posix]
r=subprocess.run(cmd,capture_output=True,text=True,encoding="utf-8",errors="replace")
if r.returncode: raise RuntimeError("jq fixture failed: "+r.stderr)
report=json.loads(r.stdout)
# Phase1's reader-broken mutation returns User=nil while this unchanged map is still 100%.
assert report.get("proves_user_resolution",True) is False, "documented jq overclaims reader success despite surviving reader-broken mutation"
print("PASS: actual jq output explicitly withholds reader-resolution proof; real 99% remains unverified")

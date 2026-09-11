from pathlib import Path
import os,re,shutil,subprocess,tempfile
ROOT=Path(__file__).resolve().parents[3]
def read(path): return (ROOT/path).read_text(encoding="utf-8")
def blocks(text): return re.findall(r"```bash\s*\n(.*?)```",text,re.S)
BASH=os.environ.get("BASH_EXE") or ("D:/Git/bin/bash.exe" if os.name=="nt" else "bash")
# Run actual documented invocation against a dependency-free exit-code consumer.
source=read("platform/cmd/platform-shadow/main.go")
if "os.Exit(code)" not in source or "return 2" not in source: raise RuntimeError("shadow consumer contract changed")
snippet=next(b for b in blocks(read("platform/docs/runbooks/SHADOW-COMPARE.md")) if "./cmd/platform-shadow" in b)
fixture=Path(os.environ.get("RUNBOOK_FIXTURE_ROOT") or tempfile.mkdtemp(prefix="shadow-doc-"))
fixture.mkdir(parents=True,exist_ok=True)
(fixture/"shadow.go").write_text('package main\nimport "os"\nfunc main(){os.Exit(2)}\n',encoding="utf-8")
go=Path("G:/cache/go-mod/golang.org/toolchain@v0.0.1-go1.27.0.windows-amd64/bin/go.exe")
gobin=str(go) if go.exists() else shutil.which("go")
env=os.environ.copy(); env.update(GOTOOLCHAIN="local",GO111MODULE="off")
env["PATH"]=str(Path(gobin).parent)+os.pathsep+env["PATH"]
snippet=snippet.replace("./cmd/platform-shadow","./shadow.go")
p=subprocess.run([BASH,"--noprofile","--norc","-c",snippet],cwd=fixture,env=env,capture_output=True,text=True)
assert p.returncode==2, "documented shadow call must preserve configuration exit 2; got "+str(p.returncode)+" "+p.stderr
print("PASS: actual invocation preserves the no-report exit status 2")

from pathlib import Path
import json,os,re,shlex,shutil,subprocess,tempfile
ROOT=Path(__file__).resolve().parents[3]
def read(path): return (ROOT/path).read_text(encoding="utf-8")
# Actual documented Action parameters are run through the current pure mode resolver.
jobs=read("platform/internal/platform/jobs/connector_config.go")
resolver=re.search(r"func ResolveEffectiveMode\(.*?\n}\n",jobs,re.S).group(0)
actions=read("platform/internal/platform/credentials/actions.go")
definition=actions.split("func connectorConfigSetDefinition()",1)[1].split("// callerPrincipal",1)[0]
allowed=set(re.findall(r'Name: "([a-z_]+)"',definition))
cases=[]
for name in ("NEWAPI","SUB2API"):
 doc=read("platform/docs/runbooks/SWITCH-"+name+"-REAL.md")
 section=doc.split("## 方式 A",1)[1].split("## 方式 B",1)[0]
 snippets=re.findall(r"```json\s*\n(.*?)```",section,re.S)
 payloads=[json.loads(s) for s in snippets]
 params=next((p for p in payloads if p.get("platform")==name.lower()),None)
 assert params is not None and set(params)<=allowed, name+" formal switch does not supply an existing database Action contract"
 cases.append(params)
fixture=Path(os.environ.get("RUNBOOK_FIXTURE_ROOT") or tempfile.mkdtemp(prefix="mode-doc-"));fixture.mkdir(parents=True,exist_ok=True)
program='package main\nimport("fmt";"strings")\ntype ConnectorConfig struct{Mode string;Version int}\ntype EffectiveConnectorConfig struct{Platform,Mode,Source string;Version int}\nconst(ModeSourceDatabase="database";ModeSourceEnv="env";ModeSourceUnknown="unknown")\n'+resolver+'\nfunc main(){\n'
for p in cases:
 program+='r:=ResolveEffectiveMode('+json.dumps(p['platform'])+',&ConnectorConfig{Mode:'+json.dumps(p['mode'])+'},"fake");fmt.Println(r.Mode+"/"+r.Source)\n' if p==cases[0] else 'r=ResolveEffectiveMode('+json.dumps(p['platform'])+',&ConnectorConfig{Mode:'+json.dumps(p['mode'])+'},"fake");fmt.Println(r.Mode+"/"+r.Source)\n'
program+='}\n';(fixture/'main.go').write_text(program,encoding='utf-8')
go=Path("G:/cache/go-mod/golang.org/toolchain@v0.0.1-go1.27.0.windows-amd64/bin/go.exe")
env=os.environ.copy();env.update(GOTOOLCHAIN="local",GO111MODULE="off")
r=subprocess.run([str(go) if go.exists() else "go","run","main.go"],cwd=fixture,env=env,capture_output=True,text=True)
if r.returncode:raise RuntimeError(r.stderr)
assert r.stdout.splitlines()==["real/database","real/database"], "documented Action parameters do not yield the claimed effective mode: "+r.stdout
print("PASS: both formal switch examples match the actual Action schema and effective-mode consumer")

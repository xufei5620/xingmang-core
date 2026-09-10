"""Exercise only the CLI's pre-secret-read validation extracted from current source."""
from pathlib import Path
import argparse,os,re,subprocess
PROJECT=Path(__file__).resolve().parents[1]

def kinds(project,fixture):
    src=(project/'backend/cmd/eligibility-repair/main.go').read_text(encoding='utf-8')
    constants=src[src.index('const ('):src.index('\nfunc main()')]
    start=src.index('\n\tif kind != kindPreAnchorUsage',src.index('\nfunc run(ctx'))
    end=src.index('\n\t// A narrowing',start)
    go='package main\nimport("fmt";"os")\n'+constants+'\nfunc check(kind string) error {\n'+src[start:end]+'\nreturn nil\n}\nfunc main(){if e:=check(os.Args[1]);e!=nil{fmt.Fprintln(os.Stderr,e);os.Exit(1)}}\n'
    file=fixture/'kind-check.go';file.write_text(go,encoding='utf-8')
    binary=fixture/('kind-check.exe' if os.name=='nt' else 'kind-check')
    env=dict(os.environ,GOWORK='off',GOPROXY='off',GOTOOLCHAIN='local')
    p=subprocess.run(['go','build','-o',str(binary),str(file)],env=env,capture_output=True,text=True)
    assert p.returncode==0,'extracted validation did not compile: '+p.stderr
    checked=0
    for name in ['PRODUCTION-RUNBOOK.md','ELIGIBILITY-OPERATIONS.md']:
        for n,line in enumerate((project/'docs'/name).read_text(encoding='utf-8').splitlines(),1):
            for kind in re.findall(r'--kind=([a-z-]+)',line):
                p=subprocess.run([str(binary),kind],capture_output=True,text=True)
                assert p.returncode==0,f'{name}:{n} kind {kind} is rejected by current CLI: {p.stderr}'
                checked+=1
    print(f'INV-DOC-01: {checked} documented kinds accepted by the current Go pre-secret guard; no repair/DB/key execution.')

if __name__=='__main__':
    p=argparse.ArgumentParser();p.add_argument('--case',choices=['kinds'],required=True);p.add_argument('--fixture-root',type=Path,required=True);p.add_argument('--project',type=Path,default=PROJECT)
    args=p.parse_args();args.fixture_root.mkdir(parents=True,exist_ok=True)
    kinds(args.project,args.fixture_root)

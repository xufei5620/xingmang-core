"""Exercise only the CLI's pre-secret-read validation extracted from current source."""
from pathlib import Path
import argparse,os,re,shlex,subprocess
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
        error_examples=False
        for n,line in enumerate((project/'docs'/name).read_text(encoding='utf-8').splitlines(),1):
            if line=='| Additional arguments | Exit |':error_examples=True
            elif error_examples and not line.startswith('|'):error_examples=False
            if error_examples:continue  # Executed by exit_codes with expected rejection.
            for kind in re.findall(r'--kind=([a-z-]+)',line):
                p=subprocess.run([str(binary),kind],capture_output=True,text=True)
                assert p.returncode==0,f'{name}:{n} kind {kind} is rejected by current CLI: {p.stderr}'
                checked+=1
    print(f'INV-DOC-01: {checked} documented kinds accepted by the current Go pre-secret guard; no repair/DB/key execution.')

def exit_codes(project,fixture):
    src=(project/'backend/cmd/eligibility-repair/main.go').read_text(encoding='utf-8')
    constants=src[src.index('const ('):src.index('\nfunc main()')]
    main=src[src.index('func main()'):src.index('\nfunc run(ctx')]
    run_start=src.index('func run(ctx');run_end=src.index('\n\tdatabaseURL, err := readOneLineSecret',run_start)
    # Preserve main's real flag parser, path checks, filters and exit mapping.
    # Replace the first credential read and all subsequent code with a sentinel.
    go='package main\nimport("context";"errors";"flag";"fmt";"io";"log/slog";"os";"path/filepath";"time")\n'+constants+main+src[run_start:run_end]+'\nreturn errors.New("FIXTURE_STOP_BEFORE_SECRET_IO")\n}\n'
    file=fixture/'exit-check.go';file.write_text(go,encoding='utf-8');binary=fixture/('exit-check.exe' if os.name=='nt' else 'exit-check')
    env=dict(os.environ,GOWORK='off',GOPROXY='off',GOTOOLCHAIN='local')
    p=subprocess.run(['go','build','-o',str(binary),str(file)],env=env,capture_output=True,text=True)
    assert p.returncode==0,'extracted CLI did not compile: '+p.stderr
    doc=(project/'docs/PRODUCTION-RUNBOOK.md').read_text(encoding='utf-8')
    section=doc.split('Exit codes -- check them',1)[1].split('**Re-evaluating one parked account',1)[0]
    rows=dict(re.findall(r'^\| ([0123]) \| (.+) \|$',section,re.M))
    examples=re.findall(r'^\| `([^`]+)` \| ([123]) \|',section,re.M)
    if not examples:
        # Old table assigned bad flag combinations to 2; apply that documented
        # claim to a concrete current-CLI combination before editing the table.
        claimed=next(int(code) for code,description in rows.items() if 'bad flag combination' in description)
        examples=[('--kind=projection-requeue-dead --event=event-fixture',claimed)]
    else:
        # The examples and the summary must classify the same errors. Execute
        # each concrete sample below using the exit claimed by its summary row.
        categories={'--kind=unknown':'unknown kind','--kind=projection-requeue-dead --event=event-fixture':'incompatible kind/filter combination','--kind=pending-reevaluate':'required filter','--database-url-file=relative-path':'non-absolute path','extra-positional-argument':'extra positional argument','--unknown-flag':'unknown flag','--apply=not-a-boolean':'malformed flag value'}
        for fragment,phrase in categories.items():
            matches=[code for code,description in rows.items() if phrase in description]
            assert len(matches)==1,'exit summary has ambiguous or missing error classification: '+phrase
            examples.append((fragment,matches[0]))
    base=['--database-url-file='+str(fixture/'inert-db-path'),'--field-keyring-file='+str(fixture/'inert-key-path'),'--migrations-dir='+str(fixture/'inert-migrations-path')]
    for fragment,claimed in examples:
        p=subprocess.run([str(binary)]+base+shlex.split(fragment),capture_output=True,text=True)
        assert 'FIXTURE_STOP_BEFORE_SECRET_IO' not in p.stderr,'example is not a pre-I/O rejection case'
        assert p.returncode==int(claimed),f'documented {fragment} exit {claimed} differs from current CLI exit {p.returncode}'
    print(f'INV-DOC-05: {len(examples)} documented error examples match actual extracted flag/filter/path exit mapping; no secret IO.')

if __name__=='__main__':
    p=argparse.ArgumentParser();p.add_argument('--case',choices=['kinds','exit-codes'],required=True);p.add_argument('--fixture-root',type=Path,required=True);p.add_argument('--project',type=Path,default=PROJECT)
    args=p.parse_args();args.fixture_root.mkdir(parents=True,exist_ok=True)
    {'kinds':kinds,'exit-codes':exit_codes}[args.case](args.project,args.fixture_root)

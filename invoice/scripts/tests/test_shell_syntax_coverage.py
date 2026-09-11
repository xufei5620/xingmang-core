"""Run each real PowerShell syntax invocation over harmless per-file fixtures."""
import argparse,json,os,pathlib,shutil,subprocess,tempfile
parser=argparse.ArgumentParser()
parser.add_argument('--root',type=pathlib.Path,default=pathlib.Path(__file__).resolve().parents[3])
parser.add_argument('--case')
args=parser.parse_args()
repo=args.root.resolve()
home=pathlib.Path(__file__).resolve().parents[3]/'invoice/release/auxiliary-test-fixtures'; home.mkdir(parents=True,exist_ok=True)
fixture=pathlib.Path(tempfile.mkdtemp(prefix='syntax-',dir=home))
extract=fixture/'extract.ps1'
extract.write_text('''param([string]$InputPath, [switch]$TopSetup)
$tokens=$null; $errors=$null
$ast=[System.Management.Automation.Language.Parser]::ParseFile($InputPath,[ref]$tokens,[ref]$errors)
if ($errors.Count) { throw 'PowerShell syntax fixture source parse failed' }
$commands=@($ast.FindAll({ param($node)
  $node -is [System.Management.Automation.Language.CommandAst] -and
  $node.Extent.Text -match '(?i)bash(?:\\.Source)? -n '
},$true))
if ($commands.Count -ne 1) { throw 'expected one bash syntax command' }
$node=$commands[0]
$loop=$null
while ($node.Parent) {
  if ($node -is [System.Management.Automation.Language.ForEachStatementAst]) { $loop=$node; break }
  $node=$node.Parent
}
if ($loop) {
  if ($TopSetup) {
    $assignments=@($ast.FindAll({ param($item)
      $item -is [System.Management.Automation.Language.AssignmentStatementAst] -and
      $item.Left -is [System.Management.Automation.Language.VariableExpressionAst]
    },$true))
    $unified=@($assignments | Where-Object { $_.Left.VariablePath.UserPath -eq 'unifiedShellScripts' })
    $combined=@($assignments | Where-Object { $_.Left.VariablePath.UserPath -eq 'scriptsForBash' })
    if ($unified.Count -ne 1 -or $combined.Count -ne 1) { throw 'one current unified/combined syntax list is required' }
    $paths=@($unified[0].Right.Expression.SafeGetValue())
    @{ paths=$paths; setup=($unified[0].Extent.Text + [Environment]::NewLine + $combined[0].Extent.Text); body=$loop.Extent.Text } | ConvertTo-Json -Compress -Depth 8
  } else { $loop.Extent.Text }
  exit 0
}
$node=$commands[0]
while ($node.Parent -isnot [System.Management.Automation.Language.StatementBlockAst]) { $node=$node.Parent }
$statements=@($node.Parent.Statements)
$index=[array]::IndexOf($statements,$node)
if ($index -lt 0 -or $index+1 -ge $statements.Count) { throw 'native exit guard missing' }
$node.Extent.Text
$statements[$index+1].Extent.Text
''',encoding='utf-8',newline='\n')
top=sorted(str(p.relative_to(repo/'invoice')).replace('\\','/') for folder in ['deploy','scripts'] for p in (repo/'invoice'/folder).rglob('*.sh'))
unified=['../deploy/rehearsal-unified/rehearse.sh','../deploy/rehearsal-unified/cleanup.sh','../deploy/unified/cutover.sh','../deploy/unified/rollback.sh']
groups=[('index','verify-source-readiness-index-operator.ps1',['deploy/postgres/apply-source-readiness-index-concurrently.sh','deploy/postgres/verify-source-readiness-index.sh']),('cleanup','verify-balance-history-cleanup-operator.ps1',['deploy/postgres/apply-balance-history-cleanup.sh','deploy/postgres/plan-balance-history-cleanup.sh','deploy/postgres/rehearse-balance-history-cleanup.sh','deploy/backup/restore-drill.sh']),('unified','verify.ps1',unified),('top','verify.ps1',top)]
failures=[]
for group,script,paths in groups:
 if args.case and not args.case.startswith(group+'-'): continue
 extracted=subprocess.run(['pwsh','-NoProfile','-File',str(extract),'-InputPath',str(args.root/'invoice/scripts'/script)]+(['-TopSetup'] if group in ('top','unified') else []),capture_output=True,text=True)
 assert extracted.returncode==0, 'syntax extraction failed: '+extracted.stderr
 current = json.loads(extracted.stdout) if group in ('top','unified') else None
 if current is not None:
  assert current['paths']==unified, 'current D/E shell list must contain every reviewed entry'
  assert all((repo/'invoice'/path).is_file() for path in unified), 'current D/E shell entry is missing'
 root=fixture/group; root.mkdir()
 for path in dict.fromkeys(paths+(unified if current is not None else [])):
  dest=root/path; dest.parent.mkdir(parents=True,exist_ok=True); dest.write_text('echo fixture\n',encoding='utf-8')
 array=','.join("'"+p.replace("'","''")+"'" for p in ([] if group=='unified' else paths))
 setup=("$shellScripts=@("+array+")\n"+current['setup']+"\n"+current['body']) if current is not None else ("$scriptsForBash=@("+array+")\n"+extracted.stdout)
 probe=root/'probe.ps1'; probe.write_text("$ErrorActionPreference='Stop'\n$bash=Get-Command bash\n"+setup,encoding='utf-8',newline='\n')
 if current is not None:
  real=subprocess.run(['pwsh','-NoProfile','-File',str(probe)],cwd=repo/'invoice',capture_output=True)
  assert real.returncode==0, 'actual current shell syntax validation failed for '+group
  print('PASS '+group+'-actual')
 for index in [-1,*range(len(paths))]:
  case=group+('-valid' if index<0 else '-invalid-'+str(index))
  if args.case and args.case!=case: continue
  if index>=0: (root/paths[index]).write_text('if true; then\n',encoding='utf-8')
  result=subprocess.run(['pwsh','-NoProfile','-File',str(probe)],cwd=root,capture_output=True)
  expected=0 if index<0 else 1
  if result.returncode!=expected: failures.append(f'{case}: expected syntax exit {expected}, got {result.returncode}')
  else: print('PASS '+case)
  if index>=0: (root/paths[index]).write_text('echo fixture\n',encoding='utf-8')
if failures: raise AssertionError('\n'.join(failures))

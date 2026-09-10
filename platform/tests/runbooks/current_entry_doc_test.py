from pathlib import Path
import json,os,re,shlex,shutil,subprocess,tempfile
ROOT=Path(__file__).resolve().parents[3]
def read(path): return (ROOT/path).read_text(encoding="utf-8")
# Current entry instructions must resolve to this monorepo's actual module/links, never an old drive.
prompt=read("platform/docs/handoffs/CODEX-PROMPT.md")
paths=re.findall(r"[A-Z]:/[^`\s]+",prompt)
assert paths and all(p.startswith("G:/xingmang/") for p in paths), "current permanent prompt routes work to an obsolete drive"
assert "cd G:/xingmang/09-wt/<slug>/platform" in prompt and (ROOT/"platform/go.mod").is_file(), "platform commands must enter the actual module after worktree creation"
root=read("README.md")
links=re.findall(r"\]\((docs/[^)]+)\)",root)
assert "docs/MONOREPO-MIGRATION.md" in links and "docs/handoffs/MONOREPO-CUTOVER.md" in links and all((ROOT/p).is_file() for p in links), "current README lacks the live migration/cutover evidence route"
assert not re.search(r"^\s*git subtree pull",root,re.M), "README still prescribes the old automatic subtree sync command"
print("PASS: permanent entry resolves to the monorepo module and current cutover evidence")

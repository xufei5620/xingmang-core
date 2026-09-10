from pathlib import Path
import os, re, subprocess

ROOT = Path(__file__).resolve().parents[3]
def read(path):
    return (ROOT / path).read_text(encoding="utf-8")

def formal_captures(doc):
    sections = re.findall(r"^### 2\.2 [^\n]*\n(.*?)(?=^## |^### |\Z)", doc, re.M | re.S)
    assert len(sections) == 1, "expected one formal capture section"
    commands = []
    for block in re.findall(r"```bash\s*\n(.*?)```", sections[0], re.S):
        for line in re.sub(r"\\\r?\n", " ", block).splitlines():
            if re.match(r"^\s*\./evidence-capture\s+capture(?:\s|$)", line):
                commands.append(line.strip())
    assert len(commands) == 2, "formal capture section must contain exactly two capture commands"
    platforms = []
    for command in commands:
        names = re.findall(r"(?:^|\s)--platform\s+(\S+)", command)
        assert len(names) == 1, "each formal capture must select exactly one platform"
        assert not re.search(r"(?:^|\s)--dry-run(?:\s|$)", command), "formal capture cannot be a dry run"
        platforms.append(names[0])
    assert set(platforms) == {"sub2api", "newapi"}, "formal capture commands must cover sub2api and newapi exactly once"
    return list(zip(platforms, commands))

BASH = os.environ.get("BASH_EXE") or ("D:/Git/bin/bash.exe" if os.name == "nt" else "bash")
# Evaluate only the actual root argument with printf; never execute capture or read a file.
source = read("platform/cmd/evidence-capture/capture.go")
assert "secrets.NewFileProvider(root)" in source, "capture provider contract changed"
doc = read("platform/docs/runbooks/USERS-REAL-APPROVAL.md")
for platform, command in formal_captures(doc):
    roots = re.findall(r'--secret-root ("[^"\n]+")', command)
    assert len(roots) == 1, platform + " formal capture lacks exactly one explicit file-store prerequisite"
    arg = roots[0]
    for state in ("missing", "empty", "provided"):
        env = os.environ.copy()
        env.pop("XM_SECRET_ROOT", None)
        if state == "empty":
            env["XM_SECRET_ROOT"] = ""
        elif state == "provided":
            env["XM_SECRET_ROOT"] = "/synthetic/read-only store"
        result = subprocess.run([BASH, "--noprofile", "--norc", "-c", "printf '%s' " + arg], env=env, capture_output=True, text=True)
        if state == "provided":
            assert result.returncode == 0 and result.stdout == env["XM_SECRET_ROOT"], platform + " formal capture changes explicit file root"
        else:
            assert result.returncode != 0, platform + " formal capture permits " + state + " file-store prerequisite"
print("PASS: both formal captures independently reject missing/empty roots and forward the supplied root")

from pathlib import Path
import re

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

# Explicit --out is the root to which capture.go appends its timestamp.
source = read("platform/cmd/evidence-capture/capture.go")
consumer = re.search(r"outDir := f.out(.*?)outDir = filepath.Join\(outDir, timestampDir\(now\)\)", source, re.S)
assert consumer is not None and 'strings.TrimSpace(outDir) == ""' in consumer.group(1), "capture output consumer changed; review test contract"
doc = read("platform/docs/runbooks/USERS-REAL-APPROVAL.md")
verify = re.search(r"cd (docs/evidence/users-real/<platform>/<timestamp>)/", doc).group(1)
for platform, command in formal_captures(doc):
    outputs = re.findall(r"(?:^|\s)--out\s+(\S+)", command)
    assert len(outputs) == 1, platform + " formal capture must provide exactly one --out"
    actual = outputs[0] + "/synthetic-time"
    expected = verify.replace("<platform>", platform).replace("<timestamp>", "synthetic-time")
    assert actual == expected, "capture/verification path mismatch for " + platform + ": " + actual + " != " + expected
print("PASS: exactly two formal captures independently match their verification directory")

"""The governance mutation suite must never restore over its caller's edits."""
import os
from pathlib import Path
import shutil
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[2]
env = os.environ.copy()
env.update(GIT_CONFIG_GLOBAL=os.devnull, GIT_CONFIG_NOSYSTEM="1", GIT_OPTIONAL_LOCKS="0")
git = shutil.which("git")
bash = str(Path(git).parents[1] / "bin/bash.exe") if os.name == "nt" else "bash"

for state in ("unstaged", "staged"):
    with tempfile.TemporaryDirectory(prefix="governance-preserve-") as temporary:
        root = Path(temporary)
        for relative in ("tests/security/governance-not-hollow.test.sh", "scripts/guard-governance-files.sh"):
            target = root / relative
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(ROOT / relative, target)
        migration = root / "db/migrations/000001.up.sql"
        migration.parent.mkdir(parents=True)
        migration.write_text("-- committed synthetic migration\n", encoding="utf-8")
        marker = root / "governance-entered.log"
        env["GOVERNANCE_PRESERVATION_MARKER"] = marker.as_posix()
        (root / "scripts/check-governance.sh").write_text(
            '#!/usr/bin/env bash\nprintf "entered\\n" >> "$GOVERNANCE_PRESERVATION_MARKER"\nexit 1\n',
            encoding="utf-8")
        def run(*args):
            return subprocess.run(args, cwd=root, env=env, capture_output=True, check=True)
        run(git, "init", "-q")
        run(git, "add", ".")
        run(git, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid",
            "-c", "commit.gpgsign=false", "commit", "-qm", "baseline")
        migration.write_text(migration.read_text(encoding="utf-8") + "-- caller edit\n", encoding="utf-8")
        if state == "staged":
            run(git, "add", str(migration))
        before = migration.read_bytes()
        index = (root / ".git/index").read_bytes()
        guard = (root / "scripts/guard-governance-files.sh").read_bytes()
        # A failing governance subprocess is deliberate: error exits must also preserve input.
        result = subprocess.run([bash, "tests/security/governance-not-hollow.test.sh"],
                                cwd=root, env=env, capture_output=True, timeout=60)
        assert result.returncode == 1, f"{state}: expected deliberate governance rejection, got {result.returncode}"
        calls = marker.read_text(encoding="utf-8").splitlines() if marker.exists() else []
        # Two baseline checks, migration tamper/deletion and three guard removals must run.
        assert calls == ["entered"] * 7, f"{state}: expected seven governance calls, got {len(calls)}"
        assert migration.read_bytes() == before, f"{state}: caller migration changed"
        assert (root / ".git/index").read_bytes() == index, f"{state}: caller index changed"
        assert (root / "scripts/guard-governance-files.sh").read_bytes() == guard, f"{state}: caller guard changed"
print("governance source preservation passed")

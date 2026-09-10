#!/usr/bin/env python3
"""Trace the real CPA install/app-start block with inert external boundaries.

This never runs the deployment entry point, installer, snapshot binary or Docker.
Only the current lifecycle slice is evaluated, in its original order. Host UID
and the absolute verification executable are replaced by explicit test doubles.
"""
from pathlib import Path
import os
import re
import shlex
import subprocess
import sys
import tempfile


def lifecycle_slice(source):
    positions = {}
    for phase in ("cpa-snapshot", "cpa-consumer-preflight", "up-app", "worker"):
        matches = list(re.finditer(r'^phase="' + phase + r'"$', source, re.M))
        if len(matches) != 1:
            raise AssertionError("cannot identify one executable " + phase + " boundary")
        positions[phase] = matches[0].start()
    start = min(positions[p] for p in positions if p != "worker")
    end = positions["worker"]
    if end <= max(positions[p] for p in positions if p != "worker"):
        raise AssertionError("CPA install or app startup escaped the checked lifecycle slice")
    code = source[start:end]
    for original, fake in (
        ('${EUID:-$(id -u)}', '0'),
        ('/opt/xingmang/cpa-snapshot/current/cpa-snapshot verify', 'audit_host_verify verify'),
    ):
        if code.count(original) != 1:
            raise AssertionError("external boundary changed; update the isolated fixture explicitly")
        code = code.replace(original, fake)
    return code


def main():
    source = Path(sys.argv[1]).read_text(encoding="utf-8")
    code = lifecycle_slice(source)
    # The calling shell passes its own tools. On Windows a PATH search for bash
    # can find the WSL launcher instead of the Git Bash that is running the gate.
    if len(sys.argv) != 5:
        raise AssertionError("the normal shell entry must provide bash/grep/tail paths")
    tools = dict(zip(("bash", "grep", "tail"), sys.argv[2:]))
    if os.name == "nt":
        tools = {name: path + ".exe" if not Path(path).is_file() and Path(path + ".exe").is_file() else path
                 for name, path in tools.items()}
    if not all(Path(path).is_file() for path in tools.values()):
        raise AssertionError("the calling shell tools are not executable files")

    def shell_path(path):
        path = str(path).replace("\\", "/")
        if os.name == "nt" and re.match(r"^[A-Za-z]:/", path):
            return "/" + path[0].lower() + path[2:]
        return path
    prelude = r'''
set -euo pipefail
die() { printf '%s\n' "$*" >&2; exit 1; }
record() { printf '%s\n' "$1" >> "$AUDIT_TRACE"; }
chmod() { [ "$*" = "0700 $tmp_dir/cpa-snapshot" ]; }
audit_host_verify() { [ "$*" = verify ]; }
systemctl() { die 'unexpected systemctl invocation in the install/app slice'; }
command_not_found_handle() { die 'unexpected external command in the install/app slice'; }
bash() {
  [ "$#" -eq 3 ] && [ "$1" = "$project_path/deploy/scripts/install-cpa-snapshot.sh" ] &&
    [ "$2" = "$tmp_dir/cpa-snapshot" ] && [ "$3" = "$target_sha" ] || return 97
  record install
  # Even output containing a success marker cannot override an installer failure.
  if [ "$AUDIT_INSTALL_RESULT" != no-evidence ]; then
    printf 'CPA SNAPSHOT INSTALL PASS: isolated-fixture\n'
  fi
  [ "$AUDIT_INSTALL_RESULT" != failed ] || return 7
}
run_compose() {
  case "$1" in
    cp) [ "$*" = "cp migrate:/usr/local/bin/cpa-snapshot $tmp_dir/cpa-snapshot" ] ;;
    run) [ "$2" = --rm ] && [ "$3" = --no-deps ] ;;
    up) [ "$*" = 'up -d platform-api platform-worker web' ] && record app-up ;;
    *) die 'unexpected Compose invocation in the install/app slice' ;;
  esac
}
expected_environment=production
cpa_mode_value=file
target_sha=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
tmp_dir="$AUDIT_TMP"
project_path="$AUDIT_TMP/project"
'''
    # Only grep/tail may execute real external programs; both read synthetic logs.
    for name in ("grep", "tail"):
        prelude += f'\n{name}() {{ {shlex.quote(shell_path(tools[name]))} "$@"; }}\n'
    with tempfile.TemporaryDirectory(prefix="cpa-install-order-") as directory:
        root = Path(directory)
        empty_bin = root / "bin"
        empty_bin.mkdir()
        script = root / "slice.sh"
        script.write_text(prelude + "\n" + code, encoding="utf-8")
        for result in ("verified", "failed", "no-evidence"):
            trace = root / (result + ".trace")
            trace.write_text("", encoding="utf-8")
            env = {k: v for k, v in os.environ.items()
                   if k not in ("BASH_ENV", "ENV", "SHELLOPTS", "BASHOPTS", "CDPATH")}
            env.update(PATH=shell_path(empty_bin), AUDIT_TMP=shell_path(root), AUDIT_TRACE=shell_path(trace),
                       AUDIT_INSTALL_RESULT=result)
            run = subprocess.run([tools["bash"], "--noprofile", "--norc", shell_path(script)],
                                 env=env, capture_output=True, text=True, timeout=15)
            events = trace.read_text(encoding="utf-8").splitlines()
            print(f"CPA install trace: {result} exit={run.returncode} events={','.join(events)}")
            if result == "verified":
                if run.returncode != 0 or events != ["install", "app-up"]:
                    raise AssertionError("verified installer must run exactly once before exactly one app startup")
            elif run.returncode == 0 or events != ["install"]:
                raise AssertionError(result + " installer must stop before app startup")
    print("CPA-INSTALL-ORDER-OK")


if __name__ == "__main__":
    try:
        main()
    except (AssertionError, OSError, subprocess.SubprocessError) as error:
        print("CPA-INSTALL-ORDER-FAIL: " + str(error), file=sys.stderr)
        raise SystemExit(1)

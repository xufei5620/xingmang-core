"""Bind the executing public operator sources to the frozen image manifest.

Staged packages need no .git directory. Only public Python, shell and audit SQL
sources are opened; environment files, signing keys and runtime data are not.
"""
import hashlib
import json
from pathlib import Path, PurePosixPath
import re

from lifecycle import require, utc


REQUIRED = frozenset({
    "deploy/rehearsal-unified/" + name for name in (
        "lifecycle.py", "preflight.py", "restore.py", "smoke.py",
        "operator_lock.py", "host_nginx.py", "operator_source.py",
        "rehearse.sh", "cleanup.sh", "capture-old-inputs.py")
} | {
    "deploy/unified/cutover.sh", "deploy/unified/rollback.sh",
    "deploy/unified/audit/role_policy.py", "deploy/unified/audit/identity_audit.py",
})


def selected(name):
    return (name in REQUIRED or
            name.startswith(("deploy/rehearsal-unified/", "deploy/unified/audit/"))
            and name.endswith((".py", ".sh", ".sql")))


def git_blob(body):
    return hashlib.sha1(b"blob " + str(len(body)).encode() + b"\0" + body).hexdigest()


def verify(root, source):
    require(isinstance(source, dict) and source.get("gitDirty") is False and
            re.fullmatch(r"[a-f0-9]{40}", source.get("gitHead", "")),
            "operator source requires an exact clean manifest HEAD")
    entries = source.get("entries")
    require(isinstance(entries, list) and all(isinstance(e, dict) for e in entries),
            "operator source inventory is missing")
    canonical = json.dumps(entries, sort_keys=True, separators=(",", ":"), ensure_ascii=False).encode()
    require(hashlib.sha256(canonical).hexdigest() == source.get("inventorySha256"),
            "operator source inventory digest differs")
    names = [e.get("path") for e in entries]
    require(all(isinstance(n, str) for n in names) and len(names) == len(set(names)),
            "operator source inventory has invalid or duplicate paths")
    require(REQUIRED <= set(names), "operator source inventory is incomplete")
    root = Path(root).absolute()
    require(root.is_dir() and not root.is_symlink(), "operator root is missing or redirected")
    files = []
    for entry in entries:
        name = entry["path"]
        if not selected(name):
            continue
        parts = PurePosixPath(name).parts
        require(parts and not PurePosixPath(name).is_absolute() and
                not any(p in (".", "..") or ":" in p or "\\" in p for p in parts),
                "operator source path is unsafe")
        require(entry.get("mode") in ("100644", "100755") and
                re.fullmatch(r"[a-f0-9]{40}", entry.get("blob", "")),
                "operator source is not a tracked ordinary file")
        path = root.joinpath(*parts)
        for parent in (path, *path.parents):
            if parent == root.parent:
                break
            require(not parent.is_symlink() and not getattr(parent, "is_junction", lambda: False)(),
                    "operator source is redirected")
        require(path.is_file() and path.resolve().is_relative_to(root.resolve()),
                "operator source file is missing or escaped")
        body = path.read_bytes()
        normalized = body.replace(b"\r\n", b"\n")
        require(entry["blob"] in (git_blob(body), git_blob(normalized)),
                "executing operator source differs from manifest: " + name)
        files.append({"path": name, "gitBlob": entry["blob"],
                      "sha256": hashlib.sha256(body).hexdigest(),
                      "normalizedSha256": hashlib.sha256(normalized).hexdigest()})
    return {"schema": "xingmang.operator-source/v1", "head": source["gitHead"],
            "inventorySha256": source["inventorySha256"], "verified_utc": utc(), "files": files}

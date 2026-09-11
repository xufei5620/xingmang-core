#!/usr/bin/env python3
"""Canonical LOCAL check/build evidence. This program deliberately has no deploy action."""
import argparse
import datetime
import hashlib
import io
import json
import os
from pathlib import Path
import re
import shutil
import stat
import subprocess
import sys
import tarfile
import tempfile

ROOT = Path(__file__).resolve().parents[1]
SCHEMA = "xingmang.unified.local-build/v1"
SOURCE_SCOPES = ("platform", "invoice", "deploy", "scripts", ".dockerignore", ".gitattributes",
                 ".gitignore", "go.work", "go.work.sum", "package.json", "pnpm-lock.yaml", "pnpm-workspace.yaml")


class GateError(RuntimeError):
    pass


def utc():
    return datetime.datetime.now(datetime.UTC).isoformat()


def sha256(data):
    return hashlib.sha256(data).hexdigest()


def canonical(value):
    return json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=False).encode("utf-8")


def local_tool_environment(environment=None):
    env = dict(os.environ if environment is None else environment)
    env.update(GIT_NO_LAZY_FETCH="1", GIT_TERMINAL_PROMPT="0")
    for key in ("DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH",
                "BUILDX_BUILDER", "BUILDKIT_HOST", "BUILDX_CONFIG"):
        env.pop(key, None)
    return env


def run(args, cwd=None, **kwargs):
    # Neither Git's missing objects nor Docker's active/remote builder is implicit.
    result = subprocess.run(args, cwd=cwd, env=local_tool_environment(), capture_output=True, **kwargs)
    if result.returncode:
        # Build diagnostics go to the dedicated local log, not arbitrary environment output.
        raise GateError("command failed (exit %d): %s" % (result.returncode, " ".join(map(str, args[:5]))))
    return result.stdout


def source_provenance(root, require_clean=True):
    root = Path(root).resolve()
    git_root = Path(run(["git", "-C", str(root), "rev-parse", "--show-toplevel"]).decode().strip()).resolve()
    if git_root != root:
        raise GateError("source root is not the exact monorepo checkout")
    status = run(["git", "-C", str(root), "status", "--porcelain=v1", "-z", "--untracked-files=all", "--", *SOURCE_SCOPES])
    if require_clean and status:
        raise GateError("uncommitted platform, invoice or root build source blocks build")
    staged = run(["git", "-C", str(root), "ls-files", "--stage", "-z", "--", *SOURCE_SCOPES])
    entries = []
    for entry in staged.split(b"\0"):
        if not entry:
            continue
        metadata, name = entry.split(b"\t", 1)
        mode, blob, stage = metadata.decode("ascii").split()
        if stage != "0":
            raise GateError("unmerged source entry")
        entries.append({"path": name.decode("utf-8"), "mode": mode, "blob": blob})
    if not any(x["path"].startswith("platform/") for x in entries) or not any(x["path"].startswith("invoice/") for x in entries):
        raise GateError("source inventory must cover platform and invoice")
    return {"gitHead": run(["git", "-C", str(root), "rev-parse", "HEAD"]).decode().strip(),
            "gitDirty": bool(status), "inventorySha256": sha256(canonical(entries)), "entries": entries}


def assert_source_matches(expected, actual):
    if expected.get("gitDirty") is not False or actual.get("gitDirty") is not False or any(
            expected.get(key) != actual.get(key) for key in ("gitHead", "inventorySha256", "entries")):
        raise GateError("source provenance changed or is uncommitted")


def context_path_allowed(name):
    parts = Path(name).parts
    if not parts or not (parts[0] in ("platform", "invoice", "deploy", "scripts") or name in SOURCE_SCOPES):
        return False
    lower = [part.lower() for part in parts]
    if any(part in (".git", "node_modules", "dist", "logs", "worktrees", "09-wt", "__pycache__", ".cache") for part in lower):
        return False
    filename = lower[-1]
    if filename == ".env" or filename.startswith(".env.") or filename.endswith((".pem", ".key", ".p12", ".pfx", ".age", ".log", ".db", ".dump", ".tar", ".zip", ".sqlite", ".exe")):
        return False
    # Package directories named secrets/credentials are source code; only runtime
    # directories outside internal/ source packages are excluded here.
    if "internal" not in lower and any(part in ("secrets", "keys", "credentials", "runtime", "source-state") for part in lower[:-1]):
        return False
    return True


def validate_context_entry(name, mode):
    if mode == "120000":
        raise GateError("symlink in build source is not permitted")
    if mode not in ("100644", "100755"):
        raise GateError("non-file in build source is not permitted")
    if Path(name).is_absolute() or ".." in Path(name).parts:
        raise GateError("unsafe build source path")


def create_context(root, source, destination):
    # A frozen tar uses only Git-tracked allowed files, never arbitrary files in a
    # root directory. Missing/sparse files fail here without reading Git blobs.
    with tarfile.open(destination, "w") as archive:
        for entry in source["entries"]:
            name = entry["path"]
            if not context_path_allowed(name):
                continue
            validate_context_entry(name, entry["mode"])
            path = root / name
            if path.is_symlink() or not path.is_file() or not path.resolve().is_relative_to(root.resolve()):
                raise GateError("missing or redirected build source file")
            body = path.read_bytes()
            # Worktree CRLF conversion is permitted only when its normalized bytes
            # match the indexed blob. Never snapshot a concurrently edited file.
            candidates = (body, body.replace(b"\r\n", b"\n"))
            if not any(hashlib.sha1(b"blob " + str(len(data)).encode() + b"\0" + data).hexdigest() == entry["blob"] for data in candidates):
                raise GateError("source file changed while creating build context")
            info = tarfile.TarInfo(name)
            info.size = len(body)
            info.mode = int(entry["mode"][-3:], 8)
            info.mtime = 0
            archive.addfile(info, io.BytesIO(body))
    return file_hash(destination)


def assert_local_endpoint(endpoint):
    if endpoint not in ("npipe:////./pipe/dockerDesktopLinuxEngine", "npipe:////./pipe/docker_engine", "unix:///var/run/docker.sock"):
        raise GateError("only an explicitly verified local Docker endpoint is allowed")


def docker_command(context):
    inspect = json.loads(run(["docker", "context", "inspect", context]).decode())
    if len(inspect) != 1:
        raise GateError("local Docker context is ambiguous")
    assert_local_endpoint(inspect[0]["Endpoints"]["docker"]["Host"])
    return ["docker", "--context", context]


def assert_local_builder(output, context):
    # buildx inspect has no --format in the installed CLI. Match its bounded
    # top-level fields, not nested labels or device Name lines.
    header, separator, nodes = output.partition("Nodes:")
    names = re.findall(r"^Name:\s*(\S+)\s*$", header, re.M)
    drivers = re.findall(r"^Driver:\s*(\S+)\s*$", header, re.M)
    endpoints = re.findall(r"^Endpoint:\s*(\S+)\s*$", nodes, re.M)
    node_names = re.findall(r"^Name:\s*(\S+)\s*$", nodes, re.M)
    if not separator or names != [context] or drivers != ["docker"] or endpoints != [context] or node_names != [context]:
        raise GateError("builder must be the explicit local Docker-context driver")
    return {"name": context, "driver": "docker", "endpoint": context}


def local_builder(docker, context):
    return assert_local_builder(run(docker + ["buildx", "inspect", context]).decode("utf-8"), context)


def definitions(root):
    items = json.loads((root / "deploy/unified/images.json").read_text("utf-8"))
    names = [item["name"] for item in items]
    expected = {"platform-api", "platform-worker", "platform-migrate", "web", "invoice-tools", "pdf-scanner", "source-agent", "invoice-postgres", "clamav", "ingest-proxy", "platform-postgres"}
    if len(names) != len(set(names)) or set(names) != expected:
        raise GateError("unified image definition inventory is incomplete or duplicated")
    for item in items:
        if item["kind"] == "built":
            if item.get("context") != "." or not (root / item["dockerfile"]).is_file():
                raise GateError("every image requires the canonical monorepo build context")
        elif item["kind"] != "pinned" or not re.search(r"@sha256:[a-f0-9]{64}$", item.get("reference", "")):
            raise GateError("external runtime image must be digest-pinned")
    return items


def assert_image_inventory(records, items):
    names = [record.get("name") for record in records]
    if len(names) != len(set(names)) or set(names) != {item["name"] for item in items}:
        raise GateError("image inventory is missing, duplicated or contains a retired image")
    if any(not re.fullmatch(r"sha256:[a-f0-9]{64}", record.get("imageId", "")) for record in records):
        raise GateError("image inventory lacks exact image IDs")


def assert_manifest_kind(doc):
    if doc.get("schema") != SCHEMA or doc.get("productionReady") is not False:
        raise GateError("only the unapproved unified local-build evidence schema is accepted")


def non_reparse_path(path):
    # Inspect every lexical ancestor before normalization: resolving a junction
    # first erases the evidence of redirection, including alias/../new-output.
    path = Path(path)
    absolute = path if path.is_absolute() else Path.cwd() / path
    for ancestor in reversed([absolute, *absolute.parents]):
        try:
            info = ancestor.lstat()
        except FileNotFoundError:
            continue
        if stat.S_ISLNK(info.st_mode) or getattr(info, "st_file_attributes", 0) & stat.FILE_ATTRIBUTE_REPARSE_POINT:
            raise GateError("artifact path contains a symlink/reparse point")
        if ancestor != absolute and not stat.S_ISDIR(info.st_mode):
            raise GateError("artifact path ancestor is not a directory")
    return Path(os.path.abspath(absolute))


def artifact_path(root, relative):
    path = Path(relative)
    if path.is_absolute() or ".." in path.parts:
        raise GateError("artifact path leaves its evidence directory")
    root = non_reparse_path(root)
    resolved = non_reparse_path(root / path)
    if not resolved.is_relative_to(root) or resolved == root:
        raise GateError("artifact path leaves its evidence directory")
    return resolved


def file_hash(path):
    with Path(path).open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def assert_file_hash(path, expected):
    if not re.fullmatch(r"[a-f0-9]{64}", expected or "") or file_hash(path) != expected:
        raise GateError("artifact hash mismatch")


def normalized_image_reference(reference):
    name, marker, digest = reference.partition("@")
    if marker:
        prefix, slash, tail = name.rpartition("/")
        name = (prefix + slash if slash else "") + tail.split(":", 1)[0]
    first = name.split("/", 1)[0]
    if "/" not in name:
        name = "docker.io/library/" + name
    elif "." not in first and ":" not in first and first != "localhost":
        name = "docker.io/" + name
    return name + ("@" + digest if marker else "")


def assert_archive_image(path, reference, image_id):
    # Docker's classic store exposes config IDs. containerd exposes OCI manifest/
    # index IDs. The latter must be reachable from index.json and prove the
    # selected linux/amd64 config and layers; an unrelated valid blob is not proof.
    index_types = {"application/vnd.oci.image.index.v1+json", "application/vnd.docker.distribution.manifest.list.v2+json"}
    manifest_types = {"application/vnd.oci.image.manifest.v1+json", "application/vnd.docker.distribution.manifest.v2+json"}
    config_types = {"application/vnd.oci.image.config.v1+json", "application/vnd.docker.container.image.v1+json"}
    if not re.fullmatch(r"sha256:[a-f0-9]{64}", image_id):
        raise GateError("invalid archive image identity")
    with tarfile.open(path, "r") as archive:
        members = {}
        for member in archive.getmembers():
            if member.isdir():
                continue
            if member.name in members:
                raise GateError("archive contains ambiguous duplicate members")
            members[member.name] = member

        def member(name, maximum=None):
            result = members.get(name)
            if not name or Path(name).is_absolute() or ".." in Path(name).parts or "\\" in name or not result or not result.isfile():
                raise GateError("archive member is missing, redirected or unsafe")
            if maximum is not None and result.size > maximum:
                raise GateError("archive metadata is oversized")
            return result

        def strict_object(pairs):
            result = {}
            for key, value in pairs:
                if key in result:
                    raise GateError("archive metadata has duplicate fields")
                result[key] = value
            return result

        def json_member(name, maximum=4 * 1024 * 1024):
            try:
                return json.load(archive.extractfile(member(name, maximum)), object_pairs_hook=strict_object)
            except (ValueError, UnicodeError) as error:
                raise GateError("invalid archive JSON metadata") from error

        def hash_member(name):
            with archive.extractfile(member(name)) as stream:
                return "sha256:" + hashlib.file_digest(stream, "sha256").hexdigest()

        def descriptor(desc, metadata=False):
            if not isinstance(desc, dict) or not re.fullmatch(r"sha256:[a-f0-9]{64}", desc.get("digest", "")) or type(desc.get("size")) is not int or desc["size"] < 0:
                raise GateError("invalid OCI descriptor")
            name = "blobs/sha256/" + desc["digest"].split(":")[1]
            info = member(name, 4 * 1024 * 1024 if metadata else None)
            if info.size != desc["size"] or hash_member(name) != desc["digest"]:
                raise GateError("OCI descriptor content or size mismatch")
            return json_member(name) if metadata else name

        doc = json_member("manifest.json", 65536)
        if not isinstance(doc, list) or len(doc) != 1:
            raise GateError("archive image inventory must contain exactly one image")
        item = doc[0]
        tags = item.get("RepoTags") or []
        if not isinstance(tags, list) or any(not isinstance(t, str) for t in tags):
            raise GateError("invalid archive tag inventory")
        if "@sha256:" not in reference and normalized_image_reference(reference) not in [normalized_image_reference(t) for t in tags]:
            raise GateError("archive image tag is not the reviewed reference")
        config_name = item.get("Config", "")
        config = json_member(config_name)
        config_digest = hash_member(config_name)
        layers = item.get("Layers")
        if not isinstance(config, dict) or config.get("os") != "linux" or config.get("architecture") != "amd64" or not isinstance(layers, list):
            raise GateError("archive must contain the selected linux/amd64 image")
        if "index.json" not in members:
            diff_ids = config.get("rootfs", {}).get("diff_ids")
            if config_digest != image_id or not isinstance(diff_ids, list) or len(diff_ids) != len(layers) or [hash_member(name) for name in layers] != diff_ids:
                raise GateError("classic archive image config or layer identity mismatch")
            return {"imageId": image_id, "configDigest": config_digest}

        transport = json_member("index.json")
        roots = transport.get("manifests", [])
        if transport.get("schemaVersion") != 2 or not isinstance(roots, list):
            raise GateError("invalid OCI transport index")
        selected = [d for d in roots if isinstance(d, dict) and d.get("digest") == image_id]
        if len(selected) != 1:
            raise GateError("OCI image ID is not uniquely reachable from transport index")
        if "@sha256:" in reference and reference.rsplit("@", 1)[1] != image_id:
            raise GateError("OCI pinned reference differs from the selected image ID")
        annotation = selected[0].get("annotations", {}).get("io.containerd.image.name")
        if annotation is not None and normalized_image_reference(annotation) != normalized_image_reference(reference):
            raise GateError("OCI selected image reference differs from reviewed reference")
        leaves = []
        visited = set()

        def visit(desc, depth=0):
            if depth > 4 or desc.get("digest") in visited:
                raise GateError("OCI descriptor chain is cyclic or ambiguous")
            visited.add(desc.get("digest"))
            value = descriptor(desc, True)
            kind = desc.get("mediaType")
            if value.get("schemaVersion") != 2 or value.get("mediaType") != kind:
                raise GateError("OCI descriptor media type differs from content")
            if kind in index_types:
                children = value.get("manifests")
                if not isinstance(children, list) or len(children) > 128:
                    raise GateError("invalid OCI child inventory")
                for child in children:
                    platform = child.get("platform")
                    if platform is None or (platform.get("os") == "linux" and platform.get("architecture") == "amd64"):
                        visit(child, depth + 1)
            elif kind in manifest_types:
                cfg = value.get("config", {})
                if cfg.get("mediaType") not in config_types:
                    raise GateError("invalid OCI image config type")
                cfg_value = descriptor(cfg, True)
                if cfg_value.get("os") == "linux" and cfg_value.get("architecture") == "amd64":
                    leaves.append((cfg, value.get("layers")))
            else:
                raise GateError("OCI image root is not an image manifest or index")

        visit(selected[0])
        if len(leaves) != 1 or leaves[0][0]["digest"] != config_digest:
            raise GateError("OCI chain does not prove the exact selected config")
        oci_layers = leaves[0][1]
        if not isinstance(oci_layers, list) or [descriptor(d) for d in oci_layers] != layers:
            raise GateError("OCI layers differ from Docker save image inventory")
        return {"imageId": image_id, "configDigest": config_digest}


def assert_buildkit_output(metadata, proof, reference):
    output = metadata.get("containerimage.digest", "")
    config = metadata.get("containerimage.config.digest")
    if not re.fullmatch(r"sha256:[a-f0-9]{64}", output):
        raise GateError("BuildKit output digest is missing")
    if proof["imageId"] == proof["configDigest"]:
        if config != proof["configDigest"]:
            raise GateError("classic BuildKit output config is not bound to the archive")
    elif output != proof["imageId"]:
        raise GateError("BuildKit output digest is not bound to the archive image")
    if config is not None and config != proof["configDigest"]:
        raise GateError("BuildKit output config differs from the archive")
    descriptor = metadata.get("containerimage.descriptor")
    if descriptor is not None and descriptor.get("digest") != output:
        raise GateError("BuildKit output descriptor differs from its digest")
    name = metadata.get("image.name")
    if name is not None and normalized_image_reference(name) != normalized_image_reference(reference):
        raise GateError("BuildKit output reference differs from the reviewed image")


def check(root):
    definitions(root)
    # The actual Compose parser validates both independent source networks and the
    # single API graph. Empty --env-file prevents loading an operator's real .env.
    with tempfile.TemporaryDirectory(prefix="xm-unified-check-") as temp:
        empty = Path(temp) / "empty.env"
        empty.write_text("", encoding="utf-8")
        for name in ("compose.json", "sources.json"):
            run(["docker", "compose", "--env-file", str(empty), "-f", str(root / "deploy/unified" / name),
                 "config", "--no-interpolate", "--no-env-resolution", "--quiet"])
    result = subprocess.run([sys.executable, "-m", "unittest", "discover", "-s", str(root / "scripts/tests"),
                             "-p", "test_unified*.py", "-v"], cwd=root)
    if result.returncode:
        raise GateError("unified topology/source/artifact contracts failed")


def base_image_evidence(metadata):
    """Record the bases actually resolved by BuildKit, including unpinned tags."""
    provenance = metadata.get('buildx.build.provenance', {})
    materials = provenance.get('materials', [])
    if not isinstance(materials, list): raise GateError('invalid base image provenance')
    records = []
    for material in materials:
        if not isinstance(material, dict): raise GateError('invalid base image material')
        uri = material.get('uri', '')
        if not uri.startswith('pkg:docker/'): continue
        digest = material.get('digest', {}).get('sha256', '')
        if not re.fullmatch(r'[a-f0-9]{64}', digest): raise GateError('base image digest missing or malformed')
        records.append({'uri': uri, 'digest': 'sha256:' + digest})
    if not records: raise GateError('BuildKit base image provenance missing')
    return sorted(records, key=lambda item: (item['uri'], item['digest']))


def build_arguments(item, tag, source, context_hash, builder=None):
    args = ["buildx", "build", "--load", "--provenance=mode=min", "--pull=false", "--platform", "linux/amd64", "--file", item["dockerfile"], "--tag", item["repository"] + ":" + tag,
            "--label", "org.opencontainers.image.revision=" + source["gitHead"],
            "--label", "xingmang.source.inventory=" + source["inventorySha256"],
            "--label", "xingmang.source.context=" + context_hash,
            "--build-arg", "BUILD_VERSION=" + tag, "--build-arg", "BUILD_COMMIT=" + source["gitHead"]]
    if builder is not None:
        args.extend(["--builder", builder])
    if item.get("target"):
        args.extend(["--target", item["target"]])
    for key, value in item.get("buildArgs", {}).items():
        args.extend(["--build-arg", key + "=" + value])
    return args + ["-"]


def build(root, tag, output, context, dry_run=False):
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_.-]{0,63}", tag):
        raise GateError("invalid exact image tag")
    check(root)
    source = source_provenance(root, require_clean=not dry_run)
    items = definitions(root)
    if dry_run:
        print(json.dumps({"mode": "dry-run", "productionReady": False, "sourceDirty": source["gitDirty"],
            "sourceHead": source["gitHead"], "commands": [build_arguments(item, tag, source, "<frozen-context-sha256>", builder=context) for item in items if item["kind"] == "built"],
            "pinnedRuntimeImages": [item["reference"] for item in items if item["kind"] == "pinned"]}, indent=2))
        return
    if not output:
        raise GateError("build requires an outside-worktree evidence directory")
    destination = non_reparse_path(output)
    if destination.is_relative_to(root.resolve()) or destination.exists():
        raise GateError("build output must be a new directory outside the worktree")
    docker = docker_command(context)
    builder = local_builder(docker, context)
    destination.mkdir(parents=True)
    (destination / "images").mkdir()
    (destination / "logs").mkdir()
    started = utc()
    records = []
    with tempfile.TemporaryDirectory(prefix="xm-unified-context-") as temp:
        snapshot = Path(temp) / "source.tar"
        context_hash = create_context(root, source, snapshot)
        for item in items:
            image_started = utc()
            reference = item["reference"] if item["kind"] == "pinned" else item["repository"] + ":" + tag
            if item["kind"] == "built":
                metadata_path = destination / "logs" / (item["name"] + ".buildkit.json")
                command = docker + build_arguments(item, tag, source, context_hash, builder=builder["name"])
                command[-1:-1] = ["--metadata-file", str(metadata_path)]
                with snapshot.open("rb") as stdin, (destination / "logs" / (item["name"] + ".log")).open("wb") as log:
                    result = subprocess.run(command, cwd=root, stdin=stdin, stdout=log, stderr=subprocess.STDOUT, env={**local_tool_environment(), "BUILDX_METADATA_PROVENANCE": "min"})
                (destination / "logs" / (item["name"] + ".json")).write_text(json.dumps({"startUtc": image_started, "endUtc": utc(), "exitCode": result.returncode}), encoding="utf-8")
                if result.returncode:
                    raise GateError("local image build failed; see scoped build log: " + item["name"])
                bases = base_image_evidence(json.loads(metadata_path.read_text("utf-8")))
                base_provenance = {"path": "logs/" + metadata_path.name, "sha256": file_hash(metadata_path)}
            else:
                bases = [{"uri": reference, "digest": "sha256:" + reference.rsplit("@sha256:", 1)[1]}]
                base_provenance = None
            image_id = run(docker + ["image", "inspect", "--format", "{{.Id}}", reference]).decode().strip()
            archive = "images/" + item["name"] + ".tar"
            run(docker + ["image", "save", "--output", str(destination / archive), reference])
            proof = assert_archive_image(destination / archive, reference, image_id)
            if item["kind"] == "built":
                assert_buildkit_output(json.loads(metadata_path.read_text("utf-8")), proof, reference)
            records.append({"name": item["name"], "reference": reference, "imageId": image_id, "archive": archive,
                "configDigest": proof["configDigest"], "archiveSha256": file_hash(destination / archive), "baseImages": bases, "buildkitMetadata": base_provenance, "startedUtc": image_started, "finishedUtc": utc()})
    assert_image_inventory(records, items)
    assert_source_matches(source, source_provenance(root))
    doc = {"schema": SCHEMA, "productionReady": False, "status": "local-build-only", "securityReleaseApproval": "not-performed",
        "startedUtc": started, "finishedUtc": utc(), "tag": tag, "source": source, "contextSha256": context_hash,
        "images": records, "builder": builder, "definitionSha256": file_hash(root / "deploy/unified/images.json")}
    manifest = destination / "manifest.json"
    with manifest.open("x", encoding="utf-8", newline="\n") as stream:
        json.dump(doc, stream, ensure_ascii=False, indent=2)
        stream.write("\n")
    print("Local build complete; production remains unapproved. Evidence: " + str(manifest))


def verify(root, manifest, context):
    path = non_reparse_path(manifest)
    if not path.is_file():
        raise GateError("build manifest must be an existing regular file")
    doc = json.loads(path.read_text("utf-8"))
    assert_manifest_kind(doc)
    assert_source_matches(doc["source"], source_provenance(root))
    assert_file_hash(root / "deploy/unified/images.json", doc["definitionSha256"])
    items = definitions(root)
    assert_image_inventory(doc["images"], items)
    docker = docker_command(context)
    if doc.get("builder") != local_builder(docker, context):
        raise GateError("local builder identity changed")
    by_name = {item["name"]: item for item in items}
    for record in doc["images"]:
        item = by_name[record["name"]]
        expected = item["reference"] if item["kind"] == "pinned" else item["repository"] + ":" + doc["tag"]
        if record["reference"] != expected:
            raise GateError("artifact image reference is not bound to the exact unified tag")
        assert_file_hash(artifact_path(path.parent, record["archive"]), record["archiveSha256"])
        proof = assert_archive_image(artifact_path(path.parent, record["archive"]), expected, record["imageId"])
        if record.get("configDigest") != proof["configDigest"]:
            raise GateError("archive config evidence changed")
        actual = run(docker + ["image", "inspect", "--format", "{{.Id}}", expected]).decode().strip()
        if actual != record["imageId"]:
            raise GateError("local image ID changed")
        if item["kind"] == "built":
            metadata = record["buildkitMetadata"]
            metadata_path = artifact_path(path.parent, metadata["path"])
            assert_file_hash(metadata_path, metadata["sha256"])
            actual_metadata = json.loads(metadata_path.read_text("utf-8"))
            assert_buildkit_output(actual_metadata, proof, expected)
            if record["baseImages"] != base_image_evidence(actual_metadata):
                raise GateError("base image provenance changed")
            labels = json.loads(run(docker + ["image", "inspect", "--format", "{{json .Config.Labels}}", expected]).decode()) or {}
            if labels.get("org.opencontainers.image.revision") != doc["source"]["gitHead"] or labels.get("xingmang.source.inventory") != doc["source"]["inventorySha256"] or labels.get("xingmang.source.context") != doc["contextSha256"]:
                raise GateError("image source label binding changed")
        elif record.get("baseImages") != [{"uri": expected, "digest": "sha256:" + expected.rsplit("@sha256:", 1)[1]}]:
            raise GateError("pinned database base digest changed")
    print("Verified local build evidence only; no deployment or security-release approval.")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    sub.add_parser("check")
    build_parser = sub.add_parser("build")
    build_parser.add_argument("--tag", required=True)
    build_parser.add_argument("--output")
    build_parser.add_argument("--docker-context", default="desktop-linux" if os.name == "nt" else "default")
    build_parser.add_argument("--dry-run", action="store_true")
    verify_parser = sub.add_parser("verify")
    verify_parser.add_argument("--manifest", required=True)
    verify_parser.add_argument("--docker-context", default="desktop-linux" if os.name == "nt" else "default")
    args = parser.parse_args()
    try:
        if args.command == "check":
            check(ROOT)
        elif args.command == "build":
            build(ROOT, args.tag, args.output, args.docker_context, args.dry_run)
        else:
            verify(ROOT, args.manifest, args.docker_context)
    except (GateError, OSError, ValueError, KeyError) as error:
        print("UNIFIED GATE FAILED: " + str(error), file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())

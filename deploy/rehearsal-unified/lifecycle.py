#!/usr/bin/env python3
"""Local-host unified cutover operator. Configuration contains paths, never key bytes.

The same executable drives an isolated synthetic rehearsal and the reviewed
server plan. It never contacts SSH, fetches Git, pulls images or reads key files.
Docker/age/OpenSSH consume explicitly named paths themselves.
"""
import argparse
import datetime as dt
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import time
import urllib.error
import urllib.request
import uuid


BACKUP_ANCHORS = {"invoice": ("invoice-backup", "solov-invoice-backup-v1"),
                  "platform": ("platform-backup", "solov-platform-backup-v1")}
STREAM_ROLES = {kind + "-" + stream for kind in ("sub2api", "newapi")
                for stream in ("payments", "identities", "usage", "credits", "balances")}
OLD_ROLES = {
    "platform": {"platform-api", "platform-worker", "platform-web", "platform-postgres"},
    "invoice": {"invoice-api", "invoice-web", "invoice-postgres", "pdf-scanner", "clamav", "ingest-proxy"},
    "sources": STREAM_ROLES, "idp": {"keycloak", "keycloak-postgres"}}
LOCAL_ENDPOINTS = {"unix:///var/run/docker.sock", "npipe:////./pipe/docker_engine",
                   "npipe:////./pipe/dockerDesktopLinuxEngine"}
NAME = re.compile(r"[a-z][a-z0-9_-]{2,80}\Z")
SHA = re.compile(r"[a-f0-9]{64}\Z")
IMAGE_ID = re.compile(r"sha256:[a-f0-9]{64}\Z")
READINESS_BUDGET_SECONDS = 300


class OperatorError(RuntimeError):
    """A safe, non-secret operator error."""


class ReadinessTimeout(OperatorError):
    """The one candidate-startup budget expired; cleanup must still run."""


class ReadinessBudget:
    def __init__(self, output):
        self.output = output
        self.active = True
        self.started = time.monotonic()
        self.deadline = self.started + READINESS_BUDGET_SECONDS
        self.value = {"status": "STARTING", "start_utc": utc(), "start_monotonic": self.started,
                      "budget_seconds": READINESS_BUDGET_SECONDS, "invoice_latches": 11, "observations": []}
        self.save()

    def save(self):
        atomic_json(self.output / "candidate-readiness.json", self.value)

    def remaining(self):
        seconds = self.deadline - time.monotonic()
        if seconds <= 0:
            raise ReadinessTimeout("candidate original 11 latches exceeded the shared 300 second startup budget")
        return seconds

    def finish(self, status):
        self.active = False  # Recovery commands must not inherit an expired startup deadline.
        self.value.update(status=status, end_utc=self.value.get("first_all_ready_utc", utc()),
                          end_monotonic=self.value.get("first_all_ready_monotonic", time.monotonic()))
        self.value["elapsed_seconds"] = self.value["end_monotonic"] - self.started
        self.save()

    def observe(self, status, body):
        self.remaining()
        observed, stamp = time.monotonic(), utc()
        response = self.output / ("candidate-readiness-" + str(len(self.value["observations"]) + 1) + ".response")
        response.write_bytes(body)  # Only the fixed public /readyz endpoint is accepted.
        self.value["observations"].append({"http_status": status, "observed_utc": stamp,
            "observed_monotonic": observed, "elapsed_seconds": observed - self.started,
            "response_path": str(response), "response_sha256": hashlib.sha256(body).hexdigest()})
        self.save()
        payload = json.loads(body)
        require(status == 200, "readiness HTTP status is not 200")
        require_ready(payload)
        require(isinstance(payload.get("invoice"), dict) and payload["invoice"].get("ready") is True and
                not payload["invoice"].get("check"), "original invoice readiness report is absent or failing")
        self.value.update(first_all_ready_utc=stamp, first_all_ready_monotonic=observed)
        self.finish("READY")
        return payload


# A child process gives the entire GET (headers plus body, including slow-drip
# peers) an enforceable wall-clock timeout. A socket's per-read timeout alone
# can be reset by each byte and cannot enforce the shared startup deadline.
READINESS_PROBE = '''import sys, urllib.request, urllib.error
class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args, **kwargs): return None
opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
try:
    response = opener.open(sys.argv[1], timeout=5)
except urllib.error.HTTPError as error:
    response = error
with response:
    body = response.read(65537)
    if len(body) > 65536: raise ValueError("readiness response too large")
    sys.stdout.buffer.write(str(response.status).encode() + b"\\n" + body)
'''


def utc():
    return dt.datetime.now(dt.timezone.utc).isoformat()


def require(value, reason):
    if not value:
        raise OperatorError(reason)


def digest(path):
    with Path(path).open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def plain_path(value, *, exists=True, directory=False):
    require(isinstance(value, str) and value and "\x00" not in value, "invalid absolute path")
    path = Path(value)
    require(path.is_absolute() and ".." not in path.parts, "path must be absolute without parent traversal")
    for part in (path, *path.parents):
        require(not part.is_symlink(), "symlink input is forbidden")
        if hasattr(part, "is_junction"):
            require(not part.is_junction(), "junction input is forbidden")
    if exists:
        require(path.is_dir() if directory else path.is_file(), "required input path is absent or has the wrong type")
    return path


def read_public_json(path):
    path = plain_path(str(path))
    require(path.stat().st_size <= 4 * 1024 * 1024, "public input exceeds size limit")
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except (UnicodeError, json.JSONDecodeError):
        raise OperatorError("invalid public JSON input") from None


def require_ready(payload):
    require(isinstance(payload, dict) and payload.get("invoice_ready") is True,
            "the original invoice readiness latches are not all ready")
    modules = payload.get("modules")
    require(isinstance(modules, dict), "module readiness is missing")
    for name in ("platform", "invoice_sources", "invoice_projection"):
        item = modules.get(name)
        require(isinstance(item, dict) and item.get("ready") is True and item.get("status") == "ready",
                "required readiness module is not ready: " + name)


def require_recent_record(record, hours, key, *, now=None):
    try:
        stamp = dt.datetime.fromisoformat(record[key])
        require(stamp.tzinfo is not None and stamp.utcoffset() is not None, "approval completion must have an explicit timezone")
        delta = ((now or dt.datetime.now(dt.timezone.utc)) - stamp).total_seconds()
        require(type(hours) is int and 1 <= hours <= 24 and 0 <= delta <= hours * 3600, "approval evidence is stale or in the future")
    except (ValueError, TypeError, KeyError):
        raise OperatorError("approval completion timestamp is missing or invalid") from None


def inherited_preflight_pass(result, mode):
    scope = "local-synthetic-host-preflight" if mode == "local-synthetic" else "server-host-preflight"
    require(result.get("status") == "PASS" and result.get("exit_code") == 0 and result.get("mode") == mode and result.get("qualification_scope") == scope,
            "inherited preflight scope or outcome is invalid")
    require([row.get("name") for row in result.get("checks", [])] == ["source-versions", "host-mode-guards", "network-addressing", "compose-environment"] and
            all(row.get("status") == "PASS" for row in result["checks"]), "inherited preflight did not execute all guards")
    if mode == "local-synthetic":
        require(bool(result.get("inherited_server_checks_requires_server")), "synthetic preflight must retain its server-only limitations")


def approved_loopback_ports(inventory, planned):
    """Only actual image/project-verified old containers may hold cutover ports."""
    approved = set()
    for row in inventory:
        for bindings in (row.get("ports") or {}).values():
            for binding in bindings or []:
                port = binding.get("HostPort", "")
                require(isinstance(port, str) and port.isdigit(), "original published port metadata is invalid")
                if int(port) in planned:
                    require(binding.get("HostIp") == "127.0.0.1", "a takeover port is not owned by an exact old loopback binding")
                    require(int(port) not in approved, "duplicate original takeover port ownership")
                    approved.add(int(port))
    return approved


def validate_previous(projects):
    require(isinstance(projects, list) and len(projects) == 4, "rollback requires all four original projects")
    require({p.get("kind") for p in projects} == set(OLD_ROLES), "rollback project domains are incomplete")
    require(len({p.get("name") for p in projects}) == 4, "rollback project names must be distinct")
    for project in projects:
        entries = project.get("services", {})
        require(isinstance(entries, dict), "rollback service inventory is invalid")
        roles = [entry.get("role") for entry in entries.values()]
        require(len(roles) == len(set(roles)) and set(roles) == OLD_ROLES[project["kind"]],
                "rollback requires the exact complete original role inventory: " + project["kind"])
        require(all(IMAGE_ID.fullmatch(entry.get("image_id", "")) for entry in entries.values()),
                "rollback image identities must be immutable")


def require_resolved_images(project, resolved, image_identity):
    services = resolved.get("services", {})
    for service, entry in project["services"].items():
        reference = services.get(service, {}).get("image")
        require(isinstance(reference, str) and reference, "resolved runtime service image is missing")
        require(image_identity(reference) == entry["image_id"], "resolved Compose image differs from the reviewed manifest")


def require_original_data(original, candidate):
    """E reuses original data; equal SQL migration ledgers do not prove that."""
    requirements = [("platform-postgres", "platform-postgres", "/var/lib/postgresql"),
                    ("invoice-postgres", "invoice-postgres", "/var/lib/postgresql"),
                    ("invoice-api", "platform-api", "/data/documents"),
                    ("platform-api", "platform-api", "/run/xm/secrets")]
    requirements += [(role, role, "/state") for role in sorted(STREAM_ROLES)]
    def mounts(rows, role, target):
        return [m for row in rows if row["role"] == role for m in row["mounts"]
                if m["target"] == target or (target == "/var/lib/postgresql" and m["target"].startswith(target + "/"))]
    for before_role, after_role, target in requirements:
        before, after = mounts(original, before_role, target), mounts(candidate, after_role, target)
        require(len(before) == len(after) == 1, "original data binding is missing or ambiguous: " + before_role)
        require(all(before[0].get(k) == after[0].get(k) for k in ("type", "source", "target")),
                "in-place cutover would attach different data: " + before_role)


def require_mount_inventory(original, resolved):
    def normalize(rows):
        return {(row["project"], row["service"]): sorted(
            [{k: m.get(k) for k in ("type", "source", "target", "rw")} for m in row["mounts"]],
            key=lambda item: item["target"]) for row in rows}
    require(normalize(original) == normalize(resolved), "original Compose mounts differ from the observed running deployment")


def require_snapshot_files(snapshot):
    if "old_input_snapshot" in snapshot:
        binding = snapshot["old_input_snapshot"]
        require(digest(plain_path(binding["path"])) == binding["sha256"], "old input preservation snapshot changed")
        for source in read_public_json(binding["path"])["files"]:
            require(digest(plain_path(source["path"])) == source["sha256"], "pre-stage original input bytes changed")
    for item in snapshot["project_files"] + snapshot.get("legacy_secret_inputs", []):
        require(digest(plain_path(item["env_file"])) == item["env_sha256"], "old original environment bytes changed")
        for source in item["compose"]:
            require(digest(plain_path(source["path"])) == source["sha256"], "old original Compose bytes changed")


def preserved_input_files(projects):
    files = {}
    for project in projects:
        require(set(project) == {"kind", "name", "compose_files", "env_file", "services"}, "old project preservation descriptor is invalid")
        require(isinstance(project["compose_files"], list) and project["compose_files"], "old Compose input list is missing")
        for value in [*project["compose_files"], project["env_file"]]:
            path = plain_path(value)
            require(path.stat().st_nlink == 1, "old retained input must not be a hard link")
            files[str(path)] = {"path": str(path), "sha256": digest(path)}
        for index, value in enumerate(project["compose_files"]):
            path = plain_path(value)
            require(path != plain_path(project["env_file"]) and path.suffix.lower() in (".json", ".yaml", ".yml") and
                    not any(p.casefold() in ("private", "secrets", "credentials") for p in path.parts), "old Compose must be an explicit public file")
            require(path.stat().st_size <= 4 * 1024 * 1024, "old public Compose is too large")
            raw = path.read_bytes()
            require(b"retired-independent-topology" not in raw,
                    "old Compose was replaced by the retired template; owner must restore the original release")
            if index == 0:
                if path.suffix.lower() == ".json":
                    document = read_public_json(path)
                    require(isinstance(document, dict) and isinstance(document.get("services"), dict) and document["services"], "old base Compose cannot have an empty service inventory")
                else:
                    require(raw.strip() and not re.search(rb"(?m)^services:[ \t]*(?:\{[ \t]*\}|null|~)[ \t]*(?:#.*)?\r?$", raw),
                            "old base Compose cannot be the empty replacement template")
    return [files[key] for key in sorted(files)]


def require_retained_layout(config, roots, source_root, output):
    require(isinstance(roots, list) and roots, "explicit old release retention roots are required")
    retained = [plain_path(value, directory=True) for value in roots]
    require(len(set(retained)) == len(retained) and all(path.parent != path for path in retained), "retention roots must be distinct release directories")
    new = plain_path(source_root, exists=False, directory=True)
    destinations = [new, plain_path(output, exists=False), plain_path(config["state_root"], exists=False, directory=True)]
    destinations += [plain_path(f, exists=False) for p in config["candidate"]["projects"] for f in p["compose_files"]]
    for root in retained:
        require(all(not path.is_relative_to(root) and not root.is_relative_to(path) for path in destinations),
                "new source, candidate Compose and operator output must be separate from retained old releases")
    files = [*{f for p in config["previous"]["projects"] for f in [*p["compose_files"], p["env_file"]]}]
    require(all(any(plain_path(f).is_relative_to(root) for root in retained) for f in files), "every old Compose and env must remain in an explicit retained root")


def capture_old_inputs(config, roots, source_root, output):
    """Capture public metadata only. Original Compose/env remain at their paths."""
    require(config.get("mode") in ("local-synthetic", "server-rehearsal", "production"), "invalid old-input capture mode")
    projects = config["previous"]["projects"]
    validate_previous(projects)
    target = plain_path(output, exists=False)
    require(not target.exists() and target.parent.is_dir(), "old-input snapshot output must be a new file in an existing directory")
    require_retained_layout(config, roots, source_root, output)
    files = preserved_input_files(projects)
    value = {"schema": "xingmang.old-inputs/v1", "status": "INPUTS_CAPTURED_NOT_RUNTIME_VERIFIED", "captured_at": utc(),
             "mode": config["mode"], "new_source_root": str(plain_path(source_root, exists=False, directory=True)),
             "retained_roots": roots, "projects": projects, "files": files}
    # Detect a concurrent edit without copying an env, key or resolved secret.
    require(preserved_input_files(projects) == files, "old inputs changed while their metadata was captured")
    with target.open("x", encoding="utf-8", newline="\n") as stream:
        json.dump(value, stream, indent=2); stream.write("\n"); stream.flush(); os.fsync(stream.fileno())
    return {"path": str(target), "sha256": digest(target)}


def require_old_inputs(config):
    descriptor = config["previous"].get("input_snapshot")
    require(isinstance(descriptor, dict) and set(descriptor) == {"path", "sha256"} and
            isinstance(descriptor["sha256"], str) and SHA.fullmatch(descriptor["sha256"]), "old input preservation snapshot is required")
    path = plain_path(descriptor["path"])
    require(path.stat().st_nlink == 1 and digest(path) == descriptor["sha256"], "old input preservation snapshot changed")
    proof = read_public_json(path)
    require(set(proof) == {"schema", "status", "captured_at", "mode", "new_source_root", "retained_roots", "projects", "files"} and
            proof["schema"] == "xingmang.old-inputs/v1" and proof["status"] == "INPUTS_CAPTURED_NOT_RUNTIME_VERIFIED" and
            proof["mode"] == config["mode"] and proof["projects"] == config["previous"]["projects"], "old input snapshot does not bind the original projects")
    require(plain_path(proof["new_source_root"], directory=True) == Path(__file__).absolute().parents[2], "old input snapshot belongs to another staged source root")
    require_retained_layout(config, proof["retained_roots"], proof["new_source_root"], descriptor["path"])
    require(proof["files"] == preserved_input_files(config["previous"]["projects"]), "retained original Compose or environment bytes changed")
    return descriptor


def require_original_compose_labels(project, state):
    files, directory = state.get("compose_files"), state.get("working_dir")
    require(isinstance(files, str) and files and isinstance(directory, str) and directory,
            "original Compose file and working-directory labels are unavailable")
    require([plain_path(value) for value in files.split(",")] == [plain_path(value) for value in project["compose_files"]] and
            plain_path(directory, directory=True) == plain_path(project["compose_files"][0]).parent,
            "selected old Compose paths or relative working directory differ from the original container labels")


def job_plan(config, descriptors, side, kind=None):
    require(side in ("candidate", "previous"), "invalid lifecycle job side")
    require(isinstance(descriptors, list) and descriptors, "ordered permission/migration jobs are required")
    selected = []
    for row in descriptors:
        require(isinstance(row, dict) and set(row) == {"project", "service"} and isinstance(row["service"], str) and NAME.fullmatch(row["service"]), "invalid lifecycle job")
        matches = [p for p in config[side]["projects"] if p["name"] == row["project"] and (kind is None or p["kind"] == kind)]
        require(len(matches) == 1, "lifecycle job is outside its permitted deployment side or project kind")
        selected.append((matches[0], row["service"]))
    return selected


def candidate_job_plan(config):
    rows = config["candidate"]["jobs"]
    plan = job_plan(config, rows, "candidate", "unified")
    require([service for _, service in plan] == ["migrate", "invoice-migrate", "invoice-permissions"],
            "lifecycle ordering must be platform migrate, invoice migrate, permissions")
    return plan


def atomic_json(path, value):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_name(path.name + "." + uuid.uuid4().hex + ".tmp")
    with temporary.open("x", encoding="utf-8", newline="\n") as stream:
        json.dump(value, stream, ensure_ascii=False, indent=2)
        stream.write("\n")
        stream.flush()
        os.fsync(stream.fileno())
    os.replace(temporary, path)


def rollback(driver, snapshot, *, original_failure=False, dry_run=False):
    if dry_run:
        return {"status": "DRY_RUN", "executed": False, "operation": "rollback",
                "steps": ["stop_new", "restore_permissions", "start_old", "check_old", "restore_nginx"]}
    result = {"status": "ROLLING_BACK", "start_utc": utc(), "snapshot": snapshot}
    driver.record(result)
    try:
        # Never permit two writable topologies when stopping the new one failed.
        driver.stop_new()
        driver.restore_permissions()
        driver.start_old()
        driver.check_old(snapshot)
        driver.restore_nginx(snapshot)
        result.update(status="ROLLED_BACK", exit_code=1 if original_failure else 0)
    except BaseException:
        result.update(status="ROLLBACK_FAILED", exit_code=1)
        raise OperatorError("rollback failed; preserve the recorded state and keep ingress closed") from None
    finally:
        result["end_utc"] = utc()
        driver.record(result)
    return result


def cutover(driver, *, dry_run=False):
    if dry_run:
        return {"status": "DRY_RUN", "executed": False, "operation": "cutover",
                "steps": ["preflight", "precheck_new", "snapshot", "stop_old", "start_new_databases", "migrate_and_permissions", "start_new", "check_new", "switch_nginx", "smoke"],
                "on_failure": "stop_new, restore_permissions, start_old, check_old, restore_nginx; return nonzero"}
    result = {"status": "PREFLIGHT", "start_utc": utc()}
    frozen = False
    try:
        driver.preflight()
        driver.precheck_new()
        snapshot = driver.snapshot()
        result.update(status="PREPARED", snapshot=snapshot)
        # The recovery identity is durable before the first stop is attempted.
        driver.record(result)
        frozen = True
        driver.stop_old()
        driver.start_new_databases()
        driver.migrate_and_permissions()
        driver.start_new()
        driver.check_new()
        driver.switch_nginx(snapshot)
        driver.smoke()
        result.update(status="COMMITTED", exit_code=0, end_utc=utc())
        driver.record(result)
        return result
    except BaseException:
        if frozen:
            rollback(driver, snapshot, original_failure=True)
        else:
            result.update(status="PREFLIGHT_FAILED", exit_code=1, end_utc=utc())
            driver.record(result)
        raise OperatorError("cutover did not complete; inspect the recorded rollback outcome") from None


def validate_config(config):
    required = {"schema", "mode", "state_root", "docker", "candidate", "previous", "backups", "approvals", "smoke_config", "rehearsal", "host_preflight", "host_nginx"}
    require(isinstance(config, dict) and set(config) == required, "operator configuration keys do not match the reviewed contract")
    require(config["schema"] == "xingmang.unified.operator/v1", "unsupported operator schema")
    require(config["mode"] in ("local-synthetic", "server-rehearsal", "production"), "unsupported operator mode")
    plain_path(config["state_root"], exists=False, directory=True)
    require(set(config["docker"]) == {"binary", "context", "config_dir"}, "Docker selection must be explicit")
    plain_path(config["docker"]["binary"])
    plain_path(config["docker"]["config_dir"], directory=True)
    require(NAME.fullmatch(config["docker"]["context"]), "invalid Docker context")
    candidate = config["candidate"]
    require(set(candidate) == {"head", "manifest", "manifest_sha256", "projects", "jobs", "ready_url", "migration_digest", "databases"}, "candidate keys are incomplete")
    require(re.fullmatch(r"[a-f0-9]{40}", candidate["head"]), "candidate requires a full commit ID")
    require(SHA.fullmatch(candidate["manifest_sha256"]) and SHA.fullmatch(candidate["migration_digest"]), "candidate artifact or migration binding is invalid")
    require(set(config["previous"]) == {"projects", "migration_digest", "ready_urls", "permission_jobs", "databases", "input_snapshot"}, "previous deployment keys are incomplete")
    validate_previous(config["previous"]["projects"])
    # CR-0010 is schema-preserving. Unknown migration differences must not be
    # papered over by mounting new SQL into old binaries or deleting ledger rows.
    require(config["previous"]["migration_digest"] == candidate["migration_digest"],
            "migration ledger differs; automatic image rollback is not declared compatible")
    all_projects = candidate["projects"] + config["previous"]["projects"]
    require({p.get("kind") for p in candidate["projects"]} == {"unified", "sources"} and len(candidate["projects"]) == 2,
            "candidate needs exactly unified and source projects, without an IdP")
    names = []
    for project in all_projects:
        require(set(project) == {"kind", "name", "compose_files", "env_file", "services"}, "invalid project descriptor")
        require(NAME.fullmatch(project["name"]), "invalid Compose project name")
        names.append(project["name"])
        require(project["compose_files"] and isinstance(project["compose_files"], list), "explicit Compose files required")
        for value in project["compose_files"]:
            plain_path(value)
        plain_path(project["env_file"])
        for name, entry in project["services"].items():
            require(NAME.fullmatch(name) and set(entry) == {"role", "image_id"} and IMAGE_ID.fullmatch(entry["image_id"]), "invalid service identity")
    require(len(names) == len(set(names)), "old and new projects must remain distinct")
    require_old_inputs(config)
    plain_path(candidate["manifest"])
    plain_path(config["smoke_config"])
    require(set(config["backups"]) == set(BACKUP_ANCHORS), "both signed database backups are required")
    return config


class DockerDriver:
    def __init__(self, config, *, record_root=None):
        self.config = validate_config(config)
        self.state = plain_path(config["state_root"], exists=False, directory=True)
        self.output = Path(record_root) if record_root else self.state
        self.env = {k: v for k, v in os.environ.items() if not k.startswith(("DOCKER_", "COMPOSE_"))}
        # Empty auth credentials are not required on the production host: no
        # pull occurs. The explicitly reviewed local context selects the engine.
        self.env["COMPOSE_DISABLE_ENV_FILE"] = "1"
        self.env["DOCKER_CONFIG"] = config["docker"]["config_dir"]
        self.docker = [config["docker"]["binary"], "--context", config["docker"]["context"]]
        self.sequence = 0

    def command(self, name, args, *, input_bytes=None, check=True, timeout=None):
        start = utc()
        budget = getattr(self, "readiness_budget", None)
        if budget and budget.active:
            left = budget.remaining()
            timeout = left if timeout is None else min(timeout, left)
        timed_out = False
        try:
            completed = subprocess.run(args, env=self.env, input=input_bytes, capture_output=True, timeout=timeout)
        except subprocess.TimeoutExpired as error:
            # subprocess.run kills and waits for this invocation's CLI/probe.
            # Docker resources it created remain owned by normal D cleanup/E rollback.
            completed = subprocess.CompletedProcess(args, 124, error.stdout or b"", error.stderr or b"")
            timed_out = True
        self.sequence += 1
        # Output may contain sensitive application diagnostics. Only hashes and
        # sizes are public; raw stdout/stderr are never persisted by this driver.
        event = {"operation": name, "start_utc": start, "end_utc": utc(), "exit_code": completed.returncode,
                 "timeout_seconds": timeout, "timed_out": timed_out,
                 "stdout_sha256": hashlib.sha256(completed.stdout).hexdigest(), "stdout_bytes": len(completed.stdout),
                 "stderr_sha256": hashlib.sha256(completed.stderr).hexdigest(), "stderr_bytes": len(completed.stderr)}
        if name.startswith("start-new-") and completed.returncode != 0:
            # Keep the original failure useful even when finally removes its
            # containers. Only fixed categories leave memory, never log text.
            categories = {
                "DEPENDENCY_FAILURE": rb"dependency|depends on|required by service|is disabled",
                "UNHEALTHY": rb"unhealthy|healthcheck",
                "PERMISSION_DENIED": rb"permission denied|operation not permitted",
                "READ_ONLY_FILESYSTEM": rb"read-only file system",
                "MOUNT_FAILURE": rb"invalid mount|error mounting|mount denied|no such file or directory",
                "PORT_IN_USE": rb"address already in use|port is already allocated",
                "NETWORK_MISSING": rb"network .*not found|declared as external.*could not be found",
                "CONTAINER_EXITED": rb"exited|exit code|not running",
                "INVALID_OPTION": rb"unknown flag|unrecognized option|unknown option",
            }
            event["stderr_classes"] = [key for key, pattern in categories.items()
                                       if re.search(pattern, completed.stderr, re.IGNORECASE)] or ["UNCLASSIFIED"]
        atomic_json(self.output / "events" / (f"{time.time_ns()}-{self.sequence:04d}-" + name + ".json"), event)
        if budget and budget.active:
            budget.remaining()
        if check:
            require(completed.returncode == 0, name + " failed")
        return completed

    def compose(self, project, name, args, **kwargs):
        return self.command(name, self.compose_argv(project, args), **kwargs)

    def compose_argv(self, project, args):
        command = self.docker + ["compose", "--project-name", project["name"], "--env-file", project["env_file"]]
        for path in project["compose_files"]:
            command += ["-f", path]
        return command + args

    def pipeline(self, name, producer, consumer):
        """Stream sensitive backup plaintext without a host temporary file."""
        start = utc()
        first = subprocess.Popen(producer, env=self.env, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
        try:
            second = subprocess.Popen(consumer, env=self.env, stdin=first.stdout, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            first.stdout.close()
            right = second.wait()
            left = first.wait()
        except BaseException:
            first.kill(); first.wait()
            raise
        self.sequence += 1
        atomic_json(self.output / "events" / (f"{time.time_ns()}-{self.sequence:04d}-" + name + ".json"),
                    {"operation": name, "start_utc": start, "end_utc": utc(),
                     "producer_exit_code": left, "consumer_exit_code": right,
                     "exit_code": 0 if left == right == 0 else 1})
        require(left == right == 0, name + " pipeline failed")

    def record(self, value):
        value = {**value, "source_head": self.config["candidate"]["head"],
                 "manifest_sha256": self.config["candidate"]["manifest_sha256"], "mode": self.config["mode"],
                 "config_sha256": hashlib.sha256(json.dumps(self.config, sort_keys=True, separators=(",", ":")).encode()).hexdigest()}
        if getattr(self, "operator_source", None):
            value["actual_operator_source"] = self.operator_source
        atomic_json(self.output / "history" / (str(time.time_ns()) + ".json"), value)
        # Attempt-only failures must never replace the supported recovery input.
        if "snapshot" in value:
            atomic_json(self.state / "deployment-record.json", value)

    def require_new_cutover(self):
        """Called under the operator lock, before any external operation."""
        path = self.state / "deployment-record.json"
        if not path.exists():
            return
        prior = read_public_json(path)
        allowed = isinstance(prior, dict) and (
            (prior.get("status") == "ROLLED_BACK" and isinstance(prior.get("snapshot"), dict) and bool(prior["snapshot"])) or
            (prior.get("status") == "PREFLIGHT_FAILED" and "snapshot" not in prior))
        if not allowed:
            self.record({"status": "CUTOVER_REJECTED", "exit_code": 1, "end_utc": utc(),
                         "reason": "existing deployment requires explicit rollback or recovery"})
            raise OperatorError("cutover already committed or recovery is incomplete; use the matching rollback configuration")

    def projects(self, side):
        return self.config[side]["projects"]

    def verify_local_engine(self):
        context = self.command("docker-context", self.docker + ["context", "inspect", self.config["docker"]["context"]])
        metadata = json.loads(context.stdout)
        require(len(metadata) == 1 and metadata[0]["Endpoints"]["docker"]["Host"] in LOCAL_ENDPOINTS,
                "remote Docker is forbidden")

    def artifact_preflight(self):
        self.verify_local_engine()
        candidate = self.config["candidate"]
        require(digest(candidate["manifest"]) == candidate["manifest_sha256"], "candidate manifest changed")
        manifest = read_public_json(candidate["manifest"])
        require(manifest.get("source", {}).get("gitHead") == candidate["head"] and manifest.get("source", {}).get("gitDirty") is False,
                "manifest is not bound to the exact clean candidate")
        from operator_source import verify as verify_operator_source
        self.operator_source = verify_operator_source(Path(__file__).resolve().parents[2], manifest["source"])
        atomic_json(self.output / "operator-source.json", self.operator_source)
        manifest_images = {row["name"]: row["imageId"] for row in manifest["images"]}
        for project in self.projects("candidate"):
            for entry in project["services"].values():
                role = "source-agent" if entry["role"] in STREAM_ROLES else entry["role"]
                require(manifest_images.get(role) == entry["image_id"], "runtime image does not match source manifest")
        for project in self.projects("candidate"):
            resolved = json.loads(self.compose(project, "config-" + project["name"], ["config", "--format", "json"]).stdout)
            require_resolved_images(project, resolved, lambda reference: self.command("resolved-image-" + project["kind"],
                                    self.docker + ["image", "inspect", reference, "--format", "{{.Id}}"]).stdout.decode().strip())
            if project["kind"] == "unified":
                prerequisites = {"services": {name: {"image_id": manifest_images.get(role)} for name, role in
                    (("migrate", "platform-migrate"), ("invoice-migrate", "invoice-tools"), ("invoice-permissions", "invoice-postgres"))}}
                require_resolved_images(prerequisites, resolved, lambda reference: self.command("resolved-prerequisite-image",
                                        self.docker + ["image", "inspect", reference, "--format", "{{.Id}}"]).stdout.decode().strip())
            for service, entry in project["services"].items():
                actual = self.command("image-" + project["kind"] + "-" + service,
                                      self.docker + ["image", "inspect", entry["image_id"], "--format", "{{.Id}}"])
                require(actual.stdout.decode().strip() == entry["image_id"], "required image is absent or changed")

    def preflight(self):
        require_old_inputs(self.config)
        self.artifact_preflight()
        from host_nginx import HostNginx
        nginx_proof = HostNginx(self).preflight()
        atomic_json(self.output / "host-nginx-preflight.json", nginx_proof)
        from preflight import run as host_preflight
        original_inventory = self.inventory("previous")
        self.verify_original_plan(original_inventory)
        require_original_data(original_inventory, self.resolved_mount_inventory())
        takeover_ports = approved_loopback_ports(original_inventory, set(self.config["host_preflight"]["required_loopback_ports"]))
        host_result = host_preflight(self, self.config, allowed_occupied_ports=takeover_ports)
        inherited_preflight_pass(host_result, self.config["mode"])
        atomic_json(self.output / "host-preflight.json", host_result)
        candidate = self.config["candidate"]
        approvals = self.config["approvals"]
        require(set(approvals) == {"rehearsal_record", "mfa_query_record", "max_age_hours", "minimum_free_bytes"}, "approval contract is incomplete")
        require(type(approvals["max_age_hours"]) is int and 1 <= approvals["max_age_hours"] <= 24, "backup age limit is invalid")
        require(type(approvals["minimum_free_bytes"]) is int and approvals["minimum_free_bytes"] > 0, "disk requirement is missing")
        import shutil
        require(shutil.disk_usage(self.state.parent).free >= approvals["minimum_free_bytes"], "insufficient free host disk")
        for project in self.projects("previous"):
            self.compose(project, "config-" + project["name"], ["config", "--quiet"])
            for service, entry in project["services"].items():
                actual = self.command("image-" + project["kind"] + "-" + service,
                                      self.docker + ["image", "inspect", entry["image_id"], "--format", "{{.Id}}"])
                require(actual.stdout.decode().strip() == entry["image_id"], "required image is absent or changed")
        from restore import verify_backups
        backup_proof = verify_backups(self, self.config["backups"], approvals["max_age_hours"])
        rehearsal = read_public_json(approvals["rehearsal_record"])
        expected_mode = "local-synthetic" if self.config["mode"] == "local-synthetic" else "server-rehearsal"
        require(rehearsal.get("status") == "PASS" and rehearsal.get("mode") == expected_mode and rehearsal.get("source_head") == candidate["head"] and
                rehearsal.get("manifest_sha256") == candidate["manifest_sha256"] and rehearsal.get("backups") == backup_proof,
                "the exact candidate and signed backups lack a completed rehearsal")
        require(rehearsal.get("cleanup_complete") is True, "rehearsal cleanup was not verified")
        require_recent_record(rehearsal, approvals["max_age_hours"], "end_utc")
        mfa = read_public_json(approvals["mfa_query_record"])
        require(mfa.get("mode") == expected_mode and mfa.get("status") == "PASS" and mfa.get("role") == "finance.read" and
                type(mfa.get("total")) is int and type(mfa.get("totp_registered")) is int and 0 < mfa["totp_registered"] <= mfa["total"] and
                (mfa["totp_registered"] == mfa["total"] or mfa.get("unregistered_disposition") == "reviewed-lockout"),
                "MFA census or unregistered staff disposition is missing")
        require_recent_record(mfa, approvals["max_age_hours"], "end_utc")

    def inventory(self, side):
        result = []
        for project in self.projects(side):
            expected = project["services"]
            actual_services = self.compose(project, "runtime-services-" + project["kind"], ["ps", "--status", "running", "--services"]).stdout.decode().split()
            require(set(actual_services) == set(expected) and len(actual_services) == len(expected), "unexpected or missing running service in reviewed deployment")
            for service, entry in expected.items():
                ids = self.compose(project, "container-id-" + project["kind"] + "-" + service, ["ps", "--all", "-q", service]).stdout.decode().split()
                require(len(ids) == 1, "expected exactly one original container per service")
                fmt = '{"image":{{json .Image}},"running":{{json .State.Running}},"exit_code":{{json .State.ExitCode}},"health":{{with index .State "Health"}}{{json .Status}}{{else}}"none"{{end}},"project":{{json (index .Config.Labels "com.docker.compose.project")}},"service":{{json (index .Config.Labels "com.docker.compose.service")}},"ports":{{json .HostConfig.PortBindings}},"mounts":[{{range $i,$m := .Mounts}}{{if $i}},{{end}}{"type":{{json $m.Type}},"source":{{json $m.Source}},"target":{{json $m.Destination}},"rw":{{json $m.RW}}}{{end}}]}'
                if side == "previous":
                    fmt = fmt[:-1] + ',"compose_files":{{json (index .Config.Labels "com.docker.compose.project.config_files")}},"working_dir":{{json (index .Config.Labels "com.docker.compose.project.working_dir")}}}'
                state = json.loads(self.command("container-state-" + project["kind"] + "-" + service, self.docker + ["inspect", ids[0], "--format", fmt]).stdout)
                require(state["project"] == project["name"] and state["service"] == service and state["image"] == entry["image_id"], "original container identity changed")
                require(state["running"] is True and state["exit_code"] == 0 and state["health"] in ("none", "healthy"), "a required runtime container is not running and healthy")
                if side == "previous": require_original_compose_labels(project, state)
                result.append({"project": project["name"], "service": service, "role": entry["role"], "image_id": state["image"], "ports": state["ports"], "mounts": sorted(state["mounts"], key=lambda m: m["target"])})
                if side == "previous":
                    result[-1]["compose_provenance"] = {"config_files": state["compose_files"], "working_dir": state["working_dir"]}
        return result

    def resolved_mount_inventory(self, side="candidate"):
        rows = []
        if side == "previous": self.legacy_secret_inputs = []
        for project in self.projects(side):
            resolved = json.loads(self.compose(project, "planned-data-" + project["kind"], ["config", "--format", "json"]).stdout)
            names = {key: value.get("name", project["name"] + "_" + key) for key, value in resolved.get("volumes", {}).items()}
            for service, entry in project["services"].items():
                mounts = []
                for value in resolved["services"][service].get("volumes", []):
                    if value["type"] not in ("volume", "bind"):
                        continue
                    source = value["source"]
                    if value["type"] == "volume":
                        observed = self.command("original-volume-" + service,
                            self.docker + ["volume", "inspect", names.get(source, source), "--format", "{{.Mountpoint}}"])
                        source = observed.stdout.decode().strip()
                        require(source, "original named volume has no actual mountpoint")
                    mounts.append({"type": value["type"], "source": source, "target": value["target"], "rw": not value.get("read_only", False)})
                # Compose file-backed secrets/configs are real read-only bind
                # mounts too. Compare paths only; never read their contents.
                for kind in ("secrets", "configs"):
                    for value in resolved["services"][service].get(kind, []):
                        value = {"source": value} if isinstance(value, str) else value
                        definition = resolved.get(kind, {}).get(value["source"], {})
                        item = resolved["services"][service]
                        secret = "database__postgres_password"
                        target = "/run/secrets/" + secret
                        legacy = (side == "previous" and project["kind"] == "platform" and entry["role"] == "platform-postgres" and
                                  kind == "secrets" and value["source"] == secret and value.get("target", secret) in (secret, target) and
                                  set(value).issubset({"source", "target", "uid", "gid", "mode"}) and
                                  str(value.get("uid", "0")) == str(value.get("gid", "0")) == "0" and value.get("mode", 0o444) == 0o444 and
                                  definition.get("environment") == "DATABASE_PASSWORD" and set(definition).issubset({"environment", "name"}) and
                                  definition.get("name", project["name"] + "_" + secret) == project["name"] + "_" + secret and
                                  item.get("environment", {}).get("POSTGRES_PASSWORD_FILE") == target and
                                  "POSTGRES_PASSWORD" not in item.get("environment", {}) and not item.get("read_only", False))
                        if legacy:
                            # Compose injects this one historical secret into the
                            # container filesystem; inventing a bind would be false.
                            require(not item.get("tmpfs") and all(
                                m.get("target") and not (target == m["target"] or target.startswith(m["target"].rstrip("/") + "/"))
                                for m in item.get("volumes", [])), "legacy PG secret injection is shadowed by a mount")
                            self.legacy_secret_inputs.append({"project": project["name"], "service": service,
                                "type": "compose-environment-injected", "secret": secret, "environment": "DATABASE_PASSWORD", "target": target,
                                "env_file": project["env_file"], "env_sha256": digest(project["env_file"]),
                                "compose": [{"path": f, "sha256": digest(f)} for f in project["compose_files"]]})
                            continue
                        require(isinstance(definition.get("file"), str) and definition["file"] and not definition.get("external") and
                                "environment" not in definition and "content" not in definition,
                                "original secret/config must have a verifiable file mount")
                        target = value.get("target", value["source"])
                        if not target.startswith("/"):
                            target = ("/run/secrets/" if kind == "secrets" else "/") + target
                        mounts.append({"type": "bind", "source": definition["file"], "target": target, "rw": False})
                rows.append({"project": project["name"], "service": service, "role": entry["role"], "mounts": mounts})
        return rows

    def verify_original_plan(self, original):
        for project in self.projects("previous"):
            resolved = json.loads(self.compose(project, "original-resolved-" + project["kind"], ["config", "--format", "json"]).stdout)
            require_resolved_images(project, resolved, lambda reference: self.command("original-resolved-image-" + project["kind"],
                                    self.docker + ["image", "inspect", reference, "--format", "{{.Id}}"]).stdout.decode().strip())
        require_mount_inventory(original, self.resolved_mount_inventory("previous"))

    def ledger_snapshot(self, side):
        descriptors = self.config[side]["databases"]
        require(set(descriptors) == {"platform", "invoice"}, "both database ledger targets are mandatory")
        queries = {"platform": ["SELECT version,dirty FROM public.schema_migrations ORDER BY version", "SELECT line,version,created_at FROM public.river_migration ORDER BY line,version"],
                   "invoice": ["SELECT name,checksum,applied_at FROM schema_migrations ORDER BY name"]}
        hashes = {}
        for domain, row in descriptors.items():
            require(set(row) == {"project", "service", "database", "owner"} and all(NAME.fullmatch(row[k]) for k in row), "invalid database ledger target")
            project = next((p for p in self.projects(side) if p["name"] == row["project"]), None)
            require(project is not None and project["services"].get(row["service"], {}).get("role") == domain + "-postgres", "ledger target has wrong domain")
            hashes[domain] = []
            for i, query in enumerate(queries[domain]):
                result = self.compose(project, "ledger-" + side + "-" + domain + "-" + str(i), ["exec", "-T", row["service"], "psql", "-X", "-v", "ON_ERROR_STOP=1", "-U", row["owner"], "-d", row["database"], "-c", "COPY (" + query + ") TO STDOUT WITH CSV HEADER"])
                require(result.stdout.count(b"\n") >= 2, "database migration ledger is empty")
                hashes[domain].append(hashlib.sha256(result.stdout).hexdigest())
        return hashes

    def platform_permissions_snapshot(self, side):
        row = self.config[side]["databases"]["platform"]
        project = next(p for p in self.projects(side) if p["name"] == row["project"])
        # This records the existing platform policy, not a new DBR1 rollout.
        # pg_roles projection intentionally excludes password-related fields.
        queries = [
            "SELECT rolname,rolsuper,rolinherit,rolcreaterole,rolcreatedb,rolcanlogin,rolreplication,rolconnlimit,rolbypassrls FROM pg_roles ORDER BY rolname",
            "SELECT r.rolname AS role,m.rolname AS member,a.admin_option,a.inherit_option,a.set_option FROM pg_auth_members a JOIN pg_roles r ON r.oid=a.roleid JOIN pg_roles m ON m.oid=a.member ORDER BY r.rolname,m.rolname",
            "SELECT n.nspname,c.relname,c.relkind,pg_get_userbyid(c.relowner) AS owner,c.relacl FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname NOT LIKE 'pg_%' AND n.nspname<>'information_schema' ORDER BY n.nspname,c.relname,c.relkind",
            "SELECT nspname,pg_get_userbyid(nspowner) AS owner,nspacl FROM pg_namespace WHERE nspname NOT LIKE 'pg_%' AND nspname<>'information_schema' ORDER BY nspname",
            "SELECT n.nspname,p.proname,pg_get_function_identity_arguments(p.oid) AS args,pg_get_userbyid(p.proowner) AS owner,p.proacl FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname NOT LIKE 'pg_%' AND n.nspname<>'information_schema' ORDER BY n.nspname,p.proname,pg_get_function_identity_arguments(p.oid)"]
        hashes = []
        for i, query in enumerate(queries):
            output = self.compose(project, "platform-existing-permissions-" + str(i), ["exec", "-T", row["service"], "psql", "-X", "-v", "ON_ERROR_STOP=1", "-U", row["owner"], "-d", row["database"], "-c", "COPY (" + query + ") TO STDOUT WITH CSV HEADER"]).stdout
            require(output, "platform permission catalog is missing")
            hashes.append(hashlib.sha256(output).hexdigest())
        return hashes

    def snapshot(self):
        from host_nginx import HostNginx
        input_snapshot = require_old_inputs(self.config)
        rows = self.inventory("previous")
        require(sum(r["role"] not in OLD_ROLES["platform"] for r in rows) == 18, "old invoice inventory is not eighteen actual containers")
        snapshot = {"actual_invoice_containers": 18, "containers": rows, "host_nginx": HostNginx(self).snapshot(), "ledger_hashes": self.ledger_snapshot("previous"), "platform_permissions": self.platform_permissions_snapshot("previous"),
                "legacy_secret_inputs": getattr(self, "legacy_secret_inputs", []), "old_input_snapshot": input_snapshot,
                "project_files": [{"name": p["name"], "compose": [{"path": f, "sha256": digest(f)} for f in p["compose_files"]],
                                   "env_file": p["env_file"], "env_sha256": digest(p["env_file"])} for p in self.projects("previous")]}
        require_snapshot_files(snapshot)
        return snapshot

    def assert_stopped(self, side):
        for project in self.projects(side):
            running = self.compose(project, "verify-stopped-" + project["kind"], ["ps", "--status", "running", "-q"]).stdout.strip()
            require(not running, "writers remain running in the stopped deployment")

    def stop_old(self):
        for kind in ("sources", "invoice", "platform", "idp"):
            project = next(p for p in self.projects("previous") if p["kind"] == kind)
            self.compose(project, "stop-old-" + kind, ["stop", "--timeout", "30", *project["services"]])
        self.assert_stopped("previous")
        from preflight import check_ports
        check_ports(self, self.config)

    def start_new_databases(self):
        project = next(p for p in self.projects("candidate") if p["kind"] == "unified")
        services = [name for name, entry in project["services"].items() if entry["role"] in ("platform-postgres", "invoice-postgres")]
        require(len(services) == 2, "both candidate databases required")
        self.compose(project, "start-new-databases", ["up", "-d", "--wait", "--wait-timeout", "120", "--no-deps", "--pull", "never", *services])

    def jobs(self, descriptors, prefix):
        policies = {"prerequisite": ("candidate", "unified"), "verify-frozen-snapshot": ("candidate", None), "restore-permissions": ("previous", None)}
        require(prefix in policies, "unknown lifecycle job purpose")
        side, kind = policies[prefix]
        # Resolve the entire list before dispatch, including its final entry.
        for project, service in job_plan(self.config, descriptors, side, kind):
            self.compose(project, prefix + "-" + service, ["run", "--rm", "--no-deps", "--pull", "never", service])

    def migrate_and_permissions(self):
        rows = self.config["candidate"]["jobs"]
        candidate_job_plan(self.config)
        self.jobs(rows, "prerequisite")
        record_path = self.state / "deployment-record.json"
        if record_path.is_file():
            record = read_public_json(record_path)
            if record.get("status") == "PREPARED":
                require(self.ledger_snapshot("candidate") == record["snapshot"]["ledger_hashes"], "migration changed the recorded ledger; in-place image rollback is incompatible")
                require(self.platform_permissions_snapshot("candidate") == record["snapshot"]["platform_permissions"], "platform existing permissions changed during cutover")

    def start_new(self):
        # A restored API can only become strictly ready after fresh source
        # heartbeats arrive. Start both projects before waiting on any latch.
        require(not getattr(self, "readiness_budget", None), "candidate startup budget cannot be reset")
        self.readiness_budget = ReadinessBudget(self.output)
        try:
            for kind in ("unified", "sources"):
                project = next(p for p in self.projects("candidate") if p["kind"] == kind)
                self.compose(project, "start-new-" + kind, ["up", "-d", "--no-deps", "--pull", "never", *project["services"]])
            for kind in ("unified", "sources"):
                project = next(p for p in self.projects("candidate") if p["kind"] == kind)
                # Compose takes integral seconds; the actual process timeout
                # remains the exact fractional remainder of the same deadline.
                seconds = max(1, int(self.readiness_budget.remaining()))
                self.compose(project, "wait-new-" + kind, ["up", "-d", "--wait", "--wait-timeout", str(seconds), "--no-recreate", "--no-deps", "--pull", "never", *project["services"]])
        except BaseException as error:
            self.readiness_budget.finish("DEADLINE_EXCEEDED" if isinstance(error, ReadinessTimeout) else "FAILED")
            raise

    def check_new(self):
        budget = getattr(self, "readiness_budget", None)
        require(budget and budget.active, "candidate readiness requires its original startup budget")
        try:
            self.inventory("candidate")
            budget.remaining()
            self.http_ready(self.config["candidate"]["ready_url"], all_modules=True)
        except BaseException as error:
            budget.finish("DEADLINE_EXCEEDED" if isinstance(error, ReadinessTimeout) else "FAILED")
            raise

    def http_ready(self, url, *, all_modules=False):
        from urllib.parse import urlsplit
        value = urlsplit(url)
        require(value.scheme == "http" and value.hostname in ("127.0.0.1", "localhost", "::1") and value.path == "/readyz" and not value.username and not value.password and not value.query and not value.fragment,
                "readiness must use the exact local-host endpoint")
        if all_modules:
            budget = self.readiness_budget
            while True:
                budget.remaining()
                result = self.command("candidate-readiness-probe", [sys.executable, "-c", READINESS_PROBE, url], check=False, timeout=5)
                try:
                    if result.returncode == 0:
                        status, body = result.stdout.split(b"\n", 1)
                        return budget.observe(int(status), body)
                except ReadinessTimeout:
                    raise
                except (OperatorError, ValueError):
                    pass
                time.sleep(min(2, budget.remaining()))
        class NoRedirect(urllib.request.HTTPRedirectHandler):
            def redirect_request(self, *args, **kwargs): return None
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
        deadline = time.monotonic() + 120
        while True:
            try:
                with opener.open(url, timeout=5) as response:
                    require(response.status == 200, "readiness HTTP status is not 200")
                    body = response.read(65537)
                    require(len(body) <= 65536, "readiness response is too large")
                    payload = json.loads(body)
                    if all_modules: require_ready(payload)
                    return payload
            except (OSError, ValueError, OperatorError):
                if time.monotonic() >= deadline:
                    raise OperatorError("local readiness did not recover") from None
                time.sleep(2)

    def precheck_new(self):
        from restore import preview_candidate
        return preview_candidate(self)

    def switch_nginx(self, snapshot):
        from host_nginx import HostNginx
        HostNginx(self).apply(snapshot["host_nginx"])

    def restore_nginx(self, snapshot):
        from host_nginx import HostNginx
        HostNginx(self, recovering=True).apply(snapshot["host_nginx"], rollback=True)

    def smoke(self):
        from smoke import REQUIRED, validate_config as validate_smoke
        script = Path(__file__).with_name("smoke.py")
        require(script.is_file(), "the actual HTTP smoke implementation is missing")
        config = read_public_json(self.config["smoke_config"])
        validate_smoke(config)
        require(config["mode"] == self.config["mode"], "smoke mode differs from the actual operation")
        self.command("http-smoke", [sys.executable, str(script), "--config", self.config["smoke_config"], "--output", str(self.output / "smoke.json")])
        result = read_public_json(self.output / "smoke.json")
        required_steps = list(REQUIRED)
        expected_digest = hashlib.sha256(json.dumps(config, sort_keys=True, separators=(",", ":")).encode()).hexdigest()
        require(result.get("mode") == self.config["mode"] and result.get("origins") == config["origins"] and
                result.get("configuration_sha256") == expected_digest and result.get("financial_writes_permitted") is False,
                "smoke evidence does not match the read-only configured operation")
        require(result.get("status") == "PASS" and result.get("exit_code") == 0 and result.get("evidence_kind") == "actual-http-smoke" and
                [s.get("name") for s in result.get("steps", [])] == required_steps and
                all(s.get("status") == "PASS" and s.get("exit_code") == 0 for s in result["steps"]), "actual HTTP smoke did not pass every required step")

    def preview_smoke(self, deployment_config):
        # Only restore.rehearse invokes this after frozen mount/ownership,
        # source-state and health checks. The entry rechecks that boundary.
        from preview_smoke import run_verified
        return run_verified(self, deployment_config)

    def stop_new(self):
        for kind in ("sources", "unified"):
            project = next(p for p in self.projects("candidate") if p["kind"] == kind)
            self.compose(project, "stop-new-" + kind, ["stop", "--timeout", "30", *project["services"]])
        self.assert_stopped("candidate")

    def restore_permissions(self):
        job_plan(self.config, self.config["previous"]["permission_jobs"], "previous")
        # Identical ledgers include 0032 unchanged. Do not remove ledger rows
        # merely to coax an older binary into starting.
        require(self.config["previous"]["migration_digest"] == self.config["candidate"]["migration_digest"], "rollback ledger is incompatible")
        record = read_public_json(self.state / "deployment-record.json")
        require_snapshot_files(record["snapshot"])
        require(record["snapshot"].get("old_input_snapshot") == require_old_inputs(self.config), "recovery record belongs to another old input snapshot")
        self.verify_original_plan(record["snapshot"]["containers"])
        for kind in ("platform", "invoice", "idp"):
            project = next(p for p in self.projects("previous") if p["kind"] == kind)
            services = [name for name, row in project["services"].items() if row["role"] in ("platform-postgres", "invoice-postgres", "keycloak-postgres")]
            self.compose(project, "restore-old-database-" + kind, ["up", "-d", "--wait", "--wait-timeout", "120", "--no-deps", "--pull", "never", *services])
        self.jobs(self.config["previous"]["permission_jobs"], "restore-permissions")
        record = read_public_json(self.state / "deployment-record.json")
        require(self.ledger_snapshot("previous") == record["snapshot"]["ledger_hashes"], "rollback ledger differs from the frozen original; keep writers stopped")
        require(self.platform_permissions_snapshot("previous") == record["snapshot"]["platform_permissions"], "platform permissions differ from the frozen original; keep writers stopped")

    def start_old(self):
        require_old_inputs(self.config)
        for kind in ("idp", "platform", "invoice", "sources"):
            project = next(p for p in self.projects("previous") if p["kind"] == kind)
            self.compose(project, "restore-old-" + kind, ["up", "-d", "--wait", "--wait-timeout", "300", "--no-deps", "--pull", "never", *project["services"]])

    def check_old(self, snapshot):
        require(self.inventory("previous") == snapshot["containers"], "old runtime role/image inventory differs after rollback")
        require_snapshot_files(snapshot)
        urls = self.config["previous"]["ready_urls"]
        require(set(urls) == {"platform", "invoice"}, "both old readiness endpoints required")
        for url in urls.values(): self.http_ready(url)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("operation", choices=("cutover", "rollback", "rehearse", "cleanup"))
    parser.add_argument("head", nargs="?")
    parser.add_argument("--config", required=True)
    parser.add_argument("--dry-run", action="store_true")
    args = parser.parse_args()
    lock = None
    exit_code = 1
    try:
        from operator_lock import acquire_lock
        config = validate_config(read_public_json(args.config))
        if args.operation == "cutover":
            require(args.head == config["candidate"]["head"], "requested SHA differs from reviewed candidate")
        else:
            require(args.head is None, "unexpected positional argument")
        if args.dry_run:
            result = {"status": "DRY_RUN", "executed": False, "operation": args.operation, "source_head": config["candidate"]["head"],
                      "previous_invoice_containers": 18, "candidate_projects": [p["name"] for p in config["candidate"]["projects"]],
                      "original_projects": [p["name"] for p in config["previous"]["projects"]]}
            if args.operation == "cutover": result.update(cutover(None, dry_run=True))
            elif args.operation == "rollback": result.update(rollback(None, {}, dry_run=True))
            print(json.dumps(result))
            return 0
        state = plain_path(config["state_root"], exists=False, directory=True)
        state.mkdir(parents=True, exist_ok=True)
        lock = acquire_lock(state)
        driver = DockerDriver(config, record_root=state / (args.operation + "-" + str(time.time_ns())))
        if args.operation == "cutover":
            driver.require_new_cutover()
        driver.verify_local_engine()
        if args.operation == "cutover":
            result = cutover(driver)
        elif args.operation == "rollback":
            prior = read_public_json(state / "deployment-record.json")
            config_sha = hashlib.sha256(json.dumps(config, sort_keys=True, separators=(",", ":")).encode()).hexdigest()
            require(prior.get("source_head") == config["candidate"]["head"] and prior.get("manifest_sha256") == config["candidate"]["manifest_sha256"] and prior.get("mode") == config["mode"] and prior.get("config_sha256") == config_sha and "snapshot" in prior,
                    "rollback has no matching durable deployment snapshot")
            result = rollback(driver, prior["snapshot"])
        else:
            from restore import rehearse, cleanup
            result = rehearse(driver) if args.operation == "rehearse" else cleanup(driver)
        print(json.dumps({"status": result["status"], "exit_code": result.get("exit_code", 0)}))
        exit_code = result.get("exit_code", 0)
    except (OperatorError, ValueError, KeyError, OSError):
        print("unified operator failed; see non-secret operation records", file=sys.stderr)
    finally:
        if lock is not None:
            try:
                lock.release()
            except OSError:
                print("operator lock release could not be verified; owner record retained", file=sys.stderr)
                exit_code = 1
    return exit_code


if __name__ == "__main__":
    sys.modules.setdefault("lifecycle", sys.modules[__name__])
    sys.exit(main())

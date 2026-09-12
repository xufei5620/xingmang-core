"""Signed frozen-copy restore. No backup signing or production writes occur here."""
import copy
import datetime as dt
import hashlib
import json
from pathlib import Path
import re

from lifecycle import (BACKUP_ANCHORS, IMAGE_ID, NAME, STREAM_ROLES, DockerDriver, OperatorError, ReadinessTimeout, end_readiness_on_failure, local_postgres_exec, require_postgres_socket_unshadowed,
                       atomic_json, audit_json, candidate_job_plan, digest, inherited_preflight_pass, job_plan, plain_path, read_public_json, recovery_audit_failure, recovery_audit_status, require, utc)


SUFFIXES = {"database": ".postgres.dump.age", "documents": ".documents.tar.age",
            "source_state": ".source-state.tar.age", "metadata": ".metadata.tar.age",
            "keycloak": ".keycloak.dump.age"}


def parse_manifest(text, expected):
    rows = {}
    for line in text.splitlines():
        match = re.fullmatch(r"([a-f0-9]{64})  ([A-Za-z0-9][A-Za-z0-9._-]{0,254})", line)
        require(match is not None, "signed backup manifest syntax is invalid")
        checksum, name = match.groups()
        require(name in expected and name not in rows, "signed backup contains an extra or duplicate component")
        rows[name] = checksum
    require(set(rows) == set(expected) and rows, "signed backup components are incomplete")
    return rows


def capture_time(domain, names, max_age_hours, now=None):
    require(type(max_age_hours) is int and 1 <= max_age_hours <= 24, "invalid backup freshness limit")
    timestamps = []
    for name in names:
        match = re.fullmatch(re.escape(domain) + r"-(\d{8}T\d{6}Z)\.[a-z.-]+\.age", name)
        require(match is not None, "backup names must preserve the signed domain timestamp")
        try:
            timestamps.append(dt.datetime.strptime(match[1], "%Y%m%dT%H%M%SZ").replace(tzinfo=dt.timezone.utc))
        except ValueError:
            raise OperatorError("invalid signed backup timestamp") from None
    require(timestamps and len(set(timestamps)) == 1, "backup components do not share one capture timestamp")
    now = now or dt.datetime.now(dt.timezone.utc)
    age = (now - timestamps[0]).total_seconds()
    require(0 <= age <= max_age_hours * 3600, "signed backup is stale or in the future")
    return timestamps[0].isoformat()


def validate_signers(domain, text):
    principal, namespace = BACKUP_ANCHORS[domain]
    pattern = re.escape(principal) + r'\s+namespaces="' + re.escape(namespace) + r'"\s+ssh-ed25519\s+[A-Za-z0-9+/=]+(?:\s+[^\r\n]*)?'
    lines = [line for line in text.splitlines() if line.strip() and not line.lstrip().startswith("#")]
    require(lines and all(re.fullmatch(pattern, line) for line in lines), "backup allowed_signers must contain only the exact domain-bound Ed25519 keys")


def verify_backups(driver, descriptors, max_age_hours):
    require(set(descriptors) == set(BACKUP_ANCHORS), "both signed backup domains are mandatory")
    proof = {}
    for domain in ("platform", "invoice"):
        row = descriptors[domain]
        require(set(row) == {"manifest", "signature", "allowed_signers", "identity_file", "age_binary", "ssh_keygen_binary", "components"}, "backup descriptor keys are invalid")
        paths = {key: plain_path(row[key]) for key in ("manifest", "signature", "allowed_signers", "identity_file", "age_binary", "ssh_keygen_binary")}
        # An identity is only statted. The age process alone receives its path.
        required = {"database", "metadata"} | ({"documents", "source_state"} if domain == "invoice" else set())
        components = row["components"]
        require(set(components) in (required, required | {"keycloak"}) if domain == "invoice" else set(components) == required, "backup component roles are invalid")
        names = {}
        for kind, value in components.items():
            path = plain_path(value)
            require(path.parent == paths["manifest"].parent and path.name.endswith(SUFFIXES[kind]) and path.stat().st_size > 0, "signed backup component path is invalid")
            names[path.name] = path
        require(len(names) == len(components), "backup component path is duplicated")
        require(paths["manifest"].stat().st_size <= 65536 and paths["allowed_signers"].stat().st_size <= 65536, "signature public input is too large")
        validate_signers(domain, paths["allowed_signers"].read_text(encoding="utf-8"))
        signed_bytes = paths["manifest"].read_bytes()
        principal, namespace = BACKUP_ANCHORS[domain]
        # Authenticity precedes even parsing and hashing ciphertext, and always
        # precedes a call to age. A caller cannot substitute a different signer.
        driver.command("verify-" + domain + "-signature", [str(paths["ssh_keygen_binary"]), "-Y", "verify", "-f", str(paths["allowed_signers"]), "-I", principal, "-n", namespace, "-s", str(paths["signature"])], input_bytes=signed_bytes)
        checksums = parse_manifest(signed_bytes.decode("ascii"), set(names))
        captured = capture_time(domain, names, max_age_hours)
        for name, path in names.items():
            require(digest(path) == checksums[name], "signed backup ciphertext checksum mismatch")
        proof[domain] = {"manifest_sha256": hashlib.sha256(signed_bytes).hexdigest(), "signature_sha256": digest(paths["signature"]),
                         "allowed_signers_sha256": digest(paths["allowed_signers"]), "capture_utc": captured, "components": checksums}
    return proof


def require_owned_volume(metadata, expected_name, owner_id):
    require(metadata.get("Name") == expected_name and metadata.get("Labels", {}).get("xingmang.rehearsal.owner") == owner_id,
            "frozen-copy volume ownership does not match this rehearsal")


def finish_rehearsal(result, cleanup_complete):
    require(result.get("status") == "PASS" and cleanup_complete is True, "a rehearsal cannot pass before successful cleanup")
    return {**result, "cleanup_complete": True, "exit_code": 0}


def temporary_identity_map(value, state):
    paths = value.get("temporary_identity_paths", [])
    if not paths:
        return {}
    require(isinstance(paths, list) and len(paths) == 2 and len(set(paths)) == 2,
            "temporary age identities must cover both domains exactly once")
    folder = state / "tmpfs" / value["owner_id"]
    expected = {domain: folder / (domain + ".age-identity") for domain in BACKUP_ANCHORS}
    actual = {plain_path(path, exists=False) for path in paths}
    require(actual == set(expected.values()), "temporary identities must use the exact owned staging directory")
    return expected


def configure_temporary_identities(config, value, state):
    staged = temporary_identity_map(value, state)
    originals = {plain_path(row["identity_file"]).resolve() for row in config["backups"].values()}
    for domain, path in staged.items():
        require(path.resolve() not in originals, "an original identity cannot be a staged copy")
        config["backups"][domain]["identity_file"] = str(path)


def stage_temporary_identities(driver, value):
    staged = temporary_identity_map(value, driver.state)
    if not staged:
        return
    folder = next(iter(staged.values())).parent
    require(not folder.exists(), "identity staging directory must be new for this invocation")
    copy_binary = plain_path(value.get("identity_copy_binary", ""))
    folder.mkdir(mode=0o700, parents=True, exist_ok=False)
    # The exclusive new directory is journaled before any tool receives a key
    # path, so even a partially failed copy is covered by the armed finally.
    atomic_json(driver.output / "temporary-identity-ownership.json",
                {"owner_id": value["owner_id"], "paths": [str(p) for p in staged.values()]})
    for domain, path in staged.items():
        original = plain_path(driver.config["backups"][domain]["identity_file"])
        response = driver.command("stage-" + domain + "-identity", [str(copy_binary), "--", str(original), str(path)])
        require(response.returncode == 0 and path.is_file(), "temporary identity copy failed")


def cleanup_temporary_identities(driver, value):
    prior_recovering = getattr(driver, "_recovering_audit", False)
    driver._recovering_audit = True
    try:
        result = cleanup_temporary_identity_files(driver, value)
        errors = list(getattr(driver, "_recovery_audit_errors", []))
        if errors:
            result.update(audit_complete=False, audit_errors=errors)
        return result
    finally:
        driver._recovering_audit = prior_recovering


def cleanup_temporary_identity_files(driver, value):
    originals = {plain_path(row["identity_file"]).resolve() for row in driver.config["backups"].values()}
    staged = temporary_identity_map(value, driver.state)
    count = 0
    if staged:
        journal = driver.output / "temporary-identity-ownership.json"
        if not journal.is_file():
            require(not any(path.exists() for path in staged.values()), "unowned temporary identities must be preserved")
            return {"temporary_identities_shredded": 0, "original_identities_preserved": True}
        require(read_public_json(journal) == {"owner_id": value["owner_id"], "paths": [str(p) for p in staged.values()]},
                "temporary identity ownership differs from this invocation")
    for path in staged.values():
        require(path.resolve() not in originals, "cleanup cannot erase an original identity")
        if not path.exists():
            continue
        plain_path(str(path))
        response = driver.command("shred-temporary-identity", [str(plain_path(value["shred_binary"])), "--iterations=3", "--zero", "--remove=unlink", "--", str(path)])
        require(response.returncode == 0 and not path.exists(), "temporary identity was not successfully shredded and removed")
        count += 1
    if staged:
        folder = next(iter(staged.values())).parent
        if folder.exists():
            folder.rmdir()  # Non-recursive: preserve and fail on any unknown entry.
    require(all(path.is_file() for path in originals), "an original age identity is missing")
    return {"temporary_identities_shredded": count, "original_identities_preserved": True}


def rehearsal_driver(driver, *, recovering=False):
    value = copy.deepcopy(driver.config["rehearsal"])
    keys = {"owner_id", "projects", "jobs", "ready_url", "smoke_config", "volumes", "tools_image", "databases", "verification_jobs", "shred_binary", "temporary_identity_paths", "archive_tmpfs_bytes"}
    require(keys <= set(value) <= keys | {"readonly_input_volumes", "identity_copy_binary", "host_preflight", "seed"}, "rehearsal contract keys are incomplete")
    require(re.fullmatch(r"[a-f0-9]{32}", value["owner_id"]), "rehearsal owner must be a unique UUID without hyphens")
    require(IMAGE_ID.fullmatch(value["tools_image"]), "restore tools require an immutable image")
    required_volumes = {"platform_database", "invoice_database", "documents", "source_state", "invoice_metadata", "platform_metadata"}
    require(required_volumes <= set(value["volumes"]) <= required_volumes | {"scanner_socket", "clamav_database"}, "all frozen-copy volumes must be explicit")
    require(type(value["archive_tmpfs_bytes"]) is int and 134217728 <= value["archive_tmpfs_bytes"] <= 34359738368, "archive tmpfs must be explicitly bounded between 128 MiB and 32 GiB")
    volume_names = list(value["volumes"].values())
    require(len(volume_names) == len(set(volume_names)) and all(NAME.fullmatch(v) and v.startswith("xm-rehearsal-") for v in volume_names), "frozen-copy volume names must be unique and rehearsal-prefixed")
    require(len(value["projects"]) == 2 and {p["kind"] for p in value["projects"]} == {"unified", "sources"} and all(p["name"].startswith("xm-rehearsal-") for p in value["projects"]), "rehearsal projects must be independently named")
    require(not {p["name"] for p in value["projects"]}.intersection(p["name"] for p in driver.config["previous"]["projects"]), "frozen projects cannot reuse an original online project name")
    config = copy.deepcopy(driver.config)
    config["candidate"].update(projects=value["projects"], jobs=value["jobs"], ready_url=value["ready_url"], databases=value["databases"])
    candidate_job_plan(config)
    verification = job_plan(config, value["verification_jobs"], "candidate")
    require([service for _, service in verification] == ["verify-invoice-restore"], "the existing invoice document decrypt verification job is mandatory")
    config["smoke_config"] = value["smoke_config"]
    if "host_preflight" in value:
        config["host_preflight"] = copy.deepcopy(value["host_preflight"])
    configure_temporary_identities(config, value, driver.state)
    trial = DockerDriver(config, record_root=driver.output)
    trial._recovering_audit = recovering
    trial._recovery_audit_errors = []
    overlay_root = driver.output
    if recovering:
        journal = read_public_json(driver.state / "rehearsal-ownership.json")
        require(journal.get("owner_id") == value["owner_id"] and journal.get("volumes") == value["volumes"] and
                journal.get("projects") == [p["name"] for p in trial.projects("candidate")],
                "cleanup lacks its original ownership configuration")
        overlay_root = plain_path(journal.get("invocation"), directory=True)
    trial.verify_local_engine()
    for project in trial.projects("candidate"):
        resolved = json.loads(trial.compose(project, "ownership-config-" + project["kind"], ["config", "--format", "json"]).stdout)
        label = {"xingmang.rehearsal.owner": value["owner_id"]}
        overlay = {"services": {name: {"labels": label} for name in resolved["services"]},
                   "networks": {name: {"labels": label} for name, network in resolved.get("networks", {}).items() if network.get("external") is not True}}
        path = overlay_root / ("ownership-" + project["kind"] + ".json")
        if recovering:
            require(read_public_json(path) == overlay, "cleanup ownership overlay changed or is missing")
        else:
            atomic_json(path, overlay)
        project["compose_files"] = list(project["compose_files"]) + [str(path)]
    return trial, value


def volume_metadata(driver, name):
    data = driver.command("inspect-volume-" + name, driver.docker + ["volume", "inspect", name])
    values = json.loads(data.stdout)
    require(len(values) == 1, "invalid frozen-copy volume metadata")
    return values[0]


def require_isolated_networks(resolved, inspect_network, future_networks=None):
    networks = resolved.get("networks", {})
    require(networks, "frozen-copy networks must be explicit")
    for value in networks.values():
        if value.get("external") is True:
            require(value.get("name", "").startswith("xm-rehearsal-"), "frozen-copy external network is outside the task")
            future = (future_networks or {}).get(value["name"])
            if future is not None:
                # The main Compose project owns this fresh network. Its exact
                # name, explicit IPAM and internal flag were collected from the
                # same reviewed resolved inputs. Creation and owner checks stay
                # in the existing new-resource/cleanup lifecycle.
                require(future.get("internal") is True and bool(future.get("ipam", {}).get("config")),
                        "referenced future network requires explicit internal IPAM")
            else:
                require(inspect_network(value["name"]).get("Internal") is True, "frozen-copy external network permits egress")
        else:
            require(value.get("internal") is True, "all frozen-copy networks must be internal")
    for service in resolved["services"].values():
        require(service.get("network_mode", "none") == "none", "frozen-copy host or shared-container networking is forbidden")
        require(set(service.get("networks", {})) <= set(networks), "frozen-copy service references an unknown network")
        for port in service.get("ports", []):
            require(isinstance(port, dict) and port.get("host_ip") == "127.0.0.1", "frozen-copy published ports must bind exact loopback")


def invalidate_receipt(driver):
    receipt = driver.state / "rehearsal-pass.json"
    if receipt.is_file():
        original = plain_path(str(receipt))
        driver.output.mkdir(parents=True, exist_ok=True)
        with (driver.output / "prior-rehearsal-receipt.json").open("xb") as saved:
            saved.write(original.read_bytes())
    atomic_json(receipt, {"status": "RUNNING", "exit_code": 1, "start_utc": utc(), "invocation": str(driver.output)})


def bind_frozen_readiness_endpoint(driver, value):
    from rehearsal_endpoint import attest_frozen_ready_endpoint
    return attest_frozen_ready_endpoint(driver, value)


def assert_project_resources_absent(driver):
    for project in driver.projects("candidate"):
        selector = "label=com.docker.compose.project=" + project["name"]
        containers = driver.command("project-must-be-new-" + project["kind"], driver.docker + ["ps", "-aq", "--filter", selector]).stdout.strip()
        require(not containers, "frozen-copy project already has containers")
        networks = driver.command("project-networks-must-be-new-" + project["kind"], driver.docker + ["network", "ls", "-q", "--filter", selector]).stdout.strip()
        require(not networks, "frozen-copy project already has networks")
        resolved = json.loads(driver.compose(project, "new-network-config-" + project["kind"], ["config", "--format", "json"]).stdout)
        for logical, network in resolved.get("networks", {}).items():
            if network.get("external") is True: continue
            name = network.get("name", project["name"] + "_" + logical)
            require(driver.command("network-must-be-new-" + logical, driver.docker + ["network", "inspect", name], check=False).returncode != 0,
                    "frozen-copy managed network name already exists")


def require_resource_owner(labels, project, owner):
    require(isinstance(labels, dict) and labels.get("com.docker.compose.project") == project and labels.get("xingmang.rehearsal.owner") == owner,
            "cleanup refuses a container or network outside this rehearsal")


def verify_project_resource_owners(driver, value):
    for project in driver.projects("candidate"):
        selector = "label=com.docker.compose.project=" + project["name"]
        for kind, listing, fmt in (("container", ["ps", "-aq", "--filter", selector], "{{json .Config.Labels}}"),
                                   ("network", ["network", "ls", "-q", "--filter", selector], "{{json .Labels}}")):
            ids = driver.command("cleanup-owned-" + kind + "-" + project["kind"], driver.docker + listing).stdout.decode().split()
            for identifier in ids:
                labels = driver.command("cleanup-owner-" + kind, driver.docker + [kind, "inspect", identifier, "--format", fmt]).stdout
                require_resource_owner(json.loads(labels), project["name"], value["owner_id"])


def validate_frozen_mounts(driver, value):
    owned = set(value["volumes"].values())
    # These original inputs are consumed in place, never restored, modified or
    # included in the cleanup journal. Data and state always use fresh volumes.
    targets = {"source_credentials": {"/fixture"}, "staff_credentials": {"/run/xm/secrets", "/run/secrets"},
               "scanner_signatures": {"/var/lib/clamav", "/clamav-db"}}
    inputs = {}
    for row in value.get("readonly_input_volumes", []):
        require(isinstance(row, dict) and set(row) == {"name", "owner_id", "kind"}, "read-only input descriptor is invalid")
        name = row["name"]
        require(isinstance(name, str) and NAME.fullmatch(name) and name.startswith("xm-rehearsal-") and name not in owned and name not in inputs,
                "read-only input must be unique and separate from frozen data")
        require(row["kind"] in targets and isinstance(row["owner_id"], str) and re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_.-]{0,63}", row["owner_id"]), "read-only input kind or owner is invalid")
        require_owned_volume(volume_metadata(driver, name), name, row["owner_id"])
        inputs[name] = targets[row["kind"]]
    reviewed = [(project, json.loads(driver.compose(project, "frozen-config-" + project["kind"], ["config", "--format", "json"]).stdout))
                for project in driver.projects("candidate")]
    future_networks = {}
    for project, resolved in reviewed:
        for logical, network in resolved.get("networks", {}).items():
            if network.get("external") is True:
                continue
            name = network.get("name", project["name"] + "_" + logical)
            require(name not in future_networks, "fresh network must have exactly one owning project")
            future_networks[name] = network
    for project, resolved in reviewed:
        require_isolated_networks(resolved, lambda name: json.loads(driver.command("frozen-network-" + name,
                                  driver.docker + ["network", "inspect", name]).stdout)[0], future_networks)
        volume_map = {k: v.get("name", project["name"] + "_" + k) for k, v in resolved.get("volumes", {}).items()}
        seen = set()
        for service_name, service in resolved["services"].items():
            role=project['services'].get(service_name,{}).get('role')
            if role in ('platform-postgres','invoice-postgres'):
                require_postgres_socket_unshadowed(service.get('volumes',[]))
            for mount in service.get("volumes", []):
                if mount["type"] == "volume":
                    name = volume_map.get(mount["source"], mount["source"])
                    require(name in owned or (name in inputs and mount.get("read_only") is True and mount.get("target") in inputs[name]),
                            "rehearsal cannot use an original or unowned writable volume")
                    if name in owned: seen.add(name)
                elif mount["type"] == "bind":
                    require(mount.get("read_only") is True, "rehearsal host bind inputs must be read-only")
        require(seen, "rehearsal must mount explicit frozen-copy data")
        # The compose environment is deliberately never persisted or printed.


def create_frozen_volumes(driver, value):
    assert_project_resources_absent(driver)
    for name in value["volumes"].values():
        observed = driver.command("volume-must-be-new-" + name, driver.docker + ["volume", "inspect", name], check=False)
        require(observed.returncode != 0, "rehearsal volume already exists; cleanup or choose a new reviewed identity")
    atomic_json(driver.state / "rehearsal-ownership.json", {"schema": "xingmang.rehearsal.ownership/v1", "owner_id": value["owner_id"], "invocation": str(driver.output), "volumes": value["volumes"], "projects": [p["name"] for p in driver.projects("candidate")], "source_head": driver.config["candidate"]["head"]})
    for name in value["volumes"].values():
        driver.command("create-frozen-" + name, driver.docker + ["volume", "create", "--label", "xingmang.rehearsal.owner=" + value["owner_id"], name])
        require_owned_volume(volume_metadata(driver, name), name, value["owner_id"])


def decrypt_args(descriptor, kind):
    return [descriptor["age_binary"], "--decrypt", "-i", descriptor["identity_file"], descriptor["components"][kind]]


def clone_scanner_signatures(driver, value):
    target = value.get("volumes", {}).get("clamav_database")
    if target is None:
        return {"configured": False}
    sources = [row for row in value.get("readonly_input_volumes", []) if row["kind"] == "scanner_signatures"]
    require(len(sources) == 1, "a frozen scanner database requires one reviewed signature source")
    source = sources[0]
    require(source["name"] != target, "scanner signature source must remain separate from its writable copy")
    require_owned_volume(volume_metadata(driver, source["name"]), source["name"], source["owner_id"])
    require_owned_volume(volume_metadata(driver, target), target, value["owner_id"])
    # These are the three public ClamAV definition families. Never copy arbitrary
    # files or links. Preserve and compare attributes and bytes before the
    # original image's /init is allowed to use its own writable database copy.
    command = r'''set -eu
for stem in main daily bytecode; do
  count=0; file=
  for suffix in cvd cld; do
    item="$stem.$suffix"
    if [ -f "/source/$item" ] && [ ! -L "/source/$item" ]; then count=$((count+1)); file="$item"; fi
  done
  [ "$count" = 1 ]
  before=$(stat -c '%u:%g:%a:%Y:%s' "/source/$file")
  checksum=$(sha256sum "/source/$file" | cut -d ' ' -f 1)
  cp -p -- "/source/$file" "/clone/$file"
  [ "$(stat -c '%u:%g:%a:%Y:%s' "/clone/$file")" = "$before" ]
  [ "$(sha256sum "/clone/$file" | cut -d ' ' -f 1)" = "$checksum" ]
  printf '%s|%s|%s\n' "$file" "$checksum" "$before"
done'''
    args = driver.docker + ["run", "--rm", "--pull", "never", "--network", "none", "--read-only", "--user", "0:0",
        "--cap-drop", "ALL", "--cap-add", "CHOWN", "--cap-add", "FOWNER", "--cap-add", "DAC_OVERRIDE",
        "--security-opt", "no-new-privileges:true", "--mount", "type=volume,src=" + source["name"] + ",dst=/source,readonly",
        "--mount", "type=volume,src=" + target + ",dst=/clone", "--entrypoint", "/bin/sh", value["tools_image"], "-c", command]
    copied = driver.command("clone-public-scanner-signatures", args)
    require(copied.returncode == 0, "scanner signature copy or metadata verification failed")
    files = []
    for line in copied.stdout.decode("ascii").splitlines():
        match = re.fullmatch(r"((main|daily|bytecode)\.(?:cvd|cld))\|([a-f0-9]{64})\|(\d+):(\d+):([0-7]{3,4}):(\d+):(\d+)", line)
        require(match is not None, "scanner signature clone evidence is invalid")
        files.append({"name": match[1], "family": match[2], "sha256": match[3], "uid": int(match[4]),
                      "gid": int(match[5]), "mode": match[6], "mtime_unix": int(match[7]), "size_bytes": int(match[8])})
    require(len(files) == 3 and {row["family"] for row in files} == {"main", "daily", "bytecode"}, "scanner signature clone evidence is incomplete")
    return {"configured": True, "source_volume": source["name"], "source_read_only": True, "copy_volume": target,
            "files": files, "original_clamav_entrypoint_and_healthcheck_preserved": True}


def extract_archive(driver, value, domain, kind):
    volume = value["volumes"][domain + "_metadata" if kind == "metadata" else kind]
    require_owned_volume(volume_metadata(driver, volume), volume, value["owner_id"])
    limits = {"documents": (107374182400, 33554432), "source_state": (4294967296, 1073741824), "metadata": (134217728, 67108864)}
    total, per_file = limits[kind]
    # The plaintext tar is in a bounded container tmpfs, never a host path.
    # The shipped BusyBox tar restores owner and mode by default as root. Its
    # GNU-only positive --same-* counterparts are unsupported; keep numeric
    # ownership and the capabilities required to preserve the verified archive.
    command = "set -eu; cat > /work/archive.tar; /usr/local/bin/invoice-archive-verify --archive /work/archive.tar --max-entries 100000 --max-total-bytes " + str(total) + " --max-file-bytes " + str(per_file) + "; tar --numeric-owner -xf /work/archive.tar -C /restore"
    consumer = driver.docker + ["run", "--rm", "-i", "--pull", "never", "--network", "none", "--read-only", "--user", "0:0", "--cap-drop", "ALL", "--cap-add", "CHOWN", "--cap-add", "FOWNER", "--cap-add", "DAC_OVERRIDE", "--security-opt", "no-new-privileges:true", "--tmpfs", "/work:rw,noexec,nosuid,size=" + str(value["archive_tmpfs_bytes"]), "--mount", "type=volume,src=" + volume + ",dst=/restore", "--entrypoint", "/bin/sh", value["tools_image"], "-c", command]
    driver.pipeline("restore-" + domain + "-" + kind, decrypt_args(driver.config["backups"][domain], kind), consumer)


def restore_database(driver, value, domain):
    row = value["databases"][domain]
    require(set(row) == {"project", "service", "database", "owner"} and all(NAME.fullmatch(row[k]) for k in row), "invalid frozen database target")
    project = next((p for p in driver.projects("candidate") if p["name"] == row["project"]), None)
    require(project is not None and project["services"].get(row["service"], {}).get("role") == domain + "-postgres", "frozen database restore target has wrong role")
    restore_args = ["pg_restore", "-w", "-h", "/var/run/postgresql", "-p", "5432", "-U", row["owner"], "-d", row["database"], "--no-owner", "--no-acl", "--exit-on-error"]
    seed=getattr(driver,'rehearsal_seed',None)
    if seed is not None:
        targets=seed.database_targets()
        if hasattr(seed,'database_restore_targets'):
            require(targets==seed.database_restore_targets,'frozen SQL targets changed across database restoration')
        else:seed.database_restore_targets=targets
        consumer=driver.docker+local_postgres_exec(targets[domain]['expected']['container_id'])+restore_args
    else:
        consumer = driver.compose_argv(project, local_postgres_exec(row['service'],compose=True)+restore_args)
    driver.pipeline("restore-" + domain + "-database", decrypt_args(driver.config["backups"][domain], "database"), consumer)


SNAPSHOT_QUERIES = {
    "platform": {
        "schema-migrations.csv": "SELECT version,dirty FROM public.schema_migrations ORDER BY version",
        "river-migrations.csv": "SELECT line,version,created_at FROM public.river_migration ORDER BY line,version"},
    "invoice": {
        "schema-migrations.csv": "SELECT name,checksum,applied_at FROM schema_migrations ORDER BY name",
        "invoice-documents.csv": "SELECT id,invoice_request_id,object_key,object_version,sha256,size_bytes,mime_type FROM invoice_documents ORDER BY id",
        "source-receiver-state.csv": "SELECT si.id,si.source_type,si.runtime_version,sis.stream_id,sis.sequence,COALESCE(sis.last_batch_hash,'') FROM source_instances si LEFT JOIN source_ingest_state sis ON sis.source_instance_id=si.id ORDER BY si.id,sis.stream_id",
        "invoice-eligibility-policy.csv": "SELECT singleton_id,to_char(eligibility_start_at AT TIME ZONE 'UTC','YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"') AS eligibility_start_utc,display_timezone,require_payment_at_or_after,require_usage_at_or_after,policy_version FROM invoice_eligibility_policy ORDER BY singleton_id"}}


def verify_snapshot_metadata(driver, value):
    result = {}
    for domain in ("platform", "invoice"):
        row = value["databases"][domain]
        project = next(p for p in driver.projects("candidate") if p["name"] == row["project"])
        volume = value["volumes"][domain + "_metadata"]
        result[domain] = {}
        for name, query in SNAPSHOT_QUERIES[domain].items():
            saved = driver.command("read-signed-" + domain + "-" + name.replace(".", "-"), driver.docker + ["run", "--rm", "--pull", "never", "--network", "none", "--read-only", "--user", "0:0", "--cap-drop", "ALL", "--cap-add", "DAC_READ_SEARCH", "--mount", "type=volume,src=" + volume + ",dst=/metadata,readonly", "--entrypoint", "/bin/cat", value["tools_image"], "/metadata/" + name]).stdout
            actual = driver.compose(project, "compare-restored-" + domain + "-" + name.replace(".", "-"), ["exec", "-T", row["service"], "psql", "-X", "-v", "ON_ERROR_STOP=1", "-U", row["owner"], "-d", row["database"], "-c", "COPY (" + query + ") TO STDOUT WITH CSV HEADER"]).stdout
            require(saved and saved == actual, "restored signed snapshot differs: " + domain + "/" + name)
            result[domain][name] = {"sha256": hashlib.sha256(actual).hexdigest(), "bytes": len(actual)}
    return result


def prepare_source_verification_networks(driver):
    projects = [p for p in driver.projects("candidate") if p["kind"] == "unified"]
    require(len(projects) == 1, "one restored main project is required")
    project = projects[0]
    services = [name for name, row in project["services"].items() if row["role"] == "platform-api"]
    require(len(services) == 1, "one stopped unified API must own the restored ingest network")
    # A database-only up creates only database networks. Create the original API
    # and its declared dependencies without starting anything or recreating the
    # already-running databases. Its managed ingest network is then available
    # to the source project. The owner journal covers every created resource.
    response = driver.compose(project, "prepare-restored-source-networks",
        ["create", "--no-build", "--no-recreate", "--pull", "never", services[0]])
    require(response.returncode == 0, "restored source network preparation failed")


def verify_frozen_source_state(driver):
    projects = [p for p in driver.projects("candidate") if p["kind"] == "sources"]
    require(len(projects) == 1, "one restored source project is required")
    project = projects[0]
    entries = project["services"]
    require(len(entries) == len(STREAM_ROLES) and {row["role"] for row in entries.values()} == STREAM_ROLES,
            "all ten restored source streams must be verified")
    result = []
    for service, row in sorted(entries.items()):
        response = driver.compose(project, "verify-restored-state-" + row["role"],
            ["run", "--rm", "--no-deps", "--pull", "never", "--entrypoint", "/source-agent-prod", service, "check-state"])
        require(response.returncode == 0, "native restored source state verification failed")
        result.append({"role": row["role"], "exit_code": response.returncode})
    return result


def preview_candidate(driver):
    """Run a fresh D on isolated copies before E stops any original writer.

    This is an actual execution, never acceptance of an earlier D receipt.
    The existing frozen-mount, no-egress and owned-resource guards still apply.
    No ingress switch or production data volume is used by the preview.
    """
    from smoke import validate_config as validate_smoke, loopback_target
    import ipaddress
    import urllib.parse
    config = copy.deepcopy(driver.config)
    value = config["rehearsal"]
    require(isinstance(value.get("host_preflight"), dict), "preview requires its independently reviewed host/network/port configuration")
    config["host_preflight"] = copy.deepcopy(value["host_preflight"])
    live_names = {p["name"] for side in ("previous", "candidate") for p in config[side]["projects"]}
    require(not live_names.intersection(p["name"] for p in value["projects"]),
            "candidate preview projects must differ from both live topologies")
    smoke_config = read_public_json(value["smoke_config"])
    if 'seed' in value:
        from public_smoke import validate_config as validate_layout
        validate_layout(smoke_config, 'local-synthetic' if config['mode']=='local-synthetic' else 'server-rehearsal')
    else:
        validate_smoke(smoke_config)
    preview_mode = "local-synthetic" if config["mode"] == "local-synthetic" else "server-rehearsal"
    require(smoke_config["mode"] == preview_mode, "preview smoke must declare the isolated rehearsal mode")
    for role, url in smoke_config["origins"].items():
        if role in smoke_config.get("connect_to", {}):
            loopback_target(smoke_config["connect_to"][role])
        else:
            host = urllib.parse.urlsplit(url).hostname
            try:
                local = host == "localhost" or ipaddress.ip_address(host).is_loopback
            except ValueError:
                local = False
            require(local, "preview must pin the configured origin to a loopback TLS listener")
    root = driver.output / "candidate-precheck"
    require(not root.exists(), "candidate preview evidence directory already exists")
    temporary_identity_map(value, driver.state)  # Validate original paths before deriving new ones.
    config["state_root"] = str(root / "state")
    config["mode"] = preview_mode
    if value["temporary_identity_paths"]:
        value["temporary_identity_paths"] = [str(Path(config["state_root"]) / "tmpfs" / value["owner_id"] / (kind + ".age-identity")) for kind in BACKUP_ANCHORS]
    preview = DockerDriver(config, record_root=root)
    result = rehearse(preview)
    require(result.get("status") == "PASS" and result.get("exit_code") == 0 and result.get("cleanup_complete") is True and
            result.get("source_head") == config["candidate"]["head"] and result.get("manifest_sha256") == config["candidate"]["manifest_sha256"],
            "candidate preview did not execute and clean up this exact artifact")
    return result


def rehearse(driver):
    require(driver.config["mode"] != "production", "run D using a rehearsal-mode configuration, never the production project descriptor")
    require(driver.config['mode'] != 'server-rehearsal' or 'seed' in driver.config.get('rehearsal', {}),
            'server D requires isolated synthetic identities; real user credentials are not rehearsal inputs')
    trial, value = rehearsal_driver(driver)
    invalidate_receipt(driver)
    result = {"status": "RUNNING", "mode": driver.config["mode"], "start_utc": utc(), "source_head": driver.config["candidate"]["head"], "manifest_sha256": driver.config["candidate"]["manifest_sha256"], "evidence_kind": "actual-frozen-copy-rehearsal"}
    created = False
    try:
        trial.artifact_preflight()
        result["actual_operator_source"] = trial.operator_source
        from preflight import run as host_preflight
        result["host_preflight"] = host_preflight(trial, trial.config)
        inherited_preflight_pass(result["host_preflight"], trial.config["mode"])
        available = trial.command("rehearsal-memory", trial.docker + ["info", "--format", "{{.MemTotal}}"])
        require(int(available.stdout.strip()) >= value["archive_tmpfs_bytes"] + 4294967296, "Docker memory is insufficient for bounded archive tmpfs plus 4 GiB runtime headroom")
        manifest = read_public_json(driver.config["candidate"]["manifest"])
        require(any(row["name"] == "invoice-tools" and row["imageId"] == value["tools_image"] for row in manifest["images"]), "archive tools are not bound to the candidate manifest")
        result["backups"] = verify_backups(trial, driver.config["backups"], driver.config["approvals"]["max_age_hours"])
        if 'seed' in value:
            from rehearsal_seed import Seed
            trial.rehearsal_seed = Seed(trial, value)
            trial.rehearsal_seed.prepare()
            # The D-only overlay now changes the complete plan: recheck that
            # actual network/environment plan before creating frozen data.
            result['host_preflight_before_seed_overlay'] = result['host_preflight']
            result['host_preflight'] = host_preflight(trial, trial.config)
            inherited_preflight_pass(result['host_preflight'], trial.config['mode'])
        validate_frozen_mounts(trial, value)
        # All names are checked absent before the first create. The ownership
        # journal is durable before any partial resource creation can fail.
        create_frozen_volumes(trial, value)
        created = True
        stage_temporary_identities(driver, value)
        result["scanner_signature_clone"] = clone_scanner_signatures(trial, value)
        for domain, kind in (("invoice", "documents"), ("invoice", "source_state"), ("invoice", "metadata"), ("platform", "metadata")):
            extract_archive(trial, value, domain, kind)
        trial.start_new_databases()
        for domain in ("platform", "invoice"): restore_database(trial, value, domain)
        result["restored_snapshot"] = verify_snapshot_metadata(trial, value)
        jobs = value["verification_jobs"]
        require([r["service"] for r in jobs] == ["verify-invoice-restore"], "the existing invoice document decrypt verification job is mandatory")
        trial.jobs(jobs, "verify-frozen-snapshot")
        prepare_source_verification_networks(trial)
        result["restored_source_state"] = verify_frozen_source_state(trial)
        trial.migrate_and_permissions()
        if getattr(trial, 'rehearsal_seed', None):
            trial.rehearsal_seed.seed()
        trial.start_new(rehearsal=True)
        trial._frozen_ready_endpoint = bind_frozen_readiness_endpoint(trial, value)
        trial.check_new(); trial.preview_smoke(driver.config)
        result.update(status="PASS", runtime_inventory=trial.inventory("candidate", allow_deferred_invoice_health=True))
    except BaseException as error:
        end_readiness_on_failure(trial, error)
        result.update(status="FAIL", exit_code=1)
        if isinstance(error, ReadinessTimeout):
            result["failure_code"] = "CANDIDATE_READINESS_DEADLINE_EXCEEDED"
        raise OperatorError("frozen-copy rehearsal failed") from None
    finally:
        try:
            ownership = driver.state / "rehearsal-ownership.json"
            our_partial = ownership.is_file() and read_public_json(ownership).get("invocation") == str(trial.output)
            if created or our_partial:
                cleaned = cleanup(driver)
                if isinstance(cleaned, dict):
                    result["identity_cleanup"] = {key: cleaned[key] for key in ("temporary_identities_shredded", "original_identities_preserved")}
                    if cleaned.get("exit_code", 0) != 0:
                        result.update(status="FAIL", exit_code=1, audit_complete=False, cleanup_audit_errors=cleaned.get("audit_errors", []))
            result["cleanup_complete"] = True
        except BaseException:
            result.update(status="FAIL", exit_code=1, cleanup_complete=False)
        finally:
            # A network/container cleanup failure must not strand staged keys.
            # This separate journal permits only this invocation's new copies.
            try:
                identities = cleanup_temporary_identities(driver, value)
                identities["temporary_identities_shredded"] += result.get("identity_cleanup", {}).get("temporary_identities_shredded", 0)
                result["identity_cleanup"] = identities
                if identities.get("audit_complete") is False:
                    result.update(status="FAIL", exit_code=1, audit_complete=False, identity_audit_errors=identities.get("audit_errors", []))
            except BaseException:
                result.update(status="FAIL", exit_code=1, cleanup_complete=False)
            seed = getattr(trial, 'rehearsal_seed', None)
            if seed is not None:
                try:
                    result['synthetic_seed_cleanup'] = seed.cleanup()
                except BaseException:
                    result.update(status='FAIL', exit_code=1, cleanup_complete=False)
                result['synthetic_seed'] = seed.record
        budget = getattr(trial, "readiness_budget", None)
        if budget:
            result["candidate_readiness"] = budget.value
            if budget.value["status"] == "DEADLINE_EXCEEDED":
                stopped = driver.output / "STOPPED.md"
                stopped.parent.mkdir(parents=True, exist_ok=True)
                stopped.write_text("# STOPPED\n\nThe complete non-freshness invoice readiness policy was not observed within the single 300 second startup budget.\n\n"
                    + "No production cutover is permitted. Inspect candidate-readiness.json and the preserved public response bytes.\n\n"
                    + "Cleanup complete: " + str(result.get("cleanup_complete", False)) + "\n", encoding="utf-8")
                result["stopped_record"] = str(stopped)
        result["end_utc"] = utc()
        atomic_json(driver.output / "rehearsal-result.json", result)
    final = finish_rehearsal(result, result["cleanup_complete"])
    atomic_json(driver.state / "rehearsal-pass.json", final)
    return final


def cleanup(driver):
    trial, value = rehearsal_driver(driver, recovering=True)
    trial._recovering_audit = True
    try:
        return cleanup_owned_resources(driver, trial, value)
    finally:
        trial._recovering_audit = False


def cleanup_owned_resources(driver, trial, value):
    trial.verify_local_engine()
    journal = read_public_json(driver.state / "rehearsal-ownership.json")
    require(journal.get("owner_id") == value["owner_id"] and journal.get("volumes") == value["volumes"] and journal.get("projects") == [p["name"] for p in trial.projects("candidate")], "cleanup lacks the matching original ownership journal")
    verify_project_resource_owners(trial, value)
    # Inspect every existing resource before issuing a removal. An absent
    # partially-created volume is harmless; a wrong owner is never removed.
    present = []
    for name in value["volumes"].values():
        result = trial.command("cleanup-inspect-" + name, trial.docker + ["volume", "inspect", name], check=False)
        if result.returncode == 0:
            rows = json.loads(result.stdout); require(len(rows) == 1, "invalid cleanup metadata")
            require_owned_volume(rows[0], name, value["owner_id"]); present.append(name)
    for project in reversed(trial.projects("candidate")):
        trial.compose(project, "cleanup-project-" + project["kind"], ["down", "--timeout", "30"])
        ids = trial.command("cleanup-containers-" + project["kind"], trial.docker + ["ps", "-aq", "--filter", "label=com.docker.compose.project=" + project["name"]]).stdout.strip()
        require(not ids, "rehearsal containers remain after cleanup")
    for name in present:
        trial.command("cleanup-volume-" + name, trial.docker + ["volume", "rm", name])
        require(trial.command("verify-removed-" + name, trial.docker + ["volume", "inspect", name], check=False).returncode != 0, "frozen-copy volume remains")
    # The original configuration retains original paths; only the trial age
    # invocations receive staged copies. Never read or hash identity contents.
    identity_cleanup = cleanup_temporary_identities(driver, value)
    for operation in identity_cleanup.get("audit_errors", []):
        recovery_audit_failure(trial, "identity-" + operation)
    result = {"status": "CLEANED", "cleanup_complete": True, "exit_code": 0, "end_utc": utc(), **identity_cleanup}
    recovery_audit_status(trial, result)
    audit_json(trial, driver.output / "cleanup-result.json", result, "cleanup-result")
    recovery_audit_status(trial, result)
    return result

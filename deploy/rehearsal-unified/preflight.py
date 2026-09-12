"""Read-only D/E host preflight. Driver persists output hashes, never raw Env.

Local synthetic qualification deliberately does not certify a Linux production
host, live upstream providers, NTP, or the current authoritative Cloudflare list.
No Docker mutation, shell interpolation, SSH, key-file read, or pull is performed.
"""
import datetime as dt
import errno
import hashlib
import importlib.util
import ipaddress as ip
import json
from pathlib import Path, PurePosixPath
import re
import shutil
import socket

_role_spec = importlib.util.spec_from_file_location('unified_role_policy', Path(__file__).resolve().parents[1] / 'unified' / 'audit' / 'role_policy.py')
role_policy = importlib.util.module_from_spec(_role_spec)
_role_spec.loader.exec_module(role_policy)


SOURCE_ROLES = {"sub2api", "sub2api_db", "newapi", "newapi_db"}
NAME = re.compile(r"[A-Za-z0-9][A-Za-z0-9_.-]{0,127}\Z")
TAG = re.compile(r"[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}\Z")
IMAGE_TAG = re.compile(r"[^\s|@]+:([A-Za-z0-9_][A-Za-z0-9_.-]{0,127})(?:@sha256:[0-9a-fA-F]{64})?\Z")
ENV_KEY = re.compile(r"[A-Za-z_][A-Za-z0-9_]*\Z")
SERVER_ONLY = ["host-root-and-actual-docker-root-disk", "host-ntp", "host-listening-ports",
               "authoritative-cloudflare-current-cidrs", "production-realip-config", "live-upstream-versions"]
SOURCE_FORMAT = '{"name":{{json .Name}},"running":{{json .State.Running}},"image":{{json .Config.Image}}}'
NETWORK_FORMAT = '{"name":{{json .Name}},"internal":{{json .Internal}},"ipam":{{json .IPAM.Config}}}'
STORAGE_FORMAT = '{"running":{{json .State.Running}},"project":{{json (index .Config.Labels "com.docker.compose.project")}},"mounts":[{{range $i,$m := .Mounts}}{{if $i}},{{end}}{"type":{{json $m.Type}},"name":{{json $m.Name}},"destination":{{json $m.Destination}}}{{end}}]}'


class PreflightError(RuntimeError):
    pass


def require(value, message):
    if not value:
        raise PreflightError(message)


def utc():
    return dt.datetime.now(dt.timezone.utc).isoformat()


def keys(value, expected, message):
    require(isinstance(value, dict) and set(value) == set(expected), message)


def network(value, message):
    require(isinstance(value, str), message)
    try:
        return ip.ip_network(value, strict=True)
    except ValueError:
        raise PreflightError(message) from None


def address(value, message):
    try:
        return ip.ip_address(value)
    except (ValueError, TypeError):
        raise PreflightError(message) from None


def public_path(value, *, posix=False):
    require(isinstance(value, str) and value and not any(x in value for x in ("\0", "\n", "\r")), "invalid public path")
    path = PurePosixPath(value) if posix else Path(value)
    require(path.is_absolute() and ".." not in path.parts, "public path must be absolute without traversal")
    if not posix:
        for part in (path, *path.parents):
            require(not part.is_symlink() and not (hasattr(part, "is_junction") and part.is_junction()), "public path links are forbidden")
    return path


def load_json(value, name):
    def unique(pairs):
        out = {}
        for key, item in pairs:
            require(key not in out, name + " has duplicate JSON keys")
            out[key] = item
        return out
    require(len(value) <= 8 * 1024 * 1024, name + " output too large")
    try:
        return json.loads(value, object_pairs_hook=unique)
    except (UnicodeError, ValueError):
        raise PreflightError(name + " has invalid JSON") from None


def text(value, name):
    try:
        return value.decode("utf-8", errors="strict")
    except (AttributeError, UnicodeError):
        raise PreflightError(name + " has invalid text") from None


def command(driver, name, argv):
    result = driver.command(name, argv, check=False)
    require(result.returncode == 0, name + " command failed")
    require(isinstance(result.stdout, bytes) and len(result.stdout) <= 8 * 1024 * 1024, name + " has invalid output")
    return result.stdout


def validate(config):
    require(isinstance(config, dict) and config.get("mode") in ("local-synthetic", "server-rehearsal", "production"), "unsupported preflight mode")
    mode = config["mode"]
    h = config.get("host_preflight")
    expected = {"sources", "expected_sub2_version", "expected_newapi_tag", "required_loopback_ports", "planned_networks", "proxy", "ingest", "cloudflare_config_file", "cloudflare_review", "required_env_keys"}
    if mode == "local-synthetic":
        expected.add("local")
    else:
        require(isinstance(h, dict) and "local" not in h, "local evidence is forbidden for server qualification")
    keys(h, expected, "host_preflight keys do not match the mode contract")
    keys(h["sources"], SOURCE_ROLES, "exactly four source roles required")
    require(all(isinstance(x, str) and NAME.fullmatch(x) for x in h["sources"].values()) and len(set(h["sources"].values())) == 4, "four distinct source container names required")
    require(isinstance(h["expected_sub2_version"], str) and re.fullmatch(r"\d+\.\d+\.\d+", h["expected_sub2_version"]), "SUB version is invalid")
    require(isinstance(h["expected_newapi_tag"], str) and TAG.fullmatch(h["expected_newapi_tag"]), "NEW tag is invalid")
    ports = h["required_loopback_ports"]
    require(isinstance(ports, list) and ports and all(type(x) is int and 1 <= x <= 65535 for x in ports) and len(set(ports)) == len(ports), "planned ports are invalid")
    plans = h["planned_networks"]
    require(isinstance(plans, list) and len(plans) >= 2, "planned network inventory is empty")
    parsed = {}
    for row in plans:
        keys(row, {"name", "cidr", "internal"}, "planned network keys invalid")
        require(isinstance(row["name"], str) and NAME.fullmatch(row["name"]) and row["name"] not in parsed and type(row["internal"]) is bool, "planned network identity invalid")
        net = network(row["cidr"], "planned network CIDR invalid")
        require(net.version == 4 and net.num_addresses >= 4, "planned network must be usable IPv4")
        require(not any(net.overlaps(old[0]) for old in parsed.values()), "planned network CIDRs overlap")
        parsed[row["name"]] = (net, row["internal"])
    proxy = h["proxy"]
    keys(proxy, {"network_name", "gateway_ip", "trusted_cidr"}, "proxy keys invalid")
    require(proxy["network_name"] in parsed, "proxy network absent")
    proxynet = parsed[proxy["network_name"]][0]
    gateway = address(proxy["gateway_ip"], "proxy address invalid")
    require(gateway.version == 4 and proxynet.network_address < gateway < proxynet.broadcast_address, "proxy address must be usable within proxy network")
    require(proxy["trusted_cidr"] == str(gateway) + "/32", "proxy trusted CIDR must be exact /32")
    ingest = h["ingest"]
    keys(ingest, {"network_name", "dynamic_range", "proxy_ip", "proxy_cidr"}, "ingest keys invalid")
    require(ingest["network_name"] in parsed and parsed[ingest["network_name"]][1] is True, "ingest network must be internal")
    net = parsed[ingest["network_name"]][0]
    dynamic = network(ingest["dynamic_range"], "ingest dynamic network invalid")
    require(dynamic.version == 4 and dynamic.subnet_of(net), "ingest dynamic range outside network")
    capacity = dynamic.num_addresses - 2 - int(net.network_address + 1 in dynamic)
    require(capacity >= 11, "ingest dynamic capacity is below eleven")
    proxyip = address(ingest["proxy_ip"], "ingest proxy address invalid")
    require(proxyip.version == 4 and net.network_address < proxyip < net.broadcast_address and proxyip not in dynamic, "ingest proxy must be usable and outside dynamic range")
    require(ingest["proxy_cidr"] == str(proxyip) + "/32", "ingest proxy CIDR must be exact /32")
    public_path(h["cloudflare_config_file"], posix=mode != "local-synthetic")
    projects = config.get("candidate", {}).get("projects")
    require(isinstance(projects, list) and projects, "candidate projects required for environment checks")
    projectnames = [x.get("name") for x in projects]
    require(all(isinstance(x, str) and NAME.fullmatch(x) for x in projectnames) and len(set(projectnames)) == len(projectnames), "candidate project names invalid")
    keys(h["required_env_keys"], projectnames, "environment requirement project coverage incomplete")
    for services in h["required_env_keys"].values():
        require(isinstance(services, dict) and services, "environment service requirements absent")
        for svc, envkeys in services.items():
            require(isinstance(svc, str) and NAME.fullmatch(svc) and isinstance(envkeys, list) and all(isinstance(k, str) and ENV_KEY.fullmatch(k) for k in envkeys) and len(envkeys) == len(set(envkeys)), "environment requirement invalid")
    if mode == "local-synthetic":
        local = h["local"]
        keys(local, {"task_directory", "storage_probe"}, "local preflight keys invalid")
        taskdir = public_path(local["task_directory"])
        require(taskdir.is_dir(), "local task disk directory missing")
        store = local["storage_probe"]
        keys(store, {"container", "project", "volume", "mount"}, "local storage probe keys invalid")
        require(all(isinstance(store[k], str) and NAME.fullmatch(store[k]) for k in ("container", "project", "volume")), "local storage identity invalid")
        public_path(store["mount"], posix=True)
    return h, parsed, capacity


def reviewed_cloudflare(h, mode):
    review = h['cloudflare_review']
    keys(review, {'path','sha256','reviewed_by','reviewed_at','expires_at','source_url','scope'}, 'Cloudflare review contract is incomplete')
    require(review['scope'] == ('synthetic' if mode == 'local-synthetic' else 'reviewed-offline'), 'Cloudflare review scope differs from operator mode')
    require(review['source_url'] == 'https://api.cloudflare.com/client/v4/ips', 'Cloudflare review source is not the official endpoint')
    require(isinstance(review['reviewed_by'], str) and 0 < len(review['reviewed_by'].strip()) <= 128, 'Cloudflare reviewer is missing')
    require(isinstance(review['sha256'], str) and re.fullmatch(r'[a-f0-9]{64}', review['sha256']), 'Cloudflare document SHA is invalid')
    try:
        reviewed, expires = [dt.datetime.fromisoformat(review[key]) for key in ('reviewed_at','expires_at')]
        require(reviewed.tzinfo is not None and expires.tzinfo is not None and
                reviewed.utcoffset() is not None and expires.utcoffset() is not None, 'Cloudflare review times require explicit timezones')
        now = dt.datetime.now(dt.timezone.utc)
        require(reviewed <= now <= expires and reviewed < expires, 'Cloudflare review is future, expired or has an invalid interval')
    except (ValueError, TypeError):
        raise PreflightError('Cloudflare review time is invalid') from None
    path = public_path(review['path'])
    require(path.suffix.lower() == '.json' and not any(part.casefold() in ('private','secrets','credentials') for part in path.parts),
            'Cloudflare artifact must be an explicit public JSON file')
    require(path.is_file() and path.stat().st_size <= 8*1024*1024 and path.stat().st_nlink == 1, 'Cloudflare public artifact is absent or invalid')
    raw = path.read_bytes()
    require(hashlib.sha256(raw).hexdigest() == review['sha256'], 'Cloudflare reviewed document SHA differs')
    return load_json(raw, 'Cloudflare reviewed artifact'), {
        'scope': review['scope'], 'document_path': str(path), 'document_sha256': review['sha256'],
        'reviewed_by': review['reviewed_by'], 'reviewed_at': review['reviewed_at'], 'expires_at': review['expires_at'],
        'source_url': review['source_url'], 'review_age_seconds': (now-reviewed).total_seconds(),
        'current_live_verified': False,
        'server_freshness_action': 'Owner must verify authoritative currency before the server window; this offline run only checks the reviewed artifact and its explicit validity interval.'}


def sources(driver, h, local):
    states = {}
    for role in sorted(SOURCE_ROLES):
        name = h["sources"][role]
        row = load_json(command(driver, "source-" + role, driver.docker + ["inspect", "--type", "container", name, "--format", SOURCE_FORMAT]), "source")
        require(isinstance(row, dict) and row.get("name") == "/" + name and row.get("running") is True and isinstance(row.get("image"), str), "source container missing, stopped or identity changed")
        states[role] = {"name": name, "image": row["image"]}
    raw = command(driver, "source-sub2api-version", driver.docker + ["exec", h["sources"]["sub2api"], "/app/sub2api", "-version"])
    matches = re.findall(r"(?m)^Sub2API\s+([0-9.]+)(?=\s|$)", text(raw, "SUB version"))
    require(matches == [h["expected_sub2_version"]], "SUB binary version mismatch or ambiguous output")
    match = IMAGE_TAG.fullmatch(states["newapi"]["image"])
    require(match is not None and match.group(1) == h["expected_newapi_tag"], "NEW image tag mismatch")
    return {"scope": "synthetic-contract" if local else "live-upstream-version", "containers": states, "sub2api_version": matches[0], "newapi_tag": match.group(1)}


def parse_df(raw, mounts):
    rows = text(raw, "disk").strip().splitlines()
    require(len(rows) == len(mounts) + 1 and re.fullmatch(r"Filesystem\s+(?:1024-blocks|1K-blocks)\s+Used\s+Available\s+(?:Capacity|Use%)\s+Mounted on", rows[0]), "disk output missing header or exact rows")
    output = []
    for row, mount in zip(rows[1:], mounts):
        fields = row.split()
        require(len(fields) == 6, "disk row invalid")
        device, total, used, available, percent, mounted = fields
        require(total.isdigit() and int(total) > 0 and used.isdigit() and re.fullmatch(r"-?\d+", available) and re.fullmatch(r"(?:100|[0-9]{1,2})%", percent), "disk numeric fields invalid")
        # df mountpoint can be an ancestor of the requested DockerRootDir.
        require(mounted.startswith("/") and (mount == mounted or mount.startswith(mounted.rstrip("/") + "/")), "disk mount does not contain requested path")
        require(int(percent[:-1]) < 80, "disk capacity is at or above eighty percent")
        output.append({"requested_path": mount, "mounted_on": mounted, "used_percent": int(percent[:-1]), "available_bytes": int(available) * 1024})
    return output


def cf_check(document, conf):
    require(isinstance(document, dict) and document.get("success") is True and isinstance(document.get("result"), dict), "Cloudflare official document failed")
    expected = set()
    for family, key in [(4, "ipv4_cidrs"), (6, "ipv6_cidrs")]:
        values = document["result"].get(key)
        require(isinstance(values, list) and values, "Cloudflare address family is empty")
        for value in values:
            net = network(value, "Cloudflare official CIDR invalid")
            require(net.version == family and str(net) not in expected, "Cloudflare CIDR family or duplication invalid")
            expected.add(str(net))
    # Comments cannot supply effective directives. Require one exact header and
    # recursive directive, so a second conflicting directive cannot hide.
    conf = re.sub(r"#[^\n]*", "", conf)
    actual = []
    for value in re.findall(r"\bset_real_ip_from\s+([^;]+);", conf):
        actual.append(str(network(value.strip(), "Cloudflare real-IP CIDR invalid")))
    require(set(actual) == expected and len(actual) == len(expected), "Cloudflare CIDR set is incomplete or enlarged")
    require(re.findall(r"\breal_ip_header\s+([^;]+);", conf) == ["CF-Connecting-IP"], "Cloudflare real-IP header must be unique and exact")
    require(re.findall(r"\breal_ip_recursive\s+([^;]+);", conf) == ["on"], "Cloudflare real-IP recursion must be uniquely on")
    return {"cidr_count": len(expected), "ipv4_count": len(document["result"]["ipv4_cidrs"]), "ipv6_count": len(document["result"]["ipv6_cidrs"])}


def host_ports(raw, planned, allowed):
    observed = []
    for row in text(raw, "port inventory").splitlines():
        fields = row.split()
        require(len(fields) >= 5 and fields[0] == "LISTEN" and fields[1].isdigit() and fields[2].isdigit(), "port inventory malformed")
        endpoint = fields[3]
        require(":" in endpoint and endpoint.rsplit(":", 1)[1].isdigit(), "port endpoint invalid")
        port = int(endpoint.rsplit(":", 1)[1])
        require(1 <= port <= 65535 and (port not in planned or port in allowed), "planned port is occupied or invalid")
        observed.append(port)
    return {"scope": "host-listeners", "planned_ports": planned, "observed_count": len(observed), "reviewed_occupied_ports": sorted(allowed)}


def local_ports(planned, allowed):
    held = []
    try:
        for port in planned:
            if port in allowed:
                continue
            for family, host in [(socket.AF_INET, "127.0.0.1"), (socket.AF_INET6, "::1")]:
                sock = None
                try:
                    sock = socket.socket(family, socket.SOCK_STREAM)
                    if hasattr(socket, "SO_EXCLUSIVEADDRUSE"):
                        sock.setsockopt(socket.SOL_SOCKET, socket.SO_EXCLUSIVEADDRUSE, 1)
                    if family == socket.AF_INET6:
                        sock.setsockopt(socket.IPPROTO_IPV6, socket.IPV6_V6ONLY, 1)
                    sock.bind((host, port))
                    held.append(sock); sock = None
                except OSError as error:
                    # IPv6-disabled hosts still have an actual IPv4 bind proof.
                    if family != socket.AF_INET6 or error.errno not in (errno.EAFNOSUPPORT, errno.EADDRNOTAVAIL):
                        raise PreflightError("planned loopback port is occupied or unavailable") from None
                finally:
                    if sock is not None:
                        sock.close()
    finally:
        for sock in held:
            sock.close()
    return {"scope": "local-loopback-bind", "planned_ports": planned, "reviewed_occupied_ports": sorted(allowed), "race_note": "point-in-time probe; bind must still succeed at startup"}


def check_ports(driver, config, allowed_occupied_ports=None):
    """Engine-only exception after exact old image/project/loopback binding proof.

    This argument is never read from configuration. The engine must call again
    with the default empty set after stopping old services, before starting new.
    """
    planned = config["host_preflight"]["required_loopback_ports"]
    require(isinstance(planned, list) and planned and all(type(p) is int and 1 <= p <= 65535 for p in planned) and len(set(planned)) == len(planned), "planned ports invalid")
    allowed = set() if allowed_occupied_ports is None else allowed_occupied_ports
    require(type(allowed) is set and all(type(p) is int for p in allowed) and allowed.issubset(planned), "reviewed occupied ports must be an engine-owned subset set")
    require(config.get("mode") in ("local-synthetic", "server-rehearsal", "production"), "unsupported port probe mode")
    if config["mode"] == "local-synthetic":
        return local_ports(planned, allowed)
    return host_ports(command(driver, "host-listeners", ["ss", "-H", "-ltn"]), planned, allowed)


def networks(driver, plans):
    ids = text(command(driver, "network-list", driver.docker + ["network", "ls", "--format", "{{.ID}}"]), "network list").split()
    require(ids and len(ids) == len(set(ids)) and all(re.fullmatch(r"[0-9a-f]{6,64}", x) for x in ids), "network inventory IDs invalid or empty")
    raw = command(driver, "network-inspect", driver.docker + ["network", "inspect", *ids, "--format", NETWORK_FORMAT])
    lines = text(raw, "network inventory").splitlines()
    require(len(lines) == len(ids), "network inventory is empty or missing inspected IDs")
    seen, reused = set(), []
    for line in lines:
        row = load_json(line, "network")
        keys(row, {"name", "internal", "ipam"}, "network fields invalid")
        name = row["name"]
        require(isinstance(name, str) and NAME.fullmatch(name) and name not in seen and type(row["internal"]) is bool, "network identity malformed")
        if name in ("host", "none") and row["ipam"] is None:
            row["ipam"] = []
        require(isinstance(row["ipam"], list) and (row["ipam"] or name in ("host", "none")), "network IPAM is missing or invalid")
        seen.add(name)
        actual = []
        for entry in row["ipam"]:
            require(isinstance(entry, dict) and isinstance(entry.get("Subnet"), str) and entry["Subnet"], "network subnet empty or invalid")
            actual.append(network(entry["Subnet"], "network subnet invalid"))
        if name in plans:
            expected, internal = plans[name]
            require(actual == [expected] and row["internal"] is internal, "same-name network does not exactly match CIDR and Internal")
            reused.append(name)
        for net in actual:
            if net.version == 4:
                for planned_name, (expected, internal) in plans.items():
                    if net.overlaps(expected):
                        require(name == planned_name and net == expected and row["internal"] is internal, "network overlaps a planned subnet")
    return {"existing_network_count": len(seen), "exact_reuse": sorted(reused)}


def environment(driver, config, h):
    checked = {}
    for project in config["candidate"]["projects"]:
        name = project["name"]
        # Profiled maintenance/bootstrap jobs remain part of the reviewed
        # environment contract. Config renders them; it never starts services.
        result = driver.compose(project, "preflight-environment-" + name, ["--profile", "*", "config", "--format", "json"])
        require(result.returncode == 0, "Compose environment rendering failed")
        doc = load_json(result.stdout, "Compose environment")
        services = doc.get("services") if isinstance(doc, dict) else None
        required = h["required_env_keys"][name]
        require(isinstance(services, dict) and set(services) == set(required), "environment requirement coverage must include every Compose service")
        checked[name] = {}
        for service, entry in services.items():
            require(isinstance(entry, dict), "Compose environment service invalid")
            env = entry.get("environment", {})
            require(isinstance(env, dict) and set(required[service]).issubset(env), "required environment keys missing")
            for key in required[service]:
                value = env[key]
                require(value is not None and str(value).strip(), "required environment value is null or blank")
            for key, value in env.items():
                require(isinstance(key, str) and ENV_KEY.fullmatch(key) and not isinstance(value, (dict, list, bool)) and not re.search(r"\$\{[^}]*\}", str(value)), "resolved environment contains invalid or unexpanded values")
            checked[name][service] = {"checked_key_count": len(env), "required_key_count": len(required[service])}
        if project.get("kind") == "unified":
            require("platform-api" in services, "unified platform-api role policy is missing")
            try:
                record = load_json(public_path(config["approvals"]["mfa_query_record"]).read_bytes(), "MFA census")
                snapshot_bytes = public_path(record["platform_snapshot"]).read_bytes()
                require(hashlib.sha256(snapshot_bytes).hexdigest() == record["platform_snapshot_sha256"], "MFA census snapshot hash mismatch")
                actual_census = role_policy.validate_census(load_json(snapshot_bytes, "MFA snapshot"))
                require(actual_census == record.get("role_policy"), "MFA joint role census does not match actual captured rows")
                checked[name]["platform-api"]["role_policy"] = role_policy.validate_resolved(
                    services["platform-api"].get("environment", {}), record.get("role_policy"))
            except role_policy.DefaultCoverageError as exc:
                raise PreflightError(str(exc)) from None
            except (ValueError, KeyError, TypeError, OSError):
                raise PreflightError("ADMIN_ROLE, explicit role scopes and joint C1 census must match with TOTP coverage and a login-ready administrator") from None
    return checked


def run(driver, config, allowed_occupied_ports=None):
    result = {"schema": "xingmang.unified.host-preflight/v1", "status": "RUNNING", "exit_code": 1, "utc_start": utc(), "checks": [], "evidence": {}}
    validated = False
    try:
        h, plans, capacity = validate(config)
        validated = True
        local = config["mode"] == "local-synthetic"
        result.update(mode=config["mode"], qualification_scope="local-synthetic-host-preflight" if local else "server-host-preflight", inherited_server_checks_requires_server=SERVER_ONLY.copy() if local else [])
        cf_document, cf_proof = reviewed_cloudflare(h, config['mode'])
        result["evidence"]["sources"] = sources(driver, h, local)
        result["checks"].append({"name": "source-versions", "status": "PASS"})
        if local:
            taskdir = public_path(h["local"]["task_directory"])
            total, used, free = shutil.disk_usage(taskdir)
            require(total > 0 and used >= 0 and free >= 0 and used * 100 < total * 80, "local task disk is at or above eighty percent")
            result["evidence"]["task_disk"] = {"scope": "local-task-directory", "total_bytes": total, "free_bytes": free, "used_percent": round(used * 100 / total, 2)}
            result["evidence"]["ports"] = check_ports(driver, config, allowed_occupied_ports)
            store = h["local"]["storage_probe"]
            row = load_json(command(driver, "local-storage-identity", driver.docker + ["inspect", "--type", "container", store["container"], "--format", STORAGE_FORMAT]), "local storage identity")
            matches = [m for m in row.get("mounts", []) if m.get("destination") == store["mount"]]
            require(row.get("running") is True and row.get("project") == store["project"] and matches == [{"type": "volume", "name": store["volume"], "destination": store["mount"]}], "local storage probe is not the task-owned mounted volume")
            raw = command(driver, "local-container-storage", driver.docker + ["exec", store["container"], "df", "-P", "--", store["mount"]])
            result["evidence"]["storage"] = {"scope": "container-storage", "rows": parse_df(raw, [store["mount"]])}
            confraw = public_path(h["cloudflare_config_file"]).read_bytes()
        else:
            require(text(command(driver, "host-os", ["uname", "-s"]), "host OS").strip() == "Linux", "server preflight requires actual Linux host")
            root = load_json(command(driver, "docker-root", driver.docker + ["info", "--format", "{{json .DockerRootDir}}"]), "Docker root")
            public_path(root, posix=True)
            require(not any(x.isspace() for x in root), "Docker root path cannot be parsed safely by df")
            raw = command(driver, "host-disks", ["env", "LC_ALL=C", "df", "-P", "--", "/", root])
            result["evidence"]["storage"] = {"scope": "linux-host-root-and-docker-root", "rows": parse_df(raw, ["/", root])}
            require(text(command(driver, "host-ntp", ["timedatectl", "show", "-p", "NTPSynchronized", "--value"]), "NTP").strip() == "yes", "host NTP must be synchronized")
            result["evidence"]["ntp"] = {"scope": "host-timedatectl", "synchronized": True}
            result["evidence"]["ports"] = check_ports(driver, config, allowed_occupied_ports)
            confraw = command(driver, "cloudflare-realip", ["cat", "--", h["cloudflare_config_file"]])
        result["evidence"]["cloudflare"] = {**cf_proof, **cf_check(cf_document, text(confraw, "Cloudflare config"))}
        result["checks"].append({"name": "host-mode-guards", "status": "PASS"})
        result["evidence"]["networks"] = {**networks(driver, plans), "ingest_dynamic_capacity": capacity}
        result["checks"].append({"name": "network-addressing", "status": "PASS"})
        result["evidence"]["environment"] = environment(driver, config, h)
        result["checks"].append({"name": "compose-environment", "status": "PASS"})
        result.update(status="PASS", exit_code=0)
    except PreflightError as error:
        result.update(status="FAIL", exit_code=1 if validated else 2, reason=str(error))
    except Exception:
        # Never copy exception text: OS and driver failures may contain Env or
        # private command diagnostics. Command event hashes retain correlation.
        result.update(status="FAIL", exit_code=1 if validated else 2, reason="preflight collection failed; inspect sanitized command events")
    finally:
        result["utc_end"] = utc()
    return result

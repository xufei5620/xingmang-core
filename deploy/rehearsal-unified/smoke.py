"""Read-only cutover CLI and shared authenticated probe sessions.

The CLI only creates and revokes authentication sessions. Financial probes
require the internal verified frozen-copy capability from preview_smoke;
configuration flags cannot enable them. No identity fabrication or TLS bypass.
Credentials are consumed only from named restricted files at runtime.
Unit simulations do not qualify a deployment; only this CLI's real HTTP run does.
"""
import argparse
import base64
import copy
import datetime as dt
import hashlib
import hmac
import http.cookiejar
import http.client
import ipaddress
import json
import os
from pathlib import Path
import re
import ssl
import socket
import stat
import struct
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid


SCHEMA = "xingmang.unified.smoke/v1"
REQUIRED = ("readiness.before", "sub.login", "new.login", "source.isolation",
            "staff.totp", "staff.page", "legacy.rejected", "requests.read",
            "readiness.after", "sessions.revoked")
OLD_ROUTES = (
    ("GET", "/invoice-api/v1/auth/login"),
    ("GET", "/invoice-api/v1/auth/callback"),
    ("GET", "/invoice-api/v1/auth/admin/step-up"),
    ("POST", "/invoice-api/v1/auth/backchannel-logout"),
    ("POST", "/invoice-api/v1/auth/console-assertion"),
    ("POST", "/api/v1/auth/console-assertion"),
)
AUTH_WRITES = frozenset(("POST", path) for path in (
    "/invoice-api/v1/auth/platform-login", "/invoice-api/v1/auth/logout",
    "/api/v1/auth/login", "/api/v1/auth/login/totp", "/api/v1/auth/logout"))


class SmokeFailure(Exception):
    def __init__(self, code="CONTRACT_REJECTED"):
        self.code = code
        super().__init__(code)


class InputFailure(SmokeFailure):
    pass


def require(condition, code):
    if not condition:
        raise SmokeFailure(code)


def is_uuid(value):
    try:
        return isinstance(value, str) and str(uuid.UUID(value)) == value and uuid.UUID(value).int != 0
    except (ValueError, AttributeError):
        return False


def positive(value):
    return type(value) is int and 0 < value <= 2**53 - 1


def check_ready(value):
    require(isinstance(value, dict) and value.get("invoice_ready") is True, "INVOICE_NOT_READY")
    modules = value.get("modules")
    require(isinstance(modules, dict), "MODULES_MISSING")
    for key in ("platform", "invoice_sources", "invoice_projection"):
        require(isinstance(modules.get(key), dict) and modules[key].get("ready") is True, "MODULE_NOT_READY")


def check_scope(items, source_id, kind):
    require(isinstance(items, list) and bool(items), "SOURCE_FIXTURE_EMPTY")
    ids = []
    for item in items:
        require(isinstance(item, dict) and is_uuid(item.get("id")), "LOT_ID_INVALID")
        require(item.get("source_instance_id") == source_id and item.get("source") == kind, "SOURCE_ISOLATION_FAILED")
        ids.append(item["id"])
    require(len(ids) == len(set(ids)), "DUPLICATE_LOT")


def check_profile(value, principal_id, email):
    require(isinstance(value, dict) and is_uuid(value.get("id")), "PROFILE_MISSING")
    require(value.get("principal_id") == principal_id, "PROFILE_OWNER_MISMATCH")
    require(value.get("email_verified") is True, "EXISTING_EMAIL_PROOF_REQUIRED")
    require(value.get("email") == email, "PROFILE_RECEIVER_MISMATCH")


def check_staff(value):
    require(isinstance(value, dict) and value.get("authenticated") is True, "STAFF_NOT_AUTHENTICATED")
    require(value.get("admin_step_up_required") is False, "STAFF_MFA_NOT_FRESH")
    user = value.get("user")
    require(isinstance(user, dict) and user.get("role") == "admin" and is_uuid(user.get("id")), "STAFF_ROLE_INVALID")
    token = value.get("csrf_token")
    require(isinstance(token, str) and 43 <= len(token) <= 128 and re.fullmatch(r"[A-Za-z0-9_-]+", token) is not None, "STAFF_CSRF_INVALID")


def utc():
    return dt.datetime.now(dt.timezone.utc).isoformat().replace("+00:00", "Z")


def json_object(body):
    def pairs(items):
        result = {}
        for key, value in items:
            if key in result:
                raise SmokeFailure("DUPLICATE_JSON_KEY")
            result[key] = value
        return result
    try:
        value = json.loads(body, object_pairs_hook=pairs)
    except (ValueError, UnicodeError):
        raise SmokeFailure("INVALID_JSON") from None
    require(isinstance(value, dict), "JSON_OBJECT_REQUIRED")
    return value


def checked_file(raw, private=False):
    if not isinstance(raw, str) or not Path(raw).is_absolute():
        raise InputFailure("ABSOLUTE_FILE_REQUIRED")
    path = Path(raw)
    try:
        mode = path.lstat().st_mode
        if path.is_symlink() or not stat.S_ISREG(mode):
            raise InputFailure("REGULAR_FILE_REQUIRED")
        # Windows ACLs are established by the rehearsal engine. POSIX runners
        # additionally reject credential files readable by group/other.
        if private and os.name != "nt" and stat.S_IMODE(mode) & 0o077:
            raise InputFailure("PRIVATE_FILE_PERMISSIONS")
        if path.stat().st_size <= 0 or path.stat().st_size > (4096 if private else 1 << 20):
            raise InputFailure("FILE_SIZE_INVALID")
    except OSError:
        raise InputFailure("REQUIRED_FILE_UNAVAILABLE") from None
    return path


def credential(raw):
    path = checked_file(raw, private=True)
    try:
        value = path.read_text("utf-8").removesuffix("\n").removesuffix("\r")
    except (OSError, UnicodeError):
        raise InputFailure("CREDENTIAL_UNREADABLE") from None
    if not value or any(ord(ch) < 32 for ch in value):
        raise InputFailure("CREDENTIAL_FORMAT_INVALID")
    return value


def origin(raw):
    try:
        parsed = urllib.parse.urlsplit(raw)
        if not isinstance(raw, str) or parsed.scheme != "https" or not parsed.netloc or parsed.path or parsed.query or parsed.fragment or parsed.username or parsed.password:
            raise ValueError()
        host = parsed.hostname
        if not host or (parsed.port is not None and not 1 <= parsed.port <= 65535):
            raise ValueError()
        try:
            ipaddress.ip_address(host)
        except ValueError:
            if host != "localhost" and ("." not in host or any(re.fullmatch(r"[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?", label) is None for label in host.split("."))):
                raise ValueError()
        if any(c in raw for c in "\\\r\n\t "):
            raise ValueError()
        return raw
    except (TypeError, ValueError, AttributeError):
        raise InputFailure("EXPLICIT_HTTPS_ORIGIN_REQUIRED") from None


def loopback_target(value):
    try:
        if set(value) != {"address", "port"} or not ipaddress.ip_address(value["address"]).is_loopback or type(value["port"]) is not int or not 1 <= value["port"] <= 65535:
            raise ValueError()
        return value["address"], value["port"]
    except (KeyError, TypeError, ValueError):
        raise InputFailure("LOOPBACK_TRANSPORT_REQUIRED") from None


def validate_config(config, *, _credentials=None):
    def private_file(path):
        if _credentials is None:
            return checked_file(path, private=True)
        from rehearsal_seed import Seed
        require(type(_credentials) is Seed and config.get('mode') in ('server-rehearsal','local-synthetic'), 'FROZEN_CREDENTIAL_READER_REQUIRED')
        return _credentials.check_credential(path)
    try:
        if config["schema"] != SCHEMA or config["mode"] not in ("local-synthetic", "server-rehearsal", "production"):
            raise InputFailure("CONFIG_SCHEMA_MODE_INVALID")
        for role in ("admin", "user"):
            origin(config["origins"][role])
        if config.get("ca_file") is not None:
            checked_file(config["ca_file"])
        if "connect_to" in config:
            if set(config["connect_to"]) != {"admin", "user"}:
                raise InputFailure("BOTH_TRANSPORT_TARGETS_REQUIRED")
            for value in config["connect_to"].values():
                loopback_target(value)
        creds = config["credentials"]
        for role in ("staff", "sub2api", "newapi"):
            item = creds[role]
            if not isinstance(item.get("username" if role == "staff" else "identifier"), str) or not item.get("username" if role == "staff" else "identifier"):
                raise InputFailure("ACCOUNT_IDENTIFIER_REQUIRED")
            private_file(item["password_file"])
        private_file(creds["staff"]["totp_file"])
        private_file(creds["sub2api"]["expected_email_file"])
        if not is_uuid(creds["sub2api"]["existing_profile_id"]):
            raise InputFailure("EXISTING_PROFILE_ID_REQUIRED")
        ids = [creds[k]["source_id"] for k in ("sub2api", "newapi")]
        if any(not is_uuid(value) for value in ids) or ids[0] == ids[1]:
            raise InputFailure("DISTINCT_SOURCE_IDS_REQUIRED")
        expected = config["expected"]
        if not isinstance(expected.get("required_staff_role", "admin"), str) or not expected.get("required_staff_role", "admin"):
            raise InputFailure("STAFF_ROLE_CONFIG_INVALID")
    except (KeyError, TypeError):
        raise InputFailure("CONFIG_FIELD_MISSING") from None


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        raise SmokeFailure("HTTP_REDIRECT_REJECTED")


class LoopbackHTTPSConnection(http.client.HTTPSConnection):
    """Keep configured Host, Origin, TLS SNI and verification; pin only the socket."""
    def __init__(self, host, *, target, **kwargs):
        self.target = target
        super().__init__(host, **kwargs)

    def connect(self):
        require(self._tunnel_host is None, "HTTP_TUNNEL_FORBIDDEN")
        self.sock = socket.create_connection(self.target, self.timeout, self.source_address)
        self.sock = self._context.wrap_socket(self.sock, server_hostname=self.host)


class LoopbackHTTPSHandler(urllib.request.HTTPSHandler):
    def __init__(self, context, target):
        self.target = target
        super().__init__(context=context)

    def https_open(self, request):
        def connection(host, **kwargs):
            return LoopbackHTTPSConnection(host, target=self.target, **kwargs)
        return self.do_open(connection, request, context=self._context)


class Client:
    def __init__(self, base, ca_file, records, connect_to=None):
        self.base = origin(base)
        self.records = records
        self.csrf = ""
        context = ssl.create_default_context(cafile=str(checked_file(ca_file)) if ca_file is not None else None)
        context.minimum_version = ssl.TLSVersion.TLSv1_2
        handler = LoopbackHTTPSHandler(context, loopback_target(connect_to)) if connect_to is not None else urllib.request.HTTPSHandler(context=context)
        self.cookies = http.cookiejar.CookieJar()
        self.opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect(),
            urllib.request.HTTPCookieProcessor(self.cookies), handler)

    def revoke(self, kind):
        prefix = "/api/v1/auth" if kind == "staff" else "/invoice-api/v1/auth"
        previous = [copy.copy(cookie) for cookie in self.cookies]
        try:
            body, media, status = self.raw("POST", prefix + "/logout", b"{}", expected=(200,) if kind == "staff" else (200, 401), with_status=True)
            require(media == "application/json", "LOGOUT_VERDICT_MISSING")
            verdict = json_object(body)
            if status == 200:
                require(verdict.get("ok") is True and "error" not in verdict, "LOGOUT_VERDICT_MISSING")
            else:
                require(kind == "user" and isinstance(verdict.get("error"), dict) and verdict["error"].get("code") == "AUTH_REQUIRED", "LOGOUT_VERDICT_MISSING")
            # Replay the original cookie, so clearing it in the logout response
            # cannot masquerade as actual server-side revocation.
            self.cookies.clear()
            for cookie in previous:
                self.cookies.set_cookie(cookie)
            path = "/invoice-api/v1/auth/staff-session" if kind == "staff" else prefix + "/session"
            body, media = self.raw("GET", path, expected=(200,))
            verdict = json_object(body)
            require(media == "application/json" and verdict.get("authenticated") is False and "error" not in verdict, "SESSION_STILL_AUTHENTICATED")
        finally:
            self.cookies.clear()

    def request_allowed(self, method, path, payload):
        return method in ("GET", "HEAD") or (method, path) in AUTH_WRITES or ((method, path) in OLD_ROUTES and payload in (None, b"{}"))

    def raw(self, method, path, payload=None, expected=(200,), content_type="application/json", *, with_status=False):
        require(path.startswith("/") and not path.startswith("//") and not urllib.parse.urlsplit(path).netloc and "\\" not in path, "HTTP_PATH_REJECTED")
        require(self.request_allowed(method, path, payload), "SMOKE_WRITE_FORBIDDEN")
        headers = {"Accept": "application/json, application/pdf, text/html", "User-Agent": "xingmang-rehearsal-smoke/1", "Origin": self.base, "Sec-Fetch-Site": "same-origin"}
        if method not in ("GET", "HEAD"):
            headers["Content-Type"] = content_type
            headers["X-Requested-With"] = "xingmang"
            if self.csrf:
                headers["X-CSRF-Token"] = self.csrf
        request = urllib.request.Request(self.base + path, data=payload, headers=headers, method=method)
        started = utc()
        status = None
        phase, headers_at, response_bytes = "connect_or_headers", None, None
        try:
            try:
                response = self.opener.open(request, timeout=30)
            except urllib.error.HTTPError as exc:
                response = exc
            with response:
                status = response.code
                phase, headers_at = "body", utc()
                body = response.read((12 << 20) + 1)
                response_bytes, phase = len(body), "complete"
                media = response.headers.get("Content-Type", "").split(";", 1)[0].lower()
            require(len(body) <= 12 << 20, "HTTP_BODY_TOO_LARGE")
            require(status in expected and not 300 <= status < 400, "HTTP_STATUS_UNEXPECTED")
            return (body, media, status) if with_status else (body, media)
        except (OSError, urllib.error.URLError, TimeoutError):
            raise SmokeFailure("HTTP_TRANSPORT_FAILED") from None
        finally:
            self.records.append({"method": method, "path": urllib.parse.urlsplit(path).path, "status": status,
                "utc_start": started, "utc_end": utc(), "transport_phase": phase,
                "headers_received_utc": headers_at, "response_bytes": response_bytes})

    def call(self, method, path, payload=None, expected=(200,)):
        data = None if payload is None else json.dumps(payload, separators=(",", ":")).encode()
        body, media = self.raw(method, path, data, expected)
        require(media == "application/json", "JSON_CONTENT_TYPE_REQUIRED")
        return json_object(body)

    def denied(self, method, path, payload=None, expected=(401, 403, 404)):
        data = None if payload is None else json.dumps(payload).encode()
        self.raw(method, path, data, expected)


def totp(secret, at=None):
    try:
        if re.fullmatch(r"[A-Z2-7]{16,128}", secret) is None:
            raise ValueError()
        key = base64.b32decode(secret + "=" * (-len(secret) % 8))
        digest = hmac.new(key, struct.pack(">Q", int(time.time() if at is None else at) // 30), hashlib.sha1).digest()
        offset = digest[-1] & 15
        number = struct.unpack(">I", digest[offset:offset + 4])[0] & 0x7fffffff
        return f"{number % 1000000:06d}"
    except (ValueError, TypeError):
        raise InputFailure("TOTP_FILE_FORMAT_INVALID") from None


def login_user(client, item, kind, read_credential=credential):
    response = client.call("POST", "/invoice-api/v1/auth/platform-login", {"platform": kind, "identifier": item["identifier"], "password": read_credential(item["password_file"])})
    require(response.get("ok") is True, "SOURCE_LOGIN_INCOMPLETE")
    session = client.call("GET", "/invoice-api/v1/auth/session")
    user = session.get("user", {})
    require(session.get("authenticated") is True and user.get("role") == "user" and user.get("platform") == kind and is_uuid(user.get("id")), "USER_SESSION_WRONG")
    require(isinstance(session.get("csrf_token"), str) and 43 <= len(session["csrf_token"]) <= 128, "USER_CSRF_MISSING")
    client.csrf = session["csrf_token"]
    return user["id"]


def login_staff(client, item, required_role, read_credential=credential):
    pending = client.call("POST", "/api/v1/auth/login", {"username": item["username"], "password": read_credential(item["password_file"])})
    require(pending.get("requires_totp") is True and isinstance(pending.get("temp_token"), str) and bool(pending["temp_token"]), "REAL_TOTP_CHALLENGE_REQUIRED")
    session = client.call("POST", "/api/v1/auth/login/totp", {"temp_token": pending["temp_token"], "code": totp(read_credential(item["totp_file"]))})
    require(session.get("requires_totp") is False and session.get("totp_enrolled") is True and session.get("must_change_password") is False and session.get("must_enroll_totp") is False, "TOTP_LOGIN_INCOMPLETE")
    require(session.get("username") == item["username"] and isinstance(session.get("roles"), list) and required_role in session["roles"], "STAFF_LOGIN_IDENTITY_WRONG")
    staff = client.call("GET", "/invoice-api/v1/auth/staff-session")
    check_staff(staff)
    client.csrf = staff["csrf_token"]


def run(config, *, _preview=None, _credentials=None):
    result = {"schema": SCHEMA, "status": "FAIL", "exit_code": 1, "utc_start": utc(), "steps": [], "http": [], "evidence_kind": "actual-http-smoke", "email_proof": "restored-existing-profile; no new verification performed", "management_page_coverage": "HTTP shell plus authenticated admin API; DOM rendering requires separate browser evidence"}
    def step(name, action):
        record = {"name": name, "status": "FAIL", "exit_code": 1, "utc_start": utc()}
        result["steps"].append(record)
        try:
            value = action()
            record.update(status="PASS", exit_code=0)
            return value
        finally:
            record["utc_end"] = utc()
    sessions = []
    required_steps = REQUIRED
    try:
        require(_credentials is None or _preview is not None, 'FROZEN_CREDENTIAL_READER_REQUIRES_PREVIEW')
        validate_config(config, _credentials=_credentials)
        read_credential = credential if _credentials is None else _credentials.read_credential
        if _preview is not None:
            from preview_smoke import FrozenPermit, REQUIRED as PREVIEW_REQUIRED
            require(type(_preview) is FrozenPermit, "VERIFIED_FROZEN_PERMIT_REQUIRED")
            _preview.check_config(config)
            required_steps = PREVIEW_REQUIRED
        result["mode"] = config["mode"]
        creds, expected = config["credentials"], config["expected"]
        def client(role):
            factory = Client if _preview is None else _preview.client
            return factory(config["origins"][role], config.get("ca_file"), result["http"], config.get("connect_to", {}).get(role))
        admin, sub, new = client("admin"), client("user"), client("user")
        result.update(origins=config["origins"], financial_writes_permitted=_preview is not None,
                      configuration_sha256=hashlib.sha256(json.dumps(config, sort_keys=True, separators=(",", ":")).encode()).hexdigest())
        if _preview is not None:
            result.update(evidence_kind="actual-frozen-preview-smoke", frozen_write_scope=_preview.proof)
        def with_session(client, kind, action):
            sessions.append((client, kind))
            return action()
        def readiness():
            if _preview is not None:
                from lifecycle import require_readiness_report
                return require_readiness_report(admin.call("GET", "/readyz?report=full"), require_freshness=False)
            return check_ready(admin.call("GET", "/readyz"))
        step("readiness.before", readiness)
        sub_principal = step("sub.login", lambda: with_session(sub, "user", lambda: login_user(sub, creds["sub2api"], "sub2api", read_credential)))
        new_principal = step("new.login", lambda: with_session(new, "user", lambda: login_user(new, creds["newapi"], "newapi", read_credential)))
        def isolation():
            require(sub_principal != new_principal, "PRINCIPAL_ISOLATION_FAILED")
            pools = {}
            for kind, client in (("sub2api", sub), ("newapi", new)):
                pools[kind] = client.call("GET", "/invoice-api/v1/user/funding-lots").get("items")
                check_scope(pools[kind], creds[kind]["source_id"], kind)
                client.denied("GET", "/invoice-api/v1/admin/invoice-requests", expected=(401, 403))
            require(not ({x["id"] for x in pools["sub2api"]} & {x["id"] for x in pools["newapi"]}), "LOT_ISOLATION_FAILED")
            profiles = sub.call("GET", "/invoice-api/v1/user/profiles").get("items")
            require(isinstance(profiles, list), "PROFILE_LIST_INVALID")
            matching = [p for p in profiles if p.get("id") == creds["sub2api"]["existing_profile_id"]]
            require(len(matching) == 1, "EXISTING_PROFILE_MISSING")
            check_profile(matching[0], sub_principal, read_credential(creds["sub2api"]["expected_email_file"]))
        step("source.isolation", isolation)
        step("staff.totp", lambda: with_session(admin, "staff", lambda: login_staff(admin, creds["staff"], expected.get("required_staff_role", "admin"), read_credential)))
        def page():
            body, media = admin.raw("GET", "/finance?sub=invoicing")
            require(media == "text/html" and b"<html" in body.lower(), "MANAGEMENT_SHELL_UNAVAILABLE")
            response = admin.call("GET", "/invoice-api/v1/admin/invoice-requests")
            require(isinstance(response.get("items"), list), "MANAGEMENT_API_UNAVAILABLE")
        step("staff.page", page)
        def retired():
            for method, path in OLD_ROUTES:
                admin.denied(method, path, {} if method == "POST" else None, expected=(404, 405, 410))
        step("legacy.rejected", retired)
        def requests_read():
            seen = set()
            for client, other, kind in ((sub, new, "sub2api"), (new, sub, "newapi")):
                items = client.call("GET", "/invoice-api/v1/user/invoice-requests").get("items")
                require(isinstance(items, list), "REQUEST_LIST_INVALID")
                for item in items:
                    require(isinstance(item, dict) and is_uuid(item.get("id")), "REQUEST_ID_INVALID")
                    require(item.get("source_instance_id") == creds[kind]["source_id"] and item.get("source_type") == kind, "REQUEST_SOURCE_CHANGED")
                    require(item["id"] not in seen, "REQUEST_ISOLATION_FAILED")
                    seen.add(item["id"])
                # The first existing record suffices for actual cross-user denial.
                # An empty account is never funded or issued an invoice by smoke.
                if items:
                    other.denied("GET", "/invoice-api/v1/user/invoice-requests/" + items[0]["id"], expected=(403, 404))
            result["existing_request_count"] = len(seen)
            result["invoice_write_coverage"] = "not performed: production-safe read-only probes"
        step("requests.read", requests_read)
        if _preview is not None:
            _preview.exercise(step, config, admin, sub, new, result)
        step("readiness.after", readiness)
        require(tuple(item["name"] for item in result["steps"] if item["status"] == "PASS") == required_steps[:-1], "INCOMPLETE_SMOKE")
        result.update(status="PASS", exit_code=0)
    except BaseException as exc:
        result.update(status="FAIL", exit_code=2 if isinstance(exc, InputFailure) else 1,
                      failure_code=exc.code if isinstance(exc, SmokeFailure) else "SMOKE_EXECUTION_FAILED",
                      failure_type=type(exc).__name__)
        # Preserve interruption semantics. The CLI writes the incomplete result
        # in its exception handler instead of emitting a stale PASS artifact.
        if not isinstance(exc, Exception):
            exc.smoke_result = result
            raise
    finally:
        if sessions:
            def revoke_sessions():
                failed = False
                for client, kind in reversed(sessions):
                    try:
                        client.revoke(kind)
                    except Exception:
                        failed = True
                require(not failed, "SESSION_CLEANUP_FAILED")
            try:
                step("sessions.revoked", revoke_sessions)
            except Exception:
                result.update(status="FAIL", exit_code=1, failure_code="SESSION_CLEANUP_FAILED")
        if result["status"] == "PASS" and tuple(x["name"] for x in result["steps"] if x["status"] == "PASS") != required_steps:
            result.update(status="FAIL", exit_code=1, failure_code="INCOMPLETE_SMOKE")
        result["utc_end"] = utc()
    return result


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config", required=True)
    parser.add_argument("--output", required=True)
    args = parser.parse_args(argv)
    result = {"schema": SCHEMA, "status": "FAIL", "exit_code": 2, "utc_start": utc(), "steps": [], "failure_code": "CONFIG_UNREADABLE"}
    interrupted = None
    try:
        config = json_object(checked_file(args.config).read_bytes())
        result = run(config)
    except BaseException as exc:
        if not isinstance(exc, Exception):
            interrupted = exc
            result = getattr(exc, "smoke_result", {**result, "exit_code": 1, "failure_code": "SMOKE_INTERRUPTED"})
        else:
            result["failure_code"] = exc.code if isinstance(exc, SmokeFailure) else "CONFIG_UNREADABLE"
    finally:
        result["utc_end"] = utc()
        # Unique per-invocation output is supplied by the engine. Replace only
        # after a complete serialized report exists; never print private config.
        destination = Path(args.output)
        temporary = destination.with_name(destination.name + "." + uuid.uuid4().hex + ".tmp")
        try:
            with temporary.open("x", encoding="utf-8", newline="\n") as stream:
                json.dump(result, stream, ensure_ascii=True, indent=2)
                stream.write("\n")
            os.replace(temporary, destination)
        except OSError:
            return 1
    if interrupted is not None:
        raise interrupted
    return result["exit_code"]


if __name__ == "__main__":
    raise SystemExit(main())

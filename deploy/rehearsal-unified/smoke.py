"""06 D HTTP smoke. Uses restored profiles and baseline wallet eligibility.

No database writes, shell commands, identity fabrication, TLS bypass, or remote
origins. Credentials are consumed only from named restricted files at runtime.
Unit simulations do not qualify a deployment; only this CLI's real HTTP run does.
"""
import argparse
import base64
import datetime as dt
import hashlib
import hmac
import http.cookiejar
import ipaddress
import json
import os
from pathlib import Path
import re
import ssl
import stat
import struct
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid


SCHEMA = "xingmang.unified.smoke/v1"
REQUIRED = ("readiness.before", "sub.login", "new.login", "source.isolation",
            "staff.totp", "staff.page", "legacy.rejected", "sub.submit",
            "staff.approve-upload-download", "readiness.after")
OLD_ROUTES = (
    ("GET", "/invoice-api/v1/auth/login"),
    ("GET", "/invoice-api/v1/auth/callback"),
    ("GET", "/invoice-api/v1/auth/admin/step-up"),
    ("POST", "/invoice-api/v1/auth/backchannel-logout"),
    ("POST", "/invoice-api/v1/auth/console-assertion"),
    ("POST", "/api/v1/auth/console-assertion"),
)


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


def check_request(value, source_id, amount, lot_id, state, prior_version=0):
    require(isinstance(value, dict) and is_uuid(value.get("id")), "REQUEST_ID_INVALID")
    require(value.get("source_instance_id") == source_id and value.get("source_type") == "sub2api", "REQUEST_SOURCE_CHANGED")
    require(type(value.get("amount_minor")) is int and value["amount_minor"] == amount, "REQUEST_AMOUNT_CHANGED")
    require(value.get("status") == state, "REQUEST_STATE_WRONG")
    require(positive(value.get("version")) and value["version"] > prior_version, "REQUEST_VERSION_STALE")
    items = value.get("allocations")
    require(isinstance(items, list) and len(items) == 1 and isinstance(items[0], dict), "REQUEST_ALLOCATION_CHANGED")
    require(items[0].get("funding_lot_id") == lot_id and type(items[0].get("amount_minor")) is int and items[0]["amount_minor"] == amount, "REQUEST_ALLOCATION_CHANGED")


def check_staff(value):
    require(isinstance(value, dict) and value.get("authenticated") is True, "STAFF_NOT_AUTHENTICATED")
    require(value.get("admin_step_up_required") is False, "STAFF_MFA_NOT_FRESH")
    user = value.get("user")
    require(isinstance(user, dict) and user.get("role") == "admin" and is_uuid(user.get("id")), "STAFF_ROLE_INVALID")
    token = value.get("csrf_token")
    require(isinstance(token, str) and 43 <= len(token) <= 128 and re.fullmatch(r"[A-Za-z0-9_-]+", token) is not None, "STAFF_CSRF_INVALID")


def check_download(body, expected):
    require(isinstance(body, bytes) and body.startswith(b"%PDF-"), "DOWNLOAD_NOT_PDF")
    require(hmac.compare_digest(hashlib.sha256(body).digest(), hashlib.sha256(expected).digest()), "DOWNLOAD_HASH_MISMATCH")


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
        if not isinstance(raw, str) or parsed.scheme != "https" or not parsed.netloc or parsed.path or parsed.query or parsed.fragment or parsed.username or parsed.password or parsed.port is None:
            raise ValueError()
        host = parsed.hostname
        if host != "localhost" and not ipaddress.ip_address(host).is_loopback:
            raise ValueError()
        if any(c in raw for c in "\\\r\n\t "):
            raise ValueError()
        return raw
    except (TypeError, ValueError, AttributeError):
        raise InputFailure("HTTPS_LOOPBACK_ORIGIN_REQUIRED") from None


def validate_config(config):
    try:
        if config["schema"] != SCHEMA or config["mode"] not in ("local-synthetic", "server-rehearsal"):
            raise InputFailure("CONFIG_SCHEMA_MODE_INVALID")
        for role in ("admin", "user"):
            origin(config["origins"][role])
        checked_file(config["ca_file"])
        creds = config["credentials"]
        for role in ("staff", "sub2api", "newapi"):
            item = creds[role]
            if not isinstance(item.get("username" if role == "staff" else "identifier"), str) or not item.get("username" if role == "staff" else "identifier"):
                raise InputFailure("ACCOUNT_IDENTIFIER_REQUIRED")
            checked_file(item["password_file"], private=True)
        checked_file(creds["staff"]["totp_file"], private=True)
        checked_file(creds["sub2api"]["expected_email_file"], private=True)
        if not is_uuid(creds["sub2api"]["existing_profile_id"]):
            raise InputFailure("EXISTING_PROFILE_ID_REQUIRED")
        ids = [creds[k]["source_id"] for k in ("sub2api", "newapi")]
        if any(not is_uuid(value) for value in ids) or ids[0] == ids[1]:
            raise InputFailure("DISTINCT_SOURCE_IDS_REQUIRED")
        expected = config["expected"]
        if not positive(expected["request_amount_minor"]) or not positive(expected["minimum_available_minor"]) or expected["minimum_available_minor"] < expected["request_amount_minor"]:
            raise InputFailure("AMOUNT_CONFIG_INVALID")
        if not isinstance(expected.get("required_staff_role", "admin"), str) or not expected.get("required_staff_role", "admin"):
            raise InputFailure("STAFF_ROLE_CONFIG_INVALID")
    except (KeyError, TypeError):
        raise InputFailure("CONFIG_FIELD_MISSING") from None


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        raise SmokeFailure("HTTP_REDIRECT_REJECTED")


class Client:
    def __init__(self, base, ca_file, records):
        self.base = origin(base)
        self.records = records
        self.csrf = ""
        context = ssl.create_default_context(cafile=str(checked_file(ca_file)))
        context.minimum_version = ssl.TLSVersion.TLSv1_2
        self.opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect(),
            urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()), urllib.request.HTTPSHandler(context=context))

    def raw(self, method, path, payload=None, expected=(200,), content_type="application/json"):
        require(path.startswith("/") and not path.startswith("//") and not urllib.parse.urlsplit(path).netloc and "\\" not in path, "HTTP_PATH_REJECTED")
        headers = {"Accept": "application/json, application/pdf, text/html", "User-Agent": "xingmang-rehearsal-smoke/1", "Origin": self.base, "Sec-Fetch-Site": "same-origin"}
        if method not in ("GET", "HEAD"):
            headers["Content-Type"] = content_type
            headers["X-Requested-With"] = "xingmang"
            if self.csrf:
                headers["X-CSRF-Token"] = self.csrf
        request = urllib.request.Request(self.base + path, data=payload, headers=headers, method=method)
        started = utc()
        status = None
        try:
            try:
                response = self.opener.open(request, timeout=30)
            except urllib.error.HTTPError as exc:
                response = exc
            with response:
                status = response.code
                body = response.read((12 << 20) + 1)
                media = response.headers.get("Content-Type", "").split(";", 1)[0].lower()
            require(len(body) <= 12 << 20, "HTTP_BODY_TOO_LARGE")
            require(status in expected and not 300 <= status < 400, "HTTP_STATUS_UNEXPECTED")
            return body, media
        except (OSError, urllib.error.URLError, TimeoutError):
            raise SmokeFailure("HTTP_TRANSPORT_FAILED") from None
        finally:
            self.records.append({"method": method, "path": urllib.parse.urlsplit(path).path, "status": status, "utc_start": started, "utc_end": utc()})

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


def synthetic_pdf():
    objects = [b"<< /Type /Catalog /Pages 2 0 R >>", b"<< /Type /Pages /Kids [3 0 R] /Count 1 >>", b"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>"]
    text = b"BT /F1 18 Tf 72 720 Td (Synthetic rehearsal invoice) Tj ET\n"
    objects += [b"<< /Length " + str(len(text)).encode() + b" >>\nstream\n" + text + b"endstream", b"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"]
    body = bytearray(b"%PDF-1.4\n%\xe2\xe3\xcf\xd3\n")
    offsets = []
    for index, obj in enumerate(objects, 1):
        offsets.append(len(body))
        body.extend(f"{index} 0 obj\n".encode() + obj + b"\nendobj\n")
    xref = len(body)
    body.extend(b"xref\n0 6\n0000000000 65535 f \n")
    for offset in offsets:
        body.extend(f"{offset:010d} 00000 n \n".encode())
    body.extend(f"trailer\n<< /Size 6 /Root 1 0 R >>\nstartxref\n{xref}\n%%EOF\n".encode())
    return bytes(body)


def multipart(fields, pdf):
    boundary = "rehearsal-" + uuid.uuid4().hex
    chunks = []
    for name, value in fields.items():
        chunks.append(f'--{boundary}\r\nContent-Disposition: form-data; name="{name}"\r\n\r\n{value}\r\n'.encode())
    chunks += [f'--{boundary}\r\nContent-Disposition: form-data; name="file"; filename="synthetic.pdf"\r\nContent-Type: application/pdf\r\n\r\n'.encode(), pdf, f"\r\n--{boundary}--\r\n".encode()]
    return b"".join(chunks), "multipart/form-data; boundary=" + boundary


def login_user(client, item, kind):
    response = client.call("POST", "/invoice-api/v1/auth/platform-login", {"platform": kind, "identifier": item["identifier"], "password": credential(item["password_file"])})
    require(response.get("ok") is True, "SOURCE_LOGIN_INCOMPLETE")
    session = client.call("GET", "/invoice-api/v1/auth/session")
    user = session.get("user", {})
    require(session.get("authenticated") is True and user.get("role") == "user" and user.get("platform") == kind and is_uuid(user.get("id")), "USER_SESSION_WRONG")
    require(isinstance(session.get("csrf_token"), str) and 43 <= len(session["csrf_token"]) <= 128, "USER_CSRF_MISSING")
    client.csrf = session["csrf_token"]
    return user["id"]


def login_staff(client, item, required_role):
    pending = client.call("POST", "/api/v1/auth/login", {"username": item["username"], "password": credential(item["password_file"])})
    require(pending.get("requires_totp") is True and isinstance(pending.get("temp_token"), str) and bool(pending["temp_token"]), "REAL_TOTP_CHALLENGE_REQUIRED")
    session = client.call("POST", "/api/v1/auth/login/totp", {"temp_token": pending["temp_token"], "code": totp(credential(item["totp_file"]))})
    require(session.get("requires_totp") is False and session.get("totp_enrolled") is True and session.get("must_change_password") is False and session.get("must_enroll_totp") is False, "TOTP_LOGIN_INCOMPLETE")
    require(session.get("username") == item["username"] and isinstance(session.get("roles"), list) and required_role in session["roles"], "STAFF_LOGIN_IDENTITY_WRONG")
    staff = client.call("GET", "/invoice-api/v1/auth/staff-session")
    check_staff(staff)
    client.csrf = staff["csrf_token"]


def run(config):
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
    try:
        validate_config(config)
        result["mode"] = config["mode"]
        creds, expected = config["credentials"], config["expected"]
        admin = Client(config["origins"]["admin"], config["ca_file"], result["http"])
        sub = Client(config["origins"]["user"], config["ca_file"], result["http"])
        new = Client(config["origins"]["user"], config["ca_file"], result["http"])
        step("readiness.before", lambda: check_ready(admin.call("GET", "/readyz")))
        sub_principal = step("sub.login", lambda: login_user(sub, creds["sub2api"], "sub2api"))
        new_principal = step("new.login", lambda: login_user(new, creds["newapi"], "newapi"))
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
            check_profile(matching[0], sub_principal, credential(creds["sub2api"]["expected_email_file"]))
            choices = [p for p in pools["sub2api"] if p.get("eligibility_kind") == "wallet" and p.get("verification") == "verified" and p.get("refund_frozen") is False and p.get("eligibility_status") == "active" and positive(p.get("consumed_cash_minor")) and type(p.get("available_minor")) is int and p["consumed_cash_minor"] >= p["available_minor"] >= expected["minimum_available_minor"]]
            require(bool(choices), "BASELINE_CONSUMED_WALLET_REQUIRED")
            return sorted(choices, key=lambda p: p["id"])[0]["id"]
        lot_id = step("source.isolation", isolation)
        step("staff.totp", lambda: login_staff(admin, creds["staff"], expected.get("required_staff_role", "admin")))
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
        amount, source_id = expected["request_amount_minor"], creds["sub2api"]["source_id"]
        def submit():
            value = sub.call("POST", "/invoice-api/v1/user/invoice-requests", {"profile_id": creds["sub2api"]["existing_profile_id"], "source_instance_id": source_id, "idempotency_key": str(uuid.uuid4()), "allocations": [{"funding_lot_id": lot_id, "amount_minor": amount}]}, expected=(201,))
            check_request(value, source_id, amount, lot_id, "pending_review")
            new.denied("GET", "/invoice-api/v1/user/invoice-requests/" + value["id"], expected=(403, 404))
            result["request_id"] = value["id"]
            return value
        submitted = step("sub.submit", submit)
        def issue():
            path = "/invoice-api/v1/admin/invoice-requests/" + submitted["id"]
            sub.denied("POST", path + "/review", {"action": "approve", "version": submitted["version"]}, expected=(401, 403))
            current = submitted
            for suffix, state in (("/review", "approved"), ("/begin-manual-issue", "manual_issuing"), ("/confirm-manual-issue", "issued_awaiting_document")):
                payload = {"version": current["version"]}
                if suffix == "/review":
                    payload.update(action="approve", note="Isolated rehearsal")
                value = admin.call("POST", path + suffix, payload)
                check_request(value, source_id, amount, lot_id, state, current["version"])
                require(value["id"] == submitted["id"], "REQUEST_ID_CHANGED")
                current = value
            # Use the actual server transition time, preserving subsecond
            # precision and avoiding host/server clock disagreement.
            issued_at = current.get("updated_at")
            require(isinstance(issued_at, str) and dt.datetime.fromisoformat(issued_at.replace("Z", "+00:00")).utcoffset() is not None, "SERVER_ISSUED_TIME_MISSING")
            pdf = synthetic_pdf()
            data, media = multipart({"version": str(current["version"]), "invoice_number": "REHEARSAL-" + uuid.uuid4().hex[:16], "issued_at": issued_at}, pdf)
            body, response_media = admin.raw("POST", path + "/documents/upload", data, (201,), media)
            require(response_media == "application/json", "UPLOAD_RESPONSE_INVALID")
            uploaded = json_object(body)
            check_request(uploaded.get("request"), source_id, amount, lot_id, "issued", current["version"])
            require(uploaded["request"]["id"] == submitted["id"], "REQUEST_ID_CHANGED")
            document = uploaded.get("document", {})
            sha = hashlib.sha256(pdf).hexdigest()
            require(is_uuid(document.get("id")) and document.get("request_id") == submitted["id"] and document.get("scan_status") == "clean" and document.get("sha256") == sha, "SCANNED_DOCUMENT_INVALID")
            user_path = "/invoice-api/v1/user/invoice-requests/" + submitted["id"] + "/document"
            for client, download_path in ((sub, user_path), (admin, path + "/document")):
                downloaded, mime = client.raw("GET", download_path)
                require(mime == "application/pdf", "DOWNLOAD_CONTENT_TYPE_INVALID")
                check_download(downloaded, pdf)
            new.denied("GET", user_path, expected=(403, 404))
            result.update(amount_minor=amount, document_sha256=sha, document_id=document["id"])
        step("staff.approve-upload-download", issue)
        step("readiness.after", lambda: check_ready(admin.call("GET", "/readyz")))
        require(tuple(item["name"] for item in result["steps"] if item["status"] == "PASS") == REQUIRED, "INCOMPLETE_SMOKE")
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

"""N-1: financial HTTP probes exclusively on verified frozen rehearsal copies.

No CLI write switch exists. Production E uses smoke.Client's read-only guard.
The caller must own fresh data volumes/networks and separate preview listeners.
"""
import datetime as dt
import hashlib
import hmac
import json
import time
import uuid
from pathlib import Path

import smoke
from smoke import require, is_uuid, json_object
from lifecycle import OperatorError, atomic_json, read_public_json

REQUIRED = (*smoke.REQUIRED[:-2], "sub.submit", "staff.approve-upload-download", *smoke.REQUIRED[-2:])
_CAPABILITY = object()


def positive(value): return type(value) is int and value > 0

def config_hash(value):
    return hashlib.sha256(json.dumps(value, sort_keys=True, separators=(",", ":")).encode()).hexdigest()


def endpoint(config, role):
    if role in config.get("connect_to", {}):
        address, port = smoke.loopback_target(config["connect_to"][role])
        return address, port
    from urllib.parse import urlsplit
    url = urlsplit(smoke.origin(config["origins"][role]))
    return url.hostname, url.port or 443


class FrozenPermit:
    def __init__(self, capability, config, proof):
        require(capability is _CAPABILITY, "VERIFIED_FROZEN_PERMIT_REQUIRED")
        require(config.get("mode") in ("local-synthetic", "server-rehearsal"), "PRODUCTION_WRITES_FORBIDDEN")
        expected = config.get("expected", {})
        require(positive(expected.get("request_amount_minor")) and positive(expected.get("minimum_available_minor"))
                and expected["minimum_available_minor"] >= expected["request_amount_minor"], "PREVIEW_AMOUNT_REQUIRED")
        self.configuration_sha256 = config_hash(config)
        self.proof, self.request_id = proof, None

    def check_config(self, config):
        require(config.get("mode") != "production" and config_hash(config) == self.configuration_sha256,
                "PREVIEW_CONFIGURATION_CHANGED")

    def allowed(self, method, path):
        if method != "POST": return False
        if path == "/invoice-api/v1/user/invoice-requests": return self.request_id is None
        if not is_uuid(self.request_id): return False
        prefix = "/invoice-api/v1/admin/invoice-requests/" + self.request_id
        return path in {prefix + suffix for suffix in ("/review", "/begin-manual-issue", "/confirm-manual-issue", "/documents/upload")}

    def client(self, origin, ca, records, target):
        return PreviewClient(origin, ca, records, target, permit=self)

    def exercise(self, step, config, admin, sub, new, result):
        self.check_config(config)
        expected, creds = config["expected"], config["credentials"]
        pools = sub.call("GET", "/invoice-api/v1/user/funding-lots").get("items")
        smoke.check_scope(pools, creds["sub2api"]["source_id"], "sub2api")
        choices = [p for p in pools if p.get("eligibility_kind") == "wallet" and p.get("verification") == "verified"
            and p.get("refund_frozen") is False and p.get("eligibility_status") == "active"
            and positive(p.get("consumed_cash_minor")) and type(p.get("available_minor")) is int
            and p["consumed_cash_minor"] >= p["available_minor"] >= expected["minimum_available_minor"]]
        require(bool(choices), "BASELINE_CONSUMED_WALLET_REQUIRED")
        lot_id = sorted(choices, key=lambda p: p["id"])[0]["id"]
        amount, source_id = expected["request_amount_minor"], creds["sub2api"]["source_id"]
        def submit():
            value = sub.call("POST", "/invoice-api/v1/user/invoice-requests", {"profile_id": creds["sub2api"]["existing_profile_id"], "source_instance_id": source_id, "idempotency_key": str(uuid.uuid4()), "allocations": [{"funding_lot_id": lot_id, "amount_minor": amount}]}, expected=(201,))
            check_request(value, source_id, amount, lot_id, "pending_review")
            new.denied("GET", "/invoice-api/v1/user/invoice-requests/" + value["id"], expected=(403, 404))
            self.request_id = value["id"]
            result["request_id"] = value["id"]
            return value
        submitted = step("sub.submit", submit)
        self.request_id = submitted["id"]
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
        result["invoice_write_coverage"] = "actual frozen-copy submission, approval, scan/upload and both SHA-verified downloads"


class PreviewClient(smoke.Client):
    def __init__(self, *args, permit, **kwargs):
        require(type(permit) is FrozenPermit, "VERIFIED_FROZEN_PERMIT_REQUIRED")
        self.permit = permit
        super().__init__(*args, **kwargs)

    def request_allowed(self, method, path, payload):
        return super().request_allowed(method, path, payload) or self.permit.allowed(method, path)


def verified_permit(driver, deployment_config):
    from restore import validate_frozen_mounts, verify_project_resource_owners, volume_metadata, require_owned_volume
    config = driver.config
    require(config["mode"] in ("local-synthetic", "server-rehearsal"), "production cannot run financial probes")
    value = config["rehearsal"]
    actual = {p["name"] for p in config["candidate"]["projects"]}
    expected = {p["name"] for p in value["projects"]}
    live = {p["name"] for side in ("candidate", "previous") for p in deployment_config[side]["projects"]}
    require(len(actual) == 2 and actual == expected and not actual.intersection(live)
            and all(name.startswith("xm-rehearsal-") for name in actual), "write probe must target only independent frozen projects")
    require(config["smoke_config"] == value["smoke_config"] and config["candidate"]["ready_url"] == value["ready_url"],
            "write probe endpoints do not match frozen descriptor")
    probe = read_public_json(config["smoke_config"])
    online = read_public_json(deployment_config["smoke_config"])
    smoke.validate_config(probe, _credentials=getattr(driver, 'rehearsal_seed', None))
    live_ports = {endpoint(online, role)[1] for role in ("admin", "user")}
    for role in ("admin", "user"):
        require(role in probe.get("connect_to", {}), "write preview requires explicit loopback TLS pinning")
        require(endpoint(probe, role)[1] not in live_ports, "write preview cannot reuse a live TLS listener")
    # Recheck actual resources immediately before any authenticated write probe.
    driver.artifact_preflight()
    validate_frozen_mounts(driver, value)
    for name in value["volumes"].values():
        require_owned_volume(volume_metadata(driver, name), name, value["owner_id"])
    inventory = driver.inventory("candidate", allow_deferred_invoice_health=True)
    require(len(inventory) == 18, "write preview requires all eighteen verified frozen runtime containers")
    verify_project_resource_owners(driver, value)
    proof = {"owner_id":value["owner_id"], "projects":sorted(actual), "volumes":sorted(value["volumes"].values()),
             "head":config["candidate"]["head"], "manifest_sha256":config["candidate"]["manifest_sha256"],
             "runtime_containers":18, "verified_utc":smoke.utc()}
    return FrozenPermit(_CAPABILITY, probe, proof), probe


def run_verified(driver, deployment_config):
    started = smoke.utc()
    result = {"status":"FAIL", "exit_code":1, "steps":[], "evidence_kind":"actual-frozen-preview-smoke"}
    try:
        permit, config = verified_permit(driver, deployment_config)
        result = smoke.run(config, _preview=permit, _credentials=getattr(driver, 'rehearsal_seed', None))
        if getattr(driver, 'rehearsal_seed', None):
            result.update(identity_coverage='synthetic seeded frozen identities only', real_customer_login_verified=False)
        require(result.get("status") == "PASS" and result.get("exit_code") == 0
                and result.get("financial_writes_permitted") is True
                and tuple(r["name"] for r in result["steps"] if r["status"] == "PASS") == REQUIRED,
                "FROZEN_WRITE_SMOKE_INCOMPLETE")
        return result
    except BaseException as error:
        if hasattr(error, "smoke_result"): result = error.smoke_result
        result.update(status="FAIL", exit_code=result.get("exit_code") or 1)
        raise
    finally:
        atomic_json(driver.output / "smoke.json", result)
        atomic_json(driver.output / "events" / (str(time.time_ns()) + "-http-preview-smoke.json"),
            {"operation":"http-preview-smoke", "start_utc":started, "end_utc":smoke.utc(), "exit_code":result.get("exit_code",1)})


def check_request(value, source_id, amount, lot_id, state, prior_version=0):
    require(isinstance(value, dict) and is_uuid(value.get("id")), "REQUEST_ID_INVALID")
    require(value.get("source_instance_id") == source_id and value.get("source_type") == "sub2api", "REQUEST_SOURCE_CHANGED")
    require(type(value.get("amount_minor")) is int and value["amount_minor"] == amount, "REQUEST_AMOUNT_CHANGED")
    require(value.get("status") == state, "REQUEST_STATE_WRONG")
    require(positive(value.get("version")) and value["version"] > prior_version, "REQUEST_VERSION_STALE")
    items = value.get("allocations")
    require(isinstance(items, list) and len(items) == 1 and isinstance(items[0], dict), "REQUEST_ALLOCATION_CHANGED")
    require(items[0].get("funding_lot_id") == lot_id and type(items[0].get("amount_minor")) is int and items[0]["amount_minor"] == amount, "REQUEST_ALLOCATION_CHANGED")

def check_download(body, expected):
    require(isinstance(body, bytes) and body.startswith(b"%PDF-"), "DOWNLOAD_NOT_PDF")
    require(hmac.compare_digest(hashlib.sha256(body).digest(), hashlib.sha256(expected).digest()), "DOWNLOAD_HASH_MISMATCH")

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

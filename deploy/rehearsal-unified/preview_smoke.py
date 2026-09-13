"""N-1: financial HTTP probes exclusively on verified frozen rehearsal copies.

No CLI write switch exists. Production E uses smoke.Client's read-only guard.
The caller must own fresh data volumes/networks and separate preview listeners.
"""
import datetime as dt
import hashlib
import hmac
import json
import re
import time
import uuid
from pathlib import Path

import smoke
from smoke import require, is_uuid, json_object
from lifecycle import OperatorError, ReadinessBudget, atomic_json, read_public_json, require_readiness_report

REQUIRED = (*smoke.REQUIRED[:-2], "sub.submit", "staff.approve-upload-download", *smoke.REQUIRED[-2:])
BLOCKED_REQUIRED = (*smoke.REQUIRED[:-2], "sub.submit-blocked", "invoice.unchanged", *smoke.REQUIRED[-2:])
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
        self.required_steps, self._driver = REQUIRED, None

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
        expiry = None
        if config['mode'] == 'server-rehearsal':
            current = require_readiness_report(admin.call('GET', '/readyz?report=full'), require_freshness=False)
            if not current['source_freshness_ready']:
                expiry = current
        pools = sub.call("GET", "/invoice-api/v1/user/funding-lots").get("items")
        smoke.check_scope(pools, creds["sub2api"]["source_id"], "sub2api")
        choices = [p for p in pools if p.get("eligibility_kind") == "wallet" and p.get("verification") == "verified"
            and p.get("refund_frozen") is False and p.get("eligibility_status") == "active"
            and positive(p.get("consumed_cash_minor")) and type(p.get("available_minor")) is int
            and p["consumed_cash_minor"] >= p["available_minor"] >= expected["minimum_available_minor"]]
        if not choices and expiry is not None:
            return self.exercise_blocked(step, config, admin, sub, new, pools, self.expired_source_proof(expiry), result)
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
            issued_at = server_issued_time(current.get("updated_at"))
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
        result.update(financial_coverage='full-write', document_coverage='scan-upload-both-downloads')

    def expired_source_proof(self, current):
        """Reuse the actual attested D and its accepted raw base report."""
        from rehearsal_seed import Seed
        driver = self._driver
        require(driver is not None and driver.config['mode'] == 'server-rehearsal', 'EXPIRED_PREVIEW_FROZEN_DRIVER_REQUIRED')
        require(driver.config['candidate']['head'] == self.proof['head'] and
                driver.config['candidate']['manifest_sha256'] == self.proof['manifest_sha256'] and
                driver.config['rehearsal']['owner_id'] == self.proof['owner_id'] and self.proof['runtime_containers'] == 18,
                'EXPIRED_PREVIEW_BINDING_CHANGED')
        driver.artifact_preflight()
        seed = getattr(driver, 'rehearsal_seed', None)
        require(type(seed) is Seed and seed.driver is driver and seed.seeded, 'EXPIRED_PREVIEW_SEED_REQUIRED')
        budget = getattr(driver, 'readiness_budget', None)
        require(type(budget) is ReadinessBudget and budget.output == driver.output and budget.active is False,
                'EXPIRED_PREVIEW_BASE_PROOF_REQUIRED')
        value = read_public_json(driver.output / 'candidate-readiness.json')
        require(value == budget.value and value.get('status') == 'BASE_READY' and value.get('phase') == 'non_freshness'
                and value.get('budget_seconds') == 300 and value.get('invoice_latches') == 11,
                'EXPIRED_PREVIEW_BASE_PROOF_CHANGED')
        elapsed = value.get('elapsed_seconds')
        require(type(elapsed) in (int, float) and 0 <= elapsed <= 300 and
                value['first_accepted_monotonic'] - value['start_monotonic'] == elapsed and
                value['first_accepted_utc'] == value['end_utc'], 'EXPIRED_PREVIEW_BASE_DEADLINE_INVALID')
        index = value.get('accepted_observation')
        require(type(index) is int and index == len(value.get('observations', [])) and index > 0,
                'EXPIRED_PREVIEW_ACCEPTED_SAMPLE_INVALID')
        observation = value['observations'][index - 1]
        path = driver.output / ('candidate-readiness-' + str(index) + '.response')
        require(Path(observation['response_path']) == path and not path.is_symlink() and observation.get('http_status') == 200
                and observation.get('accepted_utc') == value['first_accepted_utc']
                and observation.get('accepted_monotonic') == value['first_accepted_monotonic'], 'EXPIRED_PREVIEW_RAW_BINDING_CHANGED')
        raw = path.read_bytes()
        require(hashlib.sha256(raw).hexdigest() == observation['response_sha256'], 'EXPIRED_PREVIEW_RAW_SHA_CHANGED')
        accepted = require_readiness_report(json_object(raw), require_freshness=False)
        require(current['source_freshness_ready'] is False and current['expected_source_expiry'],
                'EXPIRED_PREVIEW_TYPED_EXPIRY_REQUIRED')
        return {'accepted_response_sha256': observation['response_sha256'], 'base_elapsed_seconds': elapsed,
                'accepted_reasons': accepted['expected_source_expiry'], 'current_reasons': current['expected_source_expiry']}

    def exercise_blocked(self, step, config, admin, sub, new, pools, expiry, result):
        """A refused synthetic submission is distinct from a successful invoice."""
        creds, expected = config['credentials'], config['expected']
        source_id = creds['sub2api']['source_id']
        choices = [lot for lot in pools if lot.get('eligibility_kind') == 'wallet' and
                   lot.get('verification') == 'verified' and lot.get('refund_frozen') is False and
                   positive(lot.get('consumed_cash_minor')) and lot['consumed_cash_minor'] >= expected['minimum_available_minor'] and
                   lot.get('eligibility_status') == 'source_unavailable' and lot.get('reason_code') == 'SOURCE_NOT_READY' and
                   type(lot.get('available_minor')) is int and lot['available_minor'] == 0]
        require(len(choices) == 1, 'EXPIRED_PREVIEW_BLOCKED_WALLET_REQUIRED')
        lot = choices[0]; lot_id = lot['id']
        seed = self._driver.rehearsal_seed
        before = seed.blocked_invoice_snapshot(source_id, lot_id)
        raw_lot = before['lots']['sub2api']
        require(before['users'] == {kind: {'invoice_requests': 0} for kind in ('sub2api', 'newapi')} and
                raw_lot['id'] == lot_id and raw_lot['source_instance_id'] == source_id and
                raw_lot['consumed_cash_minor'] == lot['consumed_cash_minor'] and
                all(row['reserved_minor'] == row['issued_minor'] == 0 for row in before['lots'].values()),
                'EXPIRED_PREVIEW_SYNTHETIC_BASELINE_INVALID')

        def requests():
            result_sets = {}
            for kind, client in (('sub2api', sub), ('newapi', new)):
                value = client.call('GET', '/invoice-api/v1/user/invoice-requests')
                # A newly seeded user has no rows. DB counts below make this
                # independent of HTTP pagination or a hidden inserted record.
                require(value.get('items') == [] and value.get('has_more', False) is False,
                        'EXPIRED_PREVIEW_REQUEST_SET_NOT_EMPTY')
                result_sets[kind] = []
            return result_sets

        initial = requests()
        self.required_steps = BLOCKED_REQUIRED
        result.update(financial_coverage='blocked-write', document_coverage='not_exercised_source_expired',
                      invoice_write_coverage='source-expired submission refusal; no approval, upload or download performed',
                      source_expiry_proof=expiry, financial_writes_completed=False)

        def refused():
            current = require_readiness_report(admin.call('GET', '/readyz?report=full'), require_freshness=False)
            self.expired_source_proof(current)
            response = sub.call('POST', '/invoice-api/v1/user/invoice-requests',
                {'profile_id': creds['sub2api']['existing_profile_id'], 'source_instance_id': source_id,
                 'idempotency_key': str(uuid.uuid4()),
                 'allocations': [{'funding_lot_id': lot_id, 'amount_minor': expected['request_amount_minor']}]}, expected=(503,))
            require(isinstance(response.get('error'), dict) and response['error'].get('code') == 'SOURCE_SYNC_UNAVAILABLE',
                    'EXPIRED_PREVIEW_SOURCE_REFUSAL_REQUIRED')
            result['submission_refusal'] = {'http_status': 503, 'code': 'SOURCE_SYNC_UNAVAILABLE'}

        step('sub.submit-blocked', refused)

        def unchanged():
            require(requests() == initial, 'EXPIRED_PREVIEW_REQUESTS_CHANGED')
            after = seed.blocked_invoice_snapshot(source_id, lot_id)
            require(after == before, 'EXPIRED_PREVIEW_FINANCIAL_STATE_CHANGED')
            final_pools = sub.call('GET', '/invoice-api/v1/user/funding-lots').get('items')
            smoke.check_scope(final_pools, source_id, 'sub2api')
            require([row for row in final_pools if row['id'] == lot_id] == [lot], 'EXPIRED_PREVIEW_LOT_CHANGED')
            result['financial_state_unchanged'] = {'synthetic_user_request_counts': {k: 0 for k in before['users']},
                'same_request_sets': True, 'same_funding_lots': True,
                'snapshot_sha256': hashlib.sha256(json.dumps(before, sort_keys=True).encode()).hexdigest()}
        step('invoice.unchanged', unchanged)


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
    permit = FrozenPermit(_CAPABILITY, probe, proof)
    permit._driver = driver
    return permit, probe


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
                and tuple(r["name"] for r in result["steps"] if r["status"] == "PASS") == permit.required_steps,
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


def server_issued_time(value):
    """Validate Go's RFC3339Nano shape on Python 3.10; return the original text."""
    require(isinstance(value, str) and bool(value), "SERVER_ISSUED_TIME_MISSING")
    match = re.fullmatch(
        r"([0-9]{4})-([0-9]{2})-([0-9]{2})T([0-9]{2}):([0-9]{2}):([0-9]{2})"
        r"(?:\.[0-9]{1,9})?(?:Z|[+-](?:[01][0-9]|2[0-3]):[0-5][0-9])", value)
    require(match is not None, "SERVER_ISSUED_TIME_INVALID")
    try:
        # Check the calendar only. datetime's microsecond precision must not
        # truncate or round the server's nanoseconds in the upload payload.
        dt.datetime(*(int(part) for part in match.groups()))
    except ValueError:
        raise smoke.SmokeFailure("SERVER_ISSUED_TIME_INVALID") from None
    return value


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

import copy
import importlib.util
import json
from pathlib import Path
import socket
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import sys
sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import preflight as p


CF = {"success": True, "result": {"ipv4_cidrs": ["173.245.48.0/20", "103.21.244.0/22"], "ipv6_cidrs": ["2400:cb00::/32"]}}
REALIP = "\n".join("set_real_ip_from " + x + ";" for x in CF["result"]["ipv4_cidrs"] + CF["result"]["ipv6_cidrs"]) + "\nreal_ip_header CF-Connecting-IP;\nreal_ip_recursive on;\n"
HEADER = "Filesystem 1024-blocks Used Available Capacity Mounted on\n"
DF = HEADER + "/dev/vda 1000 500 500 50% /\n/dev/vdb 1000 790 210 79% /docker-data\n"


class Driver:
    docker = ["docker", "--context", "qualification-local"]
    env = {}

    def __init__(self):
        self.calls = []
        self.replies = {
            "host-os": b"Linux\n", "docker-root": b'"/docker-data"\n', "host-disks": DF.encode(),
            "host-ntp": b"yes\n", "host-listeners": b"", "cloudflare-official": json.dumps(CF).encode(),
            "cloudflare-realip": REALIP.encode(), "network-list": b"aaa111\nbbb222\nccc333\n",
            "network-inspect": b'\n'.join(json.dumps(x).encode() for x in [
                {"name": "bridge", "internal": False, "ipam": [{"Subnet": "172.17.0.0/16"}]},
                {"name": "host", "internal": False, "ipam": []},
                {"name": "none", "internal": False, "ipam": []}]),
            "source-sub2api-version": b"Sub2API 0.1.179 (fixture)\n",
            "local-container-storage": HEADER.encode() + b"/dev/vdc 1000 500 500 50% /probe\n",
            "local-storage-identity": json.dumps({"running": True, "project": "qual-fixture", "mounts": [{"type": "volume", "name": "qual-probe", "destination": "/probe"}]}).encode(),
        }
        for role, name in [("sub2api", "fixture-sub"), ("sub2api_db", "fixture-sub-db"), ("newapi", "fixture-new"), ("newapi_db", "fixture-new-db")]:
            self.replies["source-" + role] = json.dumps({"name": "/" + name, "running": True, "image": "fixture/new:v1.0.0-rc.25" if role == "newapi" else "fixture/test:v1"}).encode()
        self.compose_doc = {"services": {"api": {"environment": {"AUTH_MODE": "session", "TEST_PASSWORD": "DO-NOT-LEAK-test-only"}}, "worker": {"environment": {"RUN_MODE": "production"}}}}

    def command(self, name, argv, check=True, input_bytes=None):
        self.calls.append((name, argv))
        value = self.replies[name]
        if name == "network-list" and value == b"aaa111\nbbb222\nccc333\n":
            value = "\n".join(f"{i + 1:012x}" for i in range(len(self.replies["network-inspect"].splitlines()))).encode()
        if isinstance(value, BaseException):
            raise value
        if isinstance(value, tuple):
            return subprocess.CompletedProcess(argv, value[0], stdout=value[1], stderr=b"DO-NOT-LEAK-error")
        return subprocess.CompletedProcess(argv, 0, stdout=value, stderr=b"")

    def compose(self, project, name, args):
        self.calls.append((name, args))
        return subprocess.CompletedProcess(args, 0, stdout=json.dumps(self.compose_doc).encode(), stderr=b"")


class PreflightTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        (self.root / "cf.json").write_text(json.dumps(CF), encoding="utf-8")
        (self.root / "realip.conf").write_text(REALIP, encoding="utf-8")
        self.cfg = {"mode": "server-rehearsal", "candidate": {"projects": [{"name": "qual-unified"}]}, "host_preflight": {
            "sources": {"sub2api": "fixture-sub", "sub2api_db": "fixture-sub-db", "newapi": "fixture-new", "newapi_db": "fixture-new-db"},
            "expected_sub2_version": "0.1.179", "expected_newapi_tag": "v1.0.0-rc.25",
            "required_loopback_ports": [58088, 58180],
            "planned_networks": [{"name": "qual-proxy", "cidr": "172.30.250.0/28", "internal": False}, {"name": "qual-ingest", "cidr": "172.30.251.0/27", "internal": True}],
            "proxy": {"network_name": "qual-proxy", "gateway_ip": "172.30.250.1", "trusted_cidr": "172.30.250.1/32"},
            "ingest": {"network_name": "qual-ingest", "dynamic_range": "172.30.251.0/28", "proxy_ip": "172.30.251.30", "proxy_cidr": "172.30.251.30/32"},
            "cloudflare_config_file": "/public/0.cloudflare.conf",
            "required_env_keys": {"qual-unified": {"api": ["AUTH_MODE", "TEST_PASSWORD"], "worker": ["RUN_MODE"]}}
        }}
        self.driver = Driver()

    def reject(self, config=None, contains=None):
        result = p.run(self.driver, config or self.cfg)
        self.assertEqual(result["status"], "FAIL", result)
        self.assertNotEqual(result["exit_code"], 0)
        if contains:
            self.assertIn(contains, result["reason"])
        self.assertNotIn("DO-NOT-LEAK", json.dumps(result))
        return result

    def test_server_gathers_fixed_commands_and_all_guards(self):
        result = p.run(self.driver, self.cfg)
        self.assertEqual(result["status"], "PASS", result)
        self.assertEqual(result["exit_code"], 0)
        self.assertEqual(result["qualification_scope"], "server-host-preflight")
        self.assertEqual(result["inherited_server_checks_requires_server"], [])
        calls = dict(self.driver.calls)
        self.assertIn("/docker-data", calls["host-disks"])
        self.assertIn("https://api.cloudflare.com/client/v4/ips", calls["cloudflare-official"])
        self.assertNotIn(".Config.Env", str(calls))
        self.assertNotIn("DO-NOT-LEAK", json.dumps(result))
        self.assertEqual(result["checks"][-1]["name"], "compose-environment")

    def test_source_four_running_and_exact_names_required(self):
        for role in self.cfg["host_preflight"]["sources"]:
            with self.subTest(role=role):
                original = self.driver.replies["source-" + role]
                item = json.loads(original); item["running"] = False
                self.driver.replies["source-" + role] = json.dumps(item).encode()
                self.reject(contains="source")
                self.driver.replies["source-" + role] = original
        del self.cfg["host_preflight"]["sources"]["newapi_db"]
        self.reject(contains="four")

    def test_sub_binary_version_is_exact_and_unambiguous(self):
        for text in [b"Sub2API 0.1.1790", b"Sub2API 0.1.178", b"Sub2API 0.1.179\nSub2API 0.1.178", b"", b"sub2api 0.1.179"]:
            with self.subTest(text=text):
                self.driver.replies["source-sub2api-version"] = text
                self.reject(contains="SUB")

    def test_new_image_tag_exact_not_status_substring_or_digest_only(self):
        for image in ["r/x:v1.0.0-rc.250", "r/v1.0.0-rc.25:latest", "r/x:V1.0.0-rc.25", "r/x@sha256:" + "a" * 64, "r/x:v1.0.0-rc.25@sha256:abc"]:
            with self.subTest(image=image):
                row = json.loads(self.driver.replies["source-newapi"]); row["image"] = image
                self.driver.replies["source-newapi"] = json.dumps(row).encode()
                self.reject(contains="NEW")
        row["image"] = "registry:5000/x:v1.0.0-rc.25@sha256:" + "a" * 64
        self.driver.replies["source-newapi"] = json.dumps(row).encode()
        self.assertEqual(p.run(self.driver, self.cfg)["status"], "PASS")

    def test_disk_requires_two_real_rows_and_below_eighty(self):
        for data in [b"", HEADER.encode(), (HEADER + "/dev/vda 100 50 50 50% /\n").encode(), DF.replace("79%", "80%").encode(), DF.replace("790", "bad").encode(), DF.replace("79%", "179%").encode(), DF.replace("/docker-data", "relative").encode(), DF.replace("/docker-data", "/unrelated").encode()]:
            with self.subTest(data=data):
                self.driver.replies["host-disks"] = data
                self.reject(contains="disk")

    def test_ntp_missing_no_or_command_failure_rejected(self):
        for value in [b"", b"no\n", b"yes\nno\n", (1, b"yes\n")]:
            self.driver.replies["host-ntp"] = value
            self.reject()

    def test_ports_occupied_and_malformed_rows_rejected(self):
        for value in [b"LISTEN 0 128 127.0.0.1:58088 0.0.0.0:*\n", b"LISTEN 0 128 [::]:58180 [::]:*\n", b"bad row\n"]:
            self.driver.replies["host-listeners"] = value
            self.reject(contains="port")
        self.driver.replies["host-listeners"] = b"LISTEN 0 128 127.0.0.1:58089 0.0.0.0:*\n"
        self.assertEqual(p.run(self.driver, self.cfg)["status"], "PASS")

    def test_verified_old_port_is_deferred_only_until_stop_old(self):
        self.driver.replies["host-listeners"] = b"LISTEN 0 128 127.0.0.1:58088 0.0.0.0:*\n"
        self.reject(contains="port")
        result = p.run(self.driver, self.cfg, allowed_occupied_ports={58088})
        self.assertEqual(result["status"], "PASS", result)
        self.assertEqual(result["evidence"]["ports"]["reviewed_occupied_ports"], [58088])
        with self.assertRaises(p.PreflightError):
            p.check_ports(self.driver, self.cfg)
        self.driver.replies["host-listeners"] += b"LISTEN 0 128 127.0.0.1:58180 0.0.0.0:*\n"
        self.assertEqual(p.run(self.driver, self.cfg, allowed_occupied_ports={58088})["status"], "FAIL")
        self.driver.replies["host-listeners"] = b""
        self.assertEqual(p.check_ports(self.driver, self.cfg)["reviewed_occupied_ports"], [])

    def test_local_reviewed_ports_are_engine_set_only_and_release_rechecked(self):
        self.local()
        sock = socket.socket(); sock.bind(("127.0.0.1", 0)); sock.listen(); self.addCleanup(sock.close)
        port = sock.getsockname()[1]
        self.cfg["host_preflight"]["required_loopback_ports"] = [port]
        self.reject(contains="port")
        self.assertEqual(p.run(self.driver, self.cfg, allowed_occupied_ports={port})["status"], "PASS")
        with self.assertRaises(p.PreflightError):
            p.check_ports(self.driver, self.cfg)
        for value in [[port], {True}, {port + 1}]:
            self.assertEqual(p.run(self.driver, self.cfg, allowed_occupied_ports=value)["status"], "FAIL")
        self.cfg["host_preflight"]["allowed_occupied_ports"] = [port]
        self.reject()

    def test_cf_requires_exact_complete_family_sets_and_three_realip_rules(self):
        originals = self.driver.replies.copy()
        variants = [REALIP.replace("set_real_ip_from 103.21.244.0/22;", ""), REALIP + "set_real_ip_from 10.0.0.0/8;\n", REALIP.replace("CF-Connecting-IP", "X-Forwarded-For"), REALIP.replace("recursive on", "recursive off"), REALIP + "real_ip_header X-Real-IP;", REALIP.replace("2400:cb00::/32", "2400:cb00::/129")]
        for value in variants:
            self.driver.replies = originals.copy()
            self.driver.replies["cloudflare-realip"] = value.encode()
            self.reject(contains="Cloudflare")
        for value in [{"success": False, "result": CF["result"]}, {"success": True, "result": {"ipv4_cidrs": CF["result"]["ipv4_cidrs"], "ipv6_cidrs": []}}]:
            self.driver.replies = originals.copy()
            self.driver.replies["cloudflare-official"] = json.dumps(value).encode()
            self.reject(contains="Cloudflare")

    def test_network_inventory_and_overlap_fail_closed(self):
        original = self.driver.replies["network-inspect"]
        for row in [{"name": "other", "internal": True, "ipam": [{"Subnet": "172.30.251.0/27"}]}, {"name": "qual-ingest", "internal": False, "ipam": [{"Subnet": "172.30.251.0/27"}]}, {"name": "qual-ingest", "internal": True, "ipam": [{"Subnet": "10.99.0.0/24"}]}, {"name": "qual-ingest", "internal": True, "ipam": [{"Subnet": "172.30.251.0/27"}, {"Subnet": "10.0.0.0/24"}]}, {"name": "broken", "internal": True, "ipam": [{"Subnet": "fd00::/129"}]}]:
            self.driver.replies["network-inspect"] = original + b"\n" + json.dumps(row).encode()
            self.reject(contains="network")
        self.driver.replies["network-inspect"] = b""
        self.reject(contains="network")

    def test_network_exact_reuse_and_ipv6_are_accepted(self):
        self.driver.replies["network-inspect"] += b"\n" + json.dumps({"name": "qual-ingest", "internal": True, "ipam": [{"Subnet": "172.30.251.0/27"}]}).encode()
        self.driver.replies["network-inspect"] += b"\n" + json.dumps({"name": "ipv6", "internal": False, "ipam": [{"Subnet": "fd00::/64"}]}).encode()
        self.assertEqual(p.run(self.driver, self.cfg)["status"], "PASS")

    def test_network_inspect_must_account_for_each_listed_id(self):
        self.driver.replies["network-list"] = b"000001\n000002\n000003\n000004\n"
        self.reject(contains="network")

    def test_builtin_null_ipam_accepted_but_user_network_empty_rejected(self):
        original = self.driver.replies["network-inspect"]
        self.driver.replies["network-inspect"] = original.replace(b'"ipam": []', b'"ipam": null')
        self.assertEqual(p.run(self.driver, self.cfg)["status"], "PASS")
        self.driver.replies["network-inspect"] = original + b'\n{"name":"unknown-user-network","internal":false,"ipam":null}'
        self.reject(contains="network")

    def test_ingest_capacity_proxy32_and_planned_overlap(self):
        original = copy.deepcopy(self.cfg)
        for key, value in [("dynamic_range", "172.30.251.0/29"), ("dynamic_range", "172.30.252.0/28"), ("proxy_ip", "172.30.251.14"), ("proxy_cidr", "172.30.251.30/27")]:
            self.cfg = copy.deepcopy(original); self.cfg["host_preflight"]["ingest"][key] = value
            self.reject()
        self.cfg = copy.deepcopy(original)
        self.cfg["host_preflight"]["ingest"].update(proxy_ip="172.30.251.14", proxy_cidr="172.30.251.14/32")
        self.reject(contains="outside")
        self.cfg = copy.deepcopy(original)
        self.cfg["host_preflight"]["proxy"]["trusted_cidr"] = "172.30.250.1/28"
        self.reject()
        self.cfg = copy.deepcopy(original)
        self.cfg["host_preflight"]["planned_networks"].append({"name": "collision", "cidr": "172.30.251.16/28", "internal": True})
        self.reject(contains="network")

    def test_environment_missing_null_blank_and_unexpanded_rejected_without_leak(self):
        for value in [None, "", "  ", "${MISSING}"]:
            self.driver.compose_doc["services"]["api"]["environment"]["AUTH_MODE"] = value
            self.reject(contains="environment")
        del self.driver.compose_doc["services"]["api"]["environment"]["AUTH_MODE"]
        self.reject(contains="environment")

    def test_environment_renders_profiled_jobs_without_starting_them(self):
        self.cfg['host_preflight']['required_env_keys']['qual-unified']['maintenance']=['RUN_MODE']
        job={'profiles':['operations'],'environment':{'RUN_MODE':'maintenance'}}
        calls=[]
        def compose(project,name,args):
            calls.append(args);doc=copy.deepcopy(self.driver.compose_doc)
            if args[:2]==['--profile','*']:doc['services']['maintenance']=job
            return subprocess.CompletedProcess(args,0,stdout=json.dumps(doc).encode())
        self.driver.compose=compose
        self.assertEqual(p.run(self.driver,self.cfg)['status'],'PASS')
        self.assertEqual(calls,[['--profile','*','config','--format','json']])
        job['environment']['RUN_MODE']=''
        self.reject(contains='environment')

    def test_optional_empty_environment_does_not_enable_disabled_features(self):
        env = self.driver.compose_doc["services"]["api"]["environment"]
        env.update(XM_CARDS_BASE_URL="", XM_CARDS_ACCOUNTS=None,
                   XM_STAFF_BOOTSTRAP_USERNAME="", XM_STAFF_BOOTSTRAP_PASSWORD="",
                   XM_FINANCE_RUNWAY_WARN_DAYS="")
        self.assertEqual(p.run(self.driver, self.cfg)["status"], "PASS")
        self.cfg["host_preflight"]["required_env_keys"]["qual-unified"]["api"].append("XM_STAFF_BOOTSTRAP_PASSWORD")
        self.reject(contains="environment")
        self.cfg["host_preflight"]["required_env_keys"]["qual-unified"]["api"].remove("XM_STAFF_BOOTSTRAP_PASSWORD")
        env["XM_CARDS_BASE_URL"] = "${UNRESOLVED}"
        self.reject(contains="environment")
        del self.cfg["host_preflight"]["required_env_keys"]["qual-unified"]["worker"]
        self.reject(contains="environment")

    def local(self):
        self.cfg["mode"] = "local-synthetic"
        self.cfg["host_preflight"]["cloudflare_config_file"] = str(self.root / "realip.conf")
        self.cfg["host_preflight"]["local"] = {"task_directory": str(self.root), "cloudflare_document": str(self.root / "cf.json"), "storage_probe": {"container": "fixture-probe", "project": "qual-fixture", "volume": "qual-probe", "mount": "/probe"}}
        sock = socket.socket(); sock.bind(("127.0.0.1", 0)); port = sock.getsockname()[1]; sock.close()
        self.cfg["host_preflight"]["required_loopback_ports"] = [port]

    def test_local_scope_uses_real_loopback_without_faking_host_ntp_or_cf(self):
        self.local()
        with patch.object(p.shutil, "disk_usage", return_value=(1000, 500, 500)):
            result = p.run(self.driver, self.cfg)
        self.assertEqual(result["status"], "PASS", result)
        self.assertEqual(result["qualification_scope"], "local-synthetic-host-preflight")
        self.assertIn("host-ntp", result["inherited_server_checks_requires_server"])
        self.assertNotIn("host-ntp", dict(self.driver.calls))
        self.assertNotIn("host-disks", dict(self.driver.calls))
        self.assertNotIn("cloudflare-official", dict(self.driver.calls))
        self.assertEqual(result["evidence"]["sources"]["scope"], "synthetic-contract")
        self.assertEqual(result["evidence"]["storage"]["scope"], "container-storage")

    def test_local_actual_occupied_port_and_task_disk_guard(self):
        self.local()
        sock = socket.socket(); sock.bind(("127.0.0.1", 0)); sock.listen(); self.addCleanup(sock.close)
        self.cfg["host_preflight"]["required_loopback_ports"] = [sock.getsockname()[1]]
        self.reject(contains="port")
        self.local()
        with patch.object(p.shutil, "disk_usage", return_value=(1000, 800, 200)):
            self.reject(contains="disk")

    def test_local_container_storage_identity_and_full_volume_rejected(self):
        self.local()
        row = json.loads(self.driver.replies["local-storage-identity"])
        for key, value in [("running", False), ("project", "unrelated"), ("mounts", [{"type": "bind", "name": "qual-probe", "destination": "/probe"}])]:
            bad = copy.deepcopy(row); bad[key] = value
            self.driver.replies["local-storage-identity"] = json.dumps(bad).encode()
            self.reject(contains="storage")
        self.driver.replies["local-storage-identity"] = json.dumps(row).encode()
        self.driver.replies["local-container-storage"] = self.driver.replies["local-container-storage"].replace(b"50%", b"80%")
        self.reject(contains="disk")

    def test_mode_unknown_and_local_evidence_cannot_pass_server(self):
        self.cfg["mode"] = "pretend-production"
        self.reject()
        self.local(); self.cfg["mode"] = "production"
        self.reject(contains="local")

    def test_command_failure_and_malformed_public_json_never_become_pass(self):
        self.driver.replies["source-newapi"] = (1, b'{}')
        self.reject()
        self.driver.replies["source-newapi"] = b'{"running": true, "running": false}'
        self.reject()

    def test_interrupt_propagates_and_never_returns_success(self):
        self.driver.replies["host-ntp"] = KeyboardInterrupt()
        with self.assertRaises(KeyboardInterrupt):
            p.run(self.driver, self.cfg)


if __name__ == "__main__":
    unittest.main()

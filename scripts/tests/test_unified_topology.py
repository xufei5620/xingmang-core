"""Local deployment contracts; no service is started and no credential is read."""
import json
import os
import re
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]


class UnifiedTopologyTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.doc = json.loads((ROOT / "deploy/unified/compose.json").read_text("utf-8"))
        cls.services = cls.doc["services"]

    def test_database_images_remain_the_original_official_pins(self):
        pins = {
            "platform-postgres": "docker.io/library/postgres:18@sha256:7341002d2b8c7c5bdd7542a671a95b36196c0b5b888daf454ae4fc33ba5346d7",
            "invoice-postgres": "postgres:18.6-alpine@sha256:d3e1620b530c944afa6e887d22eb899824da68e19c52024bf98f5220c88a65b2",
        }
        definitions = {item["name"]: item for item in json.loads((ROOT / "deploy/unified/images.json").read_text("utf-8"))}
        for role, pin in pins.items():
            with self.subTest(role=role):
                self.assertEqual(definitions[role], {"name": role, "kind": "pinned", "reference": pin})
                service = "postgres" if role == "platform-postgres" else role
                self.assertEqual(self.services[service]["image"], pin)
        self.assertEqual(self.services["invoice-permissions"]["image"], pins["invoice-postgres"])

    def test_unrelated_card_notification_settings_are_not_imported(self):
        for name in ("platform-api", "platform-worker"):
            for key in ("XM_CARDS_NOTIFY_ENABLED", "XM_CARDS_NOTIFY_RECOVER_INTERVAL"):
                self.assertNotIn(key, self.services[name]["environment"], "frozen card feature must not enter CR-0010")

    def test_exactly_one_http_api_and_no_retired_auth(self):
        listeners = [name for name, svc in self.services.items()
                     if "LISTEN_ADDR" in svc.get("environment", {}) or "HTTP_ADDR" in svc.get("environment", {})]
        self.assertEqual(listeners, ["platform-api"])
        api = self.services["platform-api"]
        self.assertEqual(api["environment"]["LISTEN_ADDR"], "0.0.0.0:8080")
        self.assertEqual(api["environment"]["ENVIRONMENT"], "production")
        self.assertEqual(api["environment"]["APP_ENV"], "production")
        self.assertEqual(api["environment"]["XM_AUTH_MODE"], "local")
        self.assertEqual(api["environment"]["AUTH_MODE"], "session")
        self.assertNotIn("HTTP_ADDR", api["environment"])
        self.assertNotIn("INVOICE_ENABLED", api["environment"])
        self.assertIn("INVOICE_STAFF_ORIGIN", api["environment"])
        self.assertIn("ADMIN_ROLE", api["environment"])
        for name, svc in self.services.items():
            self.assertNotRegex(name, r"(?i)keycloak|oidc")
            for key in svc.get("environment", {}):
                self.assertNotRegex(key, r"OIDC|CONSOLE_ASSERTION")

    def test_database_roles_and_migrations_remain_separate(self):
        api = self.services["platform-api"]
        self.assertIn("@postgres:", api["environment"]["DATABASE_URL"])
        self.assertEqual(api["environment"]["DATABASE_URL_FILE"], "/run/secrets/invoice_app_database_url")
        self.assertNotIn("invoice_owner_database_url", api["secrets"])
        for name in ("migrate", "invoice-migrate", "invoice-permissions"):
            self.assertEqual(api["depends_on"][name]["condition"], "service_completed_successfully")
        self.assertEqual(self.services["invoice-permissions"]["depends_on"]["invoice-migrate"]["condition"], "service_completed_successfully")
        self.assertIn("invoice_owner_database_url", self.services["invoice-migrate"]["secrets"])
        for volume in ("postgres18-data", "invoice_postgres_data", "invoice_document_data", "xm-secrets"):
            self.assertTrue(self.doc["volumes"][volume]["external"], "must not silently create an empty production volume")

    def test_scanner_and_ingress_keep_isolation_without_readiness_deadlock(self):
        api = self.services["platform-api"]
        self.assertIn("10000", api["group_add"])
        self.assertIn("invoice_pdf_scanner_socket:/scanner:ro", api["volumes"])
        self.assertEqual(self.services["pdf-scanner"]["network_mode"], "none")
        self.assertEqual(api["depends_on"]["pdf-scanner"]["condition"], "service_healthy")
        self.assertEqual(self.services["ingest-proxy"]["depends_on"]["platform-api"]["condition"], "service_started")
        self.assertEqual(self.services["web"]["depends_on"]["platform-api"]["condition"], "service_started")
        self.assertIn("invoice_ingest", api["networks"])
        ingress = (ROOT / "invoice/deploy/nginx/ingest-mtls.conf").read_text("utf-8")
        self.assertIn("resolver 127.0.0.11 valid=10s", ingress)
        self.assertIn("set $ingest_upstream platform-api;", ingress)
        self.assertIn("proxy_pass http://$ingest_upstream:8080;", ingress)
        self.assertIn("ssl_verify_client on;", ingress)
        self.assertIn("location = /internal/v1/source-batches", ingress)
        self.assertIn("location / { return 404; }", ingress)

    def test_same_process_routes_and_distinct_static_roots(self):
        nginx = (ROOT / "deploy/unified/nginx.conf").read_text("utf-8")
        self.assertIn("listen 80;", nginx)
        self.assertIn("listen 8081;", nginx)
        self.assertIn("root /usr/share/nginx/html;", nginx)
        self.assertIn("root /usr/share/nginx/invoice;", nginx)
        self.assertEqual(nginx.count("location /invoice-api/v1/"), 2)
        self.assertEqual(nginx.count("location /internal/ { return 404; }"), 2)
        self.assertNotIn("frame-ancestors https:", nginx)
        self.assertNotIn(":8088", nginx)
        self.assertIn("client_max_body_size 24m", nginx)
        self.assertIn("proxy_next_upstream off", nginx)
        self.assertIn("include /etc/nginx/runtime/trusted-proxy.conf;", nginx)
        self.assertGreaterEqual(nginx.count("proxy_set_header CF-Connecting-IP $remote_addr;"), 2)

    def test_all_image_contexts_are_monorepo_root(self):
        definitions = json.loads((ROOT / "deploy/unified/images.json").read_text("utf-8"))
        for item in definitions:
            if item["kind"] == "built":
                self.assertEqual(item["context"], ".")
                self.assertTrue((ROOT / item["dockerfile"]).is_file())
        self.assertEqual(sum(item["name"] == "platform-api" for item in definitions), 1)
        self.assertFalse(any(item["name"] in ("api", "keycloak", "invoice-api") for item in definitions))
        dockerfile = (ROOT / "platform/deploy/docker/go.Dockerfile").read_text("utf-8")
        self.assertIn("golang:1.27", dockerfile)
        self.assertIn("COPY invoice/backend/", dockerfile)
        self.assertIn("COPY platform/", dockerfile)
        self.assertNotIn("./cmd/api", dockerfile)

    def test_invoice_docker_go_targets_and_copied_tools_exist(self):
        dockerfile = (ROOT / "invoice/backend/Dockerfile").read_text("utf-8")
        targets = re.findall(r"\./cmd/[a-zA-Z0-9_-]+", dockerfile)
        self.assertTrue(targets, "Dockerfile has no Go command build targets")
        for target in targets:
            directory = ROOT / "invoice/backend" / target
            sources = [path for path in directory.glob("*.go") if not path.name.endswith("_test.go")]
            self.assertTrue(sources, "Docker build target has no production Go source: " + target)
        built = set(re.findall(r"-o\s+(/out/[a-zA-Z0-9_-]+)", dockerfile))
        for line in dockerfile.splitlines():
            if line.startswith("COPY --from=build "):
                for output in re.findall(r"/out/[a-zA-Z0-9_-]+", line):
                    self.assertIn(output, built, "COPY references an unbuilt Go binary")

    def test_every_compose_service_uses_an_inventory_image(self):
        definitions = json.loads((ROOT / "deploy/unified/images.json").read_text("utf-8"))
        allowed = {item["repository"] for item in definitions if item["kind"] == "built"}
        pinned = {item["reference"] for item in definitions if item["kind"] == "pinned"}
        sources = json.loads((ROOT / "deploy/unified/sources.json").read_text("utf-8"))
        for name, svc in {**self.services, **sources["services"]}.items():
            reference = svc["image"]
            self.assertTrue(reference in pinned or reference.split(":${", 1)[0] in allowed, name + " has no build inventory image")

    def test_source_identity_streams_remain_separate(self):
        sources = json.loads((ROOT / "deploy/unified/sources.json").read_text("utf-8"))
        for source in ("sub2api", "newapi"):
            for stream in ("payments", "identities", "usage", "credits", "balances"):
                service = sources["services"][source + "-" + stream]
                self.assertEqual(service["environment"]["SOURCE_TYPE"], source)
                self.assertEqual(service["environment"]["SOURCE_STATE_STREAM"], stream)
            # Historical identity attestation configuration is not an active IdP login.
            self.assertIn("SOURCE_TRUSTED_OIDC_ISSUER", sources["services"][source + "-identities"]["environment"])

    def test_actual_compose_parser_rejects_legacy_but_accepts_canonical(self):
        docker = shutil.which("docker")
        self.assertIsNotNone(docker)
        with tempfile.TemporaryDirectory() as temp:
            empty = Path(temp) / "empty.env"
            empty.write_text("", encoding="utf-8")
            for path, expected in (("deploy/unified/compose.json", 0), ("deploy/unified/sources.json", 0),
                                   ("invoice/deploy/docker-compose.prod.yml", 1),
                                   ("platform/deploy/compose/launch.yaml", 1)):
                result = subprocess.run([docker, "compose", "--env-file", str(empty), "-f", str(ROOT / path),
                    "config", "--no-interpolate", "--no-env-resolution", "--quiet"], capture_output=True, text=True)
                self.assertEqual(result.returncode, expected, result.stderr)

    def test_compose_interpolates_with_only_synthetic_configuration(self):
        text = (ROOT / "deploy/unified/compose.json").read_text("utf-8") + (ROOT / "deploy/unified/sources.json").read_text("utf-8")
        values = {key: "synthetic" for key in re.findall(r"\$\{([A-Z0-9_]+):\?", text)}
        values.update(INVOICE_PROXY_SUBNET="172.30.201.0/24", INVOICE_PROXY_GATEWAY_IP="172.30.201.1",
            UNIFIED_WEB_PROXY_IP="172.30.201.2", UNIFIED_WEB_PROXY_CIDR="172.30.201.2/32",
            INVOICE_DB_SUBNET="172.30.202.0/24", INVOICE_APP_SUBNET="172.30.203.0/24",
            CLAMAV_EGRESS_SUBNET="172.30.204.0/24", INVOICE_INGEST_SUBNET="172.30.205.0/24",
            INVOICE_INGEST_DYNAMIC_RANGE="172.30.205.128/25", INVOICE_INGEST_PROXY_IP="172.30.205.2",
            INVOICE_INGEST_PROXY_CIDR="172.30.205.2/32", PLATFORM_EGRESS_SUBNET="172.30.206.0/24",
            INVOICE_STAFF_ORIGIN="https://staff.example.invalid", PUBLIC_ORIGIN="https://invoice.example.invalid",
            ELIGIBILITY_START_AT="2026-09-01T00:00:00Z", SECRETS_DIR="/synthetic/not-mounted/secrets",
            SOURCE_TRUST_CONFIG_FILE="/synthetic/not-mounted/trust.json", SOURCE_STATE_ROOT="/synthetic/state",
            SOURCE_CUTOVER_ROOT="/synthetic/cutover", SOURCE_INSTANCES_CONFIG_FILE="/synthetic/instances.json",
            ADMIN_SETTINGS_BOOTSTRAP_FILE="/synthetic/settings.json")
        # Keep only process startup settings; do not inherit an operator's real configuration.
        env = {key: os.environ[key] for key in ("PATH", "SystemRoot", "TEMP", "TMP", "USERPROFILE", "HOME", "APPDATA", "LOCALAPPDATA") if key in os.environ}
        compose = [shutil.which("docker-compose")] if shutil.which("docker-compose") else [shutil.which("docker"), "compose"]
        with tempfile.TemporaryDirectory() as temp:
            config = Path(temp) / "synthetic.env"
            config.write_text("\n".join(key + "=" + value for key, value in values.items()) + "\n", encoding="utf-8")
            for name in ("compose.json", "sources.json"):
                result = subprocess.run(compose + ["--env-file", str(config), "-f", str(ROOT / "deploy/unified" / name),
                    "config", "--no-env-resolution", "--no-path-resolution", "--format", "json"], env=env, capture_output=True, text=True)
                self.assertEqual(result.returncode, 0, result.stderr)
                parsed = json.loads(result.stdout)
                if name == "compose.json":
                    self.assertEqual(parsed["services"]["platform-api"]["environment"]["AUTH_MODE"], "session")
                    self.assertEqual(parsed["services"]["platform-api"]["environment"]["TRUSTED_PROXY_CIDRS"], "172.30.201.2/32")
                    self.assertEqual(parsed["services"]["web"]["environment"]["UNIFIED_HOST_PROXY_CIDR"], "172.30.201.1/32")

    def test_retired_production_scripts_exit_before_tools_or_secrets(self):
        bash = Path(shutil.which("git")).parents[1] / "bin/bash.exe" if os.name == "nt" else shutil.which("bash")
        self.assertTrue(Path(bash).is_file())
        for name in ("platform/deploy/scripts/deploy-local.sh", "platform/deploy/scripts/deploy.sh", "invoice/deploy/roll-forward.sh", "invoice/deploy/backup/backup.sh"):
            result = subprocess.run([str(bash), str(ROOT / name), "--help"], capture_output=True, text=True)
            self.assertEqual(result.returncode, 64, result.stderr)
            self.assertIn("Independent deployment is retired", result.stderr)

    def test_runtime_web_config_is_local_only(self):
        bash = Path(shutil.which("git")).parents[1] / "bin/bash.exe" if os.name == "nt" else shutil.which("bash")
        with tempfile.TemporaryDirectory() as temp:
            output = Path(temp) / "config.js"
            for mode, code in (("oidc", 64), ("dev-header", 64), ("local", 0)):
                env = {**os.environ, "XM_WEB_AUTH_MODE": mode, "XM_WEB_APP_CONFIG_PATH": output.as_posix(),
                       "UNIFIED_HOST_PROXY_CIDR": "172.29.0.1/32", "XM_WEB_TRUSTED_PROXY_CONFIG_PATH": (Path(temp) / "trust.conf").as_posix()}
                result = subprocess.run([str(bash), str(ROOT / "platform/deploy/docker/web-app-config.sh")], env=env, capture_output=True, text=True)
                self.assertEqual(result.returncode, code, result.stderr)
            self.assertEqual(output.read_text("utf-8"), 'window.__XM_CONFIG__ = {"authMode":"local"};\n')
            for invalid in ("", "0.0.0.0/0", "172.29.0.0/24", "172.29.0.1/32; injected", "256.1.1.1/32"):
                env["UNIFIED_HOST_PROXY_CIDR"] = invalid
                result = subprocess.run([str(bash), str(ROOT / "platform/deploy/docker/web-app-config.sh")], env=env, capture_output=True, text=True)
                self.assertNotEqual(result.returncode, 0, invalid)

    def test_old_release_entry_points_cannot_approve_either_topology(self):
        pwsh = shutil.which("pwsh")
        self.assertIsNotNone(pwsh)
        # verify.ps1 remains the required full local source gate under 06 B2.
        # It must be run by run-detached, not recursively from this unit suite.
        for name in ("release-image-gate.ps1", "verify-release-image-artifacts.ps1", "preflight-production.ps1"):
            result = subprocess.run([pwsh, "-NoProfile", "-File", str(ROOT / "invoice/scripts" / name)], capture_output=True, text=True)
            self.assertEqual(result.returncode, 64, result.stderr)
            self.assertIn("Independent invoice release gate is retired", result.stderr)


if __name__ == "__main__":
    unittest.main()

#!/usr/bin/env python3
"""Synthetic, loopback-only nginx route/header probe; never mounts application secrets."""
import argparse
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile
import uuid

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("unified_gate", ROOT / "scripts/unified-service.py")
gate = importlib.util.module_from_spec(spec)
spec.loader.exec_module(gate)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--image", required=True, help="already-local nginx image ID; no pulling")
    parser.add_argument("--docker-context", default="desktop-linux" if os.name == "nt" else "default")
    args = parser.parse_args()
    docker = gate.docker_command(args.docker_context)
    image_id = gate.run(docker + ["image", "inspect", "--format", "{{.Id}}", args.image]).decode().strip()
    name = "xm-unified-nginx-probe-" + uuid.uuid4().hex[:12]
    network_created = False
    container_created = False
    started = gate.utc()
    with tempfile.TemporaryDirectory(prefix="xm-unified-nginx-") as temp:
        folder = Path(temp)
        # Reserve no persistent names/volumes. The only upstream is an echo server
        # in this same disposable container and completely internal test network.
        try:
            gate.run(docker + ["network", "create", "--internal", "--label", "xingmang.local-probe=" + name, name])
            network_created = True
            # This checks header rules in container loopback, not host/CDN mapping.
            (folder / "trust.conf").write_text("set_real_ip_from 127.0.0.1/32;\nreal_ip_header X-Forwarded-For;\nreal_ip_recursive on;\n", encoding="utf-8")
            (folder / "stub.conf").write_text('server { listen 8080; location / { default_type application/json; return 200 \'{"uri":"$request_uri","client":"$http_cf_connecting_ip","mock":"$http_x_mock_role","mtls":"$http_x_invoice_mtls_verified"}\'; } }\n', encoding="utf-8")
            mounts = []
            for source, target in ((ROOT / "deploy/unified/nginx.conf", "/etc/nginx/conf.d/default.conf"),
                                   (folder / "trust.conf", "/etc/nginx/runtime/trusted-proxy.conf"),
                                   (folder / "stub.conf", "/etc/nginx/conf.d/zz-synthetic-upstream.conf")):
                mounts += ["--mount", "type=bind,src=" + str(source.resolve()) + ",dst=" + target + ",readonly"]
            command = docker + ["run", "--detach", "--pull", "never", "--name", name, "--label", "xingmang.local-probe=" + name,
                "--network", name, "--network-alias", "platform-api",
                "--read-only", "--tmpfs", "/var/cache/nginx", "--tmpfs", "/var/run", *mounts,
                "--entrypoint", "nginx", image_id, "-g", "daemon off;"]
            gate.run(command)
            container_created = True
            gate.run(docker + ["exec", name, "nginx", "-t"])
            probe = docker + ["exec", name, "wget", "-q", "-O", "-", "-T", "5"]
            body = json.loads(gate.run(probe + ["--header=X-Forwarded-For: 198.51.100.77", "--header=CF-Connecting-IP: 203.0.113.99",
                "--header=X-Mock-Role: admin", "--header=X-Invoice-mTLS-Verified: SUCCESS", "http://127.0.0.1/invoice-api/v1/synthetic?source=sub2api"]).decode())
            assert body["uri"] == "/invoice-api/v1/synthetic?source=sub2api", body
            assert body["client"] == "198.51.100.77", body
            assert body["mock"] == "" and body["mtls"] == "", body
            for path in ("/internal/v1/source-batches", "/metrics"):
                result = subprocess.run(probe + ["http://127.0.0.1" + path], capture_output=True)
                assert result.returncode != 0 and b"404" in result.stderr, "public route was exposed: " + path
            print(json.dumps({"startUtc": started, "endUtc": gate.utc(), "exitCode": 0, "nginxImageId": image_id,
                "routesPreserved": True, "spoofedClientHeaderOverwritten": True, "mockAndMTLSHeadersStripped": True,
                "publicInternalAndMetricsBlocked": True, "scope": "synthetic container-loopback probe; host/CDN unverified"}, indent=2))
        finally:
            if container_created:
                subprocess.run(docker + ["rm", "--force", name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            if network_created:
                subprocess.run(docker + ["network", "rm", name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)


if __name__ == "__main__":
    main()

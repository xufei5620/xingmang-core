import importlib.util
from pathlib import Path
import unittest

ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location("build_proxy_gate", ROOT / "scripts/unified-service.py")
gate = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(gate)


class BuildProxyTests(unittest.TestCase):
    def arguments(self, **kwargs):
        return gate.build_arguments(
            {"dockerfile": "Dockerfile", "repository": "example/runtime"}, "test",
            {"gitHead": "a" * 40, "inventorySha256": "b" * 64}, "c" * 64,
            builder="qualification-local", **kwargs)

    def test_proxy_is_explicit_build_transport_only(self):
        proxy = "http://http.docker.internal:3128"
        args = self.arguments(build_proxy=proxy)
        for name in ("HTTP_PROXY", "HTTPS_PROXY"):
            self.assertEqual(args.count(name + "=" + proxy), 1)
            self.assertEqual(args[args.index(name + "=" + proxy) - 1], "--build-arg")
        self.assertEqual(args[-1], "-")
        self.assertNotIn("--network", args)
        self.assertNotIn("--add-host", args)

    def test_unconfigured_build_does_not_guess_machine_proxy(self):
        self.assertFalse(any("PROXY" in value for value in self.arguments()))

    def test_reviewed_go_proxy_is_an_explicit_build_argument(self):
        args = self.arguments(go_proxy="https://goproxy.cn,direct")
        self.assertEqual(args.count("GOPROXY=https://goproxy.cn,direct"), 1)
        self.assertEqual(args[args.index("GOPROXY=https://goproxy.cn,direct") - 1], "--build-arg")
        for value in ("direct", "https://proxy.golang.org,direct", "https://user:pass@goproxy.cn,direct"):
            with self.subTest(value=value), self.assertRaises(gate.GateError):
                self.arguments(go_proxy=value)

    def test_go_proxy_is_scoped_to_rehearsal_build_stage(self):
        source = (ROOT / "invoice/backend/Dockerfile").read_text("utf-8")
        before, after = source.split("FROM build AS rehearsal-build\n", 1)
        stage, runtime = after.split("\nFROM ", 1)
        self.assertIn("ARG GOPROXY=https://goproxy.cn,direct\n", stage)
        self.assertNotIn("GOPROXY", before)
        self.assertNotIn("GOPROXY", runtime)
        self.assertNotIn("ENV GOPROXY", stage)
        self.assertNotIn("go env -w", source)

    def test_proxy_rejects_credentials_and_non_endpoint_urls(self):
        for proxy in ("http://user:password@proxy:3128", "http://proxy:3128/path",
                      "http://proxy:3128/?token=x", "http://proxy:3128/#x",
                      "file:///tmp/proxy", "socks5://proxy:1080", "http://",
                      "http://proxy:0", "http://proxy:65536", "http://proxy\n:3128"):
            with self.subTest(proxy=proxy), self.assertRaises(gate.GateError):
                self.arguments(build_proxy=proxy)

    def test_rehearsal_build_resolves_only_tested_package_graph(self):
        source = (ROOT / "invoice/backend/Dockerfile").read_text("utf-8")
        stage = source.split("FROM build AS rehearsal-build\n", 1)[1].split("\nFROM ", 1)[0]
        self.assertNotIn("go mod download", stage)
        self.assertIn("RUN GOWORK=off go test -tags rehearsal_tools -mod=readonly -count=1 invoice-system/backend/cmd/rehearsal-seed", stage)
        self.assertIn("RUN CGO_ENABLED=0 GOOS=linux GOWORK=off go build -tags rehearsal_tools -mod=readonly", stage)
        self.assertNotIn("ARG HTTP_PROXY", source)
        self.assertNotIn("ENV HTTP_PROXY", source)


if __name__ == "__main__":
    unittest.main()

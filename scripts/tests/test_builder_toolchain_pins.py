"""A floating builder must not silently override the repository toolchain lock."""
import configparser
from pathlib import Path
import re
import unittest

ROOT = Path(__file__).resolve().parents[2]


class BuilderToolchainPins(unittest.TestCase):
    def assert_builder(self, path, image, version):
        text = (ROOT / path).read_text(encoding="utf-8")
        pattern = rf"^FROM {image}:{re.escape(version)}-alpine@sha256:[0-9a-f]{{64}} AS \w+$"
        self.assertRegex(text, re.compile(pattern, re.MULTILINE),
                         "builder must match VERSIONS.lock and an immutable registry digest")

    @staticmethod
    def version(name):
        lock = configparser.ConfigParser(inline_comment_prefixes=("#",))
        lock.read(ROOT / "platform/VERSIONS.lock", encoding="utf-8")
        return lock["toolchain"][name].strip()

    def test_platform_go_builder(self):
        self.assert_builder("platform/deploy/docker/go.Dockerfile", "golang", self.version("go"))

    def test_invoice_go_builder(self):
        self.assert_builder("invoice/backend/Dockerfile", "golang", self.version("go"))

    def test_web_node_builder(self):
        self.assert_builder("platform/deploy/docker/web.Dockerfile", "node", self.version("node"))


if __name__ == "__main__":
    unittest.main()

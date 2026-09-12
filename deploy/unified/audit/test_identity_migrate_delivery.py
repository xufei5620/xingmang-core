"""The maintenance artifact must be present without adding a login route."""
from pathlib import Path
import re
import unittest

ROOT = Path(__file__).resolve().parents[3]


class IdentityMigrationDeliveryTests(unittest.TestCase):
    def test_tools_stage_contains_built_maintenance_command(self):
        dockerfile = (ROOT/'invoice/backend/Dockerfile').read_text()
        stages = re.split(r'(?m)^FROM ', dockerfile)
        build = next(s for s in stages if ' AS build\n' in s)
        tools = next(s for s in stages if ' AS tools\n' in s)
        self.assertRegex(build, r'go build[^\n]+-o /out/invoice-identity-migrate ./cmd/identity-migrate')
        copies = [line for line in tools.splitlines() if line.startswith('COPY --from=build ')]
        self.assertTrue(any('/out/invoice-identity-migrate' in line.split() and line.split()[-1]=='/usr/local/bin/' for line in copies),
                        'the built CLI must be shipped in the tools image')
        scanner = next(s for s in stages if ' AS scanner\n' in s)
        self.assertNotIn('invoice-identity-migrate', scanner)


if __name__ == '__main__': unittest.main()

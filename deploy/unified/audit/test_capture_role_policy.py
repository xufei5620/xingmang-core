import hashlib
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

import capture_metadata
from test_identity_audit import fixture, ORIGIN


class CaptureRolePolicyTests(unittest.TestCase):
    def test_capture_passes_exact_role_to_readonly_sql_and_binds_original_rows(self):
        platform, invoice, mapping = fixture()
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root/'roles.json').write_text(json.dumps(platform['role_scope_map']))
            (root/'mapping.json').write_text(json.dumps(mapping))
            output = root/'captured'
            argv = ['capture_metadata.py', '--psql', 'psql', '--platform-service', 'platform_readonly',
                    '--invoice-service', 'invoice_readonly', '--role-map', str(root/'roles.json'),
                    '--admin-role', 'admin', '--mode', 'server-rehearsal', '--staff-origin', ORIGIN,
                    '--crosswalk', str(root/'mapping.json'), '--output-directory', str(output)]
            answers = [subprocess.CompletedProcess([], 0, json.dumps(p).encode(), b'') for p in [platform, invoice]]
            with patch.object(sys, 'argv', argv), patch.object(capture_metadata.subprocess, 'run', side_effect=answers) as execute:
                self.assertEqual(capture_metadata.main(), 0)
            self.assertIn('admin_role=admin', execute.call_args_list[0].args[0])
            self.assertIn('default_transaction_read_only=on', execute.call_args_list[0].kwargs['env']['PGOPTIONS'])
            record = json.loads((output/'mfa-query-record.json').read_text())
            self.assertEqual(record['status'], 'PASS')
            self.assertEqual(record['role_policy']['invoice_admin_total'], 1)
            self.assertEqual(record['platform_snapshot_sha256'], hashlib.sha256((output/'platform.json').read_bytes()).hexdigest())
            self.assertEqual(Path(record['platform_snapshot']), output/'platform.json')


if __name__ == '__main__': unittest.main()

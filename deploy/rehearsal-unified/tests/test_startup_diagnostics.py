"""Startup failures retain only fixed error categories, never diagnostic text."""
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import lifecycle

class StartupDiagnosticsTests(unittest.TestCase):
    def test_original_failure_is_preserved_and_only_enum_is_recorded(self):
        with tempfile.TemporaryDirectory() as tmp:
            driver=lifecycle.DockerDriver.__new__(lifecycle.DockerDriver)
            driver.env={};driver.sequence=0;driver.output=Path(tmp)
            failure=subprocess.CompletedProcess([],73,b'synthetic-private-output',b'dependency migrate failed: password=synthetic-private-value')
            with patch.object(lifecycle.subprocess,'run',return_value=failure):
                with self.assertRaises(lifecycle.OperatorError):driver.command('start-new-unified',['docker','compose','up'])
            event=json.loads(next((Path(tmp)/'events').glob('*.json')).read_text())
            self.assertEqual(event.get('stderr_classes'),['DEPENDENCY_FAILURE'])
            self.assertEqual(event['exit_code'],73)
            self.assertNotIn('synthetic-private',json.dumps(event))
            self.assertNotIn('password=',json.dumps(event))

if __name__=='__main__':unittest.main()

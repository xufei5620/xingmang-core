"""Execute the real TCP half-close regression; never substitutes for HTTP D/E."""
import os
from pathlib import Path
import shutil
import subprocess
import unittest


class LocalTransportTests(unittest.TestCase):
    def test_standard_library_relay_preserves_both_eof_directions(self):
        root=Path(__file__).resolve().parents[1]/'local-transport'
        go=shutil.which('go')
        self.assertIsNotNone(go,'the already required Go toolchain must run the transport regression')
        files=[root/'main.go',root/'main_test.go']
        self.assertTrue(all(f.is_file() for f in files),'the tested local relay must be versioned')
        env=dict(os.environ,GOPROXY='off',GOSUMDB='off',GOWORK='off',GOTOOLCHAIN='local')
        result=subprocess.run([go,'test','-count=1',*[str(f) for f in files]],cwd=root,env=env,capture_output=True,timeout=90)
        self.assertEqual(result.returncode,0,(result.stdout+result.stderr).decode('utf-8',errors='replace'))


if __name__=='__main__':unittest.main()

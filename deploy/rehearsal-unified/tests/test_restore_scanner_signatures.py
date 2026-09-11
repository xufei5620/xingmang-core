"""D copies public scanner definitions into a fresh owned writable volume."""
from pathlib import Path
import subprocess
import sys
from types import SimpleNamespace
import unittest
from unittest.mock import patch
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import restore

class ScannerSignatureCloneTests(unittest.TestCase):
    def test_clone_preserves_input_and_requires_three_verified_copies(self):
        self.assertTrue(hasattr(restore,'clone_scanner_signatures'))
        source='xm-rehearsal-original-clamav'; target='xm-rehearsal-fresh-clamav'
        value={'volumes':{'clamav_database':target},'owner_id':'a'*32,'tools_image':'sha256:'+'1'*64,
               'readonly_input_volumes':[{'name':source,'owner_id':'original','kind':'scanner_signatures'}]}
        calls=[]
        proof=''.join(name+'|'+('b'*64)+'|100:100:644:1789011840:42\n' for name in ('main.cvd','daily.cld','bytecode.cvd')).encode()
        driver=SimpleNamespace(docker=['docker'],command=lambda n,a:calls.append(a) or subprocess.CompletedProcess(a,0,proof,b''))
        def metadata(_,name):return {'Name':name,'Labels':{'xingmang.rehearsal.owner':'original' if name==source else 'a'*32}}
        with patch.object(restore,'volume_metadata',side_effect=metadata):
            result=restore.clone_scanner_signatures(driver,value)
            self.assertEqual(len(result['files']),3)
            argv=calls[-1]
            self.assertIn('type=volume,src='+source+',dst=/source,readonly',argv)
            self.assertIn('type=volume,src='+target+',dst=/clone',argv)
            self.assertEqual(argv[argv.index('--network')+1],'none')
            self.assertIn('cp -p --',argv[-1])
            self.assertIn('sha256sum',argv[-1]);self.assertIn('stat -c',argv[-1])
            driver.command=lambda n,a:subprocess.CompletedProcess(a,1,proof,b'')
            with self.assertRaises(restore.OperatorError):restore.clone_scanner_signatures(driver,value)
            driver.command=lambda n,a:subprocess.CompletedProcess(a,0,proof.splitlines()[0]+b'\n',b'')
            with self.assertRaises(restore.OperatorError):restore.clone_scanner_signatures(driver,value)
            value['owner_id']='foreign'
            with self.assertRaises(restore.OperatorError):restore.clone_scanner_signatures(driver,value)

if __name__=='__main__':unittest.main()

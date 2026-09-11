"""Frozen data stays fresh; exact owned credential inputs remain read-only."""
import copy
import json
from pathlib import Path
import subprocess
import sys
from types import SimpleNamespace
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import restore


class RestoreInputsTests(unittest.TestCase):
    def fixture(self):
        value={"volumes":{"documents":"xm-rehearsal-fresh"},"readonly_input_volumes":[
            {"name":"xm-rehearsal-input","owner_id":"20260912a","kind":"source_credentials"}]}
        resolved={"networks":{"n":{"internal":True}},"volumes":{"fresh":{"name":"xm-rehearsal-fresh"},"input":{"name":"xm-rehearsal-input"}},
                  "services":{"api":{"networks":{"n":{}},"volumes":[{"type":"volume","source":"fresh","target":"/documents"},
                    {"type":"volume","source":"input","target":"/fixture","read_only":True}]}}}
        meta={"Name":"xm-rehearsal-input","Labels":{"xingmang.rehearsal.owner":"20260912a"}}
        driver=SimpleNamespace(docker=['docker'],projects=lambda _: [{"name":"xm-rehearsal-test","kind":"unified"}],
            compose=lambda *a:subprocess.CompletedProcess([],0,json.dumps(resolved).encode(),b''),
            command=lambda *a,**k:subprocess.CompletedProcess([],0,json.dumps([meta]).encode(),b''))
        return driver,value,resolved,meta

    def test_exact_owned_readonly_source_input_is_allowed_but_never_data(self):
        driver,value,resolved,meta=self.fixture()
        restore.validate_frozen_mounts(driver,value)
        for field,wrong in (("read_only",False),("target","/documents"),("source","foreign")):
            original=copy.deepcopy(resolved['services']['api']['volumes'][1])
            resolved['services']['api']['volumes'][1][field]=wrong
            with self.subTest(field=field), self.assertRaises(restore.OperatorError):restore.validate_frozen_mounts(driver,value)
            resolved['services']['api']['volumes'][1]=original
        meta['Labels']['xingmang.rehearsal.owner']='foreign'
        with self.assertRaises(restore.OperatorError):restore.validate_frozen_mounts(driver,value)

    def test_scanner_signature_input_does_not_authorize_source_credentials(self):
        driver,value,resolved,meta=self.fixture()
        value['readonly_input_volumes'][0]['kind']='scanner_signatures'
        with self.assertRaises(restore.OperatorError):restore.validate_frozen_mounts(driver,value)
        resolved['services']['api']['volumes'][1]['target']='/clamav-db'
        restore.validate_frozen_mounts(driver,value)
        value['volumes']['documents']='xm-rehearsal-input'
        with self.assertRaises(restore.OperatorError):restore.validate_frozen_mounts(driver,value)

    def test_verified_archive_restores_original_numeric_owner_and_modes(self):
        captures=[]
        driver=SimpleNamespace(docker=['docker'],config={'backups':{'invoice':{'age_binary':'age','identity_file':'/offline/key','components':{'source_state':'/cipher.age'}}}},
            pipeline=lambda name,producer,consumer:captures.append(consumer))
        value={'volumes':{'source_state':'xm-rehearsal-state'},'owner_id':'a'*32,'archive_tmpfs_bytes':134217728,'tools_image':'sha256:'+'1'*64}
        with patch.object(restore,'volume_metadata',return_value={'Name':'xm-rehearsal-state','Labels':{'xingmang.rehearsal.owner':'a'*32}}):
            restore.extract_archive(driver,value,'invoice','source_state')
        args=captures[0]; command=args[-1]
        self.assertIn('tar --numeric-owner -xf ',command)
        # Both shipped BusyBox tar and GNU tar preserve these attributes when
        # extracting as root; GNU-only positive flags break the tools image.
        self.assertNotIn('--same-owner',command)
        self.assertNotIn('--same-permissions',command)
        self.assertNotIn('--no-same-permissions',command)
        self.assertNotIn('--no-same-owner',command)
        self.assertLess(command.index('invoice-archive-verify'),command.index('; tar '))
        self.assertEqual([args[i+1] for i,x in enumerate(args[:-1]) if x=='--cap-add'],['CHOWN','FOWNER','DAC_OVERRIDE'])
        self.assertIn('none',args)

    def test_signed_metadata_reader_can_read_private_modes_without_write_access(self):
        calls=[]
        def read_metadata(name,args):
            calls.append(args)
            self.assertIn('--cap-drop',args)
            self.assertEqual([args[i+1] for i,x in enumerate(args[:-1]) if x=='--cap-add'],['DAC_READ_SEARCH'])
            self.assertIn('--read-only',args)
            self.assertTrue(args[args.index('--mount')+1].endswith(',readonly'))
            self.assertEqual(args[args.index('--entrypoint')+1],'/bin/cat')
            return subprocess.CompletedProcess([],0,b'header\nvalue\n',b'')
        driver=SimpleNamespace(docker=['docker'],command=read_metadata,
            projects=lambda _: [{'name':'xm-rehearsal-test'}],
            compose=lambda *args: subprocess.CompletedProcess([],0,b'header\nvalue\n',b''))
        value={'databases':{domain:{'project':'xm-rehearsal-test','service':domain,'owner':'owner','database':'db'} for domain in ('platform','invoice')},
            'volumes':{domain+'_metadata':'xm-rehearsal-'+domain for domain in ('platform','invoice')},'tools_image':'sha256:'+'1'*64}
        result=restore.verify_snapshot_metadata(driver,value)
        self.assertEqual(len(calls),sum(map(len,restore.SNAPSHOT_QUERIES.values())))
        self.assertEqual(set(result),{'platform','invoice'})
        driver.compose=lambda *args: subprocess.CompletedProcess([],0,b'header\nchanged\n',b'')
        with self.assertRaises(restore.OperatorError): restore.verify_snapshot_metadata(driver,value)


if __name__=='__main__':unittest.main()

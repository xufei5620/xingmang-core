"""Staged identities are consumed by age then destroyed; originals survive."""
import copy
import json
from pathlib import Path
import subprocess
import sys
import tempfile
from types import SimpleNamespace
import unittest
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import restore

class TemporaryIdentityTests(unittest.TestCase):
    def setUp(self):
        self.assertTrue(hasattr(restore,'configure_temporary_identities'),'temporary identities are not consumed by the restore')
        self.assertTrue(hasattr(restore,'cleanup_temporary_identities'),'temporary identities have no scoped cleanup')

    def fixture(self,root):
        state=root/'state';owner='a'*32;folder=state/'tmpfs'/owner;folder.mkdir(parents=True)
        originals={d:root/(d+'.original') for d in ('platform','invoice')}
        temporary={d:folder/(d+'.age-identity') for d in originals}
        for p in [*originals.values(),*temporary.values()]:p.write_text('non-secret test fixture')
        config={'backups':{d:{'identity_file':str(p),'age_binary':'age','components':{'database':'cipher.age'}} for d,p in originals.items()}}
        value={'owner_id':owner,'temporary_identity_paths':[str(temporary[d]) for d in restore.BACKUP_ANCHORS],'shred_binary':str(root/'shred')}
        (root/'shred').write_text('tool path fixture')
        (root/'temporary-identity-ownership.json').write_text(json.dumps({'owner_id':owner,'paths':value['temporary_identity_paths']}))
        return state,config,value,originals,temporary

    def test_age_uses_both_staged_paths_without_changing_original_descriptors(self):
        with tempfile.TemporaryDirectory() as tmp:
            state,original,value,paths,temporary=self.fixture(Path(tmp));trial=copy.deepcopy(original)
            restore.configure_temporary_identities(trial,value,state)
            for domain in paths:
                self.assertEqual(restore.decrypt_args(trial['backups'][domain],'database')[3],str(temporary[domain]))
                self.assertEqual(original['backups'][domain]['identity_file'],str(paths[domain]))

    def test_incomplete_foreign_or_original_paths_are_rejected(self):
        with tempfile.TemporaryDirectory() as tmp:
            state,original,value,paths,temporary=self.fixture(Path(tmp))
            for wrong in ([str(temporary['invoice'])],[str(paths['invoice']),str(temporary['platform'])],[str(temporary['invoice'])]*2):
                with self.subTest(wrong=wrong),self.assertRaises(restore.OperatorError):
                    restore.configure_temporary_identities(copy.deepcopy(original),{**value,'temporary_identity_paths':wrong},state)

    def test_cleanup_executes_fixed_three_pass_shred_and_preserves_originals(self):
        with tempfile.TemporaryDirectory() as tmp:
            state,config,value,originals,temporary=self.fixture(Path(tmp));calls=[]
            def command(name,args):
                calls.append(args);Path(args[-1]).unlink();return subprocess.CompletedProcess(args,0)
            driver=SimpleNamespace(state=state,config=config,output=Path(tmp),command=command)
            result=restore.cleanup_temporary_identities(driver,value)
            self.assertEqual(result['temporary_identities_shredded'],2)
            for args in calls:self.assertEqual(args[1:-1],['--iterations=3','--zero','--remove=unlink','--'])
            self.assertTrue(all(p.exists() for p in originals.values()))
            self.assertTrue(all(not p.exists() for p in temporary.values()))

    def test_failed_shred_or_surviving_file_cannot_pass(self):
        with tempfile.TemporaryDirectory() as tmp:
            state,config,value,_,temporary=self.fixture(Path(tmp))
            for code in (0,1):
                driver=SimpleNamespace(state=state,config=config,output=Path(tmp),command=lambda *a:subprocess.CompletedProcess([],code))
                with self.subTest(code=code),self.assertRaises(restore.OperatorError):restore.cleanup_temporary_identities(driver,value)

    def test_failed_second_copy_is_journaled_and_first_copy_is_cleaned(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp);state,config,value,originals,temporary=self.fixture(root)
            for path in temporary.values():path.unlink()
            next(iter(temporary.values())).parent.rmdir()
            (root/'temporary-identity-ownership.json').unlink()
            value['identity_copy_binary']=str(root/'shred');calls=[]
            def command(name,args):
                calls.append(name)
                if name.startswith('stage-'):
                    if len(calls)==2:raise restore.OperatorError('copy tool failed')
                    Path(args[-1]).write_text('non-secret staged fixture')
                else:Path(args[-1]).unlink()
                return subprocess.CompletedProcess(args,0)
            driver=SimpleNamespace(state=state,config=config,output=root,command=command)
            with self.assertRaises(restore.OperatorError):restore.stage_temporary_identities(driver,value)
            result=restore.cleanup_temporary_identities(driver,value)
            self.assertEqual(result['temporary_identities_shredded'],1)
            self.assertTrue(all(p.exists() for p in originals.values()))
            self.assertTrue(all(not p.exists() for p in temporary.values()))

    def test_nonzero_shred_cannot_pass_even_if_it_removed_the_file(self):
        with tempfile.TemporaryDirectory() as tmp:
            state,config,value,_,_=self.fixture(Path(tmp))
            def failed_shred(name,args):
                Path(args[-1]).unlink();return subprocess.CompletedProcess(args,1)
            driver=SimpleNamespace(state=state,config=config,output=Path(tmp),command=failed_shred)
            with self.assertRaises(restore.OperatorError):restore.cleanup_temporary_identities(driver,value)

if __name__=='__main__':unittest.main()

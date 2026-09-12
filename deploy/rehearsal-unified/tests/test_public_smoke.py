import importlib.util
import json
from pathlib import Path
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

ROOT=Path(__file__).resolve().parents[1]
sys.path.insert(0,str(ROOT))


class PublicSmokeTests(unittest.TestCase):
    def module(self):
        self.assertTrue((ROOT/'public_smoke.py').is_file(),'E has no credential-free five-step acceptance')
        import public_smoke
        return public_smoke

    def test_five_steps_need_no_real_credentials_and_do_not_authenticate(self):
        m=self.module();calls=[]
        class Client:
            def __init__(self,base,ca_file,records,connect_to=None):pass
            def call(self,method,path):
                calls.append((method,path))
                return {'invoice_ready':True,'invoice':{'ready':True},'modules':{name:{'ready':True,'status':'ready'} for name in ('platform','invoice_sources','invoice_projection')}}
            def raw(self,method,path):calls.append((method,path));return b'<html>application</html>','text/html'
            def denied(self,method,path,*args,**kwargs):calls.append((method,path))
        with tempfile.TemporaryDirectory() as folder:
            root=Path(folder);(root/'deployment-record.json').write_text(json.dumps({'snapshot':{'ledger_hashes':{'platform':['a'],'invoice':['b']}}}))
            d=SimpleNamespace(config={'mode':'production','candidate':{'head':'a'*40,'databases':{k:{'project':k,'service':'postgres','owner':'owner','database':k} for k in ('platform','invoice')}}},state=root,output=root,
                projects=lambda _: [{'name':k} for k in ('platform','invoice')],ledger_snapshot=lambda _: {'platform':['a'],'invoice':['b']})
            def compose(project,name,args):
                self.assertIn('READ ONLY',args[-1]);self.assertIn('statement_timeout',args[-1])
                if name=='public-no-dead':value={'database':'invoice','source_ingest_dead':0,'eligibility_dead':0,'transaction_read_only':'on'}
                else:value={'database':project['name'],'migration_top':'0032' if project['name']=='invoice' else 10,'dirty':False,'transaction_read_only':'on'}
                return SimpleNamespace(stdout=json.dumps(value).encode())
            d.compose=compose
            config={'schema':'xingmang.unified.public-smoke/v1','mode':'production','origins':{'admin':'https://console.example.invalid','user':'https://invoice.example.invalid'}}
            with patch.object(m,'Client',Client):result=m.run(d,config)
            self.assertEqual(result['status'],'PASS')
            self.assertEqual([x['name'] for x in result['steps']],list(m.REQUIRED))
            self.assertFalse(result['human_login_verified'])
            self.assertFalse(any(path.endswith('/platform-login') or path.endswith('/login/totp') for _,path in calls))
            d.ledger_snapshot=lambda _:{'platform':['changed'],'invoice':['b']}
            with patch.object(m,'Client',Client),self.assertRaises(Exception):m.run(d,config)

    def test_dead_or_changed_ledger_is_rejected(self):
        m=self.module()
        for value in ({'database':'invoice','source_ingest_dead':1,'eligibility_dead':0,'transaction_read_only':'on'},
                      {'database':'invoice','source_ingest_dead':'0','eligibility_dead':0,'transaction_read_only':'on'}):
            with self.assertRaises(Exception):m.require_no_dead(value,'invoice')


if __name__=='__main__':unittest.main()

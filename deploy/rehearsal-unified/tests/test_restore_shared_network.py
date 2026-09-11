"""A fresh main network can be referenced by the separate source project."""
import copy
import json
from pathlib import Path
import subprocess
import sys
from types import SimpleNamespace
import unittest
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import restore

class SharedNetworkTests(unittest.TestCase):
    def fixture(self):
        name='xm-rehearsal-fresh-ingest'
        base={'volumes':{'data':{'name':'xm-rehearsal-fresh-data'}},'services':{'app':{'networks':{'ingest':{}},'volumes':[{'type':'volume','source':'data','target':'/state'}]}}}
        main=copy.deepcopy(base);main['networks']={'ingest':{'name':name,'internal':True,'ipam':{'config':[{'subnet':'172.31.252.0/24'}]}}}
        source=copy.deepcopy(base);source['networks']={'ingest':{'name':name,'external':True}}
        documents={'unified':main,'sources':source};inspected=[]
        def command(_,argv,**kwargs):
            inspected.append(argv[-1])
            return subprocess.CompletedProcess(argv,0,json.dumps([{'Internal':False}]).encode(),b'')
        driver=SimpleNamespace(docker=['docker'],projects=lambda _: [{'name':'xm-rehearsal-main','kind':'unified'},{'name':'xm-rehearsal-source','kind':'sources'}],
            compose=lambda p,*args:subprocess.CompletedProcess([],0,json.dumps(documents[p['kind']]).encode(),b''),command=command)
        return driver,{'volumes':{'source_state':'xm-rehearsal-fresh-data'}},documents,inspected

    def test_future_exact_internal_network_does_not_require_prior_creation(self):
        driver,value,_,inspected=self.fixture()
        failure=None
        try: restore.validate_frozen_mounts(driver,value)
        except restore.OperatorError as error: failure=str(error)
        self.assertIsNone(failure)
        self.assertEqual(inspected,[])

    def test_unknown_external_network_still_requires_actual_isolation(self):
        driver,value,documents,inspected=self.fixture()
        documents['sources']['networks']['ingest']['name']='xm-rehearsal-other'
        with self.assertRaises(restore.OperatorError):restore.validate_frozen_mounts(driver,value)
        self.assertEqual(inspected,['xm-rehearsal-other'])

    def test_future_network_must_have_one_owner_and_explicit_internal_ipam(self):
        for mutation in ('not-internal','no-ipam','duplicate-owner'):
            driver,value,documents,_=self.fixture()
            if mutation=='not-internal': documents['unified']['networks']['ingest']['internal']=False
            if mutation=='no-ipam': documents['unified']['networks']['ingest'].pop('ipam')
            if mutation=='duplicate-owner':documents['sources']['networks']['ingest']=copy.deepcopy(documents['unified']['networks']['ingest'])
            with self.subTest(mutation=mutation),self.assertRaises(restore.OperatorError):restore.validate_frozen_mounts(driver,value)

if __name__=='__main__':unittest.main()

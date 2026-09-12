import copy
import importlib.util
from pathlib import Path
import sys
import unittest
from types import SimpleNamespace

ROOT=Path(__file__).resolve().parents[1];sys.path.insert(0,str(ROOT))


class SeedIsolationTests(unittest.TestCase):
    def module(self):
        self.assertTrue((ROOT/'rehearsal_seed.py').is_file(),'no attested frozen-only seed implementation')
        import rehearsal_seed
        return rehearsal_seed

    def test_production_or_old_project_cannot_seed(self):
        m=self.module()
        value={'owner_id':'a'*32,'projects':[{'name':'xm-rehearsal-new-unified','kind':'unified'},{'name':'xm-rehearsal-new-sources','kind':'sources'}],
            'seed':{'network_cidr':'11.240.252.0/28','provider_ip':'11.240.252.10','api_ip':'11.240.252.11','port':18080,'admin_role':'admin'}}
        config={'mode':'server-rehearsal','candidate':{'projects':value['projects']},'previous':{'projects':[{'name':'old-platform'}]}}
        m.validate_scope(config,value)
        for mutate in (lambda c:c.update(mode='production'),lambda c:c['candidate']['projects'][0].update(name='old-platform'),
                       lambda c:c['previous']['projects'][0].update(name='xm-rehearsal-new-unified')):
            c=copy.deepcopy(config);mutate(c)
            with self.assertRaises(Exception):m.validate_scope(c,value)

    def test_database_needs_actual_id_image_owner_network_and_fresh_volume(self):
        m=self.module();owner='a'*32;cid='b'*64;image='sha256:'+'c'*64
        expected={'container_id':cid,'image_id':image,'project':'xm-rehearsal-run-unified','service':'postgres','volume':'xm-rehearsal-run-platform-db'}
        actual={'id':cid,'image_id':image,'running':True,'project':expected['project'],'service':'postgres','owner':owner,
            'mounts':[{'Type':'volume','Name':expected['volume'],'Destination':'/var/lib/postgresql','RW':True}],
            'networks':{'xm-rehearsal-run-db':{}}}
        volumes={expected['volume']:{'Name':expected['volume'],'Labels':{'xingmang.rehearsal.owner':owner}}}
        networks={'xm-rehearsal-run-db':{'Internal':True,'Labels':{'xingmang.rehearsal.owner':owner}}}
        m.require_database(actual,expected,owner,volumes,networks)
        for key,value in [('id','d'*64),('image_id','sha256:'+'e'*64),('project','production'),('service','api'),('owner','f'*32),('running',False)]:
            a=copy.deepcopy(actual);a[key]=value
            with self.subTest(key=key),self.assertRaises(Exception):m.require_database(a,expected,owner,volumes,networks)
        for mutate in (lambda a,v,n:a['mounts'][0].update(Name='production-volume'),lambda a,v,n:v[expected['volume']]['Labels'].clear(),lambda a,v,n:n['xm-rehearsal-run-db'].update(Internal=False)):
            a,v,n=copy.deepcopy((actual,volumes,networks));mutate(a,v,n)
            with self.assertRaises(Exception):m.require_database(a,expected,owner,v,n)

    def test_seed_cleanup_shreds_even_if_normal_resource_cleanup_will_fail(self):
        m=self.module();s=object.__new__(m.Seed);calls=[]
        s.cid='b'*64;s.image='sha256:'+'c'*64;s.owner='a'*32;s.project='xm-rehearsal-owned-seed';s.volume=s.project+'-private';s.network=s.project+'-auth';s.name=s.project+'-helper'
        s.spec={'provider_ip':'11.240.252.10','port':18080};s.created_volume=True;s.created_network=True;s.seeded=True;s.record={}
        s.assert_helper=lambda:None;s.save=lambda:None
        s.private_command=lambda op,*a:calls.append(op) or b''
        def command(op,args,**kwargs):
            calls.append(op)
            if op=='remove-helper':raise m.OperatorError('synthetic container cleanup failed')
            if op=='cleanup-volume-inspect':return SimpleNamespace(returncode=0,stdout=__import__('json').dumps([{'Name':s.volume,'Driver':'local','Options':{'type':'tmpfs'},'Labels':{'xingmang.rehearsal.owner':s.owner}}]).encode())
            if op=='cleanup-helper-inspect':return SimpleNamespace(returncode=0,stdout=__import__('json').dumps({'id':s.cid,'running':True,'owner':s.owner,'project':s.project,'service':'fixture-provider','image_id':s.image}).encode())
            if op=='cleanup-network-inspect':return SimpleNamespace(returncode=0,stdout=__import__('json').dumps([{'Name':s.network,'Internal':True,'Labels':{'xingmang.rehearsal.owner':s.owner}}]).encode())
            return SimpleNamespace(returncode=0,stdout=b'')
        s.command=command
        with self.assertRaises(m.OperatorError):s.cleanup()
        self.assertIn('shred',calls);self.assertLess(calls.index('shred'),calls.index('remove-helper'))
        self.assertTrue(s.record['cleanup']['private_shredded'])
        self.assertIn('HELPER_CLEANUP_FAILED',s.record['cleanup_errors'])


if __name__=='__main__':unittest.main()

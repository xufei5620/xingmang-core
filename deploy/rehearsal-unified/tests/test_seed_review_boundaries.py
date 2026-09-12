import errno
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from types import SimpleNamespace
from unittest.mock import patch

SOURCE=Path(__file__).resolve().parents[1]
sys.path.insert(0,str(SOURCE))
import lifecycle
import rehearsal_seed as seed


class CriticalSeedBoundaries(unittest.TestCase):
    def fixture(self,folder):
        d=object.__new__(lifecycle.DockerDriver);d.docker=['docker'];d.env={};d.output=Path(folder);d.sequence=0
        s=object.__new__(seed.Seed);s.driver=d;s.owner='a'*32;s.project='xm-rehearsal-test-seed';s.name=s.project+'-helper'
        s.image='sha256:'+'b'*64;s.cid='c'*64;s.volume=s.project+'-private';s.network=s.project+'-auth'
        s.created_volume=True;s.created_network=True;s.seeded=True;s.spec={'provider_ip':'11.240.254.10','port':18080}
        s.record={'private_material_possible':True};s.private_paths=set()
        return s

    def boundary(self,s,calls,fail_wipe=False):
        def run(args,**kwargs):
            calls.append(args)
            value=None
            if args[1:3]==['volume','inspect']:
                value=[{'Name':s.volume,'Driver':'local','Options':{'type':'tmpfs'},'Labels':{'xingmang.rehearsal.owner':s.owner}}]
            elif args[1:3]==['network','inspect']:
                value=[{'Id':'d'*64,'Name':s.network,'Internal':True,'Labels':{'xingmang.rehearsal.owner':s.owner}}]
            elif args[1]=='inspect':
                value={'id':s.cid,'image_id':s.image,'running':True,'owner':s.owner,'project':s.project,'service':'fixture-provider',
                    'privileged':False,'readonly_rootfs':True,'cap_add':['CHOWN','DAC_READ_SEARCH'],'cap_drop':['ALL'],
                    'networks':{s.network:{'IPAddress':s.spec['provider_ip'],'NetworkID':'d'*64}},
                    'mounts':[{'Destination':seed.PRIVATE,'Name':s.volume}]}
            if '--shred-fixtures' in args and fail_wipe:return subprocess.CompletedProcess(args,17,b'',b'')
            raw=json.dumps(value).encode() if value is not None else b'1:2' if 'stat' in args else b''
            return subprocess.CompletedProcess(args,0,raw,b'')
        return run

    def test_readonly_socket_mount_is_not_a_frozen_database_proof(self):
        owner='a'*32;name='xm-rehearsal-db';network='xm-rehearsal-net'
        expected={'container_id':'b'*64,'image_id':'sha256:'+'c'*64,'project':'xm-rehearsal-unified','service':'postgres','volume':name}
        actual={'id':expected['container_id'],'image_id':expected['image_id'],'project':expected['project'],'service':'postgres','owner':owner,'running':True,
            'mounts':[{'Type':'volume','Name':name,'Destination':'/var/lib/postgresql','RW':True},
                {'Type':'bind','Source':'/production/socket','Destination':'/var/run/postgresql','RW':False}], 'networks':{network:{}}}
        volumes={name:{'Name':name,'Labels':{'xingmang.rehearsal.owner':owner}}}
        networks={network:{'Internal':True,'Labels':{'xingmang.rehearsal.owner':owner}}}
        with self.assertRaises(lifecycle.OperatorError):seed.require_database(actual,expected,owner,volumes,networks)

    def test_socket_aliases_ancestors_and_files_are_protected(self):
        for target in ('/','/var','/var/run','/var/run/postgresql','/var/run/postgresql/.s.PGSQL.5432',
                       '/run','/run/postgresql','/run/postgresql/.s.PGSQL.5432'):
            with self.subTest(target=target),self.assertRaises(lifecycle.OperatorError):
                lifecycle.require_postgres_socket_unshadowed([{'type':'bind','target':target,'read_only':True}])
        lifecycle.require_postgres_socket_unshadowed([{'type':'bind','target':'/run/secrets/postgres-password','read_only':True}])

    def test_libpq_redirection_is_cleared_and_tcp_identity_is_rejected(self):
        s=object.__new__(seed.Seed);s.owner='a'*32
        s.value={'volumes':{name+'_database':'xm-rehearsal-'+name for name in ('platform','invoice')}}
        projects=[{'name':'xm-rehearsal-unified','services':{name:{'image_id':'sha256:'+'b'*64} for name in ('platform','invoice')}}]
        s.driver=SimpleNamespace(config={'candidate':{'databases':{name:{'project':projects[0]['name'],'service':name,'owner':'owner','database':name} for name in ('platform','invoice')}}},
            projects=lambda _:projects,compose=lambda p,n,a:SimpleNamespace(stdout=(('c' if a[-1]=='platform' else 'd')*64).encode()))
        def inspect(cid):
            name='platform' if cid[0]=='c' else 'invoice'
            return {'id':cid,'image_id':'sha256:'+'b'*64,'running':True,'owner':s.owner,'project':projects[0]['name'],'service':name,
                'mounts':[{'Type':'volume','Name':s.value['volumes'][name+'_database'],'Destination':'/var/lib/postgresql','RW':True}],
                'networks':{'xm-rehearsal-db':{}}}
        s.inspect=inspect;calls=[]
        def command(name,args,**kwargs):
            calls.append(args)
            if name.startswith('db-volume-'):value=[{'Name':args[-1],'Labels':{'xingmang.rehearsal.owner':s.owner}}]
            elif name.startswith('db-network-'):value=[{'Internal':True,'Labels':{'xingmang.rehearsal.owner':s.owner}}]
            else:value={'name':name.removeprefix('db-identity-'),'oid':1234,'server_addr':'172.30.240.2','server_port':5432}
            return SimpleNamespace(stdout=json.dumps(value).encode())
        s.command=command
        with self.assertRaises(lifecycle.OperatorError):s.database_targets()
        pgcall=next(args for args in calls if 'psql' in args)
        for variable in ('PGHOSTADDR','PGHOST','PGPORT','PGSERVICE','PGSERVICEFILE','PGOPTIONS','PGPASSWORD'):
            self.assertIn(variable,pgcall)
            self.assertEqual(pgcall[pgcall.index(variable)-1],'-u')
            self.assertNotIn(variable+'=',pgcall)
        self.assertIn('PGPASSFILE=/nonexistent',pgcall)

    def test_log_enospc_cannot_prevent_private_wipe(self):
        with tempfile.TemporaryDirectory() as folder:
            s=self.fixture(folder);calls=[]
            full=OSError(errno.ENOSPC,'synthetic log device full')
            with patch.object(subprocess,'run',side_effect=self.boundary(s,calls)),patch.object(seed,'atomic_json',side_effect=full),patch.object(lifecycle,'atomic_json',side_effect=full):
                with self.assertRaises(Exception):s.cleanup()
            self.assertTrue(any('--shred-fixtures' in argv for argv in calls),'logging failed before any private wipe could run')

    def test_failed_wipe_keeps_live_last_holder_and_original_tmpfs(self):
        with tempfile.TemporaryDirectory() as folder:
            s=self.fixture(folder);calls=[]
            with patch.object(subprocess,'run',side_effect=self.boundary(s,calls,fail_wipe=True)):
                with self.assertRaises(Exception):s.cleanup()
            self.assertFalse(any(argv[1:3]==['rm','-f'] for argv in calls),'failed wipe discarded last live holder')
            self.assertFalse(any(argv[1:3]==['volume','rm'] for argv in calls))


if __name__=='__main__':unittest.main()

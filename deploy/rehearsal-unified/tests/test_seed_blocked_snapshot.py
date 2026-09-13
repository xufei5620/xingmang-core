import copy
import json
from pathlib import Path
import sys
import unittest
from types import SimpleNamespace

ROOT=Path(__file__).resolve().parents[1]
sys.path.insert(0,str(ROOT))
import rehearsal_seed as m

SUB='10000000-0000-4000-8000-000000000001'
NEW='10000000-0000-4000-8000-000000000002'
SUB_USER='20000000-0000-4000-8000-000000000001'
NEW_USER='20000000-0000-4000-8000-000000000002'
LOT='30000000-0000-4000-8000-000000000001'
NEW_LOT='30000000-0000-4000-8000-000000000002'


class SeedBlockedSnapshotTests(unittest.TestCase):
    def test_newapi_lot_is_part_of_blocked_snapshot(self):
        s,_,_,_,_=self.fixture()
        result=s.blocked_invoice_snapshot(SUB,LOT)
        self.assertEqual(set(result.get('lots',{})),{'sub2api','newapi'},'both platform lots must be captured')
        self.assertEqual(result['lots']['newapi']['id'],s.summary['invoice']['clients']['newapi']['lot_id'])
        self.assertEqual(result['lots']['newapi']['source_instance_id'],NEW)
        self.assertIn('reserved_minor',result['lots']['newapi'])

    def fixture(self):
        s=object.__new__(m.Seed)
        s.seeded=True;s.owner='a'*32;s.project='xm-rehearsal-test-seed'
        s.guard={'schema_version':1,'mode':'server-rehearsal','owner':s.owner,'project':s.project,'nonce':'b'*64,
            'sources':{'sub2api':{'id':SUB},'newapi':{'id':NEW}},
            'databases':{'platform':{'name':'platform','oid':100,'server_addr':None,'server_port':None},'invoice':{'name':'invoice','oid':101,'server_addr':None,'server_port':None}}}
        s.summary={'invoice':{'clients':{'sub2api':{'user_id':SUB_USER,'lot_id':LOT},'newapi':{'user_id':NEW_USER,'lot_id':NEW_LOT}}}}
        targets={domain:{'expected':{'container_id':digit*64},'identity':copy.deepcopy(identity),'command':['exec','-i',digit*64,'psql','-X','-qAt','-v','ON_ERROR_STOP=1','-d',domain]}
                 for domain,digit,identity in [('platform','c',s.guard['databases']['platform']),('invoice','d',s.guard['databases']['invoice'])]}
        s.database_restore_targets=copy.deepcopy(targets)
        calls=[];checks=[]
        def database_targets():checks.append(True);return copy.deepcopy(targets)
        s.database_targets=database_targets
        result={'users':{'sub2api':{'invoice_requests':0},'newapi':{'invoice_requests':0}},'lots':{kind:{'id':lot,'source_instance_id':source,'consumed_cash_minor':100000,'reserved_minor':0,'issued_minor':0,'verified_cash_minor':100000,'verification':'verified','refund_frozen':False,'eligibility_kind':'WALLET_CASH'} for kind,lot,source in [('sub2api',LOT,SUB),('newapi',NEW_LOT,NEW)]}}
        def command(name,args,**kwargs):
            calls.append((name,args,kwargs))
            self.assertEqual(args,['docker',*targets['invoice']['command']])
            sql=kwargs['input_bytes'].decode()
            self.assertIn('READ ONLY;',sql);self.assertTrue(sql.rstrip().endswith('COMMIT;'))
            self.assertIn('pg_catalog.pg_database',sql);self.assertIn('current_database()',sql)
            self.assertIn('inet_server_addr() IS NOT DISTINCT FROM NULL::inet',sql)
            self.assertIn('inet_server_port() IS NOT DISTINCT FROM NULL::integer',sql)
            self.assertIn('xm_rehearsal.owner_guard',sql);self.assertIn(s.owner,sql);self.assertIn(s.project,sql);self.assertIn(s.guard['nonce'],sql)
            for token in ('INSERT INTO','UPDATE ','DELETE ','CREATE ','ALTER ','COPY ','TRUNCATE ','/run/rehearsal'):
                self.assertNotIn(token,sql)
            if name=='seed-blocked-invoice-snapshot':
                self.assertIn(SUB_USER,sql);self.assertIn(NEW_USER,sql);self.assertIn(LOT,sql);self.assertIn(SUB,sql)
                self.assertIn('invoice_user_id=',sql);self.assertIn('fl.id=',sql)
                for kind,user,source,lot in [('sub2api',SUB_USER,SUB,LOT),('newapi',NEW_USER,NEW,NEW_LOT)]:
                    branch=sql.split("'"+kind+"',(SELECT json_build_object",1)[1].split(' FROM funding_lots fl ',1)
                    self.assertIn("'reserved_minor',fl.reserved_minor",branch[0])
                    self.assertIn("'consumed_cash_minor',fl.consumed_cash_minor",branch[0])
                    self.assertIn("WHERE fl.id='"+lot+"' AND fl.source_instance_id='"+source+"' AND fl.invoice_user_id='"+user+"'",branch[1])
                self.assertIn('platform_user_id=oidc_subject',sql)
                return SimpleNamespace(returncode=0,stdout=json.dumps(result).encode())
            self.assertEqual(name,'seed-blocked-invoice-snapshot-guard-after')
            return SimpleNamespace(returncode=0,stdout=b'')
        s.driver=SimpleNamespace(config={'mode':'server-rehearsal'},docker=['docker'],command=command)
        return s,targets,result,calls,checks

    def test_owned_readonly_snapshot_returns_minimal_public_shape(self):
        s,_,expected,calls,checks=self.fixture()
        self.assertEqual(s.blocked_invoice_snapshot(SUB,LOT),expected)
        self.assertEqual(len(checks),2)
        self.assertEqual([x[0] for x in calls],['seed-blocked-invoice-snapshot','seed-blocked-invoice-snapshot-guard-after'])
        self.assertTrue(all('input_bytes' in x[2] for x in calls))

    def test_other_source_lot_or_unseeded_mode_rejected_before_query(self):
        for change,args in [(lambda s:None,(NEW,LOT)),(lambda s:None,(SUB,NEW_USER)),(lambda s:setattr(s,'seeded',False),(SUB,LOT)),
                            (lambda s:s.driver.config.update(mode='local-synthetic'),(SUB,LOT)),(lambda s:s.guard.update(owner='e'*32),(SUB,LOT)),
                            (lambda s:s.guard.update(nonce='bad'),(SUB,LOT))]:
            s,_,_,calls,_=self.fixture();change(s)
            with self.subTest(args=args),self.assertRaises(m.OperatorError):s.blocked_invoice_snapshot(*args)
            self.assertEqual(calls,[])

    def test_restored_database_drift_rejected_before_query(self):
        s,targets,_,calls,_=self.fixture();targets['invoice']['expected']['container_id']='f'*64
        with self.assertRaises(m.OperatorError):s.blocked_invoice_snapshot(SUB,LOT)
        self.assertEqual(calls,[])

    def test_database_drift_after_snapshot_is_rejected(self):
        s,targets,_,_,_=self.fixture();calls=0
        def changed():
            nonlocal calls
            calls+=1
            if calls==2:targets['invoice']['identity']['oid']+=1
            return copy.deepcopy(targets)
        s.database_targets=changed
        with self.assertRaises(m.OperatorError):s.blocked_invoice_snapshot(SUB,LOT)
        self.assertEqual(calls,2)

    def test_other_user_projection_or_lot_response_cannot_escape(self):
        for change in [lambda r:r['users'].update(other={'invoice_requests':0}),lambda r:r['lots']['sub2api'].update(id=NEW_USER),
                       lambda r:r['lots']['sub2api'].update(source_instance_id=NEW),lambda r:r['users']['sub2api'].update(invoice_requests=True),
                       lambda r:r['lots']['sub2api'].update(reserved_minor=-1),lambda r:r['lots']['newapi'].update(id=LOT),
                       lambda r:r['lots']['newapi'].update(source_instance_id=SUB),lambda r:r['lots']['newapi'].pop('reserved_minor')]:
            s,_,result,_,_=self.fixture();change(result)
            with self.assertRaises(m.OperatorError):s.blocked_invoice_snapshot(SUB,LOT)

    def test_owner_marker_failure_after_snapshot_is_not_accepted(self):
        s,_,_,_,_=self.fixture();original=s.driver.command
        def command(name,args,**kwargs):
            if name.endswith('guard-after'):raise m.OperatorError('fixture marker changed')
            return original(name,args,**kwargs)
        s.driver.command=command
        with self.assertRaises(m.OperatorError):s.blocked_invoice_snapshot(SUB,LOT)


if __name__=='__main__':unittest.main()

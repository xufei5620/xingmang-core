import copy
import hashlib
import unittest
from identity_audit import assess

S='11111111-1111-4111-8111-111111111111'
U='22222222-2222-4222-8222-222222222222'
ORIGIN='https://console.example.invalid'
OLD='https://idp.example.invalid/realms/staff'

def pair_hash(issuer,subject):
    return hashlib.sha256((issuer+'\n'+subject).encode()).hexdigest()

def fixture():
    platform={'schema':'xingmang.identity-audit.platform/v2','transaction_read_only':'on','database':'platform_audit',
              'admin_role':'admin','admin_role_total':1,'invoice_admin_total':1,
              'invoice_admin_totp_registered':1,'invoice_admin_login_ready_total':1,
              'role_scope_map':{'admin':['finance.read']},'finance_read_total':1,'finance_read_totp_registered':1,
              'finance_read_enabled_total':1,'finance_read_enabled_totp_registered':1,
              'staff':[{'id':S,'roles':['admin'],'disabled':False,'totp_registered':True,'must_change_password':False,
                        'must_enroll_totp':False,'locked':False,'finance_read':True}]}
    invoice={'schema':'xingmang.identity-audit.invoice/v1','transaction_read_only':'on','database':'invoice_audit',
             'identities':[{'id':U,'issuer':ORIGIN,'subject':S,'status':'active','platform':None,'operation_refs':1}],
             'operation_refs':[{'id':U,'source':'invoice_requests.reviewed_by','count':1}],'migration_records':[],
             'unresolved_admin_audit_actors':0}
    mapping={'schema':'xingmang.identity-audit.crosswalk/v1','legacy_issuers':[OLD],'entries':[]}
    return platform,invoice,mapping

class IdentityAuditTests(unittest.TestCase):
    def test_exact_console_pair_keeps_invoice_id(self):
        p,i,m=fixture(); result=assess(p,i,m,ORIGIN)
        self.assertEqual(result['finance_read_total'],1)
        self.assertEqual(result['rows'][0]['status'],'CONTINUOUS_CONSOLE_ID')
        self.assertEqual(result['rows'][0]['invoice_user_id'],U)
        self.assertEqual(result['blockers'],[])

    def test_unmapped_keycloak_is_not_email_guessed(self):
        p,i,m=fixture(); i['identities'][0].update(issuer=OLD,subject='legacy-one')
        result=assess(p,i,m,ORIGIN)
        self.assertIn('UNMAPPED_HISTORICAL_ACTOR',result['blockers'])

    def test_explicit_crosswalk_is_attribution_not_login_migration(self):
        p,i,m=fixture(); i['identities'][0].update(issuer=OLD,subject='legacy-one')
        m['entries']=[{'invoice_user_id':U,'legacy_issuer':OLD,'legacy_subject':'legacy-one','staff_id':S,'evidence_reference':'owner-approved-001'}]
        result=assess(p,i,m,ORIGIN)
        self.assertIn('LOGIN_IDENTITY_NOT_CONTINUOUS',result['blockers'])
        self.assertEqual(result['rows'][0]['status'],'ATTRIBUTION_ONLY')

    def test_historical_migration_record_must_bind_same_user_and_both_hashes(self):
        for changed in ['object_id','before_hash','after_hash']:
            with self.subTest(changed=changed):
                p,i,m=fixture(); m['entries']=[{'invoice_user_id':U,'legacy_issuer':OLD,'legacy_subject':'legacy-one','staff_id':S,'evidence_reference':'owner-approved-001'}]
                witness={'id':'33333333-3333-4333-8333-333333333333','object_id':U,'before_hash':pair_hash(OLD,'legacy-one'),'after_hash':pair_hash(ORIGIN,S)}
                witness[changed]='wrong'; i['migration_records']=[witness]
                self.assertIn('MIGRATION_EVIDENCE_MISMATCH',assess(p,i,m,ORIGIN)['blockers'])

    def test_exact_record_proves_preexisting_rebind_without_writes(self):
        p,i,m=fixture(); m['entries']=[{'invoice_user_id':U,'legacy_issuer':OLD,'legacy_subject':'legacy-one','staff_id':S,'evidence_reference':'owner-approved-001'}]
        i['migration_records']=[{'id':'33333333-3333-4333-8333-333333333333','object_id':U,'before_hash':pair_hash(OLD,'legacy-one'),'after_hash':pair_hash(ORIGIN,S)}]
        before=copy.deepcopy((p,i,m)); result=assess(p,i,m,ORIGIN)
        self.assertEqual(result['rows'][0]['status'],'HISTORICAL_REBIND_RECORDED')
        self.assertEqual(result['blockers'],[]); self.assertEqual((p,i,m),before)

    def test_duplicate_mapping_is_a_conflict_even_same_target(self):
        p,i,m=fixture(); row={'invoice_user_id':U,'legacy_issuer':OLD,'legacy_subject':'legacy-one','staff_id':S,'evidence_reference':'owner-approved-001'}
        m['entries']=[row,dict(row)]
        with self.assertRaises(ValueError): assess(p,i,m,ORIGIN)

    def test_effective_override_not_hardcoded_admin(self):
        p,i,m=fixture(); p['role_scope_map']={'admin':['registry.read'],'finance-reader':['finance.read']}
        p['staff'][0]['finance_read']=False
        p.update(finance_read_total=0,finance_read_totp_registered=0,finance_read_enabled_total=0,finance_read_enabled_totp_registered=0)
        with self.assertRaisesRegex(ValueError,'ADMIN_ROLE_MUST_HAVE_FINANCE_READ'):assess(p,i,m,ORIGIN)

    def test_forged_effective_scope_count_rejected(self):
        p,i,m=fixture(); p['finance_read_total']=0
        with self.assertRaises(ValueError): assess(p,i,m,ORIGIN)

    def test_unregistered_or_login_blocked_staff_reported(self):
        for flag in ['totp_registered','must_change_password','must_enroll_totp','locked','disabled']:
            with self.subTest(flag=flag):
                p,i,m=fixture(); p['staff'][0][flag]=False if flag=='totp_registered' else True
                if flag=='totp_registered':p.update(finance_read_totp_registered=0,finance_read_enabled_totp_registered=0)
                if flag=='disabled':p.update(finance_read_enabled_total=0,finance_read_enabled_totp_registered=0)
                p['invoice_admin_login_ready_total']=0
                if flag=='totp_registered':p['invoice_admin_totp_registered']=0
                self.assertTrue(assess(p,i,m,ORIGIN)['staff_followup'])

    def test_role_union_deduplicates_staff_and_trims_role(self):
        p,i,m=fixture(); p['staff'][0]['roles']=[' admin ','admin']
        self.assertEqual(assess(p,i,m,ORIGIN)['finance_read_total'],1)

    def test_go_unicode_trimspace_contract(self):
        p,i,m=fixture(); p['staff'][0]['roles']=['\u3000admin\u0085']
        p.update(admin_role_total=0,invoice_admin_total=0,invoice_admin_totp_registered=0,invoice_admin_login_ready_total=0)
        self.assertEqual(assess(p,i,m,ORIGIN)['finance_read_total'],1)
        p['staff'][0]['roles']=['\x1cadmin']
        p['staff'][0]['finance_read']=False
        p.update(finance_read_total=0,finance_read_totp_registered=0,finance_read_enabled_total=0,finance_read_enabled_totp_registered=0)
        self.assertEqual(assess(p,i,m,ORIGIN)['finance_read_total'],0)

    def test_force_query_origin_rejected_like_runtime(self):
        p,i,m=fixture()
        with self.assertRaises(ValueError):assess(p,i,m,ORIGIN+'?')

    def test_orphan_historical_actor_rejected(self):
        p,i,m=fixture(); i['identities']=[]
        self.assertIn('ORPHAN_HISTORICAL_ACTOR',assess(p,i,m,ORIGIN)['blockers'])

    def test_unknown_staff_or_customer_identity_cannot_map(self):
        for which in ['staff','customer']:
            with self.subTest(which=which):
                p,i,m=fixture()
                if which=='staff':
                    p['staff']=[]
                    p.update(finance_read_total=0,finance_read_totp_registered=0,finance_read_enabled_total=0,finance_read_enabled_totp_registered=0,
                             admin_role_total=0,invoice_admin_total=0,invoice_admin_totp_registered=0,invoice_admin_login_ready_total=0)
                else:i['identities'][0]['platform']='newapi'
                self.assertTrue(assess(p,i,m,ORIGIN)['blockers'])

    def test_reads_must_be_read_only_and_two_databases_distinct(self):
        for which in ['readonly','same_database']:
            with self.subTest(which=which):
                p,i,m=fixture()
                if which=='readonly':p['transaction_read_only']='off'
                else:i['database']=p['database']
                with self.assertRaises(ValueError):assess(p,i,m,ORIGIN)

    def test_rejects_unknown_sensitive_input_fields(self):
        p,i,m=fixture(); p['staff'][0]['totp_secret_ref']='must-not-be-accepted'
        with self.assertRaises(ValueError):assess(p,i,m,ORIGIN)

    def test_admin_audit_uuid_references_are_reconciled_without_body(self):
        p,i,m=fixture(); i['operation_refs'][0]['source']='audit_events.admin_actor'
        self.assertEqual(assess(p,i,m,ORIGIN)['blockers'],[])

    def test_unresolved_admin_audit_actors_require_owner_review(self):
        p,i,m=fixture(); i['unresolved_admin_audit_actors']=1
        self.assertIn('UNRESOLVED_ADMIN_AUDIT_ACTOR',assess(p,i,m,ORIGIN)['blockers'])

    def test_finance_scope_without_exact_admin_role_is_not_login_coverage(self):
        p,i,m=fixture();p['staff'][0]['roles']=[' admin ']
        p.update(admin_role_total=0,invoice_admin_total=0,invoice_admin_totp_registered=0,invoice_admin_login_ready_total=0)
        result=assess(p,i,m,ORIGIN)
        self.assertEqual(result['finance_read_total'],1)
        self.assertEqual(result['role_policy']['invoice_admin_total'],0)
        self.assertIn('NO_LOGIN_READY_INVOICE_ADMIN',result['blockers'])

    def test_joint_sql_count_must_match_actual_rows(self):
        for name in ['admin_role_total','invoice_admin_total','invoice_admin_totp_registered','invoice_admin_login_ready_total']:
            with self.subTest(name=name):
                p,i,m=fixture();p[name]=2
                with self.assertRaisesRegex(ValueError,'JOINT_ROLE_COUNT_MISMATCH'):assess(p,i,m,ORIGIN)

if __name__=='__main__':unittest.main()

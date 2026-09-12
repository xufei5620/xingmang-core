"""Read-only metadata/crosswalk audit. Never connects to databases or changes identities."""
import argparse
import hashlib
import json
from pathlib import Path
import uuid
from urllib.parse import urlsplit
from role_policy import COUNTS, census, explicit_policy

GO_SPACE='\u0009\u000a\u000b\u000c\u000d\u0020\u0085\u00a0\u1680\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200a\u2028\u2029\u202f\u205f\u3000'


def strict_object(pairs):
    result = {}
    for key,value in pairs:
        if key in result:
            raise ValueError('DUPLICATE_JSON_KEY')
        result[key] = value
    return result


def read_json(path):
    return json.loads(Path(path).read_text(encoding='utf-8-sig'), object_pairs_hook=strict_object)


def shape(value, required, optional=()):
    if not isinstance(value,dict) or set(value)-set(required)-set(optional) or set(required)-set(value):
        raise ValueError('UNEXPECTED_METADATA_SHAPE')


def uid(value):
    if not isinstance(value,str) or str(uuid.UUID(value)) != value:
        raise ValueError('INVALID_UUID')
    return value


def count(value):
    if type(value) is not int or value<0:
        raise ValueError('INVALID_COUNT')
    return value


def exact_https(value, origin=False):
    if not isinstance(value,str) or any(c.isspace() for c in value) or any(c in value for c in ['\\','#','*']):
        raise ValueError('INVALID_IDENTITY_ISSUER')
    url=urlsplit(value)
    if url.scheme!='https' or not url.netloc or url.username or url.password or '?' in value or url.fragment or (origin and url.path):
        raise ValueError('INVALID_IDENTITY_ISSUER')


def role_map(value):
    if not isinstance(value,dict) or not value:
        raise ValueError('INVALID_EFFECTIVE_ROLE_MAP')
    for role,scopes in value.items():
        if not isinstance(role,str) or not role or role.strip()!=role or not isinstance(scopes,list) or not scopes:
            raise ValueError('INVALID_EFFECTIVE_ROLE_MAP')
        if any(not isinstance(s,str) or not s or s.strip()!=s for s in scopes):
            raise ValueError('INVALID_EFFECTIVE_ROLE_MAP')
    return value


def pair_hash(issuer,subject):
    return hashlib.sha256((issuer+'\n'+subject).encode()).hexdigest()


def assess(platform, invoice, mapping, origin):
    exact_https(origin,True)
    shape(platform,{'schema','database','transaction_read_only','role_scope_map','admin_role',*COUNTS,'finance_read_total','finance_read_totp_registered','finance_read_enabled_total','finance_read_enabled_totp_registered','staff'},{'captured_at'})
    shape(invoice,{'schema','database','transaction_read_only','identities','operation_refs','migration_records','unresolved_admin_audit_actors'},{'captured_at'})
    shape(mapping,{'schema','legacy_issuers','entries'})
    if platform['schema']!='xingmang.identity-audit.platform/v2' or invoice['schema']!='xingmang.identity-audit.invoice/v1' or mapping['schema']!='xingmang.identity-audit.crosswalk/v1':
        raise ValueError('SCHEMA_MISMATCH')
    if platform['transaction_read_only']!='on' or invoice['transaction_read_only']!='on' or not platform['database'] or not invoice['database'] or platform['database']==invoice['database']:
        raise ValueError('DATABASE_OR_READONLY_MISMATCH')
    roles=role_map(platform['role_scope_map'])
    explicit_policy(platform['admin_role'],roles)
    staff={}; followup=[]
    for person in platform['staff']:
        shape(person,{'id','roles','disabled','must_change_password','must_enroll_totp','locked','totp_registered','finance_read'})
        key=uid(person['id'])
        if key in staff: raise ValueError('DUPLICATE_STAFF')
        if not isinstance(person['roles'],list) or any(not isinstance(r,str) for r in person['roles']): raise ValueError('INVALID_STAFF_ROLES')
        if any(type(person[f]) is not bool for f in ['disabled','must_change_password','must_enroll_totp','locked','totp_registered','finance_read']): raise ValueError('INVALID_STAFF_FLAGS')
        effective=any('finance.read' in roles.get(r.strip(GO_SPACE),[]) for r in person['roles'])
        if effective!=person['finance_read']:raise ValueError('EFFECTIVE_SCOPE_MISMATCH')
        staff[key]=person
        if effective:
            flags=[f for f in ['disabled','must_change_password','must_enroll_totp','locked'] if person[f]]
            if not person['totp_registered']:flags.append('totp_not_registered')
            if flags:followup.append({'staff_id':key,'flags':flags})
    expected={
        'finance_read_total':sum(p['finance_read'] for p in staff.values()),
        'finance_read_totp_registered':sum(p['finance_read'] and p['totp_registered'] for p in staff.values()),
        'finance_read_enabled_total':sum(p['finance_read'] and not p['disabled'] for p in staff.values()),
        'finance_read_enabled_totp_registered':sum(p['finance_read'] and not p['disabled'] and p['totp_registered'] for p in staff.values()),
    }
    if any(count(platform[k])!=v for k,v in expected.items()):raise ValueError('COVERAGE_COUNT_MISMATCH')
    joint=census(platform['admin_role'],roles,list(staff.values()))
    if any(count(platform[k])!=joint[k] for k in COUNTS):raise ValueError('JOINT_ROLE_COUNT_MISMATCH')
    issuers=mapping['legacy_issuers']
    if not isinstance(issuers,list) or len(issuers)!=len(set(issuers)):raise ValueError('INVALID_LEGACY_ISSUERS')
    for issuer in issuers:
        exact_https(issuer)
        if issuer==origin:raise ValueError('LEGACY_IS_CURRENT_ORIGIN')
    entries={}; pairs=set()
    for entry in mapping['entries']:
        shape(entry,{'invoice_user_id','legacy_issuer','legacy_subject','staff_id','evidence_reference'})
        user=uid(entry['invoice_user_id']); uid(entry['staff_id'])
        pair=(entry['legacy_issuer'],entry['legacy_subject'])
        if user in entries or pair in pairs:raise ValueError('DUPLICATE_OR_CONFLICTING_MAPPING')
        if pair[0] not in issuers or not isinstance(pair[1],str) or not pair[1] or len(pair[1])>512 or any(ord(c)<32 for c in pair[1]):raise ValueError('INVALID_LEGACY_PAIR')
        if not isinstance(entry['evidence_reference'],str) or not entry['evidence_reference'].strip():raise ValueError('MISSING_OWNER_EVIDENCE')
        entries[user]=entry; pairs.add(pair)
    identities={}; tuples=set()
    for identity in invoice['identities']:
        shape(identity,{'id','issuer','subject','status','platform','operation_refs'})
        key=uid(identity['id']); pair=(identity['issuer'],identity['subject']); exact_https(pair[0]); count(identity['operation_refs'])
        if key in identities or pair in tuples:raise ValueError('DUPLICATE_INVOICE_IDENTITY')
        if not isinstance(pair[1],str) or not pair[1]:raise ValueError('INVALID_INVOICE_SUBJECT')
        identities[key]=identity; tuples.add(pair)
    references={}; blockers=[]
    if not joint['invoice_admin_login_ready_total']:blockers.append('NO_LOGIN_READY_INVOICE_ADMIN')
    if joint['invoice_admin_totp_registered']!=joint['invoice_admin_total']:blockers.append('INVOICE_ADMIN_TOTP_INCOMPLETE')
    if count(invoice['unresolved_admin_audit_actors']):blockers.append('UNRESOLVED_ADMIN_AUDIT_ACTOR')
    allowed_refs={'invoice_requests.reviewed_by','invoice_requests.issued_by','invoice_documents.uploaded_by','payment_candidate_reviews.admin_id','payment_candidate_decisions.proposed_by','payment_candidate_decisions.approved_by','eligibility_freezes.resolved_by','audit_events.admin_actor'}
    for reference in invoice['operation_refs']:
        shape(reference,{'id','source','count'}); key=uid(reference['id']); n=count(reference['count'])
        if reference['source'] not in allowed_refs:raise ValueError('UNKNOWN_ACTOR_REFERENCE')
        references[key]=references.get(key,0)+n
        if key not in identities:blockers.append('ORPHAN_HISTORICAL_ACTOR')
    for key,identity in identities.items():
        if identity['operation_refs']!=references.get(key,0):raise ValueError('ACTOR_REFERENCE_COUNT_MISMATCH')
    for witness in invoice['migration_records']:
        shape(witness,{'id','object_id','before_hash','after_hash'},{'created_at'})
    rows=[]
    for key,identity in identities.items():
        entry=entries.get(key); current=(identity['issuer'],identity['subject']); status='UNCLASSIFIED_UNUSED_LEGACY_IDENTITY'; target=None
        if identity['platform'] is not None:
            status='CUSTOMER_IDENTITY_AS_STAFF_ACTOR'; blockers.append(status)
        elif identity['issuer']==origin:
            target=identity['subject']
            if target not in staff:status='UNKNOWN_STAFF'; blockers.append(status)
            else:status='CONTINUOUS_CONSOLE_ID'
            if entry:
                matching=[w for w in invoice['migration_records'] if w['object_id']==key and w['before_hash']==pair_hash(entry['legacy_issuer'],entry['legacy_subject']) and w['after_hash']==pair_hash(origin,entry['staff_id'])]
                if entry['staff_id']!=target or len(matching)!=1:
                    status='MIGRATION_EVIDENCE_MISMATCH'; blockers.append(status)
                elif target in staff:status='HISTORICAL_REBIND_RECORDED'
        elif entry:
            target=entry['staff_id']
            if current!=(entry['legacy_issuer'],entry['legacy_subject']):status='MAPPING_SOURCE_MISMATCH'; blockers.append(status)
            elif target not in staff:status='UNKNOWN_STAFF'; blockers.append(status)
            else:status='ATTRIBUTION_ONLY'; blockers.append('LOGIN_IDENTITY_NOT_CONTINUOUS')
        elif identity['operation_refs']>0:
            status='UNMAPPED_HISTORICAL_ACTOR'; blockers.append(status)
        rows.append({'invoice_user_id':key,'issuer':current[0],'subject':current[1],'staff_id':target,'operation_refs':identity['operation_refs'],'status':status})
    if set(entries)-set(identities):blockers.append('MAPPING_IDENTITY_NOT_FOUND')
    return {'schema':'xingmang.identity-audit.result/v2',**expected,'role_policy':joint,'staff_followup':followup,'rows':rows,
            'blockers':sorted(set(blockers)),'crosswalk_is_read_only_attribution':True,'runtime_reads_crosswalk':False,
            'writes_performed':False,'statement':'No identity, foreign key, email AAD or historical actor has been changed.'}


def main():
    parser=argparse.ArgumentParser()
    for name in ['platform','invoice','crosswalk','staff-origin','output']:
        parser.add_argument('--'+name,required=True)
    args=parser.parse_args()
    try:
        result=assess(read_json(args.platform),read_json(args.invoice),read_json(args.crosswalk),args.staff_origin)
        result['input_sha256']={name:hashlib.sha256(Path(getattr(args,name)).read_bytes()).hexdigest() for name in ['platform','invoice','crosswalk']}
        with Path(args.output).open('x',encoding='utf-8') as stream:json.dump(result,stream,ensure_ascii=False,indent=2)
        return 2 if result['blockers'] else 0
    except (ValueError,OSError) as error:
        print('identity audit rejected input ('+type(error).__name__+'); no changes applied')
        return 1


if __name__=='__main__':raise SystemExit(main())

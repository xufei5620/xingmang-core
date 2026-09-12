"""Five production post-cutover checks; no human credentials or login claims."""
import hashlib
import json
from pathlib import Path
from lifecycle import NAME, atomic_json, local_postgres_exec, read_public_json, require, require_ready, utc
from smoke import Client, OLD_ROUTES, origin, loopback_target

REQUIRED=('readyz.modules','client.pages','legacy.rejected','queues.no-dead','ledger.top')


def validate_config(config,mode):
    require(set(config)<= {'schema','mode','origins','connect_to','ca_file'} and set(config)>={'schema','mode','origins'},'public smoke rejects credential/config extensions')
    require(config['schema']=='xingmang.unified.public-smoke/v1' and config['mode']==mode,'public smoke mode/schema mismatch')
    require(set(config['origins'])=={'admin','user'},'both public origins required')
    for value in config['origins'].values():origin(value)
    if 'connect_to' in config:
        require(set(config['connect_to'])=={'admin','user'},'both explicit transports required')
        for value in config['connect_to'].values():loopback_target(value)


def sql(driver,domain,operation,query):
    target=driver.config['candidate']['databases'][domain]
    require(set(target)=={'project','service','owner','database'} and all(isinstance(value,str) and NAME.fullmatch(value) for value in target.values()),'exact local database descriptor required')
    project=next(p for p in driver.projects('candidate') if p['name']==target['project'])
    text="BEGIN READ ONLY; SET LOCAL statement_timeout='30s'; "+query+'; COMMIT;'
    raw=driver.compose(project,operation,local_postgres_exec(target['service'],compose=True)+['psql','-X','-qAt','-w','-v','ON_ERROR_STOP=1','-h','/var/run/postgresql','-p','5432','-U',target['owner'],'-d',target['database'],'-c',text]).stdout
    value=json.loads(raw)
    require(value.get('database')==target['database'] and value.get('transaction_read_only')=='on','public query database or read-only state differs')
    return value


def require_no_dead(value,database):
    require(value.get('database')==database and value.get('transaction_read_only')=='on','wrong no-dead database')
    for key in ('source_ingest_dead','eligibility_dead'):
        require(type(value.get(key)) is int and value[key]==0,'invoice dead queue is nonempty or count invalid')


def run(driver,config):
    result={'schema':'xingmang.unified.public-smoke-result/v1','status':'FAIL','exit_code':1,'mode':driver.config['mode'],
        'source_head':driver.config['candidate']['head'],'start_utc':utc(),'steps':[],'http':[],
        'human_login_verified':False,'human_login_action':'owner verifies actual customer and staff login after cutover',
        'financial_writes_permitted':False,'authentication_sessions_created':False,
        'configuration_sha256':hashlib.sha256(json.dumps(config,sort_keys=True,separators=(',',':')).encode()).hexdigest()}
    def step(name,action):
        row={'name':name,'start_utc':utc(),'status':'FAIL','exit_code':1};result['steps'].append(row)
        try:row['evidence']=action();row.update(status='PASS',exit_code=0)
        finally:row['end_utc']=utc()
    try:
        validate_config(config,driver.config['mode'])
        clients={role:Client(value,config.get('ca_file'),result['http'],config.get('connect_to',{}).get(role)) for role,value in config['origins'].items()}
        def readiness():
            evidence={role:client.call('GET','/readyz') for role,client in clients.items()}
            for payload in evidence.values():
                require_ready(payload)
                require(payload.get('invoice',{}).get('ready') is True,'original invoice report is not ready')
            return evidence
        step(REQUIRED[0],readiness)
        def pages():
            for role,path in (('user','/'),('admin','/finance')):
                body,media=clients[role].raw('GET',path)
                require(media=='text/html' and b'<html' in body.lower(),'public application shell unavailable')
            return {'customer_shell':True,'staff_shell':True,'authenticated_business_access_verified':False}
        step(REQUIRED[1],pages)
        def retired():
            for method,path in OLD_ROUTES:
                clients['admin'].denied(method,path,{} if method=='POST' else None,expected=(404,405,410))
            return {'rejected_routes':len(OLD_ROUTES)}
        step(REQUIRED[2],retired)
        def queues():
            value=sql(driver,'invoice','public-no-dead',"SELECT json_build_object('database',current_database(),'transaction_read_only',current_setting('transaction_read_only'),'source_ingest_dead',(SELECT count(*) FROM source_ingest_events WHERE processing_status='dead'),'eligibility_dead',(SELECT count(*) FROM eligibility_projection_jobs WHERE status='dead'))")
            require_no_dead(value,driver.config['candidate']['databases']['invoice']['database']);return value
        step(REQUIRED[3],queues)
        def ledgers():
            expected=read_public_json(driver.state/'deployment-record.json')['snapshot']['ledger_hashes']
            actual=driver.ledger_snapshot('candidate')
            require(actual==expected,'migration ledger differs from actual pre-cutover snapshot')
            rows={}
            for domain,top in (('platform','(SELECT version::text FROM public.schema_migrations ORDER BY version DESC LIMIT 1)'),('invoice','(SELECT name FROM schema_migrations ORDER BY name DESC LIMIT 1)')):
                dirty='(SELECT bool_or(dirty) FROM public.schema_migrations)' if domain=='platform' else 'false'
                row=sql(driver,domain,'public-ledger-top-'+domain,"SELECT json_build_object('database',current_database(),'transaction_read_only',current_setting('transaction_read_only'),'migration_top',"+top+",'dirty',"+dirty+")")
                require(row.get('migration_top') not in (None,'') and row.get('dirty') is False,'empty or dirty migration ledger')
                rows[domain]=row
            return {'tops':rows,'ledger_hashes':actual,'matches_pre_cutover':True}
        step(REQUIRED[4],ledgers)
        require([row['name'] for row in result['steps']]==list(REQUIRED),'public smoke incomplete')
        result.update(status='PASS',exit_code=0)
    except BaseException:
        result.update(status='FAIL',exit_code=1)
        raise
    finally:
        result['end_utc']=utc();atomic_json(driver.output/'smoke.json',result)
    return result

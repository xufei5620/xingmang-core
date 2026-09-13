"""Attested D-only synthetic identities. Private material lives in Docker tmpfs.

The host only handles generated values in memory. A separately owned helper
and tmpfs volume are never included in the eighteen production-shaped runtimes.
"""
import base64
import copy
import hashlib
import ipaddress
import json
import os
from pathlib import Path
import re
import secrets
import subprocess
import time
import uuid

from lifecycle import OperatorError, NAME, atomic_json, local_postgres_exec, plain_path, read_public_json, require, require_postgres_socket_unshadowed, utc

CONTAINER_FORMAT=('{"id":{{json .Id}},"image_id":{{json .Image}},"running":{{json .State.Running}},'
    '"project":{{json (index .Config.Labels "com.docker.compose.project")}},'
    '"service":{{json (index .Config.Labels "com.docker.compose.service")}},'
    '"owner":{{json (index .Config.Labels "xingmang.rehearsal.owner")}},'
    '"privileged":{{json .HostConfig.Privileged}},"readonly_rootfs":{{json .HostConfig.ReadonlyRootfs}},'
    '"cap_add":{{json .HostConfig.CapAdd}},"cap_drop":{{json .HostConfig.CapDrop}},'
    '"mounts":{{json .Mounts}},"networks":{{json .NetworkSettings.Networks}}}')
PRIVATE='/run/rehearsal'


def validate_scope(config,value):
    require(config['mode'] in ('server-rehearsal','local-synthetic'),'seed requires an actual frozen rehearsal mode')
    require(re.fullmatch('[0-9a-f]{32}',value['owner_id']),'seed owner must be random 32hex')
    expected={p['name'] for p in value['projects']};actual={p['name'] for p in config['candidate']['projects']}
    require(len(expected)==2 and actual==expected and all(name.startswith('xm-rehearsal-') for name in actual), 'seed may target only both frozen projects')
    require(not actual.intersection(p['name'] for p in config['previous']['projects']), 'seed overlaps original projects')
    spec=value['seed'];require(set(spec)=={'network_cidr','provider_ip','api_ip','port','admin_role'},'seed public layout fields differ')
    net=ipaddress.ip_network(spec['network_cidr'],strict=True)
    require(net.version==4 and net.prefixlen>=24,'seed network must be a narrowly scoped IPv4 network')
    addresses=[ipaddress.ip_address(spec[k]) for k in ('provider_ip','api_ip')]
    require(len(set(addresses))==2 and all(ip in net and ip not in (net.network_address,net.broadcast_address) for ip in addresses),'seed requires two exact distinct internal addresses')
    require(type(spec['port']) is int and 1024<spec['port']<65536 and NAME.fullmatch(spec['admin_role']),'invalid seed listener or role')


def require_database(actual,expected,owner,volumes,networks):
    require_postgres_socket_unshadowed(actual['mounts'])
    require(actual.get('running') is True and all(actual.get(key)==expected[key] for key in ('image_id','project','service')) and actual.get('id')==expected['container_id'] and actual.get('owner')==owner,'actual frozen database container identity differs')
    mounts=[row for row in actual['mounts'] if row.get('Destination')=='/var/lib/postgresql' or row.get('Destination','').startswith('/var/lib/postgresql/')]
    require(len(mounts)==1 and mounts[0].get('Type')=='volume' and mounts[0].get('Name')==expected['volume'] and expected['volume'].startswith('xm-rehearsal-'),'database is not attached to its new frozen volume')
    require(all(not row.get('RW') or row.get('Type')=='tmpfs' or row in mounts for row in actual['mounts']), 'database has an additional writable mount')
    volume=volumes[expected['volume']]
    require(volume.get('Name')==expected['volume'] and volume.get('Labels',{}).get('xingmang.rehearsal.owner')==owner,'database volume ownership differs')
    require(actual['networks'] and set(actual['networks'])==set(networks),'database network proof incomplete')
    require(all(name.startswith('xm-rehearsal-') and row.get('Internal') is True and row.get('Labels',{}).get('xingmang.rehearsal.owner')==owner for name,row in networks.items()),'database can reach an unowned or egress network')


class Seed:
    def __init__(self,driver,value):
        validate_scope(driver.config,value)
        self.driver,self.value=driver,value
        self.owner=value['owner_id'];self.spec=value['seed'];self.image=value['tools_image']
        self.project='xm-rehearsal-'+self.owner[:20]+'-seed'
        self.volume=self.project+'-private';self.network=self.project+'-auth';self.name=self.project+'-helper'
        self.cid=None;self.created_volume=False;self.created_network=False;self.seeded=False
        self.private_paths=set();self.guard=None;self.summary={}
        self.record={'schema':'xingmang.rehearsal-seed/v1','mode':driver.config['mode'],'owner':self.owner,
            'project':self.project,'private_volume':self.volume,'network':self.network,'synthetic_only':True,
            'real_customer_login_verified':False,'private_host_files_created':False,'private_material_possible':False,'start_utc':utc(),'status':'PREPARING'}
        self.save()

    def save(self):
        try:atomic_json(self.driver.output/'seed-result.json',self.record)
        except OSError:
            if not getattr(self, '_cleanup_active', False):raise
            self._cleanup_log_error=True

    def command(self,name,args,**kwargs):
        if getattr(self, '_cleanup_active', False):
            return self.cleanup_command(name,self.driver.docker+args,**kwargs)
        return self.driver.command('seed-'+name,self.driver.docker+args,**kwargs)

    def cleanup_command(self,name,args,*,input_bytes=None,check=True):
        # Logging failures cannot prevent identity checks or private erasure.
        # They are remembered and still prohibit a successful D result.
        start=utc()
        try:result=subprocess.run(args,input=input_bytes,capture_output=True,env=self.driver.env,timeout=60)
        except subprocess.TimeoutExpired:result=subprocess.CompletedProcess(args,124,b'',b'')
        event={'operation':'seed-cleanup-'+name,'start_utc':start,'end_utc':utc(),'exit_code':result.returncode,
            'stdout_sha256':hashlib.sha256(result.stdout).hexdigest(),'stderr_sha256':hashlib.sha256(result.stderr).hexdigest()}
        try:atomic_json(self.driver.output/'events'/(str(time.time_ns())+'-seed-cleanup-'+name+'.json'),event)
        except OSError:self._cleanup_log_error=True
        if check:require(result.returncode==0,'seed cleanup command failed')
        return result

    def inspect(self,target):return json.loads(self.command('inspect-container',['inspect','--type','container',target,'--format',CONTAINER_FORMAT]).stdout)

    def assert_helper(self):
        actual=self.inspect(self.cid or self.name)
        require(actual['id']==self.cid and actual['image_id']==self.image and actual['project']==self.project and actual['service']=='fixture-provider' and actual['owner']==self.owner and actual['running'] is True,'seed helper ownership or image changed')
        require(set(actual['networks'])=={self.network} and actual['networks'][self.network]['IPAddress']==self.spec['provider_ip'],'seed helper network or address changed')
        network=json.loads(self.command('helper-network-owner',['network','inspect',self.network]).stdout)[0]
        require(network.get('Internal') is True and network.get('Labels',{}).get('xingmang.rehearsal.owner')==self.owner and network.get('Id')==actual['networks'][self.network]['NetworkID'],'seed helper network is no longer the actual owned internal network')
        caps=lambda values:{value.removeprefix('CAP_') for value in values or []}
        require(actual.get('privileged') is False and actual.get('readonly_rootfs') is True and caps(actual.get('cap_drop'))=={'ALL'} and caps(actual.get('cap_add'))=={'CHOWN','DAC_READ_SEARCH'},'seed helper privilege boundary changed')
        mounts=[m for m in actual['mounts'] if m.get('Destination')==PRIVATE]
        require(len(mounts)==1 and mounts[0].get('Name')==self.volume,'seed helper private tmpfs mount changed')

    def private_command(self,operation,args,body=None):
        self.assert_helper()
        start=utc()
        # Deliberately no stdout hash or bytes are persisted for private values.
        argv=self.driver.docker+['exec','-i','--user','0',self.cid,*args]
        try:
            completed=subprocess.run(argv,input=body,capture_output=True,env=self.driver.env,timeout=60)
        except subprocess.TimeoutExpired:
            completed=subprocess.CompletedProcess(argv,124,b'',b'')
        try:
            atomic_json(self.driver.output/'events'/(str(time.time_ns())+'-seed-private-'+operation+'.json'),
                {'operation':'seed-private-'+operation,'start_utc':start,'end_utc':utc(),'exit_code':completed.returncode,'private_output_persisted':False})
        except OSError:
            if not getattr(self, '_cleanup_active', False):raise
            self._cleanup_log_error=True
        require(completed.returncode==0,'private seed helper operation failed')
        return completed.stdout

    def write_private(self,name,body):
        require(re.fullmatch('[a-zA-Z0-9_.-]+',name),'invalid private fixture filename')
        self.private_command('write', ['/bin/sh','-c','set -euC; umask 077; test ! -e "$1"; test ! -L "$1"; cat > "$1"','seed',PRIVATE+'/'+name],body)
        self.private_paths.add(PRIVATE+'/'+name)

    def read_credential(self,path):
        require(self.seeded and path in self.private_paths,'credential is outside the attested seed set')
        raw=self.private_command('read-credential',['/bin/cat',path])
        require(0<len(raw)<=65536,'private credential size invalid')
        return raw.decode('utf-8').removesuffix('\n').removesuffix('\r')

    def check_credential(self,path):
        require(self.seeded and path in self.private_paths,'credential is outside the attested seed set')
        self.private_command('stat-credential',['/bin/sh','-c','test -f "$1" && test ! -L "$1" && test -s "$1"','seed',path])

    def prepare(self):
        d=self.driver
        for kind,name in (('volume',self.volume),('network',self.network),('container',self.name)):
            require(self.command('must-be-new',[kind,'inspect',name],check=False).returncode!=0,'seed auxiliary resource already exists')
        # Journal intended exact names before the first create, including partial failures.
        self.record['auxiliary_names']=[self.volume,self.network,self.name];self.save()
        self.command('create-private-volume',['volume','create','--driver','local','--opt','type=tmpfs','--opt','device=tmpfs','--opt','o=size=32m,uid=0,gid=0,mode=0700','--label','xingmang.rehearsal.owner='+self.owner,'--label','com.docker.compose.project='+self.project,self.volume]);self.created_volume=True
        self.command('create-internal-network',['network','create','--internal','--subnet',self.spec['network_cidr'],'--label','xingmang.rehearsal.owner='+self.owner,'--label','com.docker.compose.project='+self.project,self.network]);self.created_network=True
        unified=next(p for p in d.projects('candidate') if p['kind']=='unified')
        resolved=json.loads(d.compose(unified,'seed-original-config',['config','--format','json']).stdout)
        require(resolved['services']['platform-api']['environment'].get('ADMIN_ROLE')==self.spec['admin_role'],'seed role differs from actual preflight ADMIN_ROLE')
        keyfile=resolved.get('secrets',{}).get('invoice_field_keyring',{}).get('file')
        require(isinstance(keyfile,str) and keyfile,'original invoice keyring file input missing')
        plain_path(keyfile)  # Metadata only: reject links/sockets without opening the key.
        # Docker consumes this existing opaque key path; neither Python nor logs read its contents.
        created=self.command('create-helper',['create','--pull','never','--name',self.name,'--label','xingmang.rehearsal.owner='+self.owner,'--label','com.docker.compose.project='+self.project,'--label','com.docker.compose.service=fixture-provider',
            '--network',self.network,'--ip',self.spec['provider_ip'],'--user','0','--read-only','--cap-drop','ALL','--cap-add','CHOWN','--cap-add','DAC_READ_SEARCH','--security-opt','no-new-privileges:true',
            '--env','FIELD_KEYRING_FILE=/run/field-keyring',
            '--mount','type=volume,source='+self.volume+',target='+PRIVATE,'--mount','type=bind,source='+keyfile+',target=/run/field-keyring,readonly',
            '--entrypoint','/bin/sh',self.image,'-c','exec sleep 86400'])
        self.cid=created.stdout.decode().strip();require(re.fullmatch('[0-9a-f]{64}',self.cid),'helper creation did not return a full ID')
        self.command('start-helper',['start',self.cid]);self.assert_helper()
        require(self.private_command('statfs',['stat','-f','-c','%T',PRIVATE]).strip()==b'tmpfs','seed credential volume is not actual tmpfs')
        require(self.private_command('directory-owner-mode',['stat','-c','%u:%a',PRIVATE]).strip()==b'0:700','seed private directory owner/mode differs')
        volume=json.loads(self.command('private-volume-metadata',['volume','inspect',self.volume]).stdout)[0]
        require(volume.get('Driver')=='local' and volume.get('Options',{}).get('type')=='tmpfs' and volume.get('Labels',{}).get('xingmang.rehearsal.owner')==self.owner,'tmpfs volume ownership/type differs')
        if d.config['mode']=='server-rehearsal':
            require(os.name=='posix','server rehearsal must execute on the local Linux host')
            mount=json.loads(d.command('seed-host-findmnt',['/usr/bin/findmnt','-J','-T',volume['Mountpoint'],'-o','FSTYPE']).stdout)
            require(mount.get('filesystems') and mount['filesystems'][0]['fstype']=='tmpfs','server private volume is not host tmpfs')
        self.record.update(helper_container_id=self.cid,tmpfs_verified=True,private_directory_mode='0700',private_directory_uid=0)
        # Sources come from the restored DB later; TLS generation only needs two distinct guard UUIDs now.
        self.hosts={kind:kind+'.rehearsal.invalid' for kind in ('sub2api','newapi')}
        self.guard={'schema_version':1,'mode':d.config['mode'],'owner':self.owner,'project':self.project,'bind_ip':self.spec['provider_ip'],'network_cidr':self.spec['network_cidr'],'client_source_ids':{kind:str(uuid.uuid4()) for kind in self.hosts}}
        self.write_private('provider-guard.json',json.dumps(self.guard).encode())
        self.private_command('create-staff-secret-subdirectory',['/bin/mkdir',PRIVATE+'/staff-secrets'])
        flags=['--fixture-guard',PRIVATE+'/provider-guard.json','--listen',self.spec['provider_ip']+':'+str(self.spec['port']),'--owner',self.owner,'--project',self.project]
        self.record['private_material_possible']=True;self.save()
        self.private_command('generate-tls',['/usr/local/bin/invoice-rehearsal-auth-provider','--generate-tls','--tls-directory',PRIVATE,'--tls-hosts',','.join(self.hosts.values()),*flags])
        ca=d.output/'rehearsal-provider-ca.pem';ca.parent.mkdir(parents=True,exist_ok=True)
        ca.write_bytes(self.private_command('read-public-ca',['/bin/cat',PRIVATE+'/public-ca.pem']))
        # A public overlay changes only D. Original platform secrets are not copied into the private fixture volume.
        overlay={'services':{'platform-api':{'environment':{'SUB2API_LOGIN_BASE_URL':'https://'+self.hosts['sub2api']+':'+str(self.spec['port']),
            'NEWAPI_LOGIN_BASE_URL':'https://'+self.hosts['newapi']+':'+str(self.spec['port']),'SSL_CERT_FILE':'/config/rehearsal-provider-ca.pem','XM_SECRET_ROOT':'/run/xm/secrets'},
            'extra_hosts':[host+':'+self.spec['provider_ip'] for host in self.hosts.values()],
            'networks':{'rehearsal_auth':{'ipv4_address':self.spec['api_ip']}},
            'volumes':[{'type':'volume','source':'rehearsal_private','target':'/run/xm/secrets','read_only':True,'volume':{'subpath':'staff-secrets','nocopy':True}},
                       {'type':'bind','source':str(ca),'target':'/config/rehearsal-provider-ca.pem','read_only':True}]}},
            'networks':{'rehearsal_auth':{'name':self.network,'external':True}},'volumes':{'rehearsal_private':{'name':self.volume,'external':True}}}
        path=d.output/'seed-overlay.json';atomic_json(path,overlay);unified['compose_files'].append(str(path))
        self.value.setdefault('readonly_input_volumes',[]).append({'name':self.volume,'owner_id':self.owner,'kind':'staff_credentials'})
        d.config['rehearsal']['readonly_input_volumes']=self.value['readonly_input_volumes']
        d.config['host_preflight']['planned_networks'].append({'name':self.network,'cidr':self.spec['network_cidr'],'internal':True})
        self.record.update(status='TMPFS_AND_D_OVERLAY_PREPARED',overlay_sha256=hashlib.sha256(path.read_bytes()).hexdigest());self.save()

    def database_targets(self):
        targets={}
        for domain,row in self.driver.config['candidate']['databases'].items():
            project=next(p for p in self.driver.projects('candidate') if p['name']==row['project'])
            cid=self.driver.compose(project,'seed-actual-db-'+domain,['ps','-q',row['service']]).stdout.decode().strip()
            require(re.fullmatch('[0-9a-f]{64}',cid),'one actual frozen database container required')
            expected={'container_id':cid,'image_id':project['services'][row['service']]['image_id'],'project':project['name'],'service':row['service'],'volume':self.value['volumes'][domain+'_database']}
            actual=self.inspect(cid)
            volume=json.loads(self.command('db-volume-'+domain,['volume','inspect',expected['volume']]).stdout)[0]
            networks={name:json.loads(self.command('db-network-'+domain,['network','inspect',name]).stdout)[0] for name in actual['networks']}
            require_database(actual,expected,self.owner,{expected['volume']:volume},networks)
            for key in ('owner','database'):require(NAME.fullmatch(row[key]),'invalid database descriptor')
            command=local_postgres_exec(cid)+['psql','-X','-qAt','-w','-v','ON_ERROR_STOP=1','-h','/var/run/postgresql','-p','5432','-U',row['owner'],'-d',row['database']]
            query=b"BEGIN READ ONLY; SELECT json_build_object('name',current_database(),'oid',(SELECT oid::bigint FROM pg_catalog.pg_database WHERE datname=current_database()),'server_addr',inet_server_addr()::text,'server_port',inet_server_port()); COMMIT;"
            identity=json.loads(self.command('db-identity-'+domain,command,input_bytes=query).stdout)
            require(identity.get('name')==row['database'] and type(identity.get('oid')) is int and identity['oid']>0,'actual pg_database identity differs')
            require('server_addr' in identity and 'server_port' in identity and identity['server_addr'] is None and identity['server_port'] is None,'fixed local PostgreSQL connection unexpectedly used TCP')
            targets[domain]={'expected':expected,'identity':identity,'command':command}
        require(set(targets)=={'platform','invoice'} and len({r['expected']['container_id'] for r in targets.values()})==2,'both distinct actual frozen DBs required')
        return targets

    def seed(self):
        targets=self.database_targets()  # Verify BOTH complete targets before the first write.
        if hasattr(self,'database_restore_targets'):
            require(targets==self.database_restore_targets,'seed SQL targets differ from attested restore targets')
        source_query=b"BEGIN READ ONLY; SELECT json_agg(json_build_object('id',id,'type',source_type) ORDER BY source_type) FROM source_instances WHERE enabled; COMMIT;"
        rows=json.loads(self.command('source-identities',targets['invoice']['command'],input_bytes=source_query).stdout)
        require(isinstance(rows,list) and len(rows)==2 and {r['type'] for r in rows}==set(self.hosts),'exact two restored enabled source IDs required')
        sources={r['type']:{'id':r['id'],'issuer':'https://'+self.hosts[r['type']]+':'+str(self.spec['port'])} for r in rows}
        staff_id=str(uuid.uuid4());data={'schema_version':1,'clients':{},'staff':{'id':staff_id,'username':'xm-rehearsal-'+self.owner[:16],
            'password':secrets.token_urlsafe(32),'totp':base64.b32encode(secrets.token_bytes(20)).decode().rstrip('='),'admin_role':self.spec['admin_role']}}
        for kind in self.hosts:
            email=kind+'-'+self.owner[:16]+'@rehearsal.invalid'
            data['clients'][kind]={'identifier':email if kind=='sub2api' else 'xm-rehearsal-'+self.owner[:16],
                'password':secrets.token_urlsafe(32),'subject':str(900000000000+secrets.randbelow(99999999999)),'email':email}
        self.guard.update(nonce=secrets.token_hex(32),databases={key:value['identity'] for key,value in targets.items()},sources=sources,client_source_ids={key:row['id'] for key,row in sources.items()})
        self.write_private('guard.json',json.dumps(self.guard).encode());self.write_private('private.json',json.dumps(data).encode())
        for kind,row in [('staff',data['staff']),*data['clients'].items()]:self.write_private(kind+'-password',row['password'].encode())
        self.write_private('expected-email',data['clients']['sub2api']['email'].encode())
        self.private_command('write-totp',['/bin/sh','-c','set -euC; umask 077; mkdir /run/rehearsal/staff-secrets/staff-totp; test ! -e "$1"; cat > "$1"; chown -R 10001:10001 /run/rehearsal/staff-secrets','seed',PRIVATE+'/staff-secrets/staff-totp/'+staff_id],data['staff']['totp'].encode())
        self.private_paths.add(PRIVATE+'/staff-secrets/staff-totp/'+staff_id)
        for domain,target in targets.items():
            fresh=self.database_targets()[domain];require(fresh==target,'frozen target changed before seed marker')
            marker=("BEGIN; DO $$ BEGIN IF current_database() <> '"+target['identity']['name']+"' OR (SELECT oid FROM pg_database WHERE datname=current_database()) <> "+str(target['identity']['oid'])+" THEN RAISE EXCEPTION 'wrong frozen database'; END IF; END $$; CREATE SCHEMA xm_rehearsal; CREATE TABLE xm_rehearsal.owner_guard(owner text,project text,nonce text); INSERT INTO xm_rehearsal.owner_guard VALUES ('"+self.owner+"','"+self.project+"','"+self.guard['nonce']+"'); COMMIT;").encode()
            self.command('mark-'+domain,target['command'],input_bytes=marker)
            sql=self.private_command('build-private-sql',['/usr/local/bin/rehearsal-seed','--input',PRIVATE+'/private.json','--guard',PRIVATE+'/guard.json','--domain',domain,'--summary',PRIVATE+'/summary-'+domain+'.json'],None)
            # No SQL or derived credential hashes are written to the host's records.
            fresh=self.database_targets()[domain];require(fresh==target,'frozen target changed before SQL seed')
            self.command('apply-'+domain,target['command'],input_bytes=sql)
            self.summary[domain]=json.loads(self.private_command('read-summary',['/bin/cat',PRIVATE+'/summary-'+domain+'.json']))
        self.seeded=True
        config=read_public_json(self.value['smoke_config'])
        config.update(schema='xingmang.unified.smoke/v1',mode=self.driver.config['mode'],credentials={
            'staff':{'username':data['staff']['username'],'password_file':PRIVATE+'/staff-password','totp_file':PRIVATE+'/staff-secrets/staff-totp/'+staff_id},
            **{kind:{'identifier':row['identifier'],'password_file':PRIVATE+'/'+kind+'-password','source_id':sources[kind]['id']} for kind,row in data['clients'].items()}})
        config['credentials']['sub2api'].update(existing_profile_id=self.summary['invoice']['clients']['sub2api']['existing_profile_id'],expected_email_file=PRIVATE+'/expected-email')
        config['expected']={'required_staff_role':self.spec['admin_role'],'request_amount_minor':20000,'minimum_available_minor':20000}
        path=self.driver.output/'seed-smoke.json';atomic_json(path,config)
        self.driver.config['smoke_config']=str(path);self.value['smoke_config']=str(path);self.driver.config['rehearsal']['smoke_config']=str(path)
        flags=['--fixtures',PRIVATE+'/private.json','--fixture-guard',PRIVATE+'/guard.json','--listen',self.spec['provider_ip']+':'+str(self.spec['port']),
            '--owner',self.owner,'--project',self.project,'--tls-cert',PRIVATE+'/provider-cert.pem','--tls-key',PRIVATE+'/provider-key.pem']
        self.command('start-provider',['exec','-d','--user','0',self.cid,'/usr/local/bin/invoice-rehearsal-auth-provider',*flags])
        self.record.update(status='SYNTHETIC_DATA_INSERTED',sources=sources,summary=self.summary,real_business_amounts_verified=False,db_targets={k:v['expected'] for k,v in targets.items()});self.save()

    def blocked_invoice_snapshot(self,source_id,lot_id):
        require(self.seeded is True and self.driver.config['mode']=='server-rehearsal',
                'blocked-write snapshot requires the seeded server rehearsal')
        guard=copy.deepcopy(self.guard)
        require(isinstance(guard,dict) and guard.get('mode')=='server-rehearsal'
                and guard.get('owner')==self.owner and guard.get('project')==self.project
                and re.fullmatch('[0-9a-f]{32}',self.owner) and NAME.fullmatch(self.project)
                and re.fullmatch('[0-9a-f]{64}',guard.get('nonce','')),
                'blocked-write seed ownership guard differs')
        clients=self.summary.get('invoice',{}).get('clients',{})
        require(isinstance(clients,dict) and set(clients)=={'sub2api','newapi'},'both seeded user summaries required')
        sub_user,new_user=clients['sub2api'].get('user_id'),clients['newapi'].get('user_id')
        identifier=re.compile('[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}')
        require(all(isinstance(value,str) and identifier.fullmatch(value) for value in (source_id,lot_id,sub_user,new_user))
                and sub_user!=new_user and source_id==guard['sources']['sub2api']['id']
                and lot_id==clients['sub2api'].get('lot_id'),'blocked-write snapshot may inspect only this seeded SUB lot and users')
        new_source,new_lot=guard['sources']['newapi']['id'],clients['newapi'].get('lot_id')
        require(all(isinstance(value,str) and identifier.fullmatch(value) for value in (new_source,new_lot))
                and new_source!=source_id and new_lot!=lot_id,'blocked-write NEW source and known lot must be distinct')
        scoped=(('sub2api',sub_user,source_id,lot_id),('newapi',new_user,new_source,new_lot))
        restored=copy.deepcopy(getattr(self,'database_restore_targets',None))
        require(isinstance(restored,dict) and set(restored)=={'platform','invoice'},'attested restore database targets required')
        before=self.database_targets()
        require(before==restored and all(before[k]['identity']==guard['databases'][k] for k in restored),
                'blocked-write database targets differ from attested restored seed targets')
        identity=before['invoice']['identity']
        require(NAME.fullmatch(identity['name']) and type(identity['oid']) is int and identity['oid']>0
                and identity['server_addr'] is None and identity['server_port'] is None,
                'blocked-write database identity must use the attested local socket')
        check=("DO $snapshot$ BEGIN IF NOT (current_database()='"+identity['name']+"' "
            "AND (SELECT oid::bigint FROM pg_catalog.pg_database WHERE datname=current_database())="+str(identity['oid'])+" "
            "AND inet_server_addr() IS NOT DISTINCT FROM NULL::inet "
            "AND inet_server_port() IS NOT DISTINCT FROM NULL::integer) "
            "THEN RAISE EXCEPTION 'blocked-write database identity mismatch'; END IF; "
            "IF (SELECT count(*) FROM xm_rehearsal.owner_guard)<>1 OR NOT EXISTS "
            "(SELECT 1 FROM xm_rehearsal.owner_guard WHERE owner='"+self.owner+"' AND project='"+self.project+"' AND nonce='"+guard['nonce']+"') "
            "THEN RAISE EXCEPTION 'blocked-write owner marker mismatch'; END IF; END; $snapshot$;\n")
        lot_queries=[]
        for kind,user,source,known_lot in scoped:
            lot_sql=("(SELECT json_build_object('id',fl.id,'source_instance_id',fl.source_instance_id,"
                "'consumed_cash_minor',fl.consumed_cash_minor,'reserved_minor',fl.reserved_minor,'issued_minor',fl.issued_minor,"
                "'verified_cash_minor',fl.verified_cash_minor,'verification',fl.verification_state,"
                "'refund_frozen',fl.refund_frozen,'eligibility_kind',fl.eligibility_kind) FROM funding_lots fl "
                "WHERE fl.id='"+known_lot+"' AND fl.source_instance_id='"+source+"' AND fl.invoice_user_id='"+user+"')")
            lot_queries.append("'"+kind+"',"+lot_sql)
        query=("DO $users$ BEGIN IF (SELECT count(*) FROM invoice_users WHERE "
            "((id='"+sub_user+"' AND platform='sub2api') OR (id='"+new_user+"' AND platform='newapi')) "
            "AND status='active' AND platform_user_id=oidc_subject)<>2 "
            "THEN RAISE EXCEPTION 'blocked-write seeded user projection mismatch'; END IF; END; $users$;\n"
            "SELECT json_build_object('users',json_build_object("
            "'sub2api',json_build_object('invoice_requests',(SELECT count(*) FROM invoice_requests WHERE invoice_user_id='"+sub_user+"')),"
            "'newapi',json_build_object('invoice_requests',(SELECT count(*) FROM invoice_requests WHERE invoice_user_id='"+new_user+"'))),"
            "'lots',json_build_object("+','.join(lot_queries)+"));\n")
        # Both transactions are read-only. The second sees a fresh snapshot of
        # the owner marker; a repeat inside the first snapshot would not prove
        # that a concurrently changed marker still matches after the query.
        begin='BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY;\n'
        raw=self.command('blocked-invoice-snapshot',before['invoice']['command'],input_bytes=(begin+check+query+'COMMIT;\n').encode()).stdout
        self.command('blocked-invoice-snapshot-guard-after',before['invoice']['command'],input_bytes=(begin+check+'COMMIT;\n').encode())
        after=self.database_targets()
        require(after==before==restored and self.guard==guard,'blocked-write database or seed guard changed after snapshot')
        try:result=json.loads(raw)
        except (ValueError,UnicodeDecodeError):raise OperatorError('blocked-write snapshot response is not valid JSON') from None
        fields={'id','source_instance_id','consumed_cash_minor','reserved_minor','issued_minor','verified_cash_minor','verification','refund_frozen','eligibility_kind'}
        require(isinstance(result,dict) and set(result)=={'users','lots'} and isinstance(result['users'],dict)
                and set(result['users'])=={'sub2api','newapi'},'blocked-write snapshot user scope differs')
        require(all(isinstance(row,dict) and set(row)=={'invoice_requests'} and type(row['invoice_requests']) is int
                and row['invoice_requests']>=0 for row in result['users'].values()),'blocked-write snapshot request counts invalid')
        require(isinstance(result['lots'],dict) and set(result['lots'])=={'sub2api','newapi'},'both seeded lots required in snapshot')
        for kind,user,source,known_lot in scoped:
            lot=result['lots'][kind]
            require(isinstance(lot,dict) and set(lot)==fields and lot['id']==known_lot and lot['source_instance_id']==source
                    and all(type(lot[key]) is int and lot[key]>=0 for key in ('consumed_cash_minor','reserved_minor','issued_minor','verified_cash_minor'))
                    and type(lot['refund_frozen']) is bool and lot['verification'] in ('pending','verified','frozen')
                    and lot['eligibility_kind'] in ('WALLET_CASH','SUBSCRIPTION_CASH','NON_CASH','LEGACY_NON_INVOICEABLE'),
                    'blocked-write snapshot lot scope or amounts invalid')
        return result

    def cleanup(self):
        self._cleanup_active=True;self._cleanup_log_error=False
        outcome={'private_shredded':False,'helper_removed':False,'tmpfs_removed':False,'network_removed':False}
        errors=[];volume=None;helper_safe=False
        try:
            found=self.command('cleanup-volume-inspect',['volume','inspect',self.volume],check=False)
            if found.returncode==0:
                volume=json.loads(found.stdout)[0]
                require(volume.get('Name')==self.volume and volume.get('Driver')=='local' and volume.get('Options',{}).get('type')=='tmpfs' and volume.get('Labels',{}).get('xingmang.rehearsal.owner')==self.owner,'private volume owner/type changed before cleanup')
        except BaseException:errors.append('TMPFS_OWNERSHIP_UNPROVEN');volume=None
        try:
            found=self.command('cleanup-helper-inspect',['inspect','--type','container',self.name,'--format',CONTAINER_FORMAT],check=False)
            if found.returncode==0:
                actual=json.loads(found.stdout)
                require(actual['owner']==self.owner and actual['project']==self.project and actual['service']=='fixture-provider' and actual['image_id']==self.image and (self.cid is None or actual['id']==self.cid),'helper ownership changed before cleanup')
                self.cid=actual['id']
                # Do not restart/remount a stopped helper and then misreport an
                # empty replacement tmpfs as four-pass erasure of old material.
                require(actual['running'] is True,'tmpfs holder stopped before private erasure could be verified')
                self.assert_helper();helper_safe=True
        except BaseException:errors.append('HELPER_OWNERSHIP_OR_LIVE_HOLD_UNPROVEN')
        if volume is not None:
            try:
                if self.record.get('private_material_possible',True) is False:
                    outcome['private_material_never_created']=True
                else:
                    require(helper_safe,'no live attested holder for original private tmpfs')
                    self.private_command('restore-private-file-owner',['/bin/sh','-c','set -eu; if [ -d /run/rehearsal/staff-secrets ]; then chown 0:0 /run/rehearsal/staff-secrets; chown -R 0:0 /run/rehearsal/staff-secrets; fi'])
                    guard=PRIVATE+('/guard.json' if self.seeded else '/provider-guard.json')
                    before=self.private_command('guard-inode-before-wipe',['stat','-c','%d:%i',guard]).decode().strip()
                    self.private_command('shred',['/usr/local/bin/invoice-rehearsal-auth-provider','--shred-fixtures',PRIVATE,'--fixture-guard',guard,'--listen',self.spec['provider_ip']+':'+str(self.spec['port']),'--owner',self.owner,'--project',self.project])
                    outcome.update(private_shredded=True,erasure_method='four-pass-wipe-while-original-helper-holds-tmpfs',guard_device_inode=before)
            except BaseException:errors.append('PRIVATE_SHRED_FAILED')
        try:
            release_allowed=outcome['private_shredded'] or outcome.get('private_material_never_created',False)
            if helper_safe and release_allowed:
                # This also removes the provider process's in-memory credentials.
                # It is strictly AFTER the wipe, before releasing the final tmpfs mount.
                self.command('remove-helper',['rm','-f',self.cid]);outcome['helper_removed']=True
            elif not self.cid:outcome['helper_removed']=True
            elif helper_safe:outcome['holder_retained_for_wipe_retry']=True
        except BaseException:errors.append('HELPER_CLEANUP_FAILED')
        try:
            if volume is not None and (outcome['private_shredded'] or outcome.get('private_material_never_created')):
                self.command('remove-private-volume',['volume','rm',self.volume]);outcome['tmpfs_removed']=True
            elif volume is None and not self.created_volume:outcome['tmpfs_removed']=True
        except BaseException:errors.append('TMPFS_CLEANUP_FAILED')
        try:
            if outcome.get('holder_retained_for_wipe_retry'):
                raise OperatorError('keep the original holder network for a controlled wipe retry')
            found=self.command('cleanup-network-inspect',['network','inspect',self.network],check=False)
            if found.returncode==0:
                network=json.loads(found.stdout)[0]
                require(network.get('Name')==self.network and network.get('Internal') is True and network.get('Labels',{}).get('xingmang.rehearsal.owner')==self.owner,'seed network owner changed before cleanup')
                self.command('remove-auth-network',['network','rm',self.network])
            outcome['network_removed']=True
        except BaseException:errors.append('NETWORK_CLEANUP_FAILED')
        self.record.update(cleanup=outcome,cleanup_errors=errors,end_utc=utc());self.save()
        if self._cleanup_log_error:
            errors.append('CLEANUP_AUDIT_WRITE_FAILED');self.save()
        require(not errors and outcome['tmpfs_removed'],'seed private material or auxiliary cleanup incomplete')
        return outcome

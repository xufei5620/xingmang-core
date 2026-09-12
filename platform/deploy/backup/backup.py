"""Versioned platform lifecycle backup. No arbitrary commands, SQL or hooks."""
import argparse
import datetime as dt
import hashlib
import io
import json
import os
from pathlib import Path
import re
import signal
import stat
import subprocess
import sys
import tarfile
import threading
import time
import uuid

PRINCIPAL='platform-backup'
NAMESPACE='solov-platform-backup-v1'
WRITERS=('platform-api','platform-worker')
QUERIES={
    'schema-migrations.csv':'SELECT version,dirty FROM public.schema_migrations ORDER BY version',
    'river-migrations.csv':'SELECT line,version,created_at FROM public.river_migration ORDER BY line,version',
}
META=('{{json .Id}}|{{json .Image}}|{{json .State.Running}}|{{json .State.Paused}}|{{json .State.Restarting}}|'
      '{{json (index .Config.Labels "com.docker.compose.project")}}|{{json (index .Config.Labels "com.docker.compose.service")}}|'
      '{{with index .State "Health"}}{{json .Status}}{{else}}"none"{{end}}')

class BackupError(RuntimeError): pass
def require(ok,code):
    if not ok:raise BackupError(code)
def utc():return dt.datetime.now(dt.timezone.utc).isoformat()
def sha(body):return hashlib.sha256(body).hexdigest()
def canonical(value):return json.dumps(value,sort_keys=True,separators=(',',':')).encode()
def file_sha(path):
    result=hashlib.sha256()
    with Path(path).open('rb') as stream:
        for block in iter(lambda:stream.read(1024*1024),b''):result.update(block)
    return result.hexdigest()
def sync_directory(path):
    if os.name=='posix':
        descriptor=os.open(path,os.O_RDONLY|os.O_DIRECTORY)
        try:os.fsync(descriptor)
        finally:os.close(descriptor)
def sync_file(path):
    if os.name=='posix':
        descriptor=os.open(path,os.O_RDONLY)
        try:os.fsync(descriptor)
        finally:os.close(descriptor)
def plain(value,*,directory=False):
    path=Path(value)
    require(path.is_absolute() and '..' not in path.parts,'ABSOLUTE_REVIEWED_PATH_REQUIRED')
    require(not any(p.is_symlink() or (hasattr(p,'is_junction') and p.is_junction()) for p in [path,*path.parents]),'PATH_LINK_FORBIDDEN')
    require(path.is_dir() if directory else path.is_file(),'REVIEWED_PATH_MISSING')
    return path
def write_json(path,value):
    temp=Path(str(path)+'.new-'+uuid.uuid4().hex)
    with temp.open('xb') as stream:stream.write(canonical(value)+b'\n');stream.flush();os.fsync(stream.fileno())
    os.replace(temp,path)
    sync_directory(Path(path).parent)
def unique(pairs):
    value={}
    for key,item in pairs:
        require(key not in value,'DUPLICATE_CONFIG_KEY');value[key]=item
    return value

def validate_config(config):
    keys={'schema','mode','docker','tools','database','writers','backup_dir','recipient_file','signing_key_file',
          'allowed_signers_file','restore_identity_file','quiesce_confirmed'}
    require(isinstance(config,dict) and set(config)==keys and config['schema']=='xingmang.platform-backup/v1','CONFIG_SCHEMA_MISMATCH')
    require(config['mode'] in ('production','local-synthetic'),'INVALID_MODE')
    require(config['quiesce_confirmed'] is True,'QUIESCE_WINDOW_NOT_CONFIRMED')
    docker=config['docker'];db=config['database']
    require(set(docker)=={'binary','endpoint','config_dir'} and docker['endpoint'] in
        ('unix:///var/run/docker.sock','unix:///run/docker.sock','npipe:////./pipe/dockerDesktopLinuxEngine','npipe:////./pipe/docker_engine'),'LOCAL_DOCKER_REQUIRED')
    require(set(config['tools'])=={'age','ssh_keygen'},'UNEXPECTED_TOOL_OR_HOOK')
    require(set(db)=={'container_id','image_id','project','service','user','database','socket','port'},'DATABASE_DESCRIPTOR_REQUIRED')
    require(db['service']=='postgres' and db['socket']=='/var/run/postgresql' and db['port']==5432,'FIXED_POSTGRES_SOCKET_REQUIRED')
    require(all(re.fullmatch('[A-Za-z_][A-Za-z0-9_-]{0,62}',db[key]) for key in ('user','database','project')),'INVALID_DATABASE_NAMES')
    require(set(config['writers'])==set(WRITERS),'EXACT_WRITER_SET_REQUIRED')
    for role,row in [('postgres',db),*config['writers'].items()]:
        if role!='postgres':require(set(row)=={'container_id','image_id'},'WRITER_DESCRIPTOR_REQUIRED')
        require(re.fullmatch('[0-9a-f]{64}',row['container_id']) and re.fullmatch('sha256:[0-9a-f]{64}',row['image_id']),'FULL_CONTAINER_IMAGE_PINS_REQUIRED')
    require(len({row['container_id'] for row in [db,*config['writers'].values()]})==3,'CONTAINER_ROLES_MUST_BE_DISTINCT')
    if config['mode']=='production':
        require(os.name=='posix' and os.geteuid()==0 and docker['endpoint'].startswith('unix://'),'PRODUCTION_REQUIRES_ROOT_LOCAL_LINUX')
    else:require(db['project'].startswith('xm-backup-test-'),'SYNTHETIC_CANNOT_TARGET_PRODUCTION')
    return config

def perform_backup(driver):
    before=driver.preflight()
    driver.arm(before)
    failure=None
    try:
        driver.stop_writers(before)
        initial=driver.metadata('before')
        driver.dump_database()
        require(initial==driver.metadata('after'),'METADATA_CHANGED_DURING_BACKUP')
        driver.require_stopped()
        driver.encrypt_metadata(initial)
        driver.sign_components()
    except BaseException as error:failure=error
    try:
        driver.restore_writers(before)
    except BaseException:
        try:driver.record_result('RESTORE_FAILED',backup_failed=failure is not None)
        except OSError:pass
        raise BackupError('WRITER_RESTORATION_FAILED_KEEP_ACTIVE_JOURNAL') from None
    if getattr(driver,'journal_io_failed',False):
        failure=failure or BackupError('WRITERS_RESTORED_BUT_RECOVERY_LOG_WRITE_FAILED')
    try:driver.disarm()
    except OSError:failure=failure or BackupError('WRITERS_RESTORED_BUT_ACTIVE_JOURNAL_CLEAR_FAILED')
    if failure is not None:
        try:driver.record_result('FAILED',writers_restored=True)
        except OSError:pass
        raise failure
    try:result=driver.publish()
    except BaseException:
        driver.record_result('FAILED',writers_restored=True,publication_complete=False)
        raise
    driver.record_result('PASS',writers_restored=True,backup=result)
    return result

class BackupDriver:
    def __init__(self,config):
        self.config=validate_config(config);self.events=[];self.output=None
        self.base=plain(config['backup_dir'],directory=True)
        if config['mode']=='production':
            info=self.base.stat()
            require(info.st_uid==0 and stat.S_IMODE(info.st_mode)&0o077==0,'BACKUP_DIRECTORY_REQUIRES_ROOT_ONLY_ACCESS')
        self.config_sha=sha(canonical(config));self.active=self.base/'.platform-backup-active.json'
        self.start_utc=utc()
        self.docker=[str(plain(config['docker']['binary'])),'--config',str(plain(config['docker']['config_dir'],directory=True)),
                     '--host',config['docker']['endpoint']]
        self.env={**os.environ,'DOCKER_HOST':config['docker']['endpoint'],'SSH_ASKPASS_REQUIRE':'never'}
        self.env.pop('DISPLAY',None)
        self.lock=None
        self.recovering=False;self.journal_io_failed=False

    def flush_events(self):
        if self.output:
            try:write_json(self.output/'events.json',self.events)
            except OSError:
                if not self.recovering:raise
                self.journal_io_failed=True

    def acquire(self):
        path=self.base/'.platform-backup.lock'
        require(not path.is_symlink(),'LOCK_LINK_FORBIDDEN')
        self.lock=path.open('a+b')
        try:
            if os.name=='nt':
                import msvcrt
                if path.stat().st_size==0:self.lock.write(b'0');self.lock.flush()
                self.lock.seek(0);msvcrt.locking(self.lock.fileno(),msvcrt.LK_NBLCK,1)
            else:
                import fcntl
                fcntl.flock(self.lock,fcntl.LOCK_EX|fcntl.LOCK_NB)
        except OSError:self.lock.close();self.lock=None;raise BackupError('BACKUP_ALREADY_RUNNING') from None

    def release(self):
        if self.lock is not None:self.lock.close();self.lock=None

    def command(self,name,args,*,data=None,timeout=60):
        start=utc()
        try:
            feed={'input':data} if data is not None else {'stdin':subprocess.DEVNULL}
            p=subprocess.run(args,stdout=subprocess.PIPE,stderr=subprocess.PIPE,env=self.env,timeout=timeout,**feed)
            code,out,err=p.returncode,p.stdout,p.stderr
        except subprocess.TimeoutExpired as error:code,out,err=124,error.stdout or b'',error.stderr or b''
        self.events.append({'operation':name,'start_utc':start,'end_utc':utc(),'exit_code':code,
            'stdout_sha256':sha(out),'stdout_bytes':len(out),'stderr_sha256':sha(err),'stderr_bytes':len(err)})
        self.flush_events()
        require(code==0,'COMMAND_FAILED_'+name)
        return out

    def inspect(self,service,row,*,restoring=False):
        values=self.command('inspect-'+service,self.docker+['inspect','--type','container',row['container_id'],'--format',META]).decode().strip().split('|')
        require(len(values)==8,'INVALID_CONTAINER_METADATA')
        identifier,image,running,paused,restarting,project,actual_service,health=[json.loads(value) for value in values]
        require(identifier==row['container_id'] and image==row['image_id'] and project==self.config['database']['project'] and actual_service==service,'CONTAINER_IDENTITY_CHANGED')
        require(type(running) is bool and paused is False and restarting is False,'UNSTABLE_CONTAINER_STATE')
        require(health in ('none','healthy') or not running or restoring,'PREEXISTING_UNHEALTHY_WRITER')
        return {'running':running,'health':health,'container_id':identifier,'image_id':image}

    def db_args(self,program,args):
        db=self.config['database']
        return self.docker+['exec','--user','postgres','-i','-e',
            'PGOPTIONS=-c default_transaction_read_only=on -c statement_timeout=1800000',
            '-e','PGPASSFILE=/nonexistent','-e','PGHOSTADDR=','-e','PGPASSWORD=',db['container_id'],
            '/usr/bin/env','-u','PGSERVICE','-u','PGSERVICEFILE',program,'-h',db['socket'],'-p',str(db['port']),
            '-U',db['user'],'-d',db['database'],*args]

    def preflight(self):
        require(not self.active.exists(),'UNFINISHED_BACKUP_USE_RECOVER_WITH_ORIGINAL_CONFIG')
        stamp=dt.datetime.now(dt.timezone.utc).strftime('%Y%m%dT%H%M%SZ')
        self.prefix='platform-'+stamp
        self.output=self.base/'.platform-backup-runs'/(stamp+'-'+uuid.uuid4().hex)
        self.output.mkdir(parents=True,mode=0o700)
        sync_directory(self.output.parent);sync_directory(self.base)
        for path in self.config['tools'].values():plain(path)
        for name in ('recipient_file','allowed_signers_file','signing_key_file','restore_identity_file'):
            path=plain(self.config[name]);require(0<path.stat().st_size<=65536,'CRYPTO_INPUT_SIZE_INVALID')
            require(self.base!=path and self.base not in path.parents,'KEY_MATERIAL_CANNOT_LIVE_IN_BACKUP_DIRECTORY')
            if name in ('signing_key_file','restore_identity_file') and self.config['mode']=='production':
                info=path.stat();require(info.st_uid==0 and stat.S_IMODE(info.st_mode)==0o400,'PRIVATE_KEY_OWNER_MODE_INVALID')
        crypto=[Path(self.config[name]) for name in ('recipient_file','allowed_signers_file','signing_key_file','restore_identity_file')]
        require(all(not os.path.samefile(left,right) for i,left in enumerate(crypto) for right in crypto[i+1:]),'CRYPTO_INPUT_ROLES_MUST_BE_DISTINCT')
        signers=plain(self.config['allowed_signers_file']).read_text(encoding='utf-8')
        lines=[line for line in signers.splitlines() if line.strip() and not line.lstrip().startswith('#')]
        pattern=r'platform-backup\s+namespaces="solov-platform-backup-v1"\s+ssh-ed25519\s+[A-Za-z0-9+/=]+(?:\s+[^\r\n]*)?'
        require(lines and all(re.fullmatch(pattern,line) for line in lines),'WRONG_BACKUP_SIGNER_NAMESPACE')
        require(self.inspect('postgres',self.config['database'])['running'],'DATABASE_NOT_RUNNING')
        before={service:self.inspect(service,self.config['writers'][service]) for service in WRITERS}
        for service in ('postgres',*WRITERS):
            raw=self.command('service-members-'+service,self.docker+['ps','-aq','--no-trunc','--filter',
                'label=com.docker.compose.project='+self.config['database']['project'],'--filter','label=com.docker.compose.service='+service])
            expected=self.config['database'] if service=='postgres' else self.config['writers'][service]
            require(raw.decode().split()==[expected['container_id']],'UNREVIEWED_OR_DUPLICATE_SERVICE_CONTAINER')
        identity=self.command('database-identity',self.db_args('psql',['-X','-qAt','-w','-v','ON_ERROR_STOP=1','-c',
            "SELECT current_database(),current_user,current_setting('transaction_read_only')"])).decode().strip()
        require(identity==self.config['database']['database']+'|'+self.config['database']['user']+'|on','DATABASE_SOCKET_IDENTITY_MISMATCH')
        for suffix in ('.postgres.dump.age','.metadata.tar.age','.sha256','.sha256.sig'):
            require(not (self.base/(self.prefix+suffix)).exists(),'BACKUP_TIMESTAMP_COLLISION')
        challenge=self.output/'signer-check.txt';challenge.write_bytes(b'platform-backup-preflight\n')
        self.sign_and_verify(challenge,'preflight')
        self.encrypt_bytes('recipient-check',b'public-recipient-preflight',self.output/'recipient-check.age')
        return before

    def arm(self,before):
        snapshot=self.output/'writer-state-before.json'
        write_json(snapshot,before)
        value={'schema':'xingmang.platform-backup-active/v1','config_sha256':self.config_sha,'output':str(self.output),
               'before':before,'before_file_sha256':file_sha(snapshot),'created_utc':utc()}
        with self.active.open('xb') as stream:stream.write(canonical(value)+b'\n');stream.flush();os.fsync(stream.fileno())
        sync_directory(self.base)
        self.armed=value
        self.record_result('RUNNING')

    def disarm(self):
        require(json.loads(self.active.read_bytes())==self.armed,'ACTIVE_JOURNAL_CHANGED')
        self.active.unlink()
        sync_directory(self.base)

    def stop_writers(self,before):
        for service in WRITERS:
            row=self.config['writers'][service];current=self.inspect(service,row)
            require(current==before[service],'WRITER_CHANGED_BEFORE_STOP')
            if before[service]['running']:
                self.command('stop-'+service,self.docker+['stop','--timeout','30',row['container_id']])
        self.require_stopped()

    def require_stopped(self):
        require(self.inspect('postgres',self.config['database'])['running'],'DATABASE_STOPPED_DURING_BACKUP')
        for service in WRITERS:require(not self.inspect(service,self.config['writers'][service])['running'],'WRITER_STILL_RUNNING')

    def restore_writers(self,before):
        self.recovering=True
        try:self._restore_writers(before)
        finally:self.recovering=False

    def _restore_writers(self,before):
        failures=[]
        for service in reversed(WRITERS):
            try:
                row=self.config['writers'][service];current=self.inspect(service,row,restoring=True)
                if before[service]['running'] and not current['running']:
                    self.command('restore-'+service,self.docker+['start',row['container_id']])
                deadline=time.monotonic()+180
                while True:
                    current=self.inspect(service,row,restoring=True)
                    restored=(current['running']==before[service]['running'] and
                        (not before[service]['running'] or before[service]['health']!='healthy' or current['health']=='healthy'))
                    if restored:break
                    require(time.monotonic()<deadline,'WRITER_RESTORE_TIMEOUT')
                    time.sleep(1)
            except BaseException:failures.append(service)
        require(not failures,'WRITER_RESTORE_FAILED')
        after={service:self.inspect(service,self.config['writers'][service]) for service in WRITERS}
        try:write_json(self.output/'writer-state-after.json',after)
        except OSError:self.journal_io_failed=True

    def metadata(self,phase):
        result={}
        for name,query in QUERIES.items():
            raw=self.command('metadata-'+phase+'-'+name,self.db_args('psql',['-X','-q','-w','-v','ON_ERROR_STOP=1',
                '-c','COPY ('+query+') TO STDOUT WITH CSV HEADER']))
            require(raw.count(b'\n')>=2 and len(raw)<=4*1024*1024,'EMPTY_OR_OVERSIZE_MIGRATION_LEDGER')
            result[name]=raw
        return result

    def age_args(self,path):return [self.config['tools']['age'],'-R',self.config['recipient_file'],'-o',str(path)]

    def encrypt_bytes(self,name,body,path):
        require(not path.exists(),'REFUSE_ENCRYPTED_OUTPUT_OVERWRITE')
        self.command(name,self.age_args(path),data=body)
        require(path.is_file() and path.stat().st_size>0,'EMPTY_ENCRYPTED_OUTPUT')

    def dump_database(self):
        target=self.output/(self.prefix+'.postgres.dump.age')
        start=utc();stats={};processes=[];threads=[]
        def drain(key,stream):
            digest=hashlib.sha256();length=0
            for block in iter(lambda:stream.read(65536),b''):digest.update(block);length+=len(block)
            stream.close();stats[key]={'sha256':digest.hexdigest(),'bytes':length}
        try:
            encryptor=subprocess.Popen(self.age_args(target),stdin=subprocess.PIPE,stdout=subprocess.DEVNULL,stderr=subprocess.PIPE,env=self.env)
            processes.append(encryptor)
            producer=subprocess.Popen(self.db_args('pg_dump',['--format=custom','--no-owner','--no-acl','--no-password','--lock-wait-timeout=30000']),
                stdout=encryptor.stdin,stderr=subprocess.PIPE,stdin=subprocess.DEVNULL,env=self.env)
            processes.append(producer);encryptor.stdin.close()
            for name,process in [('producer_stderr',producer),('age_stderr',encryptor)]:
                thread=threading.Thread(target=drain,args=(name,process.stderr));thread.start();threads.append(thread)
            producer_code=producer.wait(timeout=1800);age_code=encryptor.wait(timeout=1800)
        except BaseException:
            for process in processes:
                if process.poll() is None:process.kill()
            for process in processes:process.wait()
            raise
        finally:
            for thread in threads:thread.join()
            codes={'producer_exit_code':processes[1].returncode if len(processes)>1 else None,
                   'consumer_exit_code':processes[0].returncode if processes else None}
            self.events.append({'operation':'pg-dump-to-age','start_utc':start,'end_utc':utc(),**codes,**stats})
            self.flush_events()
        require(producer_code==age_code==0,'PG_DUMP_OR_AGE_PIPELINE_FAILED')
        require(target.is_file() and target.stat().st_size>0,'EMPTY_DATABASE_CIPHERTEXT')

    def encrypt_metadata(self,files):
        body=io.BytesIO()
        with tarfile.open(fileobj=body,mode='w',format=tarfile.USTAR_FORMAT) as archive:
            for name,data in sorted(files.items()):
                entry=tarfile.TarInfo(name);entry.size=len(data);entry.mode=0o600;entry.uid=entry.gid=0
                archive.addfile(entry,io.BytesIO(data))
        self.encrypt_bytes('metadata-to-age',body.getvalue(),self.output/(self.prefix+'.metadata.tar.age'))

    def sign_and_verify(self,path,name):
        self.command(name+'-sign',[self.config['tools']['ssh_keygen'],'-Y','sign','-q','-f',self.config['signing_key_file'],'-n',NAMESPACE,str(path)])
        signature=Path(str(path)+'.sig');require(signature.is_file() and signature.stat().st_size>0,'SIGNATURE_MISSING')
        self.command(name+'-verify',[self.config['tools']['ssh_keygen'],'-Y','verify','-f',self.config['allowed_signers_file'],
            '-I',PRINCIPAL,'-n',NAMESPACE,'-s',str(signature)],data=path.read_bytes())

    def sign_components(self):
        self.require_stopped()
        manifest=self.output/(self.prefix+'.sha256')
        parts=[self.output/(self.prefix+suffix) for suffix in ('.postgres.dump.age','.metadata.tar.age')]
        with manifest.open('x',encoding='ascii',newline='\n') as stream:
            stream.write(''.join(file_sha(path)+'  '+path.name+'\n' for path in sorted(parts)))
        self.sign_and_verify(manifest,'backup')

    def publish(self):
        progress=[]
        for suffix in ('.postgres.dump.age','.metadata.tar.age','.sha256.sig','.sha256'):
            source=self.output/(self.prefix+suffix);target=self.base/source.name
            require(not target.exists(),'BACKUP_PUBLICATION_COLLISION')
            # Same-filesystem hard link is atomic and refuses an existing name.
            sync_file(source)
            os.link(source,target)
            sync_directory(self.base)
            row={'source':str(source),'target':str(target),'status':'LINKED_PENDING_SOURCE_UNLINK'}
            progress.append(row);write_json(self.output/'publish-progress.json',progress)
            require(source.parent==self.output and target.parent==self.base,'PUBLICATION_PATH_MISMATCH')
            source.unlink()  # Only this run's original; never remove an existing target.
            sync_directory(self.output)
            require(target.stat().st_nlink==1,'PUBLISHED_ARTIFACT_MUST_HAVE_ONE_LINK')
            row['status']='PROMOTED';write_json(self.output/'publish-progress.json',progress)
        result={'manifest':str(self.base/(self.prefix+'.sha256')),'signature':str(self.base/(self.prefix+'.sha256.sig')),
            'allowed_signers':self.config['allowed_signers_file'],'identity_file':self.config['restore_identity_file'],
            'age_binary':self.config['tools']['age'],'ssh_keygen_binary':self.config['tools']['ssh_keygen'],
            'components':{'database':str(self.base/(self.prefix+'.postgres.dump.age')),
                          'metadata':str(self.base/(self.prefix+'.metadata.tar.age'))}}
        write_json(self.output/'backup-descriptor.json',result)
        return result

    def record_result(self,status,**extra):
        write_json(self.output/'result.json',{'schema':'xingmang.platform-backup-result/v1','status':status,'mode':self.config['mode'],
            'start_utc':self.start_utc,'end_utc':utc(),'exit_code':0 if status in ('PASS','RECOVERED') else None if status=='RUNNING' else 1,
            'config_sha256':self.config_sha,'events':str(self.output/'events.json'),**extra})

    def recover(self):
        require(self.active.is_file(),'NO_ACTIVE_BACKUP_TO_RECOVER')
        journal=json.loads(self.active.read_bytes())
        require(journal.get('schema')=='xingmang.platform-backup-active/v1' and journal['config_sha256']==self.config_sha,'RECOVERY_REQUIRES_ORIGINAL_CONFIG')
        require(set(journal['before'])==set(WRITERS),'RECOVERY_WRITER_SET_MISMATCH')
        for service,row in journal['before'].items():
            expected=self.config['writers'][service]
            require(set(row)=={'running','health','container_id','image_id'} and type(row['running']) is bool and
                row['container_id']==expected['container_id'] and row['image_id']==expected['image_id'] and
                row['health'] in ('none','healthy','starting','unhealthy'),'INVALID_RECOVERY_WRITER_STATE')
        output=plain(journal['output'],directory=True)
        require(output.parent==self.base/'.platform-backup-runs','RECOVERY_OUTPUT_OUTSIDE_BACKUP_ROOT')
        before_file=plain(str(output/'writer-state-before.json'))
        require(file_sha(before_file)==journal.get('before_file_sha256') and json.loads(before_file.read_bytes())==journal['before'],'RECOVERY_BEFORE_SNAPSHOT_CHANGED')
        self.output=output/('recovery-'+uuid.uuid4().hex);self.armed=journal
        try:self.output.mkdir(mode=0o700);sync_directory(output)
        except OSError:self.journal_io_failed=True
        try:
            self.restore_writers(journal['before'])
            require(not self.journal_io_failed,'WRITERS_RESTORED_BUT_RECOVERY_LOG_WRITE_FAILED')
            self.disarm()
        except BaseException:
            try:self.record_result('RESTORE_FAILED')
            except OSError:pass
            raise
        self.record_result('RECOVERED',writers_restored=True)

def main():
    os.umask(0o077)
    parser=argparse.ArgumentParser();parser.add_argument('--config',required=True)
    parser.add_argument('--dry-run',action='store_true');parser.add_argument('--recover',action='store_true')
    args=parser.parse_args();driver=None
    try:
        config_path=plain(args.config)
        require(config_path.suffix=='.json' and not any(part.lower() in ('keys','private','secrets','.ssh') or
            part.lower().startswith('.env') for part in config_path.parts),'PUBLIC_JSON_CONFIG_REQUIRED')
        config=json.loads(config_path.read_bytes(),object_pairs_hook=unique)
        validate_config(config)
        if args.dry_run:
            require(not args.recover,'DRY_RUN_RECOVERY_CONFLICT')
            print(json.dumps({'status':'DRY_RUN','executed':False,'mode':config['mode'],'writers':list(WRITERS)}));return 0
        driver=BackupDriver(config);driver.acquire()
        if args.recover:
            driver.recover();print(json.dumps({'status':'RECOVERED','record':str(driver.output/'result.json')}));return 0
        result=perform_backup(driver)
        print(json.dumps({'status':'PASS','backup':result,'record':str(driver.output/'result.json')}));return 0
    except (BackupError,OSError,ValueError,KeyError,KeyboardInterrupt) as error:
        code=str(error) if isinstance(error,BackupError) else 'BACKUP_OPERATION_FAILED'
        if driver is not None and driver.output and not (driver.output/'result.json').exists():
            try:driver.record_result('FAILED',failure_code=code)
            except OSError:pass
        print('Platform backup failed: '+code+'; inspect the public operation journal.',file=sys.stderr)
        return 1
    finally:
        if driver is not None:driver.release()

if __name__=='__main__':
    def interrupted(signum,frame):raise KeyboardInterrupt()
    signal.signal(signal.SIGTERM,interrupted)
    sys.exit(main())

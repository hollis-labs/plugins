#!/usr/bin/env python3
"""Disposable three-container proof: private SSH app delivery and remote PITR.

The network is Docker-internal, with no published ports or production mounts.
All keys, names and data are synthetic. The source container is destroyed before
recovery, and the repository container contains encrypted data only.
"""
import argparse
import json
import os
from pathlib import Path
import secrets
import subprocess as sp
import tempfile
import time


def main():
    parser=argparse.ArgumentParser();parser.add_argument('--image',required=True);args=parser.parse_args()
    suffix=secrets.token_hex(6);network='cerberus-remote-proof-'+suffix
    db=network+'-db';remote=network+'-remote';restore=network+'-restore';containers=[]
    def run(argv,data=None,timeout=120):
        r=sp.run(argv,input=data,stdout=sp.PIPE,stderr=sp.PIPE,timeout=timeout)
        if r.returncode:
            # Fixtures contain synthetic data only. Keep errors bounded and
            # exclude payloads, keys and passwords even in this proof.
            raise RuntimeError('fixture command failed: '+argv[0]+' '+argv[-1]+' '+r.stderr.decode(errors='replace')[:1200]+r.stdout.decode(errors='replace')[:1200])
        return r.stdout
    def put(container,path,value,owner='root',mode='600'):
        run(['docker','exec','-i',container,'sh','-c','cat > '+path+' && chown '+owner+' '+path+' && chmod '+mode+' '+path],value if isinstance(value,bytes) else value.encode())
    def pg(sql,container=db):
        return run(['docker','exec','-i','-u','postgres',container,'psql','-XqAt','-v','ON_ERROR_STOP=1','-U','postgres','-d','postgres'],sql.encode())
    def backrest(arguments,container=db):
        return run(['docker','exec','-u','postgres',container,'pgbackrest','--stanza=proof','--log-level-console=error']+arguments)
    def ready(container):
        for _ in range(100):
            result=sp.run(['docker','exec',container,'sh','-c','test "$(cat /proc/1/comm)" = postgres && pg_isready -U postgres'],capture_output=True)
            if result.returncode==0:return
            time.sleep(.1)
        raise RuntimeError('fixture readiness failed')
    with tempfile.TemporaryDirectory(prefix='cerberus-remote-proof-') as tmp:
        tmp=Path(tmp)
        try:
            run(['docker','network','create','--internal','--label','cerberus.fixture=remote-proof',network])
            run(['ssh-keygen','-q','-t','ed25519','-N','','-f',str(tmp/'id')])
            run(['openssl','req','-x509','-newkey','rsa:2048','-nodes','-days','1','-keyout',str(tmp/'tls.key'),'-out',str(tmp/'tls.crt'),'-subj','/CN=postgres.example.test','-addext','subjectAltName=DNS:postgres.example.test'])
            run(['docker','run','-d','--name',remote,'--network',network,'--network-alias','app.example.test','--label','cerberus.fixture=remote-proof','--memory','384m','--cpus','1','--pids-limit','128','--tmpfs','/repo:rw,size=256m','--entrypoint','/usr/sbin/sshd',args.image,'-D','-e','-h','/etc/ssh/ssh_host_ed25519_key','-o','PasswordAuthentication=no','-o','PermitRootLogin=prohibit-password']);containers.append(remote)
            run(['docker','exec',remote,'sh','-c','mkdir -p /root/.ssh /private /credentials; chmod 700 /root/.ssh /private /credentials; echo root:fixture-only-not-an-auth-method | chpasswd'])
            put(remote,'/root/.ssh/authorized_keys',(tmp/'id.pub').read_bytes())
            pub=run(['docker','exec',remote,'cat','/etc/ssh/ssh_host_ed25519_key.pub']).split()
            known=b'app.example.test '+pub[0]+b' '+pub[1]+b'\n'
            # Start database once with TLS/archive configuration supplied first.
            run(['docker','run','-d','--name',db,'--network',network,'--network-alias','postgres.example.test','--label','cerberus.fixture=remote-proof','--memory','512m','--cpus','2','--pids-limit','128','--tmpfs','/var/lib/postgresql/data:rw,size=256m','--tmpfs','/private:rw,size=4m','-e','POSTGRES_HOST_AUTH_METHOD=trust','--entrypoint','sleep',args.image,'infinity']);containers.append(db)
            run(['docker','exec',db,'sh','-c','mkdir -p /etc/pgbackrest /private/audit /private/broker/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa /private/broker/secrets/issued/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa; chmod 700 /private/audit; chown postgres /var/lib/postgresql/data'])
            put(db,'/private/server.key',(tmp/'tls.key').read_bytes(),'postgres')
            put(db,'/private/server.crt',(tmp/'tls.crt').read_bytes(),'postgres','644')
            put(db,'/private/id',(tmp/'id').read_bytes(),'postgres')
            put(db,'/private/known_hosts',known,'postgres')
            config='[proof]\npg1-path=/var/lib/postgresql/data\npg1-socket-path=/var/run/postgresql\n[global]\nrepo1-type=sftp\nrepo1-path=/repo\nrepo1-sftp-host=app.example.test\nrepo1-sftp-host-user=root\nrepo1-sftp-host-key-check-type=strict\nrepo1-sftp-host-key-hash-type=sha256\nrepo1-sftp-known-host=/private/known_hosts\nrepo1-sftp-private-key-file=/private/id\nrepo1-cipher-type=aes-256-cbc\nrepo1-cipher-pass='+secrets.token_urlsafe(48)+'\nrepo1-retention-full=2\nstart-fast=y\nprocess-max=2\nlog-level-console=error\nlog-level-file=off\narchive-timeout=30\n'
            put(db,'/etc/pgbackrest/pgbackrest.conf',config,'postgres')
            run(['docker','exec','-u','postgres',db,'initdb','-D','/var/lib/postgresql/data','--auth-local=trust','--auth-host=scram-sha-256'])
            run(['docker','exec','-i','-u','postgres',db,'sh','-c','cat >> /var/lib/postgresql/data/pg_hba.conf'],b'hostssl all all 0.0.0.0/0 scram-sha-256\n')
            settings="\nlisten_addresses='*'\nssl=on\nssl_cert_file='/private/server.crt'\nssl_key_file='/private/server.key'\narchive_mode=on\narchive_command='pgbackrest --stanza=proof archive-push %p'\nmax_wal_size='80MB'\nmin_wal_size='32MB'\n"
            run(['docker','exec','-i','-u','postgres',db,'sh','-c','cat >> /var/lib/postgresql/data/postgresql.conf'],settings.encode())
            run(['docker','exec','-u','postgres',db,'pg_ctl','-D','/var/lib/postgresql/data','-l','/var/lib/postgresql/data/start.log','-w','start'])
            password=secrets.token_urlsafe(32)
            pg("CREATE ROLE cb_fixture_r1 LOGIN PASSWORD '"+password+"'; CREATE DATABASE fixture; GRANT CONNECT ON DATABASE fixture TO cb_fixture_r1; CREATE TABLE public.proof(id integer PRIMARY KEY); INSERT INTO public.proof VALUES(1);")
            backrest(['stanza-create']);backrest(['--type=full','backup'])
            ref='file:///private/broker/secrets/issued/'+'a'*32+'/runtime_password'
            receipt={'id':'a'*32,'connector':'postgres','operation':'provision_app','state':'completed','binding_digest':'fixture-only','references':{'runtime_password':ref},'at':'2026-10-19T00:00:00Z'}
            put(db,'/private/broker/'+'a'*32+'/completed.json',json.dumps(receipt))
            put(db,'/private/broker/secrets/issued/'+'a'*32+'/runtime_password',password)
            # Sender reads the same files as the real host broker (root here is
            # only a disposable fixture UID). Private key/known_hosts ownership
            # is changed temporarily for delivery then returned to postgres.
            run(['docker','exec',db,'chown','root','/private/id','/private/known_hosts'])
            for filename in ('receive.py','deliver.py'):
                put(db,'/private/'+filename,(Path(__file__).parent/'remote-app'/filename).read_bytes())
            put(remote,'/private/receive.py',(Path(__file__).parent/'remote-app'/'receive.py').read_bytes())
            put(remote,'/private/ca.crt',(tmp/'tls.crt').read_bytes())
            target={'app':'fixture','host':'postgres.example.test','port':5432,'database':'fixture','user':'cb_fixture_r1','reference':ref,'directory':'/credentials','ca_file':'/private/ca.crt'}
            put(remote,'/private/profile.json',json.dumps(target))
            sender={k:target[k] for k in ('app','host','port','database','user')};sender.update(broker_root='/private/broker',issuance_id='a'*32,slot='runtime_password',ssh_host='app.example.test',ssh_user='root',ssh_port=22,identity_file='/private/id',known_hosts='/private/known_hosts',receiver='/private/receive.py',receiver_profile='/private/profile.json',audit_directory='/private/audit')
            put(db,'/private/sender.json',json.dumps(sender))
            base=['docker','exec',db,'python3','/private/deliver.py','--profile','/private/sender.json']
            preview=json.loads(run(base+['plan']))
            delivered=json.loads(run(base+['--expected-plan',preview['digest'],'--ack','apply']))
            assert delivered['native_tls_verified'] and not delivered['activated']
            # Rotate a second runtime generation and deliver it under a new
            # reviewed receiver policy. Both generations overlap until the
            # receiver has proved the replacement with native TLS.
            password2=secrets.token_urlsafe(32)
            pg("CREATE ROLE cb_fixture_r2 LOGIN PASSWORD '"+password2+"'; GRANT CONNECT ON DATABASE fixture TO cb_fixture_r2;")
            run(['docker','exec',db,'sh','-c','mkdir -p /private/broker/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb /private/broker/secrets/issued/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'])
            ref2='file:///private/broker/secrets/issued/'+'b'*32+'/password'
            receipt2=dict(receipt,id='b'*32,operation='rotate_login',references={'password':ref2})
            put(db,'/private/broker/'+'b'*32+'/completed.json',json.dumps(receipt2))
            put(db,'/private/broker/secrets/issued/'+'b'*32+'/password',password2)
            target.update(user='cb_fixture_r2',reference=ref2);put(remote,'/private/profile.json',json.dumps(target))
            sender.update(user='cb_fixture_r2',issuance_id='b'*32,slot='password');put(db,'/private/sender.json',json.dumps(sender))
            preview2=json.loads(run(base+['plan']))
            delivered2=json.loads(run(base+['--expected-plan',preview2['digest'],'--ack','apply']))
            assert delivered2['native_tls_verified']
            # Model a native consumer reopening its pool from the delivered
            # private file. No password is put in argv/environment/output.
            probe_code="""import json,os,sys,tempfile,subprocess
from pathlib import Path
b=json.loads(Path(sys.argv[1]).read_text())
with tempfile.TemporaryDirectory() as d:
 p=Path(d)/'pgpass';p.write_text(':'.join(str(b[k]) for k in ('host','port','database','user','password'))+'\\n');p.chmod(0o600)
 env={'PATH':'/usr/local/bin:/usr/bin:/bin','PGPASSFILE':str(p),'PGSSLMODE':'verify-full','PGSSLROOTCERT':b['sslrootcert'],'PGAPPNAME':'fixture-consumer','PGCONNECT_TIMEOUT':'5'}
 r=subprocess.run(['psql','-XqAt','-h',b['host'],'-p',str(b['port']),'-U',b['user'],'-d',b['database']],input=b'SELECT current_user;',env=env,capture_output=True,timeout=10)
 print('connected' if r.returncode==0 and r.stdout.strip().decode()==b['user'] else 'refused')
"""
            put(remote,'/private/probe.py',probe_code)
            def app_probe(delivery):
                return run(['docker','exec',remote,'python3','/private/probe.py','/credentials/'+delivery['delivery_id']+'.json']).strip()
            assert app_probe(delivered)==b'connected' and app_probe(delivered2)==b'connected'
            pg('ALTER ROLE cb_fixture_r1 NOLOGIN;')
            assert app_probe(delivered)==b'refused' and app_probe(delivered2)==b'connected'
            run(['docker','exec',db,'chown','postgres','/private/id','/private/known_hosts'])
            pg("INSERT INTO public.proof VALUES(2); SELECT pg_create_restore_point('remote_proof_target'); INSERT INTO public.proof VALUES(3); SELECT pg_switch_wal();")
            backrest(['check'])
            # Destroy every source data byte before restoring from the remote
            # encrypted repository with only the independently kept recovery kit.
            run(['docker','rm','-f',db]);containers.remove(db)
            run(['docker','run','-d','--name',restore,'--network',network,'--label','cerberus.fixture=remote-proof','--memory','512m','--cpus','2','--tmpfs','/var/lib/postgresql/data:rw,size=256m','--tmpfs','/private:rw,size=4m','--entrypoint','sleep',args.image,'infinity']);containers.append(restore)
            run(['docker','exec',restore,'sh','-c','mkdir -p /etc/pgbackrest; chown postgres /var/lib/postgresql/data; chmod 700 /var/lib/postgresql/data'])
            for path,value,mode in [('/private/id',(tmp/'id').read_bytes(),'600'),('/private/known_hosts',known,'600'),('/private/server.key',(tmp/'tls.key').read_bytes(),'600'),('/private/server.crt',(tmp/'tls.crt').read_bytes(),'644'),('/etc/pgbackrest/pgbackrest.conf',config,'600')]:put(restore,path,value,'postgres',mode)
            backrest(['--type=name','--target=remote_proof_target','--target-action=promote','--archive-mode=off','restore'],restore)
            run(['docker','exec','-u','postgres',restore,'pg_ctl','-D','/var/lib/postgresql/data','-l','/var/lib/postgresql/data/restore.log','-w','start'])
            assert pg("SELECT string_agg(id::text,',' ORDER BY id) FROM public.proof;",restore).strip()==b'1,2'
            assert pg('SELECT pg_is_in_recovery();',restore).strip()==b'f'
            assert json.loads(backrest(['--output=json','info'],restore))[0]['cipher']=='aes-256-cbc'
            print(json.dumps({'private_ssh_delivery':True,'native_app_tls_verified':True,'remote_rotation_overlap_and_reconnect':True,'old_login_refused_after_retirement':True,'encrypted_remote_repository':True,'source_destroyed_before_recovery':True,'pitr_excludes_later_commit':True,'app_activation':False,'production_mounts':False,'published_ports':False}))
        finally:
            for container in containers:
                info=json.loads(run(['docker','inspect',container]))[0]
                assert info['Config']['Labels']['cerberus.fixture']=='remote-proof'
                run(['docker','rm','-f',container])
            sp.run(['docker','network','rm',network],stdout=sp.DEVNULL,stderr=sp.DEVNULL)

if __name__=='__main__':main()

"""Two product databases, shared key ledger, rolling writers and offline restore drill."""
import hashlib
import json
import os
from pathlib import Path
import secrets
import subprocess
import tempfile
import time
import uuid
ROOT=Path(__file__).resolve().parents[2]
IMAGE="postgres:17.11-alpine@sha256:3540c0362c809df084ffac0ae69fe794515f9de92692f02178bcdce0ef0302ac"
name="heimdall-key-rotation-"+uuid.uuid4().hex
password=secrets.token_hex(24)
env=dict(os.environ,POSTGRES_PASSWORD=password,PGPASSWORD=password)
f=json.loads((ROOT/'contracts/fixtures/key-rotation.json').read_text())
created=False
if not __debug__:raise RuntimeError("assertions must be enabled")
def run(args,**kwargs):return subprocess.run(args,cwd=ROOT,check=True,**kwargs)
def sql(statement,database='postgres'):
 return run(['docker','exec','-i',name,'psql','-U','postgres','-d',database,'-At','-v','ON_ERROR_STOP=1'],input=statement,capture_output=True,text=True).stdout.strip()
def fp(key):return hashlib.sha256(b'heimdall.record-key-usage.v1\0'+bytes([f['keys'][key]])*32).hexdigest()
try:
 run(['docker','run','--detach','--name',name,'-e','POSTGRES_PASSWORD','-p','127.0.0.1::5432',IMAGE],env=env,stdout=subprocess.DEVNULL);created=True
 for _ in range(60):
  if subprocess.run(['docker','exec',name,'pg_isready','-U','postgres'],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL).returncode==0:break
  time.sleep(.5)
 else:raise RuntimeError('PostgreSQL readiness timeout')
 port=run(['docker','port',name,'5432'],capture_output=True,text=True).stdout.strip().split(':')[-1]
 def dsn(database,admin=False):return f"postgres://{'postgres' if admin else 'rotation_runtime'}:{password}@127.0.0.1:{port}/{database}?sslmode=disable"
 sql("CREATE ROLE rotation_runtime LOGIN PASSWORD '"+password+"'")
 for database in ('ledger','support','key_ledger'):
  sql('CREATE DATABASE '+database)
 for database in ('ledger','support'):sql((ROOT/'contracts/persistence/session/0001.sql').read_text(),database)
 def grant_product(database):sql('GRANT CONNECT ON DATABASE '+database+' TO rotation_runtime; GRANT USAGE ON SCHEMA heimdall TO rotation_runtime; GRANT SELECT,INSERT,UPDATE,DELETE ON heimdall.records TO rotation_runtime; GRANT USAGE ON SEQUENCE heimdall.record_revision_seq TO rotation_runtime',database)
 def grant_ledger(database):sql('GRANT CONNECT ON DATABASE '+database+' TO rotation_runtime; GRANT USAGE ON SCHEMA heimdall_keys TO rotation_runtime; GRANT SELECT ON ALL TABLES IN SCHEMA heimdall_keys TO rotation_runtime; GRANT UPDATE(used) ON heimdall_keys.record_key_usage TO rotation_runtime',database)
 def phase(selected,restored=False):
  ledger='restored_keys' if restored else 'key_ledger'
  processes=[]
  for language,product in [('ts','ledger'),('go','support')]:
   database='restored_'+product if restored else product
   selected_env=dict(env,HEIMDALL_ROTATION_PRODUCT=dsn(database),HEIMDALL_ROTATION_LEDGER=dsn(ledger),HEIMDALL_ROTATION_PHASE=selected)
   args=['pnpm','--filter','@heimdall/sdk','exec','vitest','run','test/postgres-key-rotation.test.ts'] if language=='ts' else ['go','test','-race','-count=1','./internal/postgres','-run','^TestKeyRotationPhases$']
   processes.append(subprocess.Popen(args,cwd=ROOT if language=='ts' else ROOT/'sdk/go',env=selected_env))
  assert [p.wait() for p in processes]==[0,0]
 with tempfile.TemporaryDirectory(prefix='heimdall-rotation-') as directory:
  directory=Path(directory);binary=directory/'heimdall-keys'
  subprocess.run(['go','build','-o',str(binary),'./cmd/heimdall-keys'],cwd=ROOT/'sdk/go',check=True)
  def cli(args,database='key_ledger'):
   run([str(binary),*args],env=dict(env,HEIMDALL_KEY_DATABASE_URL=dsn(database,True)),capture_output=True,text=True)
  def register(key,database='key_ledger'):
   path=directory/key;path.write_bytes(bytes([f['keys'][key]])*32);path.chmod(0o600)
   cli(['--register','--fresh-key','--key-file',str(path),'--limit',str(f['limit'])],database)
  cli(['--initialize','--sql',str(ROOT/'contracts/persistence/keys/0001.sql')]);register('old')
  for database in ('ledger','support'):grant_product(database)
  grant_ledger('key_ledger')
  phase('initial')
  assert sql("SELECT used FROM heimdall_keys.record_key_usage WHERE fingerprint=decode('"+fp('old')+"','hex')",'key_ledger')=='2'
  # Snapshot files contain synthetic test records only and remain outside the repository.
  for database in ('ledger','support','key_ledger'):
   with (directory/(database+'.dump')).open('wb') as output:
    run(['docker','exec',name,'pg_dump','-U','postgres','-d',database,'-Fc','--no-owner','--no-privileges'],stdout=output)
  register('next');phase('rolling')
  cli(['--retire','--fingerprint',fp('old')]);phase('retired')
  assert sql("SELECT state||':'||used FROM heimdall_keys.record_key_usage WHERE fingerprint=decode('"+fp('old')+"','hex')",'key_ledger')=='retired:4'
  assert sql("SELECT used FROM heimdall_keys.record_key_usage WHERE fingerprint=decode('"+fp('next')+"','hex')",'key_ledger')=='4'
  # New isolated restore targets have no runtime CONNECT privilege throughout preparation.
  for source,target in [('ledger','restored_ledger'),('support','restored_support'),('key_ledger','restored_keys')]:
   sql('CREATE DATABASE '+target);sql('REVOKE CONNECT ON DATABASE '+target+' FROM PUBLIC')
   with (directory/(source+'.dump')).open('rb') as backup:
    run(['docker','exec','-i',name,'pg_restore','-U','postgres','-d',target,'--no-owner','--no-privileges','--exit-on-error'],stdin=backup,capture_output=True)
   denied=subprocess.run(['docker','exec','-e','PGPASSWORD',name,'psql','-h','127.0.0.1','-U','rotation_runtime','-d',target,'-c','SELECT 1'],env=env,capture_output=True,text=True)
   assert denied.returncode!=0 and 'permission denied for database' in denied.stderr
  # Prove why a snapshot cannot be resumed as-is: it resurrects logout state and key budget.
  assert sql("SELECT state||':'||used FROM heimdall_keys.record_key_usage WHERE fingerprint=decode('"+fp('old')+"','hex')",'restored_keys')=='active:2'
  for database in ('restored_ledger','restored_support'):
   assert sql("SELECT count(*) FROM heimdall.records WHERE kind='session'",database)=='1'
   sql('DELETE FROM heimdall.records',database)
  cli(['--retire','--fingerprint',fp('old')],'restored_keys');register('recovery','restored_keys')
  assert sql("SELECT state||':'||used FROM heimdall_keys.record_key_usage WHERE fingerprint=decode('"+fp('old')+"','hex')",'restored_keys')=='retired:2'
  for database in ('restored_ledger','restored_support'):grant_product(database)
  grant_ledger('restored_keys');phase('recovered',True)
  assert sql("SELECT used FROM heimdall_keys.record_key_usage WHERE fingerprint=decode('"+fp('recovery')+"','hex')",'restored_keys')=='2'
  assert sql("SELECT state||':'||used FROM heimdall_keys.record_key_usage WHERE fingerprint=decode('"+fp('old')+"','hex')",'key_ledger')=='retired:4'
 print(json.dumps({'run':name,'postgres':IMAGE,'result':'passed','checks':['two product databases and shared key ledger','concurrent TS Go rotation phases','old-key laggards refused after retirement','retained keys read prior envelopes','real dump restore demonstrates logout and counter rollback','runtime CONNECT denied during restore preparation','restored sessions and login states discarded offline','restored keys retired and fresh material registered before runtime access','both SDKs reject retired and unknown restored keys','fresh shared recovery key consumed exactly twice','original databases unaffected by isolated restore']}))
finally:
 if created:subprocess.run(['docker','rm','--force','--volumes',name],check=True,stdout=subprocess.DEVNULL)

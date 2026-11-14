"""Offline capacity preparation, catalog checks and actual migration CLI proof."""
import json
import os
from pathlib import Path
import secrets
import subprocess
import tempfile
import time
import uuid
from pg_commit_loss import CommitLossProxy
ROOT=Path(__file__).resolve().parents[2]
IMAGE='postgres:17.11-alpine@sha256:3540c0362c809df084ffac0ae69fe794515f9de92692f02178bcdce0ef0302ac'
name='heimdall-capacity-upgrade-'+uuid.uuid4().hex
env=dict(os.environ,POSTGRES_PASSWORD=secrets.token_hex(24))
created=False
if not __debug__:raise RuntimeError('assertions must be enabled')
def run(args,**kwargs):return subprocess.run(args,cwd=ROOT,check=True,**kwargs)
def sql(statement,database='postgres'):
 r=run(['docker','exec','-i',name,'psql','-U','postgres','-d',database,'-At','-v','ON_ERROR_STOP=1'],input=statement,capture_output=True,text=True)
 return r.stdout.strip()
try:
 run(['docker','run','--detach','--name',name,'-e','POSTGRES_PASSWORD','-p','127.0.0.1::5432',IMAGE],env=env,stdout=subprocess.DEVNULL);created=True
 for _ in range(60):
  if subprocess.run(['docker','exec',name,'pg_isready','-U','postgres'],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL).returncode==0:break
  time.sleep(.5)
 else:raise RuntimeError('PostgreSQL readiness timeout')
 port=run(['docker','port',name,'5432'],capture_output=True,text=True).stdout.strip().split(':')[-1]
 def dsn(database='postgres',selected_port=port):return f"postgres://postgres:{env['POSTGRES_PASSWORD']}@127.0.0.1:{selected_port}/{database}?sslmode=disable"
 selected=dict(env,HEIMDALL_TEST_CAPACITY_UPGRADE_URL=dsn())
 subprocess.run(['go','test','-race','-count=1','./internal/postgres','-run','^TestAuditCapacityUpgrade(Input|Live)$'],cwd=ROOT/'sdk/go',env=selected,check=True)
 run(['pnpm','--filter','@techsavvyash/heimdall-sdk','exec','vitest','run','test/postgres-audit-capacity.test.ts'],env=selected)
 assert sql("SELECT database_version||':'||state FROM heimdall.schema_metadata")=='2:migrating'
 assert sql("SELECT state||':'||used_events||':'||reserved_events FROM heimdall_capacity.policy")=='preparing:1:0'
 with tempfile.TemporaryDirectory(prefix='heimdall-capacity-cli-') as directory:
  binary=Path(directory)/'heimdall-migrate'
  subprocess.run(['go','build','-o',str(binary),'./cmd/heimdall-migrate'],cwd=ROOT/'sdk/go',check=True)
  source=str(ROOT/'contracts/persistence/session')
  args=['--prepare-capacity-upgrade','--writers-drained','--sql-directory',source,'--capacity-events','100','--capacity-bytes','150000000','--capacity-deliveries','3200']
  def cli(arguments,database='postgres',expected=0,url=None):
   result=subprocess.run([str(binary),*arguments],env=dict(env,HEIMDALL_MIGRATION_DATABASE_URL=url or dsn(database)),capture_output=True,text=True)
   assert result.returncode==expected,(arguments[0],result.returncode,result.stderr)
   assert env['POSTGRES_PASSWORD'] not in result.stdout+result.stderr
   return result
  cli(args)
  for bad in [args[:1]+args[2:],args[:-2],args+['--initialize-managed-schema'],args+['--batch-size','1'],['--status','audit-v2','--capacity-events','1'],args[:-1]+['9007199254740992']]:cli(bad,expected=2)
  assert 'conflict' in cli(args[:-1]+['3199'],expected=1).stderr
  assert 'unavailable' in cli(args,expected=1,url=dsn()+'&options=-c%20default_transaction_read_only%3Don').stderr
  def ready(database):
   sql('CREATE DATABASE '+database)
   cli(['--initialize-session-schema','--sql',source+'/0001.sql'],database)
   cli(['--prepare-audit-upgrade','--writers-drained','--sql-directory',source],database)
   cli(['--backfill-audit-upgrade','--writers-drained'],database)
   cli(['--activate-audit-upgrade','--writers-drained'],database)
  ready('rollback')
  sql("CREATE FUNCTION public.reject_capacity_ddl() RETURNS event_trigger LANGUAGE plpgsql AS $$ BEGIN IF to_regclass('heimdall_capacity.backfill') IS NOT NULL THEN RAISE EXCEPTION 'test DDL failure'; END IF; END $$; CREATE EVENT TRIGGER reject_capacity_ddl ON ddl_command_end EXECUTE FUNCTION public.reject_capacity_ddl()",'rollback')
  assert 'unavailable' in cli(args,'rollback',1).stderr
  assert sql("SELECT to_regnamespace('heimdall_capacity') IS NULL",'rollback')=='t'
  assert sql("SELECT state FROM heimdall.schema_metadata",'rollback')=='ready'
  sql('DROP EVENT TRIGGER reject_capacity_ddl; DROP FUNCTION public.reject_capacity_ddl()','rollback')
  cli(args,'rollback')
  ready('bad_base');sql('ALTER TABLE heimdall.audit_events DISABLE TRIGGER ALL','bad_base')
  assert 'audit_unavailable' in cli(args,'bad_base',1).stderr
  assert sql("SELECT to_regnamespace('heimdall_capacity') IS NULL",'bad_base')=='t'
  ready('partial');sql((ROOT/'contracts/persistence/session/0003-capacity.sql').read_text(),'partial')
  assert 'unavailable' in cli(args,'partial',1).stderr
  assert sql("SELECT to_regclass('heimdall_capacity.upgrade') IS NULL",'partial')=='t'
  ready('loss')
  with CommitLossProxy(int(port)) as proxy:
   assert 'outcome_unknown' in cli(args,'loss',1,url=dsn('loss',proxy.port)).stderr
   assert proxy.dropped.is_set() and not proxy.errors
  assert sql("SELECT database_version||':'||state FROM heimdall.schema_metadata",'loss')=='2:migrating'
  assert sql('SELECT count(*) FROM heimdall_capacity.upgrade','loss')=='1'
  cli(args,'loss')
  assert 'unavailable' in cli(['--status','audit-v2'],'loss',1).stderr
 print(json.dumps({'run':name,'result':'passed','checks':['supported v2 upgrade baseline','immutable pending completed and recovered history preserved','explicit immutable quotas and pinned SQL','exact concurrent preparation retries preserve progress and charges','18 catalog and metadata drift refusals','resolved import only while preparing with zero future reserve','pending imports retain full recovery reserve','no v2 or capacity runtime admission in TS and Go after preparation','held deployment lock deadline and reuse','actual CLI argument and read-only refusals','mid-DDL rollback restores v2 readiness','old schema drift and partial manual schema refused','confirmed COMMIT response loss and receipt retry']}))
finally:
 if created:run(['docker','rm','--force','--volumes',name],stdout=subprocess.DEVNULL)

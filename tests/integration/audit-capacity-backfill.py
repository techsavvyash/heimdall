"""Bounded offline capacity accounting over real TS/Go history on pinned PostgreSQL."""
import concurrent.futures
import json
import hashlib
import os
from pathlib import Path
import secrets
import select
import subprocess
import tempfile
import time
import uuid
from pg_commit_loss import CommitLossProxy
ROOT=Path(__file__).resolve().parents[2]
IMAGE='postgres:17.11-alpine@sha256:3540c0362c809df084ffac0ae69fe794515f9de92692f02178bcdce0ef0302ac'
name='heimdall-capacity-backfill-'+uuid.uuid4().hex
env=dict(os.environ,POSTGRES_PASSWORD=secrets.token_hex(24))
f=json.loads((ROOT/'contracts/fixtures/audit-capacity-backfill.json').read_text())
created=False
bind_data=None
if not __debug__:raise RuntimeError('assertions must be enabled')
def run(args,**kwargs):return subprocess.run(args,cwd=ROOT,check=True,**kwargs)
def sql(statement,database='postgres'):
 try:
  return run(['docker','exec','-i',name,'psql','-U','postgres','-d',database,'-At','-v','ON_ERROR_STOP=1'],input=statement,capture_output=True,text=True).stdout.strip()
 except subprocess.CalledProcessError as error:
  raise RuntimeError('PostgreSQL statement failed: '+error.stderr[-1200:]) from None
def go(pattern,selected):subprocess.run(['go','test','-race','-count=1','./internal/postgres','-run',pattern],cwd=ROOT/'sdk/go',env=selected,check=True)
def ts(selected):run(['pnpm','--filter','@heimdall/sdk','exec','vitest','run','test/postgres-capacity-history.test.ts'],env=selected)
try:
 storage=[]
 if os.environ.get('HEIMDALL_TEST_POSTGRES_BIND_ROOT'):
  bind_data=tempfile.TemporaryDirectory(prefix='heimdall-capacity-data-',dir=os.environ['HEIMDALL_TEST_POSTGRES_BIND_ROOT'])
  storage=['--mount','type=bind,source='+bind_data.name+',target=/var/lib/postgresql/data']
 run(['docker','run','--detach','--name',name,*storage,'-e','POSTGRES_PASSWORD','-p','127.0.0.1::5432',IMAGE],env=env,stdout=subprocess.DEVNULL);created=True
 for _ in range(60):
  if subprocess.run(['docker','exec',name,'pg_isready','-h','127.0.0.1','-U','postgres'],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL).returncode==0:break
  time.sleep(.5)
 else:raise RuntimeError('PostgreSQL readiness timeout')
 port=run(['docker','port',name,'5432'],capture_output=True,text=True).stdout.strip().split(':')[-1]
 def dsn(database,selected_port=port):return f"postgres://postgres:{env['POSTGRES_PASSWORD']}@127.0.0.1:{selected_port}/{database}?sslmode=disable"
 with tempfile.TemporaryDirectory(prefix='heimdall-capacity-backfill-') as directory:
  binary=Path(directory)/'heimdall-migrate'
  subprocess.run(['go','build','-o',str(binary),'./cmd/heimdall-migrate'],cwd=ROOT/'sdk/go',check=True)
  source=str(ROOT/'contracts/persistence/session')
  prepare=['--prepare-capacity-upgrade','--writers-drained','--sql-directory',source,'--capacity-events','1000','--capacity-bytes','200000000','--capacity-deliveries','32000']
  backfill=['--backfill-capacity-upgrade','--writers-drained','--batch-size','2']
  def cli(args,database,expected=0,url=None):
   r=subprocess.run([str(binary),*args],env=dict(env,HEIMDALL_MIGRATION_DATABASE_URL=url or dsn(database)),capture_output=True,text=True)
   assert r.returncode==expected,(args[0],r.returncode,r.stderr)
   assert env['POSTGRES_PASSWORD'] not in r.stdout+r.stderr
   return r
  def initialize(database):
   sql('CREATE DATABASE '+database)
   cli(['--initialize-session-schema','--sql',source+'/0001.sql'],database)
   cli(['--prepare-audit-upgrade','--writers-drained','--sql-directory',source],database)
   cli(['--backfill-audit-upgrade','--writers-drained'],database)
   cli(['--activate-audit-upgrade','--writers-drained'],database)
  def snapshot(database):return sql('SELECT row_to_json(b)::text FROM heimdall_capacity.backfill b',database)
  def advance(database,batch=2):return json.loads(cli(backfill[:-1]+[str(batch)],database).stdout)
  def finish(database,batch=2):
   steps=[]
   for _ in range(200):
    p=advance(database,batch);steps.append(p)
    if p['phase']=='complete':return steps
   raise RuntimeError('backfill failed to finish')
  initialize('prepared')
  seed=dict(env,HEIMDALL_TEST_CAPACITY_HISTORY_URL=dsn('prepared'))
  go('^TestCapacityBackfill(Seed|Input)$',seed);ts(seed)
  assert sql('SELECT count(*) FROM heimdall.audit_events','prepared')==str(f['events'])
  cli(prepare,'prepared')
  for database in ['normal','concurrent','loss','loss_decision','preaccounted','limited',*[x['name'] for x in f['negative']]]:sql('CREATE DATABASE '+database+' TEMPLATE prepared')
  for bad in [backfill[:1]+backfill[2:],backfill[:-1]+['0'],backfill+['--prepare-capacity-upgrade'],backfill+['--capacity-events','1'],backfill+['--sql-directory',source]]:cli(bad,'normal',2)
  before=snapshot('normal');assert 'unavailable' in cli(backfill,'normal',1,url=dsn('normal')+'&options=-c%20default_transaction_read_only%3Don').stderr
  assert snapshot('normal')==before
  steps=finish('normal');assert steps[-1]['scannedEvents']==f['events'] and steps[-1]['scannedDecisions']==f['decisions']
  assert max(s['processed'] for s in steps)<=2
  assert sql('SELECT used_events||\':\'||reserved_events FROM heimdall_capacity.policy','normal')==f"{f['events']}:{f['reservedEvents']}"
  stable=snapshot('normal');assert advance('normal')['processed']==0 and snapshot('normal')==stable
  ts(dict(env,HEIMDALL_TEST_CAPACITY_ACCOUNTED_URL=dsn('normal')))
  go('^TestCapacityBackfillLock$',dict(env,HEIMDALL_TEST_CAPACITY_ACCOUNTED_URL=dsn('normal')))
  with concurrent.futures.ThreadPoolExecutor(max_workers=8) as pool:results=list(pool.map(lambda _:advance('concurrent',1),range(8)))
  assert sorted(r['scannedEvents'] for r in results)==list(range(1,9))
  assert finish('concurrent')[-1]['scannedDecisions']==f['decisions']
  with CommitLossProxy(int(port)) as proxy:
   assert 'outcome_unknown' in cli(backfill,'loss',1,url=dsn('loss',proxy.port)).stderr
   assert proxy.dropped.is_set() and not proxy.errors
  assert json.loads(snapshot('loss'))['scanned_events']==2
  assert advance('loss')['scannedEvents']==4
  assert finish('loss')[-1]['scannedEvents']==f['events']
  advance('loss_decision',100);assert advance('loss_decision',100)['phase']=='decisions'
  with CommitLossProxy(int(port)) as proxy:
   assert 'outcome_unknown' in cli(backfill,'loss_decision',1,url=dsn('loss_decision',proxy.port)).stderr
   assert proxy.dropped.is_set() and not proxy.errors
  assert json.loads(snapshot('loss_decision'))['scanned_decisions']==2
  assert finish('loss_decision')[-1]['scannedDecisions']==f['decisions']
  assert sql('SELECT reserved_events FROM heimdall_capacity.policy','loss_decision')==str(f['reservedEvents'])
  # Pre-existing matching imports are verified, not charged/reserved again.
  sql("INSERT INTO heimdall_capacity.event_charges(product_id,environment,event_id) SELECT product_id,environment,event_id FROM heimdall.audit_events; INSERT INTO heimdall_capacity.reservations(product_id,environment,decision_id,request_event_id) SELECT product_id,environment,decision_id,request_event_id FROM heimdall.audit_decisions WHERE resolution='pending'",'preaccounted')
  finish('preaccounted',100)
  assert sql('SELECT used_events||\':\'||reserved_events FROM heimdall_capacity.policy','preaccounted')==f"{f['events']}:{f['reservedEvents']}"
  for case in f['negative']:
   database=case['name'];sql(case['sql'],database)
   for _ in range(30):
    before=snapshot(database);counts=sql('SELECT used_events||\':\'||reserved_events FROM heimdall_capacity.policy',database)
    result=subprocess.run([str(binary),*backfill[:-1],'100'],env=dict(env,HEIMDALL_MIGRATION_DATABASE_URL=dsn(database)),capture_output=True,text=True)
    if result.returncode:
     assert 'audit_unavailable' in result.stderr,(database,result.stderr)
     assert snapshot(database)==before and sql('SELECT used_events||\':\'||reserved_events FROM heimdall_capacity.policy',database)==counts
     assert json.loads(before)['phase']==case['phase'];break
    assert json.loads(result.stdout)['phase']!='complete',database
   else:raise RuntimeError('negative case never refused')
  # Correct profile with deliberately insufficient operator limits must roll back
  # the decision page rather than publishing incomplete pending reservations.
  sql(f"UPDATE heimdall_capacity.upgrade SET event_limit={f['events']}; UPDATE heimdall_capacity.policy SET event_limit={f['events']}",'limited')
  assert advance('limited',100)['phase']=='events'
  assert advance('limited',100)['phase']=='decisions'
  before=snapshot('limited');assert 'audit_unavailable' in cli(backfill[:-1]+['100'],'limited',1).stderr
  assert snapshot('limited')==before and sql('SELECT count(*) FROM heimdall_capacity.reservations','limited')=='0'
  # Count/anti-join coverage catches a late legacy write even after completion.
  go('^TestCapacityBackfillSeed$',dict(env,HEIMDALL_TEST_CAPACITY_HISTORY_URL=dsn('concurrent'),HEIMDALL_TEST_CAPACITY_HISTORY_MODE='late'))
  before=snapshot('concurrent');assert 'audit_unavailable' in cli(backfill,'concurrent',1).stderr;assert snapshot('concurrent')==before
  initialize('budget')
  go('^TestCapacityBackfillSeed$',dict(env,HEIMDALL_TEST_CAPACITY_HISTORY_URL=dsn('budget'),HEIMDALL_TEST_CAPACITY_HISTORY_MODE='budget'))
  cli(prepare,'budget')
  first=advance('budget',100);assert 0<first['processed']<100
  assert 0<int(sql('SELECT sum(envelope_bytes) FROM heimdall_capacity.event_charges','budget'))<=4194304
  budget_steps=finish('budget',100)
  decision_steps=[s for s in budget_steps if s['phase']=='decisions' and s['processed']>0]
  assert len(decision_steps)>1 and all(s['processed']<7 for s in decision_steps)
  assert budget_steps[-1]['scannedEvents']==121 and budget_steps[-1]['scannedDecisions']==7
  # Activation revalidates accounted history and commits policy/protocol together.
  cases={
   'activate_ok':None,
   'activate_validator':None,
   'activate_loss':None,
   'activate_incomplete':"UPDATE heimdall_capacity.backfill SET phase='decisions'",
   'activate_digest':"UPDATE heimdall.audit_events SET payload_digest=decode(repeat('00',32),'hex') WHERE product_id='support'",
   'activate_missing':"DELETE FROM heimdall_capacity.event_charges c WHERE product_id='ledger' AND NOT EXISTS(SELECT 1 FROM heimdall.audit_decisions d WHERE d.product_id=c.product_id AND d.environment=c.environment AND (d.request_event_id=c.event_id OR d.terminal_event_id=c.event_id))",
   'activate_schema_drift':"ALTER TABLE heimdall_capacity.reservations DISABLE TRIGGER guard_reservation",
  }
  for database,mutation in cases.items():
   sql('CREATE DATABASE '+database+' TEMPLATE normal')
   if mutation:sql(mutation,database)
   mode='validator' if database=='activate_validator' else ('reject' if mutation else '')
   if database=='activate_loss':
    with CommitLossProxy(int(port)) as proxy:
     go('^TestCapacityActivationLive$',dict(env,HEIMDALL_TEST_CAPACITY_ACTIVATE_URL=dsn(database,proxy.port),HEIMDALL_TEST_CAPACITY_ACTIVATE_MODE='unknown'))
     assert proxy.dropped.is_set() and not proxy.errors
   go('^TestCapacityActivationLive$',dict(env,HEIMDALL_TEST_CAPACITY_ACTIVATE_URL=dsn(database),HEIMDALL_TEST_CAPACITY_ACTIVATE_MODE=mode))
  sql('CREATE DATABASE activate_publish_failure TEMPLATE normal')
  sql("CREATE ROLE activation_limited; GRANT USAGE ON SCHEMA heimdall,heimdall_capacity TO activation_limited; GRANT ALL ON ALL TABLES IN SCHEMA heimdall,heimdall_capacity TO activation_limited; GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA heimdall,heimdall_capacity TO activation_limited; REVOKE UPDATE ON heimdall.schema_metadata FROM activation_limited",'activate_publish_failure')
  go('^TestCapacityActivationLive$',dict(env,HEIMDALL_TEST_CAPACITY_ACTIVATE_URL=dsn('activate_publish_failure')+'&options=-c%20role%3Dactivation_limited',HEIMDALL_TEST_CAPACITY_ACTIVATE_MODE='reject'))
  assert sql("SELECT state FROM heimdall_capacity.policy",'activate_publish_failure')=='preparing'
  assert sql("SELECT count(*) FROM heimdall.migrations WHERE version=3",'activate_publish_failure')=='0'
  sql('CREATE DATABASE cli_activation TEMPLATE normal')
  activate=['--activate-capacity-upgrade','--writers-drained']
  for bad in [activate[:1],activate+['--batch-size','10'],activate+['--sql-directory',source],activate+['--capacity-events','10'],activate+['--activate-audit-upgrade']]:cli(bad,'cli_activation',2)
  cli(['--status','audit-v3'],'cli_activation',1)
  cli(activate,'cli_activation')
  cli(activate,'cli_activation')
  cli(['--status','audit-v3'],'cli_activation')
  cli(['--status','audit-v2'],'cli_activation',1)
  cli(['--status','managed-v3'],'cli_activation',1)
  cli(['--initialize-managed-schema','--sql',str(ROOT/'contracts/persistence/managed/0001.sql')],'cli_activation')
  cli(['--status','managed-v3'],'cli_activation')
  ready_env=dict(env,HEIMDALL_TEST_CAPACITY_READY_URL=dsn('cli_activation'))
  go('^Test(CapacityReadiness|ConnectorPostgresReuse)$',ready_env)
  run(['pnpm','--filter','@heimdall/sdk','exec','vitest','run','test/postgres-capacity-readiness.test.ts'],env=ready_env)
  sql("CREATE ROLE public_capacity_runtime; GRANT USAGE ON SCHEMA heimdall,heimdall_capacity TO public_capacity_runtime; GRANT SELECT ON heimdall.schema_metadata,heimdall.migrations,heimdall_capacity.policy,heimdall_capacity.upgrade,heimdall_capacity.backfill TO public_capacity_runtime; GRANT SELECT,INSERT ON heimdall.audit_events,heimdall.audit_deliveries,heimdall_capacity.event_charges TO public_capacity_runtime; GRANT SELECT,INSERT,UPDATE ON heimdall.audit_decisions,heimdall_capacity.reservations TO public_capacity_runtime; GRANT UPDATE(used_events,used_bytes,used_deliveries,reserved_events,reserved_bytes,reserved_deliveries) ON heimdall_capacity.policy TO public_capacity_runtime",'cli_activation')
  go('^TestLegacyPublicCapacityHistory$',ready_env)
  # Retention preparation is an offline transaction over the activated v3 history.
  retention_prepare=['--prepare-retention-upgrade','--writers-drained','--sql-directory',source,'--retention-rows','1000','--retention-bytes','8192000']
  for database in ['retention_prepare','retention_prepare_loss','retention_prepare_late','retention_prepare_small','retention_prepare_partial','retention_prepare_catalog']:
   sql('CREATE DATABASE '+database+' TEMPLATE cli_activation')
  for bad in [retention_prepare[:1]+retention_prepare[2:],retention_prepare+['--batch-size','1'],retention_prepare+['--capacity-events','1'],retention_prepare+['--activate-capacity-upgrade'],retention_prepare[:-1]+['8191'],retention_prepare[:5]+['0']+retention_prepare[6:],['--status','audit-v3','--retention-rows','1']]:cli(bad,'retention_prepare',2)
  assert 'unavailable' in cli(retention_prepare,'retention_prepare',1,url=dsn('retention_prepare')+'&options=-c%20default_transaction_read_only%3Don').stderr
  def unprepared(database):
   assert sql("SELECT to_regnamespace('heimdall_retention') IS NULL",database)=='t'
   cli(['--status','audit-v3'],database)
  unprepared('retention_prepare')
  required=int(sql("SELECT (SELECT count(*) FROM heimdall.audit_events)+(SELECT count(*) FROM heimdall.audit_decisions)+2*(SELECT count(*) FROM heimdall.audit_decisions WHERE resolution='pending')",'retention_prepare_small'))
  for args in [retention_prepare[:5]+[str(required-1)]+retention_prepare[6:],retention_prepare[:-1]+[str(required*8192-1)]]:
   assert 'unavailable' in cli(args,'retention_prepare_small',1).stderr
   unprepared('retention_prepare_small')
  # A late DDL failure must restore the entire old schema and serving readiness.
  sql("CREATE FUNCTION public.reject_retention_ddl() RETURNS event_trigger LANGUAGE plpgsql AS $$ BEGIN IF to_regclass('heimdall_retention.upgrade') IS NOT NULL THEN RAISE EXCEPTION 'injected late DDL failure'; END IF; END $$; CREATE EVENT TRIGGER reject_retention_ddl ON ddl_command_end EXECUTE FUNCTION public.reject_retention_ddl()",'retention_prepare_late')
  cli(retention_prepare,'retention_prepare_late',1);unprepared('retention_prepare_late')
  sql('CREATE SCHEMA heimdall_retention','retention_prepare_partial')
  cli(retention_prepare,'retention_prepare_partial',1)
  assert sql("SELECT state FROM heimdall.schema_metadata",'retention_prepare_partial')=='ready'
  sql('ALTER TABLE heimdall.audit_events ADD COLUMN unexpected text','retention_prepare_catalog')
  cli(retention_prepare,'retention_prepare_catalog',1)
  assert sql("SELECT to_regnamespace('heimdall_retention') IS NULL",'retention_prepare_catalog')=='t'
  # Exact-fit acceptance includes pending terminal/recovery metadata reservations.
  exact=retention_prepare[:5]+[str(required)]+retention_prepare[6:-1]+[str(required*8192)]
  cli(exact,'retention_prepare_small')
  with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:list(pool.map(lambda _:cli(retention_prepare,'retention_prepare'),range(2)))
  def retention_snapshot(database):
   return sql("SELECT jsonb_build_array((SELECT to_jsonb(u) FROM heimdall_retention.upgrade u),(SELECT to_jsonb(b) FROM heimdall_retention.backfill b),(SELECT to_jsonb(c) FROM heimdall_retention.capacity c),(SELECT to_jsonb(m) FROM heimdall.schema_metadata m),(SELECT to_jsonb(p) FROM heimdall_capacity.policy p),(SELECT count(*) FROM heimdall.audit_events),(SELECT count(*) FROM heimdall_retention.event_identities),(SELECT count(*) FROM heimdall_retention.decision_identities))",database)
  stable=retention_snapshot('retention_prepare');cli(retention_prepare,'retention_prepare');assert retention_snapshot('retention_prepare')==stable
  assert 'conflict' in cli(retention_prepare[:-1]+['8192001'],'retention_prepare',1).stderr
  assert retention_snapshot('retention_prepare')==stable
  assert sql("SELECT state||':'||database_version FROM heimdall.schema_metadata",'retention_prepare')=='migrating:3'
  assert sql("SELECT used_rows||':'||reserved_rows FROM heimdall_retention.capacity",'retention_prepare')=='0:0'
  assert sql("SELECT has_schema_privilege('public_capacity_runtime','heimdall_retention','USAGE')",'retention_prepare')=='f'
  selected=dict(env,HEIMDALL_TEST_RETENTION_PREPARED_URL=dsn('retention_prepare'))
  go('^TestRetention(UpgradeInput|PreparedReadiness)$',selected)
  run(['pnpm','--filter','@heimdall/sdk','exec','vitest','run','test/postgres-retention-upgrade.test.ts'],env=selected)
  # Changed retention structure must fail on retry rather than normalize the schema.
  sql('ALTER TABLE heimdall_retention.capacity ADD COLUMN unexpected text','retention_prepare')
  cli(retention_prepare,'retention_prepare',1)
  with CommitLossProxy(int(port)) as proxy:
   assert 'outcome_unknown' in cli(retention_prepare,'retention_prepare_loss',1,url=dsn('retention_prepare_loss',proxy.port)).stderr
   assert proxy.dropped.is_set() and not proxy.errors
  stable=retention_snapshot('retention_prepare_loss');cli(retention_prepare,'retention_prepare_loss');assert retention_snapshot('retention_prepare_loss')==stable
  print('Retention preparation passed: exact quotas, rollback, catalog, concurrent retries, old-runtime refusal and lost COMMIT',flush=True)
  retention_fixture=json.loads((ROOT/'contracts/fixtures/audit-retention-backfill.json').read_text())
  retention_backfill=['--backfill-retention-upgrade','--writers-drained','--batch-size',str(retention_fixture['batchSize'])]
  for database in ['retention_backfill','retention_backfill_loss','retention_backfill_decision_loss','retention_backfill_concurrent','retention_backfill_bad','retention_backfill_quota','retention_backfill_skip','retention_backfill_rewind']:
   sql('CREATE DATABASE '+database+' TEMPLATE retention_prepare_loss')
  def retention_advance(database,batch=2,url=None):return json.loads(cli(retention_backfill[:-1]+[str(batch)],database,url=url).stdout)
  def retention_finish(database,batch=2):
   steps=[]
   for _ in range(200):
    p=retention_advance(database,batch);steps.append(p)
    if p['phase']=='complete':return steps
   raise RuntimeError('retention backfill did not finish')
  for bad in [retention_backfill[:1]+retention_backfill[2:],retention_backfill[:-1]+['0'],retention_backfill[:-1]+['101'],retention_backfill+['--retention-rows','1'],retention_backfill+['--sql-directory',source],retention_backfill+['--prepare-retention-upgrade']]:cli(bad,'retention_backfill',2)
  cli(retention_backfill,'cli_activation',1)
  stable=retention_snapshot('retention_backfill')
  cli(retention_backfill,'retention_backfill',1,url=dsn('retention_backfill')+'&options=-c%20default_transaction_read_only%3Don')
  assert retention_snapshot('retention_backfill')==stable
  steps=retention_finish('retention_backfill')
  expected_events=int(sql('SELECT count(*) FROM heimdall.audit_events','retention_backfill'))
  expected_decisions=int(sql('SELECT count(*) FROM heimdall.audit_decisions','retention_backfill'))
  assert expected_events==retention_fixture['mixedHistory']['events'] and expected_decisions==retention_fixture['mixedHistory']['decisions']
  assert steps[-1]['scannedEvents']==expected_events and steps[-1]['scannedDecisions']==expected_decisions
  assert max(p['processed'] for p in steps)<=2 and {p['phase'] for p in steps}==set(retention_fixture['phases'])
  assert retention_finish('retention_prepare_small')[-1]['scannedEvents']==expected_events
  initialize('retention_empty');cli(prepare,'retention_empty');finish('retention_empty');cli(activate,'retention_empty');cli(retention_prepare,'retention_empty')
  empty=retention_finish('retention_empty');assert empty[-1]['scannedEvents']==0 and empty[-1]['scannedDecisions']==0
  stable=retention_snapshot('retention_backfill');assert retention_advance('retention_backfill')['processed']==0
  cli(retention_prepare,'retention_backfill');assert retention_snapshot('retention_backfill')==stable
  assert sql("SELECT used_rows||':'||reserved_rows FROM heimdall_retention.capacity",'retention_backfill')==str(expected_events+expected_decisions)+':'+str(retention_fixture['mixedHistory']['reservedRows'])
  assert sql("SELECT state||':'||database_version FROM heimdall.schema_metadata",'retention_backfill')=='migrating:3'
  with concurrent.futures.ThreadPoolExecutor(max_workers=4) as pool:pages=list(pool.map(lambda _:retention_advance('retention_backfill_concurrent',1),range(4)))
  assert sorted(p['scannedEvents'] for p in pages)==[1,2,3,4]
  assert retention_finish('retention_backfill_concurrent')[-1]['scannedEvents']==expected_events
  for database,phase in [('retention_backfill_loss','ordinary'),('retention_backfill_decision_loss','decisions')]:
   if phase=='decisions':
    for _ in range(200):
     if retention_advance(database,100)['phase']=='decisions':break
    else:raise RuntimeError('retention ordinary phase did not finish')
   before=json.loads(sql('SELECT to_jsonb(b) FROM heimdall_retention.backfill b',database))
   with CommitLossProxy(int(port)) as proxy:
    assert 'outcome_unknown' in cli(retention_backfill,database,1,url=dsn(database,proxy.port)).stderr
    assert proxy.dropped.is_set() and not proxy.errors
   after=json.loads(sql('SELECT to_jsonb(b) FROM heimdall_retention.backfill b',database))
   assert after['scanned_events']>before['scanned_events']
   assert retention_finish(database)[-1]['scannedEvents']==expected_events
  # Corruption in the second ordinary source rolls back the first copied identity too.
  sql("WITH victim AS (SELECT product_id,environment,event_id FROM heimdall.audit_events WHERE event_id='ordinary' ORDER BY product_id COLLATE \"C\",environment COLLATE \"C\" OFFSET 1 LIMIT 1) UPDATE heimdall.audit_events e SET payload_digest=decode(repeat('00',32),'hex') FROM victim v WHERE (e.product_id,e.environment,e.event_id)=(v.product_id,v.environment,v.event_id)",'retention_backfill_bad')
  stable=retention_snapshot('retention_backfill_bad');cli(retention_backfill,'retention_backfill_bad',1);assert retention_snapshot('retention_backfill_bad')==stable
  sql('UPDATE heimdall_retention.capacity SET row_limit=1; UPDATE heimdall_retention.upgrade SET row_limit=1','retention_backfill_quota')
  stable=retention_snapshot('retention_backfill_quota');cli(retention_backfill,'retention_backfill_quota',1);assert retention_snapshot('retention_backfill_quota')==stable
  retention_advance('retention_backfill_rewind',2)
  sql("UPDATE heimdall_retention.backfill SET after_product='aaa',after_environment='aaa',after_id='aaa'",'retention_backfill_rewind')
  stable=retention_snapshot('retention_backfill_rewind');cli(retention_backfill,'retention_backfill_rewind',1);assert retention_snapshot('retention_backfill_rewind')==stable
  retention_advance('retention_backfill_skip',1)
  sql("UPDATE heimdall_retention.backfill SET after_product='zz',after_environment='zz',after_id='zz'",'retention_backfill_skip')
  stable=retention_snapshot('retention_backfill_skip');cli(retention_backfill,'retention_backfill_skip',1);assert retention_snapshot('retention_backfill_skip')==stable
  sql("SET session_replication_role=replica; DELETE FROM heimdall_retention.event_identities WHERE event_id='ordinary'; SET session_replication_role=origin",'retention_backfill')
  cli(retention_backfill,'retention_backfill',1)
  sql('CREATE DATABASE retention_backfill_budget TEMPLATE cli_activation')
  go('^TestRetentionBackfillBudgetSeed$',dict(env,HEIMDALL_TEST_RETENTION_BUDGET_SEED_URL=dsn('retention_backfill_budget')))
  cli(retention_prepare,'retention_backfill_budget')
  budget=retention_fixture['budgetHistory']
  first=retention_advance('retention_backfill_budget',budget['maxBatchSize']);assert 1<first['processed']<budget['maxBatchSize']
  assert retention_finish('retention_backfill_budget',budget['maxBatchSize'])[-1]['scannedEvents']==expected_events+budget['extraEvents']
  print('Retention backfill passed: bounded groups and bytes, complete history, atomic quota and checkpoint, concurrent processes, lost COMMIT and corruption refusal',flush=True)
  retention_activate=['--activate-retention-upgrade','--writers-drained']
  for database in ['retention_activation','retention_activation_loss','retention_activation_payload','retention_activation_tenant','retention_activation_missing','retention_activation_publication','retention_activation_readonly_validation','retention_activation_catalog']:
   sql('CREATE DATABASE '+database+' TEMPLATE retention_backfill_concurrent')
  for bad in [retention_activate[:1],retention_activate+['--batch-size','1'],retention_activate+['--retention-rows','1'],retention_activate+['--sql-directory',source],retention_activate+['--activate-capacity-upgrade']]:cli(bad,'retention_activation',2)
  cli(retention_activate,'retention_prepare_loss',1)
  cli(retention_activate,'retention_activation',1,url=dsn('retention_activation')+'&options=-c%20default_transaction_read_only%3Don')
  def retention_data_snapshot(database):
   return sql("SELECT jsonb_build_array((SELECT jsonb_agg(to_jsonb(e) ORDER BY product_id,environment,event_id) FROM heimdall_retention.event_identities e),(SELECT jsonb_agg(to_jsonb(d) ORDER BY product_id,environment,decision_id) FROM heimdall_retention.decision_identities d),(SELECT to_jsonb(c) FROM heimdall_retention.capacity c),(SELECT to_jsonb(b) FROM heimdall_retention.backfill b),(SELECT to_jsonb(u) FROM heimdall_retention.upgrade u))",database)
  before=retention_data_snapshot('retention_activation');cli(retention_activate,'retention_activation');assert retention_data_snapshot('retention_activation')==before
  stable=retention_snapshot('retention_activation');cli(retention_activate,'retention_activation');assert retention_snapshot('retention_activation')==stable
  assert sql("SELECT database_version||':'||state||':'||writer_min||':'||writer_max FROM heimdall.schema_metadata",'retention_activation')=='4:ready:4:4'
  cli(['--status','audit-v4'],'retention_activation');cli(['--status','managed-v4'],'retention_activation');cli(['--status','audit-v3'],'retention_activation',1)
  selected=dict(env,HEIMDALL_TEST_RETENTION_READY_URL=dsn('retention_activation'))
  go('^TestRetention(Readiness|ActivationInput)$',selected)
  run(['pnpm','--filter','@heimdall/sdk','exec','vitest','run','test/postgres-retention-readiness.test.ts'],env=selected)
  for database,mutation in [
   ('retention_activation_payload',"UPDATE heimdall.audit_events SET event=jsonb_set(event,'{payload}',to_jsonb('!'||substr(event->>'payload',2))) WHERE event_id='ordinary'"),
   ('retention_activation_tenant',"SET session_replication_role=replica; UPDATE heimdall_retention.event_identities SET tenant_id='other' WHERE event_id='ordinary'; SET session_replication_role=origin"),
   ('retention_activation_missing',"SET session_replication_role=replica; DELETE FROM heimdall_retention.event_identities WHERE event_id='ordinary'; SET session_replication_role=origin"),
   ('retention_activation_catalog',"ALTER TABLE heimdall_retention.event_identities ADD COLUMN unexpected text")]:
   sql(mutation,database);before=retention_data_snapshot(database);cli(retention_activate,database,1);assert retention_data_snapshot(database)==before
   assert sql("SELECT database_version||':'||state FROM heimdall.schema_metadata",database)=='3:migrating'
   assert sql('SELECT count(*) FROM heimdall.migrations WHERE version=4',database)=='0'
  sql("CREATE ROLE retention_publication_limited; GRANT USAGE ON SCHEMA heimdall,heimdall_capacity,heimdall_retention TO retention_publication_limited; GRANT ALL ON ALL TABLES IN SCHEMA heimdall,heimdall_capacity,heimdall_retention TO retention_publication_limited; REVOKE UPDATE ON heimdall.schema_metadata FROM retention_publication_limited",'retention_activation_publication')
  before=retention_data_snapshot('retention_activation_publication');cli(retention_activate,'retention_activation_publication',1,url=dsn('retention_activation_publication')+'&options=-c%20role%3Dretention_publication_limited');assert retention_data_snapshot('retention_activation_publication')==before
  assert sql('SELECT count(*) FROM heimdall.migrations WHERE version=4','retention_activation_publication')=='0'
  sql("CREATE ROLE retention_readonly_validator; GRANT USAGE ON SCHEMA heimdall,heimdall_capacity,heimdall_retention TO retention_readonly_validator; GRANT ALL ON ALL TABLES IN SCHEMA heimdall,heimdall_capacity,heimdall_retention TO retention_readonly_validator; REVOKE INSERT,UPDATE ON heimdall_retention.event_identities,heimdall_retention.decision_identities,heimdall_retention.capacity FROM retention_readonly_validator",'retention_activation_readonly_validation')
  cli(retention_activate,'retention_activation_readonly_validation',url=dsn('retention_activation_readonly_validation')+'&options=-c%20role%3Dretention_readonly_validator')
  with CommitLossProxy(int(port)) as proxy:
   assert 'outcome_unknown' in cli(retention_activate,'retention_activation_loss',1,url=dsn('retention_activation_loss',proxy.port)).stderr
   assert proxy.dropped.is_set() and not proxy.errors
  stable=retention_snapshot('retention_activation_loss');cli(retention_activate,'retention_activation_loss');assert retention_snapshot('retention_activation_loss')==stable
  cli(retention_activate,'retention_empty')
  resolution_env=dict(env,HEIMDALL_TEST_RETAINED_RESOLUTION_URL=dsn('retention_activation'))
  go('^TestRetainedResolution$',resolution_env)
  run(['pnpm','--filter','@heimdall/sdk','exec','vitest','run','test/postgres-retained-resolution.test.ts'],env=resolution_env)
  print('Retention activation passed: read-only semantic revalidation, atomic publication, old-writer exclusion, TS/Go readiness and lost COMMIT retry',flush=True)
  # Public factories run on an independent activated clone; v3 migration history stays fixed.
  sql('CREATE DATABASE public_v4_capacity TEMPLATE retention_activation')
  sql("GRANT USAGE ON SCHEMA heimdall,heimdall_capacity,heimdall_retention TO public_capacity_runtime; GRANT SELECT ON ALL TABLES IN SCHEMA heimdall,heimdall_capacity,heimdall_retention TO public_capacity_runtime; GRANT INSERT ON heimdall.audit_events,heimdall.audit_deliveries,heimdall_capacity.event_charges TO public_capacity_runtime; GRANT INSERT,UPDATE ON heimdall.audit_decisions,heimdall_capacity.reservations,heimdall_retention.event_identities,heimdall_retention.decision_identities TO public_capacity_runtime; GRANT UPDATE(used_events,used_bytes,used_deliveries,reserved_events,reserved_bytes,reserved_deliveries) ON heimdall_capacity.policy TO public_capacity_runtime; GRANT UPDATE(used_rows,reserved_rows) ON heimdall_retention.capacity TO public_capacity_runtime", 'public_v4_capacity')
  public_env=dict(env,HEIMDALL_TEST_CAPACITY_READY_URL=dsn('public_v4_capacity'))
  run(['pnpm','--filter','@heimdall/sdk','exec','vitest','run','test/postgres-public-capacity.test.ts'],env=public_env)
  subprocess.run(['go','test','-race','-count=1','.', '-run','^TestPublicCapacity$'],cwd=ROOT/'sdk/go',env=public_env,check=True)
  sql('CREATE ROLE retained_runtime_profile','retention_activation')
  runtime_grants="GRANT USAGE ON SCHEMA heimdall,heimdall_capacity,heimdall_retention TO retained_runtime_profile; GRANT SELECT ON heimdall.schema_metadata,heimdall.migrations,heimdall.audit_events,heimdall.audit_deliveries,heimdall.audit_decisions TO retained_runtime_profile; GRANT SELECT ON ALL TABLES IN SCHEMA heimdall_capacity,heimdall_retention TO retained_runtime_profile; GRANT INSERT ON heimdall.audit_events,heimdall.audit_deliveries,heimdall_capacity.event_charges TO retained_runtime_profile; GRANT INSERT,UPDATE ON heimdall.audit_decisions,heimdall_capacity.reservations,heimdall_retention.event_identities,heimdall_retention.decision_identities TO retained_runtime_profile; GRANT UPDATE(used_events,used_bytes,used_deliveries,reserved_events,reserved_bytes,reserved_deliveries) ON heimdall_capacity.policy TO retained_runtime_profile; GRANT UPDATE(used_rows,reserved_rows) ON heimdall_retention.capacity TO retained_runtime_profile"
  # Hold administration and retained audit commit in one v4 transaction.
  sql('CREATE DATABASE retained_holds_v4 TEMPLATE retention_activation')
  sql('CREATE ROLE retained_hold_runtime', 'retained_holds_v4')
  sql(runtime_grants.replace('retained_runtime_profile','retained_hold_runtime')+'; GRANT INSERT,UPDATE ON heimdall_retention.event_holds TO retained_hold_runtime', 'retained_holds_v4')
  sql("UPDATE heimdall_capacity.policy SET event_limit=100000,byte_limit=1000000000,delivery_limit=100000; UPDATE heimdall_capacity.upgrade SET event_limit=100000,byte_limit=1000000000,delivery_limit=100000; UPDATE heimdall_retention.capacity SET row_limit=100000,byte_limit=819200000; UPDATE heimdall_retention.upgrade SET row_limit=100000,byte_limit=819200000; ALTER TABLE heimdall_retention.event_holds ADD CONSTRAINT test_late_hold_failure CHECK(hold_id<>'rollback')", 'retained_holds_v4')
  hold_env=dict(env,HEIMDALL_TEST_HOLD_URL=dsn('retained_holds_v4')+'&options=-c%20role%3Dretained_hold_runtime',HEIMDALL_TEST_HOLD_ADMIN=dsn('retained_holds_v4'))
  run(['pnpm','--filter','@heimdall/sdk','exec','vitest','run','test/postgres-hold.test.ts'],env=hold_env)
  go('^TestRetainedHolds$',hold_env)
  go('^TestRetainedHolds$',dict(hold_env,HEIMDALL_TEST_HOLD_PEER='1'))
  run(['pnpm','--filter','@heimdall/sdk','exec','vitest','run','test/postgres-hold.test.ts'],env=dict(hold_env,HEIMDALL_TEST_HOLD_PEER='1'))
  print('Retained holds passed: atomic acquire and release, immutable retries, late rollback and reciprocal TS/Go receipts',flush=True)
  # Prepared hold release accounting, independent of runtime protocol publication.
  sql('CREATE DATABASE hold_release_reservations TEMPLATE retained_holds_v4')
  database='hold_release_reservations'
  reserve_sql=(ROOT/'contracts/persistence/session/0005-hold-release-reservations.sql').read_text()
  def refusal(statement, reason):
   try:
    sql(statement,database)
   except RuntimeError as error:
    assert reason in str(error),str(error)
   else:
    raise AssertionError('hold reserve accepted invalid operation')
  sql('UPDATE heimdall_capacity.policy SET event_limit=used_events+reserved_events',database)
  refusal('BEGIN;'+reserve_sql+'COMMIT;', 'insufficient audit hold release capacity')
  assert sql("SELECT to_regclass('heimdall_retention.hold_release_reservations') IS NULL",database)=='t'
  sql('UPDATE heimdall_capacity.policy SET event_limit=100000',database)
  sql('BEGIN;'+reserve_sql+'COMMIT;',database)
  assert sql('SELECT active_count FROM heimdall_retention.hold_release_capacity',database)=='2'
  assert sql("SELECT count(*) FROM heimdall_retention.hold_release_reservations WHERE release_event_id IS NOT NULL",database)=='2'
  assert sql("SELECT database_version FROM heimdall.schema_metadata",database)=='4'
  sql("""UPDATE heimdall_capacity.policy SET
    event_limit=used_events+reserved_events+2,
    byte_limit=used_bytes+reserved_bytes+3000000,
    delivery_limit=used_deliveries+reserved_deliveries+64;
   UPDATE heimdall_retention.capacity SET row_limit=used_rows+reserved_rows+2,
    byte_limit=(used_rows+reserved_rows+2)*8192""",database)
  for relation,column in [('heimdall_capacity.policy','event_limit'),('heimdall_capacity.policy','byte_limit'),
      ('heimdall_capacity.policy','delivery_limit'),('heimdall_retention.capacity','row_limit'),
      ('heimdall_retention.capacity','byte_limit')]:
   refusal(f'UPDATE {relation} SET {column}={column}-1','insufficient audit hold release capacity')
  consume="""UPDATE heimdall_retention.hold_release_reservations SET release_event_id=(
    SELECT release_event_id FROM heimdall_retention.event_holds
    WHERE product_id='holds-ts' AND environment='production' AND hold_id='hold-1')
    WHERE product_id='holds-ts' AND environment='production' AND hold_id='retry-1'"""
  sql('BEGIN;'+consume+'; ROLLBACK;',database)
  assert sql('SELECT active_count FROM heimdall_retention.hold_release_capacity',database)=='2'
  refusal('BEGIN;'+consume+'; COMMIT;', 'hold release reservation does not match hold')
  assert sql('SELECT active_count FROM heimdall_retention.hold_release_capacity',database)=='2'
  refusal("SET ROLE retained_hold_runtime; UPDATE heimdall_retention.hold_release_capacity SET active_count=0",
    'permission denied')
  # A legacy charge reaches the same database quota guard.
  ordinary_charge="""SET LOCAL ROLE retained_hold_runtime;
   INSERT INTO heimdall.audit_events(product_id,environment,event_id,payload_digest,recorded_at,event)
   SELECT product_id,environment,'reserve-probe',payload_digest,recorded_at,
     jsonb_set(event,'{eventId}','"reserve-probe"')
   FROM heimdall.audit_events
   WHERE product_id='holds-ts' AND environment='production' AND event_id='ordinary';
   INSERT INTO heimdall_capacity.event_charges(product_id,environment,event_id)
     VALUES('holds-ts','production','reserve-probe')"""
  refusal('BEGIN;'+ordinary_charge+'; COMMIT;', 'insufficient audit hold release capacity')
  assert sql("SELECT count(*) FROM heimdall.audit_events WHERE event_id='reserve-probe'",database)=='0'
  legacy_release="""UPDATE heimdall_retention.event_holds AS h
   SET released_at=prior.released_at, release_event_id=prior.release_event_id
   FROM heimdall_retention.event_holds AS prior
   WHERE h.product_id='holds-ts' AND h.environment='production' AND h.hold_id='retry-1'
     AND prior.product_id=h.product_id AND prior.environment=h.environment AND prior.hold_id='hold-1'"""
  refusal('BEGIN;'+legacy_release+'; COMMIT;', 'hold release reservation does not match hold')
  assert sql("SELECT released_at IS NULL FROM heimdall_retention.event_holds WHERE product_id='holds-ts' AND hold_id='retry-1'",database)=='t'
  print('Hold release preparation passed: adoption, five exact-fit limits, rollback, deferred binding and quota privileges',flush=True)
  # BEGIN hold release CLI proof
  database='hold_release_prepare_cli'
  sql('CREATE DATABASE '+database+' TEMPLATE retained_holds_v4')
  sql('ALTER TABLE heimdall_retention.event_holds DROP CONSTRAINT test_late_hold_failure',database)
  command=['--prepare-hold-release-upgrade','--writers-drained','--sql-directory',source]
  for invalid in [command[:1]+command[2:],command+['--batch-size','1'],command+['--activate-retention-upgrade']]:
   cli(invalid,database,2)
  cli(command,database,1,url=dsn(database)+'&options=-c%20default_transaction_read_only%3Don')
  sql('UPDATE heimdall_capacity.policy SET event_limit=used_events+reserved_events; UPDATE heimdall_capacity.upgrade SET event_limit=(SELECT event_limit FROM heimdall_capacity.policy)',database)
  assert 'audit_unavailable' in cli(command,database,1).stderr
  assert sql("SELECT state||':'||(to_regclass('heimdall_retention.hold_release_reservations') IS NULL)::text FROM heimdall.schema_metadata",database)=='ready:true'
  sql('UPDATE heimdall_capacity.policy SET event_limit=100000; UPDATE heimdall_capacity.upgrade SET event_limit=100000',database)
  with CommitLossProxy(int(port)) as proxy:
   assert 'outcome_unknown' in cli(command,database,1,url=dsn(database,proxy.port)).stderr
   assert proxy.dropped.is_set() and not proxy.errors
  assert sql("SELECT database_version||':'||state FROM heimdall.schema_metadata",database)=='4:migrating'
  cli(['--status','audit-v4'],database,1)
  cli(['--status','managed-v4'],database,1)
  before=sql("SELECT jsonb_agg(to_jsonb(r) ORDER BY product_id,environment,hold_id) FROM heimdall_retention.hold_release_reservations r",database)
  cli(command,database)
  assert sql("SELECT jsonb_agg(to_jsonb(r) ORDER BY product_id,environment,hold_id) FROM heimdall_retention.hold_release_reservations r",database)==before
  assert sql('SELECT active_count FROM heimdall_retention.hold_release_capacity',database)=='2'
  sql('UPDATE heimdall_retention.hold_release_capacity SET active_count=1',database)
  assert 'audit_unavailable' in cli(command,database,1).stderr
  sql('UPDATE heimdall_retention.hold_release_capacity SET active_count=2; ALTER TABLE heimdall_retention.hold_release_reservations ADD COLUMN drift text',database)
  assert 'audit_unavailable' in cli(command,database,1).stderr
  print('Hold release CLI preparation passed: atomic v4 drain, insufficient quota, lost COMMIT retry, readonly and drift refusal',flush=True)
  # END hold release CLI proof
  # BEGIN v5 hold release proof
  def hold_test(which,settings):
   if which=='go':go('^TestRetainedHolds$',settings)
   else:run(['pnpm','--filter','@heimdall/sdk','exec','vitest','run','test/postgres-hold.test.ts'],env=settings)
  activate_release=['--activate-hold-release-upgrade','--writers-drained']
  for language,peer in [('ts','go'),('go','ts')]:
   database='hold_release_v5_'+language
   sql('CREATE DATABASE '+database+' TEMPLATE retained_holds_v4')
   sql('ALTER TABLE heimdall_retention.event_holds DROP CONSTRAINT test_late_hold_failure',database)
   cli(activate_release,database,1)
   cli(command,database)
   cli(['--status','audit-v5'],database,1)
   if language=='ts':
    with CommitLossProxy(int(port)) as proxy:
     assert 'outcome_unknown' in cli(activate_release,database,1,url=dsn(database,proxy.port)).stderr
     assert proxy.dropped.is_set() and not proxy.errors
   cli(activate_release,database)
   cli(['--status','audit-v4'],database,1)
   cli(['--status','audit-v5'],database)
   cli(['--status','managed-v5'],database)
   sql('GRANT SELECT,UPDATE(release_event_id) ON heimdall_retention.hold_release_reservations TO retained_hold_runtime',database)
   role='&options=-c%20role%3Dretained_hold_runtime'
   selected=dict(env,HEIMDALL_TEST_HOLD_URL=dsn(database)+role,HEIMDALL_TEST_HOLD_ADMIN=dsn(database),HEIMDALL_TEST_HOLD_PROFILE=language)
   hold_test(language,dict(selected,HEIMDALL_TEST_HOLD_OPERATION='acquire'))
   assert sql('SELECT active_count FROM heimdall_retention.hold_release_capacity',database)=='3'
   sql("""UPDATE heimdall_capacity.policy SET event_limit=used_events+reserved_events+3,
    byte_limit=used_bytes+reserved_bytes+4500000,delivery_limit=used_deliveries+reserved_deliveries+96;
    UPDATE heimdall_capacity.upgrade u SET event_limit=p.event_limit,byte_limit=p.byte_limit,delivery_limit=p.delivery_limit FROM heimdall_capacity.policy p;
    UPDATE heimdall_retention.capacity SET row_limit=used_rows+reserved_rows+3,byte_limit=(used_rows+reserved_rows+3)*8192;
    UPDATE heimdall_retention.upgrade u SET row_limit=p.row_limit,byte_limit=p.byte_limit FROM heimdall_retention.capacity p""",database)
   decision_reserves="SELECT concat_ws(':',reserved_events,reserved_bytes,reserved_deliveries,(SELECT reserved_rows FROM heimdall_retention.capacity)) FROM heimdall_capacity.policy"
   reserved_before=sql(decision_reserves,database)
   hold_test(language,dict(selected,HEIMDALL_TEST_HOLD_OPERATION='concurrent',HEIMDALL_TEST_HOLD_EXPECT='audit_unavailable'))
   sql("ALTER TABLE heimdall_retention.event_holds ADD CONSTRAINT test_release_failure CHECK(hold_id<>'commit-loss' OR release_event_id IS NULL)",database)
   hold_test(language,dict(selected,HEIMDALL_TEST_HOLD_OPERATION='release',HEIMDALL_TEST_HOLD_EXPECT='audit_unavailable'))
   assert sql('SELECT active_count FROM heimdall_retention.hold_release_capacity',database)=='3'
   sql("ALTER TABLE heimdall_retention.event_holds DROP CONSTRAINT test_release_failure",database)
   with CommitLossProxy(int(port)) as proxy:
    hold_test(language,dict(selected,HEIMDALL_TEST_HOLD_OPERATION='release',HEIMDALL_TEST_HOLD_FAULT='1',HEIMDALL_TEST_HOLD_URL=dsn(database,proxy.port)+role))
    assert proxy.dropped.is_set() and not proxy.errors
   hold_test(peer,dict(selected,HEIMDALL_TEST_HOLD_OPERATION='release',HEIMDALL_TEST_HOLD_RETRY='1'))
   assert sql('SELECT active_count FROM heimdall_retention.hold_release_capacity',database)=='2'
   assert sql(decision_reserves,database)==reserved_before
   assert sql("SELECT count(*) FROM heimdall_retention.hold_release_reservations r JOIN heimdall_retention.event_holds h USING(product_id,environment,hold_id) WHERE r.release_event_id=h.release_event_id AND r.hold_id='commit-loss'",database)=='1'
   cli(activate_release,database)
  print('v5 hold release passed: activation, full quota, rollback, independent decision reserves and reciprocal lost COMMIT',flush=True)
  # END v5 hold release proof
  # BEGIN v5 exact-fit acquisition proof
  for language in ['ts','go']:
   charged=sql("SELECT c.envelope_bytes FROM heimdall_capacity.event_charges c JOIN heimdall_retention.event_holds h ON (c.product_id,c.environment,c.event_id)=(h.product_id,h.environment,h.placement_event_id) WHERE h.hold_id='commit-loss'",'hold_release_v5_'+language)
   assert charged.isdecimal()
   database='hold_release_exact_'+language
   sql('CREATE DATABASE '+database+' TEMPLATE retained_holds_v4')
   sql('ALTER TABLE heimdall_retention.event_holds DROP CONSTRAINT test_late_hold_failure',database)
   cli(command,database)
   cli(activate_release,database)
   sql('GRANT SELECT,UPDATE(release_event_id) ON heimdall_retention.hold_release_reservations TO retained_hold_runtime',database)
   selected=dict(env,HEIMDALL_TEST_HOLD_URL=dsn(database)+'&options=-c%20role%3Dretained_hold_runtime',HEIMDALL_TEST_HOLD_ADMIN=dsn(database),HEIMDALL_TEST_HOLD_PROFILE=language,HEIMDALL_TEST_HOLD_OPERATION='acquire')
   exact=f"""UPDATE heimdall_capacity.policy SET event_limit=used_events+reserved_events+4,
    byte_limit=used_bytes+reserved_bytes+4500000+{charged},delivery_limit=used_deliveries+reserved_deliveries+97;
    UPDATE heimdall_retention.capacity SET row_limit=used_rows+reserved_rows+5,byte_limit=(used_rows+reserved_rows+5)*8192;"""
   receipts="""UPDATE heimdall_capacity.upgrade u SET event_limit=p.event_limit,byte_limit=p.byte_limit,delivery_limit=p.delivery_limit FROM heimdall_capacity.policy p;
    UPDATE heimdall_retention.upgrade u SET row_limit=p.row_limit,byte_limit=p.byte_limit FROM heimdall_retention.capacity p"""
   for relation,column in [('heimdall_capacity.policy','event_limit'),('heimdall_capacity.policy','byte_limit'),
       ('heimdall_capacity.policy','delivery_limit'),('heimdall_retention.capacity','row_limit'),
       ('heimdall_retention.capacity','byte_limit')]:
    sql(exact+f'UPDATE {relation} SET {column}={column}-1;'+receipts,database)
    hold_test(language,dict(selected,HEIMDALL_TEST_HOLD_EXPECT='audit_unavailable'))
    assert sql('SELECT active_count FROM heimdall_retention.hold_release_capacity',database)=='2'
   sql(exact+receipts,database)
   hold_test(language,selected)
   assert sql('SELECT active_count FROM heimdall_retention.hold_release_capacity',database)=='3'
  print('v5 exact-fit acquisition passed in both SDKs, with one-unit overflow refusal in all five limits',flush=True)
  # END v5 exact-fit acquisition proof
  # BEGIN v5 concurrent release proof
  database='hold_release_exact_ts'
  simultaneous=dict(env,HEIMDALL_TEST_HOLD_URL=dsn(database)+'&options=-c%20role%3Dretained_hold_runtime',
   HEIMDALL_TEST_HOLD_ADMIN=dsn(database),HEIMDALL_TEST_HOLD_PROFILE='ts',HEIMDALL_TEST_HOLD_OPERATION='release')
  with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
   list(pool.map(lambda language:hold_test(language,simultaneous),['ts','go']))
  assert sql('SELECT active_count FROM heimdall_retention.hold_release_capacity',database)=='2'
  assert sql("""SELECT count(*) FROM heimdall_retention.event_holds h
    JOIN heimdall_capacity.event_charges c ON (c.product_id,c.environment,c.event_id)=(h.product_id,h.environment,h.release_event_id)
    WHERE h.hold_id='commit-loss'""",database)=='1'
  print('v5 concurrent TS/Go release passed with one audit charge',flush=True)
  # END v5 concurrent release proof
  # BEGIN v5 public factory proof
  database='hold_release_public_v5'
  sql('CREATE DATABASE '+database+' TEMPLATE retention_activation')
  cli(command,database)
  cli(activate_release,database)
  sql(runtime_grants.replace('retained_runtime_profile','public_capacity_runtime'),database)
  public_v5=dict(env,HEIMDALL_TEST_CAPACITY_READY_URL=dsn(database))
  run(['pnpm','--filter','@heimdall/sdk','exec','vitest','run','test/postgres-public-capacity.test.ts'],env=public_v5)
  subprocess.run(['go','test','-race','-count=1','.', '-run','^TestPublicCapacity$'],cwd=ROOT/'sdk/go',env=public_v5,check=True)
  assert sql("SELECT database_version FROM heimdall.schema_metadata",database)=='5'
  print('v5 public factory proof passed in TS and Go',flush=True)
  # END v5 public factory proof
  # Exercise the authorization boundary with the real v5 append port.
  database='maintenance_authorization_v5'
  sql('CREATE DATABASE '+database+' TEMPLATE hold_release_public_v5')
  sql("""UPDATE heimdall_capacity.policy SET event_limit=100000,byte_limit=1000000000,delivery_limit=100000;
   UPDATE heimdall_capacity.upgrade SET event_limit=100000,byte_limit=1000000000,delivery_limit=100000;
   UPDATE heimdall_retention.capacity SET row_limit=100000,byte_limit=819200000;
   UPDATE heimdall_retention.upgrade SET row_limit=100000,byte_limit=819200000""",database)
  maintenance_env=dict(env,HEIMDALL_TEST_MAINTENANCE_AUDIT_URL=dsn(database)+'&options=-c%20role%3Dpublic_capacity_runtime')
  subprocess.run(['go','test','-race','-count=1','./internal/auditflow','-run','^TestAuditedMaintenance$'],cwd=ROOT/'sdk/go',env=maintenance_env,check=True)
  run(['pnpm','--filter','@heimdall/sdk','exec','vitest','run','test/audited-authentication.test.ts','-t','audited maintenance'],env=maintenance_env)
  assert sql("SELECT count(*) FROM heimdall_retention.decision_identities WHERE product_id='ledger' AND environment='production' AND tenant_id IS NULL AND resolution='completed'",database)=='10'
  print('Audited maintenance passed with durable v5 requests and decisions in TS and Go',flush=True)
  # BEGIN audited retirement proof
  retirement_grants=(runtime_grants.replace('retained_runtime_profile','retained_retirement_runtime')+
    '; GRANT INSERT ON heimdall_retention.retirements TO retained_retirement_runtime; GRANT UPDATE(state) ON heimdall.audit_deliveries TO retained_retirement_runtime; GRANT DELETE ON heimdall.audit_events,heimdall.audit_deliveries,heimdall.audit_decisions,heimdall_capacity.event_charges,heimdall_capacity.reservations TO retained_retirement_runtime')
  database='retirement_runtime_v5'
  sql('CREATE DATABASE '+database+' TEMPLATE retained_holds_v4')
  sql('ALTER TABLE heimdall_retention.event_holds DROP CONSTRAINT test_late_hold_failure',database)
  cli(command,database)
  cli(activate_release,database)
  sql('CREATE ROLE retained_retirement_runtime',database)
  sql(retirement_grants,database)
  role='&options=-c%20role%3Dretained_retirement_runtime'
  retirement_env=dict(env,HEIMDALL_TEST_RETIRE_URL=dsn(database)+role,HEIMDALL_TEST_RETIRE_ADMIN=dsn(database))
  def retire_test(language,settings):
   if language=='go':go('^TestRetainedRetirement$',settings)
   else:run(['pnpm','--filter','@heimdall/sdk','exec','vitest','run','test/postgres-retire.test.ts'],env=settings)
  for language in ['ts','go']:
   retire_test(language,dict(retirement_env,HEIMDALL_TEST_RETIRE_CASE='ordinary',HEIMDALL_TEST_RETIRE_EXPECT='conflict'))
  sql("UPDATE heimdall.audit_deliveries SET state='delivered',delivered_at=6001",database)
  for language in ['ts','go']:
   for case in ['pending','held','too-young','wrong-tenant']:
    retire_test(language,dict(retirement_env,HEIMDALL_TEST_RETIRE_CASE=case))
   retire_test(language,dict(retirement_env,HEIMDALL_TEST_RETIRE_CASE='ordinary',HEIMDALL_TEST_RETIRE_EXPECT='unavailable',HEIMDALL_TEST_RETIRE_URL=dsn(database)+'&options=-c%20role%3Dretained_hold_runtime'))
  sql("""CREATE FUNCTION heimdall.test_retire_failure() RETURNS trigger LANGUAGE plpgsql AS $$
   BEGIN RAISE EXCEPTION 'test retirement rollback' USING ERRCODE='23514'; END $$;
   CREATE TRIGGER test_retire_failure BEFORE DELETE ON heimdall.audit_events FOR EACH ROW EXECUTE FUNCTION heimdall.test_retire_failure()""",database)
  for language in ['ts','go']:
   retire_test(language,dict(retirement_env,HEIMDALL_TEST_RETIRE_CASE='ordinary',HEIMDALL_TEST_RETIRE_EXPECT='audit_unavailable'))
  sql('DROP TRIGGER test_retire_failure ON heimdall.audit_events; DROP FUNCTION heimdall.test_retire_failure()',database)
  for case,first,second,product,environment,count in [('ordinary','go','ts','ledger','staging',1),
    ('completed','ts','go','ledger','staging',2),('recovered','go','ts','support','production',3)]:
   selected=dict(retirement_env,HEIMDALL_TEST_RETIRE_CASE=case)
   where=f"product_id='{product}' AND environment='{environment}'"
   charged_before=int(sql('SELECT used_events FROM heimdall_capacity.policy',database))
   identities_before=int(sql('SELECT count(*) FROM heimdall_retention.event_identities',database))
   metadata_before=int(sql('SELECT used_rows FROM heimdall_retention.capacity',database))
   if case=='completed':
    retire_test(first,selected)
   else:
    with CommitLossProxy(int(port)) as proxy:
     retire_test(first,dict(selected,HEIMDALL_TEST_RETIRE_EXPECT='outcome_unknown',HEIMDALL_TEST_RETIRE_URL=dsn(database,proxy.port)+role))
     assert proxy.dropped.is_set() and not proxy.errors
   retire_test(second,dict(selected,HEIMDALL_TEST_RETIRE_RETRY='1'))
   assert sql(f"SELECT jsonb_array_length(member_event_ids) FROM heimdall_retention.retirements WHERE {where} AND retirement_id='retire-{case}'",database)==str(count)
   assert int(sql('SELECT used_events FROM heimdall_capacity.policy',database))==charged_before+1-count
   assert int(sql('SELECT count(*) FROM heimdall_retention.event_identities',database))==identities_before+1
   assert int(sql('SELECT used_rows FROM heimdall_retention.capacity',database))==metadata_before+2
   assert sql("""SELECT (p.used_events,p.used_bytes,p.used_deliveries)=(c.events,c.bytes,c.deliveries)
    FROM heimdall_capacity.policy p CROSS JOIN (
      SELECT count(*) AS events,coalesce(sum(envelope_bytes),0) AS bytes,coalesce(sum(delivery_rows),0) AS deliveries
      FROM heimdall_capacity.event_charges
    ) c""",database)=='t'
  print('Audited retirement passed: all group shapes, refusal, restricted role, rollback and cross-language receipts',flush=True)
  # END audited retirement proof
  # BEGIN TS retirement loss proof
  database='retirement_runtime_ts_loss'
  sql('CREATE DATABASE '+database+' TEMPLATE hold_release_public_v5')
  sql(retirement_grants,database)
  sql("UPDATE heimdall.audit_deliveries SET state='delivered',delivered_at=6001",database)
  role='&options=-c%20role%3Dretained_retirement_runtime'
  selected=dict(env,HEIMDALL_TEST_RETIRE_URL=dsn(database)+role,HEIMDALL_TEST_RETIRE_ADMIN=dsn(database),HEIMDALL_TEST_RETIRE_CASE='completed')
  with CommitLossProxy(int(port)) as proxy:
   retire_test('ts',dict(selected,HEIMDALL_TEST_RETIRE_EXPECT='outcome_unknown',HEIMDALL_TEST_RETIRE_URL=dsn(database,proxy.port)+role))
   assert proxy.dropped.is_set() and not proxy.errors
  retire_test('go',dict(selected,HEIMDALL_TEST_RETIRE_RETRY='1'))
  print('TS retirement lost COMMIT recovered by Go without another write',flush=True)
  # END TS retirement loss proof
  # BEGIN retained hold fault proof
  for language in ['ts','go']:
   database='retained_holds_loss_'+language
   sql('CREATE DATABASE '+database+' TEMPLATE retained_holds_v4')
   selected=dict(env,HEIMDALL_TEST_HOLD_URL=dsn(database)+'&options=-c%20role%3Dretained_hold_runtime',HEIMDALL_TEST_HOLD_ADMIN=dsn(database),HEIMDALL_TEST_HOLD_PROFILE=language)
   for operation in ['acquire','release']:
    with CommitLossProxy(int(port)) as proxy:
     hold_test(language,dict(selected,HEIMDALL_TEST_HOLD_OPERATION=operation,HEIMDALL_TEST_HOLD_FAULT='1',HEIMDALL_TEST_HOLD_URL=dsn(database,proxy.port)+'&options=-c%20role%3Dretained_hold_runtime'))
     assert proxy.dropped.is_set() and not proxy.errors
    hold_test('go' if language=='ts' else 'ts',dict(selected,HEIMDALL_TEST_HOLD_OPERATION=operation,HEIMDALL_TEST_HOLD_RETRY='1'))
   concurrent_hold_env=dict(selected,HEIMDALL_TEST_HOLD_OPERATION='concurrent')
   writers=[subprocess.Popen(['go','test','-race','-count=1','./internal/postgres','-run','^TestRetainedHolds$'],cwd=ROOT/'sdk/go',env=concurrent_hold_env),subprocess.Popen(['pnpm','--filter','@heimdall/sdk','exec','vitest','run','test/postgres-hold.test.ts'],cwd=ROOT,env=concurrent_hold_env)]
   results=[writer.wait() for writer in writers]
   assert results==[0,0],results
   assert sql("SELECT count(*) FROM heimdall_retention.event_holds WHERE product_id='holds-"+language+"' AND hold_id='concurrent'",database)=='1'
   assert sql("SELECT count(*) FROM heimdall_retention.event_identities i JOIN heimdall.audit_events e USING(product_id,environment,event_id) WHERE i.product_id='holds-"+language+"'",database)=='7'
  print('Retained hold fault proof passed: reciprocal acquire/release COMMIT loss and concurrent TS/Go exact retries',flush=True)
  sql('CREATE DATABASE retained_holds_groups TEMPLATE retained_holds_v4')
  group_env=dict(env,HEIMDALL_TEST_HOLD_URL=dsn('retained_holds_groups')+'&options=-c%20role%3Dretained_hold_runtime',HEIMDALL_TEST_HOLD_ADMIN=dsn('retained_holds_groups'),HEIMDALL_TEST_HOLD_GROUP='1')
  go('^TestRetainedHolds$',group_env)
  run(['pnpm','--filter','@heimdall/sdk','exec','vitest','run','test/postgres-hold.test.ts'],env=group_env)
  print('Retained hold group proof passed: terminal hold protects complete decision and corrupt sibling refuses without writes',flush=True)
  # BEGIN retained hold readiness proof
  sql('CREATE DATABASE retained_holds_legacy TEMPLATE retained_holds_v4')
  sql('UPDATE heimdall.schema_metadata SET database_version=3', 'retained_holds_legacy')
  for database,options in [('retained_holds_legacy',''),('retained_holds_v4','&options=-c%20default_transaction_read_only%3Don%20-c%20role%3Dretained_hold_runtime')]:
   rejected=dict(env,HEIMDALL_TEST_HOLD_URL=dsn(database)+options,HEIMDALL_TEST_HOLD_ADMIN=dsn(database),HEIMDALL_TEST_HOLD_REJECT='1')
   go('^TestRetainedHolds$',rejected)
   run(['pnpm','--filter','@heimdall/sdk','exec','vitest','run','test/postgres-hold.test.ts'],env=rejected)
  print('Retained hold readiness proof passed: legacy and read-only refusal without writes',flush=True)
  # END retained hold readiness proof
  # END retained hold fault proof
  # Retained audit and local managed writes share one v4 transaction.
  sql('CREATE DATABASE managed_retained_v4 TEMPLATE retention_activation')
  sql('CREATE ROLE heimdall_managed_runtime', 'managed_retained_v4')
  sql(runtime_grants.replace('retained_runtime_profile','heimdall_managed_runtime')+"; GRANT USAGE ON SCHEMA heimdall_managed TO heimdall_managed_runtime; GRANT SELECT,INSERT,UPDATE ON ALL TABLES IN SCHEMA heimdall_managed TO heimdall_managed_runtime; GRANT USAGE ON ALL SEQUENCES IN SCHEMA heimdall_managed TO heimdall_managed_runtime", 'managed_retained_v4')
  sql("UPDATE heimdall_capacity.policy SET event_limit=event_limit+100; UPDATE heimdall_capacity.upgrade SET event_limit=(SELECT event_limit FROM heimdall_capacity.policy); UPDATE heimdall_retention.capacity SET row_limit=row_limit+100,byte_limit=byte_limit+819200; UPDATE heimdall_retention.upgrade SET row_limit=(SELECT row_limit FROM heimdall_retention.capacity),byte_limit=(SELECT byte_limit FROM heimdall_retention.capacity)", 'managed_retained_v4')
  sql("ALTER TABLE heimdall_retention.event_identities ADD CONSTRAINT managed_late_failure CHECK(product_id NOT IN ('atomic-go-metadata_failure','atomic-ts-metadata_failure'))", 'managed_retained_v4')
  managed_env=dict(env,HEIMDALL_TEST_MANAGED_DATABASE_URL=dsn('managed_retained_v4'),HEIMDALL_TEST_MANAGED_AUDIT_V4='1')
  go('^TestManagedAudit(Invalid|Atomic)$',managed_env)
  run(['pnpm','--filter','@heimdall/sdk','exec','vitest','run','test/postgres-managed-audit.test.ts'],env=managed_env)
  for language in ['go','ts']:
   with CommitLossProxy(int(port)) as proxy:
    selected=dict(managed_env,HEIMDALL_TEST_MANAGED_AUDIT_LOSS=dsn('managed_retained_v4',proxy.port))
    if language=='go':go('^TestManagedAuditCommitLoss$',selected)
    else:run(['pnpm','--filter','@heimdall/sdk','exec','vitest','run','test/postgres-managed-audit.test.ts','-t','committed response loss'],env=selected)
    assert proxy.dropped.is_set() and not proxy.errors
  assert sql("SELECT count(*) FROM heimdall.audit_events e LEFT JOIN heimdall_retention.event_identities i USING(product_id,environment,event_id) LEFT JOIN heimdall_capacity.event_charges c USING(product_id,environment,event_id) WHERE e.product_id LIKE 'atomic-%' AND (i.event_id IS NULL OR c.event_id IS NULL)", 'managed_retained_v4')=='0'
  assert sql("SELECT used_rows=(SELECT count(*) FROM heimdall_retention.event_identities)+(SELECT count(*) FROM heimdall_retention.decision_identities) FROM heimdall_retention.capacity", 'managed_retained_v4')=='t'
  assert sql("SELECT (SELECT count(*) FROM heimdall_retention.event_identities)=(SELECT count(*) FROM heimdall.audit_events)", 'managed_retained_v4')=='t'
  assert sql("SELECT used_events=(SELECT count(*) FROM heimdall_capacity.event_charges) FROM heimdall_capacity.policy", 'managed_retained_v4')=='t'
  print('Managed retained v4 atomic rollback and confirmed COMMIT-loss tests passed in TS and Go',flush=True)
  sql('CREATE DATABASE managed_operations_v4 TEMPLATE retention_activation')
  sql(runtime_grants.replace('retained_runtime_profile','heimdall_managed_runtime')+"; GRANT USAGE ON SCHEMA heimdall_managed TO heimdall_managed_runtime; GRANT SELECT ON heimdall_managed.schema_metadata,heimdall_managed.migrations TO heimdall_managed_runtime; GRANT SELECT,INSERT,UPDATE ON heimdall_managed.workspaces,heimdall_managed.operations,heimdall_managed.membership_candidates TO heimdall_managed_runtime; GRANT DELETE ON heimdall_managed.membership_candidates TO heimdall_managed_runtime; GRANT USAGE ON ALL SEQUENCES IN SCHEMA heimdall_managed TO heimdall_managed_runtime", 'managed_operations_v4')
  sql("UPDATE heimdall_capacity.policy SET event_limit=100000,byte_limit=1000000000,delivery_limit=100000; UPDATE heimdall_capacity.upgrade SET event_limit=100000,byte_limit=1000000000,delivery_limit=100000; UPDATE heimdall_retention.capacity SET row_limit=100000,byte_limit=819200000; UPDATE heimdall_retention.upgrade SET row_limit=100000,byte_limit=819200000", 'managed_operations_v4')
  sql("CREATE ROLE heimdall_managed_status; GRANT USAGE ON SCHEMA heimdall,heimdall_capacity,heimdall_retention,heimdall_managed TO heimdall_managed_status; GRANT SELECT ON heimdall.schema_metadata,heimdall.migrations,heimdall_managed.schema_metadata,heimdall_managed.migrations,heimdall_managed.workspaces TO heimdall_managed_status; GRANT SELECT ON ALL TABLES IN SCHEMA heimdall_capacity,heimdall_retention TO heimdall_managed_status", 'managed_operations_v4')
  sql('CREATE ROLE heimdall_export_reader NOLOGIN; GRANT USAGE ON SCHEMA heimdall_managed TO heimdall_export_reader; GRANT SELECT(product_id,environment,workspace_id,lifecycle,mutation_blocked,revision,created_at,updated_at) ON heimdall_managed.workspaces TO heimdall_export_reader; GRANT SELECT(product_id,environment,workspace_id,operation_id,kind,state,dispatch_count,generation,next_attempt_at,store_id,model_id,model_hash,manifest_version,created_at,updated_at) ON heimdall_managed.operations TO heimdall_export_reader; GRANT USAGE ON SCHEMA heimdall,heimdall_capacity,heimdall_retention TO heimdall_export_reader; GRANT SELECT ON heimdall.schema_metadata,heimdall.migrations,heimdall_managed.schema_metadata,heimdall_managed.migrations,heimdall_capacity.policy,heimdall_capacity.upgrade,heimdall_capacity.backfill,heimdall_retention.capacity,heimdall_retention.upgrade,heimdall_retention.backfill TO heimdall_export_reader;', 'managed_operations_v4')
  operation_env=dict(env,HEIMDALL_TEST_MANAGED_DATABASE_URL=dsn('managed_operations_v4'),HEIMDALL_TEST_MANAGED_OPERATIONS_V4='1')
  go('^TestRetainedOperationsAdmission$',operation_env)
  run(['pnpm','--filter','@heimdall/sdk','exec','vitest','run','test/postgres-operations-retained.test.ts'],env=operation_env)
  operation_files=['test/postgres-'+n+'.test.ts' for n in ['workspaces','operations','operation-transitions','membership-intents','candidate-repair','operation-scan','candidates','export']]
  for file in operation_files:run(['pnpm','--filter','@heimdall/sdk','exec','vitest','run',file],env=operation_env)
  go('^(TestOperation(Journal|Input|Create|Transition|TransitionInput|MembershipCompletion|Scan|ScanInput)|TestMembership(Input|Intent)|TestCandidate(Decoder|Input|Primary|Repair|RepairInput)|TestWorkspace(Decoder|Input|Primary)|TestExport(Decoder|Input|Primary))$',dict(operation_env,HEIMDALL_TEST_MANAGED_READ_PEER='1'))
  for file in operation_files:run(['pnpm','--filter','@heimdall/sdk','exec','vitest','run',file],env=dict(operation_env,HEIMDALL_TEST_MANAGED_READ_PEER='1'))
  for language in ['go','ts']:
   for variable,file,ts_name,go_name in [
    ('CREATE','operations','operation create committed response loss and retry','TestOperationCreateLoss'),
    ('TRANSITION','operation-transitions','transition committed response loss','TestOperationTransitionLoss'),
    ('MEMBERSHIP','membership-intents','membership intent committed response loss','TestMembershipIntentLoss'),
    ('REPAIR','candidate-repair','candidate repair committed response loss','TestCandidateRepairLoss')]:
    with CommitLossProxy(int(port)) as proxy:
     selected=dict(operation_env,**{'HEIMDALL_TEST_'+variable+'_LOSS':dsn('managed_operations_v4',proxy.port)})
     if language=='go':go('^'+go_name+'$',selected)
     else:run(['pnpm','--filter','@heimdall/sdk','exec','vitest','run','test/postgres-'+file+'.test.ts','-t',ts_name],env=selected)
     assert proxy.dropped.is_set() and not proxy.errors
  race_env=dict(operation_env,HEIMDALL_TEST_CREATE_RACE='1')
  with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
   attempts=[pool.submit(go,'^TestOperationCreateRaceCross$',race_env),pool.submit(run,['pnpm','--filter','@heimdall/sdk','exec','vitest','run','test/postgres-operations.test.ts','-t','cross-language create race'],env=race_env)]
   for attempt in attempts:attempt.result()
  for table in ['workspaces','operations']:
   assert sql("SELECT count(*) FROM heimdall_managed."+table+" WHERE product_id='creation-cross'", 'managed_operations_v4')=='1'
  assert sql("SELECT count(*) FROM heimdall.audit_events WHERE product_id='creation-cross'", 'managed_operations_v4')=='1'
  assert sql("SELECT count(*) FROM heimdall.audit_deliveries WHERE product_id='creation-cross'", 'managed_operations_v4')=='2'
  assert sql("SELECT count(*) FROM heimdall.audit_events e LEFT JOIN heimdall_retention.event_identities i USING(product_id,environment,event_id) LEFT JOIN heimdall_capacity.event_charges c USING(product_id,environment,event_id) WHERE i.event_id IS NULL OR c.event_id IS NULL", 'managed_operations_v4')=='0'
  print('Managed operation v4 runtime writes, bidirectional reads, state transitions and COMMIT-loss recovery passed',flush=True)
  def runtime_profile(language,selected):
   if language=='go':go('^TestRetainedRuntime$',selected)
   else:run(['pnpm','--filter','@heimdall/sdk','exec','vitest','run','test/postgres-retained-runtime.test.ts'],env=selected)
  for language in ['go','ts']:
   database='retained_runtime_'+language
   sql('CREATE DATABASE '+database+' TEMPLATE retention_activation');sql(runtime_grants,database)
   runtime_profile(language,dict(env,HEIMDALL_TEST_RETAINED_RUNTIME_URL=dsn(database)+'&options=-c%20role%3Dretained_runtime_profile',HEIMDALL_TEST_RETAINED_RUNTIME_ADMIN=dsn(database)))
   runtime_profile(language,dict(env,HEIMDALL_TEST_RETAINED_RUNTIME_URL=dsn('cli_activation'),HEIMDALL_TEST_RETAINED_RUNTIME_ADMIN=dsn('cli_activation'),HEIMDALL_TEST_RETAINED_RUNTIME_REJECT='1'))
   runtime_profile(language,dict(env,HEIMDALL_TEST_RETAINED_RUNTIME_URL=dsn(database)+'&options=-c%20default_transaction_read_only%3Don%20-c%20role%3Dretained_runtime_profile',HEIMDALL_TEST_RETAINED_RUNTIME_ADMIN=dsn(database),HEIMDALL_TEST_RETAINED_RUNTIME_REJECT='1'))
   stable=retention_snapshot(database);cli(retention_activate,database);assert retention_snapshot(database)==stable
  for first,peer in [('go','ts'),('ts','go')]:
   database='retained_runtime_loss_'+first
   sql('CREATE DATABASE '+database+' TEMPLATE retention_activation');sql(runtime_grants,database)
   selected=dict(env,HEIMDALL_TEST_RETAINED_RUNTIME_LOSS='1',HEIMDALL_TEST_RETAINED_RUNTIME_ADMIN=dsn(database))
   with CommitLossProxy(int(port)) as proxy:
    runtime_profile(first,dict(selected,HEIMDALL_TEST_RETAINED_RUNTIME_FAULT='1',HEIMDALL_TEST_RETAINED_RUNTIME_URL=dsn(database,proxy.port)+'&options=-c%20role%3Dretained_runtime_profile'))
    assert proxy.dropped.is_set() and not proxy.errors
   stable=sql(json.loads((ROOT/'contracts/fixtures/retained-writes.json').read_text())['snapshot'],database)
   runtime_profile(peer,dict(selected,HEIMDALL_TEST_RETAINED_RUNTIME_URL=dsn(database)+'&options=-c%20role%3Dretained_runtime_profile'))
   assert sql(json.loads((ROOT/'contracts/fixtures/retained-writes.json').read_text())['snapshot'],database)==stable
  print('Retained v4 runtime passed: scoped writes recovery reads eligibility legacy refusal restricted privileges and reciprocal COMMIT loss',flush=True)
  for language in ['go','ts']:
   for lost in [False,True]:
    database='retained_batch_'+language+('_loss' if lost else '')
    sql('CREATE DATABASE '+database+' TEMPLATE retention_activation')
    sql(runtime_grants.replace('retained_runtime_profile','public_capacity_runtime'),database)
    selected=dict(env,HEIMDALL_TEST_RECOVERY_BATCH_URL=dsn(database),HEIMDALL_TEST_RECOVERY_BATCH_V4='1')
    def retained_batch():
     if language=='go':go('^TestRecoveryBatchPostgres$',selected)
     else:run(['pnpm','--filter','@heimdall/sdk','exec','vitest','run','test/postgres-recovery-batch.test.ts'],env=selected)
    if lost:
     with CommitLossProxy(int(port)) as proxy:
      selected['HEIMDALL_TEST_RECOVERY_BATCH_LOSS']=dsn(database,proxy.port)
      retained_batch();assert proxy.dropped.is_set() and not proxy.errors
    else:retained_batch()
  for language in ['go','ts']:
   for lost in [False,True]:
    database='runtime_recovery_'+language+('_loss' if lost else '')
    sql('CREATE DATABASE '+database+' TEMPLATE cli_activation')
    selected=dict(env,HEIMDALL_TEST_RUNTIME_RECOVERY_URL=dsn(database))
    def recover():
     if language=='go':go('^TestRuntimeRecovery$',selected)
     else:run(['pnpm','--filter','@heimdall/sdk','exec','vitest','run','test/postgres-runtime-recovery.test.ts'],env=selected)
    if lost:
     with CommitLossProxy(int(port)) as proxy:
      selected['HEIMDALL_TEST_RUNTIME_RECOVERY_LOSS']=dsn(database,proxy.port)+'&options=-c%20role%3Dpublic_capacity_runtime'
      recover()
      assert proxy.dropped.is_set() and not proxy.errors
    else:recover()
  for language in ['go','ts']:
   for lost in [False,True]:
    database='recovery_batch_'+language+('_loss' if lost else '')
    sql('CREATE DATABASE '+database+' TEMPLATE cli_activation')
    selected=dict(env,HEIMDALL_TEST_RECOVERY_BATCH_URL=dsn(database))
    def batch():
     if language=='go':go('^TestRecoveryBatchPostgres$',selected)
     else:run(['pnpm','--filter','@heimdall/sdk','exec','vitest','run','test/postgres-recovery-batch.test.ts'],env=selected)
    if lost:
     with CommitLossProxy(int(port)) as proxy:
      selected['HEIMDALL_TEST_RECOVERY_BATCH_LOSS']=dsn(database,proxy.port)
      batch()
      assert proxy.dropped.is_set() and not proxy.errors
    else:batch()
  restart_fixture=json.loads((ROOT/'contracts/fixtures/recovery-restart.json').read_text())
  restart_binary=Path(directory)/'recovery-restart.test'
  subprocess.run(['go','test','-race','-c','-o',str(restart_binary),'./internal/postgres'],cwd=ROOT/'sdk/go',check=True)
  def restart_worker(language,mode,database):
   selected=dict(env,HEIMDALL_TEST_RECOVERY_RESTART_URL=dsn(database),HEIMDALL_TEST_RECOVERY_RESTART_MODE=mode)
   args=[str(restart_binary),'-test.run','^TestRecoveryRestartProcess$','-test.timeout','30s'] if language=='go' else ['node','--import','tsx',str(ROOT/'sdk/typescript/test/fixtures/recovery-restart-worker.ts')]
   cwd=ROOT/'sdk/go/internal/postgres' if language=='go' else ROOT/'docs'
   r=subprocess.run(args,cwd=cwd,env=selected,capture_output=True,text=True,timeout=40)
   expected=-9 if mode=='crash' else 0
   assert r.returncode==expected,(language,mode,r.returncode,r.stdout,r.stderr)
  for first,second in [('go','ts'),('ts','go')]:
   database='restart_'+first
   sql('CREATE DATABASE '+database+' TEMPLATE cli_activation')
   restart_worker(first,'seed',database)
   sql("UPDATE heimdall_capacity.policy SET event_limit=used_events+reserved_events; UPDATE heimdall_capacity.upgrade SET event_limit=(SELECT event_limit FROM heimdall_capacity.policy)",database)
   budget=json.loads(sql("SELECT json_build_array(used_events,reserved_events)::text FROM heimdall_capacity.policy",database))
   restart_worker(first,'crash',database)
   assert json.loads(sql("SELECT json_build_array(used_events,reserved_events)::text FROM heimdall_capacity.policy",database))==[budget[0]+2,budget[1]-2]
   assert sql("SELECT string_agg(resolution,',' ORDER BY decision_id) FROM heimdall.audit_decisions WHERE product_id='restart'",database)=='recovered,pending'
   assert int(sql("SELECT count(*) FROM heimdall.audit_events WHERE product_id='restart'",database))==restart_fixture['eventsAfterCrash']
   restart_worker(second,'resume',database)
   assert sql("SELECT string_agg(resolution,',' ORDER BY decision_id) FROM heimdall.audit_decisions WHERE product_id='restart'",database)=='recovered,recovered'
   assert int(sql("SELECT count(*) FROM heimdall.audit_events WHERE product_id='restart'",database))==restart_fixture['eventsAfterResume']
   assert json.loads(sql("SELECT json_build_array(used_events,reserved_events)::text FROM heimdall_capacity.policy",database))==[budget[0]+4,budget[1]-4]
   assert int(sql("SELECT count(*) FROM heimdall_capacity.event_charges WHERE product_id='restart'",database))==restart_fixture['eventsAfterResume']
   assert int(sql("SELECT count(*) FROM heimdall.audit_deliveries WHERE product_id='restart'",database))==restart_fixture['eventsAfterResume']

  identity_fixture=json.loads((ROOT/'contracts/fixtures/audit-retention-identities.json').read_text())
  identity_sql=(ROOT/'contracts/persistence'/identity_fixture['path']).read_text()
  assert hashlib.sha256(identity_sql.encode()).hexdigest()==identity_fixture['checksum']
  for language in ['go','ts']:
   database='retention_port_'+language
   sql('CREATE DATABASE '+database+' TEMPLATE cli_activation')
   sql(identity_sql,database)
   selected=dict(env,HEIMDALL_TEST_RETENTION_URL=dsn(database))
   if language=='go':go('^TestPreparedRetention$',selected)
   else:run(['pnpm','--filter','@heimdall/sdk','exec','vitest','run','test/postgres-retention.test.ts'],env=selected)
  for first,second in [('go','ts'),('ts','go')]:
   database='retention_loss_'+first
   sql('CREATE DATABASE '+database+' TEMPLATE cli_activation')
   sql(identity_sql,database)
   def retention_attempt(language,selected):
    if language=='go':go('^TestRetentionCommitLoss$',selected)
    else:run(['pnpm','--filter','@heimdall/sdk','exec','vitest','run','test/postgres-retention-loss.test.ts'],env=selected)
   selected=dict(env,HEIMDALL_TEST_RETENTION_LOSS_URL=dsn(database))
   with CommitLossProxy(int(port)) as proxy:
    retention_attempt(first,dict(selected,HEIMDALL_TEST_RETENTION_FAULT_URL=dsn(database,proxy.port)))
    assert proxy.dropped.is_set() and not proxy.errors,proxy.errors
   snapshot_query="SELECT jsonb_build_object('events',(SELECT jsonb_agg(to_jsonb(i) ORDER BY product_id,environment,event_id) FROM heimdall_retention.event_identities i),'decisions',(SELECT jsonb_agg(to_jsonb(d) ORDER BY product_id,environment,decision_id) FROM heimdall_retention.decision_identities d))"
   before=sql(snapshot_query,database)
   retention_attempt(second,selected)
   assert sql(snapshot_query,database)==before
  # Prepared hold structure only: existing audit events are reference stand-ins,
  # not proof of an authorized administrative hold action.
  hold_fixture=json.loads((ROOT/'contracts/fixtures/audit-retention-holds.json').read_text())
  hold_sql=(ROOT/'contracts/persistence'/hold_fixture['path']).read_text()
  assert hashlib.sha256(hold_sql.encode()).hexdigest()==hold_fixture['checksum']
  hold_db='retention_holds'
  sql('CREATE DATABASE '+hold_db+' TEMPLATE retention_port_go')
  sql('BEGIN;'+hold_sql+'ROLLBACK;',hold_db)
  assert sql("SELECT to_regclass('heimdall_retention.event_holds') IS NULL",hold_db)=='t'
  sql(hold_sql,hold_db)
  place="INSERT INTO heimdall_retention.event_holds(product_id,environment,hold_id,event_id,tenant_id,placed_at,placement_event_id) VALUES('ledger','staging','hold-1','z-request','tenant-1',1001,'ordinary')"
  release="UPDATE heimdall_retention.event_holds SET released_at=2001,release_event_id='normal-terminal' WHERE hold_id='hold-1'"
  def reject_hold(statement):
   r=subprocess.run(['docker','exec','-i',name,'psql','-U','postgres','-d',hold_db,'-At','-v','ON_ERROR_STOP=1','-v','VERBOSITY=verbose'],input='BEGIN;'+statement+';ROLLBACK;',capture_output=True,text=True)
   assert r.returncode!=0 and '23514' in r.stderr,r.stderr
  sql('BEGIN;'+place+';ROLLBACK;',hold_db)
  assert sql('SELECT count(*) FROM heimdall_retention.event_holds',hold_db)=='0'
  for statement in [place.replace("'tenant-1'","'other'"),place.replace("1001,'ordinary'","1000,'ordinary'"),place.replace("'ordinary'","'missing'"),place.replace("'staging'","'other'"),place.replace("'ordinary'","'z-request'"),"UPDATE heimdall.audit_events SET payload_digest=decode(repeat('00',32),'hex') WHERE product_id='ledger' AND environment='staging' AND event_id='ordinary';"+place]:reject_hold(statement)
  with concurrent.futures.ThreadPoolExecutor(max_workers=2) as workers:
   futures=[workers.submit(sql,place.replace('hold-1','hold-'+str(i)),hold_db) for i in [1,2]]
   for future in futures:future.result(timeout=10)
  assert sql('SELECT count(*) FROM heimdall_retention.event_holds WHERE released_at IS NULL',hold_db)=='2'
  for statement in ["DELETE FROM heimdall_retention.event_holds WHERE hold_id='hold-1'","UPDATE heimdall_retention.event_holds SET tenant_id='other' WHERE hold_id='hold-1'","UPDATE heimdall_retention.event_holds SET event_id='ordinary' WHERE hold_id='hold-1'",release.replace('2001','1000'),release.replace("'normal-terminal'","'ordinary'")]:reject_hold(statement)
  sql(release,hold_db)
  snapshot=sql("SELECT jsonb_agg(to_jsonb(h) ORDER BY hold_id) FROM heimdall_retention.event_holds h",hold_db)
  sql(release,hold_db)
  assert sql("SELECT jsonb_agg(to_jsonb(h) ORDER BY hold_id) FROM heimdall_retention.event_holds h",hold_db)==snapshot
  assert sql('SELECT count(*) FROM heimdall_retention.event_holds WHERE released_at IS NULL',hold_db)=='1'
  reject_hold("UPDATE heimdall_retention.event_holds SET released_at=NULL,release_event_id=NULL WHERE hold_id='hold-1'")
  reject_hold("UPDATE heimdall_retention.event_holds SET released_at=2002 WHERE hold_id='hold-1'")
  # A hold on the request protects a resolved decision's whole group, not only its target row.
  assert sql("SELECT EXISTS(SELECT 1 FROM heimdall_retention.event_holds h JOIN heimdall_retention.event_identities i USING(product_id,environment,event_id) WHERE h.product_id='ledger' AND h.environment='staging' AND i.decision_id='decision-1' AND h.released_at IS NULL)",hold_db)=='t'
  assert sql("SELECT has_table_privilege('public_capacity_runtime','heimdall_retention.event_holds','INSERT')",hold_db)=='f'
  retry_fixture=json.loads((ROOT/'contracts/fixtures/audit-retention-retry-holds.json').read_text())
  retry_sql=(ROOT/'contracts/persistence'/retry_fixture['path']).read_text()
  assert hashlib.sha256(retry_sql.encode()).hexdigest()==retry_fixture['checksum']
  sql('BEGIN;'+retry_sql+'ROLLBACK;',hold_db)
  assert sql("SELECT count(*) FROM information_schema.columns WHERE table_schema='heimdall_retention' AND table_name='event_holds' AND column_name='purpose'",hold_db)=='0'
  sql(retry_sql,hold_db)
  assert sql("SELECT bool_and(purpose='administrative') FROM heimdall_retention.event_holds",hold_db)=='t'
  retry_place=place.replace('placement_event_id)',"placement_event_id,purpose)").replace("1001,'ordinary')","1001,'ordinary','retry')").replace('hold-1','retry-1')
  sql(retry_place+';'+retry_place.replace('retry-1','retry-2'),hold_db)
  for statement in [retry_place.replace('retry-1','invalid').replace("'retry'","'unknown'"),"UPDATE heimdall_retention.event_holds SET purpose='retry' WHERE hold_id='hold-1'","UPDATE heimdall_retention.event_holds SET purpose='administrative' WHERE hold_id='retry-1'","DELETE FROM heimdall_retention.event_holds WHERE hold_id='retry-1'"]:
   reject_hold(statement)
  retry_release=release.replace('hold-1','retry-1')
  sql(retry_release,hold_db)
  retry_snapshot=sql("SELECT jsonb_agg(to_jsonb(h) ORDER BY hold_id) FROM heimdall_retention.event_holds h",hold_db)
  sql(retry_release,hold_db)
  assert sql("SELECT jsonb_agg(to_jsonb(h) ORDER BY hold_id) FROM heimdall_retention.event_holds h",hold_db)==retry_snapshot
  assert sql("SELECT count(*) FROM heimdall_retention.event_holds WHERE purpose='retry' AND released_at IS NULL",hold_db)=='1'
  reject_hold("UPDATE heimdall_retention.event_holds SET released_at=NULL,release_event_id=NULL WHERE hold_id='retry-1'")
  quota_fixture=json.loads((ROOT/'contracts/fixtures/audit-retention-capacity.json').read_text())
  quota_sql=(ROOT/'contracts/persistence'/quota_fixture['path']).read_text()
  assert hashlib.sha256(quota_sql.encode()).hexdigest()==quota_fixture['checksum']
  quota_db='retention_capacity'
  sql('CREATE DATABASE '+quota_db+' TEMPLATE retention_holds')
  sql('BEGIN;'+quota_sql+'ROLLBACK;',quota_db)
  assert sql("SELECT to_regclass('heimdall_retention.capacity') IS NULL",quota_db)=='t'
  sql(quota_sql,quota_db)
  def reject_quota(statement):
   r=subprocess.run(['docker','exec','-i',name,'psql','-U','postgres','-d',quota_db,'-At','-v','ON_ERROR_STOP=1','-v','VERBOSITY=verbose'],input='BEGIN;'+statement+';ROLLBACK;',capture_output=True,text=True)
   assert r.returncode!=0 and '23514' in r.stderr,r.stderr
  reject_quota(place.replace('hold-1','uninitialized'))
  reject_quota("INSERT INTO heimdall_retention.capacity(row_limit,byte_limit) VALUES(1,8192)")
  sql("INSERT INTO heimdall_retention.capacity(row_limit,byte_limit,used_rows,reserved_rows) VALUES(1000,8192000,0,0)",quota_db)
  assert sql("SELECT used_rows=(SELECT count(*) FROM heimdall_retention.event_identities)+(SELECT count(*) FROM heimdall_retention.decision_identities)+(SELECT count(*) FROM heimdall_retention.event_holds) AND reserved_rows=4 FROM heimdall_retention.capacity",quota_db)=='t'
  sql("UPDATE heimdall_retention.capacity SET row_limit=used_rows+reserved_rows,byte_limit=(used_rows+reserved_rows)*8192",quota_db)
  before=sql("SELECT used_rows||':'||reserved_rows FROM heimdall_retention.capacity",quota_db)
  reject_quota(place.replace('hold-1','full'))
  reject_quota(retry_place.replace('retry-1','retry-full'))
  assert sql("SELECT used_rows||':'||reserved_rows FROM heimdall_retention.capacity",quota_db)==before
  # Release consumes its original fixed slot; it must work at a full quota.
  sql(release.replace('hold-1','hold-2'),quota_db)
  sql(release.replace('hold-1','retry-2'),quota_db)
  assert sql("SELECT used_rows||':'||reserved_rows FROM heimdall_retention.capacity",quota_db)==before
  sql("UPDATE heimdall_retention.capacity SET row_limit=row_limit+1,byte_limit=byte_limit+8192",quota_db)
  def race_hold(hold):
   return subprocess.run(['docker','exec','-i',name,'psql','-U','postgres','-d',quota_db,'-At','-v','ON_ERROR_STOP=1'],input=place.replace('hold-1',hold),capture_output=True,text=True).returncode
  with concurrent.futures.ThreadPoolExecutor(max_workers=2) as workers:
   results=list(workers.map(race_hold,['quota-race-a','quota-race-b']))
  assert sorted(results)==[0,3],results
  assert sql("SELECT count(*) FROM heimdall_retention.event_holds WHERE hold_id IN ('quota-race-a','quota-race-b')",quota_db)=='1'
  # Recover source history, then consume reserved metadata slots at full quota.
  go('^TestRuntimeRecovery$',dict(env,HEIMDALL_TEST_RUNTIME_RECOVERY_URL=dsn(quota_db)))
  sql("UPDATE heimdall.schema_metadata SET state='ready'",quota_db)
  sql(identity_fixture['copyEvents']+' ON CONFLICT DO NOTHING;'+identity_fixture['copyDecisions']+' ON CONFLICT(product_id,environment,decision_id) DO UPDATE SET resolution=EXCLUDED.resolution,terminal_event_id=EXCLUDED.terminal_event_id,recovery_event_id=EXCLUDED.recovery_event_id,resolved_at=EXCLUDED.resolved_at',quota_db)
  assert sql("SELECT used_rows+reserved_rows=row_limit AND reserved_rows=2 FROM heimdall_retention.capacity",quota_db)=='t'
  final_quota=sql("SELECT used_rows||':'||reserved_rows FROM heimdall_retention.capacity",quota_db)
  sql(identity_fixture['copyEvents']+' ON CONFLICT DO NOTHING;'+identity_fixture['copyDecisions']+' ON CONFLICT(product_id,environment,decision_id) DO UPDATE SET resolution=EXCLUDED.resolution,terminal_event_id=EXCLUDED.terminal_event_id,recovery_event_id=EXCLUDED.recovery_event_id,resolved_at=EXCLUDED.resolved_at',quota_db)
  assert sql("SELECT used_rows||':'||reserved_rows FROM heimdall_retention.capacity",quota_db)==final_quota
  marker_fixture=json.loads((ROOT/'contracts/fixtures/audit-retirement-markers.json').read_text())
  marker_sql=(ROOT/'contracts/persistence'/marker_fixture['path']).read_text()
  assert hashlib.sha256(marker_sql.encode()).hexdigest()==marker_fixture['checksum']
  marker_db='retirement_markers'
  sql('CREATE DATABASE '+marker_db+' TEMPLATE retention_capacity')
  sql('BEGIN;'+marker_sql+'ROLLBACK;',marker_db)
  assert sql("SELECT to_regclass('heimdall_retention.retirements') IS NULL",marker_db)=='t'
  sql(marker_sql,marker_db)
  sql("UPDATE heimdall_retention.capacity SET row_limit=row_limit+10,byte_limit=byte_limit+81920",marker_db)
  marker="INSERT INTO heimdall_retention.retirements(product_id,environment,retirement_id,target_event_id,tenant_id,retired_at,retain_for_ms,administration_event_id,member_event_ids) VALUES('ledger','staging','retirement-1','ordinary','tenant-1',2001,1000,'normal-terminal','[\"ordinary\"]')"
  def reject_marker(statement):
   r=subprocess.run(['docker','exec','-i',name,'psql','-U','postgres','-d',marker_db,'-At','-v','ON_ERROR_STOP=1','-v','VERBOSITY=verbose'],input='BEGIN;'+statement+';COMMIT;',capture_output=True,text=True)
   assert r.returncode!=0 and '23514' in r.stderr,r.stderr
  # These references prove structure only, not an authorized retention event.
  reject_marker(marker)
  sql("UPDATE heimdall.audit_deliveries SET state='delivered',delivered_at=1001 WHERE product_id='ledger' AND environment='staging' AND event_id='ordinary'",marker_db)
  before_marker=sql("SELECT used_rows FROM heimdall_retention.capacity",marker_db)
  reject_marker(marker) # Deferred commit check refuses a marker without removal.
  assert sql("SELECT count(*) FROM heimdall_retention.retirements",marker_db)=='0'
  assert sql("SELECT used_rows FROM heimdall_retention.capacity",marker_db)==before_marker
  for statement in [marker.replace("'tenant-1'","'other'"),marker.replace('2001,1000','2001,1001'),marker.replace("'normal-terminal'","'missing'"),marker.replace("'[\"ordinary\"]'","'[\"z-request\"]'"),"DELETE FROM heimdall.audit_deliveries WHERE product_id='ledger' AND environment='staging' AND event_id='ordinary';"+marker]:reject_marker(statement)
  held="INSERT INTO heimdall_retention.event_holds(product_id,environment,hold_id,event_id,tenant_id,placed_at,placement_event_id) VALUES('ledger','staging','retired-hold','ordinary','tenant-1',2001,'normal-terminal')"
  reject_marker(held+';'+marker)
  retry_held=held.replace('placement_event_id)', 'placement_event_id,purpose)').replace("2001,'normal-terminal')","2001,'normal-terminal','retry')")
  reject_marker(retry_held+';'+marker)
  remove="DELETE FROM heimdall.audit_deliveries WHERE product_id='ledger' AND environment='staging' AND event_id='ordinary'; DELETE FROM heimdall_capacity.event_charges WHERE product_id='ledger' AND environment='staging' AND event_id='ordinary'; DELETE FROM heimdall.audit_events WHERE product_id='ledger' AND environment='staging' AND event_id='ordinary'"
  reject_marker(marker+';'+retry_held+';'+remove)
  reject_marker(marker+';'+held+';'+remove) # Also catch holds placed later in the same transaction.
  sql('BEGIN;'+marker+';'+remove+';COMMIT;',marker_db)
  assert sql("SELECT used_rows FROM heimdall_retention.capacity",marker_db)==str(int(before_marker)+1)
  assert sql("SELECT count(*) FROM heimdall_retention.event_identities WHERE product_id='ledger' AND environment='staging' AND event_id='ordinary'",marker_db)=='1'
  assert sql("SELECT count(*) FROM heimdall.audit_events WHERE product_id='ledger' AND environment='staging' AND event_id='ordinary'",marker_db)=='0'
  assert sql("SELECT count(*) FROM heimdall_retention.retirements",marker_db)=='1'
  reject_marker(held)
  reject_marker("DELETE FROM heimdall_retention.retirements")
  reject_marker("UPDATE heimdall_retention.retirements SET retired_at=retired_at+1")
  sql("UPDATE heimdall_retention.retirements SET retired_at=retired_at",marker_db)
  assert sql("SELECT used_rows FROM heimdall_retention.capacity",marker_db)==str(int(before_marker)+1)
  # Re-adoption must charge permanent markers too.
  sql("DELETE FROM heimdall_retention.capacity; INSERT INTO heimdall_retention.capacity(row_limit,byte_limit) VALUES(1000,8192000)",marker_db)
  assert sql("SELECT used_rows FROM heimdall_retention.capacity",marker_db)==str(int(before_marker)+1)
  def assert_retirement_race(database,owner_action,contender_action):
   command=['docker','exec','-i',name,'psql','-U','postgres','-d',database,'-qAt','-v','ON_ERROR_STOP=1','-v','VERBOSITY=verbose']
   owner=subprocess.Popen(command,stdin=subprocess.PIPE,stdout=subprocess.PIPE,stderr=subprocess.PIPE,text=True)
   try:
    owner.stdin.write("BEGIN; SET LOCAL statement_timeout='10s';"+owner_action+";SELECT 'owner-ready';\n")
    owner.stdin.flush()
    assert select.select([owner.stdout],[],[],10)[0],'owner did not acquire locks'
    ready=owner.stdout.readline().strip()
    assert ready=='owner-ready',(ready,owner.poll())
    def contender():
     return subprocess.run(command,input="SET application_name='retirement-contender'; BEGIN; SET LOCAL statement_timeout='10s';"+contender_action+';COMMIT;',capture_output=True,text=True,timeout=15)
    with concurrent.futures.ThreadPoolExecutor(max_workers=1) as workers:
     future=workers.submit(contender)
     deadline=time.monotonic()+5
     while sql("SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND application_name='retirement-contender' AND wait_event_type='Lock')",database)!='t':
      assert not future.done(),'contender did not wait for the identity lock'
      assert time.monotonic()<deadline,'contender lock wait not observed'
      time.sleep(.02)
     out,err=owner.communicate('COMMIT;\n',timeout=10)
     assert owner.returncode==0,(out,err)
     result=future.result(timeout=10)
     assert result.returncode!=0 and '23514' in result.stderr,result.stderr
   finally:
    if owner.poll() is None:
     owner.kill();owner.communicate(timeout=5)
  for winner in ['hold','retirement']:
   database='retirement_race_'+winner
   sql('CREATE DATABASE '+database+' TEMPLATE retention_capacity')
   sql(marker_sql,database)
   sql("UPDATE heimdall_retention.capacity SET row_limit=row_limit+10,byte_limit=byte_limit+81920; UPDATE heimdall.audit_deliveries SET state='delivered',delivered_at=1001 WHERE product_id='ledger' AND environment='staging' AND event_id='ordinary'",database)
   initial_used=int(sql('SELECT used_rows FROM heimdall_retention.capacity',database))
   owner_action=held if winner=='hold' else marker+';'+remove
   contender_action=marker+';'+remove if winner=='hold' else held
   assert_retirement_race(database,owner_action,contender_action)
   expected_hold='1' if winner=='hold' else '0'
   expected_retirement='0' if winner=='hold' else '1'
   assert sql("SELECT count(*) FROM heimdall_retention.event_holds WHERE hold_id='retired-hold'",database)==expected_hold
   assert sql("SELECT count(*) FROM heimdall.audit_events WHERE product_id='ledger' AND environment='staging' AND event_id='ordinary'",database)==expected_hold
   assert sql("SELECT count(*) FROM heimdall_retention.retirements",database)==expected_retirement
   assert int(sql('SELECT used_rows FROM heimdall_retention.capacity',database))==initial_used+1
  for language in ['go','ts']:
   database='retained_read_'+language
   sql('CREATE DATABASE '+database+' TEMPLATE retirement_markers')
   selected=dict(env,HEIMDALL_TEST_RETAINED_READ_URL=dsn(database))
   if language=='go':go('^TestRetainedInspection$',selected)
   else:run(['pnpm','--filter','@heimdall/sdk','exec','vitest','run','test/postgres-retained-read.test.ts'],env=selected)
  decision_retirement=json.loads((ROOT/'contracts/fixtures/retired-decision-inspection.json').read_text())
  decision_db='retired_decisions'
  sql('CREATE DATABASE '+decision_db+' TEMPLATE retention_port_go')
  sql(hold_sql+retry_sql+quota_sql+"INSERT INTO heimdall_retention.capacity(row_limit,byte_limit) VALUES(1000,8192000);"+marker_sql,decision_db)
  sql("UPDATE heimdall_capacity.policy SET event_limit=event_limit+10; UPDATE heimdall_capacity.upgrade SET event_limit=(SELECT event_limit FROM heimdall_capacity.policy)",decision_db)
  go('^TestRetirementReferences$',dict(env,HEIMDALL_TEST_RETIREMENT_REFERENCE_URL=dsn(decision_db)))
  for language in ['go','ts']:
   database='retained_recovery_'+language
   sql('CREATE DATABASE '+database+' TEMPLATE '+decision_db)
   selected=dict(env,HEIMDALL_TEST_RETAINED_RECOVERY_URL=dsn(database))
   if language=='go':go('^TestRetainedRecovery$',selected)
   else:run(['pnpm','--filter','@heimdall/sdk','exec','vitest','run','test/postgres-retained-recovery.test.ts'],env=selected)
  for first,second in [('go','ts'),('ts','go')]:
   database='retained_recovery_loss_'+first
   sql('CREATE DATABASE '+database+' TEMPLATE '+decision_db)
   def retained_recovery_attempt(language,selected):
    if language=='go':go('^TestRetainedRecoveryLoss$',selected)
    else:run(['pnpm','--filter','@heimdall/sdk','exec','vitest','run','test/postgres-retained-recovery.test.ts'],env=selected)
   selected=dict(env,HEIMDALL_TEST_RETAINED_RECOVERY_LOSS_URL=dsn(database))
   with CommitLossProxy(int(port)) as proxy:
    retained_recovery_attempt(first,dict(selected,HEIMDALL_TEST_RETAINED_RECOVERY_FAULT_URL=dsn(database,proxy.port)))
    assert proxy.dropped.is_set() and not proxy.errors,proxy.errors
   snapshot=json.loads((ROOT/'contracts/fixtures/retained-recovery-writes.json').read_text())['snapshot']
   before=sql(snapshot,database)
   retained_recovery_attempt(second,selected)
   assert sql(snapshot,database)==before
  append_loss=json.loads((ROOT/'contracts/fixtures/retained-write-loss.json').read_text())
  append_snapshot=json.loads((ROOT/'contracts/fixtures/retained-writes.json').read_text())['snapshot']
  append_counts="SELECT jsonb_build_array(p.used_events,p.reserved_events,c.used_rows,c.reserved_rows) FROM heimdall_capacity.policy p CROSS JOIN heimdall_retention.capacity c"
  def retained_write_attempt(language,selected):
   if language=='go':go('^TestRetainedWriteLoss$',selected)
   else:run(['pnpm','--filter','@heimdall/sdk','exec','vitest','run','test/postgres-retained-write-loss.test.ts'],env=selected)
  for case in append_loss:
   for first,second in [('go','ts'),('ts','go')]:
    database='retained_write_loss_'+case['name']+'_'+first
    sql('CREATE DATABASE '+database+' TEMPLATE '+decision_db)
    counts_before=json.loads(sql(append_counts,database))
    selected=dict(env,HEIMDALL_TEST_RETAINED_WRITE_LOSS_URL=dsn(database),HEIMDALL_TEST_RETAINED_WRITE_CASE=case['name'])
    with CommitLossProxy(int(port)) as proxy:
     retained_write_attempt(first,dict(selected,HEIMDALL_TEST_RETAINED_WRITE_FAULT_URL=dsn(database,proxy.port)))
     assert proxy.dropped.is_set() and not proxy.errors,proxy.errors
    counts_after=json.loads(sql(append_counts,database))
    assert [after-before for before,after in zip(counts_before,counts_after)]==case['delta'],(case['name'],counts_before,counts_after)
    before=sql(append_snapshot,database)
    retained_write_attempt(second,selected)
    assert sql(append_snapshot,database)==before
    print(f"Retained append COMMIT loss passed: {case['name']} {first} to {second}",flush=True)
  for language in ['go','ts']:
   database='retained_write_'+language
   sql('CREATE DATABASE '+database+' TEMPLATE '+decision_db)
   selected=dict(env,HEIMDALL_TEST_RETAINED_WRITE_URL=dsn(database))
   if language=='go':go('^TestRetainedWrites$',selected)
   else:run(['pnpm','--filter','@heimdall/sdk','exec','vitest','run','test/postgres-retained-write.test.ts'],env=selected)
  for language in ['go','ts']:
   database='retention_eligibility_'+language
   sql('CREATE DATABASE '+database+' TEMPLATE '+decision_db)
   selected=dict(env,HEIMDALL_TEST_RETENTION_ELIGIBILITY_URL=dsn(database))
   if language=='go':go('^TestRetentionEligibility$',selected)
   else:run(['pnpm','--filter','@heimdall/sdk','exec','vitest','run','test/postgres-retention-eligibility.test.ts'],env=selected)
  compact_before=sql("SELECT jsonb_build_object('events',(SELECT jsonb_agg(to_jsonb(i) ORDER BY product_id,environment,event_id) FROM heimdall_retention.event_identities i),'decisions',(SELECT jsonb_agg(to_jsonb(d) ORDER BY product_id,environment,decision_id) FROM heimdall_retention.decision_identities d))",decision_db)
  initial_used=int(sql('SELECT used_rows FROM heimdall_retention.capacity',decision_db))
  for scope in decision_retirement['scopes']:
   product,environment=scope['productId'],scope['environment']
   where=f"product_id='{product}' AND environment='{environment}'"
   ids=json.loads(sql("SELECT to_jsonb(array_remove(ARRAY[request_event_id,terminal_event_id,recovery_event_id],NULL)) FROM heimdall_retention.decision_identities WHERE "+where,decision_db))
   assert len(ids)==scope['members']
   member_list=json.dumps(sorted(ids))
   array="ARRAY["+','.join("'"+id+"'" for id in ids)+"]"
   members=where+' AND event_id=ANY('+array+')'
   sql("UPDATE heimdall.audit_deliveries SET state='delivered',delivered_at=2001 WHERE "+members,decision_db)
   retirement=f"INSERT INTO heimdall_retention.retirements(product_id,environment,retirement_id,target_event_id,tenant_id,retired_at,retain_for_ms,administration_event_id,member_event_ids) VALUES('{product}','{environment}','decision-retirement','z-request','tenant-1',3001,1000,'retirement-reference','{member_list}')"
   detach="DELETE FROM heimdall_capacity.reservations WHERE "+where+"; DELETE FROM heimdall.audit_decisions WHERE "+where+';'
   def remove_members(predicate):
    return "DELETE FROM heimdall.audit_deliveries WHERE "+predicate+"; DELETE FROM heimdall_capacity.event_charges WHERE "+predicate+"; DELETE FROM heimdall.audit_events WHERE "+predicate
   # Each member protects the whole group, including recovery administration.
   for member_index,event_id in enumerate(ids):
    for purpose in ['administrative','retry']:
     for winner in ['hold','retirement']:
      database=f'decision_race_{product}_{member_index}_{winner}_{purpose}'
      sql('CREATE DATABASE '+database+' TEMPLATE '+decision_db)
      hold=f"INSERT INTO heimdall_retention.event_holds(product_id,environment,hold_id,event_id,tenant_id,placed_at,placement_event_id,purpose) VALUES('{product}','{environment}','group-hold','{event_id}','tenant-1',3001,'retirement-reference','{purpose}')"
      retire=retirement+';'+detach+remove_members(members)
      before_used=int(sql('SELECT used_rows FROM heimdall_retention.capacity',database))
      compact_snapshot="SELECT jsonb_build_object('events',(SELECT jsonb_agg(to_jsonb(i) ORDER BY product_id,environment,event_id) FROM heimdall_retention.event_identities i),'decisions',(SELECT jsonb_agg(to_jsonb(d) ORDER BY product_id,environment,decision_id) FROM heimdall_retention.decision_identities d))"
      before_identities=sql(compact_snapshot,database)
      before_deliveries=sql('SELECT count(*) FROM heimdall.audit_deliveries WHERE '+members,database)
      assert_retirement_race(database,hold if winner=='hold' else retire,retire if winner=='hold' else hold)
      held_count='1' if winner=='hold' else '0'
      retained_count=str(scope['members']) if winner=='hold' else '0'
      assert sql("SELECT count(*) FROM heimdall_retention.event_holds WHERE hold_id='group-hold'",database)==held_count
      assert sql('SELECT count(*) FROM heimdall.audit_events WHERE '+members,database)==retained_count
      assert sql('SELECT count(*) FROM heimdall_capacity.event_charges WHERE '+members,database)==retained_count
      assert sql('SELECT count(*) FROM heimdall.audit_decisions WHERE '+where,database)==held_count
      assert sql('SELECT count(*) FROM heimdall_capacity.reservations WHERE '+where,database)==held_count
      assert sql('SELECT count(*) FROM heimdall_retention.retirements WHERE '+where,database)==('0' if winner=='hold' else '1')
      assert int(sql('SELECT used_rows FROM heimdall_retention.capacity',database))==before_used+1
      assert sql(compact_snapshot,database)==before_identities
      assert sql('SELECT count(*) FROM heimdall.audit_deliveries WHERE '+members,database)==(before_deliveries if winner=='hold' else '0')
      print(f'Whole-decision lock race passed: {product}/{environment} member={event_id} purpose={purpose} winner={winner}',flush=True)
   partial=detach+remove_members(where+" AND event_id='z-request'")
   failed=subprocess.run(['docker','exec','-i',name,'psql','-U','postgres','-d',decision_db,'-At','-v','ON_ERROR_STOP=1','-v','VERBOSITY=verbose'],input='BEGIN;'+retirement+';'+partial+';COMMIT;',capture_output=True,text=True)
   assert failed.returncode!=0 and '23514' in failed.stderr,failed.stderr
   assert sql('SELECT count(*) FROM heimdall.audit_events WHERE '+members,decision_db)==str(scope['members'])
   sql('BEGIN;'+retirement+';'+detach+remove_members(members)+';COMMIT;',decision_db)
   assert sql('SELECT count(*) FROM heimdall.audit_events WHERE '+members,decision_db)=='0'
  assert sql("SELECT jsonb_build_object('events',(SELECT jsonb_agg(to_jsonb(i) ORDER BY product_id,environment,event_id) FROM heimdall_retention.event_identities i),'decisions',(SELECT jsonb_agg(to_jsonb(d) ORDER BY product_id,environment,decision_id) FROM heimdall_retention.decision_identities d))",decision_db)==compact_before
  assert int(sql('SELECT used_rows FROM heimdall_retention.capacity',decision_db))==initial_used+2
  selected=dict(env,HEIMDALL_TEST_RETIRED_DECISION_URL=dsn(decision_db))
  go('^TestRetiredDecisionInspection$',selected)
  run(['pnpm','--filter','@heimdall/sdk','exec','vitest','run','test/postgres-retired-decision.test.ts'],env=selected)
  # Exercise retired resolution on an actually activated v4 database as well.
  sql('CREATE DATABASE retired_resolution_v4 TEMPLATE retention_activation')
  sql("UPDATE heimdall_capacity.policy SET event_limit=event_limit+10; UPDATE heimdall_capacity.upgrade SET event_limit=(SELECT event_limit FROM heimdall_capacity.policy)",'retired_resolution_v4')
  go('^TestRetirementReferences$',dict(env,HEIMDALL_TEST_RETIREMENT_REFERENCE_URL=dsn('retired_resolution_v4'),HEIMDALL_TEST_RETIREMENT_REFERENCE_V4='1'))
  for scope in decision_retirement['scopes']:
   product,environment=scope['productId'],scope['environment']
   where=f"product_id='{product}' AND environment='{environment}'"
   ids=json.loads(sql("SELECT to_jsonb(array_remove(ARRAY[request_event_id,terminal_event_id,recovery_event_id],NULL)) FROM heimdall_retention.decision_identities WHERE "+where,'retired_resolution_v4'))
   member_list=json.dumps(sorted(ids));array="ARRAY["+','.join("'"+id+"'" for id in ids)+"]";members=where+' AND event_id=ANY('+array+')'
   sql("UPDATE heimdall.audit_deliveries SET state='delivered',delivered_at=2001 WHERE "+members,'retired_resolution_v4')
   retirement=f"INSERT INTO heimdall_retention.retirements(product_id,environment,retirement_id,target_event_id,tenant_id,retired_at,retain_for_ms,administration_event_id,member_event_ids) VALUES('{product}','{environment}','decision-retirement','z-request','tenant-1',3001,1000,'retirement-reference','{member_list}')"
   detach="DELETE FROM heimdall_capacity.reservations WHERE "+where+"; DELETE FROM heimdall.audit_decisions WHERE "+where+';'
   sql('BEGIN;'+retirement+';'+detach+remove_members(members)+';COMMIT;','retired_resolution_v4')
  resolution_v4=dict(env,HEIMDALL_TEST_RETAINED_RESOLUTION_URL=dsn('retired_resolution_v4'),HEIMDALL_TEST_RETAINED_RESOLUTION_RETIRED='1',HEIMDALL_TEST_RETAINED_RESOLUTION_V4='1')
  go('^TestRetainedResolution$',resolution_v4)
  run(['pnpm','--filter','@heimdall/sdk','exec','vitest','run','test/postgres-retained-resolution.test.ts'],env=resolution_v4)
  resolution_env=dict(env,HEIMDALL_TEST_RETAINED_RESOLUTION_URL=dsn(decision_db),HEIMDALL_TEST_RETAINED_RESOLUTION_RETIRED='1')
  go('^TestRetainedResolution$',resolution_env)
  run(['pnpm','--filter','@heimdall/sdk','exec','vitest','run','test/postgres-retained-resolution.test.ts'],env=resolution_env)
  retry_snapshot="SELECT jsonb_build_object('events',(SELECT jsonb_agg(to_jsonb(e) ORDER BY product_id,environment,event_id) FROM heimdall.audit_events e),'deliveries',(SELECT jsonb_agg(to_jsonb(d) ORDER BY product_id,environment,event_id,sink_id) FROM heimdall.audit_deliveries d),'metadata',(SELECT to_jsonb(c) FROM heimdall_retention.capacity c))"
  before_retry=sql(retry_snapshot,decision_db)
  selected=dict(env,HEIMDALL_TEST_RETAINED_RETRY_URL=dsn(decision_db))
  go('^TestRetainedRetry$',selected)
  run(['pnpm','--filter','@heimdall/sdk','exec','vitest','run','test/postgres-retained-retry.test.ts'],env=selected)
  assert sql(retry_snapshot,decision_db)==before_retry
  identity_db='retention_identities'
  sql('CREATE DATABASE '+identity_db+' TEMPLATE cli_activation')
  sql('BEGIN;'+identity_sql+'ROLLBACK;',identity_db)
  assert sql("SELECT to_regnamespace('heimdall_retention') IS NULL",identity_db)=='t'
  sql(identity_sql,identity_db)
  def reject_identity(statement,code):
   r=subprocess.run(['docker','exec','-i',name,'psql','-U','postgres','-d',identity_db,'-At','-v','ON_ERROR_STOP=1','-v','VERBOSITY=verbose'],input='BEGIN;'+statement+';ROLLBACK;',capture_output=True,text=True)
   assert r.returncode!=0 and code in r.stderr,(code,r.stderr)
  for case in identity_fixture['negative']:reject_identity(case['sql'],case['code'])
  sql(identity_fixture['copyEvents']+';'+identity_fixture['copyDecisions'],identity_db)
  assert sql("SELECT (SELECT count(*) FROM heimdall_retention.event_identities)=(SELECT count(*) FROM heimdall.audit_events)",identity_db)=='t'
  assert sql("SELECT (SELECT count(*) FROM heimdall_retention.decision_identities)=(SELECT count(*) FROM heimdall.audit_decisions)",identity_db)=='t'
  for statement in identity_fixture['mutations']:reject_identity(statement,'23514')
  # Mirror a real SDK finalization into the prepared retained decision record.
  go('^TestRuntimeRecovery$',dict(env,HEIMDALL_TEST_RUNTIME_RECOVERY_URL=dsn(identity_db)))
  sql("UPDATE heimdall.schema_metadata SET state='ready'",identity_db)
  sql(identity_fixture['copyEvents']+' ON CONFLICT DO NOTHING;'+identity_fixture['copyDecisions']+' ON CONFLICT(product_id,environment,decision_id) DO UPDATE SET resolution=EXCLUDED.resolution,terminal_event_id=EXCLUDED.terminal_event_id,recovery_event_id=EXCLUDED.recovery_event_id,resolved_at=EXCLUDED.resolved_at',identity_db)
  assert sql("SELECT resolution FROM heimdall_retention.decision_identities WHERE product_id='ledger' AND environment='production'",identity_db)=='recovered'

  # Source validation and schema constraints only: this privileged test deletion
  # is not an authorized SDK retention transaction and installs no purge API.
  identity_snapshot=sql("SELECT jsonb_agg(to_jsonb(i) ORDER BY product_id,environment,event_id) FROM heimdall_retention.event_identities i",identity_db)
  decision_snapshot=sql("SELECT jsonb_agg(to_jsonb(d) ORDER BY product_id,environment,decision_id) FROM heimdall_retention.decision_identities d",identity_db)
  sql("BEGIN; DELETE FROM heimdall.audit_deliveries WHERE product_id='ledger' AND environment='production' AND event_id='ordinary'; DELETE FROM heimdall_capacity.event_charges WHERE product_id='ledger' AND environment='production' AND event_id='ordinary'; DELETE FROM heimdall.audit_events WHERE product_id='ledger' AND environment='production' AND event_id='ordinary'; DELETE FROM heimdall_capacity.reservations WHERE product_id='ledger' AND environment='staging'; DELETE FROM heimdall.audit_decisions WHERE product_id='ledger' AND environment='staging'; DELETE FROM heimdall.audit_deliveries WHERE product_id='ledger' AND environment='staging'; DELETE FROM heimdall_capacity.event_charges WHERE product_id='ledger' AND environment='staging'; DELETE FROM heimdall.audit_events WHERE product_id='ledger' AND environment='staging'; COMMIT",identity_db)
  assert sql("SELECT jsonb_agg(to_jsonb(i) ORDER BY product_id,environment,event_id) FROM heimdall_retention.event_identities i",identity_db)==identity_snapshot
  assert sql("SELECT jsonb_agg(to_jsonb(d) ORDER BY product_id,environment,decision_id) FROM heimdall_retention.decision_identities d",identity_db)==decision_snapshot
  sql("UPDATE heimdall_retention.event_identities SET event_id=event_id; UPDATE heimdall_retention.decision_identities SET decision_id=decision_id",identity_db)
  assert sql("SELECT database_version||':'||state FROM heimdall.schema_metadata",identity_db)=='3:ready'
  assert sql("SELECT has_schema_privilege('public_capacity_runtime','heimdall_retention','USAGE')",identity_db)=='f'
  # Isolated port test only: no public compatibility activation.
  sql("UPDATE heimdall_capacity.policy SET state='ready'",'normal')
  go('^TestCapacityBackfillPendingRecovery$',dict(env,HEIMDALL_TEST_CAPACITY_RECOVER_HISTORY_URL=dsn('normal')))
  ts(dict(env,HEIMDALL_TEST_CAPACITY_RECOVER_HISTORY_URL=dsn('normal')))
 print(json.dumps({'run':name,'result':'passed','checks':['independent read-only retention activation with atomic v4 publication semantic corruption refusal TS/Go readiness and lost COMMIT retry','bounded retention group backfill with byte budgets atomic checkpoints concurrent and lost-COMMIT resume corruption and skipped cursor refusal','atomic retention preparation with quota preflight catalog refusal rollback concurrent retry and lost COMMIT proof','mixed TS and Go history across two products and two environments','bounded count and encoded-envelope page budgets','full digest and decision/recovery binding validation','pending recovery reservations only','matching imported charges and reservations do not double-count','eight concurrent deployment processes serialize checkpoints','lost event and decision page COMMIT resumes durable progress','invalid history and skipped cursor rollback current page','insufficient capacity refuses without partial reservations','late legacy history blocks completed coverage','completion remains migrating and preparing','atomic internal activation with stale history and schema refusal','activation COMMIT loss and retry after new writes','publication permission failure rolls back ledger and policy','activation CLI argument and protocol checks','TS and Go shared standalone and managed v3 readiness matrix','accounted requests recover in both cross-language directions','held deployment lock deadline and reuse','both v3 maintenance recovery ports use reserved capacity at a full ledger','reciprocal recovery COMMIT loss and protocol-gated exact retries','both maintenance batches observe COMMIT-loss winners over separate read connections and restart without duplicate work','SIGKILL after the first committed candidate resumes in the other language with original initiators and exact event outbox and capacity counts','reciprocal retention COMMIT loss returns unknown and the other SDK retries without changing compact metadata','both SDKs resolve identical retired retries and reject changed payloads without recreating events or deliveries','ordinary request and completion retained writes survive reciprocal COMMIT loss with exact accounting and unchanged routing','retained recovery consumes full-capacity reservations atomically and reciprocal lost COMMIT retries preserve all payload and metadata state','both SDKs atomically append retained ordinary request and normal completion records with full-capacity admission and rollback proof','both SDKs classify complete primary snapshots with durable holds and retry obligations and no storage mutations','explicit retry holds preserve immutable purpose and independent audited release without expiry or quota refunds','holds on every completed and recovered decision member exclude whole-group retirement in both observed lock orders','whole completed and recovered decisions retire atomically with preserved compact links and reciprocal SDK inspection','both SDKs distinguish live retired absent and corrupt missing history with primary readiness and cancellation','observed PostgreSQL lock waits serialize hold-first and retirement-first transactions without losing holds or creating late holds','retirement markers require atomic removal and reject held undelivered young or incomplete history','retained metadata quota blocks overflow and reserves recovery slots while independent hold release remains possible','prepared hold ledger preserves independent holds and immutable releases with scoped reference checks','prepared immutable identities bind source routing and decision members and survive privileged test payload removal']}))
finally:
 if created:run(['docker','rm','--force','--volumes',name],stdout=subprocess.DEVNULL)
 if bind_data:bind_data.cleanup()

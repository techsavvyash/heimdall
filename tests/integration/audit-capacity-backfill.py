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
if not __debug__:raise RuntimeError('assertions must be enabled')
def run(args,**kwargs):return subprocess.run(args,cwd=ROOT,check=True,**kwargs)
def sql(statement,database='postgres'):
 return run(['docker','exec','-i',name,'psql','-U','postgres','-d',database,'-At','-v','ON_ERROR_STOP=1'],input=statement,capture_output=True,text=True).stdout.strip()
def go(pattern,selected):subprocess.run(['go','test','-race','-count=1','./internal/postgres','-run',pattern],cwd=ROOT/'sdk/go',env=selected,check=True)
def ts(selected):run(['pnpm','--filter','@heimdall/sdk','exec','vitest','run','test/postgres-capacity-history.test.ts'],env=selected)
try:
 run(['docker','run','--detach','--name',name,'-e','POSTGRES_PASSWORD','-p','127.0.0.1::5432',IMAGE],env=env,stdout=subprocess.DEVNULL);created=True
 for _ in range(60):
  if subprocess.run(['docker','exec',name,'pg_isready','-U','postgres'],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL).returncode==0:break
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
  run(['pnpm','--filter','@heimdall/sdk','exec','vitest','run','test/postgres-public-capacity.test.ts'],env=ready_env)
  subprocess.run(['go','test','-race','-count=1','.', '-run','^TestPublicCapacity$'],cwd=ROOT/'sdk/go',env=ready_env,check=True)
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
  assert sql("SELECT used_rows||':'||reserved_rows FROM heimdall_retention.capacity",quota_db)==before
  # Release consumes its original fixed slot; it must work at a full quota.
  sql(release.replace('hold-1','hold-2'),quota_db)
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
  remove="DELETE FROM heimdall.audit_deliveries WHERE product_id='ledger' AND environment='staging' AND event_id='ordinary'; DELETE FROM heimdall_capacity.event_charges WHERE product_id='ledger' AND environment='staging' AND event_id='ordinary'; DELETE FROM heimdall.audit_events WHERE product_id='ledger' AND environment='staging' AND event_id='ordinary'"
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
  for winner in ['hold','retirement']:
   database='retirement_race_'+winner
   sql('CREATE DATABASE '+database+' TEMPLATE retention_capacity')
   sql(marker_sql,database)
   sql("UPDATE heimdall_retention.capacity SET row_limit=row_limit+10,byte_limit=byte_limit+81920; UPDATE heimdall.audit_deliveries SET state='delivered',delivered_at=1001 WHERE product_id='ledger' AND environment='staging' AND event_id='ordinary'",database)
   initial_used=int(sql('SELECT used_rows FROM heimdall_retention.capacity',database))
   owner_action=held if winner=='hold' else marker+';'+remove
   contender_action=marker+';'+remove if winner=='hold' else held
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
 print(json.dumps({'run':name,'result':'passed','checks':['mixed TS and Go history across two products and two environments','bounded count and encoded-envelope page budgets','full digest and decision/recovery binding validation','pending recovery reservations only','matching imported charges and reservations do not double-count','eight concurrent deployment processes serialize checkpoints','lost event and decision page COMMIT resumes durable progress','invalid history and skipped cursor rollback current page','insufficient capacity refuses without partial reservations','late legacy history blocks completed coverage','completion remains migrating and preparing','atomic internal activation with stale history and schema refusal','activation COMMIT loss and retry after new writes','publication permission failure rolls back ledger and policy','activation CLI argument and protocol checks','TS and Go shared standalone and managed v3 readiness matrix','accounted requests recover in both cross-language directions','held deployment lock deadline and reuse','both v3 maintenance recovery ports use reserved capacity at a full ledger','reciprocal recovery COMMIT loss and protocol-gated exact retries','both maintenance batches observe COMMIT-loss winners over separate read connections and restart without duplicate work','SIGKILL after the first committed candidate resumes in the other language with original initiators and exact event outbox and capacity counts','reciprocal retention COMMIT loss returns unknown and the other SDK retries without changing compact metadata','both SDKs distinguish live retired absent and corrupt missing history with primary readiness and cancellation','observed PostgreSQL lock waits serialize hold-first and retirement-first transactions without losing holds or creating late holds','retirement markers require atomic removal and reject held undelivered young or incomplete history','retained metadata quota blocks overflow and reserves recovery slots while independent hold release remains possible','prepared hold ledger preserves independent holds and immutable releases with scoped reference checks','prepared immutable identities bind source routing and decision members and survive privileged test payload removal']}))
finally:
 if created:run(['docker','rm','--force',name],stdout=subprocess.DEVNULL)

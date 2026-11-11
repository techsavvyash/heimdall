"""Bounded offline capacity accounting over real TS/Go history on pinned PostgreSQL."""
import concurrent.futures
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
  # Isolated port test only: no public compatibility activation.
  sql("UPDATE heimdall_capacity.policy SET state='ready'",'normal')
  go('^TestCapacityBackfillPendingRecovery$',dict(env,HEIMDALL_TEST_CAPACITY_RECOVER_HISTORY_URL=dsn('normal')))
  ts(dict(env,HEIMDALL_TEST_CAPACITY_RECOVER_HISTORY_URL=dsn('normal')))
 print(json.dumps({'run':name,'result':'passed','checks':['mixed TS and Go history across two products and two environments','bounded count and encoded-envelope page budgets','full digest and decision/recovery binding validation','pending recovery reservations only','matching imported charges and reservations do not double-count','eight concurrent deployment processes serialize checkpoints','lost event and decision page COMMIT resumes durable progress','invalid history and skipped cursor rollback current page','insufficient capacity refuses without partial reservations','late legacy history blocks completed coverage','completion remains migrating and preparing','accounted requests recover in both cross-language directions','held deployment lock deadline and reuse']}))
finally:
 if created:run(['docker','rm','--force',name],stdout=subprocess.DEVNULL)

"""Prepared physical capacity ledger; not public SDK admission or migration proof."""
import concurrent.futures
import hashlib
import json
import os
from pathlib import Path
import secrets
import subprocess
import time
import uuid
ROOT=Path(__file__).resolve().parents[2]
IMAGE="postgres:17.11-alpine@sha256:3540c0362c809df084ffac0ae69fe794515f9de92692f02178bcdce0ef0302ac"
f=json.loads((ROOT/'contracts/fixtures/audit-capacity-schema.json').read_text())
migration=(ROOT/'contracts/persistence'/f['path']).read_text()
assert hashlib.sha256(migration.encode()).hexdigest()==f['checksum']
name='heimdall-capacity-'+uuid.uuid4().hex
env=dict(os.environ,POSTGRES_PASSWORD=secrets.token_hex(24))
created=False
if not __debug__:raise RuntimeError('assertions must be enabled')
def sql(statement,database='postgres',fail=False):
 r=subprocess.run(['docker','exec','-i',name,'psql','-U','postgres','-d',database,'-At','-v','ON_ERROR_STOP=1','-v','VERBOSITY=verbose'],input=statement,capture_output=True,text=True)
 if not fail and r.returncode:raise RuntimeError(r.stderr)
 return r if fail else r.stdout.strip()
def event(identifier,large=False,scope='ledger'):
 # Routing-only SQL fixture: does not claim a fully validated SDK security event.
 base=f"jsonb_build_object('eventId','{identifier}','productId','{scope}','environment','test','recordedAt',10,'padding','')"
 value=base if not large else f"({base}||jsonb_build_object('padding',repeat('x',1500000-octet_length(({base})::text))))"
 return f"INSERT INTO heimdall.audit_events VALUES('{scope}','test','{identifier}',decode(repeat('a',64),'hex'),10,{value});"
def delivery(identifier,count=1,scope='ledger'):return f"INSERT INTO heimdall.audit_deliveries(product_id,environment,event_id,sink_id) SELECT '{scope}','test','{identifier}','sink-'||n FROM generate_series(1,{count}) n;"
def charge(identifier,scope='ledger'):return f"INSERT INTO heimdall_capacity.event_charges(product_id,environment,event_id) VALUES('{scope}','test','{identifier}') ON CONFLICT DO NOTHING;"
def request(identifier='request',decision='decision',scope='ledger'):
 return event(identifier,scope=scope)+delivery(identifier,scope=scope)+f"INSERT INTO heimdall.audit_decisions(product_id,environment,decision_id,request_event_id,requested_at) VALUES('{scope}','test','{decision}','{identifier}',10);"+charge(identifier,scope=scope)+f"INSERT INTO heimdall_capacity.reservations(product_id,environment,decision_id,request_event_id) VALUES('{scope}','test','{decision}','{identifier}');"
def counters(database):return sql("SELECT used_events||':'||reserved_events||':'||reserved_bytes||':'||reserved_deliveries FROM heimdall_capacity.policy",database)
def setup(database):
 sql('CREATE DATABASE '+database)
 sql((ROOT/'contracts/persistence/session/0001.sql').read_text()+(ROOT/'contracts/persistence/session/0002.sql').read_text(),database)
 sql('BEGIN;'+migration+'ROLLBACK;',database)
 assert sql("SELECT to_regnamespace('heimdall_capacity') IS NULL",database)=='t'
 sql(migration,database)
 sql("INSERT INTO heimdall_capacity.policy(event_limit,byte_limit,delivery_limit) VALUES(3,4500000,96)",database)
 sql("GRANT USAGE ON SCHEMA heimdall,heimdall_capacity TO capacity_runtime; GRANT SELECT,INSERT ON heimdall.audit_events,heimdall.audit_deliveries TO capacity_runtime; GRANT SELECT,INSERT,UPDATE ON heimdall.audit_decisions,heimdall_capacity.reservations TO capacity_runtime; GRANT SELECT,INSERT ON heimdall_capacity.event_charges TO capacity_runtime; GRANT SELECT ON heimdall_capacity.policy TO capacity_runtime; GRANT UPDATE(used_events,used_bytes,used_deliveries,reserved_events,reserved_bytes,reserved_deliveries) ON heimdall_capacity.policy TO capacity_runtime;",database)
try:
 subprocess.run(['docker','run','--detach','--name',name,'-e','POSTGRES_PASSWORD',IMAGE],env=env,check=True,stdout=subprocess.DEVNULL);created=True
 for _ in range(60):
  if subprocess.run(['docker','exec',name,'pg_isready','-U','postgres'],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL).returncode==0:break
  time.sleep(.5)
 else:raise RuntimeError('PostgreSQL readiness timeout')
 sql('CREATE ROLE capacity_runtime NOLOGIN')
 for database in ('normal','recovery','concurrent','resolve_race','delivery_race'):setup(database)
 for database in ('normal','recovery'):
  sql('BEGIN;SET ROLE capacity_runtime;'+request()+'COMMIT;',database)
  assert counters(database)=='1:2:3000000:64'
  before=sql('SELECT row_to_json(p) FROM heimdall_capacity.policy p',database)
  sql(charge('request'),database)
  assert sql('SELECT row_to_json(p) FROM heimdall_capacity.policy p',database)==before
  for case in f['cases']:
   result=sql(case['sql'],database,True);assert result.returncode and case['sqlState'] in result.stderr,case['name']
  for statement in ['DELETE FROM heimdall_capacity.reservations','DELETE FROM heimdall_capacity.event_charges','UPDATE heimdall_capacity.policy SET event_limit=100','TRUNCATE heimdall_capacity.event_charges']:
   assert sql('SET ROLE capacity_runtime;'+statement,database,True).returncode
  blocked=sql('BEGIN;SET ROLE capacity_runtime;'+event('blocked')+delivery('blocked')+charge('blocked')+'COMMIT;',database,True)
  assert blocked.returncode and '23514' in blocked.stderr
  assert sql("SELECT count(*) FROM heimdall.audit_events WHERE event_id='blocked'",database)=='0'
  terminal=event('terminal',database=='recovery')+delivery('terminal',32 if database=='recovery' else 1)
  resolution='recovered' if database=='recovery' else 'completed'
  update=f"UPDATE heimdall.audit_decisions SET resolution='{resolution}',terminal_event_id='terminal',resolved_at=20 WHERE decision_id='decision'; UPDATE heimdall_capacity.reservations SET resolution='{resolution}',terminal_event_id='terminal'"+(",recovery_event_id='administration'" if database=='recovery' else '')+" WHERE decision_id='decision';"
  prefix='BEGIN;SET ROLE capacity_runtime;SELECT singleton FROM heimdall_capacity.policy FOR UPDATE;'+terminal+update
  missing=sql(prefix+'COMMIT;',database,True)
  assert missing.returncode and '23503' in missing.stderr
  assert counters(database)=='1:2:3000000:64'
  if database=='recovery':
   missing_admin=sql(prefix+charge('terminal')+'COMMIT;',database,True)
   assert missing_admin.returncode and '23503' in missing_admin.stderr
   # Set the logical byte/delivery limits to the exact existing request plus maximum recovery pair.
   sql('UPDATE heimdall_capacity.policy SET byte_limit=used_bytes+reserved_bytes,delivery_limit=used_deliveries+reserved_deliveries',database)
  suffix=charge('terminal')
  if database=='recovery':suffix+=event('administration',True)+delivery('administration',32)+charge('administration')
  sql(prefix+suffix+'COMMIT;',database)
  assert counters(database)==('3:0:0:0' if database=='recovery' else '2:0:0:0')
  if database=='recovery':assert sql('SELECT used_bytes=byte_limit AND used_deliveries=delivery_limit FROM heimdall_capacity.policy',database)=='t'
  snapshot=sql('SELECT row_to_json(p) FROM heimdall_capacity.policy p',database)
  sql("UPDATE heimdall_capacity.reservations SET resolution=resolution;"+charge('terminal'),database)
  assert sql('SELECT row_to_json(p) FROM heimdall_capacity.policy p',database)==snapshot
  if database=='normal':sql('BEGIN;'+event('ordinary')+delivery('ordinary')+charge('ordinary')+'COMMIT;',database)
  assert counters(database)=='3:0:0:0'
 def admit(n):
  r=sql('BEGIN;SET ROLE capacity_runtime;SELECT singleton FROM heimdall_capacity.policy FOR UPDATE;'+request('r'+str(n),'d'+str(n),scope='ledger' if n%2 else 'support')+'COMMIT;','concurrent',True)
  if r.returncode:assert '23514' in r.stderr
  return r.returncode==0
 with concurrent.futures.ThreadPoolExecutor(max_workers=8) as pool:assert sum(pool.map(admit,range(8)))==1
 assert sql('SELECT count(*) FROM heimdall.audit_events','concurrent')=='1'
 assert sql('SELECT count(*) FROM heimdall.audit_decisions','concurrent')=='1'
 assert sql('SELECT count(*) FROM heimdall_capacity.event_charges','concurrent')=='1'
 assert counters('concurrent')=='1:2:3000000:64'
 sql('BEGIN;'+request()+'COMMIT;','resolve_race')
 def resolve(resolution):
  terminal='winner-'+resolution
  statement='BEGIN;SET ROLE capacity_runtime;SELECT singleton FROM heimdall_capacity.policy FOR UPDATE;'+event(terminal)+delivery(terminal)
  statement+="DO $body$ BEGIN UPDATE heimdall.audit_decisions SET resolution='"+resolution+"',terminal_event_id='"+terminal+"',resolved_at=20 WHERE decision_id='decision' AND resolution='pending'; IF NOT FOUND THEN RAISE EXCEPTION 'lost finalization' USING ERRCODE='40001'; END IF; END $body$;"
  statement+="UPDATE heimdall_capacity.reservations SET resolution='"+resolution+"',terminal_event_id='"+terminal+"'"+(",recovery_event_id='race-admin'" if resolution=='recovered' else '')+" WHERE decision_id='decision';"+charge(terminal)
  if resolution=='recovered':statement+=event('race-admin')+delivery('race-admin')+charge('race-admin')
  result=sql(statement+'COMMIT;','resolve_race',True)
  if result.returncode:assert '40001' in result.stderr
  return result.returncode==0
 with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:assert sorted(pool.map(resolve,['completed','recovered']))==[False,True]
 winner=sql("SELECT resolution FROM heimdall.audit_decisions",'resolve_race')
 expected='3' if winner=='recovered' else '2'
 assert sql('SELECT count(*) FROM heimdall.audit_events','resolve_race')==expected
 assert sql('SELECT count(*) FROM heimdall_capacity.event_charges','resolve_race')==expected
 assert counters('resolve_race')==expected+':0:0:0'
 # Observe a real lock holder after its mutation, then start the conflicting action.
 for order in ('charge_first','sink_first'):
  identifier=order.replace('_','-');sql(event(identifier),'delivery_race')
  first=charge(identifier) if order=='charge_first' else delivery(identifier)
  statement="SET application_name='capacity-lock-holder';BEGIN;"+first+"SELECT pg_sleep(2);COMMIT;"
  with concurrent.futures.ThreadPoolExecutor(max_workers=1) as pool:
   holder=pool.submit(sql,statement,'delivery_race')
   for _ in range(100):
    if sql("SELECT count(*) FROM pg_stat_activity WHERE application_name='capacity-lock-holder' AND wait_event='PgSleep'",'delivery_race')=='1':break
    time.sleep(.01)
   else:raise RuntimeError('lock holder never reached observed sleep')
   result=sql(delivery(identifier) if order=='charge_first' else charge(identifier),'delivery_race',True)
   holder.result()
  if order=='charge_first':assert result.returncode and '23514' in result.stderr
  else:assert result.returncode==0
 assert sql('SELECT used_deliveries FROM heimdall_capacity.policy','delivery_race')=='1'
 print(json.dumps({'run':name,'result':'passed','checks':['DDL rollback','server-derived bytes and sink charges','idempotent charge does not double-count','pending reservations bind scoped decisions','counter reset policy delete and late sink refusal','runtime privilege limits','full admission blocks ordinary append atomically','missing terminal or administration charge rolls back at commit','normal completion releases reserve','maximum-byte recovery fits reserved capacity','eight concurrent requests across two products admit exactly one','normal versus recovery one winner with no loser charges','late sink versus charge serialized in both orders']}))
finally:
 if created:subprocess.run(['docker','rm','--force',name],check=True,stdout=subprocess.DEVNULL)

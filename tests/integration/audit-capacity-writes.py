"""Prepared TS/Go capacity transactions on disposable PostgreSQL, not activation."""
import hashlib
import json
import os
from pathlib import Path
import secrets
import subprocess
import time
import uuid
from pg_commit_loss import CommitLossProxy
ROOT=Path(__file__).resolve().parents[2]
IMAGE='postgres:17.11-alpine@sha256:3540c0362c809df084ffac0ae69fe794515f9de92692f02178bcdce0ef0302ac'
name='heimdall-capacity-writes-'+uuid.uuid4().hex
env=dict(os.environ,POSTGRES_PASSWORD=secrets.token_hex(24))
created=False
if not __debug__:raise RuntimeError('assertions must be enabled')
def run(args,**kwargs):return subprocess.run(args,cwd=ROOT,check=True,**kwargs)
def sql(statement,database='postgres'):
 r=run(['docker','exec','-i',name,'psql','-U','postgres','-d',database,'-At','-v','ON_ERROR_STOP=1'],input=statement,capture_output=True,text=True)
 return r.stdout.strip()
f=json.loads((ROOT/'contracts/fixtures/audit-capacity-schema.json').read_text())
prepared=(ROOT/'contracts/persistence'/f['path']).read_text()
assert hashlib.sha256(prepared.encode()).hexdigest()==f['checksum']
def setup(database,ready=True):
 sql('CREATE DATABASE '+database)
 sql((ROOT/'contracts/persistence/session/0001.sql').read_text()+(ROOT/'contracts/persistence/session/0002.sql').read_text()+prepared,database)
 # Test-only empty database fixture. Not a supported historical upgrade/activation.
 sql("INSERT INTO heimdall_capacity.policy(event_limit,byte_limit,delivery_limit) VALUES(3,4500000,96);"+("UPDATE heimdall_capacity.policy SET state='ready';" if ready else ''),database)
 sql("GRANT USAGE ON SCHEMA heimdall,heimdall_capacity TO capacity_runtime; GRANT SELECT,INSERT ON heimdall.audit_events,heimdall.audit_deliveries TO capacity_runtime; GRANT SELECT,INSERT,UPDATE ON heimdall.audit_decisions,heimdall_capacity.reservations TO capacity_runtime; GRANT SELECT,INSERT ON heimdall_capacity.event_charges TO capacity_runtime; GRANT SELECT ON heimdall_capacity.policy TO capacity_runtime; GRANT UPDATE(used_events,used_bytes,used_deliveries,reserved_events,reserved_bytes,reserved_deliveries) ON heimdall_capacity.policy TO capacity_runtime;",database)
def suite(language,selected):
 if language=='go':subprocess.run(['go','test','-race','-count=1','./internal/postgres','-run','^TestAuditCapacityLive$'],cwd=ROOT/'sdk/go',env=selected,check=True)
 else:run(['pnpm','--filter','@techsavvyash/heimdall-sdk','exec','vitest','run','test/postgres-audit-capacity.test.ts'],env=selected)
try:
 run(['docker','run','--detach','--name',name,'-e','POSTGRES_PASSWORD','-p','127.0.0.1::5432',IMAGE],env=env,stdout=subprocess.DEVNULL);created=True
 for _ in range(60):
  if subprocess.run(['docker','exec',name,'pg_isready','-U','postgres'],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL).returncode==0:break
  time.sleep(.5)
 else:raise RuntimeError('PostgreSQL readiness timeout')
 port=run(['docker','port',name,'5432'],capture_output=True,text=True).stdout.strip().split(':')[-1]
 sql('CREATE ROLE capacity_runtime NOLOGIN')
 def dsn(database,selected_port=port):return f"postgres://postgres:{env['POSTGRES_PASSWORD']}@127.0.0.1:{selected_port}/{database}?sslmode=disable&options=-c%20role%3Dcapacity_runtime"
 scenarios=json.loads((ROOT/'contracts/fixtures/audit-capacity-writes.json').read_text())['scenarios']
 for language in ('ts','go'):
  for mode in [x for x in scenarios if not x.startswith('peer_')]:
   database=language+'_'+mode;setup(database,mode!='preparing')
   if mode=='bytes':sql('UPDATE heimdall_capacity.policy SET byte_limit=3000000',database)
   if mode=='deliveries':sql('UPDATE heimdall_capacity.policy SET delivery_limit=64',database)
   if mode=='sink_failure':sql("CREATE FUNCTION heimdall.reject_capacity_admin() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.event_id LIKE 'recovery-%' AND NEW.event_id<>'recovery-terminal' THEN RAISE EXCEPTION 'test admin sink failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_capacity_admin BEFORE INSERT ON heimdall.audit_deliveries FOR EACH ROW EXECUTE FUNCTION heimdall.reject_capacity_admin()",database)
   selected=dict(env,HEIMDALL_TEST_CAPACITY_URL=dsn(database),HEIMDALL_TEST_CAPACITY_MODE=mode)
   if mode.startswith('loss_'):
    with CommitLossProxy(int(port)) as proxy:
     suite(language,dict(selected,HEIMDALL_TEST_CAPACITY_LOSS=dsn(database,proxy.port)))
     assert proxy.dropped.is_set() and not proxy.errors
   else:suite(language,selected)
 for first,second in [('ts','go'),('go','ts')]:
  database='peer_'+first;setup(database)
  for language,mode in [(first,'peer_request'),(second,'peer_recovery')]:
   suite(language,dict(env,HEIMDALL_TEST_CAPACITY_URL=dsn(database),HEIMDALL_TEST_CAPACITY_MODE=mode))
 print(json.dumps({'run':name,'result':'passed','checks':['TS and Go real validated ha1 events','restricted runtime credentials','atomic admission and full-capacity refusal','normal completion releases recovery reserve','recovery consumes reserve at full admission','original retry receipts and sinks preserved','changed payload and late terminal conflict','missing charge and reservation fail closed','preparing policy refuses writes','normal versus recovery race','request ordinary completion and recovery COMMIT loss','TS to Go and Go to TS recovery','eight cross-product SDK admissions allow one','independent byte and delivery limits','recovery administration sink failure rolls back pair and slot']}))
finally:
 if created:run(['docker','rm','--force','--volumes',name],stdout=subprocess.DEVNULL)

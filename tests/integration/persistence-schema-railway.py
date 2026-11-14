"""Shared DDL/constraint proof in an owned disposable application database on Railway."""
import concurrent.futures
import hashlib
import json
import pathlib
import shlex
import subprocess
import sys
import uuid

assert __debug__ and sys.argv[1:] == ['--execute-verification-persistence']
ROOT = pathlib.Path(__file__).resolve().parents[2]
RUN = uuid.uuid4().hex
DATABASE = 'heimdall_storage_'+RUN
ROLE = 'heimdall_runtime_'+RUN
PROJECT = '944fc9be-8648-4237-82bc-72e4bd24a4c9'
ENVIRONMENT = '7bc7c286-b2dc-4415-85eb-90dd554ab2fb'
PG = '648133ac-305b-495d-a77e-4d0c49103feb'
KEY = str(pathlib.Path.home()/'.ssh/heimdall-verification')

def sql(query, database=DATABASE):
    command = 'psql -X -U heimdall_probe -d '+shlex.quote(database)+' -v ON_ERROR_STOP=1 -Atq -c '+shlex.quote(query)
    r = subprocess.run(['railway','ssh','--service',PG,'--environment',ENVIRONMENT,'--project',PROJECT,
        '--identity-file',KEY,'--',command],capture_output=True,text=True,timeout=60)
    assert r.returncode == 0, 'SQL probe failed: '+r.stderr[-2000:]
    return r.stdout.strip()

migration = ROOT/'contracts/persistence/session/0001.sql'
checksum = hashlib.sha256(migration.read_bytes()).hexdigest()
catalog = json.loads((ROOT/'contracts/persistence/catalog.json').read_text())
assert catalog['session'][0]['checksum'] == checksum
created = role_created = False
try:
    sql('CREATE DATABASE '+DATABASE, 'postgres'); created=True
    sql('CREATE ROLE '+ROLE+' NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT', 'postgres'); role_created=True
    sql("BEGIN; SELECT pg_advisory_xact_lock(74162401); "+migration.read_text()+
        " INSERT INTO heimdall.migrations(version,checksum) VALUES(1,'"+checksum+"');"
        " INSERT INTO heimdall.schema_metadata VALUES(true,1,'ready',1,1,1,1); COMMIT;")
    sql('GRANT USAGE ON SCHEMA heimdall TO '+ROLE+'; GRANT SELECT ON ALL TABLES IN SCHEMA heimdall TO '+ROLE+'; '
        'GRANT SELECT,INSERT,UPDATE,DELETE ON heimdall.records,heimdall.identity_denials TO '+ROLE+'; '
        'GRANT INSERT ON heimdall.audit_events TO '+ROLE+'; GRANT INSERT,UPDATE ON heimdall.audit_deliveries TO '+ROLE+'; '
        'GRANT USAGE ON SEQUENCE heimdall.record_revision_seq TO '+ROLE+';')
    proof = r"""
SET ROLE RUNTIME_ROLE;
DO $$
DECLARE original bigint; changed bigint; recreated bigint; n integer;
BEGIN
  IF has_schema_privilege(current_user,'heimdall','CREATE') OR has_table_privilege(current_user,'heimdall.migrations','INSERT')
     OR has_table_privilege(current_user,'heimdall.audit_events','UPDATE') OR has_table_privilege(current_user,'heimdall.audit_events','DELETE')
     OR has_sequence_privilege(current_user,'heimdall.record_revision_seq','UPDATE') THEN
    RAISE EXCEPTION 'runtime has migration, audit mutation or sequence-reset privilege';
  END IF;
  BEGIN CREATE TABLE heimdall.forbidden(id int); RAISE EXCEPTION 'runtime DDL succeeded';
    EXCEPTION WHEN insufficient_privilege THEN NULL; END;
  INSERT INTO heimdall.records(product_id,environment,kind,key_hash,key_id,envelope,expires_at)
    VALUES('ledger','production','session',decode(repeat('01',32),'hex'),'key1','synthetic-envelope',5000) RETURNING revision INTO original;
  INSERT INTO heimdall.records(product_id,environment,kind,key_hash,key_id,envelope,expires_at)
    VALUES('support','production','session',decode(repeat('01',32),'hex'),'key1','other-product',5000),
          ('ledger','staging','session',decode(repeat('01',32),'hex'),'key1','other-environment',5000),
          ('ledger','production','login',decode(repeat('01',32),'hex'),'key1','other-kind',5000);
  UPDATE heimdall.records SET envelope='wrong-scope' WHERE product_id='missing' AND revision=original;
  GET DIAGNOSTICS n = ROW_COUNT; IF n<>0 THEN RAISE EXCEPTION 'wrong scope matched'; END IF;
  UPDATE heimdall.records SET envelope='updated', revision=original WHERE product_id='ledger' AND environment='production' AND kind='session' AND revision=original AND expires_at>1000 RETURNING revision INTO changed;
  IF changed IS NULL OR changed<=original THEN RAISE EXCEPTION 'CAS did not advance revision'; END IF;
  UPDATE heimdall.records SET envelope='stale' WHERE product_id='ledger' AND environment='production' AND kind='session' AND revision=original;
  GET DIAGNOSTICS n = ROW_COUNT; IF n<>0 THEN RAISE EXCEPTION 'stale CAS succeeded'; END IF;
  UPDATE heimdall.records SET envelope='expired' WHERE product_id='ledger' AND environment='production' AND kind='session' AND revision=changed AND expires_at>5000;
  GET DIAGNOSTICS n = ROW_COUNT; IF n<>0 THEN RAISE EXCEPTION 'expiry boundary accepted'; END IF;
  DELETE FROM heimdall.records WHERE product_id='ledger' AND environment='production' AND kind='session';
  INSERT INTO heimdall.records(product_id,environment,kind,key_hash,revision,key_id,envelope,expires_at)
    VALUES('ledger','production','session',decode(repeat('01',32),'hex'),original,'key1','recreated',5000) RETURNING revision INTO recreated;
  IF recreated<=changed THEN RAISE EXCEPTION 'delete/recreate reused revision'; END IF;
  BEGIN
    INSERT INTO heimdall.audit_events VALUES('ledger','production','rollback',decode(repeat('02',32),'hex'),1000,
      '{"eventId":"rollback","productId":"ledger","environment":"production","recordedAt":1000}');
    INSERT INTO heimdall.audit_deliveries(product_id,environment,event_id,sink_id) VALUES('support','production','rollback','siem');
    RAISE EXCEPTION 'cross-product foreign key accepted';
  EXCEPTION WHEN foreign_key_violation THEN NULL; END;
  IF EXISTS(SELECT 1 FROM heimdall.audit_events WHERE event_id='rollback') THEN RAISE EXCEPTION 'failed outbox left audit event committed'; END IF;
  INSERT INTO heimdall.audit_events VALUES('ledger','production','committed',decode(repeat('03',32),'hex'),1000,
    '{"eventId":"committed","productId":"ledger","environment":"production","recordedAt":1000}');
  INSERT INTO heimdall.audit_deliveries(product_id,environment,event_id,sink_id) VALUES('ledger','production','committed','siem');
  BEGIN
    INSERT INTO heimdall.audit_events VALUES('ledger','production','bad-scope',decode(repeat('03',32),'hex'),1000,
      '{"eventId":"bad-scope","productId":"support","environment":"production","recordedAt":1000}');
    RAISE EXCEPTION 'event scope mismatch accepted';
  EXCEPTION WHEN check_violation THEN NULL; END;
  BEGIN UPDATE heimdall.audit_deliveries SET state='delivered'; RAISE EXCEPTION 'missing delivered timestamp accepted';
  EXCEPTION WHEN check_violation THEN NULL; END;
END;
$$;
""".replace('RUNTIME_ROLE',ROLE)
    sql(proof)
    revision=sql("SELECT revision FROM heimdall.records WHERE product_id='ledger' AND environment='production' AND kind='session'")
    assert revision.isdigit()
    cas="SET ROLE "+ROLE+"; WITH changed AS (UPDATE heimdall.records SET envelope='race-winner' WHERE product_id='ledger' AND environment='production' AND kind='session' AND revision="+revision+" RETURNING 1) SELECT count(*) FROM changed;"
    with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
        results=list(pool.map(lambda _: sql(cas),range(2)))
    assert sorted(results)==['0','1'], 'Concurrent CAS must have exactly one winner'
    assert sql("SELECT count(*) FROM heimdall.audit_events e JOIN heimdall.audit_deliveries d USING(product_id,environment,event_id)")=='1'
    tables=sql("SELECT tablename FROM pg_tables WHERE schemaname='heimdall' ORDER BY tablename").splitlines()
    assert tables==['audit_deliveries','audit_events','identity_denials','migrations','records','schema_metadata']
    observations={'tables':tables,'scopeIsolation':True,'staleAndExpiredCASRejected':True,'deleteRecreateRevisionAdvanced':True,
      'concurrentCASWinners':1,'runtimeDDLAndAuditMutationDenied':True,'auditOutboxRollbackAtomic':True,'committedAuditOutboxVisible':True}
finally:
    if created: sql('DROP DATABASE '+DATABASE+' WITH (FORCE)', 'postgres')
    if role_created: sql('DROP ROLE '+ROLE, 'postgres')
print(json.dumps({'suite':'persistence-schema-v1','runId':RUN,'migrationChecksum':checksum,'observations':observations,
 'cleanup':'owned application database and runtime role deleted',
 'limitations':['database schema proof only; TS/Go adapters and migration runner not implemented',
 'synthetic encrypted envelopes and audit payloads; application schema/crypto validation remains required',
 'same cluster as verification services, separate disposable application database']},indent=2))

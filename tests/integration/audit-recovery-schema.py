"""Prepared audit decision schema proof; runtime migration/recovery remain separate work."""
import concurrent.futures
import hashlib
import json
import os
from pathlib import Path
import secrets
import subprocess
import time
import uuid

ROOT = Path(__file__).resolve().parents[2]
IMAGE = "postgres:17.11-alpine@sha256:3540c0362c809df084ffac0ae69fe794515f9de92692f02178bcdce0ef0302ac"
fixture = json.loads((ROOT / "contracts/fixtures/audit-recovery-schema.json").read_text())
migration = (ROOT / "contracts/persistence" / fixture["path"]).read_text()
assert hashlib.sha256(migration.encode()).hexdigest() == fixture["checksum"]
name = "heimdall-audit-recovery-" + uuid.uuid4().hex
env = dict(os.environ, POSTGRES_PASSWORD=secrets.token_hex(24))
created = False


def sql(statement, fail=False):
    result = subprocess.run(["docker", "exec", "-i", name, "psql", "-U", "postgres", "-At", "-v", "ON_ERROR_STOP=1", "-v", "VERBOSITY=verbose"], input=statement, text=True, capture_output=True)
    if not fail and result.returncode:
        raise RuntimeError(result.stderr)
    return result if fail else result.stdout.strip()


def event(identifier, scope="ledger", recorded=10):
    # Physical-envelope fixture, not an SDK-valid security event. SQL checks only
    # routing/foreign-key integrity; runtime will validate ha1 payload semantics.
    return f"INSERT INTO heimdall.audit_events VALUES('{scope}','test','{identifier}',decode(repeat('a',64),'hex'),{recorded},jsonb_build_object('eventId','{identifier}','productId','{scope}','environment','test','recordedAt',{recorded}));"


try:
    subprocess.run(["docker", "run", "--detach", "--name", name, "-e", "POSTGRES_PASSWORD", IMAGE], env=env, check=True, stdout=subprocess.DEVNULL)
    created = True
    for _ in range(60):
        if subprocess.run(["docker", "exec", name, "pg_isready", "-h", "127.0.0.1", "-U", "postgres"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode == 0:
            break
        time.sleep(0.5)
    else:
        raise RuntimeError("PostgreSQL readiness timeout")
    refused = subprocess.run(["go", "run", "./cmd/heimdall-migrate", "-initialize-session-schema", "-sql", str(ROOT / "contracts/persistence/session/0002.sql")], cwd=ROOT / "sdk/go", env=dict(os.environ, HEIMDALL_MIGRATION_DATABASE_URL="postgres://localhost:1/not-used?sslmode=disable"), capture_output=True, text=True)
    assert refused.returncode and "invalid_argument" in refused.stderr
    sql((ROOT / "contracts/persistence/session/0001.sql").read_text())
    # An interrupted deployment transaction must leave neither table nor trigger.
    sql("BEGIN; " + migration + " ROLLBACK;")
    assert sql("SELECT to_regclass('heimdall.audit_decisions') IS NULL") == "t"
    sql("BEGIN; " + migration + " COMMIT;")
    sql(event("request") + event("terminal", recorded=20))
    sql("INSERT INTO heimdall.audit_decisions VALUES('ledger','test','decision','request',10,'pending',NULL,NULL)")
    for case in fixture["cases"]:
        failed = sql(case["sql"], fail=True)
        assert failed.returncode and case["sqlState"] in failed.stderr, case["name"]
    sql("CREATE ROLE audit_writer NOLOGIN; GRANT USAGE ON SCHEMA heimdall TO audit_writer; GRANT SELECT,INSERT ON heimdall.audit_events,heimdall.audit_deliveries TO audit_writer; GRANT SELECT,INSERT,UPDATE ON heimdall.audit_decisions TO audit_writer;")
    for statement in ["DELETE FROM heimdall.audit_decisions", "TRUNCATE heimdall.audit_decisions", "ALTER TABLE heimdall.audit_decisions ADD COLUMN bypass boolean"]:
        assert sql("SET ROLE audit_writer; " + statement, fail=True).returncode
    # Simulated normal finalizer versus recovery: a losing terminal event/outbox
    # must roll back with its failed pending-to-terminal CAS in the SAME tx.
    def contender(resolution, decision="decision"):
        identifier = "race-" + decision + "-" + resolution
        statement = "SET ROLE audit_writer; DO $race$ BEGIN " + event(identifier, recorded=20)
        statement += f"INSERT INTO heimdall.audit_deliveries(product_id,environment,event_id,sink_id) VALUES('ledger','test','{identifier}','siem');"
        statement += f"UPDATE heimdall.audit_decisions SET resolution='{resolution}',terminal_event_id='{identifier}',resolved_at=20 WHERE product_id='ledger' AND environment='test' AND decision_id='{decision}' AND resolution='pending';"
        statement += "IF NOT FOUND THEN RAISE EXCEPTION 'lost resolution race' USING ERRCODE='40001'; END IF; END $race$;"
        result = sql(statement, fail=True)
        if result.returncode:
            assert "40001" in result.stderr
        return result.returncode == 0
    with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
        assert sorted(pool.map(contender, ["completed", "recovered"])) == [False, True]
    assert sql("SELECT count(*) FROM heimdall.audit_events WHERE event_id LIKE 'race-%'") == "1"
    assert sql("SELECT count(*) FROM heimdall.audit_deliveries WHERE event_id LIKE 'race-%'") == "1"
    winner = sql("SELECT resolution FROM heimdall.audit_decisions WHERE decision_id='decision'")
    assert winner in ("completed", "recovered")
    # A terminal slot cannot reopen, change winner or change resolved time.
    for patch in ["resolution='pending',terminal_event_id=NULL,resolved_at=NULL", "terminal_event_id='terminal'", "resolved_at=21"]:
        failed = sql("UPDATE heimdall.audit_decisions SET " + patch + " WHERE decision_id='decision'", fail=True)
        assert failed.returncode and "23514" in failed.stderr
    for first in ("completed", "recovered"):
        decision = "first-" + first
        request = decision + "-request"
        sql(event(request) + f"INSERT INTO heimdall.audit_decisions VALUES('ledger','test','{decision}','{request}',10,'pending',NULL,NULL)")
        assert contender(first, decision)
        assert not contender("recovered" if first == "completed" else "completed", decision)
        assert sql(f"SELECT resolution FROM heimdall.audit_decisions WHERE decision_id='{decision}'") == first
        assert sql(f"SELECT count(*) FROM heimdall.audit_events WHERE event_id LIKE 'race-{decision}-%'") == "1"
    # Prove the decision FK independently of audit_deliveries' own retention FK.
    sql(event("retention-request") + "INSERT INTO heimdall.audit_decisions VALUES('ledger','test','retention','retention-request',10,'pending',NULL,NULL)")
    sql("UPDATE heimdall.audit_decisions SET resolution='completed',terminal_event_id='terminal',resolved_at=20 WHERE decision_id='retention'")
    failed = sql("DELETE FROM heimdall.audit_events WHERE event_id='terminal'", fail=True)
    assert failed.returncode and "23503" in failed.stderr
    # Exact no-op is harmless; same event ID is not proof a changed payload is safe.
    sql("UPDATE heimdall.audit_decisions SET resolution=resolution WHERE decision_id='decision'")
    failed = sql("DELETE FROM heimdall.audit_events WHERE event_id=(SELECT terminal_event_id FROM heimdall.audit_decisions WHERE decision_id='decision')", fail=True)
    assert failed.returncode and "23503" in failed.stderr
    sql(event("rollback-request"))
    sql("INSERT INTO heimdall.audit_decisions VALUES('ledger','test','rollback','rollback-request',10,'pending',NULL,NULL)")
    failed = sql("BEGIN; " + event("rollback-terminal", recorded=20) + "UPDATE heimdall.audit_decisions SET resolution='recovered',terminal_event_id='rollback-terminal',resolved_at=20 WHERE decision_id='rollback'; INSERT INTO heimdall.audit_deliveries(product_id,environment,event_id,sink_id) VALUES('ledger','test','rollback-terminal','invalid sink'); COMMIT;", fail=True)
    assert failed.returncode
    assert sql("SELECT resolution FROM heimdall.audit_decisions WHERE decision_id='rollback'") == "pending"
    assert sql("SELECT count(*) FROM heimdall.audit_events WHERE event_id='rollback-terminal'") == "0"
    # Partition boundaries and ordered unresolved work remain explicit.
    sql(event("other-request", scope="support") + "INSERT INTO heimdall.audit_decisions VALUES('support','test','other','other-request',10,'pending',NULL,NULL)")
    assert sql("SELECT decision_id FROM heimdall.audit_decisions WHERE product_id='ledger' AND environment='test' AND resolution='pending' AND requested_at<=10 ORDER BY requested_at,decision_id COLLATE \"C\" LIMIT 100") == "rollback"
    print(json.dumps({"run": name, "result": "passed", "postgres": IMAGE, "checksum": fixture["checksum"], "constraintCases": len(fixture["cases"]), "raceWinner": winner, "checks": ["current migration CLI refuses prepared v2", "transactional DDL rollback", "scoped event foreign keys", "immutable request and terminal state", "runtime cannot delete or run DDL", "normal finalizer versus recovery single winner", "both deterministic resolution orders preserve first terminal", "losing terminal event and outbox roll back", "outbox failure preserves pending request", "terminal retention restriction", "scoped pending scan"], "limitations": ["prepared migration not registered or runtime enabled", "SQL routing fixtures are not SDK audit payloads", "no backfill, SDK recovery or protection-release proof yet"]}))
finally:
    if created:
        subprocess.run(["docker", "rm", "--force", name], check=True, stdout=subprocess.DEVNULL)

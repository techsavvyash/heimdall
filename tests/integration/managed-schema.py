"""Managed SQL foundation proof in a disposable application database; no runtime facade claim."""
import concurrent.futures
import hashlib
import json
import os
from pathlib import Path
import secrets
import shlex
import subprocess
import sys
import time
import uuid

assert __debug__ and sys.argv[1:] in ([], ["--execute-verification-managed"])
railway = bool(sys.argv[1:])
ROOT = Path(__file__).resolve().parents[2]
run_id = uuid.uuid4().hex
name = "heimdall-managed-" + run_id
database = "heimdall_managed_" + run_id
role = "heimdall_managed_" + run_id if railway else "heimdall_managed_runtime"
image = "postgres:17.11-alpine@sha256:3540c0362c809df084ffac0ae69fe794515f9de92692f02178bcdce0ef0302ac"
env = dict(os.environ, POSTGRES_PASSWORD=secrets.token_hex(24))
created = role_created = container_created = False

def sql(query, db=None, fail=False):
    db = db or database
    if railway:
        command = "psql -X -U heimdall_probe -d " + shlex.quote(db) + " -v ON_ERROR_STOP=1 -Atq -c " + shlex.quote(query)
        args = ["railway", "ssh", "--service", "648133ac-305b-495d-a77e-4d0c49103feb", "--environment", "7bc7c286-b2dc-4415-85eb-90dd554ab2fb", "--project", "944fc9be-8648-4237-82bc-72e4bd24a4c9", "--identity-file", str(Path.home()/".ssh/heimdall-verification"), "--", command]
        result = subprocess.run(args,capture_output=True,text=True,timeout=60)
    else:
        result = subprocess.run(["docker","exec","-i",name,"psql","-X","-U","postgres","-d",db,"-v","ON_ERROR_STOP=1","-Atq"],input=query,capture_output=True,text=True,timeout=60)
    if fail:
        assert result.returncode != 0, "expected transaction failure"
    else:
        assert result.returncode == 0, "SQL proof failed: " + result.stderr[-1200:]
    return result.stdout.strip()

catalog=json.loads((ROOT/"contracts/persistence/catalog.json").read_text())
fixture=json.loads((ROOT/"contracts/fixtures/managed-schema.json").read_text())
sql_bytes={}
for family in ("session","managed"):
    entry=catalog[family][0]
    raw=(ROOT/"contracts/persistence"/entry["path"]).read_bytes()
    assert hashlib.sha256(raw).hexdigest()==entry["checksum"]
    sql_bytes[family]=raw.decode()

def install(family):
    schema="heimdall" if family=="session" else "heimdall_managed"
    return sql_bytes[family]+f" INSERT INTO {schema}.migrations(version,checksum) VALUES(1,'{catalog[family][0]['checksum']}'); INSERT INTO {schema}.schema_metadata VALUES(true,1,'ready',1,1,1,1);"

try:
    if not railway:
        subprocess.run(["docker","run","--detach","--name",name,"-e","POSTGRES_PASSWORD","-p","127.0.0.1::5432",image],env=env,check=True,stdout=subprocess.DEVNULL)
        container_created=True
        for _ in range(60):
            r=subprocess.run(["docker","exec",name,"pg_isready","-U","postgres"],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
            if r.returncode==0:break
            time.sleep(.5)
        else:raise RuntimeError("PostgreSQL readiness timeout")
        port=subprocess.check_output(["docker","port",name,"5432"],text=True).strip().split(":")[-1]
    sql("CREATE DATABASE "+database,"postgres");created=True
    sql("CREATE ROLE "+role+" NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT","postgres");role_created=True
    sql("BEGIN; "+install("session")+" COMMIT;")
    assert sql("SELECT count(*) FROM pg_namespace WHERE nspname='heimdall_managed'")=="0"
    sql("BEGIN; "+install("managed")+" SELECT 1/0; COMMIT;",fail=True)
    assert sql("SELECT count(*) FROM pg_namespace WHERE nspname='heimdall_managed'")=="0", "partial managed migration survived rollback"
    sql("BEGIN; "+install("managed")+" COMMIT;")
    sql("GRANT USAGE ON SCHEMA heimdall_managed TO "+role+"; GRANT SELECT ON ALL TABLES IN SCHEMA heimdall_managed TO "+role+"; GRANT INSERT,UPDATE ON heimdall_managed.workspaces,heimdall_managed.operations TO "+role+"; GRANT INSERT,UPDATE,DELETE ON heimdall_managed.membership_candidates TO "+role+"; GRANT USAGE ON ALL SEQUENCES IN SCHEMA heimdall_managed TO "+role+";")
    sql("GRANT USAGE ON SCHEMA heimdall TO "+role+"; GRANT SELECT,INSERT ON heimdall.audit_events,heimdall.audit_deliveries TO "+role+";")
    assert sql("SELECT (SELECT count(*) FROM heimdall.migrations)=1 AND (SELECT count(*) FROM heimdall_managed.migrations)=1")=="t"
    scope="managed-sql"
    for query in fixture["setup"]:sql("SET ROLE "+role+"; "+query.replace("$scope",scope))
    # Each expected error is isolated in a PL/pgSQL subtransaction.
    checks="SET ROLE "+role+"; DO $proof$ BEGIN "
    for case in fixture["cases"]:
        checks+="BEGIN "+case["sql"].replace("$scope",scope)+"; RAISE EXCEPTION 'unexpected acceptance'; EXCEPTION WHEN SQLSTATE '"+case["sqlState"]+"' THEN NULL; END; "
    checks+="END $proof$;"
    sql(checks)
    sql("SET ROLE "+role+"; BEGIN; "+"; ".join(x.replace("$scope",scope) for x in fixture["rollback"])+"; ROLLBACK;")
    for table in ("workspaces","operations","membership_candidates"):
        assert sql("SELECT count(*) FROM heimdall_managed."+table+" WHERE workspace_id='rollback'")=="0"
    for table in ("audit_events","audit_deliveries"):
        assert sql("SELECT count(*) FROM heimdall."+table+" WHERE event_id='managed-rollback'")=="0"
    race_scope="managed-race"
    sql(fixture["setup"][0].replace("$scope",race_scope))
    def contender(i):
        statement=fixture["raceOperation"].replace("$scope",race_scope).replace("'original'","'race-"+str(i)+"'").replace("repeat('a',64)","repeat('"+str(i+1)+"',64)")
        return sql("SET ROLE "+role+"; WITH inserted AS ("+statement+" ON CONFLICT DO NOTHING RETURNING 1) SELECT count(*) FROM inserted;")
    with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
        assert sorted(pool.map(contender,range(2)))==["0","1"],"unfinished operation race did not serialize"
    if not railway:
        env["HEIMDALL_TEST_MANAGED_DATABASE_URL"]=f"postgres://postgres:{env['POSTGRES_PASSWORD']}@127.0.0.1:{port}/{database}?sslmode=disable"
        subprocess.run(["pnpm","--filter","@heimdall/sdk","exec","vitest","run","test/postgres-managed-schema.test.ts"],cwd=ROOT,env=env,check=True)
        env["HEIMDALL_TEST_MANAGED_READ_PEER"]="1"
        subprocess.run(["go","test","-race","-count=1","./internal/postgres","-run","^TestManagedSchema$"],cwd=ROOT/"sdk/go",env=env,check=True)
        subprocess.run(["pnpm","--filter","@heimdall/sdk","exec","vitest","run","test/postgres-managed-schema.test.ts"],cwd=ROOT,env=env,check=True)
    result={"run":run_id,"backend":"Railway verification application database" if railway else image,"checksums":{k:catalog[k][0]["checksum"] for k in ("session","managed")},"constraintCases":len(fixture["cases"]),"checks":["session-only install excludes managed schema","failed managed DDL transaction leaves no schema","independent managed metadata","scoped foreign keys","immutable intent and identifiers","uncertain operation retains unique slot","concurrent unfinished inserts have one winner","runtime cannot delete history, reset sequences or run DDL","workspace operation candidate audit and outbox transaction rollback"],"sdkSQLTests":"TS and Go constraints and bidirectional byte reads passed" if not railway else "same shared SQL vectors executed remotely","result":"passed","limitations":["physical SQL foundation, not managed SDK orchestration","payload binding and state transitions must be validated by future adapters","deployment migration/status integration remains pending"]}
finally:
    if created:sql("DROP DATABASE "+database,"postgres")
    if role_created:sql("DROP ROLE "+role,"postgres")
    if container_created:subprocess.run(["docker","rm","--force",name],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL,check=True)
print(json.dumps(result))

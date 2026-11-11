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
import tempfile
from pg_commit_loss import CommitLossProxy
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
binary_dir = None

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
        # The image temporary initialization server accepts sockets, not TCP.
        for _ in range(60):
            r=subprocess.run(["docker","exec",name,"pg_isready","-h","127.0.0.1","-U","postgres"],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
            if r.returncode==0:break
            time.sleep(.5)
        else:raise RuntimeError("PostgreSQL readiness timeout")
        port=subprocess.check_output(["docker","port",name,"5432"],text=True).strip().split(":")[-1]
    sql("CREATE DATABASE "+database,"postgres");created=True
    sql("CREATE ROLE "+role+" NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT","postgres");role_created=True
    if not railway:
        binary_dir=tempfile.TemporaryDirectory(prefix="heimdall-managed-cli-")
        binary=str(Path(binary_dir.name)/"heimdall-migrate")
        subprocess.run(["go","build","-o",binary,"./cmd/heimdall-migrate"],cwd=ROOT/"sdk/go",check=True)
        direct=f"postgres://postgres:{env['POSTGRES_PASSWORD']}@127.0.0.1:{port}/{database}?sslmode=disable"
        managed_args=["-initialize-managed-schema","-sql",str(ROOT/"contracts/persistence/managed/0001.sql")]
        def cli(args,dsn=direct,expected=0):
            r=subprocess.run([binary]+args,env=dict(env,HEIMDALL_MIGRATION_DATABASE_URL=dsn),capture_output=True,text=True,timeout=40)
            assert r.returncode==expected,"migration CLI result mismatch"
            return r
        assert "unavailable" in cli(managed_args,expected=1).stderr
        assert sql("SELECT count(*) FROM pg_namespace WHERE nspname IN ('heimdall','heimdall_managed')")=="0"
        for args in (["-status","unknown"],["-status","managed","-sql","ignored"],["-status","session","-initialize-session-schema"],[]):cli(args,expected=2)
    sql("BEGIN; "+install("session")+" COMMIT;")
    if not railway:
        cli(["-status","session"])
        assert "unavailable" in cli(["-status","managed"],expected=1).stderr
        subprocess.run(["pnpm","--filter","@heimdall/sdk","exec","vitest","run","test/postgres-managed-readiness.test.ts","-t","missing managed schema"],cwd=ROOT,env=dict(env,HEIMDALL_TEST_MANAGED_DATABASE_URL=direct,HEIMDALL_TEST_MANAGED_MISSING="1"),check=True)

    assert sql("SELECT count(*) FROM pg_namespace WHERE nspname='heimdall_managed'")=="0"
    sql("BEGIN; "+install("managed")+" SELECT 1/0; COMMIT;",fail=True)
    assert sql("SELECT count(*) FROM pg_namespace WHERE nspname='heimdall_managed'")=="0", "partial managed migration survived rollback"
    if railway:
        sql("BEGIN; "+install("managed")+" COMMIT;")
    else:
        sql("CREATE ROLE heimdall_managed_deploy NOLOGIN; GRANT CREATE ON DATABASE "+database+" TO heimdall_managed_deploy; GRANT USAGE ON SCHEMA heimdall TO heimdall_managed_deploy; GRANT SELECT ON heimdall.schema_metadata,heimdall.migrations TO heimdall_managed_deploy; REVOKE USAGE ON LANGUAGE plpgsql FROM PUBLIC;")
        deploy_dsn=direct+"&options=-c%20role%3Dheimdall_managed_deploy"
        assert "unavailable" in cli(managed_args,dsn=deploy_dsn,expected=1).stderr
        assert sql("SELECT count(*) FROM pg_namespace WHERE nspname='heimdall_managed'")=="0"
        sql("GRANT USAGE ON LANGUAGE plpgsql TO PUBLIC")
        with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
            list(pool.map(lambda _:cli(managed_args,dsn=deploy_dsn),range(2)))
        cli(["-status","managed"])
        sql("CREATE ROLE heimdall_managed_status NOLOGIN; GRANT USAGE ON SCHEMA heimdall,heimdall_managed TO heimdall_managed_status; GRANT SELECT ON heimdall.schema_metadata,heimdall.migrations,heimdall_managed.schema_metadata,heimdall_managed.migrations TO heimdall_managed_status;")
        cli(["-status","managed"],dsn=direct+"&options=-c%20role%3Dheimdall_managed_status")
        lost_db=database+"_lost"
        sql("CREATE DATABASE "+lost_db,"postgres")
        try:
            sql("BEGIN; "+install("session")+" COMMIT;",lost_db)
            lost_dsn=direct.replace("/"+database+"?","/"+lost_db+"?")
            with CommitLossProxy(int(port)) as proxy:
                failed=cli(managed_args,dsn=lost_dsn.replace(":"+port+"/",":"+str(proxy.port)+"/"),expected=1)
                assert "outcome_unknown" in failed.stderr and proxy.dropped.is_set() and not proxy.errors
            assert sql("SELECT state FROM heimdall_managed.schema_metadata",lost_db)=="ready"
            cli(managed_args,dsn=lost_dsn)
        finally:sql("DROP DATABASE "+lost_db,"postgres")

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
        subprocess.run(["pnpm","--filter","@heimdall/sdk","exec","vitest","run","test/postgres-managed-schema.test.ts","test/postgres-managed-readiness.test.ts","test/postgres-workspaces.test.ts","test/postgres-managed-audit.test.ts","test/postgres-operations.test.ts","test/postgres-operation-transitions.test.ts","test/postgres-candidates.test.ts","test/postgres-membership-intents.test.ts"],cwd=ROOT,env=env,check=True)
        env["HEIMDALL_TEST_MANAGED_READ_PEER"]="1"
        subprocess.run(["go","test","-race","-count=1","./internal/postgres","-run","^(TestManaged(Schema|Readiness|ReadinessCancelled|Migration|AuditAtomic|AuditInvalid)|TestWorkspace(Decoder|Input|Primary)|TestCandidate(Decoder|Input|Primary)|TestMembership(Input|Intent)|TestOperation(Journal|Input|Create|Transition|TransitionInput|MembershipCompletion))$"],cwd=ROOT/"sdk/go",env=env,check=True)
        subprocess.run(["pnpm","--filter","@heimdall/sdk","exec","vitest","run","test/postgres-managed-schema.test.ts","test/postgres-workspaces.test.ts","test/postgres-managed-audit.test.ts","test/postgres-operations.test.ts","test/postgres-operation-transitions.test.ts","test/postgres-candidates.test.ts","test/postgres-membership-intents.test.ts"],cwd=ROOT,env=env,check=True)
        for language in ("ts","go"):
            with CommitLossProxy(int(port)) as proxy:
                fault_env=dict(env,HEIMDALL_TEST_MANAGED_AUDIT_LOSS=direct.replace(":"+port+"/",":"+str(proxy.port)+"/"))
                if language=="ts":
                    subprocess.run(["pnpm","--filter","@heimdall/sdk","exec","vitest","run","test/postgres-managed-audit.test.ts","-t","atomic managed audit committed response loss"],cwd=ROOT,env=fault_env,check=True)
                else:
                    subprocess.run(["go","test","-race","-count=1","./internal/postgres","-run","^TestManagedAuditCommitLoss$"],cwd=ROOT/"sdk/go",env=fault_env,check=True)
                assert proxy.dropped.is_set() and not proxy.errors, language+" atomic audit COMMIT loss not verified"
        for language in ("ts","go"):
            with CommitLossProxy(int(port)) as proxy:
                fault_env=dict(env,HEIMDALL_TEST_CREATE_LOSS=direct.replace(":"+port+"/",":"+str(proxy.port)+"/"))
                command=["pnpm","--filter","@heimdall/sdk","exec","vitest","run","test/postgres-operations.test.ts","-t","operation create committed response loss and retry"] if language=="ts" else ["go","test","-race","-count=1","./internal/postgres","-run","^TestOperationCreateLoss$"]
                subprocess.run(command,cwd=ROOT if language=="ts" else ROOT/"sdk/go",env=fault_env,check=True)
                assert proxy.dropped.is_set() and not proxy.errors,language+" create COMMIT fault not verified"
        for language in ("ts","go"):
            with CommitLossProxy(int(port)) as proxy:
                fault_env=dict(env,HEIMDALL_TEST_TRANSITION_LOSS=direct.replace(":"+port+"/",":"+str(proxy.port)+"/"))
                command=["pnpm","--filter","@heimdall/sdk","exec","vitest","run","test/postgres-operation-transitions.test.ts","-t","transition committed response loss"] if language=="ts" else ["go","test","-race","-count=1","./internal/postgres","-run","^TestOperationTransitionLoss$"]
                subprocess.run(command,cwd=ROOT if language=="ts" else ROOT/"sdk/go",env=fault_env,check=True)
                assert proxy.dropped.is_set() and not proxy.errors,language+" transition COMMIT fault not verified"
        for language in ("ts","go"):
            with CommitLossProxy(int(port)) as proxy:
                fault_env=dict(env,HEIMDALL_TEST_MEMBERSHIP_LOSS=direct.replace(":"+port+"/",":"+str(proxy.port)+"/"))
                command=["pnpm","--filter","@heimdall/sdk","exec","vitest","run","test/postgres-membership-intents.test.ts","-t","membership intent committed response loss"] if language=="ts" else ["go","test","-race","-count=1","./internal/postgres","-run","^TestMembershipIntentLoss$"]
                subprocess.run(command,cwd=ROOT if language=="ts" else ROOT/"sdk/go",env=fault_env,check=True)
                assert proxy.dropped.is_set() and not proxy.errors,language+" membership COMMIT fault not verified"
        race_env=dict(env,HEIMDALL_TEST_CREATE_RACE="1")
        ts=subprocess.Popen(["pnpm","--filter","@heimdall/sdk","exec","vitest","run","test/postgres-operations.test.ts","-t","cross-language create race"],cwd=ROOT,env=race_env)
        go=subprocess.Popen(["go","test","-race","-count=1","./internal/postgres","-run","^TestOperationCreateRaceCross$"],cwd=ROOT/"sdk/go",env=race_env)
        assert [ts.wait(),go.wait()]==[0,0]
        for table in ("workspaces","operations"):
            assert sql("SELECT count(*) FROM heimdall_managed."+table+" WHERE product_id='creation-cross'")=="1"
        assert sql("SELECT count(*) FROM heimdall.audit_events WHERE product_id='creation-cross'")=="1"
        assert sql("SELECT count(*) FROM heimdall.audit_deliveries WHERE product_id='creation-cross'")=="2"
    result={"run":run_id,"backend":"Railway verification application database" if railway else image,"checksums":{k:catalog[k][0]["checksum"] for k in ("session","managed")},"constraintCases":len(fixture["cases"]),"checks":["session-only install excludes managed schema","failed managed DDL transaction leaves no schema","independent managed metadata","scoped foreign keys","immutable intent and identifiers","uncertain operation retains unique slot","concurrent unfinished inserts have one winner","runtime cannot delete history, reset sequences or run DDL","workspace operation candidate audit and outbox transaction rollback"],"sdkSQLTests":"TS and Go creation, membership intents and transitions, generation races, candidate completion and bounded primary pages, audit rollback, confirmed COMMIT loss and bidirectional reads passed" if not railway else "same shared SQL vectors executed remotely","migrationCLI":"missing prerequisite refusal, read-only status, mid-DDL rollback, concurrent install, confirmed commit loss and replay passed" if not railway else "SQL-only probe","result":"passed","limitations":["physical SQL foundation, not managed SDK orchestration","remote outcome verification and public managed orchestration remain pending","public discovery and rebuild coordination remain pending"]}
finally:
    if created:sql("DROP DATABASE "+database,"postgres")
    if role_created:sql("DROP ROLE "+role,"postgres")
    if binary_dir:binary_dir.cleanup()
    if container_created:subprocess.run(["docker","rm","--force",name],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL,check=True)
print(json.dumps(result))

"""Public TS/Go factories on activated audit v2 and mixed-schema deployment proof."""
import hashlib
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
IMAGE="postgres:17.11-alpine@sha256:3540c0362c809df084ffac0ae69fe794515f9de92692f02178bcdce0ef0302ac"
name="heimdall-factory-v2-"+uuid.uuid4().hex
env=dict(os.environ,POSTGRES_PASSWORD=secrets.token_hex(24))
created=False
if not __debug__:raise RuntimeError("assertions must be enabled")
def run(args,**kwargs):return subprocess.run(args,cwd=ROOT,check=True,**kwargs)
def sql(statement,database="postgres"):
 return run(["docker","exec","-i",name,"psql","-U","postgres","-d",database,"-At","-v","ON_ERROR_STOP=1"],input=statement,capture_output=True,text=True).stdout.strip()
try:
 run(["docker","run","--detach","--name",name,"-e","POSTGRES_PASSWORD","-p","127.0.0.1::5432",IMAGE],env=env,stdout=subprocess.DEVNULL);created=True
 for _ in range(60):
  if subprocess.run(["docker","exec",name,"pg_isready","-h","127.0.0.1","-U","postgres"],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL).returncode==0:break
  time.sleep(.5)
 else:raise RuntimeError("PostgreSQL readiness timeout")
 port=run(["docker","port",name,"5432"],capture_output=True,text=True).stdout.strip().split(":")[-1]
 def dsn(database,selected_port=port,role=False):return f"postgres://postgres:{env['POSTGRES_PASSWORD']}@127.0.0.1:{selected_port}/{database}?sslmode=disable"+('&options=-c%20role%3Dheimdall_public_runtime' if role else '')
 sql("CREATE ROLE heimdall_public_runtime NOLOGIN")
 with tempfile.TemporaryDirectory(prefix="heimdall-factory-cli-") as directory:
  binary=Path(directory)/"heimdall-migrate"
  subprocess.run(["go","build","-o",str(binary),"./cmd/heimdall-migrate"],cwd=ROOT/"sdk/go",check=True)
  for database in ("factory_v1","factory_migrating","factory_v2"):
   sql("CREATE DATABASE "+database)
   cli_env=dict(env,HEIMDALL_MIGRATION_DATABASE_URL=dsn(database))
   run([str(binary),"--initialize-session-schema","--sql",str(ROOT/"contracts/persistence/session/0001.sql")],env=cli_env,capture_output=True,text=True)
   if database!="factory_v1":
    run([str(binary),"--prepare-audit-upgrade","--writers-drained","--sql-directory",str(ROOT/"contracts/persistence/session")],env=cli_env,capture_output=True,text=True)
   if database=="factory_v2":
    run([str(binary),"--activate-audit-upgrade","--writers-drained"],env=cli_env,capture_output=True,text=True)
    run([str(binary),"--initialize-managed-schema","--sql",str(ROOT/"contracts/persistence/managed/0001.sql")],env=cli_env,capture_output=True,text=True)
    status=run([str(binary),"--status","managed-v2"],env=cli_env,capture_output=True,text=True);assert "v2 and managed schema v1 ready" in status.stdout
    readonly=dict(env,HEIMDALL_MIGRATION_DATABASE_URL=dsn(database)+"&options=-c%20default_transaction_read_only%3Don")
    refused=subprocess.run([str(binary),"--initialize-managed-schema","--sql",str(ROOT/"contracts/persistence/managed/0001.sql")],env=readonly,capture_output=True,text=True);assert refused.returncode==1 and "unavailable" in refused.stderr
   sql("GRANT USAGE ON SCHEMA heimdall TO heimdall_public_runtime; GRANT SELECT ON heimdall.schema_metadata,heimdall.migrations,heimdall.identity_denials,heimdall.records,heimdall.audit_events,heimdall.audit_deliveries TO heimdall_public_runtime; GRANT INSERT ON heimdall.audit_events,heimdall.audit_deliveries TO heimdall_public_runtime; GRANT INSERT,UPDATE ON heimdall.identity_denials TO heimdall_public_runtime; GRANT DELETE ON heimdall.records TO heimdall_public_runtime;",database)
   if database!="factory_v1":sql("GRANT SELECT,INSERT,UPDATE ON heimdall.audit_decisions TO heimdall_public_runtime",database)
  sql("GRANT USAGE ON SCHEMA heimdall_managed TO heimdall_public_runtime; GRANT SELECT ON heimdall_managed.schema_metadata,heimdall_managed.migrations TO heimdall_public_runtime", "factory_v2")
  sql("CREATE FUNCTION public.reject_factory_delivery() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.event_id='broken-request' THEN RAISE EXCEPTION 'test outbox failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_factory_delivery BEFORE INSERT ON heimdall.audit_deliveries FOR EACH ROW EXECUTE FUNCTION public.reject_factory_delivery();", "factory_v2")
  env.update(HEIMDALL_TEST_FACTORY_URL=dsn("factory_v2",role=True),HEIMDALL_TEST_FACTORY_ADMIN=dsn("factory_v2"),HEIMDALL_TEST_FACTORY_V1=dsn("factory_v1",role=True),HEIMDALL_TEST_FACTORY_MIGRATING=dsn("factory_migrating",role=True))
  subprocess.run(["go","test","-race","-count=1","./postgres","-run","^TestFactoryV2$"],cwd=ROOT/"sdk/go",env=env,check=True)
  run(["pnpm","--filter","@heimdall/sdk","exec","vitest","run","test/postgres-runtime-v2.test.ts"],env=env)
  subprocess.run(["go","test","-race","-count=1","./postgres","-run","^TestFactoryV2Peer$"],cwd=ROOT/"sdk/go",env=env,check=True)
  subprocess.run(["go","test","-race","-count=1","./internal/postgres","-run","^TestManagedAuditReadiness$"],cwd=ROOT/"sdk/go",env=env,check=True)
  for language in ("go","ts"):
   with CommitLossProxy(int(port)) as proxy:
    fault=dict(env,HEIMDALL_TEST_FACTORY_LOSS=dsn("factory_v2",proxy.port,True))
    if language=="go":subprocess.run(["go","test","-race","-count=1","./postgres","-run","^TestFactoryV2Loss$"],cwd=ROOT/"sdk/go",env=fault,check=True)
    else:run(["pnpm","--filter","@heimdall/sdk","exec","vitest","run","test/postgres-runtime-v2.test.ts","-t","public v2 request COMMIT loss"],env=fault)
    assert proxy.dropped.is_set() and not proxy.errors
 assert sql("SELECT count(*) FROM heimdall.audit_events","factory_v1")=="0"
 assert sql("SELECT count(*) FROM heimdall.audit_events","factory_migrating")=="0"
 assert sql("SELECT count(*) FROM heimdall.audit_decisions","factory_v2")=="6"
 print(json.dumps({"run":name,"postgres":IMAGE,"result":"passed","checks":["both public factories require activated v2", "direct append gates v1 and migrating state without writes", "restricted runtime roles cannot change metadata history or delete slots", "atomic request completion and outbox rollback", "conflict orphan and recovery-only outcome refusal", "shared deployment lock cancellation and connection reuse", "reciprocal TS Go receipt and slot replay", "public request COMMIT loss in both languages", "combined audit v2 managed v1 readiness parity", "managed initialization after v2 activation and read-only refusal"]}))
finally:
 if created:subprocess.run(["docker","rm","--force",name],check=True,stdout=subprocess.DEVNULL)

"""Offline audit v2 activation with historical binding proof and TS/Go compatibility."""
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
name="heimdall-activation-"+uuid.uuid4().hex
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
 def dsn(database,selected_port=port):return f"postgres://postgres:{env['POSTGRES_PASSWORD']}@127.0.0.1:{selected_port}/{database}?sslmode=disable"
 base=(ROOT/"contracts/persistence/session/0001.sql").read_text()
 for database in ("activation_main","activation_loss","activation_cli"):
  sql("CREATE DATABASE "+database);sql(base,database);sql("INSERT INTO heimdall.schema_metadata VALUES(true,1,'ready',1,1,1,1); INSERT INTO heimdall.migrations(version,checksum) VALUES(1,'"+hashlib.sha256(base.encode()).hexdigest()+"');",database)
 env["HEIMDALL_TEST_ACTIVATION_URL"]=dsn("activation_main")
 env["HEIMDALL_TEST_ACTIVATION_LOSS_DIRECT"]=dsn("activation_loss")
 env["HEIMDALL_TEST_ACTIVATION_CLI"]=dsn("activation_cli")
 subprocess.run(["go","test","-race","-count=1","./internal/postgres","-run","^TestAuditActivation(Input)?$"],cwd=ROOT/"sdk/go",env=env,check=True)
 run(["pnpm","--filter","@heimdall/sdk","exec","vitest","run","test/postgres-audit-readiness.test.ts"],env=env)
 with CommitLossProxy(int(port)) as proxy:
  fault=dict(env,HEIMDALL_TEST_ACTIVATION_LOSS=dsn("activation_loss",proxy.port))
  subprocess.run(["go","test","-race","-count=1","./internal/postgres","-run","^TestAuditActivationLoss$"],cwd=ROOT/"sdk/go",env=fault,check=True)
  assert proxy.dropped.is_set() and not proxy.errors
 with tempfile.TemporaryDirectory(prefix="heimdall-activation-cli-") as directory:
  binary=Path(directory)/"heimdall-migrate"
  subprocess.run(["go","build","-o",str(binary),"./cmd/heimdall-migrate"],cwd=ROOT/"sdk/go",check=True)
  cli_env=dict(env,HEIMDALL_MIGRATION_DATABASE_URL=dsn("activation_cli"))
  refused=subprocess.run([str(binary),"--activate-audit-upgrade"],env=cli_env,capture_output=True,text=True);assert refused.returncode==2
  for _ in range(2):
   result=run([str(binary),"--activate-audit-upgrade","--writers-drained"],env=cli_env,capture_output=True,text=True);assert "v2 ready" in result.stdout
  result=run([str(binary),"--status","audit-v2"],env=cli_env,capture_output=True,text=True);assert "v2 ready" in result.stdout
  result=subprocess.run([str(binary),"--status","session"],env=cli_env,capture_output=True,text=True);assert result.returncode==1
 assert sql("SELECT database_version::text||':'||state FROM heimdall.schema_metadata","activation_main")=="2:ready"
 assert sql("SELECT count(*) FROM heimdall.audit_events","activation_main")=="137"
 assert sql("SELECT count(*) FROM heimdall.audit_decisions","activation_main")=="8"
 print(json.dumps({"run":name,"postgres":IMAGE,"result":"passed","checks":["count and byte-bounded scan covers 136 historical events", "full binding validation rejects five corruption cases", "incomplete coverage and missing slots never publish readiness", "four concurrent activators and stable retries", "cancellation and connection reuse", "matching TS and Go readiness matrix", "v1 readers refuse activated v2", "COMMIT-loss inspected retry preserves receipt", "real CLI activation and explicit v2 status"]}))
finally:
 if created:subprocess.run(["docker","rm","--force","--volumes",name],check=True,stdout=subprocess.DEVNULL)

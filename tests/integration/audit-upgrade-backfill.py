"""Automatic audit scope registration and backfill proof; never activates v2 runtime readiness."""
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
name="heimdall-coordinate-"+uuid.uuid4().hex
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
 for database in ("coordinate_main","coordinate_bad","coordinate_loss","coordinate_cli","coordinate_custom"):
  sql("CREATE DATABASE "+database);sql(base,database);sql("INSERT INTO heimdall.schema_metadata VALUES(true,1,'ready',1,1,1,1); INSERT INTO heimdall.migrations(version,checksum) VALUES(1,'"+hashlib.sha256(base.encode()).hexdigest()+"');",database)
 env["HEIMDALL_TEST_COORDINATE_DATABASE_URL"]=dsn("coordinate_main");env["HEIMDALL_TEST_COORDINATE_BAD_DATABASE_URL"]=dsn("coordinate_bad")
 env["HEIMDALL_TEST_COORDINATE_CUSTOM_DATABASE_URL"]=dsn("coordinate_custom")
 env["HEIMDALL_TEST_COORDINATE_CLI_DATABASE_URL"]=dsn("coordinate_cli");env["HEIMDALL_TEST_COORDINATE_LOSS_DIRECT"]=dsn("coordinate_loss")
 subprocess.run(["go","test","-race","-count=1","./internal/postgres","-run","^TestAuditCoordinator(Input)?$"],cwd=ROOT/"sdk/go",env=env,check=True)
 with CommitLossProxy(int(port)) as proxy:
  fault=dict(env,HEIMDALL_TEST_COORDINATE_LOSS=dsn("coordinate_loss",proxy.port),HEIMDALL_TEST_COORDINATE_LOSS_DIRECT=dsn("coordinate_loss"))
  subprocess.run(["go","test","-race","-count=1","./internal/postgres","-run","^TestAuditCoordinatorLoss$"],cwd=ROOT/"sdk/go",env=fault,check=True)
  assert proxy.dropped.is_set() and not proxy.errors
 with tempfile.TemporaryDirectory(prefix="heimdall-coordinate-cli-") as directory:
  binary=Path(directory)/"heimdall-migrate"
  subprocess.run(["go","build","-o",str(binary),"./cmd/heimdall-migrate"],cwd=ROOT/"sdk/go",check=True)
  cli_env=dict(env,HEIMDALL_MIGRATION_DATABASE_URL=dsn("coordinate_cli"))
  args=[str(binary),"--prepare-audit-upgrade","--sql-directory",str(ROOT/"contracts/persistence/session")]
  refused=subprocess.run(args,env=cli_env,capture_output=True,text=True);assert refused.returncode==2
  for _ in range(2):
   result=run(args+["--writers-drained"],env=cli_env,capture_output=True,text=True);assert "runtime remains unavailable" in result.stdout
  backfill=[str(binary),"--backfill-audit-upgrade","--writers-drained","--batch-size","2"]
  refused=subprocess.run([str(binary),"--backfill-audit-upgrade"],env=cli_env,capture_output=True,text=True);assert refused.returncode==2
  registered=0;backfilled=0
  for _ in range(100):
   result=run(backfill,env=cli_env,capture_output=True,text=True);step=json.loads(result.stdout)
   if step["status"]=="registered":registered+=step["registered"]
   elif step["status"]=="backfilled":backfilled+=1;assert step["progress"]["processed"]<=2
   elif step["status"]=="coverage_complete":break
   else:raise AssertionError("unexpected step")
  else:raise AssertionError("CLI failed to finish backfill")
  assert registered==4 and backfilled>0
  assert sql("SELECT count(*) FROM heimdall.audit_decisions","coordinate_cli")=="8"
  assert sql("SELECT count(*) FROM heimdall.audit_decision_backfill WHERE phase='complete'","coordinate_cli")=="4"
  custom_env=dict(env,HEIMDALL_MIGRATION_DATABASE_URL=dsn("coordinate_custom"))
  custom_registered=run(backfill,env=custom_env,capture_output=True,text=True);assert json.loads(custom_registered.stdout)["status"]=="registered"
  custom_refused=subprocess.run(backfill,env=custom_env,capture_output=True,text=True);assert custom_refused.returncode==1 and "audit_unavailable" in custom_refused.stderr
  failed=json.loads(custom_refused.stderr.splitlines()[0]);assert failed["status"]=="backfill_failed" and failed["productId"]=="custom-product"
  assert sql("SELECT scanned_count FROM heimdall.audit_decision_backfill","coordinate_custom")=="0"
  bad_args=subprocess.run([str(binary),"--status","session","--batch-size","100"],env=cli_env,capture_output=True,text=True);assert bad_args.returncode==2
  status=subprocess.run([str(binary),"--status","session"],env=cli_env,capture_output=True,text=True);assert status.returncode==1
  bad_dir=Path(directory)/"bad";bad_dir.mkdir()
  for filename in ("0002.sql","0002-backfill.sql","0002-upgrade.sql"):(bad_dir/filename).write_bytes((ROOT/"contracts/persistence/session"/filename).read_bytes()+b" ")
  bad=subprocess.run([str(binary),"--prepare-audit-upgrade","--writers-drained","--sql-directory",str(bad_dir)],env=dict(env,HEIMDALL_MIGRATION_DATABASE_URL="postgres://unused@127.0.0.1:1/unused?sslmode=disable"),capture_output=True,text=True);assert bad.returncode==1 and "invalid_argument" in bad.stderr
 assert sql("SELECT state FROM heimdall.schema_metadata","coordinate_cli")=="migrating"
 print(json.dumps({"run":name,"postgres":IMAGE,"result":"passed","checks":["empty coverage and automatic scope discovery","bounded registration and event steps","one-connection pool and eight concurrent coordinators","two products and two environments each","receipt gate read-only and cancellation refusal","completed-scope count drift refusal","corrupt history returns failed scope without partial bindings","registration COMMIT loss and durable restart","real CLI progresses all scopes without manual enumeration","custom history refused without matching registry", "no readiness activation"]}))
finally:
 if created:subprocess.run(["docker","rm","--force","--volumes",name],check=True,stdout=subprocess.DEVNULL)

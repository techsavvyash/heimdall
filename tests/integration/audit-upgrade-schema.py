"""Pinned audit upgrade catalog verification and schema corruption proof."""
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
name="heimdall-schema-"+uuid.uuid4().hex
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
 base=(ROOT/"contracts/persistence/session/0001.sql").read_text()
 sql(base)
 sql("INSERT INTO heimdall.schema_metadata VALUES(true,1,'ready',1,1,1,1); INSERT INTO heimdall.migrations(version,checksum) VALUES(1,'"+hashlib.sha256(base.encode()).hexdigest()+"');")
 sql("CREATE DATABASE bad_base")
 sql(base,"bad_base")
 sql("INSERT INTO heimdall.schema_metadata VALUES(true,1,'ready',1,1,1,1); INSERT INTO heimdall.migrations(version,checksum) VALUES(1,'"+hashlib.sha256(base.encode()).hexdigest()+"');","bad_base")
 env["HEIMDALL_TEST_UPGRADE_SCHEMA_BAD_BASE_URL"]=f"postgres://postgres:{env['POSTGRES_PASSWORD']}@127.0.0.1:{port}/bad_base?sslmode=disable"
 env["HEIMDALL_TEST_UPGRADE_SCHEMA_URL"]=f"postgres://postgres:{env['POSTGRES_PASSWORD']}@127.0.0.1:{port}/postgres?sslmode=disable"
 subprocess.run(["go","test","-race","-count=1","./internal/postgres","-run","^TestAuditUpgradeSchema(Fixture|Rollback)?$"],cwd=ROOT/"sdk/go",env=env,check=True)
 assert sql("SELECT state FROM heimdall.schema_metadata")=="migrating"
 print(json.dumps({"run":name,"postgres":IMAGE,"result":"passed","checks":["pinned pristine catalog profile", "25 independent schema drift cases", "stable search path", "table DDL blocked during verification", "preparation and coordinator refuse persisted drift", "schema failure rolls back initial preparation", "table lock cancellation and connection reuse", "no readiness activation"]}))
finally:
 if created:subprocess.run(["docker","rm","--force",name],check=True,stdout=subprocess.DEVNULL)

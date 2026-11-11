"""Disposable shared-key ledger concurrency and ambiguous COMMIT checks."""
import json
import os
from pathlib import Path
import secrets
import subprocess
import time
import uuid
from pg_commit_loss import CommitLossProxy
ROOT=Path(__file__).resolve().parents[2]
IMAGE="postgres:17.11-alpine@sha256:3540c0362c809df084ffac0ae69fe794515f9de92692f02178bcdce0ef0302ac"
name="heimdall-key-usage-"+uuid.uuid4().hex
env=dict(os.environ,POSTGRES_PASSWORD=secrets.token_hex(24))
created=False
if not __debug__:raise RuntimeError("assertions must be enabled")
def run(args,**kwargs):return subprocess.run(args,cwd=ROOT,check=True,**kwargs)
def sql(statement):return run(["docker","exec","-i",name,"psql","-U","postgres","-At","-v","ON_ERROR_STOP=1"],input=statement,capture_output=True,text=True).stdout.strip()
try:
 run(["docker","run","--detach","--name",name,"-e","POSTGRES_PASSWORD","-p","127.0.0.1::5432",IMAGE],env=env,stdout=subprocess.DEVNULL);created=True
 for _ in range(60):
  if subprocess.run(["docker","exec",name,"pg_isready","-U","postgres"],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL).returncode==0:break
  time.sleep(.5)
 else:raise RuntimeError("Postgres readiness timeout")
 port=run(["docker","port",name,"5432"],capture_output=True,text=True).stdout.strip().split(":")[-1]
 def dsn(selected_port=port,role=False):return f"postgres://postgres:{env['POSTGRES_PASSWORD']}@127.0.0.1:{selected_port}/postgres?sslmode=disable"+('&options=-c%20role%3Dkey_runtime' if role else '')
 env.update(HEIMDALL_TEST_KEYS_ADMIN=dsn(),HEIMDALL_TEST_KEYS_RUNTIME=dsn(role=True))
 subprocess.run(["go","test","-race","-count=1","./internal/postgres","-run","^TestKeyUsageInitialize$"],cwd=ROOT/'sdk/go',env=env,check=True)
 sql("CREATE ROLE key_runtime NOLOGIN; GRANT USAGE ON SCHEMA heimdall_keys TO key_runtime; GRANT SELECT ON ALL TABLES IN SCHEMA heimdall_keys TO key_runtime; GRANT UPDATE(used) ON heimdall_keys.record_key_usage TO key_runtime;")
 sql("INSERT INTO heimdall_keys.record_key_usage(fingerprint,encryption_limit) VALUES(decode(repeat('6',64),'hex'),2)")
 env.update(HEIMDALL_TEST_KEYS_ADMIN=dsn(),HEIMDALL_TEST_KEYS_RUNTIME=dsn(role=True))
 subprocess.run(["go","test","-race","-count=1","./internal/postgres","-run","^TestKeyUsageLive$"],cwd=ROOT/'sdk/go',env=env,check=True)
 run(["pnpm","--filter","@heimdall/sdk","exec","vitest","run","test/postgres-key-usage.test.ts"],env=env)
 assert sql("SELECT used FROM heimdall_keys.record_key_usage WHERE fingerprint=decode(repeat('6',64),'hex')")=="2"
 sql("INSERT INTO heimdall_keys.record_key_usage(fingerprint,encryption_limit) VALUES(decode(repeat('3',64),'hex'),10)")
 for language in ('go','ts'):
  with CommitLossProxy(int(port)) as proxy:
   fault=dict(env,HEIMDALL_TEST_KEYS_LOSS=dsn(proxy.port,True))
   if language=='go':subprocess.run(["go","test","-race","-count=1","./internal/postgres","-run","^TestKeyUsageLoss$"],cwd=ROOT/'sdk/go',env=fault,check=True)
   else:run(["pnpm","--filter","@heimdall/sdk","exec","vitest","run","test/postgres-key-usage.test.ts","-t","key reservation COMMIT loss"],env=fault)
   assert proxy.dropped.is_set() and not proxy.errors
 assert sql("SELECT used FROM heimdall_keys.record_key_usage WHERE fingerprint=decode(repeat('3',64),'hex')")=="2"
 for statement in ["UPDATE heimdall_keys.record_key_usage SET used=0", "UPDATE heimdall_keys.record_key_usage SET encryption_limit=100", "UPDATE heimdall_keys.record_key_usage SET state='active'", "DELETE FROM heimdall_keys.record_key_usage"]:
  result=subprocess.run(["docker","exec","-i",name,"psql","-U","postgres","-v","ON_ERROR_STOP=1"],input=statement,text=True,capture_output=True)
  assert result.returncode!=0
 print(json.dumps({'run':name,'result':'passed','checks':['TS and Go concurrent cap','idempotent registration preserves usage','irreversible retirement','runtime policy privilege refusal','both COMMIT losses consume usage without success','database guards reject reset deletion and policy change']}))
finally:
 if created:subprocess.run(["docker","rm","--force",name],check=True,stdout=subprocess.DEVNULL)

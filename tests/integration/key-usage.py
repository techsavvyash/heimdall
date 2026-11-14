"""Disposable shared-key ledger concurrency and ambiguous COMMIT checks."""
import hashlib
import tempfile
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
 with tempfile.TemporaryDirectory(prefix="heimdall-key-cli-") as directory:
  binary=Path(directory)/"heimdall-keys"
  subprocess.run(["go","build","-o",str(binary),"./cmd/heimdall-keys"],cwd=ROOT/'sdk/go',check=True)
  cli_env=dict(env,HEIMDALL_KEY_DATABASE_URL=dsn())
  def cli(args,selected_env=cli_env,expected=0):
   result=subprocess.run([str(binary),*args],cwd=ROOT,env=selected_env,capture_output=True,text=True)
   assert result.returncode==expected, (args[0],result.returncode,result.stderr)
   return result
  initial=["--initialize","--sql",str(ROOT/'contracts/persistence/keys/0001.sql')]
  cli(initial);cli(initial);cli(["--status"])
  key=Path(directory)/"key";key.write_bytes(bytes([17])*32);key.chmod(0o600)
  fingerprint=hashlib.sha256(b"heimdall.record-key-usage.v1\0"+bytes([17])*32).hexdigest()
  registration=["--register","--fresh-key","--key-file",str(key),"--limit","4"]
  cli(registration)
  sql("UPDATE heimdall_keys.record_key_usage SET used=2 WHERE fingerprint=decode('"+fingerprint+"','hex')")
  cli(registration)
  assert sql("SELECT used FROM heimdall_keys.record_key_usage WHERE fingerprint=decode('"+fingerprint+"','hex')")=="2"
  assert 'conflict' in cli([*registration[:-1],"5"],expected=1).stderr
  readonly=dict(cli_env,HEIMDALL_KEY_DATABASE_URL=dsn()+"&options=-c%20default_transaction_read_only%3Don")
  assert 'unavailable' in cli(registration,readonly,1).stderr
  with CommitLossProxy(int(port)) as proxy:
   fault=dict(cli_env,HEIMDALL_KEY_DATABASE_URL=dsn(proxy.port))
   assert 'outcome_unknown' in cli(["--retire","--fingerprint",fingerprint],fault,1).stderr
   assert proxy.dropped.is_set() and not proxy.errors
  second=Path(directory)/"second";second.write_bytes(bytes([18])*32);second.chmod(0o600)
  second_fingerprint=hashlib.sha256(b"heimdall.record-key-usage.v1\0"+bytes([18])*32).hexdigest()
  second_registration=["--register","--fresh-key","--key-file",str(second),"--limit","2"]
  with CommitLossProxy(int(port)) as proxy:
   fault=dict(cli_env,HEIMDALL_KEY_DATABASE_URL=dsn(proxy.port))
   assert 'outcome_unknown' in cli(second_registration,fault,1).stderr
   assert proxy.dropped.is_set() and not proxy.errors
  assert sql("SELECT state||':'||used FROM heimdall_keys.record_key_usage WHERE fingerprint=decode('"+second_fingerprint+"','hex')")=="active:0"
  cli(second_registration)
  cli(["--retire","--fingerprint",fingerprint])
  assert 'conflict' in cli(registration,expected=1).stderr
  assert sql("SELECT state||':'||used FROM heimdall_keys.record_key_usage WHERE fingerprint=decode('"+fingerprint+"','hex')")=="retired:2"
 subprocess.run(["go","test","-race","-count=1","./internal/postgres","-run","^TestKeyUsageInitialize$"],cwd=ROOT/'sdk/go',env=env,check=True)
 sql("CREATE ROLE key_runtime NOLOGIN; GRANT USAGE ON SCHEMA heimdall_keys TO key_runtime; GRANT SELECT ON ALL TABLES IN SCHEMA heimdall_keys TO key_runtime; GRANT UPDATE(used) ON heimdall_keys.record_key_usage TO key_runtime;")
 sql("INSERT INTO heimdall_keys.record_key_usage(fingerprint,encryption_limit) VALUES(decode(repeat('6',64),'hex'),2)")
 env.update(HEIMDALL_TEST_KEYS_ADMIN=dsn(),HEIMDALL_TEST_KEYS_RUNTIME=dsn(role=True))
 subprocess.run(["go","test","-race","-count=1","./internal/postgres","-run","^TestKeyUsageLive$"],cwd=ROOT/'sdk/go',env=env,check=True)
 run(["pnpm","--filter","@techsavvyash/heimdall-sdk","exec","vitest","run","test/postgres-key-usage.test.ts"],env=env)
 assert sql("SELECT used FROM heimdall_keys.record_key_usage WHERE fingerprint=decode(repeat('6',64),'hex')")=="2"
 sql((ROOT/'contracts/persistence/session/0001.sql').read_text())
 subprocess.run(["go","test","-race","-count=1","./internal/postgres","-run","^TestMeteredRecords$"],cwd=ROOT/'sdk/go',env=env,check=True)
 run(["pnpm","--filter","@techsavvyash/heimdall-sdk","exec","vitest","run","test/postgres-metered-records.test.ts"],env=env)
 for language in ('go','ts'):
  with CommitLossProxy(int(port)) as proxy:
   fault=dict(env,HEIMDALL_TEST_KEYS_LOSS=dsn(proxy.port,True))
   if language=='go':subprocess.run(["go","test","-race","-count=1","./internal/postgres","-run","^TestMeteredRecordsLoss$"],cwd=ROOT/'sdk/go',env=fault,check=True)
   else:run(["pnpm","--filter","@techsavvyash/heimdall-sdk","exec","vitest","run","test/postgres-metered-records.test.ts","-t","uncertain reservation"],env=fault)
   assert proxy.dropped.is_set() and not proxy.errors
 sql("INSERT INTO heimdall_keys.record_key_usage(fingerprint,encryption_limit) VALUES(decode(repeat('3',64),'hex'),10)")
 for language in ('go','ts'):
  with CommitLossProxy(int(port)) as proxy:
   fault=dict(env,HEIMDALL_TEST_KEYS_LOSS=dsn(proxy.port,True))
   if language=='go':subprocess.run(["go","test","-race","-count=1","./internal/postgres","-run","^TestKeyUsageLoss$"],cwd=ROOT/'sdk/go',env=fault,check=True)
   else:run(["pnpm","--filter","@techsavvyash/heimdall-sdk","exec","vitest","run","test/postgres-key-usage.test.ts","-t","key reservation COMMIT loss"],env=fault)
   assert proxy.dropped.is_set() and not proxy.errors
 assert sql("SELECT used FROM heimdall_keys.record_key_usage WHERE fingerprint=decode(repeat('3',64),'hex')")=="2"
 for statement in ["UPDATE heimdall_keys.record_key_usage SET used=0", "UPDATE heimdall_keys.record_key_usage SET encryption_limit=100", "UPDATE heimdall_keys.record_key_usage SET state='active'", "DELETE FROM heimdall_keys.record_key_usage"]:
  result=subprocess.run(["docker","exec","-i",name,"psql","-U","postgres","-v","ON_ERROR_STOP=1"],input=statement,text=True,capture_output=True)
  assert result.returncode!=0
 print(json.dumps({'run':name,'result':'passed','checks':['actual key CLI initialization status registration policy conflict read-only refusal and registration/retirement COMMIT loss','TS and Go concurrent cap','idempotent registration preserves usage','irreversible retirement','runtime policy privilege refusal','both COMMIT losses consume usage without success','all record write paths meter encryption including failed CAS and denied identity','retained decryption and rotation','uncertain reservation prevents encryption and persistence','database guards reject reset deletion and policy change']}))
finally:
 if created:subprocess.run(["docker","rm","--force","--volumes",name],check=True,stdout=subprocess.DEVNULL)

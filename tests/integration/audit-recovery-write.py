"""Prepared v2 atomic decision recovery on disposable PostgreSQL; no activation or old-writer fencing claim."""
import hashlib
import json
import os
from pathlib import Path
import secrets
import subprocess
import time
import uuid
from pg_commit_loss import CommitLossProxy

ROOT = Path(__file__).resolve().parents[2]
IMAGE = "postgres:17.11-alpine@sha256:3540c0362c809df084ffac0ae69fe794515f9de92692f02178bcdce0ef0302ac"
f = json.loads((ROOT / "contracts/fixtures/audit-backfill.json").read_text())
name = "heimdall-recovery-write-" + uuid.uuid4().hex
env = dict(os.environ, POSTGRES_PASSWORD=secrets.token_hex(24))
created = False
if not __debug__:
    raise RuntimeError("assertions must be enabled")

def run(args, **kwargs):
    return subprocess.run(args, cwd=ROOT, check=True, **kwargs)

def sql(statement):
    return run(["docker", "exec", "-i", name, "psql", "-U", "postgres", "-At", "-v", "ON_ERROR_STOP=1"], input=statement, capture_output=True, text=True).stdout.strip()

try:
    run(["docker", "run", "--detach", "--name", name, "-e", "POSTGRES_PASSWORD", "-p", "127.0.0.1::5432", IMAGE], env=env, stdout=subprocess.DEVNULL)
    created = True
    for _ in range(60):
        if subprocess.run(["docker", "exec", name, "pg_isready", "-h", "127.0.0.1", "-U", "postgres"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode == 0:
            break
        time.sleep(0.5)
    else:
        raise RuntimeError("PostgreSQL readiness timeout")
    port = run(["docker", "port", name, "5432"], capture_output=True, text=True).stdout.strip().split(":")[-1]
    env["HEIMDALL_TEST_RECOVERY_DATABASE_URL"] = f"postgres://postgres:{env['POSTGRES_PASSWORD']}@127.0.0.1:{port}/postgres?sslmode=disable"
    base = (ROOT / "contracts/persistence/session/0001.sql").read_text()
    sql(base)
    sql("INSERT INTO heimdall.schema_metadata VALUES(true,1,'migrating',1,1,1,1); INSERT INTO heimdall.migrations(version,checksum) VALUES(1,'" + hashlib.sha256(base.encode()).hexdigest() + "');")
    for item in f["prepared"]:
        source = (ROOT / "contracts/persistence" / item["path"]).read_text()
        assert hashlib.sha256(source.encode()).hexdigest() == item["checksum"]
        sql(source)
    run(["pnpm", "--filter", "@heimdall/sdk", "exec", "vitest", "run", "test/postgres-audit-recovery.test.ts"], env=env)
    subprocess.run(["go", "test", "-race", "-count=1", "./internal/postgres", "-run", "^TestAuditRecovery(Input)?$"], cwd=ROOT / "sdk/go", env=env, check=True)
    run(["pnpm", "--filter", "@heimdall/sdk", "exec", "vitest", "run", "test/postgres-audit-recovery.test.ts"], env=dict(env, HEIMDALL_TEST_RECOVERY_PEER="1"))
    for language in ("ts", "go"):
        with CommitLossProxy(int(port)) as proxy:
            fault = dict(env, HEIMDALL_TEST_RECOVERY_LOSS=f"postgres://postgres:{env['POSTGRES_PASSWORD']}@127.0.0.1:{proxy.port}/postgres?sslmode=disable")
            if language == "ts":
                run(["pnpm", "--filter", "@heimdall/sdk", "exec", "vitest", "run", "test/postgres-audit-recovery.test.ts", "-t", "recovery committed response loss"], env=fault)
            else:
                subprocess.run(["go", "test", "-race", "-count=1", "./internal/postgres", "-run", "^TestAuditRecoveryLoss$"], cwd=ROOT / "sdk/go", env=fault, check=True)
            assert proxy.dropped.is_set() and not proxy.errors
    assert sql("SELECT state FROM heimdall.schema_metadata") == "migrating"
    assert sql("SELECT count(*) FROM heimdall.migrations") == "1"
    print(json.dumps({"run": name, "postgres": IMAGE, "result": "passed", "checks": ["scoped deterministic administration ID", "pair validation and cutoff eligibility", "atomic unknown outcome administration and slot", "both deterministic race orders and concurrent normal completion", "eight exact recovery retries", "eight distinct recovery contenders leave one pair", "administration outbox failure rolls back all recovery writes", "missing administration is not fabricated", "reciprocal TS/Go recovery", "audited runners suppress recovery-losing allow", "both SDKs committed response loss and pair retry"]}))
finally:
    if created:
        subprocess.run(["docker", "rm", "--force", name], check=True, stdout=subprocess.DEVNULL)

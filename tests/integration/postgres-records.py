"""Disposable local PostgreSQL proof; requires Docker, pnpm and Go on PATH."""
import json
import os
from pathlib import Path
import secrets
import subprocess
import time
import uuid

ROOT = Path(__file__).resolve().parents[2]
IMAGE = "postgres:17.11-alpine@sha256:3540c0362c809df084ffac0ae69fe794515f9de92692f02178bcdce0ef0302ac"
name = "heimdall-records-" + uuid.uuid4().hex
password = secrets.token_hex(24)
env = dict(os.environ, POSTGRES_PASSWORD=password)

def run(args, **kwargs):
    return subprocess.run(args, cwd=ROOT, check=True, **kwargs)

try:
    run(["docker", "run", "--detach", "--name", name, "-e", "POSTGRES_PASSWORD", "-p", "127.0.0.1::5432", IMAGE], env=env, stdout=subprocess.DEVNULL)
    for _ in range(60):
        ready = subprocess.run(["docker", "exec", name, "pg_isready", "-U", "postgres"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        if ready.returncode == 0:
            break
        time.sleep(0.5)
    else:
        raise RuntimeError("PostgreSQL readiness timeout")
    port = run(["docker", "port", name, "5432"], capture_output=True, text=True).stdout.strip().split(":")[-1]
    run(["docker", "exec", "-i", name, "psql", "-U", "postgres", "-v", "ON_ERROR_STOP=1"], input=(ROOT / "contracts/persistence/session/0001.sql").read_bytes(), stdout=subprocess.DEVNULL)
    env["HEIMDALL_TEST_DATABASE_URL"] = f"postgres://postgres:{password}@127.0.0.1:{port}/postgres?sslmode=disable"
    for peer in (False, True):
        if peer:
            env["HEIMDALL_TEST_READ_PEER"] = "1"
        run(["pnpm", "--filter", "@heimdall/sdk", "exec", "vitest", "run", "test/postgres-records.test.ts", "test/postgres-readiness.test.ts"], env=env)
        subprocess.run(["go", "test", "-race", "-count=1", "./internal/postgres"], cwd=ROOT / "sdk/go", env=env, check=True)
    print(json.dumps({"run": name, "postgres": IMAGE, "result": "passed", "checks": ["TS and Go CRUD", "8-way CAS single winner", "expiry and product isolation", "delete/recreate revision", "tampered expiry denied", "cancelled blocked write unknown", "pool recovery", "TS reads Go", "Go reads TS", "11 readiness states in both languages", "read-only connection rejected", "missing metadata table rejected", "migration transaction visibility"]}))
finally:
    subprocess.run(["docker", "rm", "--force", name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)

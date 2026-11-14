"""Qualify the PostgreSQL timeout premise on a disposable PostgreSQL 17 node."""

import json
import os
import secrets
import subprocess
import time
from uuid import uuid4

IMAGE = "postgres:17.11-alpine@sha256:3540c0362c809df084ffac0ae69fe794515f9de92692f02178bcdce0ef0302ac"
name = "heimdall-coverage-" + uuid4().hex


def docker(*args, **kwargs):
    return subprocess.run(["docker", *args], capture_output=True, text=True, **kwargs)


def sql(sql_text, role="postgres"):
    return docker("exec", name, "psql", "-U", role, "-d", "postgres", "-At", "-v", "ON_ERROR_STOP=1",
                  "-c", sql_text, timeout=10)


try:
    docker("run", "-d", "--name", name, "-e", "POSTGRES_PASSWORD", "--tmpfs",
           "/var/lib/postgresql/data:rw,size=128m", IMAGE,
           env=dict(os.environ, POSTGRES_PASSWORD=secrets.token_hex(24)), check=True)
    deadline = time.monotonic() + 30
    while docker("exec", name, "pg_isready", "-h", "127.0.0.1").returncode:
        assert time.monotonic() < deadline, "PostgreSQL never became ready"
        time.sleep(.3)

    assert sql("""
        CREATE ROLE coverage_writer LOGIN;
        ALTER ROLE coverage_writer IN DATABASE postgres SET transaction_timeout='1000ms';
        CREATE TABLE coverage_events (created_at timestamptz DEFAULT statement_timestamp());
        GRANT INSERT ON coverage_events TO coverage_writer;
    """).returncode == 0
    assert sql("SHOW transaction_timeout", "coverage_writer").stdout.strip() == "1s"
    assert sql("SHOW max_prepared_transactions").stdout.strip() == "0"
    assert sql("INSERT INTO coverage_events DEFAULT VALUES", "coverage_writer").returncode == 0
    expired = sql("BEGIN; INSERT INTO coverage_events DEFAULT VALUES; SELECT pg_sleep(4); COMMIT", "coverage_writer")
    assert expired.returncode != 0 and "transaction timeout" in expired.stderr.lower()
    assert sql("SELECT count(*) FROM coverage_events").stdout.strip() == "1"
    print(json.dumps({"run": name, "postgres": IMAGE, "writerTimeoutMs": 1000,
                      "statementCompleted": True, "overlongTransactionTerminated": True,
                      "overlongInsertRolledBack": True, "preparedTransactionsDisabled": True}))
finally:
    docker("rm", "-f", name)

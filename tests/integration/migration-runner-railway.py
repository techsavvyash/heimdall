"""Run the compiled migration tool in a disposable Railway application database."""
import concurrent.futures
import hashlib
import io
import json
import os
from pathlib import Path
import shlex
import subprocess
import sys
import tarfile
import tempfile
import uuid

if sys.argv[1:] != ["--execute-verification-migration"]:
    raise SystemExit("require --execute-verification-migration")
ROOT = Path(__file__).resolve().parents[2]
RUN = uuid.uuid4().hex
DATABASE = "heimdall_migration_" + RUN
REMOTE = "/tmp/heimdall-migration-" + RUN
PROJECT = "944fc9be-8648-4237-82bc-72e4bd24a4c9"
ENVIRONMENT = "7bc7c286-b2dc-4415-85eb-90dd554ab2fb"
SERVICE = "648133ac-305b-495d-a77e-4d0c49103feb"
KEY = str(Path.home() / ".ssh/heimdall-verification")

def remote(command, payload=None, success=True):
    result = subprocess.run(["railway", "ssh", "--project", PROJECT,
        "--environment", ENVIRONMENT, "--service", SERVICE, "--identity-file", KEY,
        "--", command], input=payload, capture_output=True, timeout=90)
    if success and result.returncode != 0:
        raise RuntimeError("verification command failed: " + result.stderr.decode()[-1000:])
    return result

def sql(statement, database=DATABASE):
    command = "psql -X -U heimdall_probe -d " + shlex.quote(database) + " -Atq -v ON_ERROR_STOP=1 -c " + shlex.quote(statement)
    return remote(command).stdout.decode().strip()

created = uploaded = False
try:
    architecture = remote("uname -m").stdout.decode().strip()
    if architecture != "x86_64":
        raise RuntimeError("verification service architecture changed")
    with tempfile.TemporaryDirectory(prefix="heimdall-migration-") as directory:
        binary = Path(directory) / "heimdall-migrate"
        subprocess.run(["go", "build", "-trimpath", "-o", str(binary), "./cmd/heimdall-migrate"],
            cwd=ROOT / "sdk/go", env=dict(os.environ, CGO_ENABLED="0", GOOS="linux", GOARCH="amd64"), check=True)
        binary_digest = hashlib.sha256(binary.read_bytes()).hexdigest()
        migration = ROOT / "contracts/persistence/session/0001.sql"
        archive = io.BytesIO()
        with tarfile.open(fileobj=archive, mode="w:gz") as tar:
            tar.add(binary, arcname="heimdall-migrate")
            tar.add(migration, arcname="0001.sql")
        remote("mkdir -m 700 " + REMOTE)
        uploaded = True
        remote("tar -xz -C " + REMOTE, archive.getvalue())
    actual_digest = remote("sha256sum " + REMOTE + "/heimdall-migrate").stdout.decode().split()[0]
    if actual_digest != binary_digest:
        raise RuntimeError("uploaded binary checksum mismatch")
    sql("CREATE DATABASE " + DATABASE, "postgres")
    created = True
    # Unix socket access uses the verification container's existing local trust
    # configuration. No production credential is copied into an artifact.
    dsn = "host=/var/run/postgresql user=heimdall_probe dbname=" + DATABASE + " sslmode=disable"
    command = "HEIMDALL_MIGRATION_DATABASE_URL=" + shlex.quote(dsn) + " " + REMOTE + "/heimdall-migrate -initialize-session-schema -sql " + REMOTE + "/0001.sql"
    with concurrent.futures.ThreadPoolExecutor(max_workers=2) as executor:
        futures = [executor.submit(remote, command) for _ in range(2)]
        results = [future.result().stdout.decode().strip() for future in futures]
    if results != ["session schema v1 ready"] * 2:
        raise RuntimeError("unexpected runner receipt")
    checksum = json.loads((ROOT / "contracts/persistence/catalog.json").read_text())["session"][0]["checksum"]
    if sql("SELECT version::text||':'||checksum FROM heimdall.migrations") != "1:" + checksum:
        raise RuntimeError("ledger mismatch")
    if sql("SELECT database_version::text||':'||state FROM heimdall.schema_metadata") != "1:ready":
        raise RuntimeError("metadata mismatch")
    if sql("SELECT count(*) FROM information_schema.tables WHERE table_schema='heimdall'") != "6":
        raise RuntimeError("schema object count mismatch")
    sql("UPDATE heimdall.schema_metadata SET state='migrating'")
    rejected = remote(command, success=False)
    if rejected.returncode == 0 or "unavailable" not in rejected.stderr.decode():
        raise RuntimeError("incompatible state was not rejected")
    if sql("SELECT state FROM heimdall.schema_metadata") != "migrating":
        raise RuntimeError("runner repaired incompatible state")
    sql("UPDATE heimdall.schema_metadata SET state='ready'")
    remote(command)
    proof = {"runId": RUN, "projectId": PROJECT, "environmentId": ENVIRONMENT,
        "serviceId": SERVICE, "binarySha256": binary_digest, "migrationSha256": checksum,
        "checks": ["uploaded compiled Linux binary checksum", "two independent runner invocations", "exact ledger and ready metadata", "six shared schema tables", "incompatible state rejected without repair", "idempotent rerun"],
        "result": "passed"}
finally:
    try:
        if created:
            sql("DROP DATABASE " + DATABASE + " WITH (FORCE)", "postgres")
    finally:
        if uploaded:
            remote("rm -rf " + REMOTE)
if sql("SELECT count(*) FROM pg_database WHERE datname='" + DATABASE + "'", "postgres") != "0":
    raise RuntimeError("disposable database was not removed")
remote("test ! -e " + REMOTE)
proof["cleanupVerified"] = True
print(json.dumps(proof))

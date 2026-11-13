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

created = uploaded = role_created = False
ROLE = "heimdall_status_"+RUN
try:
    architecture = remote("uname -m").stdout.decode().strip()
    if architecture != "x86_64":
        raise RuntimeError("verification service architecture changed")
    postgres_version = sql("SHOW server_version", "postgres")
    if postgres_version != "17.11":
        raise RuntimeError("verification PostgreSQL version changed")
    with tempfile.TemporaryDirectory(prefix="heimdall-migration-") as directory:
        binary = Path(directory) / "heimdall-migrate"
        subprocess.run(["go", "build", "-trimpath", "-o", str(binary), "./cmd/heimdall-migrate"],
            cwd=ROOT / "sdk/go", env=dict(os.environ, CGO_ENABLED="0", GOOS="linux", GOARCH="amd64"), check=True)
        binary_digest = hashlib.sha256(binary.read_bytes()).hexdigest()
        migration = ROOT / "contracts/persistence/session/0001.sql"
        archive = io.BytesIO()
        with tarfile.open(fileobj=archive, mode="w:gz") as tar:
            tar.add(binary, arcname="heimdall-migrate")
            tar.add(migration.parent, arcname="session")
            tar.add(ROOT/"contracts/persistence/managed/0001.sql",arcname="managed-0001.sql")
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
    command = "HEIMDALL_MIGRATION_DATABASE_URL=" + shlex.quote(dsn) + " " + REMOTE + "/heimdall-migrate -initialize-session-schema -sql " + REMOTE + "/session/0001.sql"
    prefix=command.split(" -initialize-session-schema")[0]
    managed_command=prefix+" -initialize-managed-schema -sql "+REMOTE+"/managed-0001.sql"
    missing=remote(managed_command,success=False)
    if missing.returncode==0 or "unavailable" not in missing.stderr.decode():raise RuntimeError("managed prerequisite refusal failed")
    if sql("SELECT count(*) FROM pg_namespace WHERE nspname IN ('heimdall','heimdall_managed')")!="0":raise RuntimeError("implicit schema installation")
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
    with concurrent.futures.ThreadPoolExecutor(max_workers=2) as executor:
        results=list(executor.map(lambda _:remote(managed_command).stdout.decode().strip(),range(2)))
    if results!=["managed schema v1 ready"]*2:raise RuntimeError("managed runner receipt")
    managed_checksum=json.loads((ROOT/"contracts/persistence/catalog.json").read_text())["managed"][0]["checksum"]
    if sql("SELECT version::text||':'||checksum FROM heimdall_managed.migrations")!="1:"+managed_checksum:raise RuntimeError("managed ledger")
    if sql("SELECT database_version::text||':'||state FROM heimdall_managed.schema_metadata")!="1:ready":raise RuntimeError("managed metadata")
    for family in ("session","managed"):
        if remote(prefix+" -status "+family).stdout.decode().strip()!=family+" schema v1 ready":raise RuntimeError("status receipt")
    sql("CREATE ROLE "+ROLE+" NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT","postgres");role_created=True
    sql("GRANT USAGE ON SCHEMA heimdall,heimdall_managed TO "+ROLE+"; GRANT SELECT ON heimdall.schema_metadata,heimdall.migrations,heimdall_managed.schema_metadata,heimdall_managed.migrations TO "+ROLE)
    status_dsn=dsn+" options='-c role="+ROLE+"'"
    remote("HEIMDALL_MIGRATION_DATABASE_URL="+shlex.quote(status_dsn)+" "+REMOTE+"/heimdall-migrate -status managed")
    for schema in ("heimdall","heimdall_managed"):
        sql("UPDATE "+schema+".schema_metadata SET state='migrating'")
        for check in (managed_command,prefix+" -status managed"):
            rejected=remote(check,success=False)
            if rejected.returncode==0 or "unavailable" not in rejected.stderr.decode():raise RuntimeError("managed prerequisite compatibility failure")
        if sql("SELECT state FROM "+schema+".schema_metadata")!="migrating":raise RuntimeError("managed runner repaired metadata")
        sql("UPDATE "+schema+".schema_metadata SET state='ready'")
    remote(managed_command)
    def migrate(flags, success=True):
        return remote(prefix + " " + flags, success=success)

    def backfill(family, field, terminal):
        for _ in range(10):
            result = migrate(f"-backfill-{family}-upgrade -writers-drained -batch-size 2")
            if json.loads(result.stdout)[field] == terminal:
                return
        raise RuntimeError(f"{family} fixture backfill did not converge")

    def expect_refused(result):
        if result.returncode != 1 or result.stderr.decode().strip() != "unavailable":
            raise RuntimeError("incompatible or unauthorized migration command was admitted")

    prepare = " -writers-drained -sql-directory " + REMOTE + "/session"
    for family, version, limits, field, terminal in (
        ("audit", 2, "", "status", "coverage_complete"),
        ("capacity", 3, " -capacity-events 1000 -capacity-bytes 200000000 -capacity-deliveries 32000", "phase", "complete"),
        ("retention", 4, " -retention-rows 10000 -retention-bytes 81920000", "phase", "complete"),
        ("hold-release", 5, "", None, None),
    ):
        migrate(f"-prepare-{family}-upgrade" + prepare + limits)
        for status_version in (version - 1, version):
            status = "managed" if status_version == 1 else f"managed-v{status_version}"
            expect_refused(migrate("-status " + status, success=False))
        if field is not None:
            backfill(family, field, terminal)
        migrate(f"-activate-{family}-upgrade -writers-drained")
        receipt = migrate(f"-status managed-v{version}").stdout.decode().strip()
        if receipt != f"audit schema v{version} and managed schema v1 ready":
            raise RuntimeError("unexpected managed upgrade status")
    for schema in ("heimdall_capacity", "heimdall_retention"):
        sql(f"GRANT USAGE ON SCHEMA {schema} TO {ROLE}; GRANT SELECT ON ALL TABLES IN SCHEMA {schema} TO {ROLE}")
    restricted = "HEIMDALL_MIGRATION_DATABASE_URL=" + shlex.quote(status_dsn) + " " + REMOTE + "/heimdall-migrate"
    remote(restricted + " -status managed-v5")
    expect_refused(remote(restricted + " -activate-hold-release-upgrade -writers-drained", success=False))
    expect_refused(migrate("-status managed-v4", success=False))
    migrate("-activate-hold-release-upgrade -writers-drained")
    proof = {"runId": RUN, "projectId": PROJECT, "environmentId": ENVIRONMENT,
        "serviceId": SERVICE, "postgresVersion": postgres_version, "binarySha256": binary_digest, "migrationSha256": checksum, "managedMigrationSha256":managed_checksum,
        "checks": ["uploaded compiled Linux binary checksum", "two independent runner invocations", "exact ledger and ready metadata", "six shared schema tables", "incompatible state rejected without repair", "idempotent rerun", "managed initialization refuses missing prerequisite without implicit installation", "two managed installers serialize", "exact managed ledger and metadata", "session and managed read-only status", "managed status with SELECT-only role", "either incompatible family blocks managed initialization/status without repair"],
        "result": "passed"}
    proof["checks"].extend(["ordered audit v1 through v5 migration and managed readiness", "old and new managed status refuse prepared schemas", "v5 SELECT-only status and denied activation", "v4 refusal and idempotent v5 activation"])
finally:
    try:
        if created:
            sql("DROP DATABASE " + DATABASE + " WITH (FORCE)", "postgres")
    finally:
        if role_created:
            sql("DROP ROLE "+ROLE,"postgres")
        if uploaded:
            remote("rm -rf " + REMOTE)
if sql("SELECT count(*) FROM pg_database WHERE datname='" + DATABASE + "'", "postgres") != "0":
    raise RuntimeError("disposable database was not removed")
remote("test ! -e " + REMOTE)
if sql("SELECT count(*) FROM pg_roles WHERE rolname='" + ROLE + "'", "postgres") != "0":
    raise RuntimeError("disposable role was not removed")
proof["cleanupVerified"] = True
print(json.dumps(proof))

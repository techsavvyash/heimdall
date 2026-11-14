"""Disposable local PostgreSQL proof; requires Docker, pnpm and Go on PATH."""
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
name = "heimdall-records-" + uuid.uuid4().hex
password = secrets.token_hex(24)
env = dict(os.environ, POSTGRES_PASSWORD=password)

def run(args, **kwargs):
    return subprocess.run(args, cwd=ROOT, check=True, **kwargs)

try:
    run(["docker", "run", "--detach", "--name", name, "-e", "POSTGRES_PASSWORD", "--tmpfs", "/var/lib/postgresql/data:rw,size=256m", "-p", "127.0.0.1::5432", IMAGE], env=env, stdout=subprocess.DEVNULL)
    for _ in range(60):
        ready = subprocess.run(["docker", "exec", name, "pg_isready", "-h", "127.0.0.1", "-U", "postgres"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        if ready.returncode == 0:
            break
        time.sleep(0.5)
    else:
        raise RuntimeError("PostgreSQL readiness timeout")
    port = run(["docker", "port", name, "5432"], capture_output=True, text=True).stdout.strip().split(":")[-1]
    env["HEIMDALL_TEST_DATABASE_URL"] = f"postgres://postgres:{password}@127.0.0.1:{port}/postgres?sslmode=disable"
    env["HEIMDALL_MIGRATION_DATABASE_URL"] = f"postgres://migration_owner:{password}@127.0.0.1:{port}/postgres?sslmode=disable"
    def admin(sql):
        return run(["docker", "exec", "-i", name, "psql", "-U", "postgres", "-At", "-v", "ON_ERROR_STOP=1"], input=sql, capture_output=True, text=True).stdout.strip()
    admin(f"CREATE ROLE migration_owner LOGIN PASSWORD '{password}'; GRANT CREATE ON DATABASE postgres TO migration_owner; REVOKE USAGE ON LANGUAGE plpgsql FROM PUBLIC;")
    command = ["go", "run", "./cmd/heimdall-migrate", "-initialize-session-schema", "-sql", str(ROOT / "contracts/persistence/session/0001.sql")]
    failed = subprocess.run(command, cwd=ROOT / "sdk/go", env=env, capture_output=True, text=True)
    if failed.returncode == 0 or "unavailable" not in failed.stderr:
        raise RuntimeError("expected mid-migration permission failure")
    if admin("SELECT count(*) FROM pg_namespace WHERE nspname='heimdall'") != "0":
        raise RuntimeError("failed migration left partial schema")
    admin("GRANT USAGE ON LANGUAGE plpgsql TO migration_owner")
    # Independent processes contend on the same deployment advisory lock.
    runners = [subprocess.Popen(command, cwd=ROOT / "sdk/go", env=env) for _ in range(2)]
    statuses = [runner.wait() for runner in runners]
    if statuses != [0, 0]:
        raise RuntimeError("concurrent migration runners failed")
    admin("CREATE DATABASE lost_commit OWNER migration_owner")
    with CommitLossProxy(int(port)) as proxy:
        fault_env = dict(env, HEIMDALL_MIGRATION_DATABASE_URL=f"postgres://migration_owner:{password}@127.0.0.1:{proxy.port}/lost_commit?sslmode=disable")
        lost = subprocess.run(command, cwd=ROOT / "sdk/go", env=fault_env, capture_output=True, text=True, timeout=40)
        if lost.returncode == 0 or "outcome_unknown" not in lost.stderr or not proxy.dropped.is_set() or proxy.errors:
            raise RuntimeError("lost COMMIT response classification failed")
    recovered_env = dict(env, HEIMDALL_MIGRATION_DATABASE_URL=f"postgres://migration_owner:{password}@127.0.0.1:{port}/lost_commit?sslmode=disable")
    # Inspect persisted state before rerun, proving this was a committed response
    # loss rather than a pre-COMMIT disconnect followed by fresh initialization.
    persisted = run(["docker", "exec", name, "psql", "-U", "postgres", "-d", "lost_commit", "-At", "-c", "SELECT state FROM heimdall.schema_metadata"], capture_output=True, text=True).stdout.strip()
    if persisted != "ready":
        raise RuntimeError("lost response did not leave committed ready state")
    subprocess.run(command, cwd=ROOT / "sdk/go", env=recovered_env, check=True)
    admin("CREATE ROLE heimdall_audit_runtime NOLOGIN; GRANT USAGE ON SCHEMA heimdall TO heimdall_audit_runtime; GRANT SELECT ON heimdall.audit_events,heimdall.audit_deliveries TO heimdall_audit_runtime; GRANT INSERT ON heimdall.audit_events,heimdall.audit_deliveries TO heimdall_audit_runtime;")
    admin("CREATE ROLE heimdall_audit_delivery NOLOGIN; GRANT USAGE ON SCHEMA heimdall TO heimdall_audit_delivery; GRANT SELECT,UPDATE ON heimdall.audit_deliveries TO heimdall_audit_delivery;")
    admin("CREATE ROLE heimdall_identity_runtime NOLOGIN; GRANT USAGE ON SCHEMA heimdall TO heimdall_identity_runtime; GRANT SELECT,INSERT,UPDATE,DELETE ON heimdall.records,heimdall.identity_denials TO heimdall_identity_runtime; GRANT USAGE ON SEQUENCE heimdall.record_revision_seq TO heimdall_identity_runtime;")
    admin((ROOT / "contracts/persistence/keys/0001.sql").read_text())
    key_checksum = json.loads((ROOT / "contracts/persistence/catalog.json").read_text())["keys"][0]["checksum"]
    fingerprint = hashlib.sha256(b"heimdall.record-key-usage.v1\0" + bytes([7])*32).hexdigest()
    admin("INSERT INTO heimdall_keys.migrations(version,checksum) VALUES(1,'"+key_checksum+"'); INSERT INTO heimdall_keys.schema_metadata(database_version,state,reader_min,reader_max,writer_min,writer_max) VALUES(1,'ready',1,1,1,1); INSERT INTO heimdall_keys.record_key_usage(fingerprint,encryption_limit) VALUES(decode('"+fingerprint+"','hex'),100000); GRANT USAGE ON SCHEMA heimdall_keys TO heimdall_identity_runtime; GRANT SELECT ON ALL TABLES IN SCHEMA heimdall_keys TO heimdall_identity_runtime; GRANT UPDATE(used) ON heimdall_keys.record_key_usage TO heimdall_identity_runtime;")
    # Public factories require activated v4. Keep legacy v1 persistence suites
    # on their original database so neither protocol is silently relabelled.
    admin("CREATE DATABASE public_runtime OWNER migration_owner")
    runtime_dsn = f"postgres://postgres:{password}@127.0.0.1:{port}/public_runtime?sslmode=disable"
    runtime_env = dict(env, HEIMDALL_TEST_DATABASE_URL=runtime_dsn, HEIMDALL_MIGRATION_DATABASE_URL=f"postgres://migration_owner:{password}@127.0.0.1:{port}/public_runtime?sslmode=disable")
    subprocess.run(command, cwd=ROOT / "sdk/go", env=runtime_env, check=True)
    for args in (["--prepare-audit-upgrade","--writers-drained","--sql-directory",str(ROOT / "contracts/persistence/session")],["--activate-audit-upgrade","--writers-drained"]):
        subprocess.run(["go","run","./cmd/heimdall-migrate",*args],cwd=ROOT / "sdk/go",env=runtime_env,check=True)
    for args in (["--prepare-capacity-upgrade","--writers-drained","--sql-directory",str(ROOT / "contracts/persistence/session"),"--capacity-events","10000","--capacity-bytes","15000000000","--capacity-deliveries","320000"],["--backfill-capacity-upgrade","--writers-drained"],["--backfill-capacity-upgrade","--writers-drained"],["--activate-capacity-upgrade","--writers-drained"]):
        subprocess.run(["go","run","./cmd/heimdall-migrate",*args],cwd=ROOT / "sdk/go",env=runtime_env,check=True)
    for args in (["--prepare-retention-upgrade","--writers-drained","--sql-directory",str(ROOT/"contracts/persistence/session"),"--retention-rows","10000","--retention-bytes","81920000"],["--backfill-retention-upgrade","--writers-drained"],["--backfill-retention-upgrade","--writers-drained"],["--activate-retention-upgrade","--writers-drained"]):
        subprocess.run(["go","run","./cmd/heimdall-migrate",*args],cwd=ROOT/"sdk/go",env=runtime_env,check=True)
    run(["docker","exec","-i",name,"psql","-U","postgres","-d","public_runtime","-v","ON_ERROR_STOP=1"],input="CREATE ROLE heimdall_public_runtime NOLOGIN; GRANT USAGE ON SCHEMA heimdall TO heimdall_public_runtime; GRANT SELECT ON heimdall.schema_metadata,heimdall.migrations,heimdall.identity_denials,heimdall.records,heimdall.audit_events,heimdall.audit_deliveries TO heimdall_public_runtime; GRANT INSERT,UPDATE ON heimdall.identity_denials TO heimdall_public_runtime; GRANT DELETE ON heimdall.records TO heimdall_public_runtime; GRANT INSERT ON heimdall.audit_events,heimdall.audit_deliveries TO heimdall_public_runtime; GRANT SELECT,INSERT,UPDATE ON heimdall.audit_decisions TO heimdall_public_runtime; GRANT USAGE ON SCHEMA heimdall_capacity TO heimdall_public_runtime; GRANT SELECT ON heimdall_capacity.policy,heimdall_capacity.upgrade,heimdall_capacity.backfill TO heimdall_public_runtime; GRANT SELECT,INSERT ON heimdall_capacity.event_charges TO heimdall_public_runtime; GRANT SELECT,INSERT,UPDATE ON heimdall_capacity.reservations TO heimdall_public_runtime; GRANT UPDATE(used_events,used_bytes,used_deliveries,reserved_events,reserved_bytes,reserved_deliveries) ON heimdall_capacity.policy TO heimdall_public_runtime; GRANT USAGE ON SCHEMA heimdall_retention TO heimdall_public_runtime; GRANT SELECT ON ALL TABLES IN SCHEMA heimdall_retention TO heimdall_public_runtime; GRANT INSERT,UPDATE ON heimdall_retention.event_identities,heimdall_retention.decision_identities TO heimdall_public_runtime; GRANT UPDATE(used_rows,reserved_rows) ON heimdall_retention.capacity TO heimdall_public_runtime;",capture_output=True,text=True)
    run(["docker", "exec", "-i", name, "psql", "-U", "postgres", "-d", "public_runtime", "-v", "ON_ERROR_STOP=1"],
        input=(ROOT / "contracts/persistence/keys/0001.sql").read_text() + f"""
        INSERT INTO heimdall_keys.migrations(version,checksum) VALUES(1,'{key_checksum}');
        INSERT INTO heimdall_keys.schema_metadata(database_version,state,reader_min,reader_max,writer_min,writer_max)
        VALUES(1,'ready',1,1,1,1);
        INSERT INTO heimdall_keys.record_key_usage(fingerprint,encryption_limit) VALUES(decode('{fingerprint}','hex'),1000);
        GRANT USAGE ON SCHEMA heimdall_keys TO heimdall_public_runtime;
        GRANT SELECT ON ALL TABLES IN SCHEMA heimdall_keys TO heimdall_public_runtime;
        GRANT UPDATE(used) ON heimdall_keys.record_key_usage TO heimdall_public_runtime;
        GRANT INSERT,UPDATE ON heimdall.records TO heimdall_public_runtime;
        GRANT USAGE ON SEQUENCE heimdall.record_revision_seq TO heimdall_public_runtime;
        """, capture_output=True, text=True)
    subprocess.run(["go", "run", "./cmd/heimdall-migrate", "--initialize-managed-schema",
                    "--sql", str(ROOT / "contracts/persistence/managed/0001.sql")],
                   cwd=ROOT / "sdk/go", env=runtime_env, check=True)
    run(["docker", "exec", "-i", name, "psql", "-U", "postgres", "-d", "public_runtime", "-v", "ON_ERROR_STOP=1"],
        input="GRANT USAGE ON SCHEMA heimdall_managed TO heimdall_public_runtime; GRANT SELECT ON ALL TABLES IN SCHEMA heimdall_managed TO heimdall_public_runtime; GRANT INSERT,UPDATE ON heimdall_managed.workspaces,heimdall_managed.operations,heimdall_managed.membership_candidates TO heimdall_public_runtime; GRANT DELETE ON heimdall_managed.membership_candidates TO heimdall_public_runtime; GRANT USAGE ON ALL SEQUENCES IN SCHEMA heimdall_managed TO heimdall_public_runtime;",
        capture_output=True, text=True)
    run(["docker", "exec", "-i", name, "psql", "-U", "postgres", "-d", "public_runtime",
         "-v", "ON_ERROR_STOP=1", "--single-transaction", "-f", "-"],
        input=(ROOT / "contracts/persistence/directory/0001.sql").read_text() +
        (ROOT / "contracts/persistence/directory/0002.sql").read_text() +
        (ROOT / "contracts/persistence/directory/0003.sql").read_text() +
        (ROOT / "contracts/persistence/directory/0004.sql").read_text() +
        (ROOT / "contracts/persistence/directory/0005.sql").read_text() +
        (ROOT / "contracts/persistence/directory/0006.sql").read_text() +
        (ROOT / "contracts/persistence/directory/0007.sql").read_text() +
        (ROOT / "contracts/persistence/directory/0008.sql").read_text() +
        (ROOT / "contracts/persistence/directory/0009.sql").read_text() +
        "GRANT SELECT,INSERT,UPDATE ON heimdall_directory.event_checkpoints TO heimdall_public_runtime; "
        "GRANT DELETE ON heimdall.identity_denials TO heimdall_public_runtime; "
        "GRANT SELECT,INSERT,UPDATE ON heimdall_directory.enrollments TO heimdall_public_runtime; "
        "GRANT SELECT,INSERT,UPDATE ON heimdall_directory.polling TO heimdall_public_runtime; "
        "GRANT SELECT,INSERT ON heimdall_directory.subjects TO heimdall_public_runtime; "
        "GRANT USAGE ON SCHEMA heimdall_directory TO heimdall_public_runtime; "
        "GRANT USAGE ON SEQUENCE heimdall_directory.cleanup_revision TO heimdall_public_runtime; "
        "GRANT SELECT,INSERT,UPDATE ON heimdall_directory.deactivations TO heimdall_public_runtime; "
        "CREATE FUNCTION public.reject_directory_delete() RETURNS trigger LANGUAGE plpgsql AS $$ "
        "BEGIN IF OLD.environment LIKE 'directory-rollback-%' THEN RAISE EXCEPTION 'injected session deletion failure'; END IF; RETURN OLD; END; $$; "
        "CREATE TRIGGER reject_directory_delete BEFORE DELETE ON heimdall.records FOR EACH ROW EXECUTE FUNCTION public.reject_directory_delete();",
        capture_output=True, text=True)
    operator = os.environ.get("HEIMDALL_PROBE_OPERATOR_QUICKSTART") == "1"
    existing = os.environ.get("HEIMDALL_PROBE_EXISTING_WORKSPACES") == "1"
    if operator and (os.environ.get("HEIMDALL_PROBE_REFERENCE_PRODUCTS") != "1" or
                     os.environ.get("HEIMDALL_PROBE_BUSINESS_WRITE") != "1" or
                     not os.environ.get("HEIMDALL_PROBE_CONSUMER_REVISION") or
                     os.environ.get("HEIMDALL_PROBE_COORDINATED_RESTORE") == "1"):
        raise RuntimeError("Operator rehearsal requires reference products, business writes and a consumer revision")
    if existing and (operator or os.environ.get("HEIMDALL_PROBE_REFERENCE_PRODUCTS") != "1"):
        raise RuntimeError("Existing-workspace verification needs the standalone reference probe")
    if operator or existing:
        admin("CREATE DATABASE reference_application TEMPLATE public_runtime OWNER migration_owner;")
        run(["docker", "exec", name, "psql", "-U", "postgres", "-d", "reference_application",
             "-v", "ON_ERROR_STOP=1", "-c",
             "DROP TRIGGER reject_directory_delete ON heimdall.records; DROP FUNCTION public.reject_directory_delete();"],
            capture_output=True, text=True)
    run(["pnpm", "--filter", "@heimdall/sdk", "exec", "vitest", "run", "test/runtime.test.ts"], env=runtime_env)
    run(["docker", "exec", name, "psql", "-U", "postgres", "-d", "public_runtime",
         "-v", "ON_ERROR_STOP=1", "-c",
         """GRANT UPDATE(state,attempts,next_attempt_at,delivered_at) ON heimdall.audit_deliveries TO heimdall_public_runtime;
         CREATE FUNCTION public.reject_collector_delivery() RETURNS trigger LANGUAGE plpgsql AS $$
         BEGIN IF NEW.environment IN ('collector-ts','collector-go') AND NEW.sink_id='inject-failure'
         THEN RAISE EXCEPTION 'injected collector delivery failure'; END IF; RETURN NEW; END; $$;
         CREATE TRIGGER reject_collector_delivery BEFORE INSERT ON heimdall.audit_deliveries
           FOR EACH ROW EXECUTE FUNCTION public.reject_collector_delivery();"""],
        capture_output=True, text=True)
    delivery_env = dict(runtime_env, HEIMDALL_TEST_AUDIT_DELIVERY_DB=runtime_dsn + "&options=-c%20role%3Dheimdall_public_runtime")
    run(["pnpm", "--filter", "@heimdall/sdk", "exec", "vitest", "run", "test/postgres-audit-delivery.test.ts", "test/postgres-identity-collector.test.ts"], env=delivery_env)
    subprocess.run(["go", "test", "-race", "-count=1", "./postgres", "-run", "^Test(AuditDelivery|IdentityCollector)Postgres$"],
                   cwd=ROOT / "sdk/go", env=delivery_env, check=True)
    print(json.dumps({"run": name, "auditDelivery": ["typescript", "go"], "result": "passed"}), flush=True)
    for language in ("ts", "go"):
        with CommitLossProxy(int(port)) as proxy:
            fault_env = dict(delivery_env, HEIMDALL_TEST_COLLECTOR_COMMIT_LOSS=(
                f"postgres://postgres:{password}@127.0.0.1:{proxy.port}/public_runtime"
                "?sslmode=disable&options=-c%20role%3Dheimdall_public_runtime"
            ))
            if language == "ts":
                run(["pnpm", "--filter", "@heimdall/sdk", "exec", "vitest", "run",
                     "test/postgres-identity-collector.test.ts", "-t", "loses its commit response"], env=fault_env)
            else:
                subprocess.run(["go", "test", "-race", "-count=1", "./postgres", "-run",
                                "^TestIdentityCollectorCommitLoss$"],
                               cwd=ROOT / "sdk/go", env=fault_env, check=True)
            if not proxy.dropped.is_set() or proxy.errors:
                raise RuntimeError(language + " identity collector COMMIT fault was not verified")
    print(json.dumps({"run": name, "identityCollectorCommitLossAndDelivery": ["typescript", "go"], "result": "passed"}), flush=True)
    elastic_start = os.environ.get("HEIMDALL_TEST_ELASTICSEARCH_START")
    scim_siem = os.environ.get("HEIMDALL_PROBE_SCIM_SIEM") == "1"
    if elastic_start and not scim_siem and os.environ.get("HEIMDALL_PROBE_PRODUCT_SIEM") != "1":
        from live_identity_siem import run as verify_live_identity_siem
        if os.environ.get("HEIMDALL_TEST_SCHEDULED_IDENTITY") == "1":
            import tempfile
            with tempfile.TemporaryDirectory() as temp:
                binary = str(Path(temp) / "support")
                subprocess.run(["go", "build", "-o", binary, "."], cwd=ROOT / "examples/support", check=True)
                run(["pnpm", "run", "build"])
                print(json.dumps(verify_live_identity_siem(
                    dict(delivery_env, HEIMDALL_REFERENCE_SUPPORT_BINARY=binary), elastic_start)), flush=True)
        else:
            print(json.dumps(verify_live_identity_siem(delivery_env, elastic_start)), flush=True)
    directory_env = dict(runtime_env, HEIMDALL_TEST_DIRECTORY_DB=runtime_dsn + "&options=-c%20role%3Dheimdall_public_runtime")
    if os.environ.get("HEIMDALL_PROBE_SCIM_WORKER") == "true":
        run(["node", "tests/integration/scim.mjs"], env=directory_env)
    run(["pnpm", "--filter", "@heimdall/sdk", "exec", "vitest", "run", "test/postgres-directory-deactivation.test.ts"], env=directory_env)
    subprocess.run(["go", "test", "-race", "-count=1", "./postgres", "-run", "^TestDirectoryDeactivationPostgres$"],
                   cwd=ROOT / "sdk/go", env=directory_env, check=True)
    run(["docker", "exec", name, "psql", "-U", "postgres", "-d", "public_runtime",
         "-v", "ON_ERROR_STOP=1", "-c", "DROP TRIGGER reject_directory_delete ON heimdall.records; DROP FUNCTION public.reject_directory_delete();"],
        capture_output=True, text=True)
    print(json.dumps({"run": name, "directoryDeactivation": ["typescript", "go"], "result": "passed"}), flush=True)
    subprocess.run(["go", "test", "-race", "-count=1", ".", "-run", "TestPublicPostgresRuntime|TestPublicManagedRuntime|TestPublicManagedOnboarding|TestPostgresFactory"], cwd=ROOT / "sdk/go", env=runtime_env, check=True)
    if os.environ.get("HEIMDALL_PROBE_PINNED_ADMIN") == "1":
        from pinned_openfga import pinned_openfga
        with pinned_openfga(ROOT) as provider:
            pinned_env = dict(runtime_env, **provider)
            run(["pnpm", "--filter", "@heimdall/sdk", "exec", "vitest", "run", "test/runtime.test.ts", "-t", "managed onboarding safely"], env=pinned_env)
            subprocess.run(["go", "test", "-race", "-count=1", ".", "-run", "^TestPublicManagedOnboarding$"], cwd=ROOT / "sdk/go", env=pinned_env, check=True)
            print(json.dumps({"run": name, "pinnedAdministration": ["typescript", "go"], "result": "passed"}), flush=True)
    if os.environ.get("HEIMDALL_PROBE_SDK_RECOVERY") == "1":
        run(["pnpm", "run", "build"])
        recovery_env = dict(env, HEIMDALL_RECOVERY_DATABASE_URL=runtime_dsn + "&options=-c%20role%3Dheimdall_public_runtime")
        run(["node", "tests/integration/openfga-recovery.mjs"], env=recovery_env)
    if os.environ.get("HEIMDALL_PROBE_REFERENCE_PRODUCTS") == "1":
        import re
        import shutil
        import tempfile
        application_db = "reference_application" if operator or existing else "public_runtime"
        application_dsn = runtime_dsn.replace("/public_runtime?", f"/{application_db}?")
        application_env = dict(runtime_env, HEIMDALL_MIGRATION_DATABASE_URL=
                               runtime_env["HEIMDALL_MIGRATION_DATABASE_URL"].replace("/public_runtime?", f"/{application_db}?"))
        schemas = "\n".join((ROOT / "examples" / name / file).read_text()
                            for name in ("ledger", "support") for file in ("schema.sql", "invitations.sql"))
        schemas += "\nGRANT USAGE ON SCHEMA ledger,support TO heimdall_public_runtime; GRANT SELECT ON ledger.invoices,support.tickets TO heimdall_public_runtime; GRANT SELECT,INSERT,UPDATE ON ledger.invitations,support.invitations TO heimdall_public_runtime;"
        for product, migration, table, fields in (
            ("ledger", "approval.sql", "invoices", "approved_at,approved_by"),
            ("support", "assignment.sql", "tickets", "assigned_at,assigned_to"),
        ):
            schemas += "\n" + (ROOT / "examples" / product / migration).read_text()
            schemas += f"\nGRANT UPDATE ({fields}) ON {product}.{table} TO heimdall_public_runtime;"
        for product in ("ledger", "support"):
            schemas += "\n" + (ROOT / "examples" / "enterprise-provider.sql").read_text().replace(':"product_schema"', product)
            schemas += f"\nGRANT SELECT,INSERT,UPDATE ON {product}.sso_candidates,{product}.sso_publications TO heimdall_public_runtime;"
        schemas += "\nCREATE ROLE heimdall_reference_export NOLOGIN;\n" + (ROOT / "tests/integration/reference-export-grants.sql").read_text()
        if existing:
            for product in ("ledger", "support"):
                schemas += "\n" + (ROOT / "examples/existing-workspaces.sql").read_text().replace(':"product_schema"', product)
                schemas += f"""
                INSERT INTO {product}.workspaces VALUES
                    ('acme','Acme',true), ('globex','Globex',true), ('closed','Closed',false);
                GRANT SELECT ON {product}.workspaces TO heimdall_public_runtime;
                """
            schemas += "\nDROP SCHEMA heimdall_managed CASCADE;"
        run(["docker", "exec", "-i", name, "psql", "-U", "postgres", "-d", application_db, "-v", "ON_ERROR_STOP=1"],
            input=schemas, capture_output=True, text=True)
        run(["pnpm", "run", "build"])
        with tempfile.TemporaryDirectory() as temp:
            consumer = Path(temp)
            binary = str(consumer / "support-bin")
            support_source = ROOT / "examples/support"
            reference_env = dict(env, HEIMDALL_PROBE_SDK_RUNTIME="1",
                                 HEIMDALL_REFERENCE_DATABASE_URL=application_dsn,
                                 HEIMDALL_REFERENCE_SUPPORT_BINARY=binary)
            restore = os.environ.get("HEIMDALL_PROBE_COORDINATED_RESTORE") == "1"
            revocation_race = os.environ.get("HEIMDALL_PROBE_REVOCATION_RACE") == "1"
            if restore:
                reference_env["HEIMDALL_RESTORE_POSTGRES_CONTAINER"] = name
            if operator:
                admin("CREATE DATABASE reference_setup OWNER migration_owner;")
                runtime_password = secrets.token_hex(24)
                admin(f"CREATE ROLE reference_runtime LOGIN PASSWORD '{runtime_password}' "
                      "IN ROLE heimdall_public_runtime; "
                      f"CREATE ROLE reference_export LOGIN PASSWORD '{runtime_password}' "
                      "IN ROLE heimdall_reference_export;")
                setup_env = dict(runtime_env, HEIMDALL_MIGRATION_DATABASE_URL=
                                 runtime_env["HEIMDALL_MIGRATION_DATABASE_URL"].replace("/public_runtime?", "/reference_setup?"))
                for args, migration_env in (
                    (["--initialize-setup-schema", "--sql", str(ROOT / "contracts/persistence/setup/0001.sql")], setup_env),
                    (["--prepare-hold-release-upgrade", "--writers-drained", "--sql-directory", str(ROOT / "contracts/persistence/session")], application_env),
                    (["--activate-hold-release-upgrade", "--writers-drained"], application_env),
                    (["--status", "managed-v5"], application_env),
                ):
                    subprocess.run(["go", "run", "./cmd/heimdall-migrate", *args], cwd=ROOT / "sdk/go",
                                   env=migration_env, check=True, timeout=45)
                setup_dir = consumer / "operator"
                setup_dir.mkdir(mode=0o700)
                reference_env.update(HEIMDALL_REFERENCE_SETUP_DIR=str(setup_dir),
                                     HEIMDALL_SETUP_DATABASE_URL=setup_env["HEIMDALL_MIGRATION_DATABASE_URL"],
                                     HEIMDALL_REFERENCE_OPERATOR_RUNTIME_DB=application_dsn.replace(
                                         f"postgres:{password}@", f"reference_runtime:{runtime_password}@"),
                                     HEIMDALL_REFERENCE_OPERATOR_EXPORT_DB=application_dsn.replace(
                                         f"postgres:{password}@", f"reference_export:{runtime_password}@"))
                for product in ("ledger", "support"):
                    key_path = setup_dir / (product + ".key")
                    with os.fdopen(os.open(key_path, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600), "wb") as key_file:
                        key_file.write(secrets.token_bytes(32))
                    subprocess.run(["go", "run", "./cmd/heimdall-keys", "--register", "--fresh-key",
                                    "--key-file", str(key_path), "--limit", "10000"], cwd=ROOT / "sdk/go",
                                   env=dict(runtime_env, HEIMDALL_KEY_DATABASE_URL=application_dsn),
                                   check=True, timeout=45, stdout=subprocess.DEVNULL)
                run(["docker", "exec", name, "psql", "-U", "postgres", "-d", application_db, "-v", "ON_ERROR_STOP=1",
                     "-c", "GRANT SELECT ON ALL TABLES IN SCHEMA heimdall_retention TO heimdall_public_runtime;"],
                    capture_output=True, text=True)
            revision = os.environ.get("HEIMDALL_PROBE_CONSUMER_REVISION")
            consumer_info = None
            if revision:
                candidate = re.fullmatch(r"v(\d+\.\d+\.\d+-rc\.[1-9]\d*)", revision)
                if not candidate and not re.fullmatch(r"[0-9a-f]{40}", revision):
                    raise RuntimeError("The consumer revision must be a pushed commit or release candidate")
                ledger = consumer / "ledger"
                support_source = consumer / "support"
                shutil.copytree(ROOT / "examples/ledger", ledger, ignore=shutil.ignore_patterns("node_modules"))
                shutil.copytree(ROOT / "examples/support", support_source)
                archive_dir = consumer / "archive"
                archive_dir.mkdir()
                supplied_archive = os.environ.get("HEIMDALL_PROBE_CONSUMER_ARCHIVE")
                if supplied_archive:
                    archive = Path(supplied_archive).resolve(strict=True)
                    if not candidate:
                        raise RuntimeError("An external archive requires a paired candidate version")
                else:
                    run(["pnpm", "--filter", "@heimdall/sdk", "pack", "--pack-destination", str(archive_dir)])
                    archive, = archive_dir.glob("*.tgz")
                if candidate and archive.name != f"heimdall-sdk-{candidate[1]}.tgz":
                    raise RuntimeError("TypeScript archive must match the Go candidate version")
                package_path = ledger / "package.json"
                package = json.loads(package_path.read_text())
                package["dependencies"]["@heimdall/sdk"] = "file:" + str(archive)
                package_path.write_text(json.dumps(package))
                (ledger / "pnpm-workspace.yaml").write_text("minimumReleaseAge: 43200\nminimumReleaseAgeStrict: true\n")
                subprocess.run(["pnpm", "install", "--ignore-scripts"], cwd=ledger, check=True)
                module = "github.com/techsavvyash/heimdall/sdk/go"
                reference_env["GOWORK"] = "off"
                subprocess.run(["go", "mod", "edit", "-dropreplace=" + module, "-droprequire=" + module],
                               cwd=support_source, env=reference_env, check=True)
                subprocess.run(["go", "get", module + "@" + revision], cwd=support_source,
                               env=reference_env, check=True)
                subprocess.run(["go", "mod", "tidy"], cwd=support_source, env=reference_env, check=True)
                reference_env["HEIMDALL_REFERENCE_LEDGER_ENTRY"] = str(ledger / "server.mjs")
                if operator:
                    reference_env["HEIMDALL_REFERENCE_SETUP_CLI"] = str(ledger / "node_modules/@heimdall/sdk/dist/cli.js")
                go_version = subprocess.check_output(["go", "list", "-m", "-f", "{{.Version}}", module],
                                                     cwd=support_source, env=reference_env, text=True).strip()
                if candidate and go_version != revision:
                    raise RuntimeError("Go module did not resolve to the requested candidate")
                consumer_info = {"archive": archive.name, "goModuleVersion": go_version}
                if candidate:
                    consumer_info["archiveSHA256"] = hashlib.sha256(archive.read_bytes()).hexdigest()
            subprocess.run(["go", "build", "-o", binary, "."], cwd=support_source, env=reference_env, check=True)
            previous = os.environ.get("HEIMDALL_PROBE_PREVIOUS_PERSISTENCE_REVISION")
            if previous:
                if not (operator and revision and candidate and supplied_archive and re.fullmatch("[0-9a-f]{40}", previous)):
                    raise RuntimeError("Persistence qualification requires the operator probe, a candidate archive and a pushed previous commit")
                prior = consumer / "previous"
                prior.mkdir()
                exported = subprocess.check_output(["git", "archive", previous], cwd=ROOT, timeout=45)
                subprocess.run(["tar", "-xf", "-"], cwd=prior, input=exported, check=True)
                subprocess.run(["pnpm", "install", "--frozen-lockfile"], cwd=prior,
                               env=dict(os.environ, CI="true"), check=True)
                subprocess.run(["pnpm", "run", "build"], cwd=prior, check=True)
                # Use the same product wiring on each side of the SDK switch.
                prior_archive = prior / "archive"
                prior_archive.mkdir()
                subprocess.run(["pnpm", "--filter", "@heimdall/sdk", "pack", "--pack-destination", str(prior_archive)],
                               cwd=prior, check=True)
                prior_tgz, = prior_archive.glob("*.tgz")
                prior_ledger = consumer / "previous-ledger"
                shutil.copytree(ledger, prior_ledger, ignore=shutil.ignore_patterns("node_modules", "pnpm-lock.yaml"))
                package_path = prior_ledger / "package.json"
                package = json.loads(package_path.read_text())
                package["dependencies"]["@heimdall/sdk"] = "file:" + str(prior_tgz)
                package_path.write_text(json.dumps(package))
                subprocess.run(["pnpm", "install", "--ignore-scripts"], cwd=prior_ledger, check=True)
                prior_support = consumer / "previous-support"
                shutil.copytree(support_source, prior_support)
                subprocess.run(["go", "get", module + "@" + previous], cwd=prior_support, env=reference_env, check=True)
                subprocess.run(["go", "mod", "tidy"], cwd=prior_support, env=reference_env, check=True)
                prior_binary = str(consumer / "previous-support-bin")
                subprocess.run(["go", "build", "-o", prior_binary, "."], cwd=prior_support, env=reference_env, check=True)
                reference_env.update(HEIMDALL_REFERENCE_PREVIOUS_LEDGER_ENTRY=str(prior_ledger / "server.mjs"),
                                     HEIMDALL_REFERENCE_PREVIOUS_SUPPORT_BINARY=prior_binary)
                consumer_info["previousPersistence"] = {
                    "revision": previous,
                    "archiveSHA256": hashlib.sha256(prior_tgz.read_bytes()).hexdigest(),
                    "goModuleVersion": subprocess.check_output(
                        ["go", "list", "-m", "-f", "{{.Version}}", module],
                        cwd=prior_support, env=reference_env, text=True).strip(),
                }
            org_file = Path(temp) / "scim-organization"
            if scim_siem:
                assert elastic_start and os.environ.get("HEIMDALL_PROBE_SCIM_BROWSER") == "1"
                reference_env["HEIMDALL_SCIM_SIEM_ORGANIZATION_FILE"] = str(org_file)
            try:
                reference = subprocess.run(["node", "tests/integration/oidc.mjs"], cwd=ROOT, env=reference_env,
                                       capture_output=True, text=True, timeout=240)
                if reference.returncode != 0:
                    diagnostics = re.findall(r"(?:AssertionError \[ERR_ASSERTION\]:[^\n]+|(?:oidc|reference-(?:products|restore))\.mjs:\d+:\d+|OIDC probe run: [a-f0-9-]+|Restore (?:database|audit delivery) command failed(?: \([a-z_/]+\))?)", reference.stderr)
                    raise RuntimeError("Reference product verification failed: " + " | ".join(diagnostics)[:1000])
                if scim_siem:
                    from live_identity_siem import run as verify_live_identity_siem
                    live = json.loads(reference.stdout)["observations"]
                    event, = [item["scimProviderEvent"] for item in live if "scimProviderEvent" in item]
                    assert event["organizationId"] == org_file.read_text()
                    result = verify_live_identity_siem(
                        dict(delivery_env, HEIMDALL_REFERENCE_SUPPORT_BINARY=binary),
                        elastic_start, [item for item in live if "scimBrowser" in item], scim_event=event,
                    )
                    print(json.dumps({"scimSIEM": result}), flush=True)
            finally:
                if org_file.exists():
                    from live_identity_siem import ISSUER, request
                    org = org_file.read_text()
                    assert org.isascii() and org.isdecimal()
                    status, _ = request(ISSUER, "/v2/organizations/" + org,
                                        "Bearer " + os.environ["HEIMDALL_PROBE_ZITADEL_PAT"], method="DELETE")
                    assert 200 <= status < 300, "Disposable organization cleanup failed"
            result = json.loads(reference.stdout)
            observations = result["observations"]
            operator_proof = [item["operatorSetup"] for item in observations if "operatorSetup" in item]
            if operator:
                assert {item["product"] for item in operator_proof} == {"ledger", "support"}
                key_use = run(["docker", "exec", name, "psql", "-U", "postgres", "-d",
                               application_db, "-XAt", "-v", "ON_ERROR_STOP=1", "-c",
                               "SELECT count(*) FROM heimdall_keys.record_key_usage WHERE used>0;"],
                              capture_output=True, text=True)
                assert key_use.stdout.strip() == "2", "Both registered product keys must have encrypted records"
            products = [result for result in observations if "scopedRead" in result]
            if len(products) != 2:
                raise RuntimeError("missing reference product observations")
            if previous:
                assert {item["product"] for item in products if item.get("mixedPersistence")} == {"ledger", "support"}
            if revocation_race:
                assert {item["product"] for item in products if item.get("revocationRace")} == {"ledger", "support"}
            if existing:
                assert {item["product"] for item in products if item.get("existingWorkspaces")} == {"ledger", "support"}
                state = run(["docker", "exec", name, "psql", "-U", "postgres", "-d", application_db,
                             "-XAt", "-v", "ON_ERROR_STOP=1", "-c",
                             "SELECT to_regnamespace('heimdall_managed') IS NULL; "
                             "SELECT count(DISTINCT product_id) FROM heimdall.audit_decisions "
                             "WHERE environment='verification' AND resolution='completed';"],
                            capture_output=True, text=True)
                assert state.stdout.splitlines() == ["t", "2"]
                writes = run(["docker", "exec", name, "psql", "-U", "postgres", "-d", application_db,
                              "-XAt", "-v", "ON_ERROR_STOP=1", "-c", """
                    SELECT count(*) FROM ledger.invoices
                    WHERE (workspace_id='acme' AND approved_by IS NOT NULL) OR
                          (workspace_id='globex' AND approved_by IS NULL);
                    SELECT count(*) FROM support.tickets
                    WHERE (workspace_id='acme' AND assigned_to IS NOT NULL) OR
                          (workspace_id='globex' AND assigned_to IS NULL);
                    """], capture_output=True, text=True)
                assert writes.stdout.splitlines() == ["2", "2"]
            audit_rows = run(
                ["docker", "exec", "-i", name, "psql", "-U", "postgres", "-d", application_db, "-XAt", "-v", "ON_ERROR_STOP=1"],
                input="SELECT event->>'payload' FROM heimdall.audit_events WHERE environment='verification'",
                capture_output=True, text=True).stdout.splitlines()
            if existing:
                import base64
                events = [json.loads(base64.urlsafe_b64decode(payload + "===")) for payload in audit_rows]
                for product, permission in (("ledger", "invoice.approve"), ("support", "ticket.assign")):
                    scoped = [event for event in events if event["productId"] == product and
                              event.get("permission") == permission]
                    receipts = {(e["tenantId"], e["outcome"]) for e in scoped}
                    assert len(scoped) == 4
                    assert receipts == {("acme", "requested"), ("acme", "allow"),
                                        ("globex", "requested"), ("globex", "deny")}
                    for tenant in ("acme", "globex"):
                        assert len({e["decisionId"] for e in scoped if e["tenantId"] == tenant}) == 1
            elif restore:
                assert {item["product"] for item in products if item["coordinatedRestore"]} == {"ledger", "support"}
            elif os.environ.get("HEIMDALL_PROBE_SCIM_BROWSER") == "1":
                from collections import Counter
                import base64
                events = [json.loads(base64.urlsafe_b64decode(payload + "===")) for payload in audit_rows]
                for product in products:
                    proof = product["scimBrowser"]
                    assert proof["sharedProviderDisable"] and proof["cleanupReplayConfirmed"]
                    if product["product"] == "support":
                        if os.environ.get("HEIMDALL_PROBE_DIRECTORY_JOB") == "1":
                            assert proof["scheduledJob"] and proof["missedObservationDenied"]
                        else:
                            assert proof["cleanupResumedSameRevision"] and proof["deniedDuringCleanupOutage"]
                    scoped = [event for event in events if event["productId"] == product["product"]
                              and event.get("tenantId") == proof["workspaceId"]]
                    assert proof["staleReactivationRejected"] and proof["secondCleanupConfirmed"]
                    for action, count in (("remove_member", 2), ("add_member", 2)):
                        operations = {event["operationId"] for event in scoped
                                      if event["action"] == action and event["outcome"] == "requested"}
                        assert len(operations) == count
                        for operation in operations:
                            chain = [event for event in scoped if event.get("operationId") == operation]
                            assert Counter((event["action"], event["outcome"]) for event in chain) == {
                                (action, "requested"): 1,
                                (action + ".dispatch", "attempted"): 1,
                                (action + ".confirm", "succeeded"): 1,
                                (action + ".complete", "succeeded"): 1,
                            }
                    proof["auditedRemovalAndGrants"] = True
                    del proof["workspaceId"]
                    proof.pop("trace", None)
            elif os.environ.get("HEIMDALL_PROBE_PRODUCT_SIEM") != "1":
                run(["python3", "tests/integration/reference-onboarding-audit.py"],
                    input=json.dumps({"products": products, "payloads": audit_rows}), text=True)
            if os.environ.get("HEIMDALL_PROBE_PRODUCT_SIEM") == "1":
                from live_identity_siem import run as verify_live_identity_siem
                assert elastic_start and all("auditActor" in product for product in products)
                job_env = dict(delivery_env, HEIMDALL_REFERENCE_SUPPORT_BINARY=binary)
                print(json.dumps({"productSIEM": verify_live_identity_siem(job_env, elastic_start, products)}), flush=True)
                for product in products:
                    del product["auditActor"]
                    del product["auditRecipient"]
            print(json.dumps({"referenceProducts": products,
                              **({"storageRun": name, "oidcRun": result["runId"]} if restore or revocation_race or operator or existing else {}),
                              **({"operatorSetup": operator_proof, "applicationSchema": "managed-v5"} if operator else {}),
                              **({"isolatedConsumers": consumer_info} if consumer_info else {})}), flush=True)
    if os.environ.get("HEIMDALL_PROBE_DURABLE_RUNTIME") == "1":
        for args in (
            ["--prepare-hold-release-upgrade", "--writers-drained", "--sql-directory", str(ROOT / "contracts/persistence/session")],
            ["--activate-hold-release-upgrade", "--writers-drained"],
            ["--status", "audit-v5"],
        ):
            subprocess.run(["go", "run", "./cmd/heimdall-migrate", *args],
                           cwd=ROOT / "sdk/go", env=runtime_env, check=True)
        run(["docker", "exec", "-i", name, "psql", "-U", "postgres", "-d", "public_runtime", "-v", "ON_ERROR_STOP=1"],
            input="GRANT SELECT ON ALL TABLES IN SCHEMA heimdall_retention TO heimdall_public_runtime",
            capture_output=True, text=True)
        def audit_snapshot():
            result = run(["docker", "exec", "-i", name, "psql", "-U", "postgres", "-d", "public_runtime",
                          "-XAtq", "-v", "ON_ERROR_STOP=1"], input="""
                SELECT jsonb_build_object(
                    'version', (SELECT database_version FROM heimdall.schema_metadata),
                    'events', (SELECT used_events FROM heimdall_capacity.policy),
                    'bytes', (SELECT used_bytes FROM heimdall_capacity.policy),
                    'deliveries', (SELECT used_deliveries FROM heimdall_capacity.policy),
                    'retainedRows', (SELECT used_rows FROM heimdall_retention.capacity),
                    'scopes', coalesce((
                        SELECT jsonb_object_agg(product_id || '/' || environment, to_jsonb(s))
                        FROM (
                            SELECT e.product_id, e.environment, count(*) AS events,
                                sum(octet_length(e.event::text)) AS bytes,
                                count(*) FILTER (WHERE i.kind = 'ordinary'
                                    AND i.payload_digest = e.payload_digest AND i.recorded_at = e.recorded_at
                                    AND i.routing = '["siem"]'::jsonb
                                    AND c.envelope_bytes = octet_length(e.event::text) AND c.delivery_rows = 1
                                    AND d.sink_id = 'siem' AND d.state = 'pending') AS matched
                            FROM heimdall.audit_events e
                            LEFT JOIN heimdall_retention.event_identities i USING(product_id,environment,event_id)
                            LEFT JOIN heimdall_capacity.event_charges c USING(product_id,environment,event_id)
                            LEFT JOIN heimdall.audit_deliveries d USING(product_id,environment,event_id)
                            WHERE e.environment IN ('verification-ts', 'verification-go')
                            GROUP BY e.product_id,e.environment
                        ) s
                    ), '{}'::jsonb)
                )
                """, capture_output=True, text=True)
            return json.loads(result.stdout)

        before = audit_snapshot()
        live_env = dict(env, HEIMDALL_PROBE_SDK_RUNTIME="1", HEIMDALL_TEST_RUNTIME_DATABASE_URL=runtime_dsn)
        live = subprocess.run(["node", "tests/integration/oidc.mjs"], cwd=ROOT, env=live_env, capture_output=True, text=True, timeout=240)
        if live.returncode != 0:
            # Never emit child assertions or environment containing tokens/credentials.
            raise RuntimeError("combined live runtime verification failed")
        after = audit_snapshot()
        scopes = {f"{product}/verification-{language}" for product in ("ledger", "support") for language in ("ts", "go")}
        if (before["version"] != 5 or after["version"] != 5 or before["scopes"]
                or set(after["scopes"]) != scopes
                or any(scope["events"] != 3 or scope["matched"] != 3 for scope in after["scopes"].values())):
            raise RuntimeError("unexpected v5 audit history")
        deltas = {key: after[key] - before[key] for key in ("events", "bytes", "deliveries", "retainedRows")}
        if deltas != {"events": 12, "bytes": sum(s["bytes"] for s in after["scopes"].values()),
                      "deliveries": 12, "retainedRows": 12}:
            raise RuntimeError("v5 accounting differs from committed events")
        print(json.dumps({"liveRuntime": json.loads(live.stdout), "auditV5": {"scopes": sorted(scopes), **deltas}}), flush=True)
    env["HEIMDALL_TEST_AUDIT_ROLE"] = "heimdall_audit_runtime"
    for peer in (False, True):
        if peer:
            env["HEIMDALL_TEST_READ_PEER"] = "1"
        # Maintenance deliberately takes a table lock to test cancellation. Keep
        # that fault isolated from other suites' multi-connection transactions.
        run(["pnpm", "--filter", "@heimdall/sdk", "exec", "vitest", "run", "test/postgres-maintenance.test.ts"], env=env)
        run(["pnpm", "--filter", "@heimdall/sdk", "exec", "vitest", "run", "test/postgres-records.test.ts", "test/postgres-readiness.test.ts", "test/postgres-audit.test.ts", "test/postgres-sessions.test.ts"], env=env)
        run(["pnpm", "--filter", "@heimdall/sdk", "exec", "vitest", "run", "test/postgres-login.test.ts", "test/browser-login-http.test.ts"], env=env)
        subprocess.run(["go", "test", "-race", "-count=1", "./internal/browserlogin"], cwd=ROOT / "sdk/go", env=env, check=True)
        subprocess.run(["go", "test", "-race", "-count=1", "./internal/postgres"], cwd=ROOT / "sdk/go", env=env, check=True)
    for language in ("ts", "go"):
        with CommitLossProxy(int(port)) as proxy:
            fault_env = dict(env, HEIMDALL_TEST_AUDIT_COMMIT_LOSS=f"postgres://postgres:{password}@127.0.0.1:{proxy.port}/postgres?sslmode=disable")
            if language == "ts":
                run(["pnpm", "--filter", "@heimdall/sdk", "exec", "vitest", "run", "test/postgres-audit.test.ts", "-t", "audit committed response loss"], env=fault_env)
            else:
                subprocess.run(["go", "test", "-race", "-count=1", "./internal/postgres", "-run", "^TestAuditCommitLoss$"], cwd=ROOT / "sdk/go", env=fault_env, check=True)
            if not proxy.dropped.is_set() or proxy.errors:
                raise RuntimeError(language + " audit COMMIT fault was not verified")
    for language in ("ts", "go"):
        for stage in (1, 2):
            with CommitLossProxy(int(port)) as proxy:
                fault_env = dict(env, HEIMDALL_TEST_DECISION_COMMIT_LOSS=f"postgres://postgres:{password}@127.0.0.1:{proxy.port}/postgres?sslmode=disable", HEIMDALL_TEST_DECISION_LOSS_STAGE=str(stage))
                if language == "ts":
                    run(["pnpm", "--filter", "@heimdall/sdk", "exec", "vitest", "run", "test/postgres-audit.test.ts", "-t", "decision audit committed response loss"], env=fault_env)
                else:
                    subprocess.run(["go", "test", "-race", "-count=1", "./internal/postgres", "-run", "^TestDecisionCommitLoss$"], cwd=ROOT / "sdk/go", env=fault_env, check=True)
                if not proxy.dropped.is_set() or proxy.errors:
                    raise RuntimeError(language + " decision COMMIT fault was not verified")
    for language in ("ts", "go"):
        with CommitLossProxy(int(port)) as proxy:
            fault_env = dict(env, HEIMDALL_TEST_ACK_COMMIT_LOSS=f"postgres://postgres:{password}@127.0.0.1:{proxy.port}/postgres?sslmode=disable")
            if language == "ts":
                run(["pnpm", "--filter", "@heimdall/sdk", "exec", "vitest", "run", "test/postgres-audit.test.ts", "-t", "acknowledgement committed response loss"], env=fault_env)
            else:
                subprocess.run(["go", "test", "-race", "-count=1", "./internal/postgres", "-run", "^TestAcknowledgementCommitLoss$"], cwd=ROOT / "sdk/go", env=fault_env, check=True)
            if not proxy.dropped.is_set() or proxy.errors:
                raise RuntimeError(language + " acknowledgement COMMIT fault was not verified")
    for language in ("ts", "go"):
        for operation in ("begin", "defer", "quarantine"):
            with CommitLossProxy(int(port)) as proxy:
                fault_env = dict(env, HEIMDALL_TEST_TRANSITION_LOSS=f"postgres://postgres:{password}@127.0.0.1:{proxy.port}/postgres?sslmode=disable", HEIMDALL_TEST_TRANSITION_KIND=operation)
                if language == "ts":
                    run(["pnpm", "--filter", "@heimdall/sdk", "exec", "vitest", "run", "test/postgres-audit.test.ts", "-t", "delivery transition committed response loss"], env=fault_env)
                else:
                    subprocess.run(["go", "test", "-race", "-count=1", "./internal/postgres", "-run", "^TestTransitionCommitLoss$"], cwd=ROOT / "sdk/go", env=fault_env, check=True)
                if not proxy.dropped.is_set() or proxy.errors:
                    raise RuntimeError(language + " " + operation + " COMMIT fault was not verified")
    for language in ("ts", "go"):
        with CommitLossProxy(int(port)) as proxy:
            fault_env = dict(env, HEIMDALL_TEST_IDENTITY_COMMIT_LOSS=f"postgres://postgres:{password}@127.0.0.1:{proxy.port}/postgres?sslmode=disable")
            if language == "ts":
                run(["pnpm", "--filter", "@heimdall/sdk", "exec", "vitest", "run", "test/postgres-sessions.test.ts", "-t", "identity invalidation committed response loss"], env=fault_env)
            else:
                subprocess.run(["go", "test", "-race", "-count=1", "./internal/postgres", "-run", "^TestIdentityCommitLoss$"], cwd=ROOT / "sdk/go", env=fault_env, check=True)
            if not proxy.dropped.is_set() or proxy.errors:
                raise RuntimeError(language + " identity invalidation COMMIT fault was not verified")
    for language in ("ts", "go"):
        with CommitLossProxy(int(port)) as proxy:
            fault_env = dict(env, HEIMDALL_TEST_SESSION_DELETE_LOSS=f"postgres://postgres:{password}@127.0.0.1:{proxy.port}/postgres?sslmode=disable")
            if language == "ts":
                run(["pnpm", "--filter", "@heimdall/sdk", "exec", "vitest", "run", "test/postgres-sessions.test.ts", "-t", "conditional session deletion committed response loss"], env=fault_env)
            else:
                subprocess.run(["go", "test", "-race", "-count=1", "./internal/postgres", "-run", "^TestConditionalSessionCommitLoss$"], cwd=ROOT / "sdk/go", env=fault_env, check=True)
            if not proxy.dropped.is_set() or proxy.errors:
                raise RuntimeError(language + " conditional session deletion COMMIT fault was not verified")
    for language in ("ts", "go"):
        with CommitLossProxy(int(port)) as proxy:
            fault_env = dict(env, HEIMDALL_TEST_CLEANUP_LOSS=f"postgres://postgres:{password}@127.0.0.1:{proxy.port}/postgres?sslmode=disable")
            if language == "ts":
                run(["pnpm", "--filter", "@heimdall/sdk", "exec", "vitest", "run", "test/postgres-maintenance.test.ts", "-t", "cleanup committed response loss"], env=fault_env)
            else:
                subprocess.run(["go", "test", "-race", "-count=1", "./internal/postgres", "-run", "^TestCleanupCommitLoss$"], cwd=ROOT / "sdk/go", env=fault_env, check=True)
            if not proxy.dropped.is_set() or proxy.errors:
                raise RuntimeError(language + " cleanup COMMIT fault was not verified")
    print(json.dumps({"run": name, "postgres": IMAGE, "result": "passed", "checks": ["bounded expiry cleanup in both SDKs: scopes, identity denial preservation, refresh locks, concurrency, deadline, cross-language rows and committed response loss", "public TS and Go runtime with restricted PostgreSQL role, durable audit, captured sink routing and warm-cache denial", "TS and Go CRUD", "8-way CAS single winner", "expiry and product isolation", "delete/recreate revision", "tampered expiry denied", "cancelled blocked write unknown", "pool recovery", "TS reads Go", "Go reads TS", "11 readiness states in both languages", "read-only connection rejected", "missing metadata table rejected", "migration transaction visibility", "concurrent deployment migration processes", "idempotent migration and incompatible state refusal", "mid-migration permission failure rolls back entire schema", "held migration lock deadline and retry", "confirmed COMMIT response loss returns outcome_unknown and safe rerun", "TS and Go atomic audit append rollback race and conflict", "shared managed onboarding provider and activation faults in both SDKs", "both SDKs decision request commits before evaluation and terminal receipt before release", "both SDKs request and terminal audit COMMIT loss block decision release", "bidirectional audit receipts and original sink routing", "TS and Go audit committed response loss and receipt recovery", "audit append with SELECT INSERT privileges only", "shared U+0000 and Unicode event survives TS write and Go replay", "both SDKs delivery receipt matrix and atomic partial acknowledgement", "both SDKs acknowledgement committed response loss and idempotent recovery", "pending selection scope time count and byte budgets", "invalid payload and cross-scope corruption withheld", "pending readers decode both language writers", "delivery attempt races stale callbacks defer and quarantine", "explicit quarantine unblocks healthy backlog without deletion", "both SDKs claim defer quarantine COMMIT response loss", "identity-bound session cross-language reads and shared advisory lock", "both SDKs verified token and fixture provider status feed standalone durable identity gate without session cipher", "both SDKs authentication results wait for committed audit receipts with six events and outbox rows per scope", "token-negative gate preserves bearer sessions and replacement revisions", "identity-negative gate persists denial and blocks warm verifier cache", "identity gate COMMIT response loss preserves uncertain outcome", "session creation versus invalidation race and persistent denial", "identity and product isolation with generic CRUD bypass prevention", "both SDKs identity invalidation COMMIT response loss and safe replay", "identity and revision bound session invalidation", "CAS versus conditional invalidation single winner in both SDKs", "Go conditionally invalidates TS session", "both SDKs conditional deletion COMMIT loss and replacement preservation"]}))
finally:
    subprocess.run(["docker", "rm", "--force", "--volumes", name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)

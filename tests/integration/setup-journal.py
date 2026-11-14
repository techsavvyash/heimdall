"""Verify the independent setup journal on a disposable PostgreSQL database."""
import concurrent.futures
import json
import os
from pathlib import Path
import secrets
import shlex
import subprocess
import sys
import time
import uuid
import urllib.request

ROOT = Path(__file__).resolve().parents[2]
IMAGE = "postgres:17.11-alpine@sha256:3540c0362c809df084ffac0ae69fe794515f9de92692f02178bcdce0ef0302ac"
RUN = uuid.uuid4().hex
NAME = "heimdall-setup-" + RUN
DATABASE = "heimdall_setup_" + RUN
ROLE = DATABASE + "_app"
assert sys.argv[1:] in ([], ["--railway"])
railway = bool(sys.argv[1:])
catalog = json.loads((ROOT / "contracts/persistence/catalog.json").read_text())
migration = catalog["setup"][0]
source = ROOT / "contracts/persistence" / migration["path"]


def sql(query, database=DATABASE, rejected=False):
    if railway:
        args = ["railway", "ssh", "--project", "944fc9be-8648-4237-82bc-72e4bd24a4c9",
                "--environment", "7bc7c286-b2dc-4415-85eb-90dd554ab2fb",
                "--service", "648133ac-305b-495d-a77e-4d0c49103feb", "--identity-file",
                str(Path.home() / ".ssh/heimdall-verification"), "--",
                "psql -XAtq -U heimdall_probe -v ON_ERROR_STOP=1 -d " + database + " -c " + shlex.quote(query)]
        result = subprocess.run(args, capture_output=True, text=True, timeout=30)
    else:
        args = ["docker", "exec", "-i", NAME, "psql", "-XAtq", "-U", "postgres",
                "-v", "ON_ERROR_STOP=1", "-d", database]
        result = subprocess.run(args, input=query, capture_output=True, text=True, timeout=30)
    if rejected:
        assert result.returncode != 0
    elif result.returncode != 0:
        raise RuntimeError("SQL test failed: " + result.stderr[-500:])
    return result.stdout.strip()


container = database_created = role_created = False
try:
    if not railway:
        env = dict(os.environ, POSTGRES_PASSWORD=secrets.token_hex(24))
        subprocess.run(["docker", "run", "--detach", "--name", NAME, "-e", "POSTGRES_PASSWORD",
                        "--tmpfs", "/var/lib/postgresql/data:rw,size=256m", "-p", "127.0.0.1::5432", IMAGE], env=env, check=True, stdout=subprocess.DEVNULL)
        container = True
        for _ in range(60):
            if subprocess.run(["docker", "exec", NAME, "pg_isready", "-h", "127.0.0.1", "-U", "postgres"],
                              stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode == 0:
                break
            time.sleep(.5)
        else:
            raise RuntimeError("PostgreSQL readiness timeout")
        port = subprocess.check_output(["docker", "port", NAME, "5432"], text=True).strip().split(":")[-1]
    sql("CREATE DATABASE " + DATABASE, "postgres")
    database_created = True
    sql("CREATE ROLE " + ROLE + " NOLOGIN", "postgres")
    role_created = True
    if railway:
        sql("BEGIN;" + source.read_text() +
            f"INSERT INTO heimdall_setup.migrations(version,checksum) VALUES(1,'{migration['checksum']}');"
            "INSERT INTO heimdall_setup.schema_metadata VALUES(true,1,'ready',1,1,1,1);COMMIT;")
    else:
        dsn = f"postgres://postgres:{env['POSTGRES_PASSWORD']}@127.0.0.1:{port}/{DATABASE}?sslmode=disable"
        cli = ["go", "run", "./cmd/heimdall-migrate"]
        cli_env = dict(env, HEIMDALL_MIGRATION_DATABASE_URL=dsn)
        for args in (["-initialize-setup-schema", "-sql", str(source)],
                     ["-initialize-setup-schema", "-sql", str(source)], ["-status", "setup"]):
            subprocess.run(cli + args, env=cli_env, cwd=ROOT / "sdk/go", check=True, timeout=45)

    assert sql("SELECT count(*) FROM pg_namespace WHERE nspname IN ('heimdall','heimdall_managed')") == "0"
    sql(f"GRANT USAGE ON SCHEMA heimdall_setup TO {ROLE};")
    sql(f"SET ROLE {ROLE}; SELECT state FROM heimdall_setup.claim('ledger','verification',repeat('a',64),2,1,gen_random_uuid());", rejected=True)
    sql(f"GRANT SELECT ON ALL TABLES IN SCHEMA heimdall_setup TO {ROLE};"
        f"GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA heimdall_setup TO {ROLE};")
    prefix = f"SET ROLE {ROLE}; "
    dispatches = [str(uuid.uuid4()) for _ in range(4)]

    def claim(dispatch, ordinal=1, product="ledger", fingerprint="a"):
        return sql(prefix + "SELECT state,dispatch_id,resource_id FROM heimdall_setup.claim("
                   f"'{product}','verification',repeat('{fingerprint}',64),2,{ordinal},'{dispatch}');")

    def confirm(dispatch, resource, ordinal=1):
        return sql(prefix + "SELECT heimdall_setup.confirm("
                   f"'ledger','verification',repeat('a',64),{ordinal},'{dispatch}','{resource}');")

    with concurrent.futures.ThreadPoolExecutor(max_workers=4) as executor:
        claims = list(executor.map(claim, dispatches))
    winner, = [value.split("|")[1] for value in claims if value.startswith("claimed|")]
    assert claims.count(f"unresolved|{winner}|") == 3
    assert claim(str(uuid.uuid4())) == f"unresolved|{winner}|"
    assert claim(str(uuid.uuid4()), 2) == "conflict||"
    assert claim(str(uuid.uuid4()), fingerprint="b") == "conflict||"
    assert confirm(str(uuid.uuid4()), "project_1") == "f"
    assert confirm(winner, "project_1") == "t"
    assert confirm(winner, "project_1") == "t"
    assert confirm(winner, "project_2") == "f"
    assert claim(str(uuid.uuid4())) == f"confirmed|{winner}|project_1"
    assert claim(str(uuid.uuid4()), 2).startswith("claimed|")
    assert claim(str(uuid.uuid4()), product="support").startswith("claimed|")
    sql(prefix + "UPDATE heimdall_setup.steps SET resource_id=NULL;", rejected=True)
    sql(prefix + "DELETE FROM heimdall_setup.steps;", rejected=True)
    sql(prefix + "UPDATE heimdall_setup.schema_metadata SET database_version=2;", rejected=True)
    if not railway:
        runtime = dict(env, HEIMDALL_TEST_SETUP_DSN=dsn + "&options=-c%20role%3D" + ROLE)
        subprocess.run(["pnpm", "--filter", "@techsavvyash/heimdall-sdk", "exec", "vitest", "run", "test/setup-journal.test.ts"],
                       env=runtime, cwd=ROOT, check=True, timeout=45)
        subprocess.run(["go", "test", "-race", "-count=1", "./internal/postgres", "-run", "^TestSetupJournal$"],
                       env=runtime, cwd=ROOT / "sdk/go", check=True, timeout=45)
        if runtime.get("HEIMDALL_PROBE_ZITADEL_PAT"):
            issuer = runtime["HEIMDALL_PROBE_ZITADEL_ISSUER"]
            assert issuer.startswith("https://")
            def provider(path, method, body=None):
                request = urllib.request.Request(issuer + path, method=method,
                    headers={"Authorization": "Bearer " + runtime["HEIMDALL_PROBE_ZITADEL_PAT"],
                             "Content-Type": "application/json"},
                    data=json.dumps(body).encode() if body else None)
                class NoRedirect(urllib.request.HTTPRedirectHandler):
                    def redirect_request(self, *args, **kwargs):
                        return None
                with urllib.request.build_opener(NoRedirect).open(request, timeout=15) as response:
                    return json.loads(response.read() or b"{}")
            org = provider("/v2/organizations", "POST", {"name": NAME})["organizationId"]
            try:
                runtime["HEIMDALL_TEST_SETUP_ZITADEL_ORG"] = org
                subprocess.run(["pnpm", "run", "build"], cwd=ROOT, check=True, timeout=45)
                subprocess.run(["go", "test", "-race", "-count=1", "./provisioning"],
                               env=runtime, cwd=ROOT / "sdk/go", check=True, timeout=60)
                subprocess.run(["pnpm", "--filter", "@techsavvyash/heimdall-sdk", "exec", "vitest", "run", "--testTimeout=35000", "test/setup-project.test.ts", "test/setup-openfga.test.ts", "test/setup-product.test.ts"],
                               env=runtime, cwd=ROOT, check=True, timeout=60)
            finally:
                provider("/v2/organizations/" + org, "DELETE")
    print(json.dumps({"run": RUN, "railway": railway, "result": "passed",
                      "checks": ["standalone schema", "concurrent claim", "restart preserves dispatch",
                                 "ordered steps", "fingerprint conflicts", "exact confirmation replay",
                                 "product isolation", "privilege boundary"]}))
finally:
    if railway:
        if database_created:
            sql("DROP DATABASE " + DATABASE, "postgres")
        if role_created:
            sql("DROP ROLE " + ROLE, "postgres")
    elif container:
        subprocess.run(["docker", "rm", "-f", NAME], check=True, stdout=subprocess.DEVNULL)

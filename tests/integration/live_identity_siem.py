"""Disposable Railway ZITADEL, PostgreSQL and Elasticsearch integration proof."""

import base64
from concurrent.futures import ThreadPoolExecutor
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import secrets
import subprocess
import sys
import time
from urllib.error import HTTPError, URLError
from urllib.request import Request, build_opener
from uuid import uuid4
from wsgiref.simple_server import make_server

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "examples" / "audit"))
from elasticsearch_receipt import NoRedirect, create_handler

ISSUER = "https://capability-zitadel-verification.up.railway.app"
ELASTIC = "http://127.0.0.1:19243"
opener = build_opener(NoRedirect())


def request(origin, path, auth, body=None, method=None):
    data = None if body is None else json.dumps(body).encode()
    req = Request(origin + path, data, {"Authorization": auth, "Content-Type": "application/json"}, method=method)
    try:
        response = opener.open(req, timeout=10)
    except HTTPError as error:
        response = error
    with response:
        raw = response.read()
        return response.status, json.loads(raw) if raw else {}


def run(delivery_env, elastic_start):
    run_id = str(uuid4())
    admin = "Bearer " + os.environ["HEIMDALL_PROBE_ZITADEL_PAT"]
    elastic_auth = "Basic " + base64.b64encode(os.environ["HEIMDALL_TEST_ELASTICSEARCH_CREDENTIALS"].encode()).decode()
    permission = "/zitadel.internal_permission.v2.InternalPermissionService/"
    org = reader = None
    granted = False
    elastic = None
    scopes = {
        "ts": ("ledger", "live-siem-ts", "acme"),
        "go": ("support", "live-siem-go", "globex"),
    }
    indexes = {lang: "heimdall-live-" + lang + "-" + run_id for lang in scopes}
    created = []

    def api(path, body=None, method=None, auth=admin):
        status, result = request(ISSUER, path, auth, body, method)
        assert 200 <= status < 300, f"ZITADEL request failed with status {status}"
        return result

    def es(path, body=None, method=None):
        status, result = request(ELASTIC, path, elastic_auth, body, method)
        assert 200 <= status < 300, f"Elasticsearch request failed with status {status}"
        return result

    def start():
        nonlocal elastic
        elastic = subprocess.Popen([elastic_start], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        deadline = time.monotonic() + 60
        while time.monotonic() < deadline:
            assert elastic.poll() is None, "Elasticsearch verification process exited"
            try:
                status, _ = request(ELASTIC, "/_cluster/health?wait_for_status=yellow&timeout=2s", elastic_auth)
                if status == 200:
                    info = es("/")
                    assert info["version"]["number"] == "8.19.0"
                    return info["cluster_uuid"]
            except URLError:
                pass
            time.sleep(1)
        raise RuntimeError("Elasticsearch readiness deadline")

    def stop():
        nonlocal elastic
        if elastic is not None:
            if elastic.poll() is None:
                elastic.terminate()
                elastic.wait(timeout=30)
            elastic = None

    def dispatch(lang, stage, subject, start_micros, pat, drop_receipt=False):
        product, environment, tenant = scopes[lang]
        token = secrets.token_urlsafe(32)
        handler = create_handler(endpoint=ELASTIC, authorization=elastic_auth, index=indexes[lang],
                                 product=product, environment=environment, sink="siem", token=token, tenant=tenant, loopback=True)
        statuses = []

        def observed(environ, respond):
            def capture(status, headers):
                code = int(status[:3])
                statuses.append(code)
                # Return a failure after Elasticsearch has accepted the document.
                respond("503 Service Unavailable" if drop_receipt and code == 200 else status, headers)
            return handler(environ, capture)

        with make_server("127.0.0.1", 0, observed) as server, ThreadPoolExecutor(max_workers=1) as workers:
            serving = workers.submit(server.serve_forever)
            config = {
                "stage": stage, "issuer": ISSUER, "organizationId": org, "providerToken": pat,
                "subject": subject, "startMicros": start_micros, "sinkToken": token,
                "endpoint": f"http://127.0.0.1:{server.server_port}",
            }
            env = dict(delivery_env, HEIMDALL_TEST_IDENTITY_SIEM=json.dumps(config))
            try:
                if stage == "collect":
                    status, _ = request(config["endpoint"], "/", "Bearer " + token, {
                        "productId": product, "environment": environment, "sinkId": "siem",
                        "eventIds": ["wrong-tenant"], "events": [{
                            "eventId": "wrong-tenant", "productId": product,
                            "environment": environment, "tenantId": "unmapped",
                        }],
                    })
                    assert status == 403, "Tenant-scoped receiver accepted an unrelated event"
                    statuses.clear()
                if lang == "ts":
                    subprocess.run(["pnpm", "--filter", "@heimdall/sdk", "exec", "vitest", "run",
                                    "test/live-identity-siem.test.ts"], cwd=ROOT, env=env, check=True)
                else:
                    subprocess.run(["go", "test", "-race", "-count=1", "./postgres", "-run", "^TestLiveIdentitySIEM$"],
                                   cwd=ROOT / "sdk/go", env=env, check=True)
            finally:
                server.shutdown()
                serving.result(timeout=10)
        assert statuses == ([503] if stage == "outage" else [200]), "Unexpected receiver status"

    try:
        # Start and stop only the disposable process launched by this probe.
        try:
            request(ELASTIC, "/", elastic_auth)
        except URLError:
            pass
        else:
            raise RuntimeError("Elasticsearch verification port is already occupied")
        cluster = start()
        org = api("/v2/organizations", {"name": "heimdall-siem-" + run_id})["organizationId"]

        def machine(name):
            return api("/v2/users/new", {"organizationId": org, "username": name + "-" + run_id,
                                       "machine": {"name": name}})["id"]

        reader = machine("reader")
        admin_scope = {"userId": reader, "resource": {"instance": True}}
        api(permission + "CreateAdministrator", {**admin_scope, "roles": ["IAM_OWNER_VIEWER"]})
        granted = True
        pat = api(f"/v2/users/{reader}/pats", {
            "expirationDate": datetime.fromtimestamp(time.time() + 900, timezone.utc).isoformat()
        })["token"]
        subject = machine("assigned")
        unrelated = machine("unassigned")
        for user in (unrelated, subject):
            api(f"/v2/users/{user}/deactivate", {})
        query = {"resourceOwner": org, "asc": True, "limit": 20, "aggregateId": subject}
        events = api("/admin/v1/events/_search", query, auth="Bearer " + pat)["events"]
        deactivated, = [e for e in events if e["type"]["type"] == "user.deactivated"]
        date = datetime.fromisoformat(deactivated["creationDate"].replace("Z", "+00:00"))
        delta = date - datetime(1970, 1, 1, tzinfo=timezone.utc)
        micros = (delta.days * 86400 + delta.seconds) * 1000000 + delta.microseconds
        for lang, index in indexes.items():
            es("/" + index, {"settings": {"index.translog.durability": "request", "number_of_replicas": 0}}, "PUT")
            created.append(index)
            dispatch(lang, "collect", subject, micros, pat, drop_receipt=True)
        stop()
        # Tests make the pending deliveries due using the disposable database, without waiting for backoff.
        def due(lang):
            product, environment, _ = scopes[lang]
            update = (
                "UPDATE heimdall.audit_deliveries SET next_attempt_at=0 "
                f"WHERE product_id='{product}' AND environment='{environment}' AND state='pending' RETURNING event_id"
            )
            command = ["psql", delivery_env["HEIMDALL_TEST_DATABASE_URL"], "-At", "-v", "ON_ERROR_STOP=1", "-c", update]
            lines = subprocess.run(command, capture_output=True, text=True, check=True).stdout.splitlines()
            assert len(lines) == 2 and lines[-1] == "UPDATE 1"
            return lines[0]

        event_ids = {}
        for lang in scopes:
            event_ids[lang] = due(lang)
            dispatch(lang, "outage", subject, micros, pat)
            assert due(lang) == event_ids[lang], "Outage changed the pending event identity"
        assert start() == cluster
        for lang, (product, environment, tenant) in scopes.items():
            index = indexes[lang]
            key = hashlib.sha256(json.dumps([product, environment, event_ids[lang]], separators=(",", ":")).encode()).hexdigest()
            event = es(f"/{index}/_doc/{key}")["_source"]
            assert event["eventId"] == event_ids[lang] and event["tenantId"] == tenant
            assert event["target"]["id"] == subject and event["action"] == "user.deactivated"
            assert event["attributes"]["sequence"] == deactivated["sequence"]
            dispatch(lang, "retry", subject, micros, pat)
            check = (
                "SELECT state,attempts FROM heimdall.audit_deliveries "
                f"WHERE product_id='{product}' AND environment='{environment}' AND sink_id='siem'"
            )
            command = ["psql", delivery_env["HEIMDALL_TEST_DATABASE_URL"], "-At", "-v", "ON_ERROR_STOP=1", "-c", check]
            assert subprocess.run(command, capture_output=True, text=True, check=True).stdout.strip() == "delivered|3"
            es(f"/{index}/_refresh", method="POST")
            assert es(f"/{index}/_count")["count"] == 1
        return {"run": run_id, "languages": ["typescript", "go"], "elasticsearch": "8.19.0",
                "liveProvider": True, "sinkOutageRetainedPending": True, "lostReceiptRecovered": True,
                "sinkRestartPreservedDocuments": True, "oneDocumentPerProduct": True, "tenantMismatchRejected": True}
    finally:
        try:
            if elastic is not None:
                for index in created:
                    es("/" + index, method="DELETE")
        finally:
            stop()
            try:
                if granted:
                    api(permission + "DeleteAdministrator", admin_scope)
            finally:
                if org:
                    api("/v2/organizations/" + org, method="DELETE")

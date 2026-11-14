"""Disposable Railway ZITADEL, PostgreSQL and Elasticsearch integration proof."""

import base64
from concurrent.futures import ThreadPoolExecutor
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import secrets
import shlex
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


def verification_policy():
    project = "944fc9be-8648-4237-82bc-72e4bd24a4c9"
    environment = "7bc7c286-b2dc-4415-85eb-90dd554ab2fb"
    flags = ["-p", project, "-e", environment]
    deployment = json.loads(subprocess.check_output([
        "railway", "deployment", "list", *flags, "-s", "capability-zitadel", "--limit", "1", "--json",
    ]))[0]
    assert deployment["status"] == "SUCCESS"
    variables = json.loads(subprocess.check_output([
        "railway", "variable", "list", *flags, "-s", "capability-zitadel", "--json",
    ]))
    assert variables["ZITADEL_AUDITLOGRETENTION"] == "0s"
    query = """SELECT count(*) FROM projections.limits WHERE audit_log_retention IS NOT NULL;
        SHOW max_prepared_transactions;
        SELECT coalesce(array_to_string(setconfig,','),'') FROM pg_db_role_setting
        WHERE setrole=(SELECT oid FROM pg_roles WHERE rolname='heimdall_zitadel')
          AND setdatabase=(SELECT oid FROM pg_database WHERE datname='zitadel');
        SELECT trunc(EXTRACT(EPOCH FROM clock_timestamp())*1000)::bigint;"""
    command = "psql -U heimdall_probe -d zitadel -At -v ON_ERROR_STOP=1 -c " + shlex.quote(query)
    for _ in range(3):
        before = time.time_ns() // 1_000_000
        lines = subprocess.check_output([
            "railway", "ssh", *flags, "-s", "648133ac-305b-495d-a77e-4d0c49103feb",
            "--identity-file", str(Path.home()/".ssh/heimdall-verification"), "--", command,
        ], text=True, timeout=20).splitlines()
        after = time.time_ns() // 1_000_000
        assert lines[:3] == ["0", "0", "transaction_timeout=10s"]
        if before - 2000 <= int(lines[3]) <= after + 2000 and after - before <= 4000:
            return {"maxWriterTransactionMs": 10000, "maxClockOffsetMs": 5000, "minimumRetentionMs": 86400000}
    raise RuntimeError("Railway clock sample did not satisfy the audit probe timing bounds")


def run(delivery_env, elastic_start, products=None, scim_event=None):
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
    configuration_edit = os.environ.get("HEIMDALL_PROBE_SIEM_CONFIGURATION_EDIT") == "1"
    aggregate_type = "user"
    job_binary = delivery_env.get("HEIMDALL_REFERENCE_SUPPORT_BINARY")
    ledger_entry = Path(delivery_env.get("HEIMDALL_REFERENCE_LEDGER_ENTRY", ROOT / "examples/ledger/server.mjs"))
    policy = verification_policy() if scim_event or os.environ.get("HEIMDALL_TEST_SCHEDULED_IDENTITY") == "1" else None

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

    def dispatch(lang, stage, subject, start_micros, pat, drop_receipt=False, expected_events=0):
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
                "stage": stage, "policy": policy, "expectedEvents": expected_events, "issuer": ISSUER, "organizationId": org, "providerToken": pat,
                "subject": subject, "startMicros": start_micros, "sinkToken": token,
                "aggregateType": aggregate_type,
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
                if stage in ("product", "scheduled", "scim") and job_binary:
                    command = ["node", str(ledger_entry.with_name("audit-job.mjs"))] if lang == "ts" else [job_binary, "audit-job"]
                    job_env = dict(
                        delivery_env,
                        HEIMDALL_CONFIG=json.dumps({"productId": product, "environment": environment, "issuer": ISSUER}),
                        DATABASE_URL=delivery_env["HEIMDALL_TEST_AUDIT_DELIVERY_DB"],
                        HEIMDALL_AUDIT_ENDPOINT=config["endpoint"],
                        HEIMDALL_AUDIT_TOKEN=token,
                        HEIMDALL_AUDIT_TRANSPORT_PROFILE="loopback",
                    )
                    if stage in ("scheduled", "scim"):
                        job_env.update(
                            HEIMDALL_AUDIT_TENANT=tenant,
                            HEIMDALL_IDENTITY_READER_TOKEN=pat,
                            HEIMDALL_IDENTITY_MAPPING=json.dumps({
                                "organizationId": org, "startMicros": str(start_micros),
                                "aggregates": [{"type": aggregate_type, "id": subject}], "coverage": policy,
                            }),
                        )
                    for _ in range(50):
                        result = subprocess.run(command, cwd=ROOT, env=job_env, capture_output=True, text=True, timeout=50)
                        assert result.returncode == 0, "Reference audit job failed"
                        summary = json.loads(result.stdout.splitlines()[-1])
                        assert summary["quarantined"] == 0
                        if stage == "scheduled":
                            collected = json.loads(result.stdout.splitlines()[-2])
                            assert collected["collected"] == expected_events
                            assert int(collected["nextMicros"]) > start_micros
                            assert summary["delivered"] == expected_events
                            break
                        if stage == "scim":
                            job_env.pop("HEIMDALL_IDENTITY_MAPPING", None)
                            job_env.pop("HEIMDALL_IDENTITY_READER_TOKEN", None)
                        if summary["attempted"] == 0:
                            break
                    else:
                        raise RuntimeError("Reference audit job exceeded page budget")
                elif lang == "ts":
                    subprocess.run(["pnpm", "--filter", "@heimdall/sdk", "exec", "vitest", "run",
                                    "test/live-identity-siem.test.ts"], cwd=ROOT, env=env, check=True)
                else:
                    subprocess.run(["go", "test", "-race", "-count=1", "./postgres", "-run", "^TestLiveIdentitySIEM$"],
                                   cwd=ROOT / "sdk/go", env=env, check=True)
            finally:
                server.shutdown()
                serving.result(timeout=10)
        if stage in ("product", "scim"):
            assert statuses and set(statuses) == {200}
        else:
            expected_status = {"outage": [503], "collect": [200], "retry": [200],
                               "scheduled": [200] if job_binary and expected_events else []}
            assert statuses == expected_status[stage], "Unexpected receiver status"

    try:
        # Start and stop only the disposable process launched by this probe.
        try:
            request(ELASTIC, "/", elastic_auth)
        except URLError:
            pass
        else:
            raise RuntimeError("Elasticsearch verification port is already occupied")
        cluster = start()
        if scim_event:
            org = scim_event["organizationId"]
            subject = scim_event["subject"]
            date = datetime.fromisoformat(scim_event["creationDate"].replace("Z", "+00:00"))
            delta = date - datetime(1970, 1, 1, tzinfo=timezone.utc)
            micros = (delta.days * 86400 + delta.seconds) * 1000000 + delta.microseconds
            checks = {}
            for lang, (product, _, _) in list(scopes.items()):
                fixture = next(item["scimBrowser"] for item in products if item["product"] == product)
                workspace = fixture["workspaceId"]
                trace = fixture["trace"]
                assert trace["subject"] == subject
                scopes[lang] = (product, "verification", workspace)
                index = indexes[lang]
                es("/" + index, {"settings": {"index.translog.durability": "request", "number_of_replicas": 0}}, "PUT")
                created.append(index)
                dispatch(lang, "scim", subject, micros, os.environ["HEIMDALL_PROBE_ZITADEL_PAT"])
                es(f"/{index}/_refresh", method="POST")
                hits = es(f"/{index}/_search", {"size": 1000, "track_total_hits": True})["hits"]
                events = [hit["_source"] for hit in hits["hits"]]
                assert len(events) == hits["total"]["value"]
                assert all(event["productId"] == product and event["tenantId"] == workspace for event in events)
                provider, = [event for event in events if event["source"] == "zitadel"
                             and event["action"] == "user.deactivated" and event["target"]["id"] == subject
                             and event["attributes"]["sequence"] == scim_event["sequence"]]
                # Join the SDK's revision-bound idempotency key to the recorded managed operation.
                binding = ["heimdall.idempotency.v1", product, "verification", "remove_member",
                           trace["actor"], f"directory-cleanup:{trace['revision']}:{workspace}"]
                digest = hashlib.sha256(json.dumps(binding, ensure_ascii=True, separators=(",", ":")).encode()).hexdigest()
                target = "user:" + base64.urlsafe_b64encode(
                    json.dumps([ISSUER, subject], separators=(",", ":")).encode()).decode().rstrip("=")
                target_hash = hashlib.sha256(target.encode()).hexdigest()
                operation = subprocess.check_output([
                    "psql", delivery_env["HEIMDALL_TEST_DATABASE_URL"], "-XAt", "-v", "ON_ERROR_STOP=1", "-c",
                    f"SELECT operation_id FROM heimdall_managed.operations WHERE product_id='{product}' "
                    "AND environment='verification' AND kind='remove_member' AND state='completed' "
                    f"AND key_digest=decode('{digest}','hex') AND target_hash=decode('{target_hash}','hex')",
                ], text=True).strip()
                assert operation
                removed, = [event for event in events if event.get("operationId") == operation
                            and event["action"] == "remove_member.complete" and event["outcome"] == "succeeded"]
                assert provider["occurredAt"] <= int(trace["denialAt"]) <= removed["occurredAt"]
                checks[product] = {"providerEvent": True, "durableDenialRevision": True,
                                   "revisionBoundRemoval": True, "tenantScopedDocuments": len(events)}
            # The reference probe retains the disposable source only until the collectors run.
            return {"run": run_id, "elasticsearch": "8.19.0", "products": checks}
        if products is not None:
            checks = {}
            for lang, (product, _, _) in list(scopes.items()):
                scopes[lang] = (product, "verification", None)
                index = indexes[lang]
                es("/" + index, {"settings": {"index.translog.durability": "request", "number_of_replicas": 0}}, "PUT")
                created.append(index)
                dispatch(lang, "product", None, 0, None)
                es(f"/{index}/_refresh", method="POST")
                hits = es(f"/{index}/_search", {"size": 1000, "track_total_hits": True})["hits"]
                events = [hit["_source"] for hit in hits["hits"]]
                assert len(events) == hits["total"]["value"] and events
                assert all(event["productId"] == product and event["environment"] == "verification" for event in events)
                assert len({event["eventId"] for event in events}) == len(events)
                deliveries = subprocess.check_output([
                    "psql", delivery_env["HEIMDALL_TEST_DATABASE_URL"], "-XAt", "-v", "ON_ERROR_STOP=1", "-c",
                    f"SELECT event_id,state FROM heimdall.audit_deliveries WHERE product_id='{product}' "
                    "AND environment='verification' AND sink_id='siem'",
                ], text=True).splitlines()
                assert set(deliveries) == {event["eventId"] + "|delivered" for event in events}
                fixture = next(item for item in products if item["product"] == product)
                actor = {"kind": "user", "issuer": ISSUER, "subject": fixture["auditActor"]}
                recipient = {**actor, "subject": fixture["auditRecipient"]}
                workspace = fixture["onboarding"]["workspaceId"]
                action = "invoice" if product == "ledger" else "ticket"
                owned = [event for event in events if event["actor"] == actor]
                login = [event for event in owned if event["eventType"] == "heimdall.authentication.completed"
                         and event["outcome"] == "succeeded"]
                allow = [event for event in owned if event.get("tenantId") == workspace
                         and event["action"] == action + ".read" and event["outcome"] == "allow"]
                business = [event for event in owned if event.get("tenantId") == workspace
                            and event["eventType"] == "product." + action + ".listed"
                            and event["attributes"] == {"count": 0} and event["outcome"] == "succeeded"]
                grant = [event for event in owned if event.get("tenantId") == workspace
                         and event.get("operationId") == fixture["administration"]["grant"]["operationId"]
                         and event["action"] == "add_member.complete" and event["outcome"] == "succeeded"]
                assert login and allow and business and grant, f"Missing product audit chain for {product}"
                assert any(a["recordedAt"] <= b["recordedAt"] <= c["recordedAt"] <= d["recordedAt"]
                           for a in login for b in allow for c in business for d in grant)
                recipient_allow = [event for event in events if event["actor"] == recipient
                                   and event["tenantId"] == workspace and event["action"] == action + ".read"
                                   and event["outcome"] == "allow"]
                recipient_read = [event for event in events if event["actor"] == recipient
                                  and event["tenantId"] == workspace and event["eventType"] == "product." + action + ".listed"
                                  and event["attributes"] == {"count": 0} and event["outcome"] == "succeeded"]
                assert any(a["recordedAt"] <= b["recordedAt"] <= c["recordedAt"]
                           for a in grant for b in recipient_allow for c in recipient_read)
                checks[product] = {"documents": len(events), "loginDecisionBusinessAndRoleChange": True, "recipientReadAfterGrant": True}
            return {"run": run_id, "elasticsearch": "8.19.0", "products": checks, "referenceJobs": bool(job_binary)}
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
        if configuration_edit:
            assert not policy
            headers = {"x-zitadel-orgid": org}
            def provider_call(path, body, method):
                req = Request(ISSUER + path, json.dumps(body).encode(),
                              {"Authorization": admin, "Content-Type": "application/json", **headers},
                              method=method)
                with opener.open(req, timeout=10) as response:
                    return json.load(response)
            settings = {
                "issuer": "https://example.invalid", "clientId": "verification",
                "clientSecret": secrets.token_urlsafe(32), "scopes": ["openid", "profile", "email"],
                "usePkce": True,
            }
            path = "/management/v1/idps/generic_oidc"
            provider = provider_call(path, {**settings, "name": "fixture-" + run_id}, "POST")["id"]
            edit = provider_call(path + "/" + provider, {**settings, "name": "edited-" + run_id,
                                                          "clientSecret": secrets.token_urlsafe(32)}, "PUT")
            read = Request(ISSUER + f"/v2/idps/{provider}", headers={"Authorization": admin, **headers})
            with opener.open(read, timeout=10) as response:
                assert json.load(response)["idp"]["name"] == "edited-" + run_id
            subject = org
        else:
            subject = machine("assigned")
            unrelated = machine("unassigned")
            for user in (unrelated, subject):
                api(f"/v2/users/{user}/deactivate", {})
        query = {"resourceOwner": org, "asc": True, "limit": 20, "aggregateId": subject}
        events = api("/admin/v1/events/_search", query, auth="Bearer " + pat)["events"]
        if configuration_edit:
            changed, = [event for event in events if event["type"]["type"] == "org.idp.oidc.changed"
                        and str(event["sequence"]) == str(edit["details"]["sequence"])]
            aggregate_type = changed["aggregate"]["type"]["type"]
            assert changed["aggregate"]["id"] == org and changed["aggregate"]["resourceOwner"] == org
        else:
            changed, = [event for event in events if event["type"]["type"] == "user.deactivated"]
        date = datetime.fromisoformat(changed["creationDate"].replace("Z", "+00:00"))
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
            assert event["target"]["id"] == subject and event["action"] == changed["type"]["type"]
            assert event["attributes"]["sequence"] == changed["sequence"]
            assert set(event["attributes"]) == {"issuer", "organizationId", "aggregateType", "sequence", "creationDate"}
            dispatch(lang, "retry", subject, micros, pat)
            check = (
                "SELECT state,attempts FROM heimdall.audit_deliveries "
                f"WHERE product_id='{product}' AND environment='{environment}' AND sink_id='siem'"
            )
            command = ["psql", delivery_env["HEIMDALL_TEST_DATABASE_URL"], "-At", "-v", "ON_ERROR_STOP=1", "-c", check]
            assert subprocess.run(command, capture_output=True, text=True, check=True).stdout.strip() == "delivered|3"
            es(f"/{index}/_refresh", method="POST")
            assert es(f"/{index}/_count")["count"] == 1
        if policy:
            checkpoints = {}
            for lang in scopes:
                dispatch(lang, "scheduled", subject, micros, pat)
                product, environment, tenant = scopes[lang]
                checkpoints[lang] = int(subprocess.check_output([
                    "psql", delivery_env["HEIMDALL_TEST_DATABASE_URL"], "-XAt", "-v", "ON_ERROR_STOP=1", "-c",
                    f"SELECT next_micros FROM heimdall_directory.event_checkpoints WHERE product_id='{product}' "
                    f"AND environment='{environment}' AND tenant_id='{tenant}'",
                ], text=True))
            api(f"/v2/users/{subject}/reactivate", {})
            time.sleep((policy["maxWriterTransactionMs"] + 3 * policy["maxClockOffsetMs"]) / 1000 + 1)
            for lang in scopes:
                dispatch(lang, "scheduled", subject, micros, pat, expected_events=1)
                product, environment, tenant = scopes[lang]
                assert int(subprocess.check_output([
                    "psql", delivery_env["HEIMDALL_TEST_DATABASE_URL"], "-XAt", "-v", "ON_ERROR_STOP=1", "-c",
                    f"SELECT next_micros FROM heimdall_directory.event_checkpoints WHERE product_id='{product}' "
                    f"AND environment='{environment}' AND tenant_id='{tenant}'",
                ], text=True)) > checkpoints[lang]
                if job_binary:
                    index = indexes[lang]
                    es(f"/{index}/_refresh", method="POST")
                    docs = es(f"/{index}/_search")["hits"]["hits"]
                    assert len(docs) == 2 and {doc["_source"]["action"] for doc in docs} == {
                        "user.deactivated", "user.reactivated",
                    }
        return {"run": run_id, "languages": ["typescript", "go"], "elasticsearch": "8.19.0",
                "liveProvider": True, "sinkOutageRetainedPending": True, "lostReceiptRecovered": True,
                "sinkRestartPreservedDocuments": True, "oneDocumentPerProduct": True, "tenantMismatchRejected": True, "scheduledCheckpointAdvanced": bool(policy),
                **({"referenceCollectorJobs": True} if policy and job_binary else {}),
                **({"configurationEdit": changed["type"]["type"], "aggregateType": aggregate_type,
                    "metadataOnly": True} if configuration_edit else {})}
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
                if org and not scim_event:
                    api("/v2/organizations/" + org, method="DELETE")

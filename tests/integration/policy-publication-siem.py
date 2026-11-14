"""Verify a live reference product's publication reaches its tenant Elasticsearch index."""
import base64
import hashlib
import json
import os
import re
import secrets
import subprocess
import sys
from concurrent.futures import ThreadPoolExecutor
from urllib.parse import urlsplit, urlunsplit
from uuid import uuid4
from wsgiref.simple_server import make_server

from live_identity_siem import ELASTIC, create_handler, request


def run(product, tenant, event_id, version, previous_version):
    config = json.loads(os.environ["HEIMDALL_CONFIG"])
    assert product in {"ledger", "support"} and tenant in {"acme", "globex"}
    assert config["productId"] == product and config["environment"] == "verification"
    auth = "Basic " + base64.b64encode(
        os.environ["HEIMDALL_TEST_ELASTICSEARCH_CREDENTIALS"].encode()
    ).decode()

    def elastic(path, body=None, method=None):
        status, result = request(ELASTIC, path, auth, body, method)
        assert 200 <= status < 300, f"Elasticsearch returned {status}"
        return result

    assert elastic("/")["version"]["number"] == "8.19.0"
    index = f"heimdall-policy-{product}-{uuid4()}"
    token = secrets.token_urlsafe(32)
    handler = create_handler(
        endpoint=ELASTIC, authorization=auth, index=index, product=product,
        environment="verification", sink="siem", token=token, tenant=tenant, loopback=True,
    )
    db_variable = product.upper() + "_DATABASE_URL"
    env = dict(os.environ)
    port = int(env["HEIMDALL_REFERENCE_POSTGRES_TUNNEL_PORT"])
    assert 1024 <= port <= 65535
    url = urlsplit(env[db_variable])
    assert url.hostname == "capability-postgres.railway.internal" and url.port in (5432, None)
    credentials, _, _ = url.netloc.rpartition("@")
    assert credentials
    env[db_variable] = urlunsplit((url.scheme, f"{credentials}@127.0.0.1:{port}", url.path, url.query, ""))
    role_dsn = env[f"HEIMDALL_REFERENCE_{product.upper()}_AUDIT_DATABASE"]
    role_url = urlsplit(role_dsn)
    assert role_url.hostname == "127.0.0.1" and role_url.port == port
    assert role_url.path == urlsplit(env["DATABASE_URL"]).path
    assert role_url.username != urlsplit(env["DATABASE_URL"]).username
    env["DATABASE_URL"] = role_dsn
    command = (
        ["node", "examples/ledger/audit-job.mjs"] if product == "ledger"
        else [env["HEIMDALL_REFERENCE_SUPPORT_BINARY"], "audit-job"]
    )
    identity = json.dumps([product, "verification", event_id], separators=(",", ":")).encode()
    document_id = hashlib.sha256(identity).hexdigest()
    doc_path = f"/{index}/_doc/{document_id}"
    elastic(
        f"/{index}",
        {"settings": {"number_of_replicas": 0, "index.translog.durability": "request"}},
        "PUT",
    )
    try:
        with make_server("127.0.0.1", 0, handler) as server, ThreadPoolExecutor(max_workers=1) as worker:
            serving = worker.submit(server.serve_forever)
            env.update(
                HEIMDALL_AUDIT_ENDPOINT=f"http://127.0.0.1:{server.server_port}",
                HEIMDALL_AUDIT_TENANT=tenant,
                HEIMDALL_AUDIT_SINK="siem",
                HEIMDALL_AUDIT_TOKEN=token,
                HEIMDALL_AUDIT_TRANSPORT_PROFILE="loopback",
            )
            try:
                for _ in range(30):
                    result = subprocess.run(command, env=env, capture_output=True, text=True, timeout=50)
                    if result.returncode:
                        code = re.search(r'"auditJob":"([a-z_]+)"', result.stderr)
                        raise RuntimeError(f"{product} audit job failed: {code[1] if code else result.returncode}")
                    summary = json.loads(result.stdout.splitlines()[-1])
                    assert summary["quarantined"] == 0
                    status, stored = request(ELASTIC, doc_path, auth)
                    if status == 200:
                        event = stored["_source"]
                        break
                    assert status == 404 and summary["attempted"] > 0, "Publication did not reach the index"
                else:
                    raise RuntimeError("Publication delivery exceeded the job budget")
                assert event["eventId"] == event_id and event["tenantId"] == tenant
                assert event["productId"] == product and event["environment"] == "verification"
                assert event["source"] == "product" and event["eventType"] == "product.sso.policy_published"
                assert event["attributes"] == {"version": version, "previousVersion": previous_version}
                foreign_tenant = "globex" if tenant == "acme" else "acme"
                status, _ = request(env["HEIMDALL_AUDIT_ENDPOINT"], "/", "Bearer " + token, {
                    "productId": product, "environment": "verification", "sinkId": "siem",
                    "eventIds": [event_id], "events": [{**event, "tenantId": foreign_tenant}],
                })
                assert status == 403, "Receiver accepted a foreign tenant"
                elastic(f"/{index}/_refresh", method="POST")
                result = elastic(f"/{index}/_count", {"query": {"ids": {"values": [document_id]}}}, "POST")
                assert result["count"] == 1
            finally:
                server.shutdown()
                serving.result(timeout=10)
        return {"indexed": True, "foreignTenantRejected": True, "eventId": event_id, "index": index}
    finally:
        elastic(f"/{index}", method="DELETE")


if __name__ == "__main__":
    print(json.dumps(run(*sys.argv[1:])))

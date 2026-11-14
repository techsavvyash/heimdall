"""Run against disposable Elasticsearch 8.19 with index creation privileges."""

import base64
from concurrent.futures import ThreadPoolExecutor
import hashlib
import json
import os
from pathlib import Path
import secrets
import sys
from urllib.error import HTTPError
from urllib.request import Request, build_opener
from uuid import uuid4
from wsgiref.simple_server import make_server

sys.path.insert(0, str(Path(__file__).resolve().parents[2] / "examples" / "audit"))
from elasticsearch_receipt import NoRedirect, create_handler

opener = build_opener(NoRedirect())

endpoint = os.environ["HEIMDALL_TEST_ELASTICSEARCH_URL"].rstrip("/")
credentials = os.environ["HEIMDALL_TEST_ELASTICSEARCH_CREDENTIALS"]
authorization = "Basic " + base64.b64encode(credentials.encode()).decode()
run = str(uuid4())
index = "heimdall-audit-" + run
token = secrets.token_urlsafe(32)
scope = {"productId": "ledger", "environment": "test", "sinkId": "siem"}
event = {
    "schemaVersion": 1, "eventId": "identity-event", "eventType": "heimdall.identity.observed",
    "eventVersion": 1, "occurredAt": 1794528000000, "recordedAt": 1794528000001,
    "productId": "ledger", "environment": "test", "tenantId": "acme",
    "source": "zitadel", "actor": {"kind": "unknown"}, "sdkVersion": "0.0.0",
    "target": {"type": "zitadel_aggregate", "id": "user-1"}, "action": "user.deactivated",
    "outcome": "succeeded", "requestId": "identity-event",
    "attributes": {"sequence": "18446744073709551615"},
}
batch = {**scope, "eventIds": [event["eventId"]], "events": [event]}


def request(url, *, method="GET", payload=None, auth=authorization):
    body = json.dumps(payload).encode() if payload is not None else None
    req = Request(url, data=body, method=method, headers={
        "Authorization": auth, "Content-Type": "application/json",
    })
    try:
        response = opener.open(req, timeout=10)
    except HTTPError as error:
        response = error
    with response:
        return response.status, json.load(response)


def elastic(path, **kwargs):
    return request(endpoint + path, **kwargs)


created = False
try:
    status, info = elastic("/")
    assert status == 200 and info["version"]["number"] == "8.19.0"
    status, _ = elastic("/" + index, method="PUT", payload={
        "settings": {"index.translog.durability": "request", "number_of_replicas": 0},
    })
    assert status == 200
    created = True
    handler = create_handler(endpoint=endpoint, authorization=authorization, index=index,
                             product="ledger", environment="test", sink="siem", token=token, loopback=True)
    with make_server("127.0.0.1", 0, handler) as server, ThreadPoolExecutor(max_workers=1) as executor:
        serving = executor.submit(server.serve_forever)
        url = f"http://127.0.0.1:{server.server_port}"
        try:
            def submit(payload, auth="Bearer " + token):
                return request(url, method="POST", payload=payload, auth=auth)

            assert submit(batch, "Bearer invalid")[0] == 401
            assert submit({**batch, "productId": "support"})[0] == 403
            assert submit({**batch, "events": [{**event, "productId": "support"}]})[0] == 403
            receipt = {**scope, "eventIds": [event["eventId"]]}
            assert submit(batch) == (200, receipt)
            assert submit(batch) == (200, receipt)
            changed = {**event, "tenantId": "other"}
            healthy = {**event, "eventId": "product-event", "eventType": "product.invoice.approved",
                       "source": "product", "target": {"type": "invoice", "id": "invoice-1"}}
            assert submit({**batch, "events": [changed]})[0] == 503
            assert submit({**scope, "eventIds": [event["eventId"], healthy["eventId"]],
                           "events": [changed, healthy]}) == (200, {**scope, "eventIds": ["product-event"]})
        finally:
            server.shutdown()
            serving.result(timeout=10)
    assert elastic(f"/{index}/_refresh", method="POST")[0] == 200
    assert elastic(f"/{index}/_count")[1]["count"] == 2
    key = hashlib.sha256(json.dumps(["ledger", "test", event["eventId"]], separators=(",", ":")).encode()).hexdigest()
    assert elastic(f"/{index}/_doc/{key}")[1]["_source"] == event
    print(json.dumps({"run": run, "elasticsearch": info["version"]["number"],
                      "scopedReplay": True, "changedTenantRejected": True,
                      "partialReceipt": True, "storedDocuments": 2}))
finally:
    if created:
        assert elastic("/" + index, method="DELETE")[0] == 200

"""Product-owned WSGI handler for Heimdall receipts backed by a concrete Elasticsearch index."""

import hashlib
import hmac
import json
import re
from time import monotonic
from urllib.error import HTTPError
from urllib.request import HTTPRedirectHandler, Request, build_opener
from urllib.parse import urlsplit

MAX_BODY = 5 * 1024 * 1024
TOKEN = re.compile(r"[A-Za-z0-9_-]{1,128}")


class NoRedirect(HTTPRedirectHandler):
    def redirect_request(self, *_):
        return None


def create_handler(*, endpoint, authorization, index, product, environment, sink, token, tenant=None, loopback=False):
    """Mount at a product-owned HTTPS route. Use one precreated index per authorized scope."""
    if not re.fullmatch(r"[a-z0-9][a-z0-9_-]{0,127}", index):
        raise ValueError("invalid concrete index name")
    url = urlsplit(endpoint)
    if (not url.hostname or url.username or url.password or url.query or url.fragment
            or url.path not in ("", "/")
            or (url.scheme != "https" and not (loopback and url.scheme == "http" and url.hostname == "127.0.0.1"))):
        raise ValueError("invalid Elasticsearch origin")
    binding = {"productId": product, "environment": environment, "sinkId": sink}
    opener = build_opener(NoRedirect())

    def elastic(path, deadline, body=None, method=None):
        remaining = deadline - monotonic()
        if remaining <= 0:
            raise TimeoutError()
        request = Request(endpoint.rstrip("/") + path, data=body, method=method, headers={
            "Authorization": authorization, "Content-Type": "application/json",
        })
        try:
            response = opener.open(request, timeout=remaining)
        except HTTPError as error:
            response = error
        with response:
            raw = response.read(MAX_BODY + 1)
            if len(raw) > MAX_BODY:
                raise ValueError("oversized Elasticsearch response")
            return response.code, json.loads(raw)

    def handle(environ, respond):
        def reply(status, payload=None):
            respond(status, [("Content-Type", "application/json")])
            return [json.dumps(payload).encode()]

        if environ.get("REQUEST_METHOD") != "POST":
            return reply("405 Method Not Allowed")
        incoming = environ.get("HTTP_AUTHORIZATION", "").encode()
        if not hmac.compare_digest(incoming, ("Bearer " + token).encode()):
            return reply("401 Unauthorized")
        try:
            size = int(environ.get("CONTENT_LENGTH") or 0)
            if not 0 < size <= MAX_BODY:
                return reply("413 Content Too Large")
            batch = json.loads(environ["wsgi.input"].read(size))
            if not isinstance(batch, dict) or any(batch.get(k) != v for k, v in binding.items()):
                return reply("403 Forbidden")
            ids, events = batch["eventIds"], batch["events"]
            if (not isinstance(ids, list) or not isinstance(events, list)
                    or not 0 < len(ids) == len(events) <= 100
                    or any(not isinstance(e, str) or not TOKEN.fullmatch(e) for e in ids)
                    or len(set(ids)) != len(ids)):
                return reply("400 Bad Request")
            for event_id, event in zip(ids, events):
                if (not isinstance(event, dict) or event.get("eventId") != event_id
                        or event.get("productId") != product or event.get("environment") != environment
                        or (tenant is not None and event.get("tenantId") != tenant)):
                    return reply("403 Forbidden")
        except (KeyError, ValueError, TypeError):
            return reply("400 Bad Request")

        acknowledged = []
        replays = []
        deadline = monotonic() + 3
        for event_id, event in zip(ids, events):
            try:
                body = json.dumps(event, sort_keys=True, separators=(",", ":"), allow_nan=False).encode()
                identity = json.dumps([product, environment, event_id], separators=(",", ":")).encode()
                doc = hashlib.sha256(identity).hexdigest()
                status, result = elastic(f"/{index}/_create/{doc}", deadline, body)
                if status == 201 and result.get("_index") == index and result.get("_id") == doc:
                    acknowledged.append(event_id)
                elif status == 409:
                    status, stored = elastic(f"/{index}/_doc/{doc}", deadline)
                    if (status == 200 and stored.get("_index") == index and stored.get("_id") == doc
                            and json.dumps(stored.get("_source"), sort_keys=True, separators=(",", ":")).encode() == body):
                        replays.append(event_id)
            except (OSError, ValueError, AttributeError):
                break
        if replays:
            try:
                # A real-time GET can observe a concurrent write before its success response.
                status, flushed = elastic(f"/{index}/_flush?force=true&wait_if_ongoing=true", deadline, method="POST")
                shards = flushed.get("_shards", {})
                if (status == 200 and shards.get("failed") == 0
                        and shards.get("successful", 0) > 0
                        and shards.get("successful") == shards.get("total")):
                    acknowledged.extend(replays)
            except (OSError, ValueError, AttributeError):
                pass
        if not acknowledged:
            return reply("503 Service Unavailable")
        return reply("200 OK", {**binding, "eventIds": acknowledged})

    return handle

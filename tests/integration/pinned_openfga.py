"""Disposable pinned OpenFGA for the public SDK administration matrix."""
from contextlib import contextmanager
import json
import os
import secrets
import subprocess
import time
import urllib.error
import urllib.request
import uuid


@contextmanager
def pinned_openfga(root):
    project = "heimdall-admin-" + uuid.uuid4().hex[:12]
    key = secrets.token_hex(32)
    env = dict(os.environ, HEIMDALL_PROBE_POSTGRES_PASSWORD=secrets.token_hex(32),
               HEIMDALL_PROBE_OPENFGA_KEY=key)
    command = ["docker", "compose", "-p", project, "-f", "deploy/verification/compose.yaml",
               "-f", "deploy/verification/recovery.compose.yaml"]
    url = "http://127.0.0.1:8080"

    def compose(*args):
        return subprocess.check_output(command + list(args), env=env, cwd=root, text=True, timeout=120)

    def request(path, body=None):
        req = urllib.request.Request(url + path, data=json.dumps(body).encode() if body is not None else None,
                                     headers={"Authorization": "Bearer " + key, "Content-Type": "application/json"})
        with urllib.request.urlopen(req, timeout=5) as response:
            return json.load(response)

    try:
        compose("up", "-d", "--wait", "postgres")
        compose("run", "--rm", "migrate")
        compose("up", "-d", "openfga")
        for _ in range(60):
            try:
                request("/healthz")
                break
            except (OSError, urllib.error.URLError):
                time.sleep(0.25)
        else:
            raise RuntimeError("Pinned OpenFGA readiness deadline")
        fixture = json.loads((root / "contracts/fixtures/tenant-model-v1.json").read_text())
        model = next(item["expected"]["model"] for item in fixture if item["name"] == "ledger")
        scopes = {}
        for language in ("ts", "go"):
            store = request("/stores", {"name": project + "-" + language})["id"]
            model_id = request(f"/stores/{store}/authorization-models", model)["authorization_model_id"]
            scopes[language] = {"url": url, "storeId": store, "modelId": model_id}
        yield dict(HEIMDALL_TEST_PINNED_OPENFGA=json.dumps(scopes), HEIMDALL_TEST_PINNED_OPENFGA_KEY=key)
    finally:
        compose("down", "--volumes", "--remove-orphans")

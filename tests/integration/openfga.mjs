import assert from "node:assert/strict";
import { randomUUID } from "node:crypto";

const base = process.env.HEIMDALL_PROBE_OPENFGA_URL;
const key = process.env.HEIMDALL_PROBE_OPENFGA_KEY;
assert(base && key, "Set HEIMDALL_PROBE_OPENFGA_URL and HEIMDALL_PROBE_OPENFGA_KEY");
const endpoint = new URL(base);
assert(
  endpoint.protocol === "https:" ||
    (endpoint.protocol === "http:" &&
      ["localhost", "127.0.0.1", "[::1]"].includes(endpoint.hostname)),
  "Use HTTPS or explicit local loopback",
);
assert(!endpoint.username && !endpoint.password && !endpoint.search && !endpoint.hash);
const runId = randomUUID();
console.error(`OpenFGA probe run: ${runId}`);
const created = [];
const observations = {};
async function request(path, body, method = body === undefined ? "GET" : "POST", credential = key) {
  const response = await fetch(`${base.replace(/\/$/, "")}${path}`, {
    method,
    headers: {
      "content-type": "application/json",
      ...(credential ? { authorization: `Bearer ${credential}` } : {}),
    },
    ...(body === undefined ? {} : { body: JSON.stringify(body) }),
    signal: AbortSignal.timeout(15000),
    redirect: "error",
  });
  const text = await response.text();
  return { status: response.status, body: text ? JSON.parse(text) : null };
}
async function ok(path, body, method) {
  const result = await request(path, body, method);
  assert(
    result.status >= 200 && result.status < 300,
    `${method ?? "request"} ${path}: HTTP ${result.status}`,
  );
  return result.body;
}
const model = {
  schema_version: "1.1",
  type_definitions: [
    { type: "user" },
    {
      type: "workspace",
      relations: {
        member: { this: {} },
        approver: { this: {} },
        can_approve: {
          intersection: {
            child: [
              { computedUserset: { relation: "member" } },
              { computedUserset: { relation: "approver" } },
            ],
          },
        },
      },
      metadata: {
        relations: {
          member: { directly_related_user_types: [{ type: "user" }] },
          approver: { directly_related_user_types: [{ type: "user" }] },
        },
      },
    },
  ],
};
const tuple = (workspace, relation = "member") => ({
  user: "user:maya",
  relation,
  object: `workspace:${workspace}`,
});
try {
  assert.equal((await request("/stores", undefined, "GET", "")).status, 401);
  assert.equal((await request("/stores", undefined, "GET", "invalid-probe-key")).status, 401);
  observations.unauthenticatedAndInvalidKey = "401";
  for (const product of ["ledger", "support"]) {
    const store = await ok("/stores", { name: `heimdall-probe-${product}-${runId}` });
    created.push(store.id);
  }
  const [ledger, support] = created;
  const ids = [];
  for (const store of created) {
    const result = await ok(`/stores/${store}/authorization-models`, model);
    ids.push(result.authorization_model_id);
  }
  await ok(`/stores/${ledger}/write`, {
    authorization_model_id: ids[0],
    writes: {
      tuple_keys: [
        tuple("acme"),
        tuple("acme", "approver"),
        { user: "user:noah", relation: "member", object: "workspace:globex" },
      ],
    },
  });
  await ok(`/stores/${support}/write`, {
    authorization_model_id: ids[1],
    writes: {
      tuple_keys: [
        tuple("globex"),
        { user: "user:noah", relation: "member", object: "workspace:acme" },
      ],
    },
  });
  const check = async (store, modelId, workspace, relation) =>
    (
      await ok(`/stores/${store}/check`, {
        authorization_model_id: modelId,
        tuple_key: tuple(workspace, relation),
        consistency: "HIGHER_CONSISTENCY",
      })
    ).allowed;
  assert.equal(await check(ledger, ids[0], "acme", "can_approve"), true);
  assert.equal(await check(support, ids[1], "acme", "member"), false);
  assert.equal(await check(support, ids[1], "globex", "member"), true);
  observations.twoProductsFourWorkspaceObjects = "isolated relationship results";
  for (const [i, store] of created.entries()) {
    const listed = await ok(`/stores/${store}/list-objects`, {
      authorization_model_id: ids[i],
      type: "workspace",
      relation: "member",
      user: "user:maya",
      consistency: "HIGHER_CONSISTENCY",
    });
    assert.deepEqual(listed.objects, [`workspace:${i === 0 ? "acme" : "globex"}`]);
  }
  const page = await ok(`/stores/${ledger}/read`, { page_size: 1 });
  assert.equal(page.tuples.length, 1);
  assert(page.continuation_token);
  const next = await ok(`/stores/${ledger}/read`, {
    page_size: 1,
    continuation_token: page.continuation_token,
  });
  assert.equal(next.tuples.length, 1);
  observations.listing = "membership ListObjects and Read page_size=1 continuation verified";
  await ok(`/stores/${ledger}/write`, {
    authorization_model_id: ids[0],
    deletes: { tuple_keys: [tuple("acme", "approver")] },
  });
  assert.equal(await check(ledger, ids[0], "acme", "can_approve"), false);
  observations.postRevocation = "immediate HIGHER_CONSISTENCY check denied";
  assert.equal((await request(`/stores/${support}`)).status, 200);
  observations.credentialBoundary =
    "preshared key has deployment-wide access to both stores; no per-store credential isolation";
} finally {
  const cleanup = await Promise.allSettled(
    created.map((id) => ok(`/stores/${id}`, undefined, "DELETE")),
  );
  const failed = cleanup.filter((result) => result.status === "rejected");
  assert.equal(
    failed.length,
    0,
    `Cleanup failed for ${failed.length} stores in run ${runId}; inspect this run's resources`,
  );
}
console.log(
  JSON.stringify(
    {
      suite: "openfga-capabilities-v1",
      runId,
      observedAt: new Date().toISOString(),
      observations,
      cleanedUpStores: created,
    },
    null,
    2,
  ),
);

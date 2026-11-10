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
  // More than the pinned default ListObjects maximum; these are direct memberships.
  // Keep the existing two-product checks independent of this larger fixture.
  const many = Array.from({ length: 1001 }, (_, i) => ({
    user: "user:listing-probe",
    relation: "member",
    object: `workspace:listing-${i}`,
  }));
  const tooLarge = await request(`/stores/${ledger}/write`, {
    authorization_model_id: ids[0],
    writes: { tuple_keys: many.slice(0, 101) },
  });
  assert.equal(tooLarge.status, 400, "Pinned deployment must reject 101-tuple writes");
  const afterRejected = await ok(`/stores/${ledger}/read`, {
    tuple_key: { user: "user:listing-probe", relation: "member", object: "workspace:" },
    page_size: 100,
    consistency: "HIGHER_CONSISTENCY",
  });
  assert.equal(afterRejected.tuples.length, 0, "Rejected oversized write must not partially apply");
  for (let i = 0; i < many.length; i += 100) {
    await ok(`/stores/${ledger}/write`, {
      authorization_model_id: ids[0],
      writes: { tuple_keys: many.slice(i, i + 100) },
    });
  }
  const listStarted = performance.now();
  const limited = await ok(`/stores/${ledger}/list-objects`, {
    authorization_model_id: ids[0],
    type: "workspace",
    relation: "member",
    user: "user:listing-probe",
    consistency: "HIGHER_CONSISTENCY",
  });
  const listElapsedMs = Math.round(performance.now() - listStarted);
  const expected = new Set(many.map((t) => t.object));
  assert.equal(limited.objects.length, 1000, "Pinned default ListObjects cap changed");
  assert.equal(new Set(limited.objects).size, 1000);
  assert(limited.objects.every((object) => expected.has(object)));
  assert(!limited.continuation_token, "Unexpected ListObjects pagination capability");
  const seen = new Set();
  const tokens = new Set();
  let continuation;
  let pages = 0;
  const readStarted = performance.now();
  do {
    assert(pages < 20, "Tuple scan must terminate within bounded pages");
    const result = await ok(`/stores/${ledger}/read`, {
      tuple_key: { user: "user:listing-probe", relation: "member", object: "workspace:" },
      page_size: 100,
      consistency: "HIGHER_CONSISTENCY",
      ...(continuation ? { continuation_token: continuation } : {}),
    });
    pages++;
    assert(result.tuples.length <= 100);
    for (const row of result.tuples) {
      assert.equal(row.key.user, "user:listing-probe");
      assert.equal(row.key.relation, "member");
      assert(expected.has(row.key.object));
      assert(!seen.has(row.key.object), "Repeated tuple in stable paginated scan");
      seen.add(row.key.object);
    }
    continuation = result.continuation_token;
    if (continuation) {
      assert(!tokens.has(continuation), "Repeated continuation token");
      tokens.add(continuation);
    }
  } while (continuation);
  assert.deepEqual(seen, expected);
  observations.listingLimits = {
    writtenMemberships: many.length,
    maxTuplesPerAcceptedWrite: 100,
    oversizedWriteStatus: tooLarge.status,
    rejectedWritePartiallyApplied: false,
    listObjectsCount: limited.objects.length,
    listObjectsContinuation: false,
    listObjectsElapsedMs: listElapsedMs,
    readPageSize: 100,
    readPages: pages,
    readDistinctMemberships: seen.size,
    readElapsedMs: Math.round(performance.now() - readStarted),
    scope: "stable direct-user tuples; no concurrent writes or inherited membership",
  };

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

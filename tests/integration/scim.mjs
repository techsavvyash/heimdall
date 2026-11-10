import assert from "node:assert/strict";
import { randomUUID } from "node:crypto";

const issuer = process.env.HEIMDALL_PROBE_ZITADEL_ISSUER;
const pat = process.env.HEIMDALL_PROBE_ZITADEL_PAT;
assert(issuer && pat, "Set the verification issuer and bootstrap-only PAT");
const url = new URL(issuer);
assert(url.protocol === "https:" && url.origin === issuer);
const runId = randomUUID();
console.error(`SCIM probe run: ${runId}`);
const orgs = [];
const observations = [];
async function request(method, path, body, credential = pat) {
  const r = await fetch(`${issuer}${path}`, {
    method,
    headers: {
      "content-type": path.startsWith("/scim/") ? "application/scim+json" : "application/json",
      ...(credential ? { authorization: `Bearer ${credential}` } : {}),
    },
    ...(body === undefined ? {} : { body: JSON.stringify(body) }),
    redirect: "error",
    signal: AbortSignal.timeout(15000),
  });
  const text = await r.text();
  return { status: r.status, body: text ? JSON.parse(text) : null };
}
async function ok(method, path, body) {
  const r = await request(method, path, body);
  assert(
    r.status >= 200 && r.status < 300,
    `${method} ${path}: HTTP ${r.status}, code ${r.body?.code ?? r.body?.scimType ?? "unknown"}`,
  );
  return r.body;
}
async function stateWithin(userId, expected) {
  const start = performance.now();
  while (performance.now() - start < 60000) {
    const r = await request("GET", `/v2/users/${userId}`);
    if (
      expected === "deleted" ? r.status === 404 : r.status === 200 && r.body.user.state === expected
    ) {
      const elapsed = performance.now() - start;
      assert(elapsed < 60000, "Authoritative state observation exceeded 60 seconds");
      return Math.round(elapsed);
    }
    assert([200, 404].includes(r.status), `Identity read HTTP ${r.status}`);
    await new Promise((resolve) => setTimeout(resolve, 250));
  }
  assert.fail(`Identity state ${expected} not observed within 60 seconds`);
}
try {
  for (const tenant of ["acme", "globex"]) {
    const org = await ok("POST", "/v2/organizations", { name: `heimdall-scim-${tenant}-${runId}` });
    assert(org.organizationId);
    orgs.push(org.organizationId);
  }
  const paths = orgs.map((id) => `/scim/v2/${id}/Users`);
  assert.equal((await request("GET", paths[0], undefined, "")).status, 401);
  const users = [];
  for (const [index, path] of paths.entries()) {
    const user = await ok("POST", path, {
      schemas: ["urn:ietf:params:scim:schemas:core:2.0:User"],
      externalId: `employee-${runId}`,
      userName: `employee-${index}-${runId}@example.invalid`,
      name: { givenName: "Heimdall", familyName: "Probe" },
      emails: [{ value: `employee-${runId}@example.invalid`, primary: true }],
      active: true,
    });
    assert(user.id);
    users.push(user.id);
    assert.equal((await ok("GET", `${path}/${user.id}`)).externalId, `employee-${runId}`);
  }
  assert.notEqual(users[0], users[1]);
  const duplicate = await request("POST", paths[1], {
    schemas: ["urn:ietf:params:scim:schemas:core:2.0:User"],
    userName: `employee-0-${runId}@example.invalid`,
    name: { givenName: "Heimdall", familyName: "Duplicate" },
    emails: [{ value: `duplicate-${runId}@example.invalid`, primary: true }],
    active: true,
  });
  assert.equal(duplicate.status, 409);
  assert.equal(duplicate.body.scimType, "uniqueness");
  assert.equal((await request("GET", `${paths[1]}/${users[0]}`)).status, 404);
  const list = await ok(
    "GET",
    `${paths[0]}?count=1&filter=${encodeURIComponent(`externalId eq "employee-${runId}"`)}`,
  );
  assert.equal(list.Resources.length, 1);
  assert.equal(list.Resources[0].id, users[0]);
  for (const active of [false, false, true, false]) {
    const start = performance.now();
    await ok("PATCH", `${paths[0]}/${users[0]}`, {
      schemas: ["urn:ietf:params:scim:api:messages:2.0:PatchOp"],
      Operations: [{ op: "replace", path: "active", value: active }],
    });
    assert.equal((await ok("GET", `${paths[0]}/${users[0]}`)).active, active);
    const readMs = await stateWithin(
      users[0],
      active ? "USER_STATE_ACTIVE" : "USER_STATE_INACTIVE",
    );
    observations.push({
      active,
      authoritativeReadMs: readMs,
      mutationAndObservationMs: Math.round(performance.now() - start),
    });
  }
  await ok("DELETE", `${paths[0]}/${users[0]}`);
  assert.equal((await request("GET", `${paths[0]}/${users[0]}`)).status, 404);
  observations.push({ deleted: true, authoritativeReadMs: await stateWithin(users[0], "deleted") });
  assert.equal((await ok("GET", `${paths[1]}/${users[1]}`)).active, true);
} finally {
  const results = await Promise.allSettled(
    orgs.map((organizationId) => ok("DELETE", `/v2/organizations/${organizationId}`)),
  );
  assert(
    results.every((r) => r.status === "fulfilled"),
    `Organization cleanup failed for run ${runId}`,
  );
}
console.log(
  JSON.stringify(
    {
      suite: "zitadel-scim-users-v1",
      runId,
      observedAt: new Date().toISOString(),
      issuer,
      observations,
      cleanedUpOrganizations: orgs,
      limitations: [
        "bootstrap credential; least-privilege SCIM credential not yet tested",
        "single sequential sample; not a latency guarantee",
        "no SDK workers, sessions or tuple reconciliation tested",
        "cross-organization resource lookup checked; hostile credential isolation not inferred",
      ],
    },
    null,
    2,
  ),
);

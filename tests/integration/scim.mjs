import assert from "node:assert/strict";
import { randomUUID } from "node:crypto";
import { spawnSync } from "node:child_process";
import { probeLifecycleEvents } from "./lifecycle-events.mjs";

const issuer = process.env.HEIMDALL_PROBE_ZITADEL_ISSUER;
const pat = process.env.HEIMDALL_PROBE_ZITADEL_PAT;
assert(issuer && pat, "Set the verification issuer and bootstrap-only PAT");
const url = new URL(issuer);
assert(url.protocol === "https:" && url.origin === issuer);
const runId = randomUUID();
console.error(`SCIM probe run: ${runId}`);
const orgs = [];
const observations = [];
const scopedTokens = new Map();
const credentialChecks = [];
const scimUserPath = (organizationId) => `/scim/v2/${organizationId}/Users`;
const useSDK = process.env.HEIMDALL_PROBE_SCIM_SDK === "true";
async function checkDirectorySDK(options, subject, state, externalId) {
  if (!useSDK) return;
  const { createDirectoryReader } = await import("../../sdk/typescript/dist/provisioning/index.js");
  const reader = createDirectoryReader(options);
  const signal = AbortSignal.timeout(15000);
  const observed = await reader.read(subject, signal);
  assert.equal(observed.state, state);
  assert.equal(observed.externalId, externalId);
  const page = await reader.list(1, 1, signal);
  if (state === "active") {
    assert.equal(page.nextStartIndex, 2);
    assert.deepEqual(page.users, [{ id: subject, externalId }]);
  }
  if (state === "missing") assert.deepEqual(page, { users: [], nextStartIndex: null });
  const go = spawnSync(
    "go",
    ["test", "-race", "-count=1", "./provisioning", "-run", "^TestLiveDirectoryReader$"],
    {
      cwd: new URL("../../sdk/go/", import.meta.url),
      env: {
        ...process.env,
        HEIMDALL_TEST_DIRECTORY_READER: JSON.stringify({ ...options, subject, state, externalId }),
      },
      encoding: "utf8",
      timeout: 60000,
    },
  );
  if (go.status !== 0)
    console.error(
      go.stdout?.match(
        /(?:unexpected live identity result|live listing failed)(?: (?:unavailable|conflict|<nil>))?/,
      )?.[0] ?? "Go directory read failed",
    );
  assert.equal(go.status, 0);
  observations.push({ sdkDirectoryRead: { state, matched: ["typescript", "go"] } });
}
function runLiveWorker(reader, subject, stage) {
  if (process.env.HEIMDALL_PROBE_SCIM_WORKER !== "true") return;
  const begin = performance.now();
  const env = {
    ...process.env,
    HEIMDALL_TEST_LIVE_SCIM: JSON.stringify({ ...reader, subject, stage }),
  };
  for (const [command, args, cwd] of [
    [
      "pnpm",
      ["--filter", "@heimdall/sdk", "exec", "vitest", "run", "test/live-scim-worker.test.ts"],
      new URL("../../", import.meta.url),
    ],
    [
      "go",
      ["test", "-race", "-count=1", "./postgres", "-run", "^TestLiveSCIMWorker$"],
      new URL("../../sdk/go/", import.meta.url),
    ],
  ]) {
    const result = spawnSync(command, args, { env, cwd, encoding: "utf8", timeout: 45000 });
    if (result.status !== 0) console.error(command, stage, "live worker failed");
    assert.equal(result.status, 0, "Live SCIM worker failed");
  }
  observations.push({
    liveWorker: {
      stage,
      matched: ["typescript", "go"],
      workerMs: Math.round(performance.now() - begin),
    },
  });
}
async function request(method, path, body, credential) {
  credential ??=
    (path.startsWith("/scim/") ? scopedTokens.get(path.split("/")[3]) : undefined) ?? pat;
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
async function ok(method, path, body, credential) {
  const r = await request(method, path, body, credential);
  assert(
    r.status >= 200 && r.status < 300,
    `${method} ${path}: HTTP ${r.status}, code ${r.body?.code ?? r.body?.scimType ?? "unknown"}`,
  );
  return r.body;
}
async function stateWithin(userId, expected, credential) {
  const start = performance.now();
  while (performance.now() - start < 60000) {
    const r = await request("GET", `/v2/users/${userId}`, undefined, credential);
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
    const machine = await ok("POST", "/v2/users/new", {
      organizationId: org.organizationId,
      username: `directory-${tenant}-${runId}`,
      machine: { name: `SCIM ${tenant} capability probe` },
      metadata: [
        {
          key: "urn:zitadel:scim:provisioningDomain",
          value: Buffer.from(`directory-${tenant}`).toString("base64"),
        },
      ],
    });
    assert(machine.id);
    await ok(
      "POST",
      "/zitadel.internal_permission.v2.InternalPermissionService/CreateAdministrator",
      {
        userId: machine.id,
        resource: { organizationId: org.organizationId },
        roles: ["ORG_HEIMDALL_SCIM"],
      },
    );
    const token = await ok("POST", `/v2/users/${machine.id}/pats`, {
      expirationDate: new Date(Date.now() + 3600000).toISOString(),
    });
    assert(token.token);
    scopedTokens.set(org.organizationId, token.token);
  }
  const paths = orgs.map(scimUserPath);
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
  const acmeToken = scopedTokens.get(orgs[0]);
  let readerToken = acmeToken;
  for (const [method, path, body] of [
    ["GET", `${paths[1]}/${users[1]}`],
    [
      "PATCH",
      `${paths[1]}/${users[1]}`,
      {
        schemas: ["urn:ietf:params:scim:api:messages:2.0:PatchOp"],
        Operations: [{ op: "replace", path: "active", value: false }],
      },
    ],
    ["DELETE", `${paths[1]}/${users[1]}`],
    [
      "POST",
      "/zitadel.internal_permission.v2.InternalPermissionService/CreateAdministrator",
      { userId: users[0], resource: { organizationId: orgs[0] }, roles: ["ORG_OWNER"] },
    ],
  ]) {
    const result = await request(method, path, body, acmeToken);
    assert(
      [403, 404].includes(result.status),
      `Scoped credential unexpectedly permitted ${method} ${path}: ${result.status}`,
    );
    credentialChecks.push({
      method,
      target: path.includes(orgs[1]) ? "other organization user" : "administrator grant",
      status: result.status,
    });
  }
  const crossApi = await request("GET", `/v2/users/${users[1]}`, undefined, acmeToken);
  assert(
    [403, 404].includes(crossApi.status),
    "Scoped role must not read another organization through user API",
  );
  credentialChecks.push({
    target: "other organization user through v2 user API",
    status: crossApi.status,
  });
  const bootstrapRead = await ok("GET", `${paths[0]}/${users[0]}`, undefined, pat);
  assert(!bootstrapRead.externalId, "External ID must use the caller's provisioning domain");
  credentialChecks.push({
    provisioningDomain: "directory external ID not exposed through unconfigured bootstrap domain",
  });
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
  const otherDirectory = await ok("GET", paths[0], undefined, pat);
  assert(otherDirectory.Resources.some((user) => user.id === users[0]));
  const listed = await ok("GET", paths[0] + "?startIndex=1&count=10", undefined, acmeToken);
  const own = listed.Resources.find((user) => user.id === users[0]);
  assert.equal(own?.externalId, `employee-${runId}`);
  assert(!listed.Resources.some((user) => user.id === users[1]));
  const read = await ok("GET", `/v2/users/${users[0]}`, undefined, acmeToken);
  assert.equal(read.user.userId, users[0]);
  assert.equal(read.user.details.resourceOwner, orgs[0]);
  observations.push({
    directoryList: {
      scopedCredentialFindsOwnUser: true,
      scopedCredentialOmitsOtherOrganization: true,
      authoritativeUserReadConfirmsOwner: true,
      unfilteredListIncludesUserOutsideCallerProvisioningDomain: true,
    },
  });
  if (useSDK || process.env.HEIMDALL_PROBE_SCIM_READER === "true") {
    const viewer = await ok("POST", "/v2/users/new", {
      organizationId: orgs[0],
      username: `scim-reader-${runId}`,
      machine: { name: "Disposable SCIM directory reader" },
      metadata: [
        {
          key: "urn:zitadel:scim:provisioningDomain",
          value: Buffer.from("directory-acme").toString("base64"),
        },
      ],
    });
    await ok(
      "POST",
      "/zitadel.internal_permission.v2.InternalPermissionService/CreateAdministrator",
      {
        userId: viewer.id,
        resource: { organizationId: orgs[0] },
        roles: ["ORG_OWNER_VIEWER"],
      },
    );
    const { token } = await ok("POST", `/v2/users/${viewer.id}/pats`, {
      expirationDate: new Date(Date.now() + 3600000).toISOString(),
    });
    readerToken = token;
    const ownList = await ok("GET", paths[0] + "?startIndex=1&count=10", undefined, token);
    assert.equal(
      ownList.Resources.find((user) => user.id === users[0])?.externalId,
      `employee-${runId}`,
    );
    const ownUser = await ok("GET", `/v2/users/${users[0]}`, undefined, token);
    assert.equal(ownUser.user.details.resourceOwner, orgs[0]);
    const otherUser = await request("GET", `/v2/users/${users[1]}`, undefined, token);
    assert([403, 404].includes(otherUser.status));
    const otherList = await request("GET", paths[1], undefined, token);
    assert([403, 404].includes(otherList.status));
    const patch = await request(
      "PATCH",
      `${paths[0]}/${users[0]}`,
      {
        schemas: ["urn:ietf:params:scim:api:messages:2.0:PatchOp"],
        Operations: [{ op: "replace", path: "active", value: false }],
      },
      token,
    );
    assert([403, 404].includes(patch.status));
    assert.equal((await ok("GET", `${paths[0]}/${users[0]}`)).active, true);
    observations.push({
      organizationViewer: {
        directoryExternalIdVisible: true,
        ownUserOrganizationVerified: true,
        otherUserReadStatus: otherUser.status,
        otherListStatus: otherList.status,
        ownMutationStatus: patch.status,
      },
    });
  }
  const reader = { issuer, organizationId: orgs[0], token: readerToken };
  await checkDirectorySDK(reader, users[0], "active", `employee-${runId}`);
  runLiveWorker(reader, users[0], "seed");
  let inactiveReadChecked = false;
  let workerDisabled = false;
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
      acmeToken,
    );
    observations.push({
      active,
      authoritativeReadMs: readMs,
      mutationAndObservationMs: Math.round(performance.now() - start),
    });
    if (!active && !workerDisabled) {
      runLiveWorker(reader, users[0], "inactive");
      if (process.env.HEIMDALL_PROBE_SCIM_WORKER === "true")
        assert(performance.now() - start < 60000, "SCIM denial propagation exceeded 60 seconds");
      workerDisabled = true;
    } else if (active) {
      runLiveWorker(reader, users[0], "reactivated");
    }
    if (useSDK && !inactiveReadChecked && !active) {
      await checkDirectorySDK(reader, users[0], "inactive");
      inactiveReadChecked = true;
    }
  }
  await ok("DELETE", `${paths[0]}/${users[0]}`);
  assert.equal((await request("GET", `${paths[0]}/${users[0]}`)).status, 404);
  observations.push({
    deleted: true,
    authoritativeReadMs: await stateWithin(users[0], "deleted", acmeToken),
  });
  await checkDirectorySDK(reader, users[0], "missing");
  runLiveWorker(reader, users[0], "missing");
  assert.equal((await ok("GET", `${paths[1]}/${users[1]}`)).active, true);
  if (process.env.HEIMDALL_PROBE_EVENTS === "true")
    observations.push({
      lifecycleEvents: await probeLifecycleEvents({
        ok,
        request,
        orgs,
        users,
        scopedToken: acmeToken,
        issuer,
        runId,
      }),
    });
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
      suite: "zitadel-scim-minimum-role-v3",
      runId,
      observedAt: new Date().toISOString(),
      issuer,
      observations,
      credentialChecks,
      cleanedUpOrganizations: orgs,
      limitations: [
        "custom role permits organization user lifecycle across APIs; not SCIM-endpoint-only",
        "single sequential sample; not a latency guarantee",
        process.env.HEIMDALL_PROBE_SCIM_WORKER === "true"
          ? "session-row fixtures; live browser sessions and OpenFGA reconciliation remain separate"
          : "no SDK workers, sessions or tuple reconciliation tested",
        "selected user read/write/delete and administrator-grant boundaries tested; not an exhaustive permission audit",
      ],
    },
    null,
    2,
  ),
);

import { request as httpRequest } from "node:http";
import assert from "node:assert/strict";
import { randomBytes } from "node:crypto";
import { createHash } from "node:crypto";
import { spawn, spawnSync } from "node:child_process";
import { once } from "node:events";

export async function verifyReferenceProduct(input, authorize) {
  const { config, manifest, clientSecret, apiAudience, user, recipient, runId, otherWorkspace } =
    input;
  const ledger = config.productId === "ledger";
  const port = ledger ? 18640 : 18641;
  const remote = process.env.HEIMDALL_REFERENCE_RAILWAY === "1";
  const endpoint = remote
    ? new URL(manifest.clients.web.redirectUris[0]).origin
    : `http://127.0.0.1:${port}`;
  const callback = manifest.clients.web.redirectUris[0];
  const host = new URL(callback).host;
  const origin = new URL(callback).origin;
  const db = process.env.HEIMDALL_REFERENCE_DATABASE_URL;
  assert(db || remote);
  const principal =
    "user:" + Buffer.from(JSON.stringify([config.issuer, user])).toString("base64url");
  const schema = ledger ? "ledger" : "support";
  const seed = ledger
    ? "INSERT INTO ledger.invoices VALUES ('acme','acme-invoice',500),('globex','globex-invoice',800)"
    : "INSERT INTO support.tickets VALUES ('acme','acme-ticket','Acme ticket'),('globex','globex-ticket','Globex ticket')";
  async function executeSQL(statement) {
    let sql;
    if (remote) {
      sql = spawn(
        "railway",
        [
          "ssh",
          "--environment",
          "verification",
          "--service",
          "capability-postgres",
          "--identity-file",
          process.env.HEIMDALL_REFERENCE_SSH_KEY,
          "--",
          "psql -Xq -U heimdall_probe -d heimdall_phase4_reference -v ON_ERROR_STOP=1 -v VERBOSITY=verbose",
        ],
        { stdio: ["pipe", "ignore", "pipe"] },
      );
    } else {
      const database = new URL(db);
      sql = spawn("psql", ["-Xq", "-v", "ON_ERROR_STOP=1", "-v", "VERBOSITY=verbose"], {
        env: {
          ...process.env,
          PGHOST: database.hostname,
          PGPORT: database.port,
          PGUSER: decodeURIComponent(database.username),
          PGPASSWORD: decodeURIComponent(database.password),
          PGDATABASE: database.pathname.slice(1),
          PGSSLMODE: database.searchParams.get("sslmode") ?? "require",
          PGOPTIONS: "",
        },
        stdio: ["pipe", "ignore", "pipe"],
      });
    }
    let sqlDiagnostic = "";
    sql.stderr.setEncoding("utf8");
    sql.stderr.on("data", (chunk) => {
      sqlDiagnostic = (sqlDiagnostic + chunk).slice(-8192);
    });
    sql.stdin.end(statement);
    const [sqlCode] = await once(sql, "exit");
    const sqlState =
      /ERROR:\s+([A-Z0-9]{5}):/.exec(sqlDiagnostic)?.[1] ?? "connection or client error";
    assert.equal(sqlCode, 0, "Reference fixture SQL failed " + sqlState);
  }
  assert(/^[A-Za-z0-9:_-]+$/.test(principal));
  await executeSQL(`
    INSERT INTO heimdall_managed.workspaces(product_id,environment,workspace_id,name,lifecycle,mutation_blocked,created_at,updated_at)
    VALUES ('${schema}','verification','acme','Acme','active',false,1,1), ('${schema}','verification','globex','Globex','active',false,1,1) ON CONFLICT DO NOTHING;
    INSERT INTO heimdall_managed.membership_candidates(product_id,environment,workspace_id,identity_hash,principal,observed_at)
    SELECT '${schema}','verification',workspace,sha256(convert_to('${principal}','UTF8')),'${principal}',1 FROM (VALUES ('acme'),('globex')) c(workspace);
    ${seed} ON CONFLICT DO NOTHING;
  `);
  const runtimeDB = remote
    ? process.env[
        ledger ? "HEIMDALL_REFERENCE_LEDGER_DATABASE" : "HEIMDALL_REFERENCE_SUPPORT_DATABASE"
      ]
    : db + "&options=-c%20role%3Dheimdall_public_runtime";
  assert(runtimeDB);
  const cmd = ledger
    ? ["node", ["examples/ledger/server.mjs"]]
    : [process.env.HEIMDALL_REFERENCE_SUPPORT_BINARY, []];
  const recordKey = remote ? randomBytes(32) : Buffer.alloc(32, 7);
  const variables = {
    PORT: String(port),
    DATABASE_URL: runtimeDB,
    HEIMDALL_KEY_DATABASE_URL: runtimeDB,
    [ledger ? "LEDGER_DATABASE_URL" : "SUPPORT_DATABASE_URL"]: runtimeDB,
    HEIMDALL_CONFIG: JSON.stringify(config),
    HEIMDALL_MANIFEST: JSON.stringify(manifest),
    HEIMDALL_CALLBACK_URL: callback,
    HEIMDALL_POLICY_VERSION: "reference-v1",
    HEIMDALL_WORKSPACE_CREATOR_SUBJECT: user,
    HEIMDALL_REFERENCE_MEMBER_SUBJECT: recipient,
    HEIMDALL_CLIENT_SECRET: clientSecret,
    HEIMDALL_OPENFGA_KEY: process.env.HEIMDALL_PROBE_OPENFGA_KEY,
    HEIMDALL_RECORD_KEY_ID: "fixture",
    HEIMDALL_RECORD_KEYS: JSON.stringify({ fixture: recordKey.toString("base64") }),
    HEIMDALL_CURSOR_KEY: randomBytes(32).toString("base64"),
    HEIMDALL_ZITADEL_API_AUDIENCE: apiAudience,
  };
  let child;
  if (remote) {
    const fingerprint = createHash("sha256")
      .update("heimdall.record-key-usage.v1\0")
      .update(recordKey)
      .digest("hex");
    const registration = spawnSync(
      "railway",
      [
        "ssh",
        "--environment",
        "verification",
        "--service",
        "capability-postgres",
        "--identity-file",
        process.env.HEIMDALL_REFERENCE_SSH_KEY,
        "--",
        "psql -Xq -U heimdall_probe -d heimdall_phase4_reference -v ON_ERROR_STOP=1",
      ],
      {
        input: `INSERT INTO heimdall_keys.record_key_usage(fingerprint,encryption_limit) VALUES(decode('${fingerprint}','hex'),100000);
`,
        stdio: ["pipe", "ignore", "ignore"],
      },
    );
    assert.equal(registration.status, 0, "Key registration failed");
    const service = "reference-" + schema;
    const context = ["--service", service, "--environment", "verification"];
    for (const [key, value] of Object.entries({
      ...variables,
      HEIMDALL_VERIFICATION_RUN: runId,
      RAILWAY_DOCKERFILE_PATH: `examples/${schema}/Dockerfile`,
    })) {
      const setting = spawnSync(
        "railway",
        ["variable", "set", ...context, "--skip-deploys", "--stdin", key],
        {
          input: value,
          stdio: ["pipe", "ignore", "ignore"],
          timeout: 30000,
        },
      );
      assert.equal(setting.status, 0, `Could not set ${service} variable ${key}`);
    }
    const deployment = spawnSync("railway", ["up", ...context, "--detach"], {
      stdio: "ignore",
      timeout: 60000,
    });
    assert.equal(deployment.status, 0, "Reference deployment submission failed");
  } else {
    child = spawn(cmd[0], cmd[1], { stdio: "ignore", env: { ...process.env, ...variables } });
  }
  const request = (path, options = {}) =>
    remote
      ? fetch(endpoint + path, {
          ...options,
          redirect: "manual",
          signal: AbortSignal.timeout(15000),
        })
      : new Promise((resolve, reject) => {
          const request = httpRequest(
            endpoint + path,
            {
              ...options,
              signal: AbortSignal.timeout(15000),
              headers: { host, ...options.headers },
            },
            (response) => {
              const parts = [];
              response.on("data", (chunk) => parts.push(chunk));
              response.on("error", reject);
              response.on("end", () => {
                const headers = new Headers();
                for (let index = 0; index < response.rawHeaders.length; index += 2)
                  headers.append(response.rawHeaders[index], response.rawHeaders[index + 1]);
                resolve(
                  new Response(parts.length ? Buffer.concat(parts) : null, {
                    status: response.statusCode,
                    headers,
                  }),
                );
              });
            },
          );
          request.on("error", reject);
          request.end(options.body);
        });
  try {
    let ready = false;
    let deployment;
    for (let attempt = 0; attempt < (remote ? 180 : 50); attempt++) {
      if (child && child.exitCode !== null) break;
      const response = await request("/healthz").catch(() => null);
      if (response?.ok) {
        const health = await response.json().catch(() => null);
        if (health?.ready && (!remote || (health.verificationRun === runId && health.deployment))) {
          deployment = health.deployment;
          ready = true;
          break;
        }
      } else {
        await response?.body?.cancel();
      }
      await new Promise((resolve) => setTimeout(resolve, remote ? 2000 : 200));
    }
    assert(ready, "Reference backend failed readiness");
    async function signIn(recipient = false) {
      const start = await request("/auth/start");
      assert.equal(start.status, 303, `Reference ${schema} login start returned ${start.status}`);
      const binding = start.headers.getSetCookie()[0].split(";")[0];
      const target = await authorize(start.headers.get("location"), recipient);
      const complete = await request(target.pathname + target.search, {
        headers: { cookie: binding },
      });
      assert.equal(
        complete.status,
        303,
        `Reference ${schema} callback returned ${complete.status}`,
      );
      return complete.headers
        .getSetCookie()
        .find((value) => value.includes("-session="))
        .split(";")[0];
    }
    const cookie = await signIn();
    const headers = { cookie };
    const page = await request("/workspaces", { headers });
    assert.equal(page.status, 200);
    assert.deepEqual((await page.json()).items, [{ id: "acme", name: "Acme" }]);
    for (const [workspace, roles] of [
      ["acme", [manifest.ownerRole]],
      ["globex", []],
    ]) {
      const response = await request(`/workspaces/${workspace}/roles`, { headers });
      assert.equal(response.status, 200);
      assert.deepEqual(await response.json(), { roles });
    }
    const collection = ledger ? "invoices" : "tickets";
    const allowed = await request(`/workspaces/acme/${collection}`, { headers });
    assert.equal(allowed.status, 200);
    assert.deepEqual(
      await allowed.json(),
      ledger
        ? { invoices: [{ invoice_id: "acme-invoice", total_cents: 500 }] }
        : { tickets: [{ ticket_id: "acme-ticket", summary: "Acme ticket" }] },
    );
    const denied = await request(`/workspaces/globex/${collection}`, { headers });
    assert.equal(denied.status, 403);
    await denied.body?.cancel();
    const input = { name: "Onboarded " + schema };
    const mutation = {
      method: "POST",
      headers: { cookie, origin, "content-type": "application/json", "idempotency-key": runId },
      body: JSON.stringify(input),
    };
    const csrf = await request("/workspaces", {
      ...mutation,
      headers: { ...mutation.headers, origin: "https://unrelated.example" },
    });
    assert.equal(csrf.status, 403);
    await csrf.body?.cancel();
    const anonymous = await request("/workspaces", {
      ...mutation,
      headers: { ...mutation.headers, cookie: "" },
    });
    assert.equal(anonymous.status, 401);
    await anonymous.body?.cancel();
    const injected = await request("/workspaces", {
      ...mutation,
      body: JSON.stringify({ ...input, owner: user }),
    });
    assert.equal(injected.status, 400);
    await injected.body?.cancel();
    if (otherWorkspace) {
      const foreignStatus = await request("/workspace-operations/" + otherWorkspace.operationId, {
        headers,
      });
      assert.equal(foreignStatus.status, 404);
      await foreignStatus.body?.cancel();
      const foreignRoles = await request(`/workspaces/${otherWorkspace.workspaceId}/roles`, {
        headers,
      });
      assert.equal(foreignRoles.status, 200);
      assert.deepEqual(await foreignRoles.json(), { roles: [] });
    }
    const created = await request("/workspaces", mutation);
    assert.equal(created.status, 200, `Reference ${schema} create returned ${created.status}`);
    const workspace = await created.json();
    assert.equal(workspace.status, "active");
    for (const id of [workspace.workspaceId, workspace.operationId])
      assert.match(id, /^[A-Za-z0-9_-]{1,128}$/);
    const duplicate = await request("/workspaces", mutation);
    assert.equal(duplicate.status, 200);
    assert.deepEqual(await duplicate.json(), workspace);
    const conflict = await request("/workspaces", {
      ...mutation,
      body: JSON.stringify({ name: "Changed" }),
    });
    assert.equal(conflict.status, 409);
    await conflict.body?.cancel();
    const operationPath = "/workspace-operations/" + workspace.operationId;
    for (const method of ["GET", "POST"]) {
      const response = await request(operationPath, { method, headers: { cookie, origin } });
      assert.equal(response.status, 200);
      assert.deepEqual(await response.json(), workspace);
    }
    const discovered = await request("/workspaces", { headers });
    assert.equal(discovered.status, 200);
    assert.deepEqual((await discovered.json()).items, [
      { id: "acme", name: "Acme" },
      { id: workspace.workspaceId, name: input.name },
    ]);
    const owner = await request(`/workspaces/${workspace.workspaceId}/roles`, { headers });
    assert.equal(owner.status, 200);
    assert.deepEqual(await owner.json(), { roles: [manifest.ownerRole] });
    const owned = await request(`/workspaces/${workspace.workspaceId}/${collection}`, { headers });
    assert.equal(owned.status, 200);
    assert.deepEqual(await owned.json(), { [collection]: [] });
    const recipientCookie = await signIn(true);
    const memberPath = `/workspaces/${workspace.workspaceId}/members/`;
    const initialRole = ledger ? "viewer" : "reader";
    const replacementRole = ledger ? "approver" : "agent";
    const memberRequest = (action, subject, key, roles, sessionCookie = cookie, source = origin) =>
      request(memberPath + action, {
        method: "POST",
        headers: {
          cookie: sessionCookie,
          origin: source,
          "content-type": "application/json",
          "idempotency-key": key,
        },
        body: JSON.stringify({ subject, ...(roles ? { roles } : {}) }),
      });
    const add = () => memberRequest("add", recipient, runId, [initialRole]);
    const rolesPath = `/workspaces/${workspace.workspaceId}/roles`;
    const resourcePath = `/workspaces/${workspace.workspaceId}/${collection}`;
    const recipientHeaders = { cookie: recipientCookie };
    const expectDenied = async (response, status) => {
      assert.equal(response.status, status);
      await response.body?.cancel();
    };
    await expectDenied(await request(resourcePath, { headers: recipientHeaders }), 403);
    await expectDenied(
      await memberRequest(
        "add",
        recipient,
        runId,
        [initialRole],
        cookie,
        "https://unrelated.example",
      ),
      403,
    );
    await expectDenied(
      await memberRequest("add", recipient, runId, [initialRole], recipientCookie),
      403,
    );
    const added = await add();
    assert.equal(added.status, 200, `Reference ${schema} member add returned ${added.status}`);
    const grant = await added.json();
    assert.equal(grant.status, "confirmed");
    const repeated = await add();
    assert.equal(repeated.status, 200);
    assert.deepEqual(await repeated.json(), grant);
    await expectDenied(await memberRequest("add", recipient, runId, [replacementRole]), 409);
    assert.deepEqual(await (await request(rolesPath, { headers: recipientHeaders })).json(), {
      roles: [initialRole],
    });
    const changed = await memberRequest("assign-roles", recipient, runId, [replacementRole]);
    assert.equal(changed.status, 200);
    const assignment = await changed.json();
    assert.deepEqual(await (await request(rolesPath, { headers: recipientHeaders })).json(), {
      roles: [replacementRole],
    });
    const removed = await memberRequest("remove", recipient, runId);
    assert.equal(removed.status, 200);
    const removal = await removed.json();
    await expectDenied(await request(resourcePath, { headers: recipientHeaders }), 403);
    const invite = (sessionCookie) =>
      request(`/workspaces/${workspace.workspaceId}/invitations`, {
        method: "POST",
        headers: { cookie: sessionCookie, origin, "content-type": "application/json" },
        body: JSON.stringify({ subject: recipient, roles: [initialRole] }),
      });
    await expectDenied(await invite(recipientCookie), 403);
    const issued = await invite(cookie);
    assert.equal(issued.status, 201);
    const invitation = await issued.json();
    assert.match(invitation.token, /^[a-f0-9]{64}$/);
    assert(Date.parse(invitation.expiresAt) > Date.now());
    const stale = await invite(cookie);
    assert.equal(stale.status, 201);
    const staleInvitation = await stale.json();
    const expiring = await invite(cookie);
    assert.equal(expiring.status, 201);
    const expired = await expiring.json();
    const expiredHash = createHash("sha256").update(expired.token).digest("hex");
    await executeSQL(`UPDATE ${schema}.invitations SET expires_at=now()-interval '1 second'
      WHERE environment='verification' AND token_hash='${expiredHash}';`);
    const accept = (sessionCookie, source = origin, token = invitation.token) =>
      request("/invitations/accept", {
        method: "POST",
        headers: { cookie: sessionCookie, origin: source, "content-type": "application/json" },
        body: JSON.stringify({ token }),
      });
    await expectDenied(await accept(cookie), 404);
    await expectDenied(await accept(recipientCookie, origin, expired.token), 404);
    await expectDenied(await accept(recipientCookie, "https://unrelated.example"), 403);
    const readded = await accept(recipientCookie);
    assert.equal(readded.status, 200);
    const regrant = await readded.json();
    assert.equal(regrant.status, "confirmed");
    const invitationReplay = await accept(recipientCookie);
    assert.equal(invitationReplay.status, 200);
    assert.deepEqual(await invitationReplay.json(), regrant);
    const transferred = await memberRequest("transfer-ownership", recipient, runId);
    assert.equal(transferred.status, 200);
    const transfer = await transferred.json();
    assert.equal(transfer.status, "confirmed");
    await expectDenied(await accept(recipientCookie, origin, staleInvitation.token), 403);
    assert.deepEqual(await (await request(rolesPath, { headers: recipientHeaders })).json(), {
      roles: [initialRole, manifest.ownerRole].sort(),
    });
    assert.deepEqual(await (await request(rolesPath, { headers })).json(), { roles: [] });
    await expectDenied(await request(resourcePath, { headers }), 403);
    const recipientResource = await request(resourcePath, { headers: recipientHeaders });
    assert.equal(recipientResource.status, 200);
    await recipientResource.body?.cancel();
    await expectDenied(
      await request("/auth/logout", { method: "POST", headers: { ...recipientHeaders, origin } }),
      204,
    );
    const logout = await request("/auth/logout", { method: "POST", headers: { cookie, origin } });
    assert.equal(logout.status, 204);
    const replay = await request("/workspaces", { headers });
    assert.equal(replay.status, 401);
    await replay.body?.cancel();
    return {
      product: schema,
      ...(remote ? { deployment } : {}),
      session: "issued and logged out",
      discovery: "Acme",
      roles: { acme: [manifest.ownerRole], globex: [] },
      scopedRead: 200,
      onboarding: {
        ...workspace,
        replay: 200,
        changedInput: 409,
        crossOrigin: 403,
        unauthenticated: 401,
        injectedOwner: 400,
        ...(otherWorkspace ? { otherProductOperation: 404, otherProductRoles: [] } : {}),
        discovered: true,
        roles: [manifest.ownerRole],
        emptyScopedRead: 200,
      },
      administration: { grant, assignment, removal, regrant, transfer },
      invitation: {
        issued: 201,
        wrongRecipient: 404,
        expired: 404,
        crossOrigin: 403,
        accepted: 200,
        replay: 200,
        revokedInviter: 403,
      },
      otherTenant: 403,
    };
  } finally {
    if (child && child.exitCode === null) {
      child.kill();
      await once(child, "exit");
    }
  }
}

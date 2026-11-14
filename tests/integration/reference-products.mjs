import { request as httpRequest } from "node:http";
import assert from "node:assert/strict";
import { randomBytes } from "node:crypto";
import { createHash } from "node:crypto";
import { execFile, spawn, spawnSync } from "node:child_process";
import { once } from "node:events";
import { createRequire } from "node:module";
import { promisify } from "node:util";
import { setTimeout as delay } from "node:timers/promises";

export function coordinateSCIMDisable(directory, scheduled) {
  const ready = new Map();
  const completed = Promise.withResolvers();
  const replayed = Promise.withResolvers();
  const fresh = new Map();
  // One product can fail during setup before either product reaches the rendezvous.
  void completed.promise.catch(() => {});
  void replayed.promise.catch(() => {});
  return {
    fail(error) {
      completed.reject(error);
      replayed.reject(error);
    },
    async replay(product, verify) {
      fresh.set(product, verify);
      if (fresh.size === 2) {
        try {
          await directory.setActive(false);
          for (const verify of fresh.values()) await verify();
          replayed.resolve();
        } catch (error) {
          replayed.reject(error);
        }
      }
      await replayed.promise;
    },
    async join(product, session) {
      ready.set(product, session);
      if (ready.size === 2) {
        try {
          const ledger = ready.get("ledger");
          const support = ready.get("support");
          if (scheduled) {
            await ledger.seedDirectoryJob();
            await support.seedDirectoryJob();
          }
          const started = performance.now();
          await directory.setActive(false);
          if (scheduled) {
            await ledger.finishDirectoryJob(started);
            await support.assertObservationMissing();
            await delay(Math.max(0, 61000 - (performance.now() - started)));
            await support.assertMissedObservationDenial(started);
            await support.assertObservationMissing();
            await support.finishDirectoryJob(started);
          } else {
            await ledger.observeDisable(started);
            await support.observeDisable(started);
            await support.withCleanupOutage(async () => {
              await ledger.confirmCleanup();
              await support.assertStillDeniedWithGrants();
            });
            await support.confirmCleanup();
          }
          await directory.setActive(true);
          completed.resolve();
        } catch (error) {
          completed.reject(error);
        }
      }
      await completed.promise;
    },
  };
}

export async function verifyReferenceProduct(input, authorize) {
  const {
    config,
    manifest,
    clientSecret,
    apiAudience,
    user,
    recipient,
    runId,
    otherWorkspace,
    exportToken,
    federation,
    scim,
    roleAssignments,
    readMemberRelations,
  } = input;
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
          "psql -XqAt -U heimdall_probe -d heimdall_phase4_reference -v ON_ERROR_STOP=1 -v VERBOSITY=verbose",
        ],
        { stdio: ["pipe", "pipe", "pipe"] },
      );
    } else {
      const database = new URL(db);
      sql = spawn("psql", ["-XqAt", "-v", "ON_ERROR_STOP=1", "-v", "VERBOSITY=verbose"], {
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
        stdio: ["pipe", "pipe", "pipe"],
      });
    }
    let sqlDiagnostic = "";
    let output = "";
    sql.stdout.setEncoding("utf8");
    sql.stdout.on("data", (chunk) => {
      output += chunk;
    });
    sql.stderr.setEncoding("utf8");
    sql.stderr.on("data", (chunk) => {
      sqlDiagnostic = (sqlDiagnostic + chunk).slice(-8192);
    });
    sql.stdin.end(statement);
    const [sqlCode] = await once(sql, "close");
    const sqlState =
      /ERROR:\s+([A-Z0-9]{5}):/.exec(sqlDiagnostic)?.[1] ?? "connection or client error";
    assert.equal(sqlCode, 0, "Reference fixture SQL failed " + sqlState);
    return output.trim();
  }
  assert(/^[A-Za-z0-9:_-]+$/.test(principal));
  if (federation) assert(/^[A-Za-z0-9:_-]+$/.test(federation.principal));
  await executeSQL(`
    INSERT INTO heimdall_managed.workspaces(product_id,environment,workspace_id,name,lifecycle,mutation_blocked,created_at,updated_at)
    VALUES ('${schema}','verification','acme','Acme','active',false,1,1), ('${schema}','verification','globex','Globex','active',false,1,1) ON CONFLICT DO NOTHING;
    INSERT INTO heimdall_managed.membership_candidates(product_id,environment,workspace_id,identity_hash,principal,observed_at)
    SELECT '${schema}','verification',workspace,sha256(convert_to('${principal}','UTF8')),'${principal}',1 FROM (VALUES ('acme'),('globex')) c(workspace);
    ${seed} ON CONFLICT DO NOTHING;
    ${
      federation
        ? `INSERT INTO heimdall_managed.membership_candidates(product_id,environment,workspace_id,identity_hash,principal,observed_at)
      VALUES ('${schema}','verification','globex',sha256(convert_to('${federation.principal}','UTF8')),'${federation.principal}',1);`
        : ""
    }
  `);
  const runtimeDB = remote
    ? process.env[
        ledger ? "HEIMDALL_REFERENCE_LEDGER_DATABASE" : "HEIMDALL_REFERENCE_SUPPORT_DATABASE"
      ]
    : db + "&options=-c%20role%3Dheimdall_public_runtime";
  assert(runtimeDB);
  const exportDB = remote
    ? process.env.HEIMDALL_REFERENCE_EXPORT_DATABASE
    : db + "&options=-c%20role%3Dheimdall_reference_export";
  const cmd = ledger
    ? ["node", ["examples/ledger/server.mjs"]]
    : [process.env.HEIMDALL_REFERENCE_SUPPORT_BINARY, []];
  const recordKey = remote ? randomBytes(32) : Buffer.alloc(32, 7);
  const variables = {
    PORT: String(port),
    ...(exportDB ? { HEIMDALL_EXPORT_DATABASE_URL: exportDB } : {}),
    DATABASE_URL: runtimeDB,
    HEIMDALL_KEY_DATABASE_URL: runtimeDB,
    [ledger ? "LEDGER_DATABASE_URL" : "SUPPORT_DATABASE_URL"]: runtimeDB,
    HEIMDALL_CONFIG: JSON.stringify(config),
    HEIMDALL_MANIFEST: JSON.stringify(manifest),
    HEIMDALL_CALLBACK_URL: callback,
    HEIMDALL_POLICY_VERSION: "reference-v1",
    HEIMDALL_WORKSPACE_CREATOR_SUBJECT: user,
    HEIMDALL_REFERENCE_MEMBER_SUBJECT: recipient,
    HEIMDALL_REFERENCE_STRICT_SSO: "1",
    ...(process.env.HEIMDALL_PROBE_PRODUCT_SIEM === "1" ? { HEIMDALL_REFERENCE_SIEM: "1" } : {}),
    ...(federation
      ? {
          HEIMDALL_TENANT_SSO_POLICIES: JSON.stringify(federation.policies),
          HEIMDALL_FEDERATION_CREDENTIAL: federation.credential,
        }
      : {}),
    ...(federation?.cleanup
      ? {
          HEIMDALL_DIRECTORY_OPERATOR_SUBJECT: federation.cleanup.operator,
          HEIMDALL_DIRECTORY_ORGANIZATION: federation.cleanup.organizationId,
          HEIMDALL_DIRECTORY_WORKSPACES: '["globex"]',
        }
      : {}),
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
    if (federation) {
      const expectStatus = async (path, status, cookie = "") => {
        const response = await request(path, { headers: { cookie } });
        assert.equal(response.status, status, `${schema} ${path.split("?")[0]} status`);
        return response;
      };
      const start = async (tenant, previous = "") => {
        const response = await expectStatus("/auth/federation?workspace=" + tenant, 303, previous);
        const binding = response.headers.getSetCookie()[0].split(";")[0];
        return {
          path: await authorize(response.headers.get("location"), tenant),
          cookie: [binding, previous].filter(Boolean).join("; "),
        };
      };
      const finish = async (flow) => {
        const response = await expectStatus(flow.path, 303, flow.cookie);
        return response.headers
          .getSetCookie()
          .find((value) => value.includes("-session="))
          .split(";")[0];
      };
      const before = await finish(await start("acme"));
      let cleanupRequest;
      const directory = federation.cleanup;
      if (directory) {
        assert([directory.subject, directory.organizationId].every((id) => /^[0-9]+$/.test(id)));
        const identity =
          "user:" +
          Buffer.from(JSON.stringify([config.issuer, directory.subject])).toString("base64url");
        const revision = await executeSQL(`
          BEGIN;
          INSERT INTO heimdall.identity_denials(product_id,environment,identity_hash,observed_at,reason)
          VALUES ('${schema}','verification',sha256(convert_to('${identity}','UTF8')),1,'inactive');
          INSERT INTO heimdall_directory.deactivations(product_id,environment,identity_hash,issuer,subject,organization_id,observed_at,reason)
          VALUES ('${schema}','verification',sha256(convert_to('${identity}','UTF8')),'${config.issuer}','${directory.subject}','${directory.organizationId}',1,'inactive') RETURNING revision;
          COMMIT;
        `);
        assert.match(revision, /^[1-9][0-9]*$/);
        cleanupRequest = (cookie, source = origin, extra = {}) =>
          request("/directory/cleanup", {
            method: "POST",
            headers: { cookie, origin: source, "content-type": "application/json" },
            body: JSON.stringify({ subject: directory.subject, revision, ...extra }),
          });
        assert.equal((await cleanupRequest("")).status, 401);
        assert.equal((await cleanupRequest(before)).status, 403);
        assert.equal((await directory.readRelations()).length, 2);
      }
      const page = await (await expectStatus("/workspaces?limit=1", 200, before)).json();
      assert.deepEqual(
        page.items.map(({ id }) => id),
        ["acme"],
      );
      assert(page.nextCursor, "fixture must issue an Acme cursor");
      const collection = ledger ? "invoices" : "tickets";
      await expectStatus("/workspaces/acme/" + collection, 200, before);
      await expectStatus("/workspaces/globex/" + collection, 403, before);
      const stale = await start("globex", before);
      const winner = await start("globex", before);
      const after = await finish(winner);
      assert.notEqual(after, before);
      await expectStatus(stale.path, 401, stale.cookie);
      await expectStatus(winner.path, 401, winner.cookie);
      await expectStatus("/workspaces", 401, before);
      await expectStatus("/workspaces?cursor=" + encodeURIComponent(page.nextCursor), 400, after);
      const next = await (await expectStatus("/workspaces", 200, after)).json();
      assert.deepEqual(
        next.items.map(({ id }) => id),
        ["globex"],
      );
      assert.deepEqual(await (await expectStatus("/workspaces/acme/roles", 200, after)).json(), {
        roles: [],
      });
      assert.deepEqual(await (await expectStatus("/workspaces/globex/roles", 200, after)).json(), {
        roles: [manifest.ownerRole],
      });
      await expectStatus("/workspaces/acme/" + collection, 403, after);
      await expectStatus("/workspaces/globex/" + collection, 200, after);
      if (directory) {
        assert.equal((await cleanupRequest(after, "https://unrelated.example")).status, 403);
        assert.equal((await cleanupRequest(after, origin, { workspace: "acme" })).status, 400);
        assert.equal((await directory.readRelations()).length, 2);
        const result = await cleanupRequest(after);
        assert.equal(result.status, 200, `${schema} strict directory cleanup`);
        assert.deepEqual(await result.json(), { status: "confirmed" });
        assert.deepEqual(await directory.readRelations(), []);
        assert.equal((await cleanupRequest(after)).status, 200);
        await expectStatus("/workspaces/globex/" + collection, 200, after);
      }
      const logout = await request("/auth/logout", {
        method: "POST",
        headers: { cookie: after, origin },
      });
      assert.equal(logout.status, 204);
      return {
        product: schema,
        deployment,
        switch: "passed",
        ...(directory ? { strictDirectoryCleanup: "confirmed and replayed" } : {}),
        priorCookie: 401,
        staleCallback: 401,
        replay: 401,
        priorCursor: 400,
        discoveryBefore: "acme",
        discoveryAfter: "globex",
        resourceAfter: { acme: 403, globex: 200 },
      };
    }
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
    const readFailure = allowed.ok ? "" : (await allowed.json().catch(() => ({}))).code;
    assert.equal(allowed.status, 200, `Reference ${schema} scoped read failed: ${readFailure}`);
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
    if (process.env.HEIMDALL_PROBE_PRODUCT_SIEM === "1") {
      const response = await request(resourcePath, { headers: recipientHeaders });
      assert.equal(response.status, 200);
      assert.deepEqual(await response.json(), { [collection]: [] });
      return {
        product: schema,
        scopedRead: 200,
        onboarding: { workspaceId: workspace.workspaceId },
        administration: { grant },
        auditActor: user,
        auditRecipient: recipient,
      };
    }
    const repeated = await add();
    assert.equal(repeated.status, 200);
    assert.deepEqual(await repeated.json(), grant);
    await expectDenied(await memberRequest("add", recipient, runId, [replacementRole]), 409);
    assert.deepEqual(await (await request(rolesPath, { headers: recipientHeaders })).json(), {
      roles: [initialRole],
    });
    if (scim) {
      assert(!remote, "SCIM browser probe requires a disposable local product database");
      await expectDenied(
        await memberRequest("assign-roles", recipient, runId, [replacementRole]),
        200,
      );
      await expectDenied(await request(resourcePath, { headers: recipientHeaders }), 200);
      const readRelations = () => readMemberRelations(recipient, workspace.workspaceId);
      const originalTuples = await readRelations();
      assert.deepEqual(originalTuples, ["member", roleAssignments[replacementRole].assignment]);
      const command = ledger
        ? [
            "pnpm",
            "--filter",
            "@heimdall/sdk",
            "exec",
            "vitest",
            "run",
            "test/live-scim-worker.test.ts",
          ]
        : ["go", "test", "-v", "-race", "-count=1", "./postgres", "-run", "^TestLiveSCIMWorker$"];
      const { issuer, organizationId, token } = scim;
      function runDirectoryJob() {
        const args = ledger
          ? ["node", "examples/ledger/directory-job.mjs"]
          : [process.env.HEIMDALL_REFERENCE_SUPPORT_BINARY, "directory-job"];
        const job = spawnSync(args[0], args.slice(1), {
          env: {
            ...process.env,
            ...variables,
            HEIMDALL_DIRECTORY_ORGANIZATION: organizationId,
            HEIMDALL_DIRECTORY_READER_TOKEN: token,
            HEIMDALL_DIRECTORY_OPERATOR_TOKEN: exportToken,
            HEIMDALL_DIRECTORY_WORKSPACES: JSON.stringify([workspace.workspaceId]),
          },
          encoding: "utf8",
          timeout: 50000,
        });
        const code = /"directoryJob":"([a-z_]+)"/.exec(job.stderr ?? "")?.[1];
        assert.equal(
          job.status,
          0,
          `Live SCIM ${schema} directory job failed: ${code ?? "process"}`,
        );
        return Object.assign(
          {},
          ...job.stdout
            .trim()
            .split("\n")
            .map((line) => JSON.parse(line)),
        );
      }
      assert(/^[0-9]+$/.test(recipient));
      const readDirectoryRevision = () =>
        executeSQL(`SELECT revision::text || ':' || completed::text FROM heimdall_directory.deactivations
          WHERE product_id='${schema}' AND environment='verification' AND subject='${recipient}'`);
      function runWorker(stage, revision) {
        const worker = spawnSync(command[0], command.slice(1), {
          cwd: new URL(ledger ? "../../" : "../../sdk/go/", import.meta.url),
          env: {
            ...process.env,
            HEIMDALL_TEST_DIRECTORY_DB: runtimeDB,
            HEIMDALL_TEST_LIVE_SCIM: JSON.stringify({
              issuer,
              organizationId,
              token,
              subject: recipient,
              stage,
              revision,
              workspaceId: workspace.workspaceId,
              approvedRole: initialRole,
              config,
              manifest,
              operatorToken: exportToken,
              clientSecret,
              openfgaKey: variables.HEIMDALL_OPENFGA_KEY,
            }),
          },
          encoding: "utf8",
          timeout: 45000,
        });
        const step = /SCIM_STEP ([a-z-]+)/g;
        const last = [...(worker.stdout + worker.stderr).matchAll(step)].at(-1)?.[1];
        assert.equal(
          worker.status,
          0,
          `Live SCIM ${schema} ${stage} failed at ${last ?? "startup"}`,
        );
        return worker.stdout + worker.stderr;
      }
      const readRevision = (output) => {
        const revision = /SCIM_REVISION ([1-9][0-9]*)/.exec(output)?.[1];
        assert(revision, "Live SCIM worker did not report a revision");
        return revision;
      };
      let deniedInMs, scheduledCleanupInMs, revision, failedRevision, scheduledJob;
      const assertStillDeniedWithGrants = async () => {
        await expectDenied(await request(resourcePath, { headers: recipientHeaders }), 401);
        assert.deepEqual(await readRelations(), originalTuples);
      };
      await scim.join(schema, {
        async seedDirectoryJob() {
          const result = runDirectoryJob();
          assert.equal(result.discovered, 1);
          assert.equal(result.observed, 1);
          assert.equal(result.sessionsInvalidated, 0);
          assert.equal(result.cleaned, 0);
          await expectDenied(await request(resourcePath, { headers: recipientHeaders }), 200);
        },
        async assertObservationMissing() {
          assert.equal(await readDirectoryRevision(), "");
        },
        async assertMissedObservationDenial(started) {
          await assertStillDeniedWithGrants();
          deniedInMs = Math.round(performance.now() - started);
        },
        async finishDirectoryJob(started) {
          const result = runDirectoryJob();
          assert.equal(result.observed, 1);
          assert.equal(result.cleaned, 1);
          if (ledger) assert.equal(result.sessionsInvalidated, 1);
          const match = /^([1-9][0-9]*):true$/.exec(await readDirectoryRevision());
          assert(match, "Directory job did not complete cleanup");
          revision = match[1];
          await expectDenied(await request(resourcePath, { headers: recipientHeaders }), 401);
          scheduledCleanupInMs = Math.round(performance.now() - started);
          if (ledger) {
            assert(scheduledCleanupInMs < 60000);
            deniedInMs = scheduledCleanupInMs;
          }
          assert.deepEqual(await readRelations(), []);
          assert.equal(runDirectoryJob().cleaned, 0);
          scheduledJob = true;
        },
        async observeDisable(started) {
          runWorker("browser");
          await assertStillDeniedWithGrants();
          deniedInMs = Math.round(performance.now() - started);
          assert(deniedInMs < 60000);
        },
        async withCleanupOutage(whileBlocked) {
          await executeSQL(`
            CREATE FUNCTION public.fail_support_cleanup() RETURNS trigger LANGUAGE plpgsql AS $$
            BEGIN RAISE EXCEPTION 'injected directory cleanup outage' USING ERRCODE = '57P01'; END;
            $$;
            CREATE TRIGGER fail_support_cleanup BEFORE UPDATE OF cleanup_plan
            ON heimdall_directory.deactivations FOR EACH ROW
            WHEN (NEW.product_id = 'support' AND NEW.environment = 'verification')
            EXECUTE FUNCTION public.fail_support_cleanup();
          `);
          try {
            failedRevision = readRevision(runWorker("cleanup-blocked"));
            await whileBlocked();
          } finally {
            await executeSQL(`
              DROP TRIGGER fail_support_cleanup ON heimdall_directory.deactivations;
              DROP FUNCTION public.fail_support_cleanup();
            `);
          }
        },
        assertStillDeniedWithGrants,
        async confirmCleanup() {
          revision = readRevision(runWorker("cleanup"));
          if (failedRevision) assert.equal(revision, failedRevision);
          runWorker("cleanup-replay", revision);
          assert.deepEqual(await readRelations(), []);
          await expectDenied(await request(resourcePath, { headers: recipientHeaders }), 401);
        },
      });
      await expectDenied(await request(resourcePath, { headers: recipientHeaders }), 401);
      runWorker("reenroll", revision);
      assert.deepEqual(await readRelations(), ["member", roleAssignments[initialRole].assignment]);
      const freshHeaders = { cookie: await signIn(true) };
      await expectDenied(await request(resourcePath, { headers: freshHeaders }), 200);
      assert.deepEqual(await (await request(rolesPath, { headers: freshHeaders })).json(), {
        roles: [initialRole],
      });
      await expectDenied(
        await request(`/workspaces/acme/${collection}`, { headers: freshHeaders }),
        403,
      );
      await expectDenied(await request(resourcePath, { headers: recipientHeaders }), 401);
      await scim.replay(schema, async () => {
        runWorker("stale-reactivation", revision);
        await expectDenied(await request(resourcePath, { headers: freshHeaders }), 401);
        assert.deepEqual(await readRelations(), [
          "member",
          roleAssignments[initialRole].assignment,
        ]);
        const next = readRevision(runWorker("cleanup"));
        assert(BigInt(next) > BigInt(revision));
        assert.deepEqual(await readRelations(), []);
        runWorker("cleanup-replay", next);
        await expectDenied(await request(resourcePath, { headers: freshHeaders }), 401);
      });
      return {
        product: schema,
        scopedRead: 200,
        scimBrowser: {
          workspaceId: workspace.workspaceId,
          deniedInMs,
          disabledSession: 401,
          sharedProviderDisable: true,
          deniedWithTuplesPresent: !scheduledJob || !ledger,
          cleanupAcknowledged: true,
          cleanupReplayConfirmed: true,
          ...(scheduledJob
            ? { scheduledJob: true, scheduledCleanupInMs, missedObservationDenied: !ledger }
            : {}),
          ...(failedRevision
            ? { cleanupResumedSameRevision: true, deniedDuringCleanupOutage: true }
            : {}),
          removedTuples: originalTuples,
          releasedAfterApprovedEnrollment: true,
          approvedRole: initialRole,
          originalCookieAfterRelease: 401,
          freshCookie: 200,
          freshCookieOtherWorkspace: 403,
          staleReactivationRejected: true,
          secondCleanupConfirmed: true,
        },
      };
    }
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
    const setLifecycle = (action, sessionCookie, source = origin) =>
      request(`/workspaces/${workspace.workspaceId}/${action}`, {
        method: "POST",
        headers: { cookie: sessionCookie, origin: source },
      });
    const blocked = (value) =>
      executeSQL(`UPDATE heimdall_managed.workspaces SET mutation_blocked=${value}
      WHERE product_id='${schema}' AND environment='verification' AND workspace_id='${workspace.workspaceId}';`);
    assert.match(workspace.workspaceId, /^[a-z0-9-]+$/);
    await expectDenied(await setLifecycle("suspend", recipientCookie), 403);
    await expectDenied(await setLifecycle("suspend", cookie, "https://unrelated.example"), 403);
    await blocked(true);
    await expectDenied(await setLifecycle("suspend", cookie), 204);
    await expectDenied(await setLifecycle("resume", cookie), 409);
    await expectDenied(await request(resourcePath, { headers: recipientHeaders }), 403);
    assert.deepEqual(await (await request(rolesPath, { headers: recipientHeaders })).json(), {
      roles: [],
    });
    const suspendedList = await (
      await request("/workspaces", { headers: recipientHeaders })
    ).json();
    assert(!suspendedList.items.some(({ id }) => id === workspace.workspaceId));
    await blocked(false);
    assert.match(transfer.operationId, /^[a-z0-9-]+$/);
    const setTransferState = (state) =>
      executeSQL(`UPDATE heimdall_managed.operations SET state='${state}'
      WHERE product_id='${schema}' AND environment='verification' AND operation_id='${transfer.operationId}';`);
    await setTransferState("uncertain");
    try {
      await expectDenied(await setLifecycle("resume", cookie), 409);
    } finally {
      await setTransferState("completed");
    }
    await expectDenied(await request(resourcePath, { headers: recipientHeaders }), 403);
    await expectDenied(await setLifecycle("resume", cookie), 204);
    await expectDenied(await request(resourcePath, { headers: recipientHeaders }), 200);
    assert.deepEqual(await (await request(rolesPath, { headers: recipientHeaders })).json(), {
      roles: [initialRole, manifest.ownerRole].sort(),
    });
    let discoveryRebuild;
    const rebuildPort = process.env.HEIMDALL_DISCOVERY_REBUILD_RAILWAY_PORT;
    const rebuildDB = !remote || rebuildPort ? new URL(runtimeDB) : null;
    if (remote && rebuildDB) {
      rebuildDB.hostname = "127.0.0.1";
      rebuildDB.port = rebuildPort;
    }
    if (rebuildDB) {
      const { Pool } = createRequire(
        new URL("../../examples/ledger/package.json", import.meta.url),
      )("pg");
      const { rebuildDiscovery } = await import("../../sdk/typescript/dist/provisioning/index.js");
      const workspaceId = workspace.workspaceId;
      const object = "workspace:" + workspaceId;
      const principalFor = (subject) =>
        "user:" + Buffer.from(JSON.stringify([config.issuer, subject])).toString("base64url");
      const write = async (change) => {
        const response = await fetch(
          `${config.openfga.url}/stores/${config.openfga.storeId}/write`,
          {
            method: "POST",
            headers: {
              authorization: "Bearer " + variables.HEIMDALL_OPENFGA_KEY,
              "content-type": "application/json",
            },
            signal: AbortSignal.timeout(15000),
            body: JSON.stringify({ authorization_model_id: config.openfga.modelId, ...change }),
          },
        );
        assert.equal(response.status, 200);
        await response.body?.cancel();
      };
      const addedMembers = Array.from({ length: 21 }, (_, i) => ({
        user: principalFor("repair-probe-" + i),
        relation: "member",
        object,
      }));
      await write({ writes: { tuple_keys: addedMembers } });
      const recipientPrincipal = principalFor(recipient);
      assert(/^[A-Za-z0-9:_-]+$/.test(recipientPrincipal));
      await executeSQL(`DELETE FROM heimdall_managed.membership_candidates WHERE product_id='${schema}'
        AND environment='verification' AND workspace_id='${workspaceId}' AND principal='${recipientPrincipal}';`);
      const pool = new Pool({ connectionString: rebuildDB.href });
      const options = { workspaceId, policyVersion: "reference-v1", auditSinks: [] };
      const page = async (language, checkpoint) => {
        if (language === "ts")
          return rebuildDiscovery(
            pool,
            JSON.stringify(config),
            JSON.stringify(manifest),
            { ...options, openfgaToken: variables.HEIMDALL_OPENFGA_KEY, checkpoint },
            AbortSignal.timeout(30000),
          );
        const { stdout } = await promisify(execFile)(
          "go",
          ["test", "./provisioning", "-run", "^TestDiscoveryRebuildProbe$", "-count=1", "-v"],
          {
            cwd: "sdk/go",
            timeout: 45000,
            env: {
              ...process.env,
              HEIMDALL_DISCOVERY_REBUILD_DATABASE: rebuildDB.href,
              HEIMDALL_DISCOVERY_REBUILD_INPUT: JSON.stringify({
                config,
                manifest,
                options: { ...options, checkpoint },
              }),
            },
          },
        );
        const failed = /^REBUILD_PROBE_ERROR ([a-z_]+)$/m.exec(stdout);
        if (failed) throw Object.assign(new Error(failed[1]), { code: failed[1] });
        return JSON.parse(/^REBUILD_PROBE (.+)$/m.exec(stdout)[1]);
      };
      try {
        await blocked(true);
        try {
          await assert.rejects(page(ledger ? "ts" : "go"), { code: "conflict" });
        } finally {
          await blocked(false);
        }
        const first = await page(ledger ? "ts" : "go");
        assert.equal(first.processed, 20);
        assert.equal(first.checkpoint.phase, "remote");
        const other = ledger ? "go" : "ts";
        await assert.rejects(page(other, { ...first.checkpoint, workspaceId: "globex" }), {
          code: "invalid_argument",
        });
        const second = await page(other, first.checkpoint);
        assert.equal(second.checkpoint.phase, "candidates");
        assert.equal(first.processed + second.processed, 23);
        const restored = await (await request("/workspaces", { headers: recipientHeaders })).json();
        assert(restored.items.some(({ id }) => id === workspaceId));
        // A direct deletion leaves a stale hint. Only the explicit check may remove it.
        await write({ deletes: { tuple_keys: [addedMembers[0]] } });
        const third = await page(ledger ? "ts" : "go", second.checkpoint);
        assert.equal(third.processed, 20);
        assert.equal(third.checkpoint.phase, "candidates");
        const fourth = await page(other, third.checkpoint);
        assert.equal(third.processed + fourth.processed, 23);
        assert.equal(fourth.checkpoint, null);
        const { rows } = await pool.query(
          "SELECT principal FROM heimdall_managed.membership_candidates WHERE product_id=$1 AND environment=$2 AND workspace_id=$3",
          [schema, "verification", workspaceId],
        );
        assert.equal(rows.length, 22);
        assert(!rows.some(({ principal }) => principal === addedMembers[0].user));
        assert(rows.some(({ principal }) => principal === recipientPrincipal));
        discoveryRebuild = { remoteMembers: 23, verifiedCandidates: 23, retainedCandidates: 22 };
      } finally {
        await pool.end();
      }
    }
    const reconcile = (sessionCookie) =>
      request("/workspace-operations/reconcile", {
        method: "POST",
        headers: { cookie: sessionCookie, origin },
      });
    await expectDenied(await reconcile(recipientCookie), 403);
    assert.match(workspace.operationId, /^[a-z0-9-]+$/);
    // Recreate the local state after provider confirmation and before activation.
    await executeSQL(`BEGIN;
      UPDATE heimdall_managed.operations SET state='confirmed'
        WHERE product_id='${schema}' AND environment='verification' AND operation_id='${workspace.operationId}';
      UPDATE heimdall_managed.workspaces SET lifecycle='pending',mutation_blocked=true
        WHERE product_id='${schema}' AND environment='verification' AND workspace_id='${workspace.workspaceId}';
      COMMIT;`);
    const swept = await reconcile(cookie);
    assert.equal(swept.status, 200);
    const summary = await swept.json();
    assert(
      summary.operations.some(
        ({ operationId, disposition }) =>
          operationId === workspace.operationId && disposition === "completed",
      ),
    );
    await expectDenied(await request(resourcePath, { headers: recipientHeaders }), 200);
    const sweptAgain = await reconcile(cookie);
    assert.equal(sweptAgain.status, 200);
    assert(
      !(await sweptAgain.json()).operations.some(
        ({ operationId }) => operationId === workspace.operationId,
      ),
    );
    const metricsPath = "/workspace-operations/metrics";
    await expectDenied(await request(metricsPath, { headers: recipientHeaders }), 403);
    const metrics = await request(metricsPath, { headers });
    assert.equal(metrics.status, 200);
    const observedMetrics = await metrics.json();
    assert.deepEqual(Object.keys(observedMetrics).sort(), [
      "attempts",
      "blocked",
      "cleanupLagMs",
      "lastSuccessAt",
      "queueAgeMs",
    ]);
    assert(
      Number.isSafeInteger(observedMetrics.attempts) &&
        Number.isSafeInteger(observedMetrics.blocked),
    );
    assert(
      Number.isSafeInteger(observedMetrics.lastSuccessAt) && observedMetrics.lastSuccessAt > 0,
    );
    let managedExport;
    if (exportDB) {
      const authHeader = { authorization: "Bearer " + exportToken };
      const exportPath = "/managed-export?kind=operations&workspace=" + workspace.workspaceId;
      const inventoryId = runId + "-" + schema + "-inventory";
      const pageId = runId + "-" + schema + "-page";
      await expectDenied(await request(exportPath + "&operation=" + pageId), 401);
      await expectDenied(
        await request("/managed-export?kind=operations&operation=" + pageId, {
          headers: authHeader,
        }),
        400,
      );
      const page = await request(exportPath + "&operation=" + pageId, { headers: authHeader });
      assert.equal(page.status, 200);
      const one = await page.json();
      assert.equal(one.items.length, 1);
      assert.equal(one.nextId, one.items[0].operationId);
      const inventory = await request(exportPath + "&inventory=1&operation=" + inventoryId, {
        headers: authHeader,
      });
      assert.equal(inventory.status, 200);
      const all = await inventory.json();
      assert.equal(all.nextId, null);
      assert.equal(all.items.length, 6);
      assert.deepEqual(all.items[0], one.items[0]);
      assert(
        all.items.every(
          (entry) =>
            entry.workspaceId === workspace.workspaceId &&
            entry.productId === schema &&
            entry.state === "completed" &&
            !Object.keys(entry).some((field) =>
              ["actor", "target", "intent", "name", "keyDigest"].includes(field),
            ),
        ),
      );
      managedExport = { pageId, inventoryId, operations: all.items.length };
    }
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
      strictSSO: {
        workspace: "globex",
        membership: "allowed by OpenFGA",
        discovery: "omitted",
        roles: [],
        scopedRead: denied.status,
      },
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
      lifecycle: { suspended: 204, unauthorized: 403, blockedResume: 409, resumed: 204 },
      ...(discoveryRebuild ? { discoveryRebuild } : {}),
      reconcile: {
        operator: 200,
        unauthorized: 403,
        confirmedCompleted: true,
        terminalExcluded: true,
      },
      ...(managedExport ? { managedExport } : {}),
      metrics: { operator: 200, unauthorized: 403, lastSuccessRecorded: true },
      otherTenant: 403,
    };
  } finally {
    if (federation && remote) {
      const names = ["HEIMDALL_FEDERATION_CREDENTIAL", "HEIMDALL_TENANT_SSO_POLICIES"];
      if (federation.cleanup)
        names.push(
          "HEIMDALL_DIRECTORY_OPERATOR_SUBJECT",
          "HEIMDALL_DIRECTORY_ORGANIZATION",
          "HEIMDALL_DIRECTORY_WORKSPACES",
        );
      for (const name of names) {
        const cleanup = spawnSync(
          "railway",
          [
            "variable",
            "set",
            "--service",
            "reference-" + schema,
            "--environment",
            "verification",
            "--skip-deploys",
            name + "=",
          ],
          { stdio: "ignore", timeout: 30000 },
        );
        assert.equal(cleanup.status, 0, "Could not clear disposable federation variables");
      }
    }
    if (child && child.exitCode === null) {
      child.kill();
      await once(child, "exit");
    }
  }
}

import { request as httpRequest } from "node:http";
import assert from "node:assert/strict";
import { randomBytes } from "node:crypto";
import { createHash } from "node:crypto";
import { spawn, spawnSync } from "node:child_process";
import { once } from "node:events";

export async function verifyReferenceProduct(input, authorize) {
  const { config, manifest, clientSecret, apiAudience, user, runId } = input;
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
  assert(/^[A-Za-z0-9:_-]+$/.test(principal));
  let sqlDiagnostic = "";
  sql.stderr.setEncoding("utf8");
  sql.stderr.on("data", (chunk) => {
    sqlDiagnostic = (sqlDiagnostic + chunk).slice(-8192);
  });
  sql.stdin.end(`
    INSERT INTO heimdall_managed.workspaces(product_id,environment,workspace_id,name,lifecycle,mutation_blocked,created_at,updated_at)
    VALUES ('${schema}','verification','acme','Acme','active',false,1,1), ('${schema}','verification','globex','Globex','active',false,1,1) ON CONFLICT DO NOTHING;
    INSERT INTO heimdall_managed.membership_candidates(product_id,environment,workspace_id,identity_hash,principal,observed_at)
    SELECT '${schema}','verification',workspace,sha256(convert_to('${principal}','UTF8')),'${principal}',1 FROM (VALUES ('acme'),('globex')) c(workspace);
    ${seed} ON CONFLICT DO NOTHING;
  `);
  const [sqlCode] = await once(sql, "exit");
  const sqlState =
    /ERROR:\s+([A-Z0-9]{5}):/.exec(sqlDiagnostic)?.[1] ?? "connection or client error";
  assert.equal(sqlCode, 0, "Reference fixture seeding failed " + sqlState);
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
          request.end();
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
    const start = await request("/auth/start");
    assert.equal(start.status, 303, `Reference ${schema} login start returned ${start.status}`);
    const binding = start.headers.getSetCookie()[0].split(";")[0];
    const target = await authorize(start.headers.get("location"));
    const complete = await request(target.pathname + target.search, {
      headers: { cookie: binding },
    });
    assert.equal(complete.status, 303, `Reference ${schema} callback returned ${complete.status}`);
    const cookie = complete.headers
      .getSetCookie()
      .find((value) => value.includes("-session="))
      .split(";")[0];
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
      otherTenant: 403,
    };
  } finally {
    if (child && child.exitCode === null) {
      child.kill();
      await once(child, "exit");
    }
  }
}

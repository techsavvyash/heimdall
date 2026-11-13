import assert from "node:assert/strict";
import { fork } from "node:child_process";
import { randomUUID } from "node:crypto";
import { once } from "node:events";
import { readFileSync } from "node:fs";
import { verifyReferenceProduct } from "./reference-products.mjs";

const issuer = process.env.HEIMDALL_PROBE_ZITADEL_ISSUER;
const pat = process.env.HEIMDALL_PROBE_ZITADEL_PAT;
const fga = process.env.HEIMDALL_PROBE_OPENFGA_URL;
const key = process.env.HEIMDALL_PROBE_OPENFGA_KEY;
assert.equal(process.env.HEIMDALL_REFERENCE_RAILWAY, "1");
for (const origin of [issuer, fga])
  assert(origin?.startsWith("https://") && new URL(origin).origin === origin);
assert(pat && key);
const runId = randomUUID();
const fixtures = JSON.parse(
  readFileSync(new URL("../../contracts/fixtures/tenant-model-v1.json", import.meta.url), "utf8"),
);
const api = async (origin, token, path, body, method = "POST") => {
  const response = await fetch(origin + path, {
    method,
    headers: { authorization: "Bearer " + token, "content-type": "application/json" },
    ...(body ? { body: JSON.stringify(body) } : {}),
    redirect: "error",
    signal: AbortSignal.timeout(15000),
  });
  assert(response.ok, `${path} HTTP ${response.status}`);
  const raw = await response.text();
  return raw ? JSON.parse(raw) : null;
};
const z = (path, body, method) => api(issuer, pat, path, body, method);
const f = (path, body, method) => api(fga, key, path, body, method);
const principal = (subject) =>
  "user:" + Buffer.from(JSON.stringify([issuer, subject])).toString("base64url");

async function receive(child, expected) {
  const timer = AbortSignal.timeout(180000);
  const message = await new Promise((resolve, reject) => {
    const done = (value) => {
      child.off("message", received);
      child.off("exit", exited);
      timer.removeEventListener("abort", exited);
      value();
    };
    const exited = () => done(() => reject(new Error("Directory fixture stopped or timed out")));
    const received = (message) => done(() => resolve(message));
    child.once("message", received);
    child.once("exit", exited);
    timer.addEventListener("abort", exited, { once: true });
  });
  assert.equal(message.type, expected, "Directory fixture request failed");
  return message;
}

async function verifyProduct(product) {
  const suffix = runId + "-" + product;
  const { organizationId } = await z("/v2/organizations", { name: "heimdall-switch-" + suffix });
  const children = [];
  let store;
  try {
    const { projectId } = await z("/zitadel.project.v2.ProjectService/CreateProject", {
      organizationId,
      name: "switch-" + suffix,
    });
    const redirectUri = `https://reference-${product}-verification.up.railway.app/auth/callback`;
    const app = await z("/zitadel.application.v2.ApplicationService/CreateApplication", {
      projectId,
      name: "switch-web",
      oidcConfiguration: {
        redirectUris: [redirectUri],
        responseTypes: ["OIDC_RESPONSE_TYPE_CODE"],
        grantTypes: ["OIDC_GRANT_TYPE_AUTHORIZATION_CODE", "OIDC_GRANT_TYPE_REFRESH_TOKEN"],
        applicationType: "OIDC_APP_TYPE_WEB",
        authMethodType: "OIDC_AUTH_METHOD_TYPE_BASIC",
        accessTokenType: "OIDC_TOKEN_TYPE_JWT",
      },
    });
    const shared = { ...app.oidcConfiguration, projectId, redirectUri };
    const directories = new Map();
    for (const [workspace, protocol] of [
      ["acme", "saml"],
      ["globex", "oidc"],
    ]) {
      const child = fork(new URL("./federation.mjs", import.meta.url), [], {
        env: {
          ...process.env,
          HEIMDALL_PROBE_SWITCH_CHILD: "1",
          HEIMDALL_PROBE_NARROW_LOGIN: "true",
          HEIMDALL_PROBE_FEDERATION_PROTOCOL: protocol,
          HEIMDALL_PROBE_SHARED_CLIENT_JSON: JSON.stringify(shared),
          HEIMDALL_PROBE_SHARED_EMAIL: `switch-${suffix}@example.invalid`,
        },
        stdio: ["ignore", "ignore", "pipe", "ipc"],
      });
      const finished = once(child, "exit");
      children.push({ child, finished });
      child.stderr.setEncoding("utf8");
      child.stderr.on("data", (chunk) => {
        const run = chunk.match(/Federation probe run: [a-f0-9-]+/);
        if (run) console.error(product, workspace, run[0]);
        const failure = chunk.match(/(?:AssertionError.*|Error:.*HTTP [0-9]{3}.*)/)?.[0];
        if (failure) console.error(product, workspace, failure.slice(0, 220));
      });
      directories.set(workspace, { child, ...(await receive(child, "ready")) });
    }
    const acme = directories.get("acme");
    const globex = directories.get("globex");
    assert.notEqual(acme.subject, globex.subject);
    assert.notEqual(acme.policy.organizationId, globex.policy.organizationId);
    assert.equal(acme.apiAudience, globex.apiAudience);
    store = await f("/stores", { name: "heimdall-switch-" + suffix });
    const fixture = fixtures.find((entry) => entry.name === product);
    const manifest = structuredClone(fixture.manifest);
    manifest.clients.web.redirectUris = [redirectUri];
    const model = await f(`/stores/${store.id}/authorization-models`, fixture.expected.model);
    const modelId = model.authorization_model_id;
    const role = fixture.expected.roles[manifest.ownerRole].assignment;
    await f(`/stores/${store.id}/write`, {
      authorization_model_id: modelId,
      writes: {
        tuple_keys: [
          ...["acme", "globex"].map((workspace) => ({
            user: principal(acme.subject),
            object: "workspace:" + workspace,
            relation: "member",
          })),
          { user: principal(acme.subject), object: "workspace:acme", relation: role },
          ...["member", role].map((relation) => ({
            user: principal(globex.subject),
            object: "workspace:globex",
            relation,
          })),
        ],
      },
    });
    return await verifyReferenceProduct(
      {
        config: {
          schemaVersion: 1,
          productId: product,
          environment: "verification",
          issuer,
          audience: projectId,
          clientId: shared.clientId,
          zitadelProjectId: projectId,
          clientKey: "web",
          manifestVersion: manifest.version,
          openfga: { url: fga, storeId: store.id, modelId },
        },
        manifest,
        clientSecret: shared.clientSecret,
        apiAudience: acme.apiAudience,
        user: acme.subject,
        recipient: globex.subject,
        runId,
        federation: {
          principal: principal(globex.subject),
          policies: { acme: acme.policy, globex: globex.policy },
          credential: acme.credential,
        },
      },
      async (location, tenant) => {
        const { child } = directories.get(tenant);
        const response = receive(child, "callback");
        child.send({ type: "authorize", location });
        const callback = new URL((await response).location);
        assert.equal(callback.origin + callback.pathname, redirectUri);
        return callback.pathname + callback.search;
      },
    );
  } finally {
    for (const { child } of children) if (child.connected) child.disconnect();
    const cleanup = await Promise.allSettled([
      ...children.map(async ({ finished }) =>
        assert.equal((await finished)[0], 0, "Directory cleanup failed"),
      ),
      z(`/v2/organizations/${organizationId}`, undefined, "DELETE"),
      ...(store ? [f(`/stores/${store.id}`, undefined, "DELETE")] : []),
    ]);
    assert(
      cleanup.every((result) => result.status === "fulfilled"),
      "Disposable product cleanup failed",
    );
  }
}

const results = await Promise.allSettled(["ledger", "support"].map(verifyProduct));
for (const result of results)
  if (result.status === "rejected") console.error(result.reason.message);
assert(
  results.every((result) => result.status === "fulfilled"),
  "Two-directory reference verification failed",
);
console.log(JSON.stringify({ runId, results: results.map((result) => result.value) }, null, 2));

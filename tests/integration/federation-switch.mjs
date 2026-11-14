import assert from "node:assert/strict";
import { fork } from "node:child_process";
import { randomBytes, randomUUID } from "node:crypto";
import { once } from "node:events";
import { readFileSync } from "node:fs";
import { verifyReferenceProduct } from "./reference-products.mjs";

const issuer = process.env.HEIMDALL_PROBE_ZITADEL_ISSUER;
const pat = process.env.HEIMDALL_PROBE_ZITADEL_PAT;
const loginPat = process.env.HEIMDALL_PROBE_ZITADEL_LOGIN_PAT;
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
  let passwordSession;
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
    const verifyCleanup = process.env.HEIMDALL_PROBE_STRICT_DIRECTORY_CLEANUP === "1";
    const target = verifyCleanup
      ? await z("/v2/users/new", {
          organizationId: globex.policy.organizationId,
          username: "cleanup-" + suffix,
          human: {
            profile: { givenName: "Cleanup", familyName: "Probe" },
            email: { email: `cleanup-${suffix}@example.invalid`, isVerified: true },
          },
        })
      : null;
    const assignment =
      fixture.expected.roles[product === "ledger" ? "viewer" : "reader"].assignment;
    const verifyPolicy = process.env.HEIMDALL_PROBE_SHARED_SSO_POLICY === "1";
    const password = "Aa1!" + randomBytes(24).toString("hex");
    const localUser = verifyPolicy
      ? await z("/v2/users/new", {
          organizationId: acme.policy.organizationId,
          username: "password-" + suffix,
          human: {
            profile: { givenName: "Local", familyName: "Probe" },
            email: { email: `password-${suffix}@example.invalid`, isVerified: true },
            password: { password, changeRequired: false },
          },
        })
      : null;
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
          ...(target
            ? ["member", assignment].map((relation) => ({
                user: principal(target.id),
                object: "workspace:globex",
                relation,
              }))
            : []),
          ...(localUser
            ? ["member", assignment].map((relation) => ({
                user: principal(localUser.id),
                object: "workspace:acme",
                relation,
              }))
            : []),
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
          policies: {
            acme: acme.policy,
            globex: globex.policy,
            ...(verifyPolicy ? { "acme-peer": acme.policy } : {}),
          },
          credential: acme.credential,
          ...(localUser
            ? {
                sharedPolicy: {
                  async authorizePassword(location) {
                    assert(loginPat);
                    passwordSession = await api(issuer, loginPat, "/v2/sessions", {
                      checks: { user: { userId: localUser.id }, password: { password } },
                      lifetime: "600s",
                    });
                    async function follow(url) {
                      assert.equal(new URL(url).origin, issuer);
                      const response = await fetch(url, {
                        redirect: "manual",
                        signal: AbortSignal.timeout(15000),
                      });
                      assert(
                        [302, 303, 307].includes(response.status),
                        "Expected a password login redirect",
                      );
                      const next = response.headers.get("location");
                      await response.body?.cancel();
                      return new URL(next, url);
                    }
                    const login = await follow(location);
                    const id = login.searchParams.get("authRequest");
                    assert(id);
                    const result = await api(issuer, loginPat, `/v2/oidc/auth_requests/${id}`, {
                      session: {
                        sessionId: passwordSession.sessionId,
                        sessionToken: passwordSession.sessionToken,
                      },
                    });
                    let callback = new URL(result.callbackUrl);
                    for (let attempt = 0; attempt < 4 && callback.origin === issuer; attempt++)
                      callback = await follow(callback.href);
                    assert.equal(callback.origin + callback.pathname, redirectUri);
                    assert(callback.searchParams.has("code"));
                    return callback.pathname + callback.search;
                  },
                  grantPeer: () =>
                    f(`/stores/${store.id}/write`, {
                      authorization_model_id: modelId,
                      writes: {
                        tuple_keys: ["member", assignment].map((relation) => ({
                          user: principal(acme.subject),
                          object: "workspace:acme-peer",
                          relation,
                        })),
                      },
                    }),
                },
              }
            : {}),
          ...(target
            ? {
                cleanup: {
                  subject: target.id,
                  operator: globex.subject,
                  organizationId: globex.policy.organizationId,
                  async readRelations() {
                    const result = await f(`/stores/${store.id}/read`, {
                      tuple_key: { user: principal(target.id), object: "workspace:globex" },
                      consistency: "HIGHER_CONSISTENCY",
                    });
                    return (result.tuples ?? []).map(({ key }) => key.relation).sort();
                  },
                },
              }
            : {}),
        },
      },
      async (location, tenant) => {
        const { child } = directories.get(tenant === "acme-peer" ? "acme" : tenant);
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
      ...(passwordSession
        ? [api(issuer, loginPat, `/v2/sessions/${passwordSession.sessionId}`, undefined, "DELETE")]
        : []),
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

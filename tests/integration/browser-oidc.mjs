import assert from "node:assert/strict";
import { randomUUID, randomBytes, createHash, createPublicKey, verify } from "node:crypto";
import { readFile, writeFile, unlink, chmod } from "node:fs/promises";

const issuer = process.env.HEIMDALL_PROBE_ZITADEL_ISSUER;
const pat = process.env.HEIMDALL_PROBE_ZITADEL_PAT;
const loginPat = process.env.HEIMDALL_PROBE_ZITADEL_LOGIN_PAT;
const stateFile = process.env.HEIMDALL_PROBE_BROWSER_STATE;
const command = process.argv[2];
assert(issuer && pat && loginPat && stateFile);
assert(new URL(issuer).origin === issuer && issuer.startsWith("https://"));
assert(["setup", "verify", "cleanup"].includes(command));
async function api(method, path, body, token = pat) {
  const r = await fetch(issuer + path, {
    method,
    headers: { "content-type": "application/json", authorization: `Bearer ${token}` },
    ...(body === undefined ? {} : { body: JSON.stringify(body) }),
    redirect: "error",
    signal: AbortSignal.timeout(15000),
  });
  assert(r.status >= 200 && r.status < 300, `Browser fixture API: HTTP ${r.status}`);
  const text = await r.text();
  return text ? JSON.parse(text) : null;
}
let state;
async function save() {
  await writeFile(stateFile, JSON.stringify(state), { mode: 0o600 });
  await chmod(stateFile, 0o600);
}
if (command === "setup") {
  // Exclusive file creation prevents replacing another live browser fixture.
  state = { runId: randomUUID(), issuer, sessions: [] };
  await writeFile(stateFile, JSON.stringify(state), { flag: "wx", mode: 0o600 });
  state.organizationId = (
    await api("POST", "/v2/organizations", { name: `heimdall-browser-${state.runId}` })
  ).organizationId;
  await save();
  state.username = `browser-${state.runId}`;
  state.password = `Aa1!${state.runId}`;
  state.userId = (
    await api("POST", "/v2/users/new", {
      organizationId: state.organizationId,
      username: state.username,
      human: {
        profile: { givenName: "Browser", familyName: "Probe" },
        email: { email: `${state.username}@example.invalid`, isVerified: true },
        password: { password: state.password, changeRequired: false },
      },
    })
  ).id;
  state.projectId = (
    await api("POST", "/zitadel.project.v2.ProjectService/CreateProject", {
      organizationId: state.organizationId,
      name: "browser-product",
    })
  ).projectId;
  state.redirectUri = "https://heimdall-browser.example.invalid/callback";
  state.client = (
    await api("POST", "/zitadel.application.v2.ApplicationService/CreateApplication", {
      projectId: state.projectId,
      name: "browser-client",
      oidcConfiguration: {
        redirectUris: [state.redirectUri],
        responseTypes: ["OIDC_RESPONSE_TYPE_CODE"],
        grantTypes: ["OIDC_GRANT_TYPE_AUTHORIZATION_CODE"],
        applicationType: "OIDC_APP_TYPE_WEB",
        authMethodType: "OIDC_AUTH_METHOD_TYPE_BASIC",
        accessTokenType: "OIDC_TOKEN_TYPE_JWT",
      },
    })
  ).oidcConfiguration;
  assert(state.client.clientId && state.client.clientSecret && !state.client.nonCompliant);
  state.verifier = randomBytes(32).toString("base64url");
  state.nonce = randomUUID();
  state.oauthState = randomUUID();
  const query = new URLSearchParams({
    client_id: state.client.clientId,
    redirect_uri: state.redirectUri,
    response_type: "code",
    scope: `openid profile urn:zitadel:iam:org:project:id:${state.projectId}:aud urn:zitadel:iam:org:id:${state.organizationId}`,
    prompt: "login",
    login_hint: state.username,
    nonce: state.nonce,
    state: state.oauthState,
    code_challenge_method: "S256",
    code_challenge: createHash("sha256").update(state.verifier).digest("base64url"),
  });
  state.authorizationUrl = `${issuer}/oauth/v2/authorize?${query}`;
  await save();
  // Only synthetic interactive credentials, never API PATs or the client secret.
  console.log(
    JSON.stringify({
      runId: state.runId,
      authorizationUrl: state.authorizationUrl,
      username: state.username,
      password: state.password,
    }),
  );
} else {
  state = JSON.parse(await readFile(stateFile, "utf8"));
  assert.equal(state.issuer, issuer);
  if (command === "verify") {
    const callback = new URL(process.env.HEIMDALL_PROBE_BROWSER_CALLBACK);
    assert.equal(callback.origin + callback.pathname, state.redirectUri);
    assert.equal(callback.searchParams.get("state"), state.oauthState);
    assert(!callback.searchParams.get("error"));
    const code = callback.searchParams.get("code");
    assert(code);
    const response = await fetch(`${issuer}/oauth/v2/token`, {
      method: "POST",
      headers: {
        "content-type": "application/x-www-form-urlencoded",
        authorization: `Basic ${Buffer.from(`${state.client.clientId}:${state.client.clientSecret}`).toString("base64")}`,
      },
      body: new URLSearchParams({
        grant_type: "authorization_code",
        code,
        redirect_uri: state.redirectUri,
        code_verifier: state.verifier,
      }),
      redirect: "error",
      signal: AbortSignal.timeout(15000),
    });
    assert.equal(response.status, 200);
    const tokens = await response.json();
    const discovery = await api("GET", "/.well-known/openid-configuration", undefined, "");
    assert.equal(new URL(discovery.jwks_uri).origin, issuer);
    const { keys } = await api("GET", new URL(discovery.jwks_uri).pathname, undefined, "");
    function claims(token, audience) {
      const parts = token.split(".");
      assert.equal(parts.length, 3);
      const header = JSON.parse(Buffer.from(parts[0], "base64url"));
      const value = JSON.parse(Buffer.from(parts[1], "base64url"));
      assert.equal(header.alg, "RS256");
      const key = keys.find((k) => k.kid === header.kid && k.kty === "RSA");
      assert(key);
      assert(
        verify(
          "RSA-SHA256",
          Buffer.from(parts.slice(0, 2).join(".")),
          createPublicKey({ key, format: "jwk" }),
          Buffer.from(parts[2], "base64url"),
        ),
      );
      assert.equal(value.iss, issuer);
      assert.equal(value.sub, state.userId);
      assert([value.aud].flat().includes(audience));
      assert(value.exp > Date.now() / 1000 && value.iat <= Date.now() / 1000 + 5);
      return value;
    }
    const id = claims(tokens.id_token, state.client.clientId);
    claims(tokens.access_token, state.projectId);
    assert.equal(id.nonce, state.nonce);
    assert(id.sid);
    state.sessions.push(id.sid);
    await save();
    const session = (await api("GET", `/v2/sessions/${id.sid}`, undefined, loginPat)).session;
    assert.equal(session.factors.user.id, state.userId);
    assert.equal(session.factors.user.organizationId, state.organizationId);
    assert(session.factors.password?.verifiedAt && !session.factors.intent);
    assert.equal(id.auth_time, Math.floor(Date.parse(session.factors.password.verifiedAt) / 1000));
    console.log(
      JSON.stringify(
        {
          suite: "browser-oidc-v1",
          runId: state.runId,
          observedAt: new Date().toISOString(),
          observations: {
            callbackState: "matched",
            nonceSignatureIssuerSubjectAudience: "verified",
            pkceCodeExchange: "passed",
            signedSessionIdMatchesAPI: true,
            signedAuthTimeMatchesPasswordFactor: true,
            passwordFactorPresent: true,
            enterpriseIntentAbsent: true,
            amr: id.amr ?? null,
          },
          limitations: [
            "manual browser interaction; standalone test callback verifier",
            "not SDK callback claim or durable session persistence",
            "password login only; no enterprise browser federation",
          ],
        },
        null,
        2,
      ),
    );
  } else {
    for (const id of state.sessions) await api("DELETE", `/v2/sessions/${id}`, undefined, loginPat);
    if (state.organizationId) await api("DELETE", `/v2/organizations/${state.organizationId}`);
    await unlink(stateFile);
    console.log(
      JSON.stringify({
        runId: state.runId,
        cleanup: "owned sessions and organization deleted; protected local fixture removed",
      }),
    );
  }
}

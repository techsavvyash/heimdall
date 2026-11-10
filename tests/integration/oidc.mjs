import assert from "node:assert/strict";
import {
  randomUUID,
  randomBytes,
  createHash,
  createPublicKey,
  generateKeyPairSync,
  sign,
  verify,
} from "node:crypto";

const issuer = process.env.HEIMDALL_PROBE_ZITADEL_ISSUER;
const pat = process.env.HEIMDALL_PROBE_ZITADEL_PAT;
const loginPat = process.env.HEIMDALL_PROBE_ZITADEL_LOGIN_PAT;
assert(
  issuer && pat && loginPat,
  "Set verification issuer, bootstrap PAT and dedicated Login client PAT",
);
assert(new URL(issuer).origin === issuer && issuer.startsWith("https://"));
const runId = randomUUID();
console.error(`OIDC probe run: ${runId}`);
let orgId;
const sessions = [];
const observations = [];
async function req(method, path, body, credential = pat) {
  const response = await fetch(issuer + path, {
    method,
    headers: {
      "content-type": "application/json",
      ...(credential ? { authorization: `Bearer ${credential}` } : {}),
    },
    ...(body === undefined ? {} : { body: JSON.stringify(body) }),
    redirect: "error",
    signal: AbortSignal.timeout(15000),
  });
  const text = await response.text();
  return { status: response.status, body: text ? JSON.parse(text) : null };
}
async function ok(method, path, body, credential) {
  const r = await req(method, path, body, credential);
  assert(
    r.status >= 200 && r.status < 300,
    `${method} ${path}: HTTP ${r.status}, code ${r.body?.code ?? "unknown"}`,
  );
  return r.body;
}
async function form(path, client, values) {
  const r = await fetch(issuer + path, {
    method: "POST",
    headers: {
      "content-type": "application/x-www-form-urlencoded",
      authorization: `Basic ${Buffer.from(`${client.clientId}:${client.clientSecret}`).toString("base64")}`,
    },
    body: new URLSearchParams(values),
    redirect: "error",
    signal: AbortSignal.timeout(15000),
  });
  const text = await r.text();
  return { status: r.status, body: text ? JSON.parse(text) : null };
}
async function redirect(url) {
  assert(new URL(url).origin === issuer, "Only follow issuer redirects");
  const r = await fetch(url, { redirect: "manual", signal: AbortSignal.timeout(15000) });
  assert([302, 303, 307].includes(r.status), `Expected redirect, got ${r.status}`);
  return new URL(r.headers.get("location"), url);
}
function validateJWT(token, keys, audience, subject, nonce) {
  const parts = token.split(".");
  assert(parts.length === 3, "Expected JWT");
  const header = JSON.parse(Buffer.from(parts[0], "base64url"));
  const claims = JSON.parse(Buffer.from(parts[1], "base64url"));
  assert(header.alg === "RS256", "Pinned token algorithm must be RS256");
  const key = keys.find((k) => k.kid === header.kid && k.kty === "RSA");
  assert(key, "No matching signing key");
  assert(
    verify(
      "RSA-SHA256",
      Buffer.from(parts.slice(0, 2).join(".")),
      createPublicKey({ key, format: "jwk" }),
      Buffer.from(parts[2], "base64url"),
    ),
    "Invalid signature",
  );
  assert(claims.iss === issuer && claims.sub === subject, "Issuer/subject mismatch");
  assert([claims.aud].flat().includes(audience), "Audience mismatch");
  assert(
    claims.exp > Date.now() / 1000 && claims.iat <= Date.now() / 1000 + 5,
    "Invalid token time",
  );
  if (nonce) assert(claims.nonce === nonce, "Nonce mismatch");
  return claims;
}
try {
  const org = await ok("POST", "/v2/organizations", { name: `heimdall-oidc-${runId}` });
  orgId = org.organizationId;
  assert(orgId);
  const password = `Aa1!${randomBytes(24).toString("hex")}`;
  const user = await ok("POST", "/v2/users/new", {
    organizationId: orgId,
    username: `oidc-${runId}`,
    human: {
      profile: { givenName: "Heimdall", familyName: "Probe" },
      email: { email: `oidc-${runId}@example.invalid`, isVerified: true },
      password: { password, changeRequired: false },
    },
  });
  assert(user.id);
  const clients = [];
  for (const product of ["ledger", "support"]) {
    const project = await ok("POST", "/zitadel.project.v2.ProjectService/CreateProject", {
      organizationId: orgId,
      name: `heimdall-${product}-${runId}`,
    });
    const redirectUri = `https://${product}.example.invalid/callback`;
    const app = await ok("POST", "/zitadel.application.v2.ApplicationService/CreateApplication", {
      projectId: project.projectId,
      name: product,
      oidcConfiguration: {
        redirectUris: [redirectUri],
        responseTypes: ["OIDC_RESPONSE_TYPE_CODE"],
        grantTypes: ["OIDC_GRANT_TYPE_AUTHORIZATION_CODE", "OIDC_GRANT_TYPE_REFRESH_TOKEN"],
        applicationType: "OIDC_APP_TYPE_WEB",
        authMethodType: "OIDC_AUTH_METHOD_TYPE_BASIC",
        accessTokenType: "OIDC_TOKEN_TYPE_JWT",
      },
    });
    assert(!app.oidcConfiguration.nonCompliant, "OIDC app must be compliant");
    clients.push({ ...app.oidcConfiguration, projectId: project.projectId, redirectUri, product });
  }
  const discovery = await ok("GET", "/.well-known/openid-configuration", undefined, "");
  assert(new URL(discovery.jwks_uri).origin === issuer);
  const { keys } = await ok("GET", new URL(discovery.jwks_uri).pathname, undefined, "");
  const untrustedKey = generateKeyPairSync("rsa", { modulusLength: 2048 }).privateKey;
  let session = await ok(
    "POST",
    "/v2/sessions",
    { checks: { user: { userId: user.id }, password: { password } }, lifetime: "3600s" },
    loginPat,
  );
  sessions.push(session.sessionId);
  const otherUser = await ok("POST", "/v2/users/new", {
    organizationId: orgId,
    username: `other-${runId}`,
    human: {
      profile: { givenName: "Other", familyName: "Probe" },
      email: { email: `other-${runId}@example.invalid`, isVerified: true },
      password: { password, changeRequired: false },
    },
  });
  const otherSession = await ok(
    "POST",
    "/v2/sessions",
    {
      checks: { user: { userId: otherUser.id }, password: { password } },
      lifetime: "3600s",
    },
    loginPat,
  );
  sessions.push(otherSession.sessionId);
  const tokens = [];
  async function authorize(client, apiAudience = false) {
    const verifier = randomBytes(32).toString("base64url");
    const nonce = randomUUID();
    const state = randomUUID();
    const query = new URLSearchParams({
      client_id: client.clientId,
      redirect_uri: client.redirectUri,
      response_type: "code",
      scope: `openid profile offline_access urn:zitadel:iam:org:project:id:${client.projectId}:aud${apiAudience ? " urn:zitadel:iam:org:project:id:zitadel:aud" : ""}`,
      state,
      nonce,
      code_challenge: createHash("sha256").update(verifier).digest("base64url"),
      code_challenge_method: "S256",
    });
    const login = await redirect(`${issuer}/oauth/v2/authorize?${query}`);
    const authRequestId = login.searchParams.get("authRequest");
    assert(authRequestId, "Authorization request ID missing");
    const callback = await ok(
      "POST",
      `/v2/oidc/auth_requests/${authRequestId}`,
      { session: { sessionId: session.sessionId, sessionToken: session.sessionToken } },
      loginPat,
    );
    let target = new URL(callback.callbackUrl);
    for (let i = 0; i < 4 && target.origin === issuer; i++) target = await redirect(target.href);
    assert(
      target.origin + target.pathname === client.redirectUri,
      "Unexpected callback destination",
    );
    assert(target.searchParams.get("state") === state, "State mismatch");
    const code = target.searchParams.get("code");
    assert(code, `No authorization code; error ${target.searchParams.get("error")}`);
    return { verifier, nonce, code };
  }
  for (const client of clients) {
    const auth = await authorize(client);
    const result = await form("/oauth/v2/token", client, {
      grant_type: "authorization_code",
      code: auth.code,
      redirect_uri: client.redirectUri,
      code_verifier: auth.verifier,
    });
    assert(result.status === 200, `Code exchange failed: ${result.status} ${result.body.error}`);
    const id = validateJWT(result.body.id_token, keys, client.clientId, user.id, auth.nonce);
    const access = validateJWT(result.body.access_token, keys, client.projectId, user.id);
    const withoutAPIAudience = await req(
      "GET",
      `/v2/sessions/${id.sid}`,
      undefined,
      result.body.access_token,
    );
    assert.equal(
      withoutAPIAudience.status,
      401,
      "Product-only token must not access ZITADEL session API",
    );
    const apiAuth = await authorize(client, true);
    const apiToken = await form("/oauth/v2/token", client, {
      grant_type: "authorization_code",
      code: apiAuth.code,
      redirect_uri: client.redirectUri,
      code_verifier: apiAuth.verifier,
    });
    assert.equal(apiToken.status, 200);
    const apiID = validateJWT(
      apiToken.body.id_token,
      keys,
      client.clientId,
      user.id,
      apiAuth.nonce,
    );
    const apiAccess = validateJWT(apiToken.body.access_token, keys, client.projectId, user.id);
    const otherProduct = clients.find((candidate) => candidate !== client);
    assert(![apiAccess.aud].flat().includes(otherProduct.projectId));
    const otherProductAPIIntrospection = await form("/oauth/v2/introspect", otherProduct, {
      token: apiToken.body.access_token,
    });
    assert.equal(otherProductAPIIntrospection.status, 200);
    assert.equal(otherProductAPIIntrospection.body.active, false);
    assert.equal(apiID.sid, session.sessionId);
    const ownSession = await req(
      "GET",
      `/v2/sessions/${apiID.sid}`,
      undefined,
      apiToken.body.access_token,
    );
    assert.equal(ownSession.status, 200, "API-audience user token must read own session");
    assert.equal(ownSession.body.session.factors.user.id, user.id);
    assert.equal(ownSession.body.session.factors.user.organizationId, orgId);
    const foreignSession = await req(
      "GET",
      `/v2/sessions/${otherSession.sessionId}`,
      undefined,
      apiToken.body.access_token,
    );
    assert(
      [403, 404].includes(foreignSession.status),
      `Another user's session must be denied: ${foreignSession.status}`,
    );
    const ownUser = await req("GET", `/v2/users/${user.id}`, undefined, apiToken.body.access_token);
    assert.equal(ownUser.status, 200, "API-audience user token must read own identity state");
    assert.equal(ownUser.body.user.state, "USER_STATE_ACTIVE");
    observations.push({
      product: client.product,
      selfServiceEvidence: {
        productOnlySessionStatus: withoutAPIAudience.status,
        apiAudienceOwnSessionStatus: ownSession.status,
        anotherUserSessionStatus: foreignSession.status,
        ownIdentityStatus: ownUser.status,
        otherProductIntrospectionActive: otherProductAPIIntrospection.body.active,
        credential: "user access token; no PAT or session token",
      },
    });
    const other = clients.find((c) => c !== client);
    assert(![access.aud].flat().includes(other.projectId), "Cross-product access audience");
    assert.throws(
      () => validateJWT(result.body.id_token, keys, other.clientId, user.id),
      /Audience mismatch/,
    );
    assert.throws(
      () => validateJWT(result.body.id_token, [], client.clientId, user.id),
      /No matching signing key/,
    );
    const introspection = await form("/oauth/v2/introspect", client, {
      token: result.body.access_token,
    });
    assert(
      introspection.status === 200 && introspection.body.active === true,
      "Fresh token must introspect active",
    );
    const original = result.body.access_token.split(".");
    const originalHeader = JSON.parse(Buffer.from(original[0], "base64url"));
    const keyRejections = [];
    for (const [scenario, kid] of [
      ["unknown signing key", `heimdall-unknown-${runId}`],
      ["known key ID with untrusted signature", originalHeader.kid],
    ]) {
      const header = Buffer.from(JSON.stringify({ ...originalHeader, kid })).toString("base64url");
      const input = `${header}.${original[1]}`;
      const forged = `${input}.${sign("RSA-SHA256", Buffer.from(input), untrustedKey).toString("base64url")}`;
      assert.throws(
        () => validateJWT(forged, keys, client.projectId, user.id),
        scenario === "unknown signing key" ? /No matching signing key/ : /Invalid signature/,
      );
      const rejected = await form("/oauth/v2/introspect", client, { token: forged });
      assert(
        rejected.status === 200 && rejected.body.active === false,
        `${scenario}: forged token must not introspect active`,
      );
      keyRejections.push({
        scenario,
        localValidation: "rejected",
        introspectionStatus: rejected.status,
        active: rejected.body.active,
      });
    }
    assert(result.body.refresh_token, "Refresh token missing");
    const wrongClient = await form("/oauth/v2/introspect", other, {
      token: result.body.access_token,
    });
    assert(
      wrongClient.status === 200 && wrongClient.body.active === false,
      "Other product must not introspect this token as active",
    );
    const refreshed = await form("/oauth/v2/token", client, {
      grant_type: "refresh_token",
      refresh_token: result.body.refresh_token,
    });
    assert(
      refreshed.status === 200,
      `Active user's refresh failed: ${refreshed.status} ${refreshed.body.error}`,
    );
    validateJWT(refreshed.body.access_token, keys, client.projectId, user.id);
    assert(refreshed.body.refresh_token, "Rotated refresh token missing");
    tokens.push({ client, ...refreshed.body });
    observations.push({
      product: client.product,
      signatureIssuerAudienceNonce: "verified",
      subject: "same human",
      amr: id.amr,
      accessAudiences: access.aud,
      introspectionActive: true,
      keyRejections,
      otherProductIntrospectionActive: false,
      refreshBeforeDeactivation: "succeeded",
    });
  }
  const badAuth = await authorize(clients[0]);
  const bad = await form("/oauth/v2/token", clients[0], {
    grant_type: "authorization_code",
    code: badAuth.code,
    redirect_uri: clients[0].redirectUri,
    code_verifier: randomBytes(32).toString("base64url"),
  });
  assert(bad.status === 400 && bad.body.error === "invalid_grant", "Wrong PKCE verifier must fail");
  const originalSession = session;
  session = await ok(
    "POST",
    "/v2/sessions",
    { checks: { user: { userId: user.id }, password: { password } }, lifetime: "3600s" },
    loginPat,
  );
  sessions.push(session.sessionId);
  const logoutAuth = await authorize(clients[0]);
  const logoutToken = await form("/oauth/v2/token", clients[0], {
    grant_type: "authorization_code",
    code: logoutAuth.code,
    redirect_uri: clients[0].redirectUri,
    code_verifier: logoutAuth.verifier,
  });
  assert(logoutToken.status === 200, "Logout test requires a live token");
  await ok("DELETE", `/v2/sessions/${session.sessionId}`, undefined, loginPat);
  sessions.splice(sessions.indexOf(session.sessionId), 1);
  const logoutState = await form("/oauth/v2/introspect", clients[0], {
    token: logoutToken.body.access_token,
  });
  assert(
    logoutState.status === 200 && logoutState.body.active === false,
    "Deleted provider session must introspect inactive",
  );
  const logoutRefresh = await form("/oauth/v2/token", clients[0], {
    grant_type: "refresh_token",
    refresh_token: logoutToken.body.refresh_token,
  });
  assert(
    logoutRefresh.status === 200,
    "Pinned session-deletion refresh behavior changed; review logout integration",
  );
  const refreshedLogoutState = await form("/oauth/v2/introspect", clients[0], {
    token: logoutRefresh.body.access_token,
  });
  assert(refreshedLogoutState.status === 200);
  const revoked = await form("/oauth/v2/revoke", clients[0], {
    token: logoutRefresh.body.refresh_token,
    token_type_hint: "refresh_token",
  });
  assert(revoked.status === 200, "Explicit refresh-token revocation failed");
  const afterRevoke = await form("/oauth/v2/token", clients[0], {
    grant_type: "refresh_token",
    refresh_token: logoutRefresh.body.refresh_token,
  });
  assert(
    afterRevoke.status === 400 &&
      ["invalid_grant", "invalid_request"].includes(afterRevoke.body.error),
    "Revoked refresh token must fail",
  );
  const afterRefreshRevokeState = await form("/oauth/v2/introspect", clients[0], {
    token: logoutRefresh.body.access_token,
  });
  assert(afterRefreshRevokeState.status === 200);
  const accessRevoked = await form("/oauth/v2/revoke", clients[0], {
    token: logoutRefresh.body.access_token,
    token_type_hint: "access_token",
  });
  assert(accessRevoked.status === 200);
  const afterBothRevoked = await form("/oauth/v2/introspect", clients[0], {
    token: logoutRefresh.body.access_token,
  });
  assert(
    afterBothRevoked.status === 200 && afterBothRevoked.body.active === false,
    "Explicitly revoked access token must be inactive",
  );
  observations.push({
    providerSessionDeletion: {
      introspectionActive: logoutState.body.active,
      refreshStatus: logoutRefresh.status,
      refreshedAccessTokenActive: refreshedLogoutState.body.active,
      explicitRefreshRevocationStatus: revoked.status,
      accessActiveAfterRefreshRevocation: afterRefreshRevokeState.body.active,
      explicitAccessRevocationStatus: accessRevoked.status,
      accessActiveAfterBothRevocations: afterBothRevoked.body.active,
      refreshAfterExplicitRevocationStatus: afterRevoke.status,
      refreshAfterExplicitRevocationError: afterRevoke.body.error,
    },
  });
  session = originalSession;
  const unaffected = await form("/oauth/v2/introspect", tokens[0].client, {
    token: tokens[0].access_token,
  });
  assert(
    unaffected.status === 200 && unaffected.body.active === true,
    "Deleting a separate session must not revoke this session",
  );
  await ok("POST", `/v2/users/${user.id}/deactivate`, {});
  const inactive = await ok("GET", `/v2/users/${user.id}`);
  assert(inactive.user.state === "USER_STATE_INACTIVE");
  for (const token of tokens) {
    const state = await form("/oauth/v2/introspect", token.client, { token: token.access_token });
    assert(
      state.status === 200 && state.body.active === false,
      "Inactive identity must not introspect active",
    );
    const refresh = await form("/oauth/v2/token", token.client, {
      grant_type: "refresh_token",
      refresh_token: token.refresh_token,
    });
    assert(
      refresh.status === 400 && ["invalid_request", "invalid_grant"].includes(refresh.body.error),
      "Inactive identity refresh must fail",
    );
    observations.push({
      product: token.client.product,
      afterDeactivation: {
        authoritativeUserState: inactive.user.state,
        introspectionActive: state.body.active,
        refreshStatus: refresh.status,
        refreshError: refresh.body.error ?? null,
      },
    });
  }
  await ok("DELETE", `/v2/users/${user.id}`);
  assert((await req("GET", `/v2/users/${user.id}`)).status === 404, "Deleted user must be missing");
  for (const token of tokens) {
    const deleted = await form("/oauth/v2/introspect", token.client, { token: token.access_token });
    assert(
      deleted.status === 200 && deleted.body.active === false,
      "Deleted identity token must be inactive",
    );
  }
  observations.push({ afterDeletion: "user API 404; both product tokens introspect inactive" });
} finally {
  const cleanup = await Promise.allSettled(
    sessions.map((sessionId) => ok("DELETE", `/v2/sessions/${sessionId}`, undefined, loginPat)),
  );
  if (orgId) await ok("DELETE", `/v2/organizations/${orgId}`);
  assert(
    cleanup.every((r) => r.status === "fulfilled"),
    `Session cleanup failed for run ${runId}`,
  );
}
console.log(
  JSON.stringify(
    {
      suite: "zitadel-human-oidc-v1",
      runId,
      observedAt: new Date().toISOString(),
      issuer,
      observations,
      wrongPKCE: "invalid_grant",
      cleanedUpOrganization: orgId,
      limitations: [
        "Login client API performs password check; browser Login UI not exercised",
        "strict upstream SAML/OIDC evidence not verified",
        "probe JWT assertions are not SDK authentication implementation",
        "deactivation samples are not a revocation latency guarantee",
      ],
    },
    null,
    2,
  ),
);

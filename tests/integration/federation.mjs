import assert from "node:assert/strict";
import { randomUUID, randomBytes, createHash, createPublicKey, verify } from "node:crypto";

const protocol = process.env.HEIMDALL_PROBE_FEDERATION_PROTOCOL ?? "oidc";
assert(["oidc", "saml"].includes(protocol));
const testCrossOrgLink = process.env.HEIMDALL_PROBE_CROSS_ORG_LINK === "true";
const z = process.env.HEIMDALL_PROBE_ZITADEL_ISSUER;
const k = process.env.HEIMDALL_PROBE_IDP_URL;
const pat = process.env.HEIMDALL_PROBE_ZITADEL_PAT;
const loginPat = process.env.HEIMDALL_PROBE_ZITADEL_LOGIN_PAT;
const adminPassword = process.env.HEIMDALL_PROBE_IDP_ADMIN_PASSWORD;
assert(
  z && k && pat && loginPat && adminPassword,
  "Set the protected verification endpoints and credentials",
);
for (const origin of [z, k])
  assert(origin.startsWith("https://") && new URL(origin).origin === origin);
const sharedClient = process.env.HEIMDALL_PROBE_SHARED_CLIENT_JSON
  ? JSON.parse(process.env.HEIMDALL_PROBE_SHARED_CLIENT_JSON)
  : null;
if (sharedClient)
  assert(sharedClient.projectId && sharedClient.clientId && sharedClient.clientSecret);
const runId = randomUUID();
const syntheticEmail =
  process.env.HEIMDALL_PROBE_SHARED_EMAIL ?? `federation-${runId}@example.invalid`;
assert(syntheticEmail.endsWith("@example.invalid"));
const realm = `heimdall-${runId}`;
const password = `Aa1!${randomBytes(24).toString("hex")}`;
const clientSecret = randomBytes(32).toString("hex");
console.error(`Federation probe run: ${runId}`);
let adminToken;
let orgId;
let homeOrgId;
let realmCreated = false;
const sessions = [];
async function api(origin, method, path, body, token, extra = {}) {
  const r = await fetch(origin + path, {
    method,
    headers: { "content-type": "application/json", authorization: `Bearer ${token}`, ...extra },
    ...(body === undefined ? {} : { body: JSON.stringify(body) }),
    redirect: "error",
    signal: AbortSignal.timeout(15000),
  });
  const text = await r.text();
  const data = text ? JSON.parse(text) : null;
  assert(
    r.status >= 200 && r.status < 300,
    `${method} ${path}: HTTP ${r.status}, code ${data?.code ?? "unknown"}`,
  );
  return data;
}
const za = (method, path, body, token = pat, extra) => api(z, method, path, body, token, extra);
const ka = (method, path, body) => api(k, method, path, body, adminToken);
const observations = [];
function htmlAttribute(value) {
  return value
    .replace(/&#(x[0-9a-f]+|[0-9]+);/gi, (_, n) =>
      String.fromCodePoint(
        n[0].toLowerCase() === "x" ? Number.parseInt(n.slice(1), 16) : Number(n),
      ),
    )
    .replaceAll("&quot;", '"')
    .replaceAll("&apos;", "'")
    .replaceAll("&lt;", "<")
    .replaceAll("&gt;", ">")
    .replaceAll("&amp;", "&");
}
try {
  const login = await fetch(`${k}/realms/master/protocol/openid-connect/token`, {
    method: "POST",
    headers: { "content-type": "application/x-www-form-urlencoded" },
    body: new URLSearchParams({
      grant_type: "password",
      client_id: "admin-cli",
      username: "heimdall-fixture-admin",
      password: adminPassword,
    }),
    redirect: "error",
    signal: AbortSignal.timeout(15000),
  });
  assert(login.status === 200, `Fixture administrator login: HTTP ${login.status}`);
  adminToken = (await login.json()).access_token;
  assert(adminToken);
  await ka("POST", "/admin/realms", {
    realm,
    enabled: true,
    registrationAllowed: false,
    loginWithEmailAllowed: false,
    resetPasswordAllowed: false,
  });
  realmCreated = true;
  if (protocol === "oidc")
    await ka("POST", `/admin/realms/${realm}/clients`, {
      clientId: "heimdall-zitadel",
      enabled: true,
      protocol: "openid-connect",
      publicClient: false,
      secret: clientSecret,
      standardFlowEnabled: true,
      directAccessGrantsEnabled: false,
      redirectUris: [`${z}/idps/callback`],
    });
  await ka("POST", `/admin/realms/${realm}/users`, {
    username: "federation-user",
    firstName: "Federation",
    lastName: "Probe",
    enabled: true,
    email: syntheticEmail,
    emailVerified: true,
    credentials: [{ type: "password", value: password, temporary: false }],
  });
  const upstreamUsers = await ka(
    "GET",
    `/admin/realms/${realm}/users?username=federation-user&exact=true`,
  );
  assert(upstreamUsers.length === 1);
  const upstreamUser = upstreamUsers[0].id;
  const org = await za("POST", "/v2/organizations", { name: `heimdall-federation-${runId}` });
  orgId = org.organizationId;
  assert(orgId);
  homeOrgId = orgId;
  if (testCrossOrgLink) {
    const home = await za("POST", "/v2/organizations", { name: `heimdall-home-${runId}` });
    homeOrgId = home.organizationId;
    assert(homeOrgId && homeOrgId !== orgId);
  }

  const providerOptions = {
    isLinkingAllowed: false,
    isCreationAllowed: false,
    isAutoCreation: false,
    isAutoUpdate: false,
  };
  const provider = await za(
    "POST",
    `/management/v1/idps/${protocol === "oidc" ? "generic_oidc" : "saml"}`,
    protocol === "oidc"
      ? {
          name: `fixture-oidc-${runId}`,
          issuer: `${k}/realms/${realm}`,
          clientId: "heimdall-zitadel",
          clientSecret,
          scopes: ["openid", "profile", "email"],
          usePkce: true,
          providerOptions,
        }
      : {
          name: `fixture-saml-${runId}`,
          metadataUrl: `${k}/realms/${realm}/protocol/saml/descriptor`,
          binding: "SAML_BINDING_REDIRECT",
          withSignedRequest: true,
          signatureAlgorithm: "SAML_SIGNATURE_RSA_SHA256",
          nameIdFormat: "SAML_NAME_ID_FORMAT_PERSISTENT",
          providerOptions,
        },
    pat,
    { "x-zitadel-orgid": orgId },
  );
  assert(provider.id);
  const providerRead = await za("GET", `/v2/idps/${provider.id}`);
  assert.equal(providerRead.idp.id, provider.id);
  assert.equal(providerRead.idp.details.resourceOwner, orgId, "Provider organization mismatch");
  assert.equal(providerRead.idp.state, "IDP_STATE_ACTIVE");
  assert.equal(providerRead.idp.type, protocol === "oidc" ? "IDP_TYPE_OIDC" : "IDP_TYPE_SAML");
  observations.push({
    providerConfiguration: {
      ownerMatchesTargetOrganization: true,
      active: true,
      protocolMatches: true,
    },
  });
  let samlAcs;
  if (protocol === "saml") {
    const metadataResponse = await fetch(`${z}/idps/${provider.id}/saml/metadata`, {
      redirect: "error",
      signal: AbortSignal.timeout(15000),
    });
    assert(metadataResponse.status === 200);
    const metadata = await metadataResponse.text();
    const entityId = metadata.match(/entityID="([^"]+)"/)?.[1];
    const acsTag = metadata.match(/<(?:\w+:)?AssertionConsumerService\b[^>]+>/)?.[0];
    samlAcs = acsTag?.match(/Location="([^"]+)"/)?.[1];
    const cert = metadata
      .match(/<(?:\w+:)?X509Certificate\b[^>]*>([^<]+)</)?.[1]
      .replace(/\s/g, "");
    assert(entityId && samlAcs && cert && new URL(samlAcs).origin === z, "SP metadata incomplete");
    await ka("POST", `/admin/realms/${realm}/clients`, {
      clientId: entityId,
      enabled: true,
      protocol: "saml",
      redirectUris: [samlAcs],
      attributes: {
        "saml.assertion.consumer.url.post": samlAcs,
        "saml.force.post.binding": "true",
        "saml.server.signature": "true",
        "saml.assertion.signature": "true",
        "saml.client.signature": "true",
        "saml.signing.certificate": cert,
        "saml.signature.algorithm": "RSA_SHA256",
        saml_name_id_format: "persistent",
        "saml.authnstatement": "true",
      },
    });
    const profile = await ka("GET", `/admin/realms/${realm}/users/profile`);
    await ka("PUT", `/admin/realms/${realm}/users/profile`, {
      ...profile,
      unmanagedAttributePolicy: "ADMIN_EDIT",
    });
    await ka("PUT", `/admin/realms/${realm}/users/${upstreamUser}`, {
      ...upstreamUsers[0],
      attributes: { [`saml.persistent.name.id.for.${entityId}`]: [upstreamUser] },
    });
    const configuredUser = await ka("GET", `/admin/realms/${realm}/users/${upstreamUser}`);
    assert(
      configuredUser.attributes?.[`saml.persistent.name.id.for.${entityId}`]?.[0] === upstreamUser,
      "Persistent NameID fixture mapping not stored",
    );
  }
  if (testCrossOrgLink) {
    const crossLink = { idpId: provider.id, userId: upstreamUser, userName: "federation-user" };
    const crossUserInput = {
      organizationId: homeOrgId,
      username: `cross-home-${runId}`,
      human: {
        profile: { givenName: "Cross", familyName: "Home" },
        email: { email: `cross-home-${runId}@example.invalid`, isVerified: true },
        idpLinks: [crossLink],
      },
    };
    async function rejectCrossLink(path, body) {
      const response = await fetch(z + path, {
        method: "POST",
        headers: { "content-type": "application/json", authorization: `Bearer ${pat}` },
        body: JSON.stringify(body),
        redirect: "error",
        signal: AbortSignal.timeout(15000),
      });
      const error = await response.json();
      assert.equal(response.status, 400, "Cross-organization provider link behavior changed");
      assert.equal(error.code, 9, "Expected provider precondition rejection");
      return { status: response.status, code: error.code };
    }
    const createWithLink = await rejectCrossLink("/v2/users/new", crossUserInput);
    delete crossUserInput.human.idpLinks;
    const crossUser = await za("POST", "/v2/users/new", crossUserInput);
    const addLink = await rejectCrossLink(`/v2/users/${crossUser.id}/links`, {
      idpLink: crossLink,
    });
    observations.push({
      crossOrganizationProviderLink: {
        createWithLink,
        addLink,
        credential: "bootstrap administrator; not a narrow-role permission test",
      },
    });
  }
  const user = await za("POST", "/v2/users/new", {
    organizationId: orgId,
    username: `federated-${runId}`,
    human: {
      profile: { givenName: "Federation", familyName: "Probe" },
      email: { email: syntheticEmail, isVerified: true },
      idpLinks: [{ idpId: provider.id, userId: upstreamUser, userName: "federation-user" }],
    },
  });
  assert(user.id);
  const complete = "https://heimdall-probe.example.invalid/complete";
  async function authenticate(tamper = false) {
    let capturedSamlFields;
    const intent = await za(
      "POST",
      "/v2/idp_intents",
      {
        idpId: provider.id,
        urls: {
          successUrl: complete,
          failureUrl: "https://heimdall-probe.example.invalid/failure",
        },
      },
      loginPat,
    );
    const upstreamAuthorization = new URL(intent.authUrl);
    assert(upstreamAuthorization.origin === k);
    if (protocol === "oidc") {
      assert(upstreamAuthorization.searchParams.get("code_challenge_method") === "S256");
      assert(upstreamAuthorization.searchParams.get("code_challenge"));
    } else {
      assert(upstreamAuthorization.searchParams.get("SAMLRequest"));
      assert(upstreamAuthorization.searchParams.get("Signature"), "SAML request must be signed");
    }
    const cookies = new Map();
    async function page(url, body) {
      const origin = new URL(url).origin;
      assert([z, k].includes(origin), "Unexpected federation network destination");
      const jar = cookies.get(origin) ?? new Map();
      const r = await fetch(url, {
        method: body ? "POST" : "GET",
        headers: {
          cookie: [...jar].map(([name, value]) => `${name}=${value}`).join("; "),
          ...(body ? { "content-type": "application/x-www-form-urlencoded" } : {}),
        },
        ...(body ? { body: new URLSearchParams(body) } : {}),
        redirect: "manual",
        signal: AbortSignal.timeout(15000),
      });
      for (const cookie of r.headers.getSetCookie()) {
        const pair = cookie.split(";", 1)[0];
        const split = pair.indexOf("=");
        jar.set(pair.slice(0, split), pair.slice(split + 1));
      }
      cookies.set(origin, jar);
      return r;
    }
    let target = new URL(intent.authUrl);
    let response = await page(target.href);
    let passwordSubmitted = false;
    for (let i = 0; i < 12; i++) {
      if ([302, 303, 307].includes(response.status)) {
        target = new URL(response.headers.get("location"), target);
        if (target.origin === new URL(complete).origin) break;
        response = await page(target.href);
        continue;
      }
      assert(
        response.status === 200 && target.origin === k,
        `Unexpected fixture login response: ${response.status}`,
      );
      const html = await response.text();
      if (protocol === "saml" && passwordSubmitted) {
        const action = html.match(/<form\b[^>]*\baction="([^"]+)"/)?.[1];
        assert(action && htmlAttribute(action) === samlAcs, "Unexpected SAML POST destination");
        const fields = {};
        for (const input of html.matchAll(/<input\b[^>]+>/g)) {
          const name = input[0].match(/\bname="([^"]+)"/)?.[1];
          const value = input[0].match(/\bvalue="([^"]*)"/)?.[1];
          if (name && value !== undefined) fields[name] = htmlAttribute(value);
        }
        assert(fields.SAMLResponse && fields.RelayState, "SAML response fields missing");
        const samlXml = Buffer.from(fields.SAMLResponse, "base64").toString("utf8");
        assert(
          [...samlXml.matchAll(/<(?:\w+:)?Signature\b/g)].length === 2,
          "Fixture must sign both SAML response and assertion",
        );
        capturedSamlFields = { ...fields };
        if (tamper) {
          const changed = samlXml.replace(
            new RegExp(`(<(?:[A-Za-z0-9_]+:)?NameID\\b[^>]*>)${upstreamUser}(<)`),
            `$1${randomUUID()}$2`,
          );
          assert(changed !== samlXml, "Tamper test must change the signed NameID");
          fields.SAMLResponse = Buffer.from(changed).toString("base64");
        }
        target = new URL(samlAcs);
        response = await page(target.href, fields);
        continue;
      }
      assert(!passwordSubmitted, "Fixture password login did not complete");
      // This parser handles only the pinned fixture's login form, never product input.
      const action = html.match(/<form\b[^>]*\bid="kc-form-login"[^>]*\baction="([^"]+)"/);
      assert(action, "Pinned Keycloak login form missing");
      target = new URL(htmlAttribute(action[1]), target);
      assert(target.origin === k, "Password destination must be the fixture IdP");
      response = await page(target.href, {
        username: "federation-user",
        password,
        credentialId: "",
      });
      passwordSubmitted = true;
    }
    return { target, page, capturedSamlFields };
  }
  if (protocol === "saml") {
    const altered = await authenticate(true);
    assert(
      altered.target.origin + altered.target.pathname ===
        "https://heimdall-probe.example.invalid/failure",
      "Tampered signed assertion must fail",
    );
    assert(
      !altered.target.searchParams.get("token"),
      "Tampered assertion must not return intent credentials",
    );
    observations.push({ tamperedNameId: "rejected at failure callback without intent token" });
  }
  const { target, page, capturedSamlFields } = await authenticate();
  assert(target.origin + target.pathname === complete, "Federation did not reach success callback");
  const intentId = target.searchParams.get("id");
  const intentToken = target.searchParams.get("token");
  assert(intentId && intentToken, "Federation callback missing intent credentials");
  const verified = await za(
    "POST",
    `/v2/idp_intents/${intentId}`,
    { idpIntentToken: intentToken },
    loginPat,
  );
  assert(verified.idpInformation.idpId === provider.id, "Provider mismatch");
  assert(verified.idpInformation.userId === upstreamUser, "Upstream subject mismatch");
  assert(verified.userId === user.id, "Linked ZITADEL subject mismatch");
  async function denied(path, body) {
    const response = await fetch(z + path, {
      method: "POST",
      headers: { "content-type": "application/json", authorization: `Bearer ${loginPat}` },
      body: JSON.stringify(body),
      redirect: "error",
      signal: AbortSignal.timeout(15000),
    });
    assert(
      [400, 401, 403, 404].includes(response.status),
      `Invalid federation evidence was not rejected: ${response.status}`,
    );
    return response.status;
  }
  const invalidIntentTokenStatus = await denied(`/v2/idp_intents/${intentId}`, {
    idpIntentToken: "invalid-probe-token",
  });
  const wrongUser = await za("POST", "/v2/users/new", {
    organizationId: orgId,
    username: `wrong-federated-${runId}`,
    human: {
      profile: { givenName: "Wrong", familyName: "Probe" },
      email: { email: `wrong-${runId}@example.invalid`, isVerified: true },
      idpLinks: [{ idpId: provider.id, userId: randomUUID(), userName: "different-user" }],
    },
  });
  const mismatchedUserStatus = await denied("/v2/sessions", {
    checks: {
      user: { userId: wrongUser.id },
      idpIntent: { idpIntentId: intentId, idpIntentToken: intentToken },
    },
    lifetime: "3600s",
  });
  const session = await za(
    "POST",
    "/v2/sessions",
    {
      checks: {
        user: { userId: user.id },
        idpIntent: { idpIntentId: intentId, idpIntentToken: intentToken },
      },
      lifetime: "3600s",
    },
    loginPat,
  );
  sessions.push(session.sessionId);
  const read = await za("GET", `/v2/sessions/${session.sessionId}`, undefined, loginPat);
  const reusedIntent = await fetch(`${z}/v2/sessions`, {
    method: "POST",
    headers: { "content-type": "application/json", authorization: `Bearer ${loginPat}` },
    body: JSON.stringify({
      checks: {
        user: { userId: user.id },
        idpIntent: { idpIntentId: intentId, idpIntentToken: intentToken },
      },
      lifetime: "3600s",
    }),
    redirect: "error",
    signal: AbortSignal.timeout(15000),
  });
  const reuseBody = await reusedIntent.json();
  if (reusedIntent.status < 300 && reuseBody.sessionId) sessions.push(reuseBody.sessionId);
  assert.equal(
    reusedIntent.status,
    400,
    "Consumed provider intent must not create another session",
  );
  observations.push({
    verifiedIntentReuse: { status: reusedIntent.status, createdSecondVerifiedSession: false },
  });

  assert(read.session.factors.intent.verifiedAt, "Session intent verification missing");
  assert(
    read.session.factors.user.id === user.id && read.session.factors.user.organizationId === orgId,
    "Session identity binding mismatch",
  );
  assert(!read.session.factors.password, "Federated session must not have local password evidence");
  const project = sharedClient
    ? { projectId: sharedClient.projectId }
    : await za("POST", "/zitadel.project.v2.ProjectService/CreateProject", {
        organizationId: orgId,
        name: `federation-product-${runId}`,
      });
  const redirectUri = "https://heimdall-product.example.invalid/callback";
  const app = sharedClient
    ? { oidcConfiguration: sharedClient }
    : await za("POST", "/zitadel.application.v2.ApplicationService/CreateApplication", {
        projectId: project.projectId,
        name: "federated-product",
        oidcConfiguration: {
          redirectUris: [redirectUri],
          responseTypes: ["OIDC_RESPONSE_TYPE_CODE"],
          grantTypes: ["OIDC_GRANT_TYPE_AUTHORIZATION_CODE"],
          applicationType: "OIDC_APP_TYPE_WEB",
          authMethodType: "OIDC_AUTH_METHOD_TYPE_BASIC",
          accessTokenType: "OIDC_TOKEN_TYPE_JWT",
        },
      });
  const client = app.oidcConfiguration;
  assert(client && !client.nonCompliant);
  const nonce = randomUUID();
  const state = randomUUID();
  const verifier = randomBytes(32).toString("base64url");
  const query = new URLSearchParams({
    client_id: client.clientId,
    redirect_uri: redirectUri,
    response_type: "code",
    scope: `openid urn:zitadel:iam:org:project:id:${project.projectId}:aud urn:zitadel:iam:org:id:${orgId}`,
    state,
    nonce,
    code_challenge_method: "S256",
    code_challenge: createHash("sha256").update(verifier).digest("base64url"),
  });
  const authorization = await page(`${z}/oauth/v2/authorize?${query}`);
  assert(authorization.status === 302);
  const authRequestId = new URL(authorization.headers.get("location"), z).searchParams.get(
    "authRequest",
  );
  assert(authRequestId);
  const callback = await za(
    "POST",
    `/v2/oidc/auth_requests/${authRequestId}`,
    { session: { sessionId: session.sessionId, sessionToken: session.sessionToken } },
    loginPat,
  );
  let callbackUrl = new URL(callback.callbackUrl);
  for (let i = 0; i < 4 && callbackUrl.origin === z; i++) {
    const redirect = await page(callbackUrl.href);
    assert([302, 303, 307].includes(redirect.status));
    callbackUrl = new URL(redirect.headers.get("location"), callbackUrl);
  }
  assert(
    callbackUrl.origin + callbackUrl.pathname === redirectUri &&
      callbackUrl.searchParams.get("state") === state,
    "Downstream callback binding mismatch",
  );
  const code = callbackUrl.searchParams.get("code");
  assert(code, "Downstream authorization code missing");
  const exchange = await fetch(`${z}/oauth/v2/token`, {
    method: "POST",
    headers: {
      "content-type": "application/x-www-form-urlencoded",
      authorization: `Basic ${Buffer.from(`${client.clientId}:${client.clientSecret}`).toString("base64")}`,
    },
    body: new URLSearchParams({
      grant_type: "authorization_code",
      code,
      redirect_uri: redirectUri,
      code_verifier: verifier,
    }),
    redirect: "error",
    signal: AbortSignal.timeout(15000),
  });
  assert(exchange.status === 200, `Federated token exchange HTTP ${exchange.status}`);
  const tokens = await exchange.json();
  const discovery = await fetch(`${z}/.well-known/openid-configuration`, {
    redirect: "error",
    signal: AbortSignal.timeout(15000),
  }).then((r) => r.json());
  assert(new URL(discovery.jwks_uri).origin === z);
  const { keys } = await fetch(discovery.jwks_uri, {
    redirect: "error",
    signal: AbortSignal.timeout(15000),
  }).then((r) => r.json());
  function verifiedClaims(token, audience) {
    const [header64, body64, signature64] = token.split(".");
    const header = JSON.parse(Buffer.from(header64, "base64url"));
    const claims = JSON.parse(Buffer.from(body64, "base64url"));
    assert(header.alg === "RS256");
    const key = keys.find((k) => k.kid === header.kid && k.kty === "RSA");
    assert(
      key &&
        verify(
          "RSA-SHA256",
          Buffer.from(`${header64}.${body64}`),
          createPublicKey({ key, format: "jwk" }),
          Buffer.from(signature64, "base64url"),
        ),
      "Invalid downstream signature",
    );
    assert(
      claims.iss === z && claims.sub === user.id && [claims.aud].flat().includes(audience),
      "Downstream principal/audience mismatch",
    );
    assert(claims.exp > Date.now() / 1000 && claims.iat <= Date.now() / 1000 + 5);
    return claims;
  }
  const idClaims = verifiedClaims(tokens.id_token, client.clientId);
  verifiedClaims(tokens.access_token, project.projectId);
  assert(idClaims.nonce === nonce, "Downstream nonce mismatch");
  const codeReplay = await fetch(`${z}/oauth/v2/token`, {
    method: "POST",
    headers: {
      "content-type": "application/x-www-form-urlencoded",
      authorization: `Basic ${Buffer.from(`${client.clientId}:${client.clientSecret}`).toString("base64")}`,
    },
    body: new URLSearchParams({
      grant_type: "authorization_code",
      code,
      redirect_uri: redirectUri,
      code_verifier: verifier,
    }),
    redirect: "error",
    signal: AbortSignal.timeout(15000),
  });
  const replayError = await codeReplay.json();
  assert(
    codeReplay.status === 400 && ["invalid_grant", "invalid_request"].includes(replayError.error),
    `Consumed downstream code must not be redeemable again: HTTP ${codeReplay.status}, ${replayError.error ?? "no OAuth error"}`,
  );
  observations.push({
    downstreamCodeReplay: { status: codeReplay.status, error: replayError.error },
  });

  assert(
    idClaims.sid === session.sessionId,
    "Signed token session must match verified provider session",
  );
  assert(
    idClaims.auth_time === Math.floor(Date.parse(read.session.factors.intent.verifiedAt) / 1000),
    "Signed authentication time must match verified intent",
  );
  if (sharedClient)
    observations.push({
      verifiedEnterpriseEvidence: {
        schemaVersion: 1,
        productId: "federation-product",
        environment: "verification",
        issuer: z,
        subject: idClaims.sub,
        clientId: client.clientId,
        tenantId: orgId,
        organizationId: providerRead.idp.details.resourceOwner,
        policyVersion: "probe-policy-1",
        providerId: provider.id,
        protocol,
        upstreamSubject: upstreamUser,
        providerSessionId: idClaims.sid,
        authTime: idClaims.auth_time * 1000,
        verifiedAt: Date.parse(read.session.factors.intent.verifiedAt),
      },
    });

  observations.push({
    downstreamOidc: {
      signatureIssuerSubjectAudienceNonce: "verified",
      organizationScopedRequest: true,
      amr: idClaims.amr ?? null,
      tokenSessionMatchesProviderSession: idClaims.sid === session.sessionId,
      authTimeMatchesIntent:
        idClaims.auth_time ===
        Math.floor(Date.parse(read.session.factors.intent.verifiedAt) / 1000),
    },
  });

  if (protocol === "saml") {
    const replay = await page(samlAcs, capturedSamlFields);
    const location = replay.headers.get("location");
    const replayTarget = location ? new URL(location, samlAcs) : null;
    const reachesSuccess = replayTarget && replayTarget.origin + replayTarget.pathname === complete;
    assert(!reachesSuccess, "Replayed SAML response reached success callback");
    assert([302, 303, 400, 403, 404].includes(replay.status), "Unexpected replay response");
    observations.push({
      replayedSamlResponse: {
        status: replay.status,
        callbackPath: replayTarget?.pathname ?? null,
        returnedIntentToken: Boolean(replayTarget?.searchParams.get("token")),
      },
    });
  }

  observations.push({
    protocol: `upstream ${protocol.toUpperCase()}`,
    providerIdVerified: true,
    upstreamSubjectVerified: true,
    linkedZitadelSubjectVerified: true,
    organizationVerified: true,
    crossOrganizationLinkRejectionTested: testCrossOrgLink,
    successfulUserHomeMatchesProviderOrganization: true,
    sessionOrganizationMatchesProviderOrganization:
      read.session.factors.user.organizationId === orgId,
    sessionIntentVerified: true,
    sessionIntentFields: Object.keys(read.session.factors.intent),
    passwordFallbackUsed: false,
    invalidIntentTokenStatus,
    mismatchedUserStatus,
  });
} finally {
  const results = await Promise.allSettled([
    ...sessions.map((id) => za("DELETE", `/v2/sessions/${id}`, undefined, loginPat)),
    ...(orgId ? [za("DELETE", `/v2/organizations/${orgId}`)] : []),
    ...(homeOrgId && homeOrgId !== orgId ? [za("DELETE", `/v2/organizations/${homeOrgId}`)] : []),
    ...(realmCreated ? [ka("DELETE", `/admin/realms/${realm}`)] : []),
  ]);
  assert(
    results.every((r) => r.status === "fulfilled"),
    `Federation cleanup incomplete for run ${runId}`,
  );
}
console.log(
  JSON.stringify(
    {
      suite: `upstream-${protocol}-intent-v1`,
      runId,
      observedAt: new Date().toISOString(),
      issuer: z,
      fixture: k,
      observations,
      cleanup: "owned organizations, realm and sessions deleted",
      limitations: [
        "protocol fixture form automation; not browser UI validation",
        "strict tenant switching, policy-version invalidation and browser binding still unverified",
        "SDK strict policy lifecycle not implemented",
        "intent evidence requires Login client API access; integration scope still under verification",
      ],
    },
    null,
    2,
  ),
);

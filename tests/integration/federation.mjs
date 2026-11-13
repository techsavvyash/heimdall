import assert from "node:assert/strict";
import { spawn, spawnSync } from "node:child_process";
import { randomUUID, randomBytes, createHash, createPublicKey, verify } from "node:crypto";
import { once } from "node:events";
import { createServer } from "node:http";
import { createBrowserBridge } from "./browser-bridge.mjs";

const protocol = process.env.HEIMDALL_PROBE_FEDERATION_PROTOCOL ?? "oidc";
assert(["oidc", "saml"].includes(protocol));
const testCrossOrgLink = process.env.HEIMDALL_PROBE_CROSS_ORG_LINK === "true";
const z = process.env.HEIMDALL_PROBE_ZITADEL_ISSUER;
const k = process.env.HEIMDALL_PROBE_IDP_URL;
const pat = process.env.HEIMDALL_PROBE_ZITADEL_PAT;
const loginPat = process.env.HEIMDALL_PROBE_ZITADEL_LOGIN_PAT;
const narrowLogin = process.env.HEIMDALL_PROBE_NARROW_LOGIN === "true";
const browserMode = process.env.HEIMDALL_PROBE_BROWSER === "true";
const expirySeconds = Number(process.env.HEIMDALL_PROBE_INTENT_EXPIRY_SECONDS ?? 0);
assert(Number.isInteger(expirySeconds) && expirySeconds >= 0 && expirySeconds <= 45);
assert(
  !browserMode || (narrowLogin && expirySeconds === 0),
  "Browser mode requires narrow login and a normal intent lifetime",
);
let flowPat = loginPat;
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
let narrowMachineId;
let browserBridge;
let probePassed = false;
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
async function authenticateFixtureAdmin() {
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
}
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
  await authenticateFixtureAdmin();
  await ka("POST", "/admin/realms", {
    realm,
    enabled: true,
    registrationAllowed: false,
    loginWithEmailAllowed: false,
    resetPasswordAllowed: false,
    ...(expirySeconds
      ? { accessTokenLifespan: expirySeconds, accessCodeLifespan: expirySeconds }
      : {}),
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
  if (narrowLogin) {
    const machine = await za("POST", "/v2/users/new", {
      organizationId: orgId,
      username: `federation-agent-${runId}`,
      machine: { name: "Narrow federation probe" },
    });
    narrowMachineId = machine.id;
    flowPat = (
      await za("POST", `/v2/users/${machine.id}/pats`, {
        expirationDate: new Date(Date.now() + 3600000).toISOString(),
      })
    ).token;
    assert(flowPat);
    async function forbidden(method, path, body) {
      const response = await fetch(z + path, {
        method,
        headers: { "content-type": "application/json", authorization: `Bearer ${flowPat}` },
        ...(body === undefined ? {} : { body: JSON.stringify(body) }),
        redirect: "error",
        signal: AbortSignal.timeout(15000),
      });
      const result = await response.json();
      // A roleless token authenticates but may not create sessions. User reads hide unauthorized resources.
      assert(
        [403, 404].includes(response.status),
        `${path}: expected denial, HTTP ${response.status}, code ${result.code}`,
      );
      return response.status;
    }
    const rolelessCreateSessionStatus = await forbidden("POST", "/v2/sessions", {});
    await za(
      "POST",
      "/zitadel.internal_permission.v2.InternalPermissionService/CreateAdministrator",
      {
        userId: machine.id,
        resource: { instance: true },
        roles: ["IAM_HEIMDALL_FEDERATION"],
      },
    );
    const unrelatedUserReadStatus = await forbidden("GET", `/v2/users/${user.id}`);
    const administratorGrantStatus = await forbidden(
      "POST",
      "/zitadel.internal_permission.v2.InternalPermissionService/UpdateAdministrator",
      {
        userId: machine.id,
        resource: { instance: true },
        roles: ["IAM_OWNER"],
      },
    );
    observations.push({
      narrowLoginCredential: {
        role: "IAM_HEIMDALL_FEDERATION",
        permissions: ["session.read", "session.write", "session.link"],
        scope: "instance",
        rolelessCreateSessionStatus,
        unrelatedUserReadStatus,
        administratorGrantStatus,
      },
    });
  }
  if (browserMode) browserBridge = await createBrowserBridge();
  const complete = browserMode
    ? `${browserBridge.origin}/complete`
    : "https://heimdall-probe.example.invalid/complete";
  async function authenticate(tamper = false, authorizationUrl, destination = complete) {
    let capturedSamlFields;
    const intent = authorizationUrl
      ? { authUrl: authorizationUrl }
      : await za(
          "POST",
          "/v2/idp_intents",
          {
            idpId: provider.id,
            urls: {
              successUrl: browserMode ? browserBridge.callbackUrl : complete,
              failureUrl: "https://heimdall-probe.example.invalid/failure",
            },
          },
          flowPat,
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
    if (browserMode) {
      const expectedIntent = upstreamAuthorization.searchParams.get(
        protocol === "oidc" ? "state" : "RelayState",
      );
      assert(expectedIntent);
      console.error(`Disposable fixture username: federation-user; password: ${password}`);
      const target = await browserBridge.authenticate(intent.authUrl, expectedIntent);
      observations.push({
        browserCallback: {
          missingCookie: 401,
          wrongCookie: 401,
          wrongState: 400,
          wrongIntent: 400,
          replay: 409,
          matchedBrowserAndIntent: true,
        },
      });
      return {
        target,
        page: (url) => {
          assert.equal(new URL(url).origin, z, "Only issuer redirects may be followed");
          return fetch(url, { redirect: "manual", signal: AbortSignal.timeout(15000) });
        },
      };
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
        if (target.origin === new URL(destination).origin) break;
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
  if (protocol === "saml" && !browserMode) {
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
  if (expirySeconds) {
    const expired = (await authenticate()).target;
    assert.equal(expired.origin + expired.pathname, complete);
    const expiredId = expired.searchParams.get("id");
    const expiredToken = expired.searchParams.get("token");
    assert(expiredId && expiredToken);
    const before = await za(
      "POST",
      `/v2/idp_intents/${expiredId}`,
      { idpIntentToken: expiredToken },
      flowPat,
    );
    assert.equal(before.userId, user.id);
    assert.equal(before.idpInformation.idpId, provider.id);
    const waitMs = (expirySeconds + 2) * 1000;
    console.error(`Waiting ${waitMs}ms for the disposable upstream credential lifetime`);
    await new Promise((resolve) => setTimeout(resolve, waitMs));
    const rejections = [];
    for (const [operation, path, body] of [
      ["retrieve", `/v2/idp_intents/${expiredId}`, { idpIntentToken: expiredToken }],
      [
        "create session",
        "/v2/sessions",
        {
          checks: {
            user: { userId: user.id },
            idpIntent: { idpIntentId: expiredId, idpIntentToken: expiredToken },
          },
        },
      ],
    ]) {
      const response = await fetch(z + path, {
        method: "POST",
        headers: { "content-type": "application/json", authorization: `Bearer ${flowPat}` },
        body: JSON.stringify(body),
        redirect: "error",
        signal: AbortSignal.timeout(15000),
      });
      const result = await response.json();
      if (response.status < 300 && result.sessionId) sessions.push(result.sessionId);
      assert(
        response.status >= 400 && response.status < 500,
        `Expired intent ${operation} must fail: HTTP ${response.status}`,
      );
      assert(!result.sessionId && !result.idpInformation);
      rejections.push({ operation, status: response.status, code: result.code });
    }
    observations.push({
      expiredIntent: {
        upstreamLifetimeSeconds: expirySeconds,
        waitedMs: waitMs,
        validBeforeWait: true,
        rejections,
      },
    });
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
    flowPat,
  );
  assert(verified.idpInformation.idpId === provider.id, "Provider mismatch");
  assert(verified.idpInformation.userId === upstreamUser, "Upstream subject mismatch");
  assert(verified.userId === user.id, "Linked ZITADEL subject mismatch");
  async function denied(path, body) {
    const response = await fetch(z + path, {
      method: "POST",
      headers: { "content-type": "application/json", authorization: `Bearer ${flowPat}` },
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
    flowPat,
  );
  sessions.push(session.sessionId);
  const read = await za("GET", `/v2/sessions/${session.sessionId}`, undefined, flowPat);
  const reusedIntent = await fetch(`${z}/v2/sessions`, {
    method: "POST",
    headers: { "content-type": "application/json", authorization: `Bearer ${flowPat}` },
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
  const redirectUri =
    sharedClient?.redirectUri ?? "https://heimdall-product.example.invalid/callback";
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
  async function productCode() {
    const nonce = randomUUID();
    const state = randomUUID();
    const verifier = randomBytes(32).toString("base64url");
    const query = new URLSearchParams({
      client_id: client.clientId,
      redirect_uri: redirectUri,
      response_type: "code",
      scope: `openid urn:zitadel:iam:org:project:id:${project.projectId}:aud urn:zitadel:iam:org:id:${orgId} urn:zitadel:iam:org:project:id:zitadel:aud`,
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
      flowPat,
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
    return { code, nonce, verifier };
  }
  const { code, nonce, verifier } = await productCode();
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
  const selfSession = await za(
    "GET",
    `/v2/sessions/${idClaims.sid}`,
    undefined,
    tokens.access_token,
  );
  assert.deepEqual(
    selfSession.session.factors,
    read.session.factors,
    "User token must return the same verified federation factors as the Login client",
  );
  observations.push({
    selfServiceEvidence: {
      credential: "user access token with ZITADEL API audience; no PAT or session token",
      sessionFactorsMatchFlowCredential: true,
      intentFields: Object.keys(selfSession.session.factors.intent),
    },
  });
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
  if (process.env.HEIMDALL_PROBE_SDK_FEDERATION === "1") {
    const apiAudience = [idClaims.aud]
      .flat()
      .find((value) => value !== client.clientId && value !== project.projectId);
    assert(apiAudience);
    const checks = [];
    for (const language of ["typescript", "go"]) {
      for (const mismatch of [false, true]) {
        const login = await productCode();
        const config = {
          issuer: z,
          profile: "production",
          clientId: client.clientId,
          clientSecret: client.clientSecret,
          audience: project.projectId,
          zitadelAPIAudience: apiAudience,
          redirectUri,
        };
        const input = {
          config,
          subject: user.id,
          code: login.code,
          nonce: login.nonce,
          pkceVerifier: login.verifier,
          federation: {
            subject: user.id,
            sessionId: mismatch ? "other-session" : session.sessionId,
            verifiedAt: Date.parse(read.session.factors.intent.verifiedAt),
          },
          expectFederationDenied: mismatch,
        };
        const command =
          language === "typescript"
            ? [
                "pnpm",
                "--filter",
                "@heimdall/sdk",
                "exec",
                "vitest",
                "run",
                "test/browser-oidc.live.test.ts",
              ]
            : [
                "go",
                "test",
                "-race",
                "-count=1",
                "./internal/identity",
                "-run",
                "^TestLiveBrowserExchange$",
              ];
        const result = spawnSync(command[0], command.slice(1), {
          cwd: new URL(language === "typescript" ? "../../" : "../../sdk/go/", import.meta.url),
          env: { ...process.env, HEIMDALL_TEST_BROWSER_EXCHANGE: JSON.stringify(input) },
          encoding: "utf8",
          timeout: 60000,
        });
        assert.equal(
          result.status,
          0,
          `${language} federation exchange failed (wrong session: ${mismatch})`,
        );
      }
      checks.push({ language, matchedSignedBinding: "accepted", wrongSignedSession: "denied" });
    }
    observations.push({ sdkFederatedExchange: checks });
  }
  if (process.env.HEIMDALL_PROBE_SDK_FEDERATION_HTTP === "1") {
    const bridgeKey = randomBytes(32).toString("hex");
    const bridge = createServer(async (request, response) => {
      try {
        assert(
          request.method === "POST" && request.headers.authorization === "Bearer " + bridgeKey,
        );
        let raw = "";
        for await (const chunk of request) {
          raw += chunk;
          assert(raw.length <= 16384);
        }
        const payload = JSON.parse(raw);
        if (request.url === "/session") {
          assert(/^[A-Za-z0-9_-]{1,128}$/.test(payload.sessionId));
          sessions.push(payload.sessionId);
          response.writeHead(204);
          response.end();
          return;
        }
        assert.equal(request.url, "/upstream");
        const { authorizationUrl } = payload;
        assert.equal(new URL(authorizationUrl).origin, k);
        const { target } = await authenticate(false, authorizationUrl, redirectUri);
        assert.equal(target.origin + target.pathname, redirectUri);
        response.writeHead(200, {
          "content-type": "application/json",
          "cache-control": "no-store",
        });
        response.end(JSON.stringify({ callback: target.href }));
      } catch {
        response.writeHead(400);
        response.end();
      }
    });
    bridge.listen(0, "127.0.0.1");
    await once(bridge, "listening");
    const commands = [
      [
        "typescript",
        [
          "pnpm",
          "--filter",
          "@heimdall/sdk",
          "exec",
          "vitest",
          "run",
          "test/browser-federation.live.test.ts",
        ],
      ],
      [
        "go",
        [
          "go",
          "test",
          "-race",
          "-count=1",
          "./internal/browserlogin",
          "-run",
          "^TestLiveFederationHTTP$",
        ],
      ],
    ];
    const apiAudience = [idClaims.aud]
      .flat()
      .find((value) => value !== client.clientId && value !== project.projectId);
    assert(apiAudience);
    const input = {
      config: {
        issuer: z,
        profile: "production",
        clientId: client.clientId,
        clientSecret: client.clientSecret,
        audience: project.projectId,
        zitadelAPIAudience: apiAudience,
        redirectUri,
      },
      credential: flowPat,
      fixtureOrigin: k,
      subject: user.id,
      policy: {
        providerId: provider.id,
        organizationId: orgId,
        protocol,
        policyVersion: "probe-v1",
        maxAgeMs: 300000,
      },
      bridge: `http://127.0.0.1:${bridge.address().port}`,
      bridgeKey,
    };
    try {
      const checks = [];
      for (const [language, command] of commands) {
        const child = spawn(command[0], command.slice(1), {
          cwd: new URL(language === "typescript" ? "../../" : "../../sdk/go/", import.meta.url),
          env: { ...process.env, HEIMDALL_TEST_FEDERATION_HTTP: JSON.stringify(input) },
          stdio: ["ignore", "pipe", "pipe"],
          timeout: 60000,
        });
        let diagnostic = "";
        for (const pipe of [child.stdout, child.stderr]) {
          pipe.setEncoding("utf8");
          pipe.on("data", (chunk) => {
            diagnostic = (diagnostic + chunk).slice(-12000);
          });
        }
        const [status] = await once(child, "close");
        if (status !== 0) {
          const codes = [
            ...new Set(
              diagnostic.match(
                /\b(?:unauthenticated|invalid_argument|unavailable|deadline_exceeded|bridge rejected federation)\b/g,
              ) ?? [],
            ),
          ];
          const frames =
            diagnostic.match(
              /(?:browser-federation\.live\.test\.ts|browser-login-http\.ts|zitadel-federation\.ts|federation_live_test\.go):\d+(?::\d+)?/g,
            ) ?? [];
          console.error(JSON.stringify({ language, codes, frames }));
        }
        assert.equal(status, 0, `${language} browser federation failed`);
        checks.push({
          language,
          exactIntent: "accepted",
          missingCookie: "denied",
          wrongIntent: "denied",
          changedPolicy: "denied",
          replay: "denied",
        });
      }
      observations.push({ sdkBrowserFederation: checks });
    } finally {
      bridge.closeAllConnections();
      bridge.close();
    }
  }
  if (process.send && process.env.HEIMDALL_PROBE_SWITCH_CHILD === "1") {
    const apiAudience = [idClaims.aud]
      .flat()
      .find((value) => value !== client.clientId && value !== project.projectId);
    assert(apiAudience);
    await new Promise((resolve, reject) => {
      const timer = setTimeout(() => reject(new Error("Switch probe parent timed out")), 900000);
      process.on("disconnect", () => {
        clearTimeout(timer);
        resolve();
      });
      process.on("message", async (message) => {
        if (message.type !== "authorize") return;
        try {
          const { target } = await authenticate(false, message.location, redirectUri);
          assert.equal(target.origin + target.pathname, redirectUri);
          process.send({ type: "callback", location: target.href });
        } catch {
          process.send({ type: "failure" });
        }
      });
      process.send({
        type: "ready",
        apiAudience,
        subject: user.id,
        credential: flowPat,
        policy: {
          providerId: provider.id,
          organizationId: orgId,
          protocol,
          policyVersion: "switch-v1",
          maxAgeMs: 300000,
        },
      });
    });
  }
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

  if (protocol === "saml" && !browserMode) {
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
  probePassed = true;
} finally {
  const administratorCleanup = await Promise.allSettled(
    narrowMachineId
      ? [
          za(
            "POST",
            "/zitadel.internal_permission.v2.InternalPermissionService/DeleteAdministrator",
            {
              userId: narrowMachineId,
              resource: { instance: true },
            },
          ),
        ]
      : [],
  );
  const results = await Promise.allSettled([
    ...sessions.map((id) => za("DELETE", `/v2/sessions/${id}`, undefined, loginPat)),
    ...(orgId ? [za("DELETE", `/v2/organizations/${orgId}`)] : []),
    ...(homeOrgId && homeOrgId !== orgId ? [za("DELETE", `/v2/organizations/${homeOrgId}`)] : []),
    ...(realmCreated
      ? [authenticateFixtureAdmin().then(() => ka("DELETE", `/admin/realms/${realm}`))]
      : []),
  ]);
  const cleanupPassed = [...administratorCleanup, ...results].every(
    (r) => r.status === "fulfilled",
  );
  browserBridge?.finish(probePassed && cleanupPassed);
  for (const result of [...administratorCleanup, ...results])
    if (result.status === "rejected") console.error(`Cleanup failure: ${result.reason.message}`);
  assert(cleanupPassed, `Federation cleanup incomplete for run ${runId}`);
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
        browserMode
          ? "Loopback HTTP browser callback harness; not production SDK HTTP or durable storage"
          : "protocol fixture form automation; not browser UI validation",
        "strict tenant switching and policy-version invalidation still unverified",
        browserMode
          ? "Upstream login uses browser; downstream product OIDC exchange uses backend HTTP probe"
          : "Browser callback binding unverified in this mode",
        "SDK strict policy lifecycle not implemented",
        narrowLogin
          ? "Dedicated instance session role performs fixture authentication; production durable callback claims remain unverified"
          : "Login client performs fixture authentication; downstream session evidence also verified with user token",
      ],
    },
    null,
    2,
  ),
);

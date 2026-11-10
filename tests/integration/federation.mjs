import assert from "node:assert/strict";
import { randomUUID, randomBytes } from "node:crypto";

const protocol = process.env.HEIMDALL_PROBE_FEDERATION_PROTOCOL ?? "oidc";
assert(["oidc", "saml"].includes(protocol));
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
const runId = randomUUID();
const realm = `heimdall-${runId}`;
const password = `Aa1!${randomBytes(24).toString("hex")}`;
const clientSecret = randomBytes(32).toString("hex");
console.error(`Federation probe run: ${runId}`);
let adminToken;
let orgId;
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
    email: `federation-${runId}@example.invalid`,
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
  const user = await za("POST", "/v2/users/new", {
    organizationId: orgId,
    username: `federated-${runId}`,
    human: {
      profile: { givenName: "Federation", familyName: "Probe" },
      email: { email: `federation-${runId}@example.invalid`, isVerified: true },
      idpLinks: [{ idpId: provider.id, userId: upstreamUser, userName: "federation-user" }],
    },
  });
  assert(user.id);
  const complete = "https://heimdall-probe.example.invalid/complete";
  const intent = await za(
    "POST",
    "/v2/idp_intents",
    {
      idpId: provider.id,
      urls: { successUrl: complete, failureUrl: "https://heimdall-probe.example.invalid/failure" },
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
    response = await page(target.href, { username: "federation-user", password, credentialId: "" });
    passwordSubmitted = true;
  }
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
  assert(read.session.factors.intent.verifiedAt, "Session intent verification missing");
  assert(
    read.session.factors.user.id === user.id && read.session.factors.user.organizationId === orgId,
    "Session identity binding mismatch",
  );
  assert(!read.session.factors.password, "Federated session must not have local password evidence");
  observations.push({
    protocol: `upstream ${protocol.toUpperCase()}`,
    providerIdVerified: true,
    upstreamSubjectVerified: true,
    linkedZitadelSubjectVerified: true,
    organizationVerified: true,
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
      cleanup: "owned organization, realm and sessions deleted",
      limitations: [
        "protocol fixture form automation; not browser UI validation",
        "downstream token/policy binding and strict tenant switching still unverified",
        "SDK strict policy lifecycle not implemented",
        "intent evidence requires Login client API access; integration scope still under verification",
      ],
    },
    null,
    2,
  ),
);

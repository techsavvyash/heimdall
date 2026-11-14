import assert from "node:assert/strict";
import { randomUUID } from "node:crypto";

const issuer = process.env.HEIMDALL_PROBE_ZITADEL_ISSUER;
const pat = process.env.HEIMDALL_PROBE_ZITADEL_PAT;
assert(
  issuer && pat,
  "Set HEIMDALL_PROBE_ZITADEL_ISSUER and the bootstrap-only HEIMDALL_PROBE_ZITADEL_PAT",
);
const origin = new URL(issuer);
assert.equal(origin.protocol, "https:");
assert(!origin.username && !origin.password && !origin.search && !origin.hash);
const runId = randomUUID();
console.error(`ZITADEL probe run: ${runId}`);
async function request(path, body, credential) {
  const r = await fetch(`${issuer}${path}`, {
    method: body === undefined ? "GET" : "POST",
    headers: {
      "content-type": "application/json",
      "connect-protocol-version": "1",
      ...(credential ? { authorization: `Bearer ${credential}` } : {}),
    },
    ...(body === undefined ? {} : { body: JSON.stringify(body) }),
    signal: AbortSignal.timeout(15000),
    redirect: "error",
  });
  const text = await r.text();
  return { status: r.status, body: text ? JSON.parse(text) : null };
}
async function ok(path, body, credential) {
  const r = await request(path, body, credential);
  assert(r.status >= 200 && r.status < 300, `${path}: HTTP ${r.status}`);
  return r.body;
}
const discovery = await ok("/.well-known/openid-configuration");
assert.equal(discovery.issuer, issuer);
for (const field of [
  "authorization_endpoint",
  "token_endpoint",
  "jwks_uri",
  "introspection_endpoint",
]) {
  const url = new URL(discovery[field]);
  assert.equal(url.origin, origin.origin);
  assert.equal(url.protocol, "https:");
  assert(!url.username && !url.password);
}
assert(discovery.code_challenge_methods_supported.includes("S256"));
assert(discovery.grant_types_supported.includes("authorization_code"));
assert(discovery.grant_types_supported.includes("refresh_token"));
const keys = await ok(new URL(discovery.jwks_uri).pathname);
assert(keys.keys.length > 0);
for (const key of keys.keys) {
  assert(key.kid && ["RSA", "EC"].includes(key.kty));
  for (const privateField of ["d", "p", "q", "dp", "dq", "qi", "k"])
    assert(!Object.hasOwn(key, privateField));
}
assert.equal((await request("/auth/v1/users/me")).status, 401);
assert.equal((await request("/auth/v1/users/me", undefined, "invalid-probe-token")).status, 401);
const self = await ok("/auth/v1/users/me", undefined, pat);
assert(self.user.machine, "Bootstrap probe expects a machine credential");
const organizationId = self.user.details.resourceOwner;
assert(organizationId);
const service = "/zitadel.project.v2.ProjectService/";
const created = [];
try {
  for (const product of ["ledger", "support"]) {
    const result = await ok(
      `${service}CreateProject`,
      { organizationId, name: `heimdall-probe-${product}-${runId}` },
      pat,
    );
    assert(result.projectId);
    created.push(result.projectId);
    const read = await ok(`${service}GetProject`, { projectId: result.projectId }, pat);
    assert.equal(read.project.name, `heimdall-probe-${product}-${runId}`);
  }
  assert.notEqual(created[0], created[1]);
} finally {
  const results = await Promise.allSettled(
    created.map((projectId) => ok(`${service}DeleteProject`, { projectId }, pat)),
  );
  assert.equal(
    results.filter((r) => r.status === "rejected").length,
    0,
    `Cleanup failed in run ${runId}; inspect this run's projects`,
  );
}
console.log(
  JSON.stringify(
    {
      suite: "zitadel-bootstrap-capabilities-v1",
      runId,
      observedAt: new Date().toISOString(),
      issuer,
      observations: {
        discovery: "exact HTTPS issuer and same-origin endpoints",
        pkce: "S256 advertised",
        publicSigningKeys: keys.keys.length,
        unauthenticatedAndInvalidToken: "401",
        provisioning:
          "bootstrap machine credential created, read and deleted two distinct projects using v2 Connect API",
      },
      cleanedUpProjects: created,
      notYetVerified: [
        "human token audiences and unknown-key rejection",
        "organization context",
        "strict SAML/OIDC SSO evidence",
        "SCIM Users lifecycle",
        "identity disable/delete freshness",
      ],
    },
    null,
    2,
  ),
);

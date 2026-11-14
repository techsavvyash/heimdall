import assert from "node:assert/strict";
import { randomUUID } from "node:crypto";
import { execFile } from "node:child_process";
import { promisify } from "node:util";

const z = process.env.HEIMDALL_PROBE_ZITADEL_ISSUER;
const pat = process.env.HEIMDALL_PROBE_ZITADEL_PAT;
assert(z && pat && new URL(z).origin === z && z.startsWith("https://"));
const runId = randomUUID();
const execute = promisify(execFile);
let orgId;
const results = [];
async function api(method, path, body) {
  const r = await fetch(z + path, {
    method,
    headers: { "content-type": "application/json", authorization: `Bearer ${pat}` },
    ...(body === undefined ? {} : { body: JSON.stringify(body) }),
    redirect: "error",
    signal: AbortSignal.timeout(15000),
  });
  assert(r.status >= 200 && r.status < 300, `Shared product setup: HTTP ${r.status}`);
  return r.status === 204 ? null : r.json();
}
try {
  orgId = (await api("POST", "/v2/organizations", { name: `heimdall-shared-product-${runId}` }))
    .organizationId;
  assert(orgId);
  const project = await api("POST", "/zitadel.project.v2.ProjectService/CreateProject", {
    organizationId: orgId,
    name: `shared-enterprise-product-${runId}`,
  });
  const app = await api("POST", "/zitadel.application.v2.ApplicationService/CreateApplication", {
    projectId: project.projectId,
    name: "two-tenant-product",
    oidcConfiguration: {
      redirectUris: ["https://heimdall-product.example.invalid/callback"],
      responseTypes: ["OIDC_RESPONSE_TYPE_CODE"],
      grantTypes: ["OIDC_GRANT_TYPE_AUTHORIZATION_CODE"],
      applicationType: "OIDC_APP_TYPE_WEB",
      authMethodType: "OIDC_AUTH_METHOD_TYPE_BASIC",
      accessTokenType: "OIDC_TOKEN_TYPE_JWT",
    },
  });
  const shared = { ...app.oidcConfiguration, projectId: project.projectId };
  assert(shared.clientId && shared.clientSecret && !shared.nonCompliant);
  for (const protocol of ["saml", "oidc"]) {
    console.error(`Shared product ${runId}: ${protocol} tenant login`);
    let stdout;
    try {
      ({ stdout } = await execute(process.execPath, ["tests/integration/federation.mjs"], {
        env: {
          ...process.env,
          HEIMDALL_PROBE_FEDERATION_PROTOCOL: protocol,
          HEIMDALL_PROBE_SHARED_CLIENT_JSON: JSON.stringify(shared),
          HEIMDALL_PROBE_SHARED_EMAIL: `shared-person-${runId}@example.invalid`,
          HEIMDALL_PROBE_CROSS_ORG_LINK: "true",
        },
        timeout: 180000,
        maxBuffer: 1024 * 1024,
      }));
    } catch (error) {
      // Extract only non-secret run/status markers, never the child error or environment.
      const childRun = String(error.stderr ?? "").match(/Federation probe run: ([a-f0-9-]+)/)?.[1];
      const status = String(error.stderr ?? "").match(/HTTP ([0-9]{3})/)?.[1];
      throw new Error(
        `Federation child failed for ${protocol}; run ${childRun ?? "unknown"}; HTTP ${status ?? "unknown"}`,
      );
    }
    const result = JSON.parse(stdout);
    const evidence = result.observations.find(
      (o) => o.verifiedEnterpriseEvidence,
    )?.verifiedEnterpriseEvidence;
    assert(evidence && evidence.protocol === protocol && evidence.clientId === shared.clientId);
    results.push({ runId: result.runId, evidence, cleanup: result.cleanup });
  }
  const [saml, oidc] = results.map((r) => r.evidence);
  assert.notEqual(saml.subject, oidc.subject, "Same email must not merge organization accounts");
  assert.notEqual(saml.organizationId, oidc.organizationId);
  assert.notEqual(saml.providerId, oidc.providerId);
  assert.equal(saml.clientId, oidc.clientId);
  assert.equal(saml.issuer, oidc.issuer);
} finally {
  if (orgId) await api("DELETE", `/v2/organizations/${orgId}`);
}
console.log(
  JSON.stringify(
    {
      suite: "two-tenant-shared-product-federation-v1",
      runId,
      observedAt: new Date().toISOString(),
      sameSyntheticEmail: true,
      distinctTenantSubjects: true,
      sameProductClient: true,
      results,
      cleanup: "both tenant fixtures and shared product organization deleted",
      limitations: [
        "sequential protocol logins; not browser session switching",
        "each child removes its sessions before the next child runs",
        "does not establish one stable subject across independent tenant directories",
        "SDK atomic session replacement, membership and audit integration remain unimplemented",
      ],
    },
    null,
    2,
  ),
);

import assert from "node:assert/strict";
import { auditEventDigest, createHeimdall } from "@techsavvyash/heimdall-sdk";

const c = JSON.parse(process.env.HEIMDALL_TEST_RUNTIME);
const signal = AbortSignal.timeout(30000);
const options = {
  policyVersion: "runtime-v1",
  identityFreshnessMs: 0,
  credentials: {
    clientSecret: async () => c.clientSecret,
    openfgaKey: async () => process.env.HEIMDALL_PROBE_OPENFGA_KEY,
  },
  storage: {
    productId: c.config.productId,
    environment: c.config.environment,
    ready: async () => {},
    identity: { check: async () => false, invalidate: async () => {} },
    appendAudit: async (event) => ({
      productId: event.productId,
      environment: event.environment,
      eventId: event.eventId,
      recordedAt: event.recordedAt,
      payloadDigest: auditEventDigest(event),
    }),
  },
};
const start = () =>
  createHeimdall(JSON.stringify(c.config), JSON.stringify(c.manifest), options, { signal });
if (c.rejectModel) {
  await assert.rejects(start, { code: "unavailable" });
} else {
  const runtime = await start();
  const principal = await runtime.tokens.verify(c.token, { signal });
  assert.equal(principal.subject, c.subject);
  for (const workspace of ["acme", "globex"]) {
    const tenant = runtime.tenant(workspace);
    assert.equal(
      await tenant.can(principal, c.permission, { signal }),
      workspace === "acme" && !c.denyAcme,
    );
    assert.deepEqual(
      await tenant.roles(principal, { signal }),
      workspace === "acme" ? c.expectedRoles : [],
    );
  }
}

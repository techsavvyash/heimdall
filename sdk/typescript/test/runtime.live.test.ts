import { Pool } from "pg";
import { createPostgresStorage } from "../src/postgres.js";
import { expect, test } from "vitest";
import { createHeimdall, auditEventDigest as auditDigest } from "../src/index.js";
test.skipIf(!process.env.HEIMDALL_TEST_RUNTIME || !!process.env.HEIMDALL_TEST_RUNTIME_DATABASE_URL)(
  "live public runtime with recording storage fixture",
  async () => {
    const c = JSON.parse(process.env.HEIMDALL_TEST_RUNTIME!);
    const events: Record<string, unknown>[] = [];
    const runtime = await createHeimdall(
      JSON.stringify(c.config),
      JSON.stringify(c.manifest),
      {
        policyVersion: "runtime-v1",
        identityFreshnessMs: 0,
        credentials: {
          clientSecret: async () => c.clientSecret,
          openfgaKey: async () => process.env.HEIMDALL_PROBE_OPENFGA_KEY!,
        },
        storage: {
          productId: c.config.productId,
          environment: c.config.environment,
          ready: async () => {},
          identity: { check: async () => false, invalidate: async () => {} },
          appendAudit: async (e) => {
            events.push(e);
            return {
              productId: e.productId as string,
              environment: e.environment as string,
              eventId: e.eventId as string,
              recordedAt: e.recordedAt as number,
              payloadDigest: auditDigest(e),
            };
          },
        },
      },
      { signal: new AbortController().signal },
    );
    const principal = await runtime.tokens.verify(c.token, {
      signal: new AbortController().signal,
    });
    expect(principal.subject).toBe(c.subject);
    expect(principal.productId).toBe(c.config.productId);
    await expect(
      runtime.tokens.verify("invalid", { signal: new AbortController().signal }),
    ).rejects.toThrow("unauthenticated");
    const signal = new AbortController().signal;
    const tenants = c.permission ? ["acme", "globex"] : [];
    const decisions = await Promise.all(
      tenants.map((id) => runtime.tenant(id).can(principal, c.permission, { signal })),
    );
    expect(decisions).toEqual(c.permission ? [true, false] : []);
    expect(events.map((e) => e.outcome).sort()).toEqual(
      c.permission
        ? ["allow", "deny", "deny", "requested", "requested", "succeeded"]
        : ["deny", "succeeded"],
    );
  },
  30000,
);

test.skipIf(!process.env.HEIMDALL_TEST_RUNTIME || !process.env.HEIMDALL_TEST_RUNTIME_DATABASE_URL)(
  "live public runtime with PostgreSQL storage",
  async () => {
    const c = JSON.parse(process.env.HEIMDALL_TEST_RUNTIME!);
    c.config.environment += "-ts";
    const pool = new Pool({
      connectionString: process.env.HEIMDALL_TEST_RUNTIME_DATABASE_URL,
      options: "-c role=heimdall_public_runtime",
    });
    const signal = new AbortController().signal;
    const storage = createPostgresStorage(pool, { ...c.config, auditSinks: ["siem"] });
    try {
      const runtime = await createHeimdall(
        JSON.stringify(c.config),
        JSON.stringify(c.manifest),
        {
          policyVersion: "runtime-v1",
          identityFreshnessMs: 60000,
          credentials: {
            clientSecret: async () => c.clientSecret,
            openfgaKey: async () => process.env.HEIMDALL_PROBE_OPENFGA_KEY!,
          },
          storage,
        },
        { signal },
      );
      const principal = await runtime.tokens.verify(c.token, { signal });
      expect(principal.subject).toBe(c.subject);
      expect(principal.productId).toBe(c.config.productId);
      await expect(runtime.tokens.verify("invalid", { signal })).rejects.toThrow("unauthenticated");
      await storage.identity.invalidate(
        {
          issuer: c.config.issuer,
          subject: c.subject,
          context: "bearer",
          tokenDigest: "a".repeat(64),
          reason: "identity_inactive",
        },
        signal,
      );
      await expect(runtime.tokens.verify(c.token, { signal })).rejects.toThrow("unauthenticated");
      const events = await pool.query(
        "SELECT event FROM heimdall.audit_events WHERE product_id=$1 AND environment=$2",
        [c.config.productId, c.config.environment],
      );
      const decoded = events.rows.map((r) =>
        JSON.parse(Buffer.from(r.event.payload, "base64url").toString("utf8")),
      );
      expect(decoded.map((e) => e.outcome).sort()).toEqual(["deny", "deny", "succeeded"]);
      expect(
        decoded.every(
          (e) => e.productId === c.config.productId && e.environment === c.config.environment,
        ),
      ).toBe(true);
      const deliveries = await pool.query(
        "SELECT count(*)::int AS count FROM heimdall.audit_deliveries WHERE product_id=$1 AND environment=$2 AND sink_id='siem'",
        [c.config.productId, c.config.environment],
      );
      expect(deliveries.rows[0].count).toBe(3);
    } finally {
      await pool.end();
    }
  },
  30000,
);

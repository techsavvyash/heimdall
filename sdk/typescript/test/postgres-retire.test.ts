import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { Pool } from "pg";
import { test } from "vitest";
import { validateAuditShape } from "../src/internal/audit-shape.js";
import { validateAuditEvent } from "../src/internal/audit.js";
import { PostgresAudit } from "../src/internal/postgres/audit.js";
import {
  validateAuditRetirement,
  type AuditRetirementRequest,
} from "../src/internal/audit-retire.js";

const f = JSON.parse(
  readFileSync(
    new URL("../../../contracts/fixtures/audit-retire-storage.json", import.meta.url),
    "utf8",
  ),
) as {
  snapshot: string;
  bindingMutations: {
    name: string;
    request?: Partial<AuditRetirementRequest>;
    event?: Record<string, unknown>;
  }[];
  cases: {
    name: string;
    request: AuditRetirementRequest;
    administration: Record<string, unknown>;
    expected: string;
  }[];
};
test("retirement binding refuses mismatched administration", () => {
  const c = f.cases[0]!;
  validateAuditRetirement(c.request, c.administration);
  for (const m of f.bindingMutations)
    assert.throws(
      () =>
        validateAuditRetirement(
          { ...c.request, ...m.request },
          { ...c.administration, ...m.event },
        ),
      { code: "invalid_argument" },
      m.name,
    );
});
test.skipIf(!process.env.HEIMDALL_TEST_RETIRE_URL)("audited scoped retirement", async () => {
  const c = f.cases.find((c) => c.name === process.env.HEIMDALL_TEST_RETIRE_CASE)!;
  const pool = new Pool({ connectionString: process.env.HEIMDALL_TEST_RETIRE_URL });
  const admin = new Pool({ connectionString: process.env.HEIMDALL_TEST_RETIRE_ADMIN });
  const r = c.request;
  const port = PostgresAudit.forRetainedRuntime(
    pool,
    r.productId,
    r.environment,
    ["siem"],
    (event) => {
      validateAuditShape(event);
      validateAuditEvent(event as Parameters<typeof validateAuditEvent>[0], "sdk", () => false);
    },
  );
  try {
    const before = (await admin.query(f.snapshot)).rows;
    const expectCode = process.env.HEIMDALL_TEST_RETIRE_EXPECT ?? c.expected;
    if (expectCode !== "receipt") {
      await assert.rejects(port.retire(r, c.administration), { code: expectCode });
      if (expectCode !== "outcome_unknown")
        assert.deepEqual((await admin.query(f.snapshot)).rows, before);
      return;
    }
    const original = await port.inspect(r.tenantId, r.eventId);
    const receipt = await port.retire(r, c.administration);
    assert.deepEqual(await port.resolveReceipt(c.administration), receipt);
    const retired = await port.inspect(r.tenantId, r.eventId);
    assert.equal(retired?.state, "retired");
    if (original?.event) {
      const resolved = await port.resolveReceipt(original.event);
      assert.equal(resolved?.payloadDigest, original.identity.digest);
    }
    if (process.env.HEIMDALL_TEST_RETIRE_RETRY === "1")
      assert.deepEqual((await admin.query(f.snapshot)).rows, before);
    const after = (await admin.query(f.snapshot)).rows;
    await assert.rejects(
      port.retire(r, { ...c.administration, actor: { kind: "system", id: "other" } }),
      { code: "conflict" },
    );
    assert.deepEqual((await admin.query(f.snapshot)).rows, after);
  } finally {
    await pool.end();
    await admin.end();
  }
});

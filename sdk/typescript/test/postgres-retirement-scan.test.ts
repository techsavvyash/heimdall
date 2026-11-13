import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { Pool } from "pg";
import { test } from "vitest";
import { validateAuditShape } from "../src/internal/audit-shape.js";
import { validateAuditEvent } from "../src/internal/audit.js";
import { PostgresAudit } from "../src/internal/postgres/audit.js";

const fixture = JSON.parse(
  readFileSync(
    new URL("../../../contracts/fixtures/retirement-scan.json", import.meta.url),
    "utf8",
  ),
) as {
  cases: {
    name: string;
    productId: string;
    environment: string;
    tenantId: string | null;
    now?: number;
    afterRetirement?: boolean;
    eventIds?: string[];
    scanned?: string[];
    excluded?: string[];
  }[];
};
const url = process.env.HEIMDALL_TEST_RETIRE_SCAN_URL;
for (const c of fixture.cases.filter(
  (c) => !!c.afterRetirement === (process.env.HEIMDALL_TEST_RETIRE_SCAN_AFTER === "1"),
)) {
  test.skipIf(!url)(`retirement scan: ${c.name}`, async () => {
    const pool = new Pool({ connectionString: url });
    try {
      const port = PostgresAudit.forRetainedRuntime(
        pool,
        c.productId,
        c.environment,
        ["siem"],
        (event) => {
          validateAuditShape(event);
          validateAuditEvent(event as Parameters<typeof validateAuditEvent>[0], "sdk", () => false);
        },
      );
      const now = c.now ?? 10000;
      const duration = 1000;
      const scanned: string[] = [];
      const found: string[] = [];
      let after: Parameters<typeof port.scanRetirementCandidates>[3] = null;
      for (let pageNumber = 0; pageNumber < 100; pageNumber++) {
        const page = await port.scanRetirementCandidates(c.tenantId, now, duration, after, 1);
        found.push(...page.eventIds);
        if (!page.next) break;
        scanned.push(page.next.eventId);
        await assert.rejects(
          port.scanRetirementCandidates(c.tenantId, now + 1, duration, page.next, 1),
          {
            code: "invalid_argument",
          },
        );
        await assert.rejects(
          port.scanRetirementCandidates("different-tenant", now, duration, page.next, 1),
          {
            code: "invalid_argument",
          },
        );
        after = page.next;
      }
      assert.ok(scanned.length < 100);
      if (c.scanned) assert.deepEqual(scanned, c.scanned);
      if (c.eventIds) assert.deepEqual(found, c.eventIds);
      for (const id of c.excluded ?? []) {
        if (c.afterRetirement) assert.ok(!scanned.includes(id), id);
        assert.ok(!found.includes(id), id);
      }
      await assert.rejects(
        port.scanRetirementCandidates(c.tenantId, now, duration, null, 1, AbortSignal.abort()),
        {
          code: "cancelled",
        },
      );
    } finally {
      await pool.end();
    }
  });
}

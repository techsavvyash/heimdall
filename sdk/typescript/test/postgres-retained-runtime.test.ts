import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { Pool } from "pg";
import { expect, test } from "vitest";
import { PostgresAudit } from "../src/internal/postgres/audit.js";
import { validateAuditShape } from "../src/internal/audit-shape.js";
import { validateAuditEvent } from "../src/internal/audit.js";
const root = new URL("../../../contracts/fixtures/", import.meta.url);
const fixture = JSON.parse(readFileSync(new URL("retained-runtime.json", root), "utf8")) as {
  cases: {
    name: string;
    operation: string;
    base: string;
    patch?: Record<string, unknown>;
    expected: string;
    before?: string;
    stable?: boolean;
  }[];
  loss: Record<string, unknown>;
};
const snapshot = JSON.parse(readFileSync(new URL("retained-writes.json", root), "utf8"))
  .snapshot as string;
const bases = () =>
  JSON.parse(readFileSync(new URL("audit-capacity-writes.json", root), "utf8")) as Record<
    string,
    Record<string, unknown>
  >;
test.skipIf(!process.env.HEIMDALL_TEST_RETAINED_RUNTIME_URL)(
  "strict v4 retained runtime operations",
  async () => {
    const pool = new Pool({ connectionString: process.env.HEIMDALL_TEST_RETAINED_RUNTIME_URL });
    const admin = new Pool({ connectionString: process.env.HEIMDALL_TEST_RETAINED_RUNTIME_ADMIN });
    const port = PostgresAudit.forRetainedRuntime(
      pool,
      "ledger",
      "production",
      ["siem"],
      (event) => {
        validateAuditShape(event);
        validateAuditEvent(event as Parameters<typeof validateAuditEvent>[0], "sdk", () => false);
      },
    );
    try {
      if (process.env.HEIMDALL_TEST_RETAINED_RUNTIME_REJECT === "1") {
        await assert.rejects(port.append(bases().ordinary!), { code: "unavailable" });
        const f = bases();
        await assert.rejects(port.recover(f.terminal!, f.administration!, 2001), {
          code: "unavailable",
        });
        await assert.rejects(port.scanPendingDecisions(2001, null, 1), { code: "unavailable" });
        await assert.rejects(port.readResolution("decision-1"), { code: "unavailable" });
        return;
      }
      if (process.env.HEIMDALL_TEST_RETAINED_RUNTIME_LOSS === "1") {
        const event = { ...bases().ordinary, ...fixture.loss };
        if (process.env.HEIMDALL_TEST_RETAINED_RUNTIME_FAULT === "1") {
          await assert.rejects(port.append(event), { code: "outcome_unknown" });
          return;
        }
        const receipt = await port.append(event);
        assert.deepEqual(await port.resolveReceipt(event), receipt);
        return;
      }
      for (const c of fixture.cases) {
        if (c.before) await admin.query(c.before);
        const before = (await admin.query(snapshot)).rows;
        const f = bases();
        const event = { ...f[c.base], ...c.patch };
        let status = "receipt";
        try {
          switch (c.operation) {
            case "append": {
              const receipt = await port.append(event);
              assert.deepEqual(await port.resolveReceipt(event), receipt);
              break;
            }
            case "recover":
              await port.recover(f.terminal!, f.administration!, 2001);
              break;
            case "scan":
              status =
                (await port.scanPendingDecisions(2001, null, 1)).items.length === 0
                  ? "empty"
                  : "items";
              break;
            case "resolution":
              status = (await port.readResolution("decision-1"))?.state ?? "absent";
              break;
            case "inspect":
              status = (await port.inspect("tenant-1", "ordinary"))?.state ?? "absent";
              break;
            case "eligibility":
              status = (await port.eligibility("tenant-1", "ordinary", 3001, 1000)) ?? "absent";
              break;
            default:
              throw new Error("unknown operation");
          }
        } catch (error) {
          status = (error as Error).message;
        }
        expect({ name: c.name, status }).toEqual({ name: c.name, status: c.expected });
        if (c.stable) assert.deepEqual((await admin.query(snapshot)).rows, before);
      }
      await expect(port.append(bases().ordinary!, AbortSignal.abort())).rejects.toThrow(
        "cancelled",
      );
      await expect(pool.query("DELETE FROM heimdall_retention.event_identities")).rejects.toThrow(
        "permission denied",
      );
      await expect(
        pool.query("UPDATE heimdall_retention.capacity SET row_limit=row_limit+1"),
      ).rejects.toThrow("permission denied");
    } finally {
      await pool.end();
      await admin.end();
    }
  },
);

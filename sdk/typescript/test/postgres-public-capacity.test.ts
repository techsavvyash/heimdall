import { readFileSync } from "node:fs";
import { Pool } from "pg";
import { expect, test } from "vitest";
import { createPostgresStorage } from "../src/postgres.js";
test.skipIf(!process.env.HEIMDALL_TEST_CAPACITY_READY_URL)(
  "public TS writes enforce v3 and reserved capacity",
  async () => {
    const url = process.env.HEIMDALL_TEST_CAPACITY_READY_URL;
    const admin = new Pool({ connectionString: url });
    const pool = new Pool({ connectionString: url, options: "-c role=public_capacity_runtime" });
    const f = JSON.parse(
      readFileSync(
        new URL("../../../contracts/fixtures/audit-capacity-writes.json", import.meta.url),
        "utf8",
      ),
    );
    for (const event of [f.request, f.normal, f.ordinary]) {
      event.productId = "public-ts";
      event.environment = "production";
    }
    const storage = createPostgresStorage(pool, {
      productId: "public-ts",
      environment: "production",
      auditSinks: ["siem"],
    });
    const signal = new AbortController().signal;
    const original = (await admin.query("SELECT event_limit::text FROM heimdall_capacity.policy"))
      .rows[0].event_limit;
    try {
      await storage.ready(signal);
      await admin.query(
        "UPDATE heimdall_capacity.policy SET event_limit=used_events+reserved_events+3; UPDATE heimdall_capacity.upgrade SET event_limit=(SELECT event_limit FROM heimdall_capacity.policy)",
      );
      const receipt = await storage.appendAudit(f.request, signal);
      expect(await storage.appendAudit(f.request, signal)).toEqual(receipt);
      await expect(storage.appendAudit(f.ordinary, signal)).rejects.toThrow("audit_unavailable");
      const done = await storage.appendAudit(f.normal, signal);
      expect(await storage.appendAudit(f.normal, signal)).toEqual(done);
      expect(
        (
          await admin.query(
            "SELECT count(*)::int AS n FROM heimdall.audit_events WHERE product_id='public-ts'",
          )
        ).rows[0].n,
      ).toBe(2);
      expect(
        (
          await admin.query(
            "SELECT count(*)::int AS n FROM heimdall_capacity.event_charges WHERE product_id='public-ts'",
          )
        ).rows[0].n,
      ).toBe(2);
      for (const state of ["migrating", "ready"]) {
        await admin.query("UPDATE heimdall.schema_metadata SET state=$1,database_version=2", [
          state,
        ]);
        await expect(storage.ready(signal)).rejects.toThrow("unavailable");
        await expect(storage.appendAudit(f.normal, signal)).rejects.toThrow("unavailable");
      }
    } finally {
      await admin.query("UPDATE heimdall.schema_metadata SET state='ready',database_version=3");
      await admin.query("UPDATE heimdall_capacity.policy SET event_limit=$1", [original]);
      await admin.query("UPDATE heimdall_capacity.upgrade SET event_limit=$1", [original]);
      await pool.end();
      await admin.end();
    }
  },
);

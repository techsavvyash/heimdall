import { Pool } from "pg";
import { expect, test } from "vitest";
import { PostgresAudit } from "../src/internal/postgres/audit.js";
import { validateAuditShape } from "../src/internal/audit-shape.js";
import { validateAuditEvent } from "../src/internal/audit.js";
test.skipIf(!process.env.HEIMDALL_TEST_RETENTION_URL)(
  "prepared identities persist validated groups atomically",
  async () => {
    const pool = new Pool({ connectionString: process.env.HEIMDALL_TEST_RETENTION_URL });
    const port = (p: string, e: string) =>
      new PostgresAudit(pool, p, e, [], (event) => {
        validateAuditShape(event);
        validateAuditEvent(event as Parameters<typeof validateAuditEvent>[0], "sdk", () => false);
      });
    try {
      const groups = await pool.query(
        "SELECT d.product_id,d.environment,d.resolution,array_remove(ARRAY[d.request_event_id,d.terminal_event_id,r.recovery_event_id],NULL) AS ids FROM heimdall.audit_decisions d JOIN heimdall_capacity.reservations r USING(product_id,environment,decision_id) ORDER BY 1,2,3",
      );
      const first = groups.rows[0]!;
      const a = port(first.product_id, first.environment);
      await pool.query(
        "CREATE FUNCTION heimdall_retention.test_refuse() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test rollback' USING ERRCODE='23514'; END $$; CREATE TRIGGER test_refuse BEFORE INSERT ON heimdall_retention.decision_identities FOR EACH ROW EXECUTE FUNCTION heimdall_retention.test_refuse()",
      );
      await expect(a.retainIdentityGroup("tenant-1", first.resolution, first.ids)).rejects.toThrow(
        "audit_unavailable",
      );
      expect(
        (await pool.query("SELECT count(*)::int AS n FROM heimdall_retention.event_identities"))
          .rows[0].n,
      ).toBe(0);
      await pool.query(
        "DROP TRIGGER test_refuse ON heimdall_retention.decision_identities; DROP FUNCTION heimdall_retention.test_refuse()",
      );
      await expect(a.retainIdentityGroup("other", first.resolution, first.ids)).rejects.toThrow(
        "audit_unavailable",
      );
      await expect(
        a.retainIdentityGroup("tenant-1", first.resolution, first.ids, AbortSignal.abort()),
      ).rejects.toThrow("cancelled");
      const key = [first.product_id, first.environment, first.ids[0]];
      const saved = (
        await pool.query(
          "SELECT encode(payload_digest,'hex') AS digest FROM heimdall.audit_events WHERE product_id=$1 AND environment=$2 AND event_id=$3",
          key,
        )
      ).rows[0].digest;
      await pool.query(
        "UPDATE heimdall.audit_events SET payload_digest=decode(repeat('00',32),'hex') WHERE product_id=$1 AND environment=$2 AND event_id=$3",
        key,
      );
      await expect(a.retainIdentityGroup("tenant-1", first.resolution, first.ids)).rejects.toThrow(
        "audit_unavailable",
      );
      await pool.query(
        "UPDATE heimdall.audit_events SET payload_digest=decode($4,'hex') WHERE product_id=$1 AND environment=$2 AND event_id=$3",
        [...key, saved],
      );
      for (const g of groups.rows) {
        const p = port(g.product_id, g.environment);
        const original = await p.retainIdentityGroup("tenant-1", g.resolution, g.ids);
        expect(await p.retainIdentityGroup("tenant-1", g.resolution, g.ids)).toEqual(original);
      }
      const ordinary = await pool.query(
        "SELECT product_id,environment,event_id FROM heimdall.audit_events e WHERE NOT EXISTS(SELECT 1 FROM heimdall_retention.event_identities i WHERE (i.product_id,i.environment,i.event_id)=(e.product_id,e.environment,e.event_id))",
      );
      for (const row of ordinary.rows) {
        const p = port(row.product_id, row.environment);
        const original = await p.retainIdentityGroup("tenant-1", "ordinary", [row.event_id]);
        expect(await p.retainIdentityGroup("tenant-1", "ordinary", [row.event_id])).toEqual(
          original,
        );
      }
      expect(
        (
          await pool.query(
            "SELECT (SELECT count(*) FROM heimdall.audit_events)=(SELECT count(*) FROM heimdall_retention.event_identities) AS events,(SELECT count(*) FROM heimdall.audit_decisions)=(SELECT count(*) FROM heimdall_retention.decision_identities) AS decisions",
          )
        ).rows[0],
      ).toEqual({ events: true, decisions: true });
    } finally {
      await pool.end();
    }
  },
);

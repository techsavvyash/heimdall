import { readFileSync } from "node:fs";
import { Ajv2020 } from "ajv/dist/2020.js";
import { Pool } from "pg";
import { expect, test } from "vitest";
import { PostgresAudit } from "../src/internal/postgres/audit.js";
import { validateAuditEvent } from "../src/internal/audit.js";
import { HeimdallError } from "../src/internal/errors.js";
const root = new URL("../../../contracts/", import.meta.url);
const fixtures = JSON.parse(readFileSync(new URL("fixtures/audit-digests-v1.json", root), "utf8"));
const shape = new Ajv2020({ strict: true }).compile(
  JSON.parse(readFileSync(new URL("audit-event.schema.json", root), "utf8")),
);
const validate = (event: Record<string, unknown>) => {
  if (!shape(event)) throw new HeimdallError("invalid_argument");
  validateAuditEvent(event as Parameters<typeof validateAuditEvent>[0], "sdk", () => false);
};
test("audit rejects invalid envelope before acquiring a connection", async () => {
  const pool = new Pool();
  const store = new PostgresAudit(pool, "ledger", "production", [], validate);
  try {
    await expect(store.append({ eventId: "x" })).rejects.toThrow("invalid_argument");
  } finally {
    await pool.end();
  }
});
test.skipIf(!process.env.HEIMDALL_TEST_DATABASE_URL || !!process.env.HEIMDALL_TEST_READ_PEER)(
  "atomic audit persistence",
  async () => {
    const pool = new Pool({ connectionString: process.env.HEIMDALL_TEST_DATABASE_URL });
    const runtime = new Pool({
      connectionString: process.env.HEIMDALL_TEST_DATABASE_URL,
      options: `-c role=${process.env.HEIMDALL_TEST_AUDIT_ROLE}`,
    });
    const store = new PostgresAudit(
      runtime,
      "ledger",
      "production",
      ["primary", "secondary"],
      validate,
    );
    const event = fixtures[0].event;
    try {
      const unicode = JSON.parse(
        readFileSync(new URL("fixtures/audit-storage-unicode.json", root), "utf8"),
      );
      expect((await store.append(unicode)).eventId).toBe("unicode-nul-shared");
      const receipt = await store.append(event);
      expect(receipt.payloadDigest).toBe(fixtures[0].digest);
      expect(await store.append(fixtures[1].event)).toEqual(receipt);
      const race = { ...event, eventId: "ts-audit-race" };
      const receipts = await Promise.all(Array.from({ length: 8 }, () => store.append(race)));
      expect(new Set(receipts.map((r) => r.payloadDigest)).size).toBe(1);
      expect(
        (
          await pool.query(
            "SELECT count(*)::int AS n FROM heimdall.audit_deliveries WHERE event_id='ts-audit-race'",
          )
        ).rows[0].n,
      ).toBe(2);
      await expect(store.append({ ...race, action: "different" })).rejects.toThrow("conflict");
      await expect(store.append({ ...event, productId: "support" })).rejects.toThrow(
        "invalid_argument",
      );
      await expect(
        store.append({ ...event, eventId: "ts-cancelled" }, AbortSignal.abort()),
      ).rejects.toThrow("cancelled");
      await pool.query(
        "ALTER TABLE heimdall.audit_deliveries ADD CONSTRAINT ts_test_failure CHECK(sink_id<>'z-ts-blocked')",
      );
      try {
        const failing = new PostgresAudit(
          runtime,
          "ledger",
          "production",
          ["a-good", "z-ts-blocked"],
          validate,
        );
        await expect(failing.append({ ...event, eventId: "ts-audit-rollback" })).rejects.toThrow(
          "unavailable",
        );
        expect(
          (
            await pool.query(
              "SELECT count(*)::int AS n FROM heimdall.audit_events WHERE event_id='ts-audit-rollback'",
            )
          ).rows[0].n,
        ).toBe(0);
        expect(
          (
            await pool.query(
              "SELECT count(*)::int AS n FROM heimdall.audit_deliveries WHERE event_id='ts-audit-rollback'",
            )
          ).rows[0].n,
        ).toBe(0);
      } finally {
        await pool.query("ALTER TABLE heimdall.audit_deliveries DROP CONSTRAINT ts_test_failure");
      }
      await pool.query(
        "UPDATE heimdall.audit_events SET event=jsonb_set(event,'{action}','\"tampered\"') WHERE event_id='ts-audit-race'",
      );
      await expect(store.append(race)).rejects.toThrow("audit_unavailable");
    } finally {
      await runtime.end();
      await pool.end();
    }
  },
  15000,
);
test.skipIf(!process.env.HEIMDALL_TEST_DATABASE_URL || !process.env.HEIMDALL_TEST_READ_PEER)(
  "reads Go audit receipt without changing original sink routing",
  async () => {
    const pool = new Pool({ connectionString: process.env.HEIMDALL_TEST_DATABASE_URL });
    try {
      const store = new PostgresAudit(pool, "ledger", "production", ["new-sink"], validate);
      const receipt = await store.append({ ...fixtures[0].event, eventId: "go-audit-shared" });
      expect(receipt.eventId).toBe("go-audit-shared");
      expect(
        (
          await pool.query(
            "SELECT sink_id FROM heimdall.audit_deliveries WHERE event_id='go-audit-shared' ORDER BY sink_id",
          )
        ).rows,
      ).toEqual([{ sink_id: "primary" }, { sink_id: "secondary" }]);
    } finally {
      await pool.end();
    }
  },
);

test.skipIf(!process.env.HEIMDALL_TEST_AUDIT_COMMIT_LOSS)(
  "audit committed response loss",
  async () => {
    const pool = new Pool({ connectionString: process.env.HEIMDALL_TEST_AUDIT_COMMIT_LOSS });
    const direct = new Pool({ connectionString: process.env.HEIMDALL_TEST_DATABASE_URL });
    const event = { ...fixtures[0].event, eventId: "ts-audit-lost-commit" };
    try {
      const store = new PostgresAudit(
        pool,
        "ledger",
        "production",
        ["primary", "secondary"],
        validate,
      );
      await expect(store.append(event)).rejects.toThrow("outcome_unknown");
      expect(
        (
          await direct.query(
            "SELECT count(*)::int AS n FROM heimdall.audit_deliveries WHERE event_id=$1",
            [event.eventId],
          )
        ).rows[0].n,
      ).toBe(2);
      const recovered = new PostgresAudit(direct, "ledger", "production", ["new-sink"], validate);
      expect((await recovered.append(event)).eventId).toBe(event.eventId);
      expect(
        (
          await direct.query(
            "SELECT count(*)::int AS n FROM heimdall.audit_deliveries WHERE event_id=$1",
            [event.eventId],
          )
        ).rows[0].n,
      ).toBe(2);
    } finally {
      await pool.end();
      await direct.end();
    }
  },
  15000,
);

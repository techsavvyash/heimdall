import { readFileSync } from "node:fs";
import { Pool } from "pg";
import { expect, test, vi } from "vitest";
import { RecordCipher } from "../src/internal/envelopes.js";
import { PostgresRecords } from "../src/internal/postgres/records.js";
import { SessionRecords } from "../src/internal/postgres/sessions.js";
import {
  registerRecordKeyUsage,
  retireRecordKeyUsage,
} from "../src/internal/postgres/key-usage.js";
const fixture = JSON.parse(
  readFileSync(
    new URL("../../../contracts/fixtures/metered-records.json", import.meta.url),
    "utf8",
  ),
) as { initialLimit: number; rotationLimit: number; expiresAt: number; payload: string };
const expires = fixture.expiresAt;
const payload = Buffer.from(fixture.payload);
test.skipIf(!process.env.HEIMDALL_TEST_KEYS_ADMIN)(
  "all record writers consume committed reservations",
  async () => {
    const app = new Pool({ connectionString: process.env.HEIMDALL_TEST_KEYS_ADMIN, max: 1 });
    const usage = new Pool({ connectionString: process.env.HEIMDALL_TEST_KEYS_RUNTIME, max: 1 });
    const cipher = new RecordCipher("old", { old: Buffer.alloc(32, 11) });
    const seal = vi.spyOn(cipher, "seal");
    const records = new PostgresRecords(app, usage, cipher, "metered-ts", "test");
    const sessions = new SessionRecords(app, usage, cipher, "metered-ts", "test", () => ({
      productId: "metered-ts",
      environment: "test",
      issuer: "https://issuer.example",
      subject: "user",
    }));
    try {
      await registerRecordKeyUsage(app, cipher.activeFingerprint(), fixture.initialLimit, true);
      const login = await records.create("login", "login", payload, expires);
      expect(
        await records.compareAndSwap("login", "login", "9223372036854775807", payload, expires),
      ).toBeNull();
      const session = await sessions.createSession("session", payload, expires);
      expect(
        await sessions.compareAndSwapSession("session", session!.revision, payload, expires),
      ).not.toBeNull();
      await expect(records.create("login", "login", payload, expires)).rejects.toThrow("conflict");
      await sessions.invalidateIdentity("https://issuer.example", "user", 1, "inactive");
      await expect(sessions.createSession("denied", payload, expires)).rejects.toThrow(
        "unauthenticated",
      );
      expect(seal).toHaveBeenCalledTimes(fixture.initialLimit);
      for (const attempt of [
        () => records.create("login", "over", payload, expires),
        () => records.compareAndSwap("login", "login", login!.revision, payload, expires),
        () => sessions.createSession("over", payload, expires),
        () => sessions.compareAndSwapSession("session", session!.revision, payload, expires),
      ])
        await expect(attempt()).rejects.toThrow("unavailable");
      expect(seal).toHaveBeenCalledTimes(fixture.initialLimit);
      const alias = new RecordCipher("renamed", { renamed: Buffer.alloc(32, 11) });
      expect(alias.activeFingerprint()).toBe(cipher.activeFingerprint());
      await expect(
        new PostgresRecords(app, usage, alias, "other", "test").create(
          "login",
          "alias",
          payload,
          expires,
        ),
      ).rejects.toThrow("unavailable");
      await retireRecordKeyUsage(app, cipher.activeFingerprint());
      expect((await records.get("login", "login"))!.value).toEqual(payload);
      const rotated = new RecordCipher("next", {
        old: Buffer.alloc(32, 11),
        next: Buffer.alloc(32, 13),
      });
      await registerRecordKeyUsage(app, rotated.activeFingerprint(), fixture.rotationLimit, true);
      const replacement = new PostgresRecords(app, usage, rotated, "metered-ts", "test");
      expect((await replacement.get("login", "login"))!.value).toEqual(payload);
      expect(
        await replacement.compareAndSwap("login", "login", login!.revision, payload, expires),
      ).not.toBeNull();
      await expect(replacement.create("login", "over", payload, expires)).rejects.toThrow(
        "unavailable",
      );
      expect(
        (
          await app.query(
            "SELECT used::int FROM heimdall_keys.record_key_usage WHERE fingerprint=decode($1,'hex')",
            [cipher.activeFingerprint()],
          )
        ).rows[0].used,
      ).toBe(fixture.initialLimit);
    } finally {
      seal.mockRestore();
      await Promise.all([app.end(), usage.end()]);
    }
  },
);
test.skipIf(!process.env.HEIMDALL_TEST_KEYS_LOSS)(
  "uncertain reservation prevents encryption and record persistence",
  async () => {
    const app = new Pool({ connectionString: process.env.HEIMDALL_TEST_KEYS_ADMIN });
    const usage = new Pool({ connectionString: process.env.HEIMDALL_TEST_KEYS_LOSS });
    const cipher = new RecordCipher("loss", { loss: Buffer.alloc(32, 14) });
    const seal = vi.spyOn(cipher, "seal");
    try {
      await registerRecordKeyUsage(app, cipher.activeFingerprint(), 10, true);
      const records = new PostgresRecords(app, usage, cipher, "metered-loss-ts", "test");
      await expect(records.create("login", "loss", payload, expires)).rejects.toThrow(
        "outcome_unknown",
      );
      expect(seal).not.toHaveBeenCalled();
      expect(await records.get("login", "loss")).toBeNull();
    } finally {
      seal.mockRestore();
      await Promise.all([app.end(), usage.end()]);
    }
  },
);

test("pre-cancelled record writes never encrypt or open a connection", async () => {
  const pool = new Pool();
  const cipher = new RecordCipher("test", { test: Buffer.alloc(32, 11) });
  const seal = vi.spyOn(cipher, "seal");
  try {
    const records = new PostgresRecords(pool, pool, cipher, "ledger", "test");
    await expect(
      records.create("login", "cancelled", payload, expires, AbortSignal.abort()),
    ).rejects.toThrow("cancelled");
    expect(seal).not.toHaveBeenCalled();
    expect(pool.totalCount).toBe(0);
  } finally {
    seal.mockRestore();
    await pool.end();
  }
});

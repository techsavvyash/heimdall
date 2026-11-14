import { readFileSync } from "node:fs";
import { Pool } from "pg";
import { expect, test } from "vitest";
import { RecordCipher } from "../src/internal/envelopes.js";
import { PostgresRecords } from "../src/internal/postgres/records.js";
const fixture = JSON.parse(
  readFileSync(new URL("../../../contracts/fixtures/key-rotation.json", import.meta.url), "utf8"),
) as { expiresAt: number; payload: string; keys: Record<string, number>; phases: string[] };
test.skipIf(!process.env.HEIMDALL_ROTATION_PRODUCT)(
  "separate product and ledger rotation phases",
  async () => {
    const app = new Pool({ connectionString: process.env.HEIMDALL_ROTATION_PRODUCT, max: 1 });
    const ledger = new Pool({ connectionString: process.env.HEIMDALL_ROTATION_LEDGER, max: 1 });
    const phase = process.env.HEIMDALL_ROTATION_PHASE!;
    expect(fixture.phases).toContain(phase);
    const keys = Object.fromEntries(
      Object.entries(fixture.keys).map(([id, value]) => [id, Buffer.alloc(32, value)]),
    );
    const records = (active: string) =>
      new PostgresRecords(app, ledger, new RecordCipher(active, keys), "ledger", "production");
    const value = Buffer.from(fixture.payload);
    try {
      const actions: Record<string, () => Promise<void>> = {
        initial: async () => {
          await records("old").create("session", "snapshot", value, fixture.expiresAt);
        },
        rolling: async () => {
          expect((await records("next").get("session", "snapshot"))!.value).toEqual(value);
          await records("next").create("login", "rotated", value, fixture.expiresAt);
          await records("old").create("login", "lagging", value, fixture.expiresAt);
          await records("old").delete("session", "snapshot");
        },
        retired: async () => {
          await expect(
            records("old").create("login", "retired", value, fixture.expiresAt),
          ).rejects.toThrow("unavailable");
          expect((await records("next").get("login", "lagging"))!.value).toEqual(value);
          expect(await records("next").get("session", "snapshot")).toBeNull();
          await records("next").create("login", "after-retirement", value, fixture.expiresAt);
        },
        recovered: async () => {
          expect(await records("recovery").get("session", "snapshot")).toBeNull();
          expect(await records("recovery").get("login", "rotated")).toBeNull();
          for (const active of ["old", "next"])
            await expect(
              records(active).create("login", "refused-" + active, value, fixture.expiresAt),
            ).rejects.toThrow("unavailable");
          await records("recovery").create("session", "fresh", value, fixture.expiresAt);
          expect((await records("recovery").get("session", "fresh"))!.value).toEqual(value);
        },
      };
      await actions[phase]!();
    } finally {
      await Promise.all([app.end(), ledger.end()]);
    }
  },
);

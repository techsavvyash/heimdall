import { readFileSync } from "node:fs";
import { createHash } from "node:crypto";
import { Pool } from "pg";
import { expect, test } from "vitest";
import {
  checkCapacityReadiness,
  checkManagedCapacityReadiness,
} from "../src/internal/postgres/readiness.js";
const root = new URL("../../../contracts/", import.meta.url);
test("shared retention preparation pins all SQL and the PostgreSQL catalog", () => {
  const fixture = JSON.parse(
    readFileSync(new URL("fixtures/audit-retention-upgrade.json", root), "utf8"),
  ) as {
    files: { path: string; checksum: string }[];
    schemaSQLChecksum: string;
    signatureChecksum: string;
  };
  expect(fixture.files).toHaveLength(6);
  for (const file of fixture.files) {
    expect(
      createHash("sha256")
        .update(readFileSync(new URL("persistence/session/" + file.path, root)))
        .digest("hex"),
    ).toBe(file.checksum);
  }
  for (const [file, checksum] of [
    ["audit_retention_schema.sql", fixture.schemaSQLChecksum],
    ["audit_retention_schema.json", fixture.signatureChecksum],
  ]) {
    expect(
      createHash("sha256")
        .update(readFileSync(new URL("../../go/internal/postgres/" + file, import.meta.url)))
        .digest("hex"),
    ).toBe(checksum);
  }
});
test.skipIf(!process.env.HEIMDALL_TEST_RETENTION_PREPARED_URL)(
  "prepared retention refuses old runtime admission",
  async () => {
    const pool = new Pool({
      connectionString: process.env.HEIMDALL_TEST_RETENTION_PREPARED_URL,
      max: 1,
    });
    try {
      await expect(checkCapacityReadiness(pool)).rejects.toThrow("unavailable");
      await expect(checkManagedCapacityReadiness(pool)).rejects.toThrow("unavailable");
    } finally {
      await pool.end();
    }
  },
);

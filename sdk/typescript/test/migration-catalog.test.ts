import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { expect, it } from "vitest";
import { validateStorageCompatibility } from "../src/internal/storage-contract.js";
it("binds real shared SQL bytes to the storage compatibility policy", () => {
  const root = new URL("../../../contracts/persistence/", import.meta.url);
  const catalog = JSON.parse(readFileSync(new URL("catalog.json", root), "utf8")) as {
    formatVersion: number;
    session: Array<{ version: number; path: string; checksum: string }>;
  };
  expect(catalog.formatVersion).toBe(1);
  expect(catalog.session.length).toBeGreaterThan(0);
  for (const [index, migration] of catalog.session.entries()) {
    expect(migration.version).toBe(index + 1);
    expect(migration.path).toMatch(/^session\/[0-9]{4}\.sql$/);
    const sql = readFileSync(new URL(migration.path, root));
    expect(sql.includes(13)).toBe(false);
    expect(createHash("sha256").update(sql).digest("hex")).toBe(migration.checksum);
  }
  const migrations = catalog.session.map(({ version, checksum }) => ({ version, checksum }));
  const metadata = {
    databaseVersion: migrations.length,
    state: "ready" as const,
    readers: { min: 1, max: 1 },
    writers: { min: 1, max: 1 },
    migrations,
  };
  const policy = {
    readerVersion: 1,
    writerVersion: 1,
    minDatabaseVersion: 1,
    maxDatabaseVersion: migrations.length,
    migrations,
  };
  expect(() => validateStorageCompatibility(metadata, policy)).not.toThrow();
  expect(() =>
    validateStorageCompatibility(
      { ...metadata, migrations: [{ version: 1, checksum: "0".repeat(64) }] },
      policy,
    ),
  ).toThrow("unavailable");
});

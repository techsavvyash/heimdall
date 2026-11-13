import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { expect, it } from "vitest";
import { validateStorageCompatibility } from "../src/internal/storage-contract.js";
it.each(["session", "managed", "keys", "setup"] as const)(
  "binds %s SQL bytes to compatibility policy",
  (family) => {
    const root = new URL("../../../contracts/persistence/", import.meta.url);
    const catalog = JSON.parse(readFileSync(new URL("catalog.json", root), "utf8")) as {
      formatVersion: number;
      session: Array<{ version: number; path: string; checksum: string }>;
      managed: Array<{ version: number; path: string; checksum: string }>;
      setup: Array<{ version: number; path: string; checksum: string }>;
      keys: Array<{ version: number; path: string; checksum: string }>;
    };
    expect(catalog.formatVersion).toBe(1);
    expect(catalog[family].length).toBeGreaterThan(0);
    const sessionPaths: Record<number, string> = {
      3: "session/0003-capacity.sql",
      4: "session/0004-retention-identities.sql",
      5: "session/0005-hold-release-reservations.sql",
    };
    for (const [index, migration] of catalog[family].entries()) {
      expect(migration.version).toBe(index + 1);
      const regularPath = `${family}/${String(index + 1).padStart(4, "0")}.sql`;
      expect(migration.path).toBe(
        family === "session" ? (sessionPaths[migration.version] ?? regularPath) : regularPath,
      );
      const sql = readFileSync(new URL(migration.path, root));
      expect(sql.includes(13)).toBe(false);
      expect(createHash("sha256").update(sql).digest("hex")).toBe(migration.checksum);
    }
    const migrations = catalog[family].map(({ version, checksum }) => ({ version, checksum }));
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
  },
);

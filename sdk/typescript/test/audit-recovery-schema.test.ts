import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { expect, test } from "vitest";

test("prepared audit recovery SQL matches its shared proof checksum", () => {
  const fixture = JSON.parse(
    readFileSync(
      new URL("../../../contracts/fixtures/audit-recovery-schema.json", import.meta.url),
      "utf8",
    ),
  );
  const sql = readFileSync(
    new URL("../../../contracts/persistence/session/0002.sql", import.meta.url),
  );
  expect(createHash("sha256").update(sql).digest("hex")).toBe(fixture.checksum);
});

test("prepared audit backfill SQL matches its shared proof checksums", () => {
  const fixture = JSON.parse(
    readFileSync(
      new URL("../../../contracts/fixtures/audit-backfill.json", import.meta.url),
      "utf8",
    ),
  );
  for (const item of fixture.prepared) {
    const sql = readFileSync(
      new URL(`../../../contracts/persistence/${item.path}`, import.meta.url),
    );
    expect(createHash("sha256").update(sql).digest("hex")).toBe(item.checksum);
  }
});

test("audit upgrade preparation files match shared checksums", () => {
  const f = JSON.parse(
    readFileSync(
      new URL("../../../contracts/fixtures/audit-upgrade-prepare.json", import.meta.url),
      "utf8",
    ),
  );
  for (const item of f.files) {
    const sql = readFileSync(
      new URL(`../../../contracts/persistence/session/${item.path}`, import.meta.url),
    );
    expect(createHash("sha256").update(sql).digest("hex")).toBe(item.checksum);
  }
});

test("prepared audit capacity SQL matches the shared proof checksum", () => {
  const fixture = JSON.parse(
    readFileSync(
      new URL("../../../contracts/fixtures/audit-capacity-schema.json", import.meta.url),
      "utf8",
    ),
  );
  const sql = readFileSync(
    new URL(`../../../contracts/persistence/${fixture.path}`, import.meta.url),
  );
  expect(createHash("sha256").update(sql).digest("hex")).toBe(fixture.checksum);
});

test("offline capacity preparation matches the shared pinned SQL", () => {
  const fixture = JSON.parse(
    readFileSync(
      new URL("../../../contracts/fixtures/audit-capacity-upgrade.json", import.meta.url),
      "utf8",
    ),
  );
  for (const item of fixture.files) {
    const sql = readFileSync(
      new URL(`../../../contracts/persistence/session/${item.path}`, import.meta.url),
    );
    expect(createHash("sha256").update(sql).digest("hex")).toBe(item.checksum);
  }
});

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

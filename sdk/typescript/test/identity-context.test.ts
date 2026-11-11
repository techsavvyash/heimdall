import { readFileSync } from "node:fs";
import { expect, it } from "vitest";
import { parseIdentityContext, sessionIdentityContext } from "../src/internal/identity-context.js";
const cases = JSON.parse(
  readFileSync(
    new URL("../../../contracts/fixtures/identity-context.json", import.meta.url),
    "utf8",
  ),
) as { context: string; valid: boolean; key?: string; revision?: string }[];
it.each(cases)("validates identity context $context", (c) => {
  const result = (() => {
    try {
      return { value: parseIdentityContext(c.context) };
    } catch {
      return { error: "invalid_argument" };
    }
  })();
  expect(result).toEqual(
    c.valid
      ? { value: c.key ? { key: c.key, revision: c.revision } : null }
      : { error: "invalid_argument" },
  );
});
it("builds canonical contexts from loaded revisions", () => {
  expect(sessionIdentityContext("opaque_storage_key", "1")).toBe("session:opaque_storage_key:1");
  expect(() => sessionIdentityContext("a".repeat(129), "1")).toThrow("invalid_argument");
});

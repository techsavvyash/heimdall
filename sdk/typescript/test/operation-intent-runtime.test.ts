import { readFileSync } from "node:fs";
import { expect, test } from "vitest";
import {
  parseIntent,
  parseBoundIntent,
  intentDigests,
  type Intent,
} from "../src/internal/operations.js";
const root = new URL("../../../contracts/fixtures/", import.meta.url);
const cases = JSON.parse(readFileSync(new URL("operation-intent-runtime.json", root), "utf8")) as {
  name: string;
  json: string;
  valid: boolean;
}[];
for (const c of cases.filter((c) => c.valid))
  test(`intent parser ${c.name}`, () => {
    const value = parseIntent(c.json);
    expect(JSON.parse(JSON.stringify(value))).toEqual(JSON.parse(c.json));
    expect(intentDigests(value, "key").fingerprint).toMatch(/^[a-f0-9]{64}$/);
  });
for (const c of cases.filter((c) => !c.valid))
  test(`intent parser rejects ${c.name}`, () => {
    expect(() => parseIntent(c.json)).toThrow("invalid_argument");
  });
test("intent parser preserves all established digest vectors", () => {
  const cases = JSON.parse(readFileSync(new URL("operation-intent-v1.json", root), "utf8")) as {
    intent: Intent;
    idempotencyKey: string;
    valid: boolean;
    fingerprint: string;
    keyDigest: string;
  }[];
  for (const c of cases.filter((c) => c.valid))
    expect(intentDigests(parseIntent(JSON.stringify(c.intent)), c.idempotencyKey)).toEqual({
      fingerprint: c.fingerprint,
      keyDigest: c.keyDigest,
    });
});
test("intent parser bounds bytes and rejects native invalid Unicode", () => {
  const raw = cases[0]!.json;
  expect(() => parseIntent(" ".repeat(1048577) + raw)).toThrow("invalid_argument");
  expect(parseIntent(" ".repeat(1048576 - Buffer.byteLength(raw)) + raw).name).toBe("Acme");
  expect(() => parseIntent(raw.replace("Acme", "\ud800"))).toThrow("invalid_argument");
  const malformed = JSON.parse(raw);
  malformed.roles = ["viewer", "viewer"];
  malformed.kind = "assign_roles";
  malformed.name = "";
  malformed.workspaceId = "acme";
  expect(() => intentDigests(malformed, "key")).toThrow("invalid_argument");
  expect(() => intentDigests(null as unknown as Intent, "key")).toThrow("invalid_argument");
});

const bindings = JSON.parse(
  readFileSync(new URL("operation-intent-binding.json", root), "utf8"),
) as {
  name: string;
  json: string;
  binding: Parameters<typeof parseBoundIntent>[1];
  valid: boolean;
}[];
for (const c of bindings.filter((c) => c.valid))
  test(`bound intent ${c.name}`, () => {
    expect(parseBoundIntent(c.json, c.binding)).toEqual(parseIntent(c.json));
  });
for (const c of bindings.filter((c) => !c.valid))
  test(`bound intent rejects ${c.name}`, () => {
    expect(() => parseBoundIntent(c.json, c.binding)).toThrow("unavailable");
  });

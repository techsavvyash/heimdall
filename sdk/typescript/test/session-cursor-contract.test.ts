import { readFileSync } from "node:fs";
import { Ajv2020 } from "ajv/dist/2020.js";
import { describe, expect, it } from "vitest";
import { sessionStatus } from "../src/internal/session-contract.js";
import { validateCursorScope } from "../src/internal/cursor-contract.js";

const root = new URL("../../../contracts/", import.meta.url);
const read = (path: string): unknown => JSON.parse(readFileSync(new URL(path, root), "utf8"));
type SessionCase = {
  name: string;
  record: Parameters<typeof sessionStatus>[0];
  scope: Parameters<typeof sessionStatus>[1];
  now: number;
  freshnessMs: number;
  expected?: string;
  error?: string;
};
type CursorCase = {
  name: string;
  payload: Parameters<typeof validateCursorScope>[0];
  scope: Parameters<typeof validateCursorScope>[1];
  now: number;
  valid: boolean;
};
const sessions = read("fixtures/session-v1.json") as SessionCase[];
const cursors = read("fixtures/cursor-v1.json") as CursorCase[];
const ajv = new Ajv2020({ strict: true });
const sessionSchema = ajv.compile(read("session-payload.schema.json") as object);
const cursorSchema = ajv.compile(read("cursor-payload.schema.json") as object);

describe("session record semantics", () => {
  it.each(sessions)("structural precondition $name", (c) => {
    expect(sessionSchema(c.record)).toBe(true);
  });
  it.each(sessions.filter((c) => !c.error))("$name", (c) => {
    expect(sessionStatus(c.record, c.scope, c.now, c.freshnessMs)).toBe(c.expected);
  });
  it.each(sessions.filter((c) => c.error))("$name", (c) => {
    expect(() => sessionStatus(c.record, c.scope, c.now, c.freshnessMs)).toThrow(c.error);
  });
});
describe("cursor scope semantics", () => {
  it.each(cursors)("structural precondition $name", (c) => {
    expect(cursorSchema(c.payload)).toBe(true);
  });
  it.each(cursors.filter((c) => c.valid))("$name", (c) => {
    expect(() => validateCursorScope(c.payload, c.scope, c.now)).not.toThrow();
  });
  it.each(cursors.filter((c) => !c.valid))("$name", (c) => {
    expect(() => validateCursorScope(c.payload, c.scope, c.now)).toThrow("invalid_argument");
  });
});

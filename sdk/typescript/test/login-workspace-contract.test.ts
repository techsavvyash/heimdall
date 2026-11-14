import { readFileSync } from "node:fs";
import { Ajv2020 } from "ajv/dist/2020.js";
import { describe, expect, it } from "vitest";
import { validateLoginClaim } from "../src/internal/login-contract.js";
import { workspaceGate } from "../src/internal/workspace-contract.js";
const root = new URL("../../../contracts/", import.meta.url);
const read = (name: string): unknown => JSON.parse(readFileSync(new URL(name, root), "utf8"));
const ajv = new Ajv2020({ strict: true });
const loginShape = ajv.compile(read("login-payload.schema.json") as object);
const workspaceShape = ajv.compile(read("workspace-record.schema.json") as object);
const logins = read("fixtures/login-v1.json") as {
  name: string;
  record: Parameters<typeof validateLoginClaim>[0];
  scope: Parameters<typeof validateLoginClaim>[1];
  now: number;
  error?: string;
}[];
const workspaces = read("fixtures/workspace-gate-v1.json") as {
  name: string;
  record: Parameters<typeof workspaceGate>[0];
  scope: Parameters<typeof workspaceGate>[1];
  expected?: string;
  error?: string;
}[];
describe("login claim preconditions", () => {
  it.each(logins)("schema precondition $name", (c) => expect(loginShape(c.record)).toBe(true));
  it.each(logins.filter((c) => !c.error))("accepts $name", (c) =>
    expect(() => validateLoginClaim(c.record, c.scope, c.now)).not.toThrow(),
  );
  it.each(logins.filter((c) => c.error))("rejects $name", (c) =>
    expect(() => validateLoginClaim(c.record, c.scope, c.now)).toThrow(c.error),
  );
});
describe("managed workspace gate", () => {
  it.each(workspaces.filter((c) => c.record !== null))("schema precondition $name", (c) =>
    expect(workspaceShape(c.record)).toBe(true),
  );
  it.each(workspaces.filter((c) => !c.error))("$name", (c) =>
    expect(workspaceGate(c.record, c.scope)).toBe(c.expected),
  );
  it.each(workspaces.filter((c) => c.error))("rejects $name", (c) =>
    expect(() => workspaceGate(c.record, c.scope)).toThrow(c.error),
  );
});

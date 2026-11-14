import { readFileSync } from "node:fs";
import { Ajv2020 } from "ajv/dist/2020.js";
import { describe, expect, it } from "vitest";
import { intentDigests, nextOperationState } from "../src/internal/operations.js";
const root = new URL("../../../contracts/", import.meta.url);
const read = (path: string): unknown => JSON.parse(readFileSync(new URL(path, root), "utf8"));
type IntentCase = {
  name: string;
  intent: Parameters<typeof intentDigests>[0];
  idempotencyKey: string;
  valid: boolean;
  fingerprint?: string;
  keyDigest?: string;
};
type StateCase = {
  name: string;
  state: string;
  event: string;
  dispatchCount: number;
  generation: number;
  expectedGeneration: number;
  expected?: string;
  error?: string;
};
const intents = read("fixtures/operation-intent-v1.json") as IntentCase[];
const states = read("fixtures/operation-state-v1.json") as StateCase[];
const shape = new Ajv2020({ strict: true }).compile(read("operation-intent.schema.json") as object);
describe("immutable intent digests", () => {
  it.each(intents)("structural precondition $name", (c) => expect(shape(c.intent)).toBe(true));
  it.each(intents.filter((c) => c.valid))("$name", (c) => {
    expect(intentDigests(c.intent, c.idempotencyKey)).toEqual({
      fingerprint: c.fingerprint,
      keyDigest: c.keyDigest,
    });
  });
  it.each(intents.filter((c) => !c.valid))("$name", (c) => {
    expect(() => intentDigests(c.intent, c.idempotencyKey)).toThrow("invalid_argument");
  });
});
describe("operation transitions", () => {
  it.each(states.filter((c) => !c.error))("$name", (c) => {
    expect(
      nextOperationState(c.state, c.event, c.dispatchCount, c.generation, c.expectedGeneration),
    ).toBe(c.expected);
  });
  it.each(states.filter((c) => c.error))("$name", (c) => {
    expect(() =>
      nextOperationState(c.state, c.event, c.dispatchCount, c.generation, c.expectedGeneration),
    ).toThrow(c.error);
  });
});

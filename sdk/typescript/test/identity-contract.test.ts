import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { encodeIdentity } from "../src/internal/identity-encoding.js";

type Vector = {
  name: string;
  issuer: string;
  subject: string;
  expected?: string;
  error?: string;
};
const vectors: Vector[] = JSON.parse(
  readFileSync(new URL("../../../contracts/fixtures/identity/v1.json", import.meta.url), "utf8"),
);

describe("shared identity encoding v1", () => {
  it("contains shared vectors", () => expect(vectors.length).toBeGreaterThan(0));
  it.each(vectors.filter((v) => !v.error))("encodes $name", (vector) => {
    expect(encodeIdentity(vector.issuer, vector.subject)).toBe(vector.expected);
  });
  it.each(vectors.filter((v) => v.error))("rejects $name", (vector) => {
    expect(() => encodeIdentity(vector.issuer, vector.subject)).toThrow(vector.error);
  });
  it.each(["\ud800", "\udfff", "prefix\ud800suffix"])("rejects malformed Unicode %j", (value) => {
    expect(() => encodeIdentity("https://id.example.com", value)).toThrow("invalid_argument");
    expect(() => encodeIdentity(value, "maya")).toThrow("invalid_argument");
  });
});

import { readFileSync } from "node:fs";
import { expect, it } from "vitest";
import { recordKeyFingerprint, validateKeyUsage } from "../src/internal/key-usage.js";
const fixture = JSON.parse(
  readFileSync(
    new URL("../../../contracts/fixtures/record-key-usage.json", import.meta.url),
    "utf8",
  ),
) as {
  vectors: { name: string; keyHex: string; fingerprint: string }[];
  validLimits: number[];
  invalidLimits: number[];
};
it.each(fixture.vectors)("identifies key material: $name", ({ keyHex, fingerprint }) => {
  expect(recordKeyFingerprint(Buffer.from(keyHex, "hex"))).toBe(fingerprint);
  for (const limit of fixture.validLimits)
    expect(() => validateKeyUsage(fingerprint, limit)).not.toThrow();
  for (const limit of [...fixture.invalidLimits, NaN, Infinity, 1.5])
    expect(() => validateKeyUsage(fingerprint, limit)).toThrow("invalid_argument");
});
it("rejects malformed material and noncanonical fingerprints", () => {
  for (const length of [0, 31, 33])
    expect(() => recordKeyFingerprint(new Uint8Array(length))).toThrow("invalid_argument");
  for (const value of ["", "A".repeat(64), "0".repeat(63), "0".repeat(64) + "\n"])
    expect(() => validateKeyUsage(value, 1)).toThrow("invalid_argument");
});

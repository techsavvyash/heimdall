import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { validateEndpoint, validateConfigEndpoints } from "../src/internal/endpoints.js";
const cases = JSON.parse(
  readFileSync(new URL("../../../contracts/fixtures/endpoints-v1.json", import.meta.url), "utf8"),
) as {
  name: string;
  url: string;
  profile: Parameters<typeof validateEndpoint>[1];
  valid: boolean;
}[];
describe("endpoint configuration", () => {
  it.each(cases.filter((c) => c.valid))("accepts $name", (c) =>
    expect(() => validateEndpoint(c.url, c.profile)).not.toThrow(),
  );
  it.each(cases.filter((c) => !c.valid))("rejects $name", (c) =>
    expect(() => validateEndpoint(c.url, c.profile)).toThrow("invalid_argument"),
  );
  it("defaults to production and validates both endpoints", () => {
    expect(() =>
      validateConfigEndpoints({
        issuer: "https://id.example",
        openfga: { url: "http://localhost" },
      }),
    ).toThrow("invalid_argument");
    expect(() =>
      validateConfigEndpoints({
        issuer: "http://localhost",
        openfga: { url: "https://fga.example" },
      }),
    ).toThrow("invalid_argument");
    expect(() =>
      validateConfigEndpoints({
        issuer: "http://localhost",
        openfga: { url: "http://127.0.0.1:8081" },
        transportProfile: "loopback",
      }),
    ).not.toThrow();
  });
});

import { readFileSync } from "node:fs";
import { afterEach, expect, test, vi } from "vitest";
import { parseDeployment } from "../src/internal/configuration.js";
import { TokenVerifier } from "../src/internal/token-verifier.js";
const read = (name: string) =>
  JSON.parse(readFileSync(new URL("../../../contracts/fixtures/" + name, import.meta.url), "utf8"));
const f = read("audience-boundaries.json");
const base = read("configuration-v1.json");
type Case = {
  name: string;
  character: string;
  count: number;
  valid: boolean;
  token: string | null;
};
const cases = f.cases as Case[];
const config = (audience: string) => ({
  issuer: f.issuer,
  audience,
  jwksUri: f.issuer + "/keys",
  profile: "production" as const,
});
afterEach(() => vi.unstubAllGlobals());
for (const c of cases.filter((c) => c.valid))
  test(`audience config and signature ${c.name}`, async () => {
    const audience = c.character.repeat(c.count);
    const deployment = parseDeployment(
      JSON.stringify({ ...base.config, issuer: f.issuer, audience }),
      JSON.stringify(base.manifest),
    );
    const fetcher = vi.fn<typeof fetch>(async () => Response.json(f.jwks));
    vi.stubGlobal("fetch", fetcher);
    const verifier = new TokenVerifier(config(deployment.config.audience), () => f.now);
    const principal = await verifier.verify(c.token!);
    expect(principal.subject).toBe("audience-test");
    expect(verifier.owns(principal)).toBe(true);
    await expect(
      new TokenVerifier(config("another-product"), () => f.now).verify(c.token!),
    ).rejects.toThrow("unauthenticated");
  });
for (const c of cases.filter((c) => !c.valid))
  test(`audience rejects ${c.name} before network`, () => {
    const audience = c.character.repeat(c.count);
    const fetcher = vi.fn<typeof fetch>();
    vi.stubGlobal("fetch", fetcher);
    expect(() =>
      parseDeployment(JSON.stringify({ ...base.config, audience }), JSON.stringify(base.manifest)),
    ).toThrow("invalid_argument");
    expect(() => new TokenVerifier(config(audience))).toThrow("invalid_argument");
    expect(fetcher).not.toHaveBeenCalled();
  });
test("audience rejects unpaired Unicode without repairing identity", () => {
  expect(() => new TokenVerifier(config("\ud800"))).toThrow("invalid_argument");
});

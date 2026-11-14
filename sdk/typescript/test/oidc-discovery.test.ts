import { readFileSync } from "node:fs";
import { createServer } from "node:http";
import { afterEach, expect, test, vi } from "vitest";
import { discoverOIDC, validateOIDCMetadata } from "../src/internal/oidc-discovery.js";
const f = JSON.parse(
  readFileSync(new URL("../../../contracts/fixtures/oidc-discovery.json", import.meta.url), "utf8"),
);
afterEach(() => vi.unstubAllGlobals());
for (const c of f.cases.filter((c: { valid: boolean }) => c.valid))
  test(`discovery ${c.name}`, () => {
    const value = validateOIDCMetadata({ ...f.metadata, ...c.patch }, f.issuer, "production");
    expect(value.issuer).toBe(f.issuer);
    expect(Object.isFrozen(value)).toBe(true);
    expect(Object.keys(value)).toHaveLength(6);
  });
for (const c of f.cases.filter((c: { valid: boolean }) => !c.valid))
  test(`discovery ${c.name}`, () => {
    expect(() =>
      validateOIDCMetadata({ ...f.metadata, ...c.patch }, f.issuer, "production"),
    ).toThrow("unavailable");
  });
test("discovery fetch is issuer pinned and bounded", async () => {
  const fetcher = vi.fn<typeof fetch>(async () => Response.json(f.metadata));
  vi.stubGlobal("fetch", fetcher);
  expect((await discoverOIDC(f.issuer, "production")).jwks_uri).toBe(f.metadata.jwks_uri);
  expect(fetcher.mock.calls[0]).toEqual([
    f.issuer + "/.well-known/openid-configuration",
    expect.objectContaining({ redirect: "error" }),
  ]);
  for (const raw of ["{", " ".repeat(65537), '{"issuer":"\\ud800"}']) {
    vi.stubGlobal("fetch", async () => new Response(raw));
    await expect(discoverOIDC(f.issuer, "production")).rejects.toThrow("unavailable");
  }
  const cancelled = new AbortController();
  cancelled.abort();
  vi.stubGlobal("fetch", fetcher);
  fetcher.mockClear();
  await expect(discoverOIDC(f.issuer, "production", cancelled.signal)).rejects.toThrow("cancelled");
  await expect(discoverOIDC("http://identity.example.test", "production")).rejects.toThrow(
    "invalid_argument",
  );
  expect(fetcher).not.toHaveBeenCalled();
});
test("discovery refuses redirects and honors caller deadline over a real transport", async () => {
  let redirect = true;
  let requests = 0;
  const server = createServer((req, res) => {
    requests++;
    if (redirect) {
      res.writeHead(302, { location: "/other" });
      res.end();
    } else {
      req.on("close", () => res.destroy());
    }
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  const addr = server.address() as { port: number };
  const issuer = `http://127.0.0.1:${addr.port}`;
  try {
    await expect(discoverOIDC(issuer, "loopback")).rejects.toThrow("unavailable");
    expect(requests).toBe(1);
    redirect = false;
    await expect(discoverOIDC(issuer, "loopback", AbortSignal.timeout(30))).rejects.toThrow(
      "deadline_exceeded",
    );
  } finally {
    server.closeAllConnections();
    await new Promise<void>((resolve) => server.close(() => resolve()));
  }
});
test.skipIf(!process.env.HEIMDALL_TEST_DISCOVERY_ISSUER)("live pinned discovery", async () => {
  const issuer = process.env.HEIMDALL_TEST_DISCOVERY_ISSUER!;
  const metadata = await discoverOIDC(issuer, "production");
  expect(metadata.issuer).toBe(issuer);
  expect(metadata.jwks_uri).toBe(issuer + "/oauth/v2/keys");
});

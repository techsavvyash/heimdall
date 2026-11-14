import { createServer } from "node:http";
import { readFileSync } from "node:fs";
import { afterEach, expect, test, vi } from "vitest";
import { TokenVerifier } from "../src/internal/token-verifier.js";
const f = JSON.parse(
  readFileSync(
    new URL("../../../contracts/fixtures/token-verification.json", import.meta.url),
    "utf8",
  ),
);
const config = {
  issuer: f.issuer,
  audience: f.audience,
  jwksUri: f.issuer + "/keys",
  profile: "production" as const,
};
const token = (name: string) => f.vectors.find((v: { name: string }) => v.name === name).token;
afterEach(() => vi.unstubAllGlobals());
for (const vector of f.vectors.filter((v: { valid: boolean }) => v.valid))
  test(`signature vector ${vector.name}`, async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => Response.json(f.jwks)),
    );
    const verifier = new TokenVerifier(config, () => f.now);
    const result = await verifier.verify(vector.token);
    expect(result).toEqual({ issuer: f.issuer, subject: f.subject, expiresAt: f.now + 3600000 });
    expect(verifier.owns(result)).toBe(true);
    expect(verifier.owns({ ...result })).toBe(false);
    expect(new TokenVerifier(config).owns(result)).toBe(false);
    expect(Object.isFrozen(result)).toBe(true);
  });
for (const vector of f.vectors.filter((v: { valid: boolean }) => !v.valid))
  test(`signature vector ${vector.name}`, async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => Response.json(f.jwks)),
    );
    await expect(new TokenVerifier(config, () => f.now).verify(vector.token)).rejects.toThrow(
      "unauthenticated",
    );
  });
test("bounded rotation, failure cooldown, no stale keys", async () => {
  let now = f.now;
  let keys = { keys: [f.jwks.keys[0]] };
  let fail = false;
  const fetcher = vi.fn<() => Promise<Response>>(async () => {
    if (fail) throw new Error("secret");
    return Response.json(keys);
  });
  vi.stubGlobal("fetch", fetcher);
  const verifier = new TokenVerifier(config, () => now);
  await Promise.all(Array.from({ length: 8 }, () => verifier.verify(token("valid"))));
  expect(fetcher).toHaveBeenCalledTimes(1);
  await expect(verifier.verify(token("rotated"))).rejects.toThrow("unauthenticated");
  expect(fetcher).toHaveBeenCalledTimes(1);
  keys = f.jwks;
  now += 30000;
  await verifier.verify(token("rotated"));
  expect(fetcher).toHaveBeenCalledTimes(2);
  fail = true;
  now += 30000;
  await expect(verifier.verify(token("unknown-key"))).rejects.toThrow("unavailable");
  await verifier.verify(token("valid"));
  expect(fetcher).toHaveBeenCalledTimes(3);
  await expect(verifier.verify(token("unknown-key"))).rejects.toThrow("unavailable");
  expect(fetcher).toHaveBeenCalledTimes(3);
  now += 300000;
  await expect(verifier.verify(token("valid"))).rejects.toThrow("unavailable");
  expect(fetcher).toHaveBeenCalledTimes(4);
  fail = false;
  now += 30000;
  await verifier.verify(token("valid"));
  expect(fetcher).toHaveBeenCalledTimes(5);
});
test("caller cancellation does not cancel other key waiters", async () => {
  let release!: () => void;
  const blocked = new Promise<void>((r) => {
    release = r;
  });
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => {
      await blocked;
      return Response.json(f.jwks);
    }),
  );
  const verifier = new TokenVerifier(config, () => f.now);
  const abort = new AbortController();
  const cancelled = verifier.verify(token("valid"), abort.signal);
  const other = verifier.verify(token("valid"));
  abort.abort("secret");
  await expect(cancelled).rejects.toThrow("cancelled");
  release();
  await other;
  const already = new AbortController();
  already.abort();
  await expect(verifier.verify(token("valid"), already.signal)).rejects.toThrow("cancelled");
});
test("bounded caller deadline", async () => {
  let release!: () => void;
  const blocked = new Promise<void>((r) => {
    release = r;
  });
  vi.stubGlobal("fetch", async () => {
    await blocked;
    return Response.json(f.jwks);
  });
  const verifier = new TokenVerifier(config, () => f.now);
  try {
    await expect(verifier.verify(token("valid"), AbortSignal.timeout(15))).rejects.toThrow(
      "deadline_exceeded",
    );
  } finally {
    release();
  }
});
for (const [name, jwks] of Object.entries({
  empty: { keys: [] },
  duplicate: { keys: [f.jwks.keys[0], f.jwks.keys[0]] },
  private: { keys: [{ ...f.jwks.keys[0], d: "secret" }] },
  wrongAlgorithm: { keys: [{ ...f.jwks.keys[0], alg: "HS256" }] },
  tooLarge: { keys: Array(17).fill(f.jwks.keys[0]) },
}))
  test(`reject invalid keyset ${name}`, async () => {
    vi.stubGlobal("fetch", async () => Response.json(jwks));
    await expect(new TokenVerifier(config, () => f.now).verify(token("valid"))).rejects.toThrow(
      "unavailable",
    );
  });
test("bound response size and status", async () => {
  for (const response of [
    new Response("x".repeat(262145)),
    new Response("redirect", { status: 302, headers: { location: "https://attacker.example" } }),
    new Response("down", { status: 503 }),
  ]) {
    vi.stubGlobal("fetch", async (_url: unknown, options: RequestInit) => {
      expect(options.redirect).toBe("error");
      return response;
    });
    await expect(new TokenVerifier(config, () => f.now).verify(token("valid"))).rejects.toThrow(
      "unavailable",
    );
  }
});
test("trusted config snapshots and rejects cross-origin JWKS", async () => {
  expect(() => new TokenVerifier({ ...config, jwksUri: "https://attacker.example/keys" })).toThrow(
    "invalid_argument",
  );
  const mutable = { ...config };
  const verifier = new TokenVerifier(mutable, () => f.now);
  mutable.audience = "support";
  mutable.jwksUri = "https://attacker.example/keys";
  vi.stubGlobal("fetch", async (url: string) => {
    expect(url).toBe(config.jwksUri);
    return Response.json(f.jwks);
  });
  await verifier.verify(token("valid"));
});

test("backward clock invalidates cached keys", async () => {
  let now = f.now;
  vi.stubGlobal("fetch", async () => Response.json(f.jwks));
  const verifier = new TokenVerifier(config, () => now);
  await verifier.verify(token("valid"));
  now -= 1;
  await expect(verifier.verify(token("valid"))).rejects.toThrow("unavailable");
});

test("HTTP redirects never reach another endpoint", async () => {
  let redirected = 0;
  const server = createServer((request, response) => {
    if (request.url === "/keys") {
      response.writeHead(302, { location: "/untrusted" });
      response.end();
    } else {
      redirected++;
      response.end(JSON.stringify(f.jwks));
    }
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  const address = server.address();
  if (!address || typeof address === "string") throw new Error("test server");
  const issuer = `http://127.0.0.1:${address.port}`;
  try {
    await expect(
      new TokenVerifier(
        { issuer, audience: f.audience, jwksUri: issuer + "/keys", profile: "loopback" },
        () => f.now,
      ).verify(token("valid")),
    ).rejects.toThrow("unavailable");
    expect(redirected).toBe(0);
  } finally {
    await new Promise<void>((resolve, reject) =>
      server.close((error) => (error ? reject(error) : resolve())),
    );
  }
});

test("network retrieval has a five-second bound", async () => {
  const server = createServer(() => {});
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  const address = server.address();
  if (!address || typeof address === "string") throw new Error("test server");
  const issuer = `http://127.0.0.1:${address.port}`;
  try {
    await expect(
      new TokenVerifier(
        { issuer, audience: f.audience, jwksUri: issuer + "/keys", profile: "loopback" },
        () => f.now,
      ).verify(token("valid")),
    ).rejects.toThrow("deadline_exceeded");
  } finally {
    server.closeAllConnections();
    await new Promise<void>((resolve, reject) =>
      server.close((error) => (error ? reject(error) : resolve())),
    );
  }
}, 10000);

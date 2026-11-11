import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { afterEach, expect, test, vi } from "vitest";
import { IdentityVerifier } from "../src/internal/identity-verifier.js";
import { HeimdallError } from "../src/internal/errors.js";
const read = (name: string) =>
  JSON.parse(readFileSync(new URL("../../../contracts/fixtures/" + name, import.meta.url), "utf8"));
const f = read("token-verification.json");
const cases = read("identity-status.json");
const config = {
  issuer: f.issuer,
  audience: f.audience,
  jwksUri: f.issuer + "/keys",
  profile: "production" as const,
  productId: "ledger",
  environment: "production",
  policyVersion: "p1",
  clientId: "client",
};
const raw = f.vectors.find((v: { name: string }) => v.name === "valid").token;
afterEach(() => vi.unstubAllGlobals());
function setup(c = cases[0], freshnessMs = 60000) {
  let now = f.now;
  let blocked = false;
  let failure = false;
  let introspections = 0;
  const negatives: unknown[] = [];
  const gate = {
    check: async () => blocked,
    invalidate: async (n: unknown) => {
      negatives.push(n);
      if (failure) throw new HeimdallError("unavailable");
    },
  };
  const fetcher = vi.fn<typeof fetch>(async (input, options) => {
    expect(options?.redirect).toBe("error");
    const url = String(input);
    if (url.endsWith("/keys")) return Response.json(f.jwks);
    if (url.endsWith("/introspect")) {
      introspections++;
      assert.equal(
        new Headers(options?.headers).get("authorization"),
        "Basic " + Buffer.from("client:s%3Ae%2Bcret").toString("base64"),
      );
      return Response.json(c.introspection, { status: c.introspectionStatus });
    }
    expect(url).toBe(f.issuer + "/v2/users/" + encodeURIComponent(f.subject));
    expect(new Headers(options?.headers).get("authorization")).toBe("Bearer " + raw);
    return Response.json(c.user, { status: c.userStatus });
  });
  vi.stubGlobal("fetch", fetcher);
  return {
    verifier: new IdentityVerifier(
      { ...config, freshnessMs },
      async () => "s:e+cret",
      gate,
      () => now,
    ),
    setNow: (n: number) => {
      now = n;
    },
    block: () => {
      blocked = true;
    },
    fail: () => {
      failure = true;
    },
    calls: () => introspections,
    negatives,
    fetcher,
  };
}
for (const c of cases.filter((c: { expected: string }) => c.expected === "active"))
  test(`${c.name}`, async () => {
    const s = setup(c);
    const p = await s.verifier.verify(raw);
    expect(p.kind).toBe("human");
    expect(p.subject).toBe(f.subject);
    expect(p.identityValidatedAt).toBe(f.now);
    expect(s.verifier.owns(p)).toBe(true);
    expect(s.verifier.owns({ ...p })).toBe(false);
    expect(Object.isFrozen(p)).toBe(true);
  });
for (const c of cases.filter((c: { expected: string }) => c.expected !== "active"))
  test(`${c.name}`, async () => {
    const s = setup(c);
    await expect(s.verifier.verify(raw, "session:1")).rejects.toThrow(c.expected);
    expect(s.negatives.map((n) => (n as { reason: string }).reason)).toEqual(
      c.negativeReason ? [c.negativeReason] : [],
    );
  });
test("exact freshness deadline, context isolation and no stale fallback", async () => {
  const c = structuredClone(cases[0]);
  const s = setup(c);
  await s.verifier.verify(raw, "a:1");
  s.setNow(f.now + 59999);
  await s.verifier.verify(raw, "a:1");
  expect(s.calls()).toBe(1);
  await s.verifier.verify(raw, "b:1");
  expect(s.calls()).toBe(2);
  c.introspectionStatus = 503;
  s.setNow(f.now + 60000);
  await expect(s.verifier.verify(raw, "a:1")).rejects.toThrow("unavailable");
  expect(s.calls()).toBe(3);
  await s.verifier.verify(raw, "b:1");
  expect(s.calls()).toBe(3);
});
test("zero freshness performs every online check and local gate blocks warm cache", async () => {
  const s = setup(cases[0], 0);
  await s.verifier.verify(raw);
  await s.verifier.verify(raw);
  expect(s.calls()).toBe(2);
  s.block();
  await expect(s.verifier.verify(raw)).rejects.toThrow("unauthenticated");
  expect(s.calls()).toBe(2);
});
test("negative observation clears all contexts of the token", async () => {
  const c = structuredClone(cases[0]);
  const s = setup(c);
  await s.verifier.verify(raw, "a");
  await s.verifier.verify(raw, "b");
  c.introspection = { active: false };
  await expect(s.verifier.verify(raw, "c")).rejects.toThrow("unauthenticated");
  await expect(s.verifier.verify(raw, "a")).rejects.toThrow("unauthenticated");
  expect(s.calls()).toBe(4);
});
test("denial persistence failure cannot return a principal", async () => {
  const s = setup(cases.find((c: { name: string }) => c.name === "inactive-token"));
  s.fail();
  await expect(s.verifier.verify(raw)).rejects.toThrow("unavailable");
  expect(s.negatives).toHaveLength(1);
});
test("credential loader deadline and redaction", async () => {
  const s = setup();
  const verifier = new IdentityVerifier(
    config,
    async () => new Promise(() => {}),
    { check: async () => false, invalidate: async () => {} },
    () => f.now,
  );
  await expect(verifier.verify(raw, "bearer", AbortSignal.timeout(20))).rejects.toThrow(
    "deadline_exceeded",
  );
  expect(s.calls()).toBe(0);
  const bad = new IdentityVerifier(
    config,
    async () => {
      throw new Error("secret-value");
    },
    { check: async () => false, invalidate: async () => {} },
    () => f.now,
  );
  await expect(bad.verify(raw)).rejects.toThrow("unavailable");
});
test("local gate rejects non-boolean results", async () => {
  setup();
  const verifier = new IdentityVerifier(
    config,
    async () => "secret",
    { check: async () => undefined as unknown as boolean, invalidate: async () => {} },
    () => f.now,
  );
  await expect(verifier.verify(raw)).rejects.toThrow("unavailable");
});

test("an older positive response cannot survive a newer token denial", async () => {
  const c = cases[0];
  let calls = 0;
  let release!: () => void;
  let started!: () => void;
  const held = new Promise<void>((r) => {
    release = r;
  });
  const entered = new Promise<void>((r) => {
    started = r;
  });
  const negatives: string[] = [];
  vi.stubGlobal("fetch", async (input: unknown) => {
    const url = String(input);
    if (url.endsWith("/keys")) return Response.json(f.jwks);
    if (url.endsWith("/introspect")) {
      calls++;
      if (calls === 1) {
        started();
        await held;
        return Response.json(c.introspection);
      }
      return Response.json({ active: false });
    }
    return Response.json(c.user);
  });
  const verifier = new IdentityVerifier(
    config,
    async () => "secret",
    {
      check: async () => false,
      invalidate: async (n) => {
        negatives.push(n.context);
      },
    },
    () => f.now,
  );
  const old = verifier.verify(raw, "old");
  await entered;
  await expect(verifier.verify(raw, "new")).rejects.toThrow("unauthenticated");
  release();
  await expect(old).rejects.toThrow("unauthenticated");
  await expect(verifier.verify(raw, "old")).rejects.toThrow("unauthenticated");
  expect(calls).toBe(3);
  expect(negatives).toEqual(["new", "old", "old"]);
});

test("freshness is capped by authoritative token expiry", async () => {
  const c = structuredClone(cases[0]);
  c.introspection.exp = f.now / 1000 + 1;
  const s = setup(c);
  await s.verifier.verify(raw);
  s.setNow(f.now + 1000);
  c.introspectionStatus = 503;
  await expect(s.verifier.verify(raw)).rejects.toThrow("unavailable");
  expect(s.calls()).toBe(2);
});

test("positive status cache has bounded capacity", async () => {
  const s = setup();
  for (let i = 0; i < 1025; i++) await s.verifier.verify(raw, `request:${i}`);
  expect(s.calls()).toBe(1025);
  await s.verifier.verify(raw, "request:0");
  expect(s.calls()).toBe(1026);
});
test("backward clock does not reuse positive identity status", async () => {
  const s = setup();
  await s.verifier.verify(raw);
  s.setNow(f.now - 1);
  await expect(s.verifier.verify(raw)).rejects.toThrow("unavailable");
});
test("invalidation that outlives cancellation has an uncertain outcome", async () => {
  setup(cases.find((c: { name: string }) => c.name === "inactive-token"));
  let release!: () => void;
  let entered!: () => void;
  const started = new Promise<void>((r) => {
    entered = r;
  });
  const held = new Promise<void>((r) => {
    release = r;
  });
  const verifier = new IdentityVerifier(
    config,
    async () => "s:e+cret",
    {
      check: async () => false,
      invalidate: async () => {
        entered();
        await held;
      },
    },
    () => f.now,
  );
  const abort = new AbortController();
  const result = verifier.verify(raw, "session:1", abort.signal);
  await started;
  abort.abort();
  await expect(result).rejects.toThrow("outcome_unknown");
  release();
});

test("revoking one token does not invalidate another token for the same human", async () => {
  const other = f.vectors.find((v: { name: string }) => v.name === "rotated").token;
  let inactive = false;
  let calls = 0;
  const reasons: string[] = [];
  vi.stubGlobal("fetch", async (input: unknown, options: RequestInit) => {
    if (String(input).endsWith("/keys")) return Response.json(f.jwks);
    if (String(input).endsWith("/introspect")) {
      calls++;
      const requested = new URLSearchParams(options.body as URLSearchParams).get("token");
      return Response.json(
        inactive && requested === raw ? { active: false } : cases[0].introspection,
      );
    }
    return Response.json(cases[0].user);
  });
  const verifier = new IdentityVerifier(
    config,
    async () => "secret",
    {
      check: async () => false,
      invalidate: async (n) => {
        reasons.push(n.reason);
      },
    },
    () => f.now,
  );
  await verifier.verify(raw, "a");
  await verifier.verify(other, "b");
  inactive = true;
  await expect(verifier.verify(raw, "c")).rejects.toThrow("unauthenticated");
  expect((await verifier.verify(other, "b")).kind).toBe("human");
  expect(calls).toBe(3);
  expect(reasons).toEqual(["token_inactive"]);
});

test("malformed provider Unicode is not repaired", async () => {
  setup();
  vi.stubGlobal("fetch", async (input: unknown) => {
    if (String(input).endsWith("/keys")) return Response.json(f.jwks);
    if (String(input).endsWith("/introspect")) return Response.json(cases[0].introspection);
    return new Response(
      JSON.stringify(cases[0].user).replace('"human":{}', '"human":{},"username":"\\ud800"'),
    );
  });
  const verifier = new IdentityVerifier(
    config,
    async () => "secret",
    { check: async () => false, invalidate: async () => {} },
    () => f.now,
  );
  await expect(verifier.verify(raw)).rejects.toThrow("unavailable");
});

test("cancellation at the warm-cache boundary never releases a principal", async () => {
  setup();
  const controller = new AbortController();
  let armed = false;
  let afterGate = false;
  const verifier = new IdentityVerifier(
    config,
    async () => "s:e+cret",
    {
      check: async () => {
        if (armed) afterGate = true;
        return false;
      },
      invalidate: async () => {},
    },
    () => {
      if (afterGate) controller.abort();
      return f.now;
    },
  );
  await verifier.verify(raw);
  armed = true;
  await expect(verifier.verify(raw, "bearer", controller.signal)).rejects.toThrow("cancelled");
});

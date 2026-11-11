import { readFileSync } from "node:fs";
import { afterEach, expect, test, vi } from "vitest";
import { AuditedAuthentication } from "../src/internal/audited-authentication.js";
import { auditDigest } from "../src/internal/audit-receipts.js";
const read = (name: string) =>
  JSON.parse(readFileSync(new URL("../../../contracts/" + name, import.meta.url), "utf8"));
const f = read("fixtures/token-verification.json");
const cases = read("fixtures/identity-status.json") as {
  name: string;
  expected: string;
  introspection: unknown;
  introspectionStatus: number;
  user: unknown;
  userStatus: number;
}[];
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
const receipt = (event: Record<string, unknown>) => ({
  productId: event.productId as string,
  environment: event.environment as string,
  eventId: event.eventId as string,
  recordedAt: event.recordedAt as number,
  payloadDigest: auditDigest(event),
});
afterEach(() => vi.unstubAllGlobals());
function provider(c = cases[0]!) {
  vi.stubGlobal(
    "fetch",
    vi.fn<typeof fetch>(async (input) =>
      String(input).endsWith("/keys")
        ? Response.json(f.jwks)
        : String(input).endsWith("/introspect")
          ? Response.json(c.introspection, { status: c.introspectionStatus })
          : Response.json(c.user, { status: c.userStatus }),
    ),
  );
}
function setup(append: ConstructorParameters<typeof AuditedAuthentication>[3]) {
  return new AuditedAuthentication(
    config,
    async () => "secret",
    { check: async () => false, invalidate: async () => {} },
    append,
    () => f.now,
  );
}
test.each(cases)("audits identity $name", async (c) => {
  provider(c);
  const events: Record<string, unknown>[] = [];
  const runner = setup(async (e) => {
    events.push(e);
    return receipt(e);
  });
  const result = await runner.verify(raw, "bearer").then(
    (p) => ({ status: "active", owned: runner.owns(p) }),
    (e) => ({ status: e.code, owned: false }),
  );
  expect(result).toEqual({ status: c.expected, owned: c.expected === "active" });
  expect(events).toHaveLength(1);
  expect(events[0]!.outcome).toBe(
    c.expected === "active"
      ? "succeeded"
      : c.expected === "unauthenticated"
        ? "deny"
        : "unavailable",
  );
  expect(events[0]!.actor).toEqual(
    c.expected === "active"
      ? { kind: "user", issuer: f.issuer, subject: f.subject }
      : { kind: "unknown" },
  );
  expect(JSON.stringify(events)).not.toContain(raw);
  expect(runner.owns({})).toBe(false);
});
test.each(["failure", "productId", "environment", "eventId", "recordedAt", "payloadDigest"])(
  "blocks principal for audit %s",
  async (field) => {
    provider();
    const runner = setup(async (e) => {
      if (field === "failure") throw new Error("secret");
      return { ...receipt(e), [field]: "wrong" };
    });
    await expect(runner.verify(raw, "bearer")).rejects.toThrow("audit_unavailable");
  },
);
test("withholds principal until commit and audits every cached invocation", async () => {
  provider();
  let release!: () => void;
  const wait = new Promise<void>((r) => {
    release = r;
  });
  let entered!: () => void;
  const inside = new Promise<void>((r) => {
    entered = r;
  });
  let returned = false;
  const events: Record<string, unknown>[] = [];
  const runner = setup(async (e) => {
    events.push(e);
    entered();
    await wait;
    return receipt(e);
  });
  const pending = runner.verify(raw, "bearer").then((p) => {
    returned = true;
    return p;
  });
  await inside;
  expect(returned).toBe(false);
  release();
  await pending;
  await runner.verify(raw, "bearer");
  expect(events).toHaveLength(2);
  expect(events[0]!.eventId).not.toBe(events[1]!.eventId);
  expect(events[0]!.requestId).not.toBe(events[1]!.requestId);
});
test("records precancelled attempts and cancellation during commit never returns a principal", async () => {
  provider();
  const abort = new AbortController();
  abort.abort();
  const events: Record<string, unknown>[] = [];
  const runner = setup(async (e) => {
    events.push(e);
    return receipt(e);
  });
  await expect(runner.verify(raw, "bearer", abort.signal)).rejects.toThrow("cancelled");
  expect(events[0]!.outcome).toBe("cancelled");
  expect(events[0]!.actor).toEqual({ kind: "unknown" });
  const later = new AbortController();
  const second = setup(async (e) => {
    later.abort();
    return receipt(e);
  });
  await expect(second.verify(raw, "bearer", later.signal)).rejects.toThrow("cancelled");
});
test.each(
  read("fixtures/authentication-release.json") as {
    name: string;
    advanceMs: number;
    error: string;
  }[],
)("release guard $name", async (c) => {
  provider();
  let now = f.now;
  const events: Record<string, unknown>[] = [];
  const runner = new AuditedAuthentication(
    config,
    async () => "secret",
    { check: async () => false, invalidate: async () => {} },
    async (e) => {
      events.push(e);
      now += c.advanceMs;
      return receipt(e);
    },
    () => now,
  );
  const result = await runner.verify(raw, "bearer").then(
    () => "",
    (e) => e.code,
  );
  expect(result).toBe(c.error);
  expect(events).toHaveLength(c.error ? 2 : 1);
  expect(events.at(-1)!.reasonCode ?? "").toBe(c.error);
  expect(new Set(events.map((e) => e.requestId)).size).toBe(1);
  expect(new Set(events.map((e) => e.eventId)).size).toBe(events.length);
});

test("failed corrective audit blocks a completed observation", async () => {
  provider();
  const abort = new AbortController();
  let calls = 0;
  const runner = setup(async (e) => {
    if (++calls === 2) throw new Error("secret");
    abort.abort();
    return receipt(e);
  });
  await expect(runner.verify(raw, "bearer", abort.signal)).rejects.toThrow("audit_unavailable");
  expect(calls).toBe(2);
});

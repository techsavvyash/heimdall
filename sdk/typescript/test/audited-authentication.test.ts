import { Pool } from "pg";
import { PostgresAudit } from "../src/internal/postgres/audit.js";
import { validateAuditShape } from "../src/internal/audit-shape.js";
import { HeimdallError } from "../src/internal/errors.js";
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

test.each(
  read("fixtures/audited-maintenance.json") as {
    name: string;
    tenantId: string | null;
    grantedTenant: string | null;
    fault?: string;
    expected: string;
    outcomes: string[];
    policyCalls: number;
  }[],
)("audited maintenance $name", async (c) => {
  const url = process.env.HEIMDALL_TEST_MAINTENANCE_AUDIT_URL;
  const pool = url ? new Pool({ connectionString: url }) : null;
  const durable = pool
    ? PostgresAudit.forRetainedRuntime(pool, "ledger", "production", ["siem"], validateAuditShape)
    : null;
  try {
    provider(cases.find((p) => p.expected === "active")!);
    const events: Record<string, unknown>[] = [];
    let denied = false;
    let now = f.now as number;
    const runner = new AuditedAuthentication(
      config,
      async () => "secret",
      { check: async () => denied, invalidate: async () => {} },
      async (event) => {
        events.push(event);
        if (c.fault === "request" && event.outcome === "requested")
          throw new HeimdallError("unavailable");
        const r = durable ? await durable.append(event) : receipt(event);
        if (c.fault === "terminal" && event.outcome === "allow") r.eventId = "mismatch";
        return r;
      },
      () => now,
    );
    const request = {
      tenantId: c.tenantId,
      action: c.fault === "action" ? "invoice.approve" : "audit.retention.retire",
      targetEventId: "target",
      operationId: "retire-1",
      modelId: "model",
      manifestVersion: 1,
    };
    const policy = vi.fn<Parameters<typeof runner.runMaintenance>[2]>(
      async (scope: Parameters<Parameters<typeof runner.runMaintenance>[2]>[0]) => {
        if (c.fault === "policy") throw new HeimdallError("unavailable");
        if (c.fault === "revoked") denied = true;
        if (c.fault === "stale") now += 60000;
        const allowed = scope.tenantId === c.grantedTenant;
        expect(Reflect.set(scope, "tenantId", "mutated")).toBe(false);
        return allowed;
      },
    );
    const apply = vi.fn<Parameters<typeof runner.runMaintenance>[3]>(
      async (scope: Parameters<Parameters<typeof runner.runMaintenance>[3]>[0]) => {
        expect(events.map((e) => e.outcome)).toEqual(["succeeded", "requested", "allow"]);
        expect(scope).toEqual({
          ...request,
          productId: "ledger",
          environment: "production",
          policyVersion: "p1",
          actor: { issuer: f.issuer, subject: f.subject },
        });
        return receipt(events[2]!);
      },
    );
    const status = await runner
      .runMaintenance(c.fault === "token" ? "invalid" : raw, request, policy, apply)
      .then(
        () => "receipt",
        (e: HeimdallError) => e.code,
      );
    expect(status).toBe(c.expected);
    expect(events.map((e) => e.outcome)).toEqual(c.outcomes);
    expect(policy).toHaveBeenCalledTimes(c.policyCalls);
    expect(apply).toHaveBeenCalledTimes(c.expected === "receipt" ? 1 : 0);
    const decision = events[1];
    expect(
      decision
        ? {
            productId: decision.productId,
            environment: decision.environment,
            tenantId: decision.tenantId,
            operationId: decision.operationId,
            permission: decision.permission,
          }
        : null,
    ).toEqual(
      c.outcomes.length >= 2
        ? {
            productId: "ledger",
            environment: "production",
            tenantId: c.tenantId,
            operationId: request.operationId,
            permission: request.action,
          }
        : null,
    );
  } finally {
    if (pool) await pool.end();
  }
});

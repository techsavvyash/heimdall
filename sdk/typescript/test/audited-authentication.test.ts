import { createOperatorPolicy } from "../src/internal/operator-policy.js";
import { ManagedExport } from "../src/internal/postgres/export.js";
import { MaintenanceCommand } from "../src/internal/maintenance-command.js";
import { validateAuditHold } from "../src/internal/audit-hold.js";
import { validateAuditRetirement } from "../src/internal/audit-retire.js";
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

test("bound maintenance command", async () => {
  provider(cases.find((p) => p.expected === "active")!);
  const url = process.env.HEIMDALL_TEST_MAINTENANCE_COMMAND_URL;
  const pool = url ? new Pool({ connectionString: url }) : null;
  const durable = pool
    ? PostgresAudit.forRetainedRuntime(pool, "ledger", "production", [], validateAuditShape)
    : null;
  const target = "bound-maintenance-ts";
  let calls = 0;
  const check = (event: Record<string, unknown>) => {
    calls++;
    expect(event.actor).toEqual({ kind: "user", issuer: f.issuer, subject: f.subject });
    expect(event.tenantId).toBe(null);
    expect(event.productId).toBe("ledger");
    expect(event.environment).toBe("production");
    expect(event.policyVersion).toBe("p1");
    return receipt(event);
  };
  const fake: ConstructorParameters<typeof MaintenanceCommand>[1] = {
    applyHold: async (r, event) => {
      validateAuditHold(r, event);
      return check(event);
    },
    retire: async (r, event) => {
      validateAuditRetirement(r, event);
      expect(r.retainForMs).toBe(1000);
      return check(event);
    },
  };
  try {
    if (durable)
      await durable.append({
        schemaVersion: 1,
        eventId: target,
        eventType: "heimdall.policy.completed",
        eventVersion: 1,
        occurredAt: f.now - 5000,
        recordedAt: f.now - 5000,
        productId: "ledger",
        environment: "production",
        tenantId: null,
        actor: { kind: "system", id: "test-seed" },
        target: { type: "audit_event", id: target },
        action: "audit.test.seed",
        outcome: "succeeded",
        requestId: target,
        source: "sdk",
        sdkVersion: "0.0.0",
        attributes: {},
        policyVersion: "p1",
      });
    const auth = setup(durable ? durable.append : async (event) => receipt(event));
    const command = new MaintenanceCommand(
      auth,
      durable ?? fake,
      "pinned-model",
      7,
      1000,
      createOperatorPolicy(
        {
          productId: config.productId,
          environment: config.environment,
          policyVersion: config.policyVersion,
          grants: [
            "audit.hold.administrative.acquire",
            "audit.hold.administrative.release",
            "audit.hold.retry.acquire",
            "audit.hold.retry.release",
            "audit.retention.retire",
          ].map((action) => ({
            issuer: f.issuer,
            subject: f.subject,
            tenantId: null,
            action,
          })),
        },
        async (scope) => scope.targetEventId === target && scope.operationId === "hold-retry-ts",
      ),
      () => f.now,
    );
    let queued: Parameters<typeof command.runQueuedRetirement>[1] | undefined;
    let queuedIndex = -1;
    if (durable && pool) {
      const statement = readFileSync(
        new URL("../../../contracts/fixtures/retirement-sweep-save.sql", import.meta.url),
        "utf8",
      );
      const sweep: Parameters<typeof command.queueRetirementPage>[0] = {
        id: "sweep-ts",
        tenantId: null,
        at: f.now,
        after: null,
      };
      do {
        const position = JSON.stringify(sweep.after);
        await command.queueRetirementPage(
          sweep,
          8,
          durable.scanRetirementCandidates,
          async (batch) => {
            const result = await pool.query(statement, [
              sweep.id,
              position,
              JSON.stringify(batch.next),
              JSON.stringify(batch),
            ]);
            return result.rowCount === 1;
          },
        );
        const result = await pool.query<{
          batch: Parameters<typeof command.runQueuedRetirement>[1];
        }>("SELECT batch FROM public.test_retirement_batches WHERE id=$1 AND position=$2::jsonb", [
          sweep.id,
          position,
        ]);
        const batch = result.rows[0]!.batch;
        queuedIndex = batch.operations.findIndex((op) => op.targetEventId === target);
        if (queuedIndex >= 0) queued = batch;
        sweep.after = batch.next;
      } while (!queued && sweep.after);
      if (!queued) throw new Error("target was not queued");
    }
    const shared = read("fixtures/maintenance-command.json") as (Pick<
      Parameters<typeof command.run>[1],
      "tenantId" | "action" | "operationId"
    > & { name: string; expected: string; future?: boolean; retry?: boolean })[];
    let original: ReturnType<typeof receipt> | undefined;
    for (const c of shared) {
      const before = calls;
      const op = {
        tenantId: c.tenantId,
        action: c.action,
        operationId: c.operationId + "-ts",
        targetEventId: target,
        at: f.now + (c.future ? 1 : 0),
      };
      const execution =
        queued && !c.future && c.action === "audit.retention.retire"
          ? command.runQueuedRetirement(raw, queued, queuedIndex)
          : command.run(raw, op);
      const result = await execution.then(
        (value) => ({ status: "receipt", value }),
        (e: HeimdallError) => ({ status: e.code, value: undefined }),
      );
      expect({ name: c.name, status: result.status }).toEqual({ name: c.name, status: c.expected });
      expect(result.status === "receipt" || calls === before).toBe(true);
      expect(!c.retry || JSON.stringify(result.value) === JSON.stringify(original)).toBe(true);
      if (result.value && c.action === "audit.retention.retire") original = result.value;
    }
    const persisted = durable && original ? await durable.inspect(null, original.eventId) : null;
    expect(persisted?.event?.actor ?? null).toEqual(
      durable ? { kind: "user", issuer: f.issuer, subject: f.subject } : null,
    );
    expect(persisted?.event?.policyVersion ?? null).toBe(durable ? "p1" : null);
    expect(durable ? (await durable.inspect(null, target))?.state : calls).toBe(
      durable ? "retired" : 6,
    );
  } finally {
    if (pool) await pool.end();
  }
});

const exportCases = read("fixtures/audited-managed-export.json") as {
  name: string;
  kind: "workspaces" | "operations";
  workspaceId: string | null;
  fault?: string;
  expected: string;
  scans: number;
}[];
test.each(exportCases)("audited managed export $name", async (c) => {
  provider(cases.find((p) => p.expected === "active")!);
  let revoked = false;
  let scans = 0;
  const events: Record<string, unknown>[] = [];
  const runner = new AuditedAuthentication(
    config,
    async () => "secret",
    { check: async () => revoked, invalidate: async () => {} },
    async (event) => {
      events.push(event);
      const result = receipt(event);
      if (
        (c.fault === "receipt" && event.eventType === "heimdall.export.completed") ||
        (c.fault === "decision" && event.outcome === "allow")
      )
        result.eventId = "wrong";
      return result;
    },
    () => f.now,
  );
  const result = await runner
    .runManagedExport(
      raw,
      { kind: c.kind, workspaceId: c.workspaceId, after: null, limit: 1, operationId: "export-1" },
      "model",
      1,
      async (scope) =>
        scope.tenantId === c.workspaceId &&
        scope.action === "managed.export." + c.kind &&
        c.fault !== "deny",
      async (kind, workspaceId) => {
        scans++;
        if (events.at(-1)?.outcome !== "allow") throw new Error("missing allow");
        revoked = c.fault === "revoke";
        return JSON.stringify({
          schemaVersion: 1,
          productId: c.fault === "scope" ? "support" : "ledger",
          environment: "production",
          kind,
          workspaceId,
          items: [],
          nextId: null,
        });
      },
    )
    .then(
      () => "page",
      (error: HeimdallError) => error.code,
    );
  expect({ result, scans }).toEqual({ result: c.expected, scans: c.scans });
  const completion = events.find((event) => event.eventType === "heimdall.export.completed");
  expect(completion?.outcome).toBe(
    c.scans ? (c.fault === "scope" ? "failed" : "succeeded") : undefined,
  );
});

test.skipIf(!process.env.HEIMDALL_TEST_AUDITED_EXPORT_READER)(
  "live audited managed export",
  async () => {
    provider(cases.find((p) => p.expected === "active")!);
    const reader = new Pool({ connectionString: process.env.HEIMDALL_TEST_AUDITED_EXPORT_READER });
    const writer = new Pool({ connectionString: process.env.HEIMDALL_TEST_AUDITED_EXPORT_WRITER });
    try {
      const store = ManagedExport.forRetainedRuntime(reader, "ledger", "production");
      const audit = PostgresAudit.forRetainedRuntime(
        writer,
        "ledger",
        "production",
        [],
        validateAuditShape,
      );
      const page = await setup(audit.append).runManagedExport(
        raw,
        {
          kind: "workspaces",
          workspaceId: "export-probe",
          after: null,
          limit: 1,
          operationId: "export-ts",
        },
        "model",
        1,
        createOperatorPolicy({
          productId: config.productId,
          environment: config.environment,
          policyVersion: config.policyVersion,
          grants: [
            {
              issuer: f.issuer,
              subject: f.subject,
              tenantId: "export-probe",
              action: "managed.export.workspaces",
            },
          ],
        }),
        store.scan.bind(store),
      );
      expect(JSON.parse(page).items).toEqual([
        {
          productId: "ledger",
          environment: "production",
          workspaceId: "export-probe",
          lifecycle: "suspended",
          mutationBlocked: true,
          revision: expect.stringMatching(/^[1-9][0-9]*$/),
          createdAt: 1,
          updatedAt: 2,
        },
      ]);
    } finally {
      await Promise.all([reader.end(), writer.end()]);
    }
  },
);

test("retirement sweep persists stable proposals with the cursor", async () => {
  const f = read("fixtures/retirement-sweep.json");
  const command = new MaintenanceCommand(
    setup(async (e) => receipt(e)),
    {
      applyHold: async () => {
        throw new Error();
      },
      retire: async () => {
        throw new Error();
      },
    },
    "pinned-model",
    7,
    1000,
    async () => false,
    () => f.sweep.at,
  );
  const next = {
    productId: "ledger",
    environment: "production",
    tenantId: null,
    now: f.sweep.at,
    retainForMs: 1000,
    eventId: "cursor",
  };
  const scan = async () => ({ eventIds: f.events, next });
  let stored: Parameters<Parameters<typeof command.queueRetirementPage>[3]>[0] | undefined;
  const save: Parameters<typeof command.queueRetirementPage>[3] = async (batch) => {
    expect(batch.operations).toEqual(f.operations);
    expect(batch.next).toEqual(next);
    if (stored) return false;
    stored = batch;
    throw new HeimdallError("outcome_unknown");
  };
  await expect(command.queueRetirementPage(f.sweep, 8, scan, save)).rejects.toMatchObject({
    code: "outcome_unknown",
  });
  await expect(command.queueRetirementPage(f.sweep, 8, scan, save)).rejects.toMatchObject({
    code: "conflict",
  });
  await expect(
    command.runQueuedRetirement("unused", { ...stored!, policyVersion: "p2" }, 0),
  ).rejects.toMatchObject({ code: "conflict" });
  await command.queueRetirementPage(
    f.sweep,
    8,
    async () => ({ eventIds: [], next }),
    async (batch) => {
      expect(batch.operations).toEqual([]);
      expect(batch.next).toEqual(next);
      return true;
    },
  );
});

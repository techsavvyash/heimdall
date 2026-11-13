import { recoverWorkspaceCreation } from "../src/provisioning/index.js";
import { Pool } from "pg";
import { createManagedPostgresStorage, createPostgresStorage } from "../src/postgres.js";
import { readFileSync } from "node:fs";
import { afterEach, expect, test, vi } from "vitest";
import { createHeimdall, auditEventDigest as auditDigest } from "../src/index.js";
import type { RuntimeOptions } from "../src/index.js";
import { compileTenantModel } from "../src/internal/tenant-model.js";
const read = (name: string) =>
  JSON.parse(readFileSync(new URL("../../../contracts/fixtures/" + name, import.meta.url), "utf8"));
const base = read("configuration-v1.json");
const token = read("token-verification.json");
const status = read("identity-status.json")[0];
const raw = token.vectors.find((v: { name: string }) => v.name === "valid").token;
const cases = read("runtime-startup.json") as { name: string; failure: string; expected: string }[];
afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});
function setup(failure = "", environment = "production") {
  vi.spyOn(Date, "now").mockReturnValue(token.now);
  const config = { ...base.config, environment, issuer: token.issuer, audience: token.audience };
  const artifact = compileTenantModel(base.manifest, config);
  const metadata = JSON.parse(
    JSON.stringify(read("oidc-discovery.json").metadata).replaceAll(
      "https://identity.example.test",
      token.issuer,
    ),
  );
  metadata.jwks_uri = token.issuer + "/keys";
  if (failure === "issuer") metadata.issuer += "/";
  if (failure === "introspection") metadata.introspection_endpoint = token.issuer + "/unexpected";
  const events: Record<string, unknown>[] = [];
  const fga = { writes: 0, fault: "none" };
  const tuples: { user: string; object: string; relation: string }[] = [];
  const order: string[] = [];
  const options: RuntimeOptions = {
    policyVersion: failure === "policy" ? "bad\n" : "p1",
    credentials: {
      clientSecret: async () => "secret",
      openfgaKey: async () => {
        if (failure === "credential") throw new Error("secret");
        return "key";
      },
    },
    storage: {
      productId: failure === "scope" ? "support" : "ledger",
      environment: "production",
      ready: async () => {
        order.push("ready");
        if (failure === "storage") throw new Error("secret");
      },
      identity: { check: async () => false, invalidate: async () => {} },
      appendAudit: async (e) => {
        events.push(e);
        return {
          productId: e.productId as string,
          environment: e.environment as string,
          eventId: e.eventId as string,
          recordedAt: e.recordedAt as number,
          payloadDigest: auditDigest(e),
        };
      },
    },
  };
  if (failure === "missing") Object.assign(options.storage, { ready: undefined });
  vi.stubGlobal(
    "fetch",
    vi.fn<typeof fetch>(async (input, init) => {
      const url = String(input);
      if (url.endsWith("openid-configuration")) {
        order.push("discovery");
        return Response.json(metadata, { status: failure === "discovery" ? 503 : 200 });
      }
      if (url.includes("authorization-models")) {
        order.push("model");
        return Response.json({
          authorization_model: {
            ...(artifact.model as object),
            id: failure === "model" ? "wrong" : config.openfga.modelId,
          },
        });
      }
      if (url.endsWith("/read")) {
        if (fga.fault === "before_dispatch") return Response.json({}, { status: 503 });
        const { tuple_key: key, consistency } = JSON.parse(String(init?.body));
        if (consistency !== "HIGHER_CONSISTENCY") return Response.json({}, { status: 400 });
        return Response.json({
          tuples: tuples
            .filter(
              (tuple) =>
                tuple.object === key.object &&
                tuple.relation === key.relation &&
                (!key.user || tuple.user === key.user),
            )
            .map((key) => ({ key })),
        });
      }
      if (url.endsWith("/write")) {
        fga.writes++;
        if (fga.fault === "lost_before_apply") throw new Error("simulated interrupted write");
        tuples.push(...JSON.parse(String(init?.body)).writes.tuple_keys);
        if (fga.fault === "lost_response") throw new Error("simulated lost response");
        return Response.json({});
      }
      if (url.endsWith("/check")) {
        order.push("check");
        const check = JSON.parse(String(init?.body));
        if (
          check.authorization_model_id !== config.openfga.modelId ||
          check.consistency !== "HIGHER_CONSISTENCY" ||
          check.tuple_key.user !==
            "user:" +
              Buffer.from(
                JSON.stringify([token.issuer, token.subject]).replace(
                  /[\u007f-\uffff]/g,
                  (c) => "\\u" + c.charCodeAt(0).toString(16).padStart(4, "0"),
                ),
              ).toString("base64url") ||
          ![
            "member",
            artifact.permissions["invoice.approve"],
            ...Object.values(artifact.roles).map((r) => r.effective),
          ].includes(check.tuple_key.relation)
        )
          return new Response(null, { status: 400 });
        if (check.tuple_key.object === "workspace:outage")
          return new Response(null, { status: 503 });
        return Response.json({
          allowed:
            (check.tuple_key.object === "workspace:acme" &&
              ["member", "p_0", artifact.roles.approver!.effective].includes(
                check.tuple_key.relation,
              )) ||
            (check.tuple_key.relation === "member" &&
              tuples.some(
                (tuple) => tuple.object === check.tuple_key.object && tuple.relation === "member",
              )),
        });
      }
      if (url.endsWith("/keys")) return Response.json(token.jwks);
      if (url.endsWith("/introspect")) return Response.json(status.introspection);
      return Response.json(status.user);
    }),
  );
  return { config, options, events, order, fga };
}
test.each(cases)("public runtime startup $name", async (c) => {
  const s = setup(c.failure);
  const abort = new AbortController();
  if (c.failure === "cancelled") abort.abort();
  const result = await createHeimdall(
    JSON.stringify(s.config),
    JSON.stringify(base.manifest),
    s.options,
    { signal: abort.signal },
  ).then(
    () => "ready",
    (e) => e.code,
  );
  expect(result).toBe(c.expected);
  expect(s.events).toHaveLength(0);
});
test("public bearer verification requires audit and preserves captured ports", async () => {
  const s = setup();
  const signal = new AbortController().signal;
  const runtime = await createHeimdall(
    JSON.stringify(s.config),
    JSON.stringify(base.manifest),
    s.options,
    { signal },
  );
  expect(s.order).toEqual(["ready", "discovery", "model"]);
  s.options.storage.appendAudit = async () => {
    throw new Error("replacement");
  };
  s.options.credentials.clientSecret = async () => {
    throw new Error("replacement");
  };
  const principal = await runtime.tokens.verify(raw, { signal });
  expect(principal.subject).toBe(token.subject);
  expect(principal.productId).toBe("ledger");
  expect(Object.isFrozen(principal)).toBe(true);
  expect(Object.isFrozen(runtime.tokens)).toBe(true);
  expect(s.events.map((e) => e.outcome)).toEqual(["succeeded"]);
  await expect(runtime.tokens.verify("invalid", { signal })).rejects.toThrow("unauthenticated");
  expect(s.events.map((e) => e.outcome)).toEqual(["succeeded", "deny"]);
});
test("public runtime bounds nonsettling readiness and rejects absent audit", async () => {
  const s = setup();
  s.options.storage.ready = () => new Promise(() => {});
  await expect(
    createHeimdall(JSON.stringify(s.config), JSON.stringify(base.manifest), s.options, {
      signal: AbortSignal.timeout(20),
    }),
  ).rejects.toThrow("deadline_exceeded");
  Object.assign(s.options.storage, { appendAudit: undefined });
  await expect(
    createHeimdall(JSON.stringify(s.config), JSON.stringify(base.manifest), s.options, {
      signal: new AbortController().signal,
    }),
  ).rejects.toThrow("invalid_argument");
});
test("public authentication never releases a principal on failed audit", async () => {
  const s = setup();
  s.options.storage.appendAudit = async () => {
    throw new Error("sensitive store error");
  };
  const signal = new AbortController().signal;
  const runtime = await createHeimdall(
    JSON.stringify(s.config),
    JSON.stringify(base.manifest),
    s.options,
    { signal },
  );
  await expect(runtime.tokens.verify(raw, { signal })).rejects.toThrow("audit_unavailable");
});
test("startup captures ports and policy before readiness yields", async () => {
  const s = setup();
  s.options.storage.ready = async () => {
    s.options.policyVersion = "changed";
    s.options.credentials.clientSecret = async () => {
      throw new Error("replacement");
    };
    s.options.storage.appendAudit = async () => {
      throw new Error("replacement");
    };
  };
  const signal = new AbortController().signal;
  const runtime = await createHeimdall(
    JSON.stringify(s.config),
    JSON.stringify(base.manifest),
    s.options,
    { signal },
  );
  expect((await runtime.tokens.verify(raw, { signal })).policyVersion).toBe("p1");
  expect(s.events).toHaveLength(1);
});

test.each(
  read("postgres-runtime.json") as {
    name: string;
    productId: string;
    environment: string;
    auditSinks: string[];
    valid: boolean;
  }[],
)("PostgreSQL factory $name", async (c) => {
  const pool = new Pool();
  const connect = vi.spyOn(pool, "connect");
  try {
    let result = false;
    try {
      const storage = createPostgresStorage(pool, c);
      result = storage.productId === c.productId && Object.isFrozen(storage);
    } catch (error) {
      if ((error as { code: string }).code !== "invalid_argument") throw error;
    }
    expect(result).toBe(c.valid);
    expect(connect).not.toHaveBeenCalled();
  } finally {
    await pool.end();
  }
});

test.skipIf(!process.env.HEIMDALL_TEST_DATABASE_URL || !!process.env.HEIMDALL_TEST_READ_PEER)(
  "public runtime uses PostgreSQL readiness denial and durable audit",
  async () => {
    const s = setup("", "public-ts");
    const pool = new Pool({
      connectionString: process.env.HEIMDALL_TEST_DATABASE_URL,
      options: "-c role=heimdall_public_runtime",
    });
    const signal = new AbortController().signal;
    const sinks = ["siem"];
    const storage = createPostgresStorage(pool, {
      ...s.config,
      auditSinks: sinks,
      sessions: { keyUsagePool: pool, activeKeyId: "k1", keys: { k1: Buffer.alloc(32, 7) } },
    });
    sinks[0] = "changed";
    s.options.storage = storage;
    try {
      const runtime = await createHeimdall(
        JSON.stringify(s.config),
        JSON.stringify(base.manifest),
        s.options,
        { signal },
      );
      const callbackUrl = base.manifest.clients.web.redirectUris[0];
      const sessionOptions = { callbackUrl, returnTo: "/workspaces", zitadelAPIAudience: "api" };
      await expect(
        runtime.createSessions(
          { ...sessionOptions, callbackUrl: "https://other.example/callback" },
          { signal },
        ),
      ).rejects.toThrow("invalid_argument");
      const sessions = await runtime.createSessions(sessionOptions, { signal });
      const response = await sessions.start(new Request(new URL("/auth/start", callbackUrl)));
      expect(response.status).toBe(303);
      expect(response.headers.get("set-cookie")).toMatch(/^__Host-heimdall-/);
      expect(
        (
          await pool.query(
            "SELECT count(*)::int AS count FROM heimdall.records WHERE environment='public-ts' AND kind='login'",
          )
        ).rows[0].count,
      ).toBe(1);
      const actor = await runtime.tokens.verify(raw, { signal });
      expect(await runtime.tenant("acme").can(actor, "invoice.approve", { signal })).toBe(true);
      await expect(
        runtime.tenant("globex").require(actor, "invoice.approve", { signal }),
      ).rejects.toThrow("forbidden");
      await expect(
        runtime.tenant("outage").can(actor, "invoice.approve", { signal }),
      ).rejects.toThrow("unavailable");
      await expect(
        runtime.tenant("acme").can({ ...actor }, "invoice.approve", { signal }),
      ).rejects.toThrow("unauthenticated");
      await expect(runtime.tokens.verify("invalid", { signal })).rejects.toThrow("unauthenticated");
      expect(await runtime.tenant("acme").roles(actor, { signal })).toEqual(["approver"]);
      expect(await runtime.tenant("globex").roles(actor, { signal })).toEqual([]);
      await expect(runtime.tenant("outage").roles(actor, { signal })).rejects.toThrow(
        "unavailable",
      );
      const roleAudit = await pool.query(
        "SELECT event FROM heimdall.audit_events WHERE environment='public-ts'",
      );
      const eventsForRoles = roleAudit.rows.map(({ event }) =>
        JSON.parse(Buffer.from(event.payload, "base64url").toString()),
      );
      const parent = eventsForRoles.find(
        (event) =>
          event.tenantId === "acme" &&
          event.action === "roles.list" &&
          event.outcome === "requested",
      );
      expect(
        eventsForRoles.filter((event) => event.parentDecisionId === parent.decisionId),
      ).toHaveLength(8);
      const discovery = runtime.createWorkspaceDiscovery({
        scanBudget: 1,
        activeKeyId: "cursor",
        keys: { cursor: Buffer.alloc(32, 4) },
        source: {
          scan: async ({ after }) => {
            const id = after === null ? "globex" : after === "globex" ? "acme" : "outage";
            return {
              items: [{ workspace: { id, name: "Shared name" }, position: id }],
              exhausted: id !== "globex",
            };
          },
        },
      });
      const sparse = await discovery.list(actor, { signal });
      expect(sparse.items).toEqual([]);
      expect(sparse.nextCursor).toMatch(/^hc1\./);
      const page = await discovery.list(actor, { signal, cursor: sparse.nextCursor! });
      expect(page).toEqual({ items: [{ id: "acme", name: "Shared name" }], nextCursor: null });
      await expect(
        discovery.list(actor, { signal, cursor: sparse.nextCursor! + "x" }),
      ).rejects.toThrow("invalid_argument");
      const unavailable = runtime.createWorkspaceDiscovery({
        activeKeyId: "cursor",
        keys: { cursor: Buffer.alloc(32, 4) },
        source: {
          scan: async () => ({
            items: [{ workspace: { id: "outage", name: "Unavailable" }, position: "outage" }],
            exhausted: true,
          }),
        },
      });
      await expect(unavailable.list(actor, { signal })).rejects.toThrow("unavailable");
      await storage.identity.invalidate(
        {
          issuer: token.issuer,
          subject: token.subject,
          context: "bearer",
          tokenDigest: "a".repeat(64),
          reason: "identity_inactive",
        },
        signal,
      );
      await expect(runtime.tokens.verify(raw, { signal })).rejects.toThrow("unauthenticated");
      const events = await pool.query(
        "SELECT count(*)::int AS count FROM heimdall.audit_events WHERE product_id='ledger' AND environment='public-ts'",
      );
      expect(events.rows[0].count).toBe(45);
      const delivery = await pool.query(
        "SELECT sink_id, count(*)::int AS count FROM heimdall.audit_deliveries WHERE product_id='ledger' AND environment='public-ts' GROUP BY sink_id",
      );
      expect(delivery.rows).toEqual([{ sink_id: "siem", count: 45 }]);
      await expect(storage.appendAudit({}, signal)).rejects.toThrow("invalid_argument");
      expect((await pool.query("SELECT 1 AS alive")).rows[0].alive).toBe(1);
    } finally {
      await pool.end();
    }
    await expect(
      createHeimdall(JSON.stringify(s.config), JSON.stringify(base.manifest), s.options, {
        signal,
      }),
    ).rejects.toThrow("unavailable");
  },
);

test.skipIf(!process.env.HEIMDALL_TEST_DATABASE_URL || !!process.env.HEIMDALL_TEST_READ_PEER)(
  "managed runtime gates every tenant read and owns candidate discovery",
  async () => {
    const s = setup("", "managed-ts");
    const dsn = process.env.HEIMDALL_TEST_DATABASE_URL;
    const pool = new Pool({ connectionString: dsn, options: "-c role=heimdall_public_runtime" });
    const admin = new Pool({ connectionString: dsn });
    const signal = new AbortController().signal;
    try {
      s.options.storage = createManagedPostgresStorage(pool, { ...s.config, auditSinks: [] });
      const runtime = await createHeimdall(
        JSON.stringify(s.config),
        JSON.stringify(base.manifest),
        s.options,
        { signal },
      );
      const actor = await runtime.tokens.verify(raw, { signal });
      const principal =
        "user:" +
        Buffer.from(
          JSON.stringify([token.issuer, token.subject]).replace(
            /[\u007f-\uffff]/g,
            (c) => "\\u" + c.charCodeAt(0).toString(16).padStart(4, "0"),
          ),
        ).toString("base64url");
      await admin.query(
        "INSERT INTO heimdall_managed.workspaces(product_id,environment,workspace_id,name,lifecycle,mutation_blocked,created_at,updated_at) VALUES('ledger','managed-ts','acme','Acme','active',false,1,1)",
      );
      await admin.query(
        "INSERT INTO heimdall_managed.membership_candidates(product_id,environment,workspace_id,identity_hash,principal,observed_at) VALUES('ledger','managed-ts','acme',sha256(convert_to($1,'UTF8')),$1,1)",
        [principal],
      );
      const options = { activeKeyId: "cursor", keys: { cursor: Buffer.alloc(32, 4) } };
      expect(() =>
        runtime.createWorkspaceDiscovery({
          ...options,
          source: { scan: async () => ({ items: [], exhausted: true }) },
        }),
      ).toThrow("invalid_argument");
      const discovery = runtime.createWorkspaceDiscovery(options);
      expect(await runtime.tenant("acme").can(actor, "invoice.approve", { signal })).toBe(true);
      expect(await runtime.tenant("acme").roles(actor, { signal })).toEqual(["approver"]);
      expect((await discovery.list(actor, { signal })).items).toEqual([
        { id: "acme", name: "Acme" },
      ]);
      const requests = s.order.filter((entry) => entry === "check").length;
      await admin.query(
        "UPDATE heimdall_managed.workspaces SET mutation_blocked=true WHERE environment='managed-ts'",
      );
      expect(await runtime.tenant("acme").can(actor, "invoice.approve", { signal })).toBe(false);
      expect(await runtime.tenant("acme").roles(actor, { signal })).toEqual([]);
      expect((await discovery.list(actor, { signal })).items).toEqual([]);
      expect(s.order.filter((entry) => entry === "check")).toHaveLength(requests);
    } finally {
      await Promise.all([pool.end(), admin.end()]);
    }
  },
);

test("public sessions persist browser-bound login through the custom store", async () => {
  const s = setup();
  const signal = new AbortController().signal;
  const writes: string[] = [];
  s.options.sessionStorage = {
    activeKeyId: "k1",
    keys: { k1: Buffer.alloc(32, 7) },
    reserveKeyUsage: async () => {
      writes.push("reserve");
    },
    store: {
      async create(key, value) {
        expect(key).toMatch(/^heimdall\.v1\|ledger\|production\|login\|[a-f0-9]{64}$/);
        expect(Buffer.from(value).toString()).toMatch(/^hr1\.k1\./);
        writes.push("create");
        return "1";
      },
      read: async () => null,
      compareAndSwap: async () => null,
      delete: async () => {},
    },
  };
  const runtime = await createHeimdall(
    JSON.stringify(s.config),
    JSON.stringify(base.manifest),
    s.options,
    { signal },
  );
  const callbackUrl = base.manifest.clients.web.redirectUris[0] as string;
  const sessions = await runtime.createSessions(
    { callbackUrl, returnTo: "/workspaces", zitadelAPIAudience: "zitadel" },
    { signal },
  );
  expect((await sessions.start(new Request(new URL("/auth/start", callbackUrl)))).status).toBe(303);
  expect(writes).toEqual(["reserve", "create"]);
});

test.skipIf(!process.env.HEIMDALL_TEST_DATABASE_URL || !!process.env.HEIMDALL_TEST_READ_PEER)(
  "managed onboarding safely resumes across provider and activation faults",
  async () => {
    const s = setup("", "onboarding-ts");
    const dsn = process.env.HEIMDALL_TEST_DATABASE_URL;
    const pool = new Pool({ connectionString: dsn, options: "-c role=heimdall_public_runtime" });
    const admin = new Pool({ connectionString: dsn });
    const call = { signal: new AbortController().signal };
    const fixture = read("workspace-onboarding.json");
    let approvals = 0;
    let permit = false;
    try {
      s.options.storage = createManagedPostgresStorage(pool, { ...s.config, auditSinks: ["siem"] });
      s.options.authorizeWorkspaceCreation = async () => {
        approvals++;
        return permit;
      };
      const open = () =>
        createHeimdall(JSON.stringify(s.config), JSON.stringify(base.manifest), s.options, call);
      const runtime = await open();
      const actor = await runtime.tokens.verify(raw, call);
      const api = runtime.onboarding!;
      const input = { name: fixture.name, idempotencyKey: fixture.idempotencyKey };
      await expect(api.create(actor, input, call)).rejects.toThrow("forbidden");
      expect(s.fga.writes).toBe(0);
      permit = true;
      const other = await open();
      await expect(other.onboarding!.create(actor, input, call)).rejects.toThrow("unauthenticated");
      const restartedActor = await other.tokens.verify(raw, call);
      const expectedActive = [];
      for (const item of fixture.cases) {
        const request = { ...input, idempotencyKey: input.idempotencyKey + "-" + item.fault };
        const before = s.fga.writes;
        s.fga.fault = item.fault;
        const activationFailure = item.fault === "activation_failure";
        let creationError = "";
        if (activationFailure) {
          await admin.query(`CREATE FUNCTION public.fail_onboarding_ts() RETURNS trigger LANGUAGE plpgsql AS $$
            BEGIN IF NEW.environment='onboarding-ts' AND NEW.lifecycle='active' THEN
              RAISE EXCEPTION 'test activation failure'; END IF; RETURN NEW; END $$;
            CREATE TRIGGER fail_onboarding_ts BEFORE UPDATE ON heimdall_managed.workspaces
            FOR EACH ROW EXECUTE FUNCTION public.fail_onboarding_ts()`);
          try {
            creationError = await api.create(actor, request, call).then(
              () => "",
              (error: { code: string }) => error.code,
            );
          } finally {
            await admin.query(
              "DROP TRIGGER fail_onboarding_ts ON heimdall_managed.workspaces; DROP FUNCTION public.fail_onboarding_ts()",
            );
          }
        }
        expect(creationError).toMatch(
          activationFailure ? /^(unavailable|audit_unavailable)$/ : /^$/,
        );
        const result = await api.create(actor, request, call);
        expect(result.status).toBe(item.status);
        expect(result.reason).toBe(item.reason);
        expect(s.fga.writes - before).toBe(item.writes);
        permit = false;
        const count = approvals;
        expect(await api.create(actor, request, call)).toEqual(result);
        expect(approvals).toBe(count);
        await expect(api.create(actor, { ...request, name: "Changed" }, call)).rejects.toThrow(
          "conflict",
        );
        expect(await api.get(actor, result.operationId, call)).toEqual(result);
        const stored = await admin.query(
          "SELECT state,generation FROM heimdall_managed.operations WHERE environment='onboarding-ts' AND operation_id=$1",
          [result.operationId],
        );
        const uncertain = ["lost_response", "lost_before_apply"].includes(item.fault);
        expect(stored.rows[0]?.state).toBe(
          activationFailure
            ? "confirmed"
            : uncertain
              ? "uncertain"
              : item.fault === "before_dispatch"
                ? "retryable"
                : "completed",
        );
        expect(await s.options.storage.managed!.allows(result.workspaceId, call.signal)).toBe(
          item.status === "active",
        );
        s.fga.fault = "none";
        const resumed = await other.onboarding!.resume(restartedActor, result.operationId, call);
        expect(resumed).toEqual(
          uncertain
            ? result
            : {
                operationId: result.operationId,
                workspaceId: result.workspaceId,
                status: "active",
              },
        );
        const recovery = {
          operationId: result.operationId,
          initiatingActor: { issuer: actor.issuer, subject: actor.subject },
          expectedGeneration: Number(stored.rows[0]?.generation),
          policyVersion: "p1",
          auditSinks: ["siem"],
          recoveryToken: "new-key",
        };
        const runRecovery = (
          verifyQuiescence: Parameters<typeof recoverWorkspaceCreation>[3]["verifyQuiescence"],
        ) =>
          recoverWorkspaceCreation(
            pool,
            JSON.stringify(s.config),
            JSON.stringify(base.manifest),
            {
              ...recovery,
              verifyQuiescence,
            },
            call.signal,
          );
        await expect(runRecovery(async () => null)).rejects.toThrow(
          uncertain ? "forbidden" : "conflict",
        );
        const recovered = await runRecovery(async (scope) =>
          scope.workspaceId === result.workspaceId &&
          scope.generation === recovery.expectedGeneration
            ? { operatorId: "operator", evidenceId: "drain-record" }
            : null,
        ).catch((error: { code: string }) => error.code);
        expect(recovered).toEqual(
          uncertain
            ? {
                operationId: result.operationId,
                workspaceId: result.workspaceId,
                status: "active",
              }
            : "conflict",
        );
        if (uncertain)
          await runRecovery(async () => null).catch((error: { code: string }) => {
            if (error.code !== "conflict") throw error;
          });
        expectedActive.push(result.workspaceId);
        expect(s.fga.writes - before).toBe(
          item.fault === "before_dispatch"
            ? 1
            : item.writes + Number(item.fault === "lost_before_apply"),
        );
        permit = true;
      }
      const race = { name: "Concurrent", idempotencyKey: "concurrent-create" };
      const beforeRace = s.fga.writes;
      const results = await Promise.all(
        Array.from({ length: 4 }, () => api.create(actor, race, call)),
      );
      expect(new Set(results.map(({ operationId }) => operationId)).size).toBe(1);
      expect(new Set(results.map(({ workspaceId }) => workspaceId)).size).toBe(1);
      expect(s.fga.writes - beforeRace).toBe(1);
      const winner = await api.get(actor, results[0]!.operationId, call);
      expect(winner.status).toBe("active");
      expectedActive.push(winner.workspaceId);
      const page = await runtime
        .createWorkspaceDiscovery({ activeKeyId: "cursor", keys: { cursor: Buffer.alloc(32, 9) } })
        .list(actor, call);
      expect(page.items.map(({ id }) => id)).toEqual(expectedActive);
      const counts = await admin.query(
        "SELECT count(*)::int AS count FROM heimdall.audit_deliveries WHERE environment='onboarding-ts' AND sink_id='siem'",
      );
      expect(counts.rows[0]?.count).toBeGreaterThan(20);
      const audit = await admin.query(
        "SELECT event->>'payload' AS payload FROM heimdall.audit_events WHERE environment='onboarding-ts'",
      );
      const recoveryEvents = audit.rows
        .map(({ payload }) => JSON.parse(Buffer.from(payload, "base64url").toString()))
        .filter((event) => event.requestId === "drain-record");
      expect(recoveryEvents).toHaveLength(6);
      expect(
        recoveryEvents.every(
          (event) => event.actor.kind === "system" && event.actor.id === "operator",
        ),
      ).toBe(true);
    } finally {
      await Promise.all([pool.end(), admin.end()]);
    }
  },
);

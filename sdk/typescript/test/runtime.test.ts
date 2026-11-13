import { Pool } from "pg";
import { createPostgresStorage } from "../src/postgres.js";
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
    vi.fn<typeof fetch>(async (input) => {
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
      if (url.endsWith("/keys")) return Response.json(token.jwks);
      if (url.endsWith("/introspect")) return Response.json(status.introspection);
      return Response.json(status.user);
    }),
  );
  return { config, options, events, order };
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
      await runtime.tokens.verify(raw, { signal });
      await expect(runtime.tokens.verify("invalid", { signal })).rejects.toThrow("unauthenticated");
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
      expect(events.rows[0].count).toBe(3);
      const delivery = await pool.query(
        "SELECT sink_id, count(*)::int AS count FROM heimdall.audit_deliveries WHERE product_id='ledger' AND environment='public-ts' GROUP BY sink_id",
      );
      expect(delivery.rows).toEqual([{ sink_id: "siem", count: 3 }]);
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

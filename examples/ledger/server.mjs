import { createServer } from "node:http";
import { addAbortSignal } from "node:stream";
import { Pool } from "pg";
import { createHeimdall, HeimdallError } from "@heimdall/sdk";
import { createManagedPostgresStorage } from "@heimdall/sdk/postgres";

function required(name) {
  const value = process.env[name];
  if (!value) throw new Error("Missing environment variable " + name);
  return value;
}

const configJSON = required("HEIMDALL_CONFIG");
const manifestJSON = required("HEIMDALL_MANIFEST");
const config = JSON.parse(configJSON);
if (config.productId !== "ledger") throw new Error("Expected the Ledger product");
const callbackUrl = required("HEIMDALL_CALLBACK_URL");
const origin = new URL(callbackUrl).origin;
const workspaceCreator = process.env.HEIMDALL_WORKSPACE_CREATOR_SUBJECT;
const pool = new Pool({ connectionString: required("DATABASE_URL") });
const keyUsagePool = new Pool({ connectionString: required("HEIMDALL_KEY_DATABASE_URL") });
const business = new Pool({
  connectionString: required("LEDGER_DATABASE_URL"),
  statement_timeout: 5000,
  connectionTimeoutMillis: 5000,
});

const signal = AbortSignal.timeout(10000);
const auth = await createHeimdall(
  configJSON,
  manifestJSON,
  {
    policyVersion: required("HEIMDALL_POLICY_VERSION"),
    authorizeWorkspaceCreation: async (actor) => actor.subject === workspaceCreator,
    credentials: {
      clientSecret: async () => required("HEIMDALL_CLIENT_SECRET"),
      openfgaKey: async () => required("HEIMDALL_OPENFGA_KEY"),
    },
    storage: createManagedPostgresStorage(pool, {
      productId: config.productId,
      environment: config.environment,
      auditSinks: [],
      sessions: {
        keyUsagePool,
        activeKeyId: required("HEIMDALL_RECORD_KEY_ID"),
        keys: Object.fromEntries(
          Object.entries(JSON.parse(required("HEIMDALL_RECORD_KEYS"))).map(([id, key]) => [
            id,
            Buffer.from(key, "base64"),
          ]),
        ),
      },
    }),
  },
  { signal },
);
const sessions = await auth.createSessions(
  {
    callbackUrl,
    returnTo: "/workspaces",
    zitadelAPIAudience: required("HEIMDALL_ZITADEL_API_AUDIENCE"),
  },
  { signal },
);
const discovery = auth.createWorkspaceDiscovery({
  activeKeyId: "v1",
  keys: { v1: Buffer.from(required("HEIMDALL_CURSOR_KEY"), "base64") },
});

const read = (value, status = 200) =>
  Response.json(value, {
    status,
    headers: { "cache-control": "no-store", "referrer-policy": "no-referrer" },
  });

async function workspaceInput(request) {
  if (request.headers.get("content-type")?.split(";")[0]?.trim() !== "application/json")
    throw new HeimdallError("invalid_argument");
  const chunks = [];
  let size = 0;
  if (!request.body) throw new HeimdallError("invalid_argument");
  for await (const chunk of request.body) {
    size += chunk.byteLength;
    if (size > 4096) throw new HeimdallError("invalid_argument");
    chunks.push(chunk);
  }
  let input;
  try {
    input = JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(Buffer.concat(chunks)));
  } catch {
    throw new HeimdallError("invalid_argument");
  }
  if (
    !input ||
    typeof input.name !== "string" ||
    Object.keys(input).some((field) => field !== "name")
  )
    throw new HeimdallError("invalid_argument");
  const idempotencyKey = request.headers.get("idempotency-key") ?? "";
  if (idempotencyKey.includes(", ")) throw new HeimdallError("invalid_argument");
  return { name: input.name, idempotencyKey };
}

async function route(request) {
  const url = new URL(request.url);
  if (request.method === "GET" && url.pathname === "/healthz")
    return read({
      ready: true,
      deployment: process.env.RAILWAY_DEPLOYMENT_ID ?? null,
      verificationRun: process.env.HEIMDALL_VERIFICATION_RUN ?? null,
    });
  if (request.method === "GET" && url.pathname === "/")
    return read({ login: "/auth/start", workspaces: "/workspaces" });
  if (url.pathname === "/auth/start") return sessions.start(request);
  if (url.pathname === new URL(callbackUrl).pathname) return sessions.callback(request);
  if (url.pathname === "/auth/logout") return sessions.logout(request);
  const operation = /^\/workspace-operations\/([A-Za-z0-9_-]{1,128})$/.exec(url.pathname);
  const create = request.method === "POST" && url.pathname === "/workspaces";
  const resume = request.method === "POST" && operation;
  if (create || resume || (request.method === "GET" && operation)) {
    if ((create || resume) && request.headers.get("origin") !== origin)
      throw new HeimdallError("forbidden");
    const actor = await sessions.authenticate(request);
    const call = { signal: request.signal };
    const result = create
      ? await auth.onboarding.create(actor, await workspaceInput(request), call)
      : resume
        ? await auth.onboarding.resume(actor, operation[1], call)
        : await auth.onboarding.get(actor, operation[1], call);
    return read(result, (create || resume) && result.status === "pending" ? 202 : 200);
  }
  if (request.method !== "GET") return read({ code: "not_found" }, 404);
  const scoped = /^\/workspaces\/([A-Za-z0-9_-]{1,128})\/(invoices|roles)$/.exec(url.pathname);
  if (!scoped && url.pathname !== "/workspaces") return read({ code: "not_found" }, 404);
  const actor = await sessions.authenticate(request);
  if (!scoped)
    return read(
      await discovery.list(actor, {
        signal: request.signal,
        ...(url.searchParams.has("cursor") ? { cursor: url.searchParams.get("cursor") } : {}),
      }),
    );
  const [, workspace, collection] = scoped;
  const tenant = auth.tenant(workspace);
  if (collection === "roles")
    return read({ roles: await tenant.roles(actor, { signal: request.signal }) });
  await tenant.require(actor, "invoice.read", { signal: request.signal });
  const result = await business.query(
    "SELECT invoice_id, total_cents FROM ledger.invoices WHERE workspace_id=$1 ORDER BY invoice_id LIMIT 100",
    [workspace],
  );
  return read({ invoices: result.rows });
}

const statuses = {
  unauthenticated: 401,
  forbidden: 403,
  invalid_argument: 400,
  not_found: 404,
  conflict: 409,
};
createServer(async (incoming, outgoing) => {
  const cancellation = new AbortController();
  outgoing.once("close", () => {
    if (!outgoing.writableFinished) cancellation.abort();
  });
  let response;
  try {
    const target = new URL(incoming.url, origin);
    if (target.origin !== origin) {
      response = read({ code: "invalid_argument" }, 400);
    } else {
      const signal = AbortSignal.any([cancellation.signal, AbortSignal.timeout(15000)]);
      response = await route(
        new Request(target, {
          method: incoming.method,
          ...(incoming.method === "POST" && target.pathname === "/workspaces"
            ? { body: addAbortSignal(signal, incoming), duplex: "half" }
            : {}),
          headers: Object.entries(incoming.headers).flatMap(([name, value]) =>
            (Array.isArray(value) ? value : value === undefined ? [] : [value]).map((entry) => [
              name,
              entry,
            ]),
          ),
          signal,
        }),
      );
    }
  } catch (error) {
    const code = error instanceof HeimdallError ? error.code : "unavailable";
    response = read(
      error instanceof HeimdallError ? error.toJSON() : { code },
      statuses[code] ?? 503,
    );
  }
  incoming.resume();
  const cookies = response.headers.getSetCookie();
  const headers = Object.fromEntries(response.headers);
  delete headers["set-cookie"];
  outgoing.writeHead(response.status, {
    ...headers,
    ...(cookies.length ? { "set-cookie": cookies } : {}),
  });
  outgoing.end(Buffer.from(await response.arrayBuffer()));
}).listen(Number(process.env.PORT ?? "8080"), "0.0.0.0");

import { createServer } from "node:http";
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

const statuses = { unauthenticated: 401, forbidden: 403, invalid_argument: 400, not_found: 404 };
createServer(async (incoming, outgoing) => {
  const cancellation = new AbortController();
  outgoing.once("close", () => cancellation.abort());
  let response;
  try {
    const target = new URL(incoming.url, origin);
    if (target.origin !== origin) {
      response = read({ code: "invalid_argument" }, 400);
    } else {
      response = await route(
        new Request(target, {
          method: incoming.method,
          headers: Object.entries(incoming.headers).flatMap(([name, value]) =>
            (Array.isArray(value) ? value : value === undefined ? [] : [value]).map((entry) => [
              name,
              entry,
            ]),
          ),
          signal: AbortSignal.any([cancellation.signal, AbortSignal.timeout(15000)]),
        }),
      );
    }
  } catch (error) {
    const code = error instanceof HeimdallError ? error.code : "unavailable";
    response = read({ code }, statuses[code] ?? 503);
  }
  const cookies = response.headers.getSetCookie();
  const headers = Object.fromEntries(response.headers);
  delete headers["set-cookie"];
  outgoing.writeHead(response.status, {
    ...headers,
    ...(cookies.length ? { "set-cookie": cookies } : {}),
  });
  outgoing.end(Buffer.from(await response.arrayBuffer()));
}).listen(Number(process.env.PORT ?? "8080"), "0.0.0.0");

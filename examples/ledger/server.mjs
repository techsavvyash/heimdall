import { createHash, randomBytes } from "node:crypto";
import { createServer } from "node:http";
import { addAbortSignal } from "node:stream";
import { Pool } from "pg";
import { createHeimdall, HeimdallError } from "@heimdall/sdk";
import { createManagedPostgresStorage, createRecordMaintenance } from "@heimdall/sdk/postgres";
import { tenantSSOPolicy } from "./tenant-sso.mjs";

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
const federationPolicies = process.env.HEIMDALL_TENANT_SSO_POLICIES;
const workspaceCreator = process.env.HEIMDALL_WORKSPACE_CREATOR_SUBJECT;
const pool = new Pool({ connectionString: required("DATABASE_URL") });
const keyUsagePool = new Pool({ connectionString: required("HEIMDALL_KEY_DATABASE_URL") });
const business = new Pool({
  connectionString: required("LEDGER_DATABASE_URL"),
  statement_timeout: 5000,
  connectionTimeoutMillis: 5000,
});

const exportDSN = process.env.HEIMDALL_EXPORT_DATABASE_URL;
const exportPool = exportDSN && new Pool({ connectionString: exportDSN });
const maintenance = createRecordMaintenance(pool, config.productId, config.environment);
let lastSuccessAt = null;
const signal = AbortSignal.timeout(10000);
const auth = await createHeimdall(
  configJSON,
  manifestJSON,
  {
    policyVersion: required("HEIMDALL_POLICY_VERSION"),
    tenantSSOPolicy: tenantSSOPolicy(),
    authorizeWorkspaceCreation: async (actor) => actor.subject === workspaceCreator,
    authorizeWorkspaceLifecycle: async (actor) => actor.subject === workspaceCreator,
    ...(exportPool
      ? { authorizeManagedExport: async (scope) => scope.actor.subject === workspaceCreator }
      : {}),
    credentials: {
      clientSecret: async () => required("HEIMDALL_CLIENT_SECRET"),
      openfgaKey: async () => required("HEIMDALL_OPENFGA_KEY"),
    },
    storage: createManagedPostgresStorage(pool, {
      productId: config.productId,
      environment: config.environment,
      auditSinks: [],
      ...(exportPool ? { exportPool } : {}),
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
    ...(federationPolicies
      ? { federation: { credential: async () => required("HEIMDALL_FEDERATION_CREDENTIAL") } }
      : {}),
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

async function mutationInput(request) {
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
  if (!input || typeof input !== "object" || Array.isArray(input))
    throw new HeimdallError("invalid_argument");
  const idempotencyKey = request.headers.get("idempotency-key") ?? "";
  if (idempotencyKey.includes(", ")) throw new HeimdallError("invalid_argument");
  return { input, idempotencyKey };
}

async function workspaceInput(request) {
  const { input, idempotencyKey } = await mutationInput(request);
  if (typeof input.name !== "string" || Object.keys(input).length !== 1)
    throw new HeimdallError("invalid_argument");
  return { name: input.name, idempotencyKey };
}

const memberRoute =
  /^\/workspaces\/([A-Za-z0-9_-]{1,128})\/members\/(add|assign-roles|remove|transfer-ownership)$/;
const eligibleSubjects = new Set([workspaceCreator, process.env.HEIMDALL_REFERENCE_MEMBER_SUBJECT]);

const invitationRoute = /^\/workspaces\/([A-Za-z0-9_-]{1,128})\/invitations$/;

async function invitation(request, workspace) {
  if (request.headers.get("origin") !== origin) throw new HeimdallError("forbidden");
  const actor = await sessions.authenticate(request);
  const { input } = await mutationInput(request);
  const call = { signal: request.signal };
  if (workspace) {
    if (
      Object.keys(input).length !== 2 ||
      typeof input.subject !== "string" ||
      !Array.isArray(input.roles) ||
      input.roles.some((role) => typeof role !== "string")
    )
      throw new HeimdallError("invalid_argument");
    if (!eligibleSubjects.has(input.subject)) throw new HeimdallError("forbidden");
    const token = randomBytes(32).toString("hex");
    const hash = createHash("sha256").update(token).digest("hex");
    const target = { issuer: config.issuer, subject: input.subject };
    await auth.tenant(workspace).memberships.authorizeInvitation(
      actor,
      {
        target,
        roles: input.roles,
        idempotencyKey: hash,
      },
      call,
    );
    const created = await business.query(
      `INSERT INTO ledger.invitations(environment,token_hash,workspace_id,issuer,recipient,inviter,roles,expires_at)
       VALUES($1,$2,$3,$4,$5,$6,$7,now()+interval '1 hour') RETURNING expires_at`,
      [
        config.environment,
        hash,
        workspace,
        target.issuer,
        target.subject,
        actor.subject,
        JSON.stringify(input.roles),
      ],
    );
    return read({ token, expiresAt: created.rows[0].expires_at }, 201);
  }
  if (
    Object.keys(input).length !== 1 ||
    typeof input.token !== "string" ||
    !/^[a-f0-9]{64}$/.test(input.token)
  )
    throw new HeimdallError("invalid_argument");
  const hash = createHash("sha256").update(input.token).digest("hex");
  const claim = [config.environment, hash, actor.issuer, actor.subject];
  const {
    rows: [record],
  } = await business.query(
    `UPDATE ledger.invitations SET claimed_at=COALESCE(claimed_at,now())
     WHERE environment=$1 AND token_hash=$2 AND issuer=$3 AND recipient=$4
     AND (claimed_at IS NOT NULL OR expires_at>now())
     RETURNING workspace_id,inviter,roles,operation_id`,
    claim,
  );
  if (!record) throw new HeimdallError("not_found");
  if (record.operation_id)
    return read({
      workspaceId: record.workspace_id,
      operationId: record.operation_id,
      status: "confirmed",
    });
  const result = await auth.tenant(record.workspace_id).memberships.acceptInvitation(
    actor,
    { issuer: actor.issuer, subject: record.inviter },
    {
      target: { issuer: actor.issuer, subject: actor.subject },
      roles: record.roles,
      idempotencyKey: hash,
    },
    call,
  );
  if (result.status === "confirmed")
    await business.query(
      `UPDATE ledger.invitations SET operation_id=$5
       WHERE environment=$1 AND token_hash=$2 AND issuer=$3 AND recipient=$4`,
      [...claim, result.operationId],
    );
  return read(result, result.status === "pending" ? 202 : 200);
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
  if (federationPolicies && url.pathname === "/auth/federation") {
    const workspace = url.searchParams.getAll("workspace");
    if (workspace.length !== 1) throw new HeimdallError("invalid_argument");
    return sessions.startFederation(request, workspace[0]);
  }
  if (url.pathname === new URL(callbackUrl).pathname) return sessions.callback(request);
  if (url.pathname === "/auth/logout") return sessions.logout(request);
  if (request.method === "GET" && url.pathname === "/managed-export" && auth.exportManagedPage) {
    const bearer = /^Bearer (\S+)$/i.exec(request.headers.get("authorization") ?? "");
    if (!bearer) throw new HeimdallError("unauthenticated");
    const inventory = url.searchParams.get("inventory") === "1";
    const run = inventory ? auth.exportManagedInventory : auth.exportManagedPage;
    const data = await run(
      bearer[1],
      {
        kind: url.searchParams.get("kind"),
        workspaceId: url.searchParams.get("workspace"),
        after: url.searchParams.get("after"),
        operationId: url.searchParams.get("operation"),
        limit: 1,
      },
      { signal: request.signal },
    );
    return read(JSON.parse(data));
  }
  if (request.method === "POST" && url.pathname === "/workspace-operations/reconcile") {
    if (request.headers.get("origin") !== origin) throw new HeimdallError("forbidden");
    const actor = await sessions.authenticate(request);
    if (actor.subject !== workspaceCreator) throw new HeimdallError("forbidden");
    const result = await auth.reconcile({ signal: request.signal });
    lastSuccessAt = Date.now();
    return read(result);
  }
  if (request.method === "GET" && url.pathname === "/workspace-operations/metrics") {
    const actor = await sessions.authenticate(request);
    if (actor.subject !== workspaceCreator) throw new HeimdallError("forbidden");
    return read({
      ...(await auth.operationMetrics({ signal: request.signal })),
      cleanupLagMs: await maintenance.cleanupLag(request.signal),
      lastSuccessAt,
    });
  }
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
  if (request.method === "POST") {
    const lifecycle = /^\/workspaces\/([A-Za-z0-9_-]{1,128})\/(suspend|resume)$/.exec(url.pathname);
    if (lifecycle) {
      if (request.headers.get("origin") !== origin) throw new HeimdallError("forbidden");
      const actor = await sessions.authenticate(request);
      const tenant = auth.tenant(lifecycle[1]);
      const change = lifecycle[2] === "suspend" ? tenant.suspend : tenant.resume;
      await change(actor, { signal: request.signal });
      return new Response(null, { status: 204 });
    }
    const issue = invitationRoute.exec(url.pathname);
    if (issue || url.pathname === "/invitations/accept") return invitation(request, issue?.[1]);
  }
  const administration = request.method === "POST" && memberRoute.exec(url.pathname);
  if (administration) {
    if (request.headers.get("origin") !== origin) throw new HeimdallError("forbidden");
    const actor = await sessions.authenticate(request);
    const { input, idempotencyKey } = await mutationInput(request);
    const [, workspace, action] = administration;
    const takesRoles = action === "add" || action === "assign-roles";
    if (
      typeof input.subject !== "string" ||
      Object.keys(input).length !== (takesRoles ? 2 : 1) ||
      (takesRoles &&
        (!Array.isArray(input.roles) || input.roles.some((role) => typeof role !== "string")))
    )
      throw new HeimdallError("invalid_argument");
    if (!eligibleSubjects.has(input.subject)) throw new HeimdallError("forbidden");
    const options = { target: { issuer: config.issuer, subject: input.subject }, idempotencyKey };
    const members = auth.tenant(workspace).memberships;
    const call = { signal: request.signal };
    let result;
    switch (action) {
      case "add":
        result = await members.add(actor, { ...options, roles: input.roles }, call);
        break;
      case "assign-roles":
        result = await members.assignRoles(actor, { ...options, roles: input.roles }, call);
        break;
      case "remove":
        result = await members.remove(actor, options, call);
        break;
      default:
        result = await members.transferOwnership(actor, options, call);
    }
    return read(result, result.status === "pending" ? 202 : 200);
  }
  if (request.method !== "GET") return read({ code: "not_found" }, 404);
  const scoped = /^\/workspaces\/([A-Za-z0-9_-]{1,128})\/(invoices|roles)$/.exec(url.pathname);
  if (!scoped && url.pathname !== "/workspaces") return read({ code: "not_found" }, 404);
  const actor = await sessions.authenticate(request);
  if (!scoped)
    return read(
      await discovery.list(actor, {
        signal: request.signal,
        ...(url.searchParams.has("limit") ? { limit: Number(url.searchParams.get("limit")) } : {}),
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
          ...(incoming.method === "POST" &&
          (target.pathname === "/workspaces" ||
            memberRoute.test(target.pathname) ||
            invitationRoute.test(target.pathname) ||
            target.pathname === "/invitations/accept")
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

import { Pool } from "pg";
import { createHeimdall, HeimdallError } from "@heimdall/sdk";
import {
  createDirectoryCleanup,
  createDirectoryPolling,
  createManagedPostgresStorage,
} from "@heimdall/sdk/postgres";
import { directoryMapping } from "./directory-cleanup.mjs";
import { providerSettings } from "./provider-settings.mjs";
import { tenantSSOPolicy } from "./tenant-sso.mjs";

function required(name) {
  const value = process.env[name];
  if (!value) throw new Error("Missing directory job configuration");
  return value;
}

const stop = new AbortController();
process.once("SIGTERM", () => stop.abort());
process.once("SIGINT", () => stop.abort());
const signal = AbortSignal.any([stop.signal, AbortSignal.timeout(45000)]);
const pool = new Pool({ connectionString: process.env.DATABASE_URL });
let business;

try {
  required("DATABASE_URL");
  const configJSON = required("HEIMDALL_CONFIG");
  const { productId, environment, issuer } = JSON.parse(configJSON);
  const workspaces = directoryMapping();
  if (productId !== "ledger") throw new HeimdallError("invalid_argument");
  const scope = {
    productId,
    environment,
    issuer,
    organizationId: required("HEIMDALL_DIRECTORY_ORGANIZATION"),
    token: required("HEIMDALL_DIRECTORY_READER_TOKEN"),
  };
  const polling = createDirectoryPolling(pool, scope);
  const discovery = await polling.discover(25, signal);
  let after = null;
  let observed = 0;
  let sessionsInvalidated = 0;
  do {
    const page = await polling.observeKnown(after, 10, signal);
    observed += page.observed;
    sessionsInvalidated += page.sessionsInvalidated;
    after = page.nextSubject;
  } while (after !== null);
  console.log(JSON.stringify({ ...discovery, observed, sessionsInvalidated }));

  const cleanup = createDirectoryCleanup(pool, scope);
  const pending = await cleanup.pending(null, 10, signal);
  if (pending.length > 0) {
    business = new Pool({
      connectionString: required("LEDGER_DATABASE_URL"),
      statement_timeout: 5000,
      connectionTimeoutMillis: 5000,
    });
    const settings = providerSettings(
      business,
      { productId, environment, issuer },
      tenantSSOPolicy(),
      false,
    );
    const runtime = await createHeimdall(
      configJSON,
      required("HEIMDALL_MANIFEST"),
      {
        policyVersion: required("HEIMDALL_POLICY_VERSION"),
        tenantSSOPolicy: settings.policy,
        credentials: {
          clientSecret: async () => required("HEIMDALL_CLIENT_SECRET"),
          openfgaKey: async () => required("HEIMDALL_OPENFGA_KEY"),
        },
        storage: createManagedPostgresStorage(pool, { ...scope, auditSinks: [] }),
      },
      { signal },
    );
    const actor = await runtime.tokens.verify(required("HEIMDALL_DIRECTORY_OPERATOR_TOKEN"), {
      signal,
    });
    if (
      process.env.HEIMDALL_DIRECTORY_OPERATOR_SUBJECT &&
      actor.subject !== process.env.HEIMDALL_DIRECTORY_OPERATOR_SUBJECT
    )
      throw new HeimdallError("forbidden");
    const manifest = JSON.parse(required("HEIMDALL_MANIFEST"));
    for (const workspace of workspaces) {
      const roles = await runtime.tenant(workspace).roles(actor, { signal });
      if (!roles.includes(manifest.ownerRole)) throw new HeimdallError("forbidden");
    }
    for (const { subject, revision } of pending) {
      const result = await cleanup.reconcile(
        runtime,
        actor,
        { subject, revision, workspaces },
        signal,
      );
      if (result.status !== "confirmed") throw new HeimdallError("outcome_unknown");
    }
  }
  console.log(
    JSON.stringify({
      cleaned: pending.length,
      oldestCleanupAgeMs: pending.length ? Math.max(0, Date.now() - pending[0].observedAt) : 0,
    }),
  );
} catch (error) {
  console.error(
    JSON.stringify({ directoryJob: error instanceof HeimdallError ? error.code : "failed" }),
  );
  process.exitCode = 1;
} finally {
  await business?.end();
  await pool.end();
}

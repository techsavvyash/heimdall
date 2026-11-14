import { Pool } from "pg";
import { HeimdallError } from "@techsavvyash/heimdall-sdk";
import {
  createAuditDelivery,
  createIdentityCollector,
  deriveIdentityEventCoverage,
} from "@techsavvyash/heimdall-sdk/postgres";
import { exportPolicyPublications, validatePolicyPublication } from "./provider-settings.mjs";
import { validateInvoiceRead } from "./read-audit.mjs";

const stop = new AbortController();
process.once("SIGTERM", () => stop.abort());
process.once("SIGINT", () => stop.abort());
const signal = AbortSignal.any([stop.signal, AbortSignal.timeout(45_000)]);
const env = process.env;
let pool, business;
try {
  const { productId, environment, issuer } = JSON.parse(env.HEIMDALL_CONFIG ?? "null");
  if (productId !== "ledger" || !env.DATABASE_URL) throw new HeimdallError("invalid_argument");
  const tenantId = env.HEIMDALL_AUDIT_TENANT || undefined;
  const sinkId = env.HEIMDALL_AUDIT_SINK || "siem";
  const scope = { productId, environment };
  pool = new Pool({ connectionString: env.DATABASE_URL });
  const delivery = createAuditDelivery(pool, {
    ...scope,
    sinkId,
    tenantId,
    endpoint: env.HEIMDALL_AUDIT_ENDPOINT,
    token: env.HEIMDALL_AUDIT_TOKEN,
    transportProfile: env.HEIMDALL_AUDIT_TRANSPORT_PROFILE,
    validateCustom: (kind, version, attributes) =>
      validateInvoiceRead(kind, version, attributes) ||
      validatePolicyPublication(kind, version, attributes),
  });
  let collectionError;
  if (env.HEIMDALL_IDENTITY_MAPPING) {
    try {
      const { organizationId, startMicros, aggregates, coverage } = JSON.parse(
        env.HEIMDALL_IDENTITY_MAPPING,
      );
      if (!tenantId || typeof startMicros !== "string" || !/^[1-9][0-9]{0,17}$/.test(startMicros))
        throw new HeimdallError("invalid_argument");
      const collector = createIdentityCollector(pool, {
        ...scope,
        issuer,
        tenantId,
        organizationId,
        startMicros: BigInt(startMicros),
        aggregates,
        token: env.HEIMDALL_IDENTITY_READER_TOKEN,
        auditSinks: [sinkId],
      });
      const result = await collector.collect(deriveIdentityEventCoverage(coverage), signal);
      console.log(
        JSON.stringify({ collected: result.recorded, nextMicros: result.nextMicros.toString() }),
      );
    } catch (error) {
      collectionError =
        error instanceof HeimdallError ? error : new HeimdallError("invalid_argument");
    }
  }
  if (env.HEIMDALL_PROVIDER_SETTINGS) {
    if (!env.LEDGER_DATABASE_URL || sinkId !== "siem") throw new HeimdallError("invalid_argument");
    business = new Pool({
      connectionString: env.LEDGER_DATABASE_URL,
      statement_timeout: 5000,
      connectionTimeoutMillis: 5000,
    });
    await exportPolicyPublications(business, pool, scope, sinkId, tenantId, signal);
  }
  const result = await delivery.deliver(20, signal);
  const metrics = await delivery.metrics(signal);
  console.log(JSON.stringify({ ...result, metrics }));
  if (collectionError) throw collectionError;
} catch (error) {
  console.error(
    JSON.stringify({ auditJob: error instanceof HeimdallError ? error.code : "failed" }),
  );
  process.exitCode = 1;
} finally {
  await business?.end();
  await pool?.end();
}

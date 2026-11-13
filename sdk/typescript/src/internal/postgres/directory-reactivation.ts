import type { Pool } from "pg";
import {
  createDirectoryReader,
  type DirectoryReaderOptions,
} from "../../provisioning/directory-reader.js";
import { providerId } from "../../provisioning/provider-request.js";
import { HeimdallError } from "../errors.js";
import { IdentityRecords } from "./identity.js";
import { transaction } from "./query.js";
import { lockRetentionAdmission } from "./readiness.js";

export function createDirectoryReactivation(
  pool: Pool,
  options: DirectoryReaderOptions & { productId: string; environment: string },
) {
  const reader = createDirectoryReader(options);
  const { productId, environment, issuer, organizationId } = options;
  const records = IdentityRecords.retainedRecords(pool, productId, environment);
  return async function prepare(subject: string, revision: string, signal: AbortSignal) {
    if (
      !(signal instanceof AbortSignal) ||
      !providerId(subject) ||
      typeof revision !== "string" ||
      !/^[1-9][0-9]{0,18}(?![\s\S])/.test(revision) ||
      BigInt(revision) > 9223372036854775807n
    )
      throw new HeimdallError("invalid_argument");
    // Start before the provider reads so a concurrent deny cannot be mistaken for an older observation.
    const startedAt = Date.now();
    const observed = await reader.read(subject, signal);
    if (observed.state !== "active" || observed.externalId === undefined)
      throw new HeimdallError("forbidden");
    const identity = records.identity(issuer, subject);
    return transaction(
      pool,
      async (client) => {
        await lockRetentionAdmission(client);
        await client.query("SELECT pg_advisory_xact_lock($1::bigint)", [records.lock(identity)]);
        const result = await client.query<{ startedAt: string }>(
          `UPDATE heimdall_directory.deactivations d
         SET reactivation_started_at=COALESCE(d.reactivation_started_at,$8)
         WHERE d.product_id=$1 AND d.environment=$2 AND d.issuer=$3 AND d.organization_id=$4
           AND d.identity_hash=$5 AND d.subject=$6 AND d.revision=$7::bigint
           AND d.completed AND d.reason='inactive' AND d.observed_at<$8
           AND EXISTS(SELECT 1 FROM heimdall.identity_denials n WHERE n.product_id=$1
             AND n.environment=$2 AND n.identity_hash=$5 AND n.reason='inactive' AND n.observed_at<COALESCE(d.reactivation_started_at,$8))
         RETURNING d.reactivation_started_at::text AS "startedAt"`,
          [productId, environment, issuer, organizationId, identity, subject, revision, startedAt],
        );
        if (result.rowCount !== 1) throw new HeimdallError("conflict");
        return { subject, revision, startedAt: Number(result.rows[0]!.startedAt) };
      },
      signal,
    );
  };
}

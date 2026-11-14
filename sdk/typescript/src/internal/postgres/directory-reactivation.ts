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

export function validReactivationRevision(revision: string) {
  return (
    typeof revision === "string" &&
    /^[1-9][0-9]{0,18}(?![\s\S])/.test(revision) &&
    BigInt(revision) <= 9223372036854775807n
  );
}

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
      !validReactivationRevision(revision)
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

/** Release a prepared identity after the exact approved enrollment set is confirmed. */
export function createDirectoryReactivationRelease(
  pool: Pool,
  options: DirectoryReaderOptions & { productId: string; environment: string },
) {
  const reader = createDirectoryReader(options);
  const { productId, environment, issuer, organizationId } = options;
  const records = IdentityRecords.retainedRecords(pool, productId, environment);
  return async function release(
    input: {
      subject: string;
      revision: string;
      enrollments: readonly { workspaceId: string; ruleId: string }[];
    },
    signal: AbortSignal,
  ) {
    const { subject, revision } = input;
    if (
      !(signal instanceof AbortSignal) ||
      !providerId(subject) ||
      !validReactivationRevision(revision) ||
      !Array.isArray(input.enrollments) ||
      input.enrollments.length > 100 ||
      input.enrollments.some(
        (item) => !item || !providerId(item.workspaceId) || !providerId(item.ruleId),
      )
    )
      throw new HeimdallError("invalid_argument");
    const approvals = new Set(
      input.enrollments.map(({ workspaceId, ruleId }) => workspaceId + ":" + ruleId),
    );
    if (approvals.size !== input.enrollments.length) throw new HeimdallError("invalid_argument");
    const observed = await reader.read(subject, signal);
    if (observed.state !== "active" || observed.externalId === undefined)
      throw new HeimdallError("forbidden");
    const hash = records.identity(issuer, subject);
    const scope = [productId, environment, issuer, organizationId, subject, revision];
    await transaction(
      pool,
      async (client) => {
        await lockRetentionAdmission(client);
        await client.query("SELECT pg_advisory_xact_lock($1::bigint)", [records.lock(hash)]);
        const { rows } = await client.query<{
          workspaceId: string;
          ruleId: string;
          confirmed: boolean;
        }>(
          `SELECT e.workspace_id AS "workspaceId",e.rule_id AS "ruleId", EXISTS (
          SELECT 1 FROM heimdall_managed.operations o
          WHERE o.product_id=e.product_id AND o.environment=e.environment AND o.operation_id=e.operation_id
            AND o.target_hash=$7 AND o.workspace_id=e.workspace_id AND o.state='completed'
            AND o.kind IN ('add_member','assign_roles')) AS confirmed
        FROM heimdall_directory.enrollments e WHERE product_id=$1 AND environment=$2 AND issuer=$3
          AND organization_id=$4 AND subject=$5 AND reactivation_revision=$6::bigint LIMIT 101`,
          [...scope, hash],
        );
        if (
          rows.length !== approvals.size ||
          rows.some((row) => !row.confirmed || !approvals.has(row.workspaceId + ":" + row.ruleId))
        )
          throw new HeimdallError("conflict");
        const released = await client.query<{
          released: boolean;
          startedAt: string;
          denied: boolean;
        }>(
          `SELECT d.reactivation_released AS released,d.reactivation_started_at::text AS "startedAt",
          EXISTS(SELECT 1 FROM heimdall.identity_denials n WHERE n.product_id=$1
            AND n.environment=$2 AND n.identity_hash=$7) AS denied
        FROM heimdall_directory.deactivations d WHERE d.product_id=$1 AND d.environment=$2 AND d.issuer=$3
          AND d.organization_id=$4 AND d.subject=$5 AND d.revision=$6::bigint AND d.identity_hash=$7
          AND d.completed AND d.reason='inactive' AND d.reactivation_started_at IS NOT NULL`,
          [...scope, hash],
        );
        const record = released.rows[0];
        if (!record) throw new HeimdallError("conflict");
        if (record.released) {
          if (record.denied) throw new HeimdallError("conflict");
          return;
        }
        const denied = await client.query(
          `DELETE FROM heimdall.identity_denials WHERE product_id=$1 AND environment=$2 AND identity_hash=$3
        AND reason='inactive' AND observed_at<$4::bigint`,
          [productId, environment, hash, record.startedAt],
        );
        if (denied.rowCount !== 1) throw new HeimdallError("conflict");
        await client.query(
          "UPDATE heimdall_directory.deactivations SET reactivation_released=true WHERE product_id=$1 AND environment=$2 AND identity_hash=$3",
          [productId, environment, hash],
        );
      },
      signal,
    );
  };
}

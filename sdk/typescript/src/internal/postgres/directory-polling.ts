import type { Pool } from "pg";
import { HeimdallError, checkCancellation } from "../errors.js";
import {
  createDirectoryReader,
  type DirectoryReaderOptions,
} from "../../provisioning/directory-reader.js";
import { providerId } from "../../provisioning/provider-request.js";
import { createDirectoryDeactivation } from "./directory-deactivations.js";
import { query, transaction } from "./query.js";
import { lockRetentionAdmission } from "./readiness.js";

/** Compose explicit, bounded discovery and known-subject polling for a product directory. */
export function createDirectoryPolling(
  pool: Pool,
  options: DirectoryReaderOptions & { productId: string; environment: string },
) {
  const reader = createDirectoryReader(options);
  const observe = createDirectoryDeactivation(pool, options);
  const scope = [options.productId, options.environment, options.issuer, options.organizationId];
  const where = "product_id=$1 AND environment=$2 AND issuer=$3 AND organization_id=$4";
  function validate(limit: number, signal: AbortSignal) {
    if (!(signal instanceof AbortSignal) || !Number.isInteger(limit) || limit < 1 || limit > 50)
      throw new HeimdallError("invalid_argument");
    checkCancellation(signal);
  }
  return Object.freeze({
    async discover(limit: number, signal: AbortSignal) {
      validate(limit, signal);
      const checkpoint = await transaction(
        pool,
        async (client) => {
          await lockRetentionAdmission(client);
          await client.query(
            "INSERT INTO heimdall_directory.polling(product_id,environment,issuer,organization_id,start_index) VALUES($1,$2,$3,$4,1) ON CONFLICT DO NOTHING",
            scope,
          );
          const { rows } = await client.query<{ start: string; revision: string }>(
            "SELECT start_index::text AS start,revision::text FROM heimdall_directory.polling WHERE " +
              where,
            scope,
          );
          if (!rows[0]) throw new HeimdallError("unavailable");
          return rows[0];
        },
        signal,
      );
      const page = await reader.list(Number(checkpoint.start), limit, signal);
      const subjects = page.users
        .filter((user) => user.externalId !== undefined)
        .map((user) => user.id);
      await transaction(
        pool,
        async (client) => {
          await lockRetentionAdmission(client);
          const changed = await client.query(
            "UPDATE heimdall_directory.polling SET start_index=$5,revision=revision+1 WHERE " +
              where +
              " AND revision=$6::bigint",
            [...scope, page.nextStartIndex ?? 1, checkpoint.revision],
          );
          if (changed.rowCount !== 1) throw new HeimdallError("conflict");
          await client.query(
            "INSERT INTO heimdall_directory.subjects(product_id,environment,issuer,organization_id,subject) SELECT $1,$2,$3,$4,subject FROM unnest($5::text[]) AS subject ON CONFLICT DO NOTHING",
            [...scope, subjects],
          );
        },
        signal,
      );
      return { discovered: subjects.length, cycleComplete: page.nextStartIndex === null };
    },
    async observeKnown(after: string | null, limit: number, signal: AbortSignal) {
      validate(limit, signal);
      if (after !== null && !providerId(after)) throw new HeimdallError("invalid_argument");
      const rows = await query<{ subject: string }>(
        pool,
        "SELECT subject FROM heimdall_directory.subjects WHERE " +
          where +
          " AND subject>$5 ORDER BY subject LIMIT $6",
        [...scope, after ?? "", limit],
        false,
        signal,
      );
      let sessionsInvalidated = 0;
      for (const { subject } of rows) {
        sessionsInvalidated += (await observe(subject, signal)).sessionsInvalidated;
      }
      return {
        observed: rows.length,
        sessionsInvalidated,
        nextSubject: rows.length === limit ? rows.at(-1)!.subject : null,
      };
    },
  });
}

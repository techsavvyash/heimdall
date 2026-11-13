import { randomUUID } from "node:crypto";
import type { Pool, PoolClient } from "pg";
import { HeimdallError } from "../internal/errors.js";
import { query, transaction } from "../internal/postgres/query.js";

const checksum = "d17603a8fe7865f9a94eab614c5e4f5110ad6ab9634f1bff808a2f5e39fa5ff6";
const readiness = `SELECT NOT pg_is_in_recovery()
  AND current_setting('transaction_read_only')='off'
  AND (SELECT count(*) FROM heimdall_setup.schema_metadata)=1
  AND (SELECT count(*) FROM heimdall_setup.schema_metadata WHERE singleton AND database_version=1
    AND state='ready' AND reader_min=1 AND writer_min=1)=1
  AND (SELECT count(*) FROM heimdall_setup.migrations)=1
  AND (SELECT count(*) FROM heimdall_setup.migrations WHERE version=1 AND checksum=$1)=1 AS ready`;

type Scope = { productId: string; environment: string; fingerprint: string; stepCount: number };
type Step = {
  state: "claimed" | "unresolved" | "confirmed";
  dispatchId: string;
  resourceId: string | null;
};

export function setupJournal(pool: Pool, scope: Scope) {
  const { productId, environment, fingerprint, stepCount } = scope;
  async function mutate<T>(action: (client: PoolClient) => Promise<T>, signal: AbortSignal) {
    return transaction(
      pool,
      async (client) => {
        const result = await client.query<{ ready: boolean }>(readiness, [checksum]);
        if (!result.rows[0]?.ready) throw new HeimdallError("unavailable");
        return action(client);
      },
      signal,
    );
  }
  return {
    async ready(signal: AbortSignal) {
      const rows = await query<{ ready: boolean }>(pool, readiness, [checksum], false, signal);
      if (!rows[0]?.ready) throw new HeimdallError("unavailable");
    },
    async requireDispatch(ordinal: number, signal: AbortSignal) {
      const rows = await query<{ present: boolean }>(
        pool,
        "SELECT EXISTS (SELECT 1 FROM heimdall_setup.steps WHERE product_id=$1 AND environment=$2 AND ordinal=$3) AS present",
        [productId, environment, ordinal],
        false,
        signal,
      );
      if (!rows[0]?.present) throw new HeimdallError("conflict");
    },
    async claim(ordinal: number, signal: AbortSignal): Promise<Step> {
      const id = randomUUID();
      return mutate(async (client) => {
        const { rows } = await client.query<Step | { state: "conflict" }>(
          'SELECT state,dispatch_id::text AS "dispatchId",resource_id AS "resourceId" FROM heimdall_setup.claim($1,$2,$3,$4,$5,$6)',
          [productId, environment, fingerprint, stepCount, ordinal, id],
        );
        const row = rows[0];
        if (row?.state === "conflict") throw new HeimdallError("conflict");
        if (
          !row ||
          !["claimed", "unresolved", "confirmed"].includes(row.state) ||
          (row.state === "claimed" && row.dispatchId !== id)
        )
          throw new HeimdallError("unavailable");
        return row;
      }, signal);
    },
    async confirm(ordinal: number, dispatchId: string, resourceId: string, signal: AbortSignal) {
      await mutate(async (client) => {
        const { rows } = await client.query<{ confirmed: boolean }>(
          "SELECT heimdall_setup.confirm($1,$2,$3,$4,$5,$6) AS confirmed",
          [productId, environment, fingerprint, ordinal, dispatchId, resourceId],
        );
        if (!rows[0]?.confirmed) throw new HeimdallError("conflict");
      }, signal);
    },
  };
}

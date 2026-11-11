import type { Pool } from "pg";
import { HeimdallError } from "../errors.js";
import { transaction } from "./query.js";

// Explicit physical expiry maintenance; no keys, background timers or journal retention.
export class RecordMaintenance {
  constructor(
    private readonly pool: Pool,
    private readonly productId: string,
    private readonly environment: string,
    private readonly now: () => number = Date.now,
  ) {
    if (
      ![productId, environment].every(
        (s) => typeof s === "string" && /^[A-Za-z0-9_-]{1,64}(?![\s\S])/.test(s),
      )
    )
      throw new HeimdallError("invalid_argument");
  }
  async cleanupExpired(limit: number, signal?: AbortSignal): Promise<number> {
    const cutoff = this.now();
    if (
      !Number.isSafeInteger(cutoff) ||
      cutoff < 0 ||
      !Number.isInteger(limit) ||
      limit < 1 ||
      limit > 100
    )
      throw new HeimdallError("invalid_argument");
    return transaction(
      this.pool,
      async (client) => {
        const result = await client.query<{ deleted: number }>(
          `WITH expired AS (
 SELECT product_id,environment,kind,key_hash FROM heimdall.records
 WHERE product_id=$1 AND environment=$2 AND expires_at<=$3
 ORDER BY expires_at,kind,key_hash LIMIT $4 FOR UPDATE SKIP LOCKED
), removed AS (
 DELETE FROM heimdall.records r USING expired e
 WHERE r.product_id=e.product_id AND r.environment=e.environment
 AND r.kind=e.kind AND r.key_hash=e.key_hash AND r.expires_at<=$3 RETURNING 1
) SELECT count(*)::int AS deleted FROM removed`,
          [this.productId, this.environment, cutoff, limit],
        );
        const count = result.rows[0]?.deleted;
        if (!Number.isInteger(count) || count! < 0 || count! > limit)
          throw new HeimdallError("unavailable");
        return count!;
      },
      signal,
    );
  }
}

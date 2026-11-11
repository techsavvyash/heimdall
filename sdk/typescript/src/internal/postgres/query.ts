import type { Pool, PoolClient, QueryResultRow } from "pg";
import { HeimdallError, checkCancellation } from "../errors.js";
export async function query<Row extends QueryResultRow>(
  pool: Pool,
  text: string,
  values: unknown[],
  write: boolean,
  caller?: AbortSignal,
): Promise<Row[]> {
  const signal = caller
    ? AbortSignal.any([caller, AbortSignal.timeout(5000)])
    : AbortSignal.timeout(5000);
  checkCancellation(signal);
  let client: PoolClient | undefined;
  let released = false;
  let dispatched = false;
  const release = (destroy = false) => {
    if (client && !released) {
      released = true;
      client.release(destroy);
    }
  };
  let abort!: () => void;
  const cancelled = new Promise<never>((_, reject) => {
    abort = () => {
      release(true);
      try {
        checkCancellation(signal);
      } catch (error) {
        reject(dispatched && write ? new HeimdallError("outcome_unknown") : error);
      }
    };
    signal.addEventListener("abort", abort, { once: true });
  });
  try {
    client = await Promise.race([
      pool.connect().then((c) => {
        if (signal.aborted) {
          c.release(true);
          checkCancellation(signal);
        }
        return c;
      }),
      cancelled,
    ]);
    checkCancellation(signal);
    dispatched = true;
    const result = await Promise.race([client.query<Row>(text, values), cancelled]);
    checkCancellation(signal);
    return result.rows;
  } catch (error) {
    if (error instanceof HeimdallError) {
      if (dispatched && write && signal.aborted) throw new HeimdallError("outcome_unknown");
      throw error;
    }
    if (typeof error === "object" && error !== null && "code" in error && error.code === "23505")
      throw new HeimdallError("conflict");
    throw new HeimdallError(dispatched && write ? "outcome_unknown" : "unavailable");
  } finally {
    signal.removeEventListener("abort", abort);
    release();
  }
}

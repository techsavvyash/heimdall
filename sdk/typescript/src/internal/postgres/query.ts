import type { Pool, PoolClient, QueryResultRow } from "pg";
import { HeimdallError, checkCancellation } from "../errors.js";
async function withConnection<T>(
  pool: Pool,
  action: (client: PoolClient, markWrite: () => void) => Promise<T>,
  caller?: AbortSignal,
): Promise<T> {
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
  let rejectConnection!: (error: Error) => void;
  const failed = new Promise<never>((_, reject) => {
    rejectConnection = reject;
  });
  const onConnectionError = (error: Error) => {
    release(true);
    rejectConnection(error);
  };
  let abort!: () => void;
  const cancelled = new Promise<never>((_, reject) => {
    abort = () => {
      release(true);
      try {
        checkCancellation(signal);
      } catch (error) {
        reject(dispatched ? new HeimdallError("outcome_unknown") : error);
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
    client.on("error", onConnectionError);
    const result = await Promise.race([
      action(client, () => {
        dispatched = true;
      }),
      cancelled,
      failed,
    ]);
    checkCancellation(signal);
    return result;
  } catch (error) {
    if (error instanceof HeimdallError) {
      if (dispatched && signal.aborted) throw new HeimdallError("outcome_unknown");
      throw error;
    }
    if (typeof error === "object" && error !== null && "code" in error && error.code === "23505")
      throw new HeimdallError("conflict");
    throw new HeimdallError(dispatched ? "outcome_unknown" : "unavailable");
  } finally {
    signal.removeEventListener("abort", abort);
    release();
    client?.removeListener("error", onConnectionError);
  }
}

export function query<Row extends QueryResultRow>(
  pool: Pool,
  text: string,
  values: unknown[],
  write: boolean,
  caller?: AbortSignal,
): Promise<Row[]> {
  return withConnection(
    pool,
    async (client, markWrite) => {
      if (write) markWrite();
      return (await client.query<Row>(text, values)).rows;
    },
    caller,
  );
}
export function transaction<T>(
  pool: Pool,
  action: (client: PoolClient) => Promise<T>,
  caller?: AbortSignal,
): Promise<T> {
  return withConnection(
    pool,
    async (client, markWrite) => {
      await client.query("BEGIN ISOLATION LEVEL READ COMMITTED");
      try {
        await client.query("SET LOCAL synchronous_commit='on'");
        const result = await action(client);
        markWrite();
        await client.query("COMMIT");
        return result;
      } catch (error) {
        try {
          await client.query("ROLLBACK");
        } catch {
          /* Connection close also aborts an uncommitted transaction. */
        }
        throw error;
      }
    },
    caller,
  );
}

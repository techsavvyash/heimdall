import { checkCancellation, HeimdallError } from "./errors.js";
import { known } from "./audited-decision.js";
import type { ManagedOperation, OperationRecords } from "./postgres/operations.js";

export type ReconcileOptions = {
  signal: AbortSignal;
  maxOperations?: number;
  after?: string;
};
type Disposition = "completed" | "retryable" | "blocked";
export type ReconcileResult = {
  attempted: number;
  completed: number;
  retryable: number;
  blocked: number;
  next: string | null;
  operations: { operationId: string; disposition: Disposition; reason?: string }[];
};

export async function reconcileOperations(
  records: OperationRecords,
  options: ReconcileOptions,
  drive: (
    op: ManagedOperation,
    signal: AbortSignal,
  ) => Promise<{ status: string; reason?: string }>,
): Promise<ReconcileResult> {
  const max = options?.maxOperations ?? 100;
  if (
    !(options?.signal instanceof AbortSignal) ||
    !Number.isInteger(max) ||
    max < 1 ||
    max > 1000 ||
    (options.after !== undefined &&
      (typeof options.after !== "string" || !/^[A-Za-z0-9_-]{1,128}$/.test(options.after)))
  )
    throw new HeimdallError("invalid_argument");
  const signal = AbortSignal.any([options.signal, AbortSignal.timeout(30000)]);
  checkCancellation(signal);
  const result: ReconcileResult = {
    attempted: 0,
    completed: 0,
    retryable: 0,
    blocked: 0,
    next: options.after ?? null,
    operations: [],
  };
  do {
    const page = await records.scan(
      "reconcile",
      result.next,
      Date.now(),
      Math.min(100, max - result.operations.length),
      signal,
    );
    for (const op of page.items) {
      checkCancellation(signal);
      let disposition: Disposition = "blocked";
      let reason: string | undefined = "recovery_required";
      if (op.state !== "dispatching" && op.state !== "uncertain") {
        if (op.state !== "confirmed" && op.intent.kind !== "create_workspace") {
          reason = "initiator_required";
        } else {
          result.attempted++;
          try {
            const status = await drive(op, signal);
            reason = status.reason;
            disposition =
              status.status === "active" || status.status === "confirmed"
                ? "completed"
                : reason === "retryable"
                  ? "retryable"
                  : "blocked";
          } catch (error) {
            const code = known(error).code;
            reason = code;
            disposition = [
              "unavailable",
              "audit_unavailable",
              "deadline_exceeded",
              "conflict",
            ].includes(code)
              ? "retryable"
              : "blocked";
          }
        }
      }
      result[disposition]++;
      result.operations.push({
        operationId: op.operationId,
        disposition,
        ...(reason ? { reason } : {}),
      });
    }
    checkCancellation(signal);
    result.next = page.nextOperationId;
  } while (result.next && result.operations.length < max);
  return result;
}

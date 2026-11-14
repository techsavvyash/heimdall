export const errorCodes = [
  "unauthenticated",
  "forbidden",
  "invalid_argument",
  "not_found",
  "conflict",
  "unavailable",
  "deadline_exceeded",
  "cancelled",
  "audit_unavailable",
  "outcome_unknown",
] as const;
type ErrorCode = (typeof errorCodes)[number];

// Never retain provider exceptions, tokens or caller-provided message text.
export class HeimdallError extends Error {
  readonly code: ErrorCode;
  readonly operationId?: string;

  constructor(code: ErrorCode, operationId?: string) {
    const valid =
      errorCodes.includes(code) &&
      (operationId === undefined ||
        operationId === "" ||
        (operationId.length <= 128 && !/[^A-Za-z0-9_-]/.test(operationId)));
    super(valid ? code : "invalid_argument");
    this.name = "HeimdallError";
    this.code = valid ? code : "invalid_argument";
    if (valid && operationId !== undefined && operationId !== "") this.operationId = operationId;
  }

  toJSON(): { code: ErrorCode; operationId?: string } {
    return this.operationId === undefined
      ? { code: this.code }
      : { code: this.code, operationId: this.operationId };
  }
}

// Call only for a transport failure: a definitive service rejection has its own
// error mapping. Caller cancellation cannot prove a dispatched mutation stopped.
export function transportFailure(
  reason: "dependency" | "deadline" | "cancelled",
  mutationMayExecute: boolean,
  operationId?: string,
): HeimdallError {
  if (mutationMayExecute) return new HeimdallError("outcome_unknown", operationId);
  const code =
    reason === "dependency"
      ? "unavailable"
      : reason === "deadline"
        ? "deadline_exceeded"
        : "cancelled";
  return new HeimdallError(code, operationId);
}

export function checkCancellation(signal: AbortSignal): void {
  if (!signal.aborted) return;
  // AbortSignal.timeout uses TimeoutError. Arbitrary abort reasons are not copied.
  const deadline = signal.reason instanceof DOMException && signal.reason.name === "TimeoutError";
  throw new HeimdallError(deadline ? "deadline_exceeded" : "cancelled");
}

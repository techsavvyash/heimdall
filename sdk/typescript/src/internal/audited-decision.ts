import { randomUUID } from "node:crypto";
import { auditDigest, validateAuditReceipt } from "./audit-receipts.js";
import { validateAuditEvent } from "./audit.js";
import { HeimdallError, checkCancellation } from "./errors.js";

type Event = Record<string, unknown>;
type Receipt = Parameters<typeof validateAuditReceipt>[2];
type Requested = Event & {
  productId: string;
  environment: string;
  eventId: string;
  recordedAt: number;
};

export async function bounded<T>(
  action: (signal: AbortSignal) => Promise<T>,
  signal: AbortSignal,
): Promise<T> {
  checkCancellation(signal);
  let abort: (() => void) | undefined;
  try {
    return await Promise.race([
      Promise.resolve().then(() => {
        checkCancellation(signal);
        return action(signal);
      }),
      new Promise<never>((_resolve, reject) => {
        abort = () => {
          try {
            checkCancellation(signal);
          } catch (e) {
            reject(e);
          }
        };
        signal.addEventListener("abort", abort, { once: true });
        if (signal.aborted) abort();
      }),
    ]);
  } finally {
    if (abort) signal.removeEventListener("abort", abort);
  }
}
export function known(error: unknown): HeimdallError {
  return error instanceof HeimdallError
    ? new HeimdallError(error.code)
    : new HeimdallError("unavailable");
}
// Internal only: the caller must authenticate the actor and construct trusted request metadata.
export class AuditedDecision {
  constructor(
    private readonly productId: string,
    private readonly environment: string,
    private readonly append: (event: Event, signal: AbortSignal) => Promise<Receipt>,
    private readonly validate: (event: Event) => void,
    private readonly now: () => number = Date.now,
  ) {
    if (
      ![productId, environment].every(
        (v) => typeof v === "string" && /^[A-Za-z0-9_-]{1,64}$/.test(v),
      ) ||
      typeof append !== "function" ||
      typeof validate !== "function" ||
      typeof now !== "function"
    )
      throw new HeimdallError("invalid_argument");
  }
  private commit(event: Requested) {
    return commitAuditEvent(event, this.append, this.validate);
  }
  async run(
    input: Requested,
    evaluate: (signal: AbortSignal) => Promise<boolean>,
    signal?: AbortSignal,
  ): Promise<boolean> {
    const operation = AbortSignal.any([AbortSignal.timeout(5000), ...(signal ? [signal] : [])]);
    let request: Requested;
    try {
      request = structuredClone(input);
      this.validate(request);
      validateAuditEvent(
        request as unknown as Parameters<typeof validateAuditEvent>[0],
        "sdk",
        () => false,
      );
      auditDigest(request);
      if (
        request.productId !== this.productId ||
        request.environment !== this.environment ||
        request.eventType !== "heimdall.authorization.requested" ||
        request.outcome !== "requested" ||
        typeof evaluate !== "function"
      )
        throw new Error();
    } catch {
      throw new HeimdallError("invalid_argument");
    }
    request.eventId = randomUUID();
    request.decisionId = randomUUID();
    await this.commit(request);
    let result = false;
    let failure: HeimdallError | undefined;
    try {
      result = await bounded(evaluate, operation);
      if (typeof result !== "boolean") throw new HeimdallError("unavailable");
      checkCancellation(operation);
    } catch (error) {
      failure = known(error);
    }
    const outcome = failure
      ? failure.code === "cancelled"
        ? "cancelled"
        : failure.code === "invalid_argument"
          ? "invalid"
          : ["unauthenticated", "forbidden"].includes(failure.code)
            ? "deny"
            : "unavailable"
      : result
        ? "allow"
        : "deny";
    const recordedAt = Math.max(request.recordedAt, this.now());
    const completed: Requested = {
      ...request,
      eventId: randomUUID(),
      eventType: "heimdall.authorization.completed",
      outcome,
      occurredAt: recordedAt,
      recordedAt,
      ...(failure ? { reasonCode: failure.code } : {}),
    };
    // Never copy an arbitrary error message, evaluation return value, or stack into audit.
    await this.commit(completed);
    if (failure) throw failure;
    checkCancellation(operation);
    return result;
  }
}

export async function commitAuditEvent(
  event: Requested,
  append: (event: Event, signal: AbortSignal) => Promise<Receipt>,
  validate: (event: Event) => void,
) {
  try {
    validate(event);
    validateAuditEvent(
      event as unknown as Parameters<typeof validateAuditEvent>[0],
      "sdk",
      () => false,
    );
    const digest = auditDigest(event);
    // A separate bounded finalization budget permits durable cancellation outcomes.
    const receipt = await bounded(
      (signal) => append(structuredClone(event), signal),
      AbortSignal.timeout(5000),
    );
    validateAuditReceipt(event, digest, receipt);
  } catch {
    throw new HeimdallError("audit_unavailable");
  }
}

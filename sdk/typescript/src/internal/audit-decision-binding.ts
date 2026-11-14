import { validateAuditShape } from "./audit-shape.js";
import { validateAuditEvent } from "./audit.js";
import { auditDigest } from "./audit-receipts.js";
import { HeimdallError } from "./errors.js";

type DecisionBinding = {
  productId: string;
  environment: string;
  decisionId: string;
  requestEventId: string;
  requestedAt: number;
  resolution: "pending" | "completed" | "recovered";
  terminalEventId: string | null;
  resolvedAt: number | null;
};
const mutable = new Set([
  "eventId",
  "eventType",
  "outcome",
  "occurredAt",
  "recordedAt",
  "reasonCode",
  "sdkVersion",
]);
// Decoded, digest-verified history only. This does not scan storage or activate v2.
export function bindAuditDecision(
  productId: string,
  environment: string,
  events: readonly Record<string, unknown>[],
): DecisionBinding {
  if (
    ![productId, environment].every(
      (s) => typeof s === "string" && s.length > 0 && s.length <= 64 && !/[^A-Za-z0-9_-]/.test(s),
    )
  )
    throw new HeimdallError("invalid_argument");
  try {
    if (!Array.isArray(events) || events.length < 1 || events.length > 2) throw new Error();
    let request: Record<string, unknown> | undefined;
    let terminal: Record<string, unknown> | undefined;
    for (const event of events) {
      validateAuditShape(event);
      validateAuditEvent(
        event as unknown as Parameters<typeof validateAuditEvent>[0],
        "sdk",
        () => false,
      );
      auditDigest(event);
      if (
        event.productId !== productId ||
        event.environment !== environment ||
        event.source !== "sdk"
      )
        throw new Error();
      if (event.eventType === "heimdall.authorization.requested" && !request) request = event;
      else if (event.eventType === "heimdall.authorization.completed" && !terminal)
        terminal = event;
      else throw new Error();
    }
    if (!request) throw new Error();
    const context = (event: Record<string, unknown>) =>
      Object.fromEntries(Object.entries(event).filter(([key]) => !mutable.has(key)));
    if (
      terminal &&
      (terminal.eventId === request.eventId ||
        Number(terminal.recordedAt) < Number(request.recordedAt) ||
        Number(terminal.occurredAt) < Number(request.occurredAt) ||
        auditDigest(context(terminal)) !== auditDigest(context(request)))
    )
      throw new Error();
    return {
      productId,
      environment,
      decisionId: request.decisionId as string,
      requestEventId: request.eventId as string,
      requestedAt: request.recordedAt as number,
      resolution: terminal
        ? terminal.outcome === "outcome_unknown"
          ? "recovered"
          : "completed"
        : "pending",
      terminalEventId: (terminal?.eventId as string | undefined) ?? null,
      resolvedAt: (terminal?.recordedAt as number | undefined) ?? null,
    };
  } catch {
    throw new HeimdallError("audit_unavailable");
  }
}

import { createHash } from "node:crypto";
import { HeimdallError } from "./errors.js";
// Inputs have already passed full audit shape/semantic validation.
export function recoveryEventId(productId: string, environment: string, terminalId: string) {
  return (
    "recovery-" +
    createHash("sha256")
      .update(`heimdall.recovery.v1\0${productId}\0${environment}\0${terminalId}`)
      .digest("hex")
  );
}
export function validateRecoveryPair(
  terminal: Record<string, unknown>,
  administration: Record<string, unknown>,
  cutoff: number,
) {
  const target = administration.target as { type: string; id: string };
  const actor = administration.actor as { kind: string };
  if (
    !Number.isSafeInteger(cutoff) ||
    cutoff < 0 ||
    terminal.eventType !== "heimdall.authorization.completed" ||
    terminal.outcome !== "outcome_unknown" ||
    terminal.source !== "sdk" ||
    administration.source !== "sdk" ||
    administration.eventType !== "heimdall.recovery.completed" ||
    administration.outcome !== "succeeded" ||
    actor.kind === "unknown" ||
    administration.productId !== terminal.productId ||
    administration.environment !== terminal.environment ||
    administration.tenantId !== terminal.tenantId ||
    administration.decisionId !== terminal.decisionId ||
    target.type !== "authorization_decision" ||
    target.id !== terminal.decisionId ||
    administration.action !== "authorization.recover" ||
    terminal.recordedAt !== terminal.occurredAt ||
    administration.recordedAt !== terminal.recordedAt ||
    administration.occurredAt !== terminal.recordedAt ||
    (terminal.recordedAt as number) < cutoff ||
    administration.eventId === terminal.eventId ||
    administration.eventId !==
      recoveryEventId(
        terminal.productId as string,
        terminal.environment as string,
        terminal.eventId as string,
      )
  )
    throw new HeimdallError("invalid_argument");
}

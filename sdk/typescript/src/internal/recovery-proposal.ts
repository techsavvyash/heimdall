import { auditDigest } from "./audit-receipts.js";
import { bindAuditDecision } from "./audit-decision-binding.js";
import { recoveryEventId, validateRecoveryPair } from "./audit-recovery.js";
import { validateAuditShape } from "./audit-shape.js";
import { validateAuditEvent } from "./audit.js";
import { HeimdallError } from "./errors.js";

type RecoveryOptions = {
  terminalId: string;
  requestId: string;
  actor: Record<string, unknown>;
  at: number;
  cutoff: number;
  sdkVersion: string;
};
export type RecoveryProposal = {
  formatVersion: 1;
  cutoff: number;
  requestDigest: string;
  terminal: Record<string, unknown>;
  administration: Record<string, unknown>;
};
// Explicit time/IDs make journal retries independent of clocks and randomness.
export function buildRecoveryProposal(
  request: Record<string, unknown>,
  options: RecoveryOptions,
): RecoveryProposal {
  const requestDigest = auditDigest(request);
  const original = JSON.parse(JSON.stringify(request)) as Record<string, unknown>;
  bindAuditDecision(original.productId as string, original.environment as string, [original]);
  if (
    !options ||
    !Number.isSafeInteger(options.cutoff) ||
    options.cutoff < 0 ||
    !Number.isSafeInteger(options.at) ||
    options.at < options.cutoff
  )
    throw new HeimdallError("invalid_argument");
  const terminal = {
    ...original,
    eventId: options.terminalId,
    eventType: "heimdall.authorization.completed",
    outcome: "outcome_unknown",
    occurredAt: options.at,
    recordedAt: options.at,
    sdkVersion: options.sdkVersion,
  };
  delete (terminal as Record<string, unknown>).reasonCode;
  const administration: Record<string, unknown> = {
    schemaVersion: 1,
    eventVersion: 1,
    eventId: recoveryEventId(
      original.productId as string,
      original.environment as string,
      options.terminalId,
    ),
    eventType: "heimdall.recovery.completed",
    productId: original.productId,
    environment: original.environment,
    actor: options.actor,
    target: { type: "authorization_decision", id: original.decisionId },
    action: "authorization.recover",
    outcome: "succeeded",
    occurredAt: options.at,
    recordedAt: options.at,
    requestId: options.requestId,
    decisionId: original.decisionId,
    source: "sdk",
    sdkVersion: options.sdkVersion,
    attributes: {},
  };
  if (Object.hasOwn(original, "tenantId")) administration.tenantId = original.tenantId;
  const proposal: RecoveryProposal = {
    formatVersion: 1,
    cutoff: options.cutoff,
    requestDigest,
    terminal,
    administration,
  };
  validateRecoveryProposal(original, proposal);
  return JSON.parse(JSON.stringify(proposal)) as RecoveryProposal;
}
// Verify persisted proposals against the original decoded, digest-verified request.
export function validateRecoveryProposal(
  request: Record<string, unknown>,
  proposal: RecoveryProposal,
): void {
  if (
    !proposal ||
    Object.keys(proposal).sort().join(",") !==
      "administration,cutoff,formatVersion,requestDigest,terminal" ||
    proposal.formatVersion !== 1 ||
    proposal.requestDigest !== auditDigest(request)
  )
    throw new HeimdallError("invalid_argument");
  for (const event of [proposal.terminal, proposal.administration]) {
    validateAuditShape(event);
    validateAuditEvent(event as Parameters<typeof validateAuditEvent>[0], "sdk", () => false);
    auditDigest(event);
  }
  const binding = bindAuditDecision(request.productId as string, request.environment as string, [
    request,
    proposal.terminal,
  ]);
  validateRecoveryPair(proposal.terminal, proposal.administration, proposal.cutoff);
  if (binding.requestedAt > proposal.cutoff) throw new HeimdallError("invalid_argument");
}

export function decodeRecoveryProposal(
  request: Record<string, unknown>,
  raw: string,
): RecoveryProposal {
  if (typeof raw !== "string" || Buffer.byteLength(raw) > 4500000)
    throw new HeimdallError("invalid_argument");
  let proposal: RecoveryProposal;
  try {
    proposal = JSON.parse(raw) as RecoveryProposal;
  } catch {
    throw new HeimdallError("invalid_argument");
  }
  validateRecoveryProposal(request, proposal);
  return proposal;
}

// Journal idempotency compares this fingerprint; it is not an authorization token.
export function recoveryProposalDigest(
  request: Record<string, unknown>,
  proposal: RecoveryProposal,
): string {
  validateRecoveryProposal(request, proposal);
  return auditDigest({
    kind: "recovery_proposal",
    formatVersion: 1,
    cutoff: proposal.cutoff,
    requestDigest: proposal.requestDigest,
    terminalDigest: auditDigest(proposal.terminal),
    administrationDigest: auditDigest(proposal.administration),
  });
}

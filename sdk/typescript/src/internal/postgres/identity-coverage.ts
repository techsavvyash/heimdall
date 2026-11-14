import { HeimdallError } from "../errors.js";

export type IdentityEventCoverage = {
  retainedFromMicros: bigint;
  visibleBeforeMicros: bigint;
  validUntilMs: number;
};

export type IdentityCoveragePolicy = {
  maxWriterTransactionMs: number;
  maxClockOffsetMs: number;
  minimumRetentionMs: number;
};

/** Derive bounds from operator-verified limits for the pinned provider deployment. */
export function deriveIdentityEventCoverage(
  policy: IdentityCoveragePolicy,
  nowMs = Date.now(),
): IdentityEventCoverage {
  const { maxWriterTransactionMs, maxClockOffsetMs, minimumRetentionMs } = policy;
  const lifetimeMs = 30000;
  if (
    ![maxWriterTransactionMs, maxClockOffsetMs, minimumRetentionMs, nowMs].every(
      Number.isSafeInteger,
    ) ||
    maxWriterTransactionMs < 1 ||
    maxClockOffsetMs < 0 ||
    minimumRetentionMs < 1 ||
    nowMs < 1 ||
    nowMs > 253402300799999 - lifetimeMs
  )
    throw new HeimdallError("invalid_argument");

  const now = BigInt(nowMs);
  const writer = BigInt(maxWriterTransactionMs);
  const skew = BigInt(maxClockOffsetMs);
  const visibleBefore = now - writer - 3n * skew;
  const retentionFloor = now + BigInt(lifetimeMs) + skew - BigInt(minimumRetentionMs);
  if (visibleBefore <= 0n || visibleBefore <= retentionFloor)
    throw new HeimdallError("unavailable");

  return {
    retainedFromMicros: (retentionFloor > 0n ? retentionFloor : 0n) * 1000n,
    visibleBeforeMicros: visibleBefore * 1000n,
    validUntilMs: nowMs + lifetimeMs,
  };
}

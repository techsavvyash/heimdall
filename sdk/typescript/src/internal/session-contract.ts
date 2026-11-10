import type { validateEnterpriseEvidence } from "./enterprise-evidence.js";
import { HeimdallError } from "./errors.js";

type Scope = {
  productId: string;
  environment: string;
  issuer: string;
  subject: string;
  policyVersion: string;
};
type Record = Scope & {
  enterpriseEvidence?: NonNullable<Parameters<typeof validateEnterpriseEvidence>[0]>;
  createdAt: number;
  lastActivityAt: number;
  absoluteExpiresAt: number;
  idleExpiresAt: number;
  accessTokenExpiresAt: number;
  identityValidatedAt: number;
  state: "active" | "refreshing" | "reauth_required";
  refreshToken?: string;
  refreshAttempt?: { id: string; expiresAt: number };
};

// Schema-validated, decrypted records only. A status is not verified authentication.
export function sessionStatus(
  record: Record,
  scope: Scope,
  now: number,
  freshnessMs: number,
):
  | "active"
  | "refreshing"
  | "reauth_required"
  | "refresh_required"
  | "identity_revalidation_required" {
  if (
    !Number.isSafeInteger(freshnessMs) ||
    freshnessMs < 0 ||
    freshnessMs > 60000 ||
    !Number.isSafeInteger(now) ||
    now < 0 ||
    record.createdAt > record.lastActivityAt ||
    record.lastActivityAt > now ||
    record.lastActivityAt >= record.idleExpiresAt ||
    record.idleExpiresAt > record.absoluteExpiresAt ||
    record.identityValidatedAt < record.createdAt ||
    record.identityValidatedAt > now ||
    (record.refreshAttempt !== undefined &&
      (record.refreshAttempt.expiresAt > record.absoluteExpiresAt ||
        record.refreshAttempt.expiresAt <= record.createdAt))
  ) {
    throw new HeimdallError("invalid_argument");
  }
  if (
    record.productId !== scope.productId ||
    record.environment !== scope.environment ||
    record.issuer !== scope.issuer ||
    record.subject !== scope.subject ||
    record.policyVersion !== scope.policyVersion ||
    now >= record.absoluteExpiresAt ||
    now >= record.idleExpiresAt
  ) {
    throw new HeimdallError("unauthenticated");
  }
  if (record.state === "reauth_required") return "reauth_required";
  if (record.state === "refreshing") {
    return now >= record.refreshAttempt!.expiresAt ? "reauth_required" : "refreshing";
  }
  if (now >= record.accessTokenExpiresAt)
    return record.refreshToken ? "refresh_required" : "reauth_required";
  if (now - record.identityValidatedAt >= freshnessMs) return "identity_revalidation_required";
  return "active";
}

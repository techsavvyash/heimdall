import { HeimdallError } from "./errors.js";

type Scope = {
  productId: string;
  environment: string;
  issuer: string;
  subject: string;
  clientId: string;
  tenantId: string;
  organizationId: string;
  policyVersion: string;
  providerId: string;
  protocol: "saml" | "oidc";
};
type Evidence = Scope & {
  upstreamSubject: string;
  providerSessionId: string;
  authTime: number;
  verifiedAt: number;
};

// Schema-validated, authenticated stored evidence only. Does not verify a provider or token.
// Scope and maxAgeMs come from the current trusted target policy, never the browser.
export function validateEnterpriseEvidence(
  evidence: Evidence | null,
  scope: Scope,
  now: number,
  maxAgeMs: number,
): void {
  if (!Number.isSafeInteger(now) || now < 0 || !Number.isSafeInteger(maxAgeMs) || maxAgeMs <= 0)
    throw new HeimdallError("invalid_argument");
  if (evidence === null) throw new HeimdallError("unauthenticated");
  if (
    evidence.verifiedAt > now ||
    evidence.authTime !== Math.floor(evidence.verifiedAt / 1000) * 1000
  )
    throw new HeimdallError("invalid_argument");
  if (
    evidence.productId !== scope.productId ||
    evidence.environment !== scope.environment ||
    evidence.issuer !== scope.issuer ||
    evidence.subject !== scope.subject ||
    evidence.clientId !== scope.clientId ||
    evidence.tenantId !== scope.tenantId ||
    evidence.organizationId !== scope.organizationId ||
    evidence.policyVersion !== scope.policyVersion ||
    evidence.providerId !== scope.providerId ||
    evidence.protocol !== scope.protocol ||
    now - evidence.authTime >= maxAgeMs
  )
    throw new HeimdallError("unauthenticated");
}

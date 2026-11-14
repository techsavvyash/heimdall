import { HeimdallError } from "./errors.js";

export type EnterpriseScope = {
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
export type EnterpriseEvidence = EnterpriseScope & {
  upstreamSubject: string;
  providerSessionId: string;
  authTime: number;
  verifiedAt: number;
};

// Schema-validated, authenticated stored evidence only. Does not verify a provider or token.
// Scope and maxAgeMs come from the current trusted target policy, never the browser.
export function validateEnterpriseEvidence(
  evidence: EnterpriseEvidence | null,
  scope: EnterpriseScope,
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

export type TenantSSOPolicy = Pick<
  EnterpriseScope,
  "organizationId" | "providerId" | "policyVersion" | "protocol"
> & {
  maxAgeMs: number;
};

export function readEnterpriseEvidence(value: unknown): EnterpriseEvidence {
  if (typeof value !== "object" || !value || Array.isArray(value))
    throw new HeimdallError("unavailable");
  const e = value as Record<string, unknown>;
  const fields = {
    productId: 64,
    environment: 64,
    issuer: 2048,
    subject: 1024,
    clientId: 128,
    tenantId: 128,
    organizationId: 128,
    policyVersion: 128,
    providerId: 128,
    upstreamSubject: 1024,
    providerSessionId: 128,
  };
  if (
    Object.keys(e).length !== Object.keys(fields).length + 4 ||
    e.schemaVersion !== 1 ||
    (e.protocol !== "saml" && e.protocol !== "oidc") ||
    ![e.authTime, e.verifiedAt].every(
      (v) => typeof v === "number" && Number.isSafeInteger(v) && v >= 0,
    ) ||
    !Object.entries(fields).every(([key, max]) => {
      const v = e[key];
      return typeof v === "string" && v.isWellFormed() && v.length > 0 && [...v].length <= max;
    })
  )
    throw new HeimdallError("unavailable");
  return Object.freeze({ ...e }) as EnterpriseEvidence;
}

import { timingSafeEqual } from "node:crypto";
import { HeimdallError } from "./errors.js";
type Policy = {
  tenantId: string;
  organizationId: string;
  policyVersion: string;
  strictSSO: boolean;
  providerId?: string;
  protocol?: "saml" | "oidc";
};
type Scope = {
  productId: string;
  environment: string;
  issuer: string;
  clientId: string;
  redirectUri: string;
  returnToKey: string;
  browserBindingDigest: string;
  tenantPolicy: Policy | null;
};
type Login = Scope & {
  createdAt: number;
  expiresAt: number;
  state: "pending" | "claimed";
  claimedAt?: number;
};

// Decrypted/schema-validated input only. This does not atomically claim a callback.
export function validateLoginClaim(record: Login, scope: Scope, now: number): void {
  if (
    !Number.isSafeInteger(now) ||
    now < 0 ||
    record.createdAt > now ||
    record.expiresAt <= record.createdAt ||
    record.expiresAt - record.createdAt > 600000 ||
    (record.claimedAt !== undefined &&
      (record.claimedAt < record.createdAt || record.claimedAt > now))
  )
    throw new HeimdallError("invalid_argument");
  const policy = record.tenantPolicy;
  const expected = scope.tenantPolicy;
  const policyMatches =
    policy === null
      ? expected === null
      : expected !== null &&
        policy.tenantId === expected.tenantId &&
        policy.organizationId === expected.organizationId &&
        policy.policyVersion === expected.policyVersion &&
        policy.strictSSO === expected.strictSSO &&
        policy.providerId === expected.providerId &&
        policy.protocol === expected.protocol;
  const binding = Buffer.from(record.browserBindingDigest, "utf8");
  const supplied = Buffer.from(scope.browserBindingDigest, "utf8");
  if (
    record.state !== "pending" ||
    now >= record.expiresAt ||
    record.productId !== scope.productId ||
    record.environment !== scope.environment ||
    record.issuer !== scope.issuer ||
    record.clientId !== scope.clientId ||
    record.redirectUri !== scope.redirectUri ||
    record.returnToKey !== scope.returnToKey ||
    !policyMatches ||
    binding.length !== supplied.length ||
    !timingSafeEqual(binding, supplied)
  )
    throw new HeimdallError("unauthenticated");
}

import { maintenanceTargetType } from "./audit.js";
import type { MaintenanceContext } from "./audited-authentication.js";
import { HeimdallError, checkCancellation } from "./errors.js";
import { encodeIdentity } from "./identity-encoding.js";

type Grant = {
  issuer: string;
  subject: string;
  action: string;
  tenantId: string | null;
};
type OperatorPolicy = (scope: MaintenanceContext, signal: AbortSignal) => Promise<boolean>;
const token = (value: unknown, max: number): value is string =>
  typeof value === "string" &&
  value.length > 0 &&
  value.length <= max &&
  !/[^A-Za-z0-9_-]/.test(value);

export function createOperatorPolicy(
  config: {
    productId: string;
    environment: string;
    policyVersion: string;
    grants: readonly Grant[];
  },
  retryEnded?: OperatorPolicy,
): OperatorPolicy {
  if (
    !config ||
    !token(config.productId, 64) ||
    !token(config.environment, 64) ||
    !token(config.policyVersion, 128) ||
    !Array.isArray(config.grants)
  )
    throw new HeimdallError("invalid_argument");
  const { productId, environment, policyVersion } = config;
  const grants = new Set<string>();
  for (const grant of config.grants) {
    if (
      !grant ||
      !maintenanceTargetType(grant.action) ||
      (grant.tenantId !== null && !token(grant.tenantId, 128)) ||
      (grant.action === "managed.export.operations" && grant.tenantId === null) ||
      (grant.action === "audit.hold.retry.release" && typeof retryEnded !== "function")
    )
      throw new HeimdallError("invalid_argument");
    encodeIdentity(grant.issuer, grant.subject);
    const key = JSON.stringify([grant.issuer, grant.subject, grant.action, grant.tenantId]);
    if (grants.has(key)) throw new HeimdallError("invalid_argument");
    grants.add(key);
  }
  return async (scope, signal) => {
    checkCancellation(signal);
    if (
      scope.productId !== productId ||
      scope.environment !== environment ||
      scope.policyVersion !== policyVersion
    )
      return false;
    if (
      !grants.has(
        JSON.stringify([scope.actor.issuer, scope.actor.subject, scope.action, scope.tenantId]),
      )
    )
      return false;
    if (scope.action === "audit.hold.retry.release") return retryEnded!(scope, signal);
    return true;
  };
}

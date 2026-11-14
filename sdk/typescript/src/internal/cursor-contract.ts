import { HeimdallError } from "./errors.js";
type Scope = { productId: string; environment: string; principal: string; modelId: string };
type Payload = Scope & { issuedAt: number; expiresAt: number; position: string };

// Invoke after signature and structural verification, never on raw client JSON.
export function validateCursorScope(payload: Payload, scope: Scope, now: number): void {
  if (
    !Number.isSafeInteger(now) ||
    now < 0 ||
    payload.issuedAt > now ||
    payload.expiresAt <= payload.issuedAt ||
    now >= payload.expiresAt ||
    payload.productId !== scope.productId ||
    payload.environment !== scope.environment ||
    payload.principal !== scope.principal ||
    payload.modelId !== scope.modelId
  ) {
    throw new HeimdallError("invalid_argument");
  }
}

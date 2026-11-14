import { providerRequest } from "./provider-request.js";
export { providerId } from "./provider-request.js";

export function zitadel(
  issuer: string,
  token: string,
  profile: "production" | "loopback",
  signal: AbortSignal,
) {
  return providerRequest(issuer, token, profile, signal, { "connect-protocol-version": "1" });
}

import { validateEndpoint } from "./endpoints.js";
import { checkCancellation, HeimdallError } from "./errors.js";

type Profile = "production" | "loopback";
const endpoints = [
  "jwks_uri",
  "authorization_endpoint",
  "token_endpoint",
  "introspection_endpoint",
  "revocation_endpoint",
] as const;
const capabilities: Record<string, string[]> = {
  response_types_supported: ["code"],
  grant_types_supported: ["authorization_code", "refresh_token"],
  id_token_signing_alg_values_supported: ["RS256"],
  code_challenge_methods_supported: ["S256"],
  token_endpoint_auth_methods_supported: ["client_secret_basic"],
  introspection_endpoint_auth_methods_supported: ["client_secret_basic"],
  revocation_endpoint_auth_methods_supported: ["client_secret_basic"],
};
type Metadata = Readonly<{ issuer: string } & Record<(typeof endpoints)[number], string>>;

// Only selected endpoints escape discovery; advertised algorithms cannot widen trust.
export function validateOIDCMetadata(value: unknown, issuer: string, profile: Profile): Metadata {
  validateEndpoint(issuer, profile);
  try {
    if (!value || typeof value !== "object" || Array.isArray(value)) throw new Error();
    const data = value as Record<string, unknown>;
    if (data.issuer !== issuer) throw new Error();
    const result = { issuer } as { issuer: string } & Record<(typeof endpoints)[number], string>;
    for (const key of endpoints) {
      const endpoint = data[key];
      if (typeof endpoint !== "string") throw new Error();
      validateEndpoint(endpoint, profile);
      if (new URL(endpoint).origin !== new URL(issuer).origin) throw new Error();
      result[key] = endpoint;
    }
    for (const [key, required] of Object.entries(capabilities)) {
      const values = data[key];
      if (
        !Array.isArray(values) ||
        values.some((v) => typeof v !== "string") ||
        required.some((v) => !values.includes(v))
      )
        throw new Error();
    }
    return Object.freeze(result);
  } catch {
    throw new HeimdallError("unavailable");
  }
}

export async function discoverOIDC(
  issuer: string,
  profile: Profile,
  caller?: AbortSignal,
): Promise<Metadata> {
  validateEndpoint(issuer, profile);
  const signal = AbortSignal.any([AbortSignal.timeout(5000), ...(caller ? [caller] : [])]);
  checkCancellation(signal);
  try {
    const response = await fetch(issuer.replace(/\/$/, "") + "/.well-known/openid-configuration", {
      signal,
      redirect: "error",
      headers: { accept: "application/json" },
    });
    if (response.status !== 200 || !response.body) {
      await response.body?.cancel();
      throw new Error();
    }
    const reader = response.body.getReader();
    const parts: Uint8Array[] = [];
    let size = 0;
    try {
      for (;;) {
        const { done, value } = await reader.read();
        if (done) break;
        size += value.length;
        if (size > 65536) throw new Error();
        parts.push(value);
      }
    } finally {
      await reader.cancel();
    }
    const raw = new TextDecoder("utf-8", { fatal: true, ignoreBOM: true }).decode(
      Buffer.concat(parts),
    );
    const result = validateOIDCMetadata(
      JSON.parse(raw, (key, value: unknown) => {
        if (!key.isWellFormed() || (typeof value === "string" && !value.isWellFormed()))
          throw new Error();
        return value;
      }),
      issuer,
      profile,
    );
    checkCancellation(signal);
    return result;
  } catch {
    checkCancellation(signal);
    throw new HeimdallError("unavailable");
  }
}

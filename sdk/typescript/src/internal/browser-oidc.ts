import { validateEndpoint } from "./endpoints.js";
import { checkCancellation, HeimdallError } from "./errors.js";
import { discoverOIDC } from "./oidc-discovery.js";
import { TokenVerifier } from "./token-verifier.js";

type Config = {
  issuer: string;
  profile: "production" | "loopback";
  clientId: string;
  audience: string;
  zitadelAPIAudience?: string;
  clientSecret: string;
  redirectUri: string;
};
type Claim = { nonce: string; pkceVerifier: string };
const visible = (value: unknown, max: number): value is string =>
  typeof value === "string" &&
  value.length > 0 &&
  value.length <= max &&
  !/[^\x21-\x7e]/.test(value);
const formEncode = (value: string) => new URLSearchParams([["v", value]]).toString().slice(2);

export async function createBrowserOIDC(config: Config, signal?: AbortSignal) {
  const { issuer, profile, clientId, clientSecret, redirectUri, audience, zitadelAPIAudience } =
    config;
  validateEndpoint(redirectUri, profile);
  if (
    !visible(clientId, 128) ||
    !visible(clientSecret, 4096) ||
    (zitadelAPIAudience !== undefined && !visible(zitadelAPIAudience, 128))
  )
    throw new HeimdallError("invalid_argument");
  const metadata = await discoverOIDC(issuer, profile, signal);
  const verifier = new TokenVerifier({ issuer, profile, audience, jwksUri: metadata.jwks_uri });
  const authorization =
    "Basic " +
    Buffer.from(formEncode(clientId) + ":" + formEncode(clientSecret)).toString("base64");

  return {
    authorizationURL(state: string, nonce: string, pkceChallenge: string) {
      const url = new URL(metadata.authorization_endpoint);
      url.search = new URLSearchParams({
        client_id: clientId,
        redirect_uri: redirectUri,
        response_type: "code",
        scope: `openid profile offline_access urn:zitadel:iam:org:project:id:${audience}:aud${zitadelAPIAudience === undefined ? "" : " urn:zitadel:iam:org:project:id:zitadel:aud"}`,
        state,
        nonce,
        code_challenge: pkceChallenge,
        code_challenge_method: "S256",
      }).toString();
      return url.href;
    },
    // Claim is supplied by the callback so no code is dispatched before a confirmed claim.
    async exchange(code: string, claim: () => Promise<Claim>, caller?: AbortSignal) {
      if (caller) checkCancellation(caller);
      if (!visible(code, 4096)) throw new HeimdallError("unauthenticated");
      const { nonce, pkceVerifier } = await claim();
      if (caller) checkCancellation(caller);
      const signal = AbortSignal.any([AbortSignal.timeout(5000), ...(caller ? [caller] : [])]);
      const body = new URLSearchParams({
        grant_type: "authorization_code",
        code,
        redirect_uri: redirectUri,
        code_verifier: pkceVerifier,
      });
      let response: Response;
      let raw: string;
      try {
        response = await fetch(metadata.token_endpoint, {
          method: "POST",
          headers: { authorization, accept: "application/json" },
          redirect: "error",
          body,
          signal,
        });
        if (![200, 400].includes(response.status) || !response.body) {
          await response.body?.cancel();
          throw new Error();
        }
        const reader = response.body.getReader();
        const parts: Uint8Array[] = [];
        let length = 0;
        try {
          for (;;) {
            const { done, value } = await reader.read();
            if (done) break;
            length += value.length;
            if (length > 65536) throw new Error();
            parts.push(value);
          }
        } finally {
          await reader.cancel();
        }
        raw = new TextDecoder("utf-8", { fatal: true, ignoreBOM: true }).decode(
          Buffer.concat(parts),
        );
      } catch {
        checkCancellation(signal);
        throw new HeimdallError("unavailable");
      }
      checkCancellation(signal);
      let payload: Record<string, unknown>;
      try {
        const value: unknown = JSON.parse(raw, (key, value: unknown) => {
          if (!key.isWellFormed() || (typeof value === "string" && !value.isWellFormed()))
            throw new Error();
          return value;
        });
        if (!value || typeof value !== "object" || Array.isArray(value)) throw new Error();
        payload = value as Record<string, unknown>;
      } catch {
        throw new HeimdallError("unavailable");
      }
      if (response.status === 400 && payload.error === "invalid_grant")
        throw new HeimdallError("unauthenticated");
      if (
        response.status !== 200 ||
        payload.error !== undefined ||
        typeof payload.token_type !== "string" ||
        payload.token_type.toLowerCase() !== "bearer" ||
        !visible(payload.access_token, 16384) ||
        !visible(payload.id_token, 16384) ||
        (payload.refresh_token !== undefined && !visible(payload.refresh_token, 16384))
      )
        throw new HeimdallError("unavailable");
      const identity = await verifier.verifyIDToken(
        payload.id_token,
        clientId,
        nonce,
        payload.access_token,
        caller,
        zitadelAPIAudience,
      );
      const access = await verifier.verify(payload.access_token, caller);
      if (identity.subject !== access.subject) throw new HeimdallError("unauthenticated");
      return {
        identity,
        accessExpiresAt: access.expiresAt,
        accessToken: payload.access_token,
        refreshToken: payload.refresh_token,
      };
    },
  };
}

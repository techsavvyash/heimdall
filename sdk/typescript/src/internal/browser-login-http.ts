import { createBrowserOIDC } from "./browser-oidc.js";
import { checkCancellation, HeimdallError } from "./errors.js";
import type { BrowserLoginRecords } from "./postgres/login.js";

type OIDCConfig = Parameters<typeof createBrowserOIDC>[0];
type Scope = { productId: string; environment: string; returnToKey: string };
const scopeToken = (value: unknown, max = 64) =>
  typeof value === "string" &&
  value.length > 0 &&
  value.length <= max &&
  !/[^A-Za-z0-9_-]/.test(value);
const stateToken = (value: string) => /^[A-Za-z0-9_-]{43}(?![\s\S])/.test(value);

export async function createBrowserLoginHTTP(
  config: OIDCConfig & Scope,
  records: Pick<BrowserLoginRecords, "start" | "claim">,
  signal?: AbortSignal,
) {
  const { productId, environment, returnToKey, issuer, clientId, redirectUri } = config;
  if (
    ![productId, environment, returnToKey].every((value) => scopeToken(value)) ||
    !scopeToken(config.audience, 128)
  )
    throw new HeimdallError("invalid_argument");
  const oidc = await createBrowserOIDC(config, signal);
  const callback = new URL(redirectUri);
  const secure = callback.protocol === "https:";
  const prefix = `${secure ? "__Host-" : ""}heimdall-${productId.length}-${productId}-${environment}-login-`;
  const attributes = `Path=/; HttpOnly; SameSite=Lax${secure ? "; Secure" : ""}`;
  const target = {
    productId,
    environment,
    issuer,
    clientId,
    redirectUri,
    returnToKey,
    tenantPolicy: null,
  };

  return {
    scope: Object.freeze({ productId, environment, returnToKey, secure }),
    async start(request: Request) {
      if (request.method !== "GET" || new URL(request.url).origin !== callback.origin)
        throw new HeimdallError("invalid_argument");
      const login = await records.start(target, request.signal);
      return new Response(null, {
        status: 303,
        headers: {
          location: oidc.authorizationURL(login.state, login.nonce, login.pkceChallenge),
          "set-cookie": `${prefix}${login.state}=${login.browserBinding}; Max-Age=600; ${attributes}`,
          "cache-control": "no-store",
          "referrer-policy": "no-referrer",
        },
      });
    },

    async callback(request: Request) {
      checkCancellation(request.signal);
      const url = new URL(request.url);
      if (
        request.method !== "GET" ||
        url.origin !== callback.origin ||
        url.pathname !== callback.pathname ||
        url.search.length > 16384 ||
        /;|%(?![\da-f]{2})/i.test(url.search)
      )
        throw new HeimdallError("unauthenticated");
      const state = url.searchParams.getAll("state");
      const code = url.searchParams.getAll("code");
      const responseIssuer = url.searchParams.getAll("iss");
      if (
        state.length !== 1 ||
        !stateToken(state[0]!) ||
        code.length !== 1 ||
        url.searchParams.has("error") ||
        responseIssuer.length > 1 ||
        (responseIssuer.length === 1 && responseIssuer[0] !== issuer)
      )
        throw new HeimdallError("unauthenticated");
      const name = prefix + state[0];
      const cookies = request.headers.get("cookie") ?? "";
      if (cookies.length > 16384) throw new HeimdallError("unauthenticated");
      const binding = cookies
        .split(";")
        .map((cookie) => cookie.trim())
        .filter((cookie) => cookie.startsWith(name + "="));
      if (binding.length !== 1) throw new HeimdallError("unauthenticated");
      const tokens = await oidc.exchange(
        code[0]!,
        () => records.claim(target, state[0]!, binding[0]!.slice(name.length + 1), request.signal),
        request.signal,
      );
      return { tokens, returnToKey, clearBindingCookie: `${name}=; Max-Age=0; ${attributes}` };
    },
  };
}

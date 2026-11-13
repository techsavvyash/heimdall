import { zitadelFederation } from "./zitadel-federation.js";
import {
  readEnterpriseEvidence,
  validateEnterpriseEvidence,
  type TenantSSOPolicy,
} from "./enterprise-evidence.js";
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
  records: Pick<BrowserLoginRecords, "start" | "claim"> &
    Partial<Pick<BrowserLoginRecords, "startFederation" | "claimFederation">>,
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

  function bindingFor(request: Request, state: string) {
    const name = prefix + state;
    const cookies = request.headers.get("cookie") ?? "";
    if (cookies.length > 16384) throw new HeimdallError("unauthenticated");
    const binding = cookies
      .split(";")
      .map((cookie) => cookie.trim())
      .filter((cookie) => cookie.startsWith(name + "="));
    if (binding.length !== 1) throw new HeimdallError("unauthenticated");
    return {
      binding: binding[0]!.slice(name.length + 1),
      clearBindingCookie: `${name}=; Max-Age=0; ${attributes}`,
    };
  }

  const scoped = (tenantId: string, policy: TenantSSOPolicy) => ({
    ...target,
    tenantPolicy: {
      tenantId,
      providerId: policy.providerId,
      protocol: policy.protocol,
      organizationId: policy.organizationId,
      policyVersion: policy.policyVersion,
      strictSSO: true,
    },
  });

  function startResponse(login: { state: string; browserBinding: string }, location: string) {
    return new Response(null, {
      status: 303,
      headers: {
        location,
        "set-cookie": `${prefix}${login.state}=${login.browserBinding}; Max-Age=600; ${attributes}`,
        "cache-control": "no-store",
        "referrer-policy": "no-referrer",
      },
    });
  }

  return {
    scope: Object.freeze({
      productId,
      environment,
      returnToKey,
      secure,
      origin: callback.origin,
      issuer,
    }),
    refresh: oidc.refresh,
    async start(request: Request) {
      if (request.method !== "GET" || new URL(request.url).origin !== callback.origin)
        throw new HeimdallError("invalid_argument");
      const login = await records.start(target, request.signal);
      return startResponse(
        login,
        oidc.authorizationURL(login.state, login.nonce, login.pkceChallenge),
      );
    },

    async startFederation(
      request: Request,
      tenantId: string,
      policy: TenantSSOPolicy,
      credential: string,
    ) {
      if (
        request.method !== "GET" ||
        new URL(request.url).origin !== callback.origin ||
        !records.startFederation
      )
        throw new HeimdallError("invalid_argument");
      const federation = zitadelFederation(issuer, credential, request.signal);
      const login = await records.startFederation(
        scoped(tenantId, policy),
        ({ state, nonce, pkceChallenge }) => {
          const success = new URL(redirectUri);
          success.searchParams.set("state", state);
          success.searchParams.set("tenant", tenantId);
          return federation.start(
            oidc.authorizationURL(state, nonce, pkceChallenge, policy.organizationId),
            success.href,
            policy,
          );
        },
        request.signal,
      );
      return startResponse(login, login.authorizationUrl);
    },

    async federationCallback(
      request: Request,
      tenantId: string,
      policy: TenantSSOPolicy,
      credential: string,
    ) {
      const url = new URL(request.url);
      const state = url.searchParams.getAll("state");
      const intents = url.searchParams.getAll("id");
      const tokens = url.searchParams.getAll("token");
      const tenants = url.searchParams.getAll("tenant");
      if (
        request.method !== "GET" ||
        url.origin !== callback.origin ||
        url.pathname !== callback.pathname ||
        url.search.length > 16384 ||
        /;|%(?![\da-f]{2})/i.test(url.search) ||
        state.length !== 1 ||
        !stateToken(state[0]!) ||
        intents.length !== 1 ||
        tokens.length !== 1 ||
        tenants.length !== 1 ||
        tenants[0] !== tenantId ||
        url.searchParams.has("error") ||
        !records.claimFederation
      )
        throw new HeimdallError("unauthenticated");
      const { binding, clearBindingCookie } = bindingFor(request, state[0]!);
      const claimed = await records.claimFederation(
        scoped(tenantId, policy),
        state[0]!,
        binding,
        intents[0]!,
        request.signal,
      );
      const operation = AbortSignal.any([
        request.signal,
        AbortSignal.timeout(Math.max(0, claimed.expiresAt - Date.now())),
      ]);
      const federation = zitadelFederation(issuer, credential, operation);
      const verified = await federation.finish({
        intentId: intents[0]!,
        intentToken: tokens[0]!,
        authRequestId: claimed.authRequestId,
        state: state[0]!,
        redirectUri,
        policy,
      });
      const productTokens = await oidc.exchange(
        verified.code,
        async () => ({ ...claimed, federation: verified.federation }),
        operation,
      );
      checkCancellation(operation);
      if (Date.now() >= claimed.expiresAt) throw new HeimdallError("unauthenticated");
      const { subject, sessionId, verifiedAt } = verified.federation;
      const scope = {
        productId,
        environment,
        clientId,
        issuer,
        subject,
        tenantId,
        providerId: policy.providerId,
        protocol: policy.protocol,
        organizationId: policy.organizationId,
        policyVersion: policy.policyVersion,
      };
      const evidence = readEnterpriseEvidence({
        schemaVersion: 1,
        ...scope,
        upstreamSubject: verified.upstreamSubject,
        providerSessionId: sessionId,
        verifiedAt,
        authTime: Math.floor(verifiedAt / 1000) * 1000,
      });
      validateEnterpriseEvidence(evidence, scope, Date.now(), policy.maxAgeMs);
      return {
        tokens: productTokens,
        evidence,
        expiresAt: claimed.expiresAt,
        returnToKey,
        clearBindingCookie,
      };
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
      const { binding, clearBindingCookie } = bindingFor(request, state[0]!);
      const tokens = await oidc.exchange(
        code[0]!,
        () => records.claim(target, state[0]!, binding, request.signal),
        request.signal,
      );
      return { tokens, returnToKey, clearBindingCookie };
    },
  };
}

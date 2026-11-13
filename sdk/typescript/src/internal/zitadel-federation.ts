import { checkCancellation, HeimdallError } from "./errors.js";
import type { TenantSSOPolicy } from "./enterprise-evidence.js";

const id = (value: unknown): value is string =>
  typeof value === "string" && /^[A-Za-z0-9_-]{1,200}(?![\s\S])/.test(value);
const visible = (value: unknown, max: number): value is string =>
  typeof value === "string" && value.length <= max && /^[\x21-\x7e]+(?![\s\S])/.test(value);
const redirects = [302, 303, 307, 308];
const single = (url: URL, key: string) => {
  const values = url.searchParams.getAll(key);
  return values.length === 1 ? values[0] : undefined;
};

export function zitadelFederation(issuer: string, credential: string, signal: AbortSignal) {
  const base = new URL(issuer);
  if (base.pathname !== "/" || !visible(credential, 4096))
    throw new HeimdallError("invalid_argument");

  async function request(url: URL, authenticated: boolean, body?: object) {
    if (url.origin !== base.origin || url.username || url.password || url.hash)
      throw new HeimdallError("unavailable");
    const operation = AbortSignal.any([signal, AbortSignal.timeout(5000)]);
    checkCancellation(operation);
    try {
      const response = await fetch(url, {
        signal: operation,
        redirect: "manual",
        ...(authenticated
          ? {
              headers: {
                authorization: "Bearer " + credential,
                "content-type": "application/json",
              },
            }
          : {}),
        ...(body === undefined ? {} : { method: "POST", body: JSON.stringify(body) }),
      });
      return { response, operation };
    } catch {
      checkCancellation(operation);
      throw new HeimdallError("unavailable");
    }
  }

  async function api<T>(path: string, body?: object): Promise<T> {
    const { response, operation } = await request(new URL(path, base), true, body);
    if (![200, 201].includes(response.status) || !response.body) {
      await response.body?.cancel();
      throw new HeimdallError(
        [400, 404].includes(response.status) ? "unauthenticated" : "unavailable",
      );
    }
    const reader = response.body.getReader();
    try {
      const chunks = [];
      let size = 0;
      for (;;) {
        const { value, done } = await reader.read();
        if (done) break;
        size += value.length;
        if (size > 1_048_576) throw new Error();
        chunks.push(value);
      }
      return JSON.parse(
        new TextDecoder("utf-8", { fatal: true }).decode(Buffer.concat(chunks)),
      ) as T;
    } catch {
      checkCancellation(operation);
      throw new HeimdallError("unavailable");
    } finally {
      await reader.cancel();
    }
  }

  async function redirect(url: URL) {
    const { response } = await request(url, false);
    await response.body?.cancel();
    const location = response.headers.get("location");
    if (!redirects.includes(response.status) || !visible(location, 16384))
      throw new HeimdallError("unavailable");
    return new URL(location, url);
  }

  return {
    async start(authorization: string, callback: string, policy: TenantSSOPolicy) {
      const login = await redirect(new URL(authorization));
      const authRequestId = single(login, "authRequest");
      if (!id(authRequestId) || !authRequestId.startsWith("V2_") || !id(policy.providerId))
        throw new HeimdallError("unavailable");
      const result = await api<{ authUrl?: string }>("/v2/idp_intents", {
        idpId: policy.providerId,
        urls: { successUrl: callback, failureUrl: callback },
      });
      if (!visible(result?.authUrl, 16384)) throw new HeimdallError("unavailable");
      const target = URL.parse(result.authUrl);
      if (
        !target ||
        target.protocol !== "https:" ||
        target.username ||
        target.password ||
        target.hash
      )
        throw new HeimdallError("unavailable");
      const intentId = single(target, policy.protocol === "oidc" ? "state" : "RelayState");
      if (!id(intentId)) throw new HeimdallError("unavailable");
      return { authRequestId, intentId, authorizationUrl: target.href };
    },

    async finish(input: {
      intentId: string;
      intentToken: string;
      authRequestId: string;
      state: string;
      redirectUri: string;
      policy: TenantSSOPolicy;
    }) {
      const { intentId, intentToken, authRequestId, state, redirectUri, policy } = input;
      if (!id(intentId) || !visible(intentToken, 500) || !id(authRequestId))
        throw new HeimdallError("unauthenticated");
      const intent = await api<{
        userId?: string;
        idpInformation?: {
          idpId?: string;
          userId?: string;
          saml?: { assertion?: string };
          oauth?: { idToken?: string };
        };
      }>(`/v2/idp_intents/${intentId}`, { idpIntentToken: intentToken });
      const linked = intent?.userId;
      const provider = intent?.idpInformation;
      const upstreamSubject = provider?.userId;
      if (
        !id(linked) ||
        provider?.idpId !== policy.providerId ||
        typeof upstreamSubject !== "string" ||
        !upstreamSubject.isWellFormed() ||
        !upstreamSubject ||
        [...upstreamSubject].length > 1024 ||
        !(policy.protocol === "saml" ? provider.saml?.assertion : provider.oauth?.idToken)
      )
        throw new HeimdallError("unauthenticated");

      const created = await api<{ sessionId?: string; sessionToken?: string }>("/v2/sessions", {
        checks: {
          user: { userId: linked },
          idpIntent: { idpIntentId: intentId, idpIntentToken: intentToken },
        },
        lifetime: "3600s",
      });
      const sessionId = created?.sessionId;
      const sessionToken = created?.sessionToken;
      if (!id(sessionId) || sessionId.length > 128 || !visible(sessionToken, 500))
        throw new HeimdallError("unavailable");

      const read = await api<{
        session?: {
          id?: string;
          factors?: {
            user?: { id?: string; organizationId?: string };
            password?: unknown;
            intent?: { verifiedAt?: string };
          };
        };
      }>(`/v2/sessions/${sessionId}`);
      const factors = read?.session?.factors;
      const verifiedAt = Date.parse(factors?.intent?.verifiedAt ?? "");
      if (
        read?.session?.id !== sessionId ||
        factors?.user?.id !== linked ||
        factors.user.organizationId !== policy.organizationId ||
        factors.password !== undefined ||
        !Number.isSafeInteger(verifiedAt) ||
        verifiedAt < 0
      )
        throw new HeimdallError("unauthenticated");

      const result = await api<{ callbackUrl?: string }>(
        `/v2/oidc/auth_requests/${authRequestId}`,
        {
          session: { sessionId, sessionToken },
        },
      );
      const callback = new URL(redirectUri);
      let location = visible(result?.callbackUrl, 16384) ? URL.parse(result.callbackUrl) : null;
      for (
        let hop = 0;
        location &&
        hop < 4 &&
        location.origin === base.origin &&
        (location.origin !== callback.origin || location.pathname !== callback.pathname);
        hop++
      )
        location = await redirect(location);
      const code = location && single(location, "code");
      if (
        !location ||
        location.origin !== callback.origin ||
        location.pathname !== callback.pathname ||
        location.username ||
        location.password ||
        location.hash ||
        location.searchParams.has("error") ||
        single(location, "state") !== state ||
        !visible(code, 4096)
      )
        throw new HeimdallError("unauthenticated");
      return { code, upstreamSubject, federation: { subject: linked, sessionId, verifiedAt } };
    },
  };
}

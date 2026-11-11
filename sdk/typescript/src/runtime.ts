import { parseDeployment } from "./internal/configuration.js";
import { discoverOIDC } from "./internal/oidc-discovery.js";
import { PinnedModel } from "./internal/model-binding.js";
import { AuditedAuthentication } from "./internal/audited-authentication.js";
import { bounded, known } from "./internal/audited-decision.js";
import { HeimdallError, checkCancellation } from "./internal/errors.js";

type AuthenticationArguments = ConstructorParameters<typeof AuditedAuthentication>;
export type IdentityGate = AuthenticationArguments[2];
export type AuditAppend = AuthenticationArguments[3];
export type AuditReceipt = Awaited<ReturnType<AuditAppend>>;
export type RuntimeStorage = {
  productId: string;
  environment: string;
  /** Read-only readiness: validate durable stores and their configured scope. Never migrate here. */
  ready(signal: AbortSignal): Promise<void>;
  identity: IdentityGate;
  appendAudit: AuditAppend;
};
export type RuntimeOptions = {
  policyVersion: string;
  identityFreshnessMs?: number;
  credentials: {
    clientSecret(signal: AbortSignal): Promise<string>;
    openfgaKey(signal: AbortSignal): Promise<string>;
  };
  storage: RuntimeStorage;
};
declare const principalBrand: unique symbol;
export type Principal = Awaited<ReturnType<AuditedAuthentication["verify"]>> & {
  readonly [principalBrand]: true;
};
export type Heimdall = Readonly<{
  tokens: Readonly<{
    verify(accessToken: string, options: { signal: AbortSignal }): Promise<Principal>;
  }>;
}>;

/** Construct a scoped core runtime. Storage ports are trusted durable product integrations. */
export async function createHeimdall(
  configJSON: string,
  manifestJSON: string,
  options: RuntimeOptions,
  request: { signal: AbortSignal },
): Promise<Heimdall> {
  if (!(request?.signal instanceof AbortSignal)) throw new HeimdallError("invalid_argument");
  const signal = AbortSignal.any([request.signal, AbortSignal.timeout(5000)]);
  checkCancellation(signal);
  const deployment = parseDeployment(configJSON, manifestJSON);
  const { config, manifest, artifact } = deployment;
  const storage = options?.storage;
  if (
    !options ||
    typeof options.policyVersion !== "string" ||
    !/^[A-Za-z0-9_-]{1,128}(?![\s\S])/.test(options.policyVersion) ||
    (options.identityFreshnessMs !== undefined &&
      (!Number.isSafeInteger(options.identityFreshnessMs) ||
        options.identityFreshnessMs < 0 ||
        options.identityFreshnessMs > 60000)) ||
    storage?.productId !== config.productId ||
    storage?.environment !== config.environment ||
    typeof storage?.ready !== "function" ||
    typeof storage?.appendAudit !== "function" ||
    typeof storage?.identity?.check !== "function" ||
    typeof storage?.identity?.invalidate !== "function" ||
    typeof options.credentials?.clientSecret !== "function" ||
    typeof options.credentials?.openfgaKey !== "function" ||
    !(request?.signal instanceof AbortSignal)
  )
    throw new HeimdallError("invalid_argument");
  // Capture all ports before the first await; caller mutation cannot redirect this runtime.
  const ready = storage.ready.bind(storage);
  const append = storage.appendAudit.bind(storage);
  const gate = {
    check: storage.identity.check.bind(storage.identity),
    invalidate: storage.identity.invalidate.bind(storage.identity),
  };
  const secret = options.credentials.clientSecret.bind(options.credentials);
  const key = options.credentials.openfgaKey.bind(options.credentials);
  const policyVersion = options.policyVersion;
  const freshnessMs = options.identityFreshnessMs ?? 60000;
  const profile = config.transportProfile ?? "production";
  try {
    await bounded(ready, signal);
    const metadata = await discoverOIDC(config.issuer, profile, signal);
    // The identity-status implementation uses the pinned ZITADEL introspection route.
    if (
      metadata.introspection_endpoint !==
      config.issuer.replace(/\/$/, "") + "/oauth/v2/introspect"
    )
      throw new HeimdallError("unavailable");
    await new PinnedModel({ ...config.openfga, profile }, manifest, config, artifact, key).verify(
      signal,
    );
    checkCancellation(signal);
    const authentication = new AuditedAuthentication(
      {
        issuer: config.issuer,
        audience: config.audience,
        jwksUri: metadata.jwks_uri,
        profile,
        productId: config.productId,
        environment: config.environment,
        clientId: config.clientId,
        policyVersion,
        freshnessMs,
      },
      secret,
      gate,
      append,
    );
    return Object.freeze({
      tokens: Object.freeze({
        verify: async (raw: string, call: { signal: AbortSignal }) => {
          if (!(call?.signal instanceof AbortSignal)) throw new HeimdallError("invalid_argument");
          return (await authentication.verify(raw, "bearer", call.signal)) as Principal;
        },
      }),
    });
  } catch (error) {
    checkCancellation(signal);
    throw known(error);
  }
}

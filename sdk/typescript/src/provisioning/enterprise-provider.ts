import { bounded, known } from "../internal/audited-decision.js";
import { validateEndpoint } from "../internal/endpoints.js";
import { checkCancellation, HeimdallError } from "../internal/errors.js";
import { providerId, providerRequest } from "./provider-request.js";

export type EnterpriseProviderOptions = {
  issuer: string;
  organizationId: string;
  name: string;
  token: string;
  transportProfile?: "production" | "loopback";
  provider:
    | { protocol: "oidc"; issuer: string; clientId: string; clientSecret?: string }
    | { protocol: "saml"; metadataXml: Uint8Array };
} & (
  | { existingProviderId: string; claimDispatch?: never }
  | { existingProviderId?: never; claimDispatch: (signal: AbortSignal) => Promise<void> }
);

const scopes = ["openid", "profile", "email"];
const safeOptions = {
  isLinkingAllowed: false,
  isCreationAllowed: false,
  isAutoCreation: false,
  isAutoUpdate: false,
};
const samlSettings = {
  binding: "SAML_BINDING_REDIRECT",
  withSignedRequest: true,
  signatureAlgorithm: "SAML_SIGNATURE_RSA_SHA256",
  nameIdFormat: "SAML_NAME_ID_FORMAT_PERSISTENT",
};

/** Provision a candidate after product authorization, or verify an explicitly recovered ID. */
export async function prepareEnterpriseProvider(
  options: EnterpriseProviderOptions,
  signal: AbortSignal,
) {
  const { issuer, organizationId, name, token, provider, existingProviderId, claimDispatch } =
    options;
  if (
    !providerId(organizationId) ||
    typeof name !== "string" ||
    !name.isWellFormed() ||
    !name.trim() ||
    [...name].length > 200 ||
    (existingProviderId !== undefined
      ? !providerId(existingProviderId) || claimDispatch !== undefined
      : typeof claimDispatch !== "function")
  )
    throw new HeimdallError("invalid_argument");
  const profile = options.transportProfile ?? "production";
  const operation = AbortSignal.any([signal, AbortSignal.timeout(20000)]);
  const call = providerRequest(issuer, token, profile, operation, {
    "x-zitadel-orgid": organizationId,
  });
  let settings;
  const protocol = provider?.protocol;
  if (provider?.protocol === "oidc") {
    if (typeof provider.issuer !== "string") throw new HeimdallError("invalid_argument");
    validateEndpoint(provider.issuer, "production");
    if (
      typeof provider.clientId !== "string" ||
      !provider.clientId.isWellFormed() ||
      !provider.clientId.trim() ||
      [...provider.clientId].length > 200 ||
      (existingProviderId === undefined &&
        (typeof provider.clientSecret !== "string" ||
          !provider.clientSecret.isWellFormed() ||
          !provider.clientSecret ||
          Buffer.byteLength(provider.clientSecret) > 4096))
    )
      throw new HeimdallError("invalid_argument");
    settings = { issuer: provider.issuer, clientId: provider.clientId, scopes, usePkce: true };
  } else if (provider?.protocol === "saml") {
    if (
      !(provider.metadataXml instanceof Uint8Array) ||
      !provider.metadataXml.length ||
      provider.metadataXml.length > 262144
    )
      throw new HeimdallError("invalid_argument");
    settings = {
      ...samlSettings,
      metadataXml: Buffer.from(provider.metadataXml).toString("base64"),
    };
  } else {
    throw new HeimdallError("invalid_argument");
  }
  let id = existingProviderId;
  if (id === undefined) {
    const secret = provider.protocol === "oidc" ? provider.clientSecret : undefined;
    const claim = AbortSignal.any([operation, AbortSignal.timeout(5000)]);
    try {
      await bounded(claimDispatch!, claim);
    } catch (error) {
      throw new HeimdallError(known(error).code);
    }
    checkCancellation(operation);
    try {
      const path = protocol === "oidc" ? "generic_oidc" : "saml";
      const created = await call<{ id?: string }>("/management/v1/idps/" + path, {
        name,
        ...settings,
        providerOptions: safeOptions,
        ...(secret === undefined ? {} : { clientSecret: secret }),
      });
      if (!providerId(created?.id)) throw new Error();
      id = created.id;
    } catch {
      throw new HeimdallError("outcome_unknown");
    }
  }

  type Read = {
    idp?: {
      id?: string;
      name?: string;
      state?: string;
      type?: string;
      details?: { resourceOwner?: string };
      config?: {
        options?: Record<string, unknown>;
        oidc?: { issuer?: string; clientId?: string; scopes?: string[] };
        saml?: {
          binding?: string;
          withSignedRequest?: boolean;
          signatureAlgorithm?: string;
          nameIdFormat?: string;
          metadataXml?: string;
        };
      };
    };
  };
  let read: Read | null;
  try {
    read = await call<Read>("/v2/idps/" + id);
    if (!read?.idp && existingProviderId === undefined) throw new Error();
  } catch (error) {
    if (existingProviderId === undefined) throw new HeimdallError("outcome_unknown", id);
    throw error;
  }
  const observed = read?.idp;
  const config = observed?.config;
  if (
    observed?.id !== id ||
    observed.name !== name ||
    observed.state !== "IDP_STATE_ACTIVE" ||
    observed.type !== (protocol === "oidc" ? "IDP_TYPE_OIDC" : "IDP_TYPE_SAML") ||
    observed.details?.resourceOwner !== organizationId ||
    !config?.options ||
    Object.keys(safeOptions).some(
      (k) => config.options![k] !== undefined && config.options![k] !== false,
    )
  )
    throw new HeimdallError("conflict", id);
  if ("issuer" in settings) {
    const oidc = config.oidc;
    if (
      !oidc ||
      oidc.issuer !== settings.issuer ||
      oidc.clientId !== settings.clientId ||
      !Array.isArray(oidc.scopes) ||
      oidc.scopes.length !== scopes.length ||
      !scopes.every((scope) => oidc.scopes!.includes(scope))
    )
      throw new HeimdallError("conflict", id);
  } else {
    const saml = config.saml;
    if (
      !saml ||
      saml.metadataXml !== settings.metadataXml ||
      saml.binding !== samlSettings.binding ||
      saml.withSignedRequest !== true ||
      saml.signatureAlgorithm !== samlSettings.signatureAlgorithm ||
      saml.nameIdFormat !== samlSettings.nameIdFormat
    )
      throw new HeimdallError("conflict", id);
  }
  const base = new URL(issuer).origin;
  return protocol === "oidc"
    ? {
        providerId: id,
        organizationId,
        protocol: "oidc" as const,
        callbackUrl: base + "/idps/callback",
      }
    : {
        providerId: id,
        organizationId,
        protocol: "saml" as const,
        metadataUrl: base + "/idps/" + id + "/saml/metadata",
      };
}

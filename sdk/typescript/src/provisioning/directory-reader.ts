import { known } from "../internal/audited-decision.js";
import { HeimdallError } from "../internal/errors.js";
import { providerId, providerRequest } from "./provider-request.js";

export type DirectoryReaderOptions = {
  issuer: string;
  organizationId: string;
  token: string;
  transportProfile?: "production" | "loopback";
};

type DirectoryUser = { id: string; externalId?: string };
type DirectoryResource = { id: string; externalId?: string | null };
type AuthoritativeUser = {
  user?: {
    userId?: string;
    details?: { resourceOwner?: string };
    state?: string;
    human?: object;
  };
};

/** Server-side reads for an explicitly configured directory and its provisioning domain. */
export function createDirectoryReader(options: DirectoryReaderOptions) {
  const { issuer, organizationId, token, transportProfile = "production" } = options;
  if (!providerId(organizationId)) throw new HeimdallError("invalid_argument");
  providerRequest(issuer, token, transportProfile, new AbortController().signal);
  const path = "/scim/v2/" + organizationId + "/Users";
  const request = (signal: AbortSignal) => providerRequest(issuer, token, transportProfile, signal);
  const readUser = (value: DirectoryResource | null): DirectoryUser => {
    if (
      !value ||
      !providerId(value.id) ||
      (value.externalId !== null &&
        value.externalId !== undefined &&
        (typeof value.externalId !== "string" ||
          !value.externalId.isWellFormed() ||
          Buffer.byteLength(value.externalId) > 4096))
    )
      throw new HeimdallError("unavailable");
    return value.externalId ? { id: value.id, externalId: value.externalId } : { id: value.id };
  };
  const page = async (startIndex: number, count: number, signal: AbortSignal) => {
    if (
      !(signal instanceof AbortSignal) ||
      !Number.isSafeInteger(startIndex) ||
      startIndex < 1 ||
      startIndex > Number.MAX_SAFE_INTEGER - count ||
      !Number.isSafeInteger(count) ||
      count < 1 ||
      count > 50
    )
      throw new HeimdallError("invalid_argument");
    const response = await request(signal)<{
      Resources?: DirectoryResource[];
      startIndex?: number;
      totalResults?: number;
    }>(path + "?startIndex=" + startIndex + "&count=" + count);
    if (
      !response ||
      response.startIndex !== startIndex ||
      typeof response.totalResults !== "number" ||
      !Number.isSafeInteger(response.totalResults) ||
      response.totalResults < 0 ||
      !Array.isArray(response.Resources) ||
      response.Resources.length > count
    )
      throw new HeimdallError("unavailable");
    const users = response.Resources.map(readUser);
    if (new Set(users.map((user) => user.id)).size !== users.length)
      throw new HeimdallError("unavailable");
    return {
      users,
      nextStartIndex: users.length === count ? startIndex + count : null,
    };
  };

  return Object.freeze({
    async list(startIndex: number, count: number, signal: AbortSignal) {
      try {
        return await page(startIndex, count, signal);
      } catch (error) {
        throw known(error);
      }
    },
    async read(subject: string, signal: AbortSignal) {
      if (!providerId(subject) || !(signal instanceof AbortSignal))
        throw new HeimdallError("invalid_argument");
      const operation = AbortSignal.any([signal, AbortSignal.timeout(15000)]);
      try {
        const call = request(operation);
        const response = await call<AuthoritativeUser>("/v2/users/" + subject);
        if (!response) {
          // A mis-scoped reader can also receive 404. Verify access to this directory first.
          await page(1, 1, operation);
          if (await call<DirectoryResource>(path + "/" + subject))
            throw new HeimdallError("unavailable");
          return { state: "missing" as const };
        }
        const user = response.user;
        if (!user) throw new HeimdallError("unavailable");
        if (user.userId !== subject || user.details?.resourceOwner !== organizationId)
          throw new HeimdallError("conflict");
        if (!user.human || typeof user.human !== "object" || Array.isArray(user.human))
          throw new HeimdallError("conflict");
        switch (user.state) {
          case "USER_STATE_DELETED":
            return { state: "missing" as const };
          case "USER_STATE_INACTIVE":
          case "USER_STATE_LOCKED":
            return { state: "inactive" as const };
          case "USER_STATE_INITIAL":
            return { state: "initial" as const };
          case "USER_STATE_ACTIVE": {
            const scim = await call<DirectoryResource & { active?: boolean }>(path + "/" + subject);
            if (scim?.active !== true) throw new HeimdallError("unavailable");
            const resource = readUser(scim);
            if (resource.id !== subject) throw new HeimdallError("conflict");
            return {
              state: "active" as const,
              ...(resource.externalId === undefined ? {} : { externalId: resource.externalId }),
            };
          }
          default:
            throw new HeimdallError("unavailable");
        }
      } catch (error) {
        throw known(error);
      }
    },
  });
}

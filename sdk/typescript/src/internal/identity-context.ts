import { HeimdallError } from "./errors.js";
// The key is an internal opaque storage key, never the raw browser cookie.
export function sessionIdentityContext(key: string, revision: string): string {
  if (
    typeof key !== "string" ||
    !/^[A-Za-z0-9_-]{1,128}$/.test(key) ||
    typeof revision !== "string" ||
    !/^[1-9][0-9]{0,18}$/.test(revision) ||
    BigInt(revision) > 9223372036854775807n
  )
    throw new HeimdallError("invalid_argument");
  return `session:${key}:${revision}`;
}
export function parseIdentityContext(value: string): { key: string; revision: string } | null {
  if (value === "bearer") return null;
  if (typeof value !== "string") throw new HeimdallError("invalid_argument");
  const parts = value.split(":");
  if (
    parts.length !== 3 ||
    parts[0] !== "session" ||
    sessionIdentityContext(parts[1]!, parts[2]!) !== value
  )
    throw new HeimdallError("invalid_argument");
  return { key: parts[1]!, revision: parts[2]! };
}

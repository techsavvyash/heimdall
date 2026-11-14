import { createHash } from "node:crypto";
import { HeimdallError } from "./errors.js";
const maxRecordKeyEncryptions = 4294967295;
export function recordKeyFingerprint(key: Uint8Array): string {
  if (!(key instanceof Uint8Array) || key.length !== 32)
    throw new HeimdallError("invalid_argument");
  return createHash("sha256")
    .update("heimdall.record-key-usage.v1\0", "ascii")
    .update(key)
    .digest("hex");
}
export function validateKeyUsage(fingerprint: string, limit?: number): void {
  if (
    typeof fingerprint !== "string" ||
    !/^[a-f0-9]{64}(?![\s\S])/.test(fingerprint) ||
    (limit !== undefined &&
      (!Number.isSafeInteger(limit) || limit < 1 || limit > maxRecordKeyEncryptions))
  )
    throw new HeimdallError("invalid_argument");
}

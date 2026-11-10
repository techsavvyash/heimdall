import { createHash } from "node:crypto";
import { HeimdallError } from "./errors.js";

// Call after envelope/schema validation. This fingerprints values, not JSON text.
export function auditDigest(event: Record<string, unknown>): string {
  const hash = createHash("sha256").update("heimdall.audit.v1\0");
  let bytes = 0;
  const write = (value: Buffer | string) => {
    bytes += Buffer.byteLength(value);
    if (bytes > 1048576) throw new HeimdallError("invalid_argument");
    hash.update(value);
  };
  const count = (value: number) => {
    if (value > 1048576) throw new HeimdallError("invalid_argument");
    const b = Buffer.alloc(4);
    b.writeUInt32BE(value);
    write(b);
  };
  const visit = (value: unknown, depth: number): void => {
    if (depth > 16) throw new HeimdallError("invalid_argument");
    if (value === null) return write("n");
    if (typeof value === "boolean") return write(value ? "t" : "f");
    if (typeof value === "number") {
      if (!Number.isFinite(value) || Math.abs(value) > Number.MAX_SAFE_INTEGER)
        throw new HeimdallError("invalid_argument");
      write("d");
      const b = Buffer.alloc(8);
      b.writeDoubleBE(value === 0 ? 0 : value);
      return write(b);
    }
    if (typeof value === "string") {
      if (!value.isWellFormed()) throw new HeimdallError("invalid_argument");
      const b = Buffer.from(value, "utf8");
      write("s");
      count(b.length);
      return write(b);
    }
    if (Array.isArray(value)) {
      write("a");
      count(value.length);
      for (const item of value) visit(item, depth + 1);
      return;
    }
    if (typeof value !== "object" || value === undefined)
      throw new HeimdallError("invalid_argument");
    const keys = Object.keys(value).sort();
    write("o");
    count(keys.length);
    for (const key of keys) {
      if (!key || key.length > 64 || /[^A-Za-z0-9_-]/.test(key))
        throw new HeimdallError("invalid_argument");
      visit(key, depth + 1);
      visit((value as Record<string, unknown>)[key], depth + 1);
    }
  };
  visit(event, 0);
  return hash.digest("hex");
}

type Scope = { productId: string; environment: string };
type EventIdentity = Scope & { eventId: string; recordedAt: number };
type Receipt = EventIdentity & { payloadDigest: string };
type Delivery = Scope & { sinkId: string; eventIds: string[] };

// Structural validation and authenticated store/transport context are prerequisites.
export function validateAuditReceipt(event: EventIdentity, digest: string, receipt: Receipt): void {
  if (
    event.productId !== receipt.productId ||
    event.environment !== receipt.environment ||
    event.eventId !== receipt.eventId ||
    event.recordedAt !== receipt.recordedAt ||
    digest !== receipt.payloadDigest
  )
    throw new HeimdallError("audit_unavailable");
}

export function validateDeliveryReceipt(batch: Delivery, receipt: Delivery): void {
  const sent = new Set(batch.eventIds);
  if (
    batch.productId !== receipt.productId ||
    batch.environment !== receipt.environment ||
    batch.sinkId !== receipt.sinkId ||
    sent.size !== batch.eventIds.length ||
    sent.size === 0 ||
    receipt.eventIds.length === 0 ||
    new Set(receipt.eventIds).size !== receipt.eventIds.length ||
    receipt.eventIds.some((id) => !sent.has(id))
  )
    throw new HeimdallError("unavailable");
}

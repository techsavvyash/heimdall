import {
  createCipheriv,
  createDecipheriv,
  createHmac,
  randomBytes,
  timingSafeEqual,
} from "node:crypto";
import { HeimdallError } from "./errors.js";

const recordLimit = 1048576;
const cursorLimit = 65536;
const token = (value: string, max = 128) =>
  typeof value === "string" &&
  value.length > 0 &&
  value.length <= max &&
  !/[^A-Za-z0-9_-]/.test(value);

class KeyRing {
  readonly active: string;
  private readonly keys = new Map<string, Buffer>();
  constructor(active: string, keys: Readonly<Record<string, Uint8Array>>) {
    if (!token(active, 64) || Object.keys(keys).length === 0 || Object.keys(keys).length > 32) {
      throw new HeimdallError("invalid_argument");
    }
    for (const [id, key] of Object.entries(keys)) {
      if (!token(id, 64) || !(key instanceof Uint8Array) || key.length !== 32) {
        throw new HeimdallError("invalid_argument");
      }
      this.keys.set(id, Buffer.from(key));
    }
    if (!this.keys.has(active)) throw new HeimdallError("invalid_argument");
    this.active = active;
  }
  get(id: string): Buffer | undefined {
    return this.keys.get(id);
  }
}

type RecordScope = {
  productId: string;
  environment: string;
  kind: "session" | "login";
  recordKey: string;
};

function aad(scope: RecordScope, keyId: string): Buffer {
  if (
    !token(scope.productId, 64) ||
    !token(scope.environment, 64) ||
    !token(scope.recordKey) ||
    !["session", "login"].includes(scope.kind)
  ) {
    throw new HeimdallError("invalid_argument");
  }
  return Buffer.from(
    [
      "heimdall.record.v1",
      keyId,
      scope.productId,
      scope.environment,
      scope.kind,
      scope.recordKey,
    ].join("|"),
    "ascii",
  );
}

function decode(value: string, max: number): Buffer {
  if (
    value.length === 0 ||
    value.length > Math.ceil((max * 4) / 3) ||
    /[^A-Za-z0-9_-]/.test(value)
  ) {
    throw new Error("invalid envelope");
  }
  const bytes = Buffer.from(value, "base64url");
  if (bytes.length > max || bytes.toString("base64url") !== value)
    throw new Error("invalid envelope");
  return bytes;
}

export class RecordCipher {
  private readonly ring: KeyRing;
  constructor(activeKeyId: string, keys: Readonly<Record<string, Uint8Array>>) {
    this.ring = new KeyRing(activeKeyId, keys);
  }
  seal(scope: RecordScope, plaintext: Uint8Array): string {
    const associated = aad(scope, this.ring.active);
    if (plaintext.length === 0 || plaintext.length > recordLimit)
      throw new HeimdallError("invalid_argument");
    try {
      const nonce = randomBytes(12);
      const cipher = createCipheriv("aes-256-gcm", this.ring.get(this.ring.active)!, nonce, {
        authTagLength: 16,
      });
      cipher.setAAD(associated);
      const encrypted = Buffer.concat([
        nonce,
        cipher.update(plaintext),
        cipher.final(),
        cipher.getAuthTag(),
      ]);
      return ["hr1", this.ring.active, encrypted.toString("base64url")].join(".");
    } catch {
      throw new HeimdallError("unavailable");
    }
  }
  open(scope: RecordScope, envelope: string): Buffer {
    // Validate trusted context separately from untrusted envelope failures.
    aad(scope, this.ring.active);
    try {
      if (envelope.length > 1400000) throw new Error("too large");
      const parts = envelope.split(".");
      if (parts.length !== 3 || parts[0] !== "hr1" || !token(parts[1]!, 64))
        throw new Error("format");
      const key = this.ring.get(parts[1]!);
      if (!key) throw new Error("unknown key");
      const bytes = decode(parts[2]!, recordLimit + 28);
      if (bytes.length < 29) throw new Error("truncated");
      const decipher = createDecipheriv("aes-256-gcm", key, bytes.subarray(0, 12), {
        authTagLength: 16,
      });
      decipher.setAAD(aad(scope, parts[1]!));
      decipher.setAuthTag(bytes.subarray(-16));
      // No plaintext is returned until final authenticates the complete record.
      return Buffer.concat([decipher.update(bytes.subarray(12, -16)), decipher.final()]);
    } catch {
      throw new HeimdallError("unauthenticated");
    }
  }
}

export class CursorSigner {
  private readonly ring: KeyRing;
  constructor(activeKeyId: string, keys: Readonly<Record<string, Uint8Array>>) {
    this.ring = new KeyRing(activeKeyId, keys);
  }
  sign(payload: Uint8Array): string {
    if (payload.length === 0 || payload.length > cursorLimit)
      throw new HeimdallError("invalid_argument");
    const encoded = Buffer.from(payload).toString("base64url");
    const mac = createHmac("sha256", this.ring.get(this.ring.active)!)
      .update(["heimdall.cursor.v1", this.ring.active, encoded].join("|"), "ascii")
      .digest("base64url");
    return ["hc1", this.ring.active, encoded, mac].join(".");
  }
  verify(envelope: string): Buffer {
    try {
      if (envelope.length > 88000) throw new Error("too large");
      const parts = envelope.split(".");
      if (parts.length !== 4 || parts[0] !== "hc1" || !token(parts[1]!, 64))
        throw new Error("format");
      const key = this.ring.get(parts[1]!);
      if (!key) throw new Error("unknown key");
      const payload = decode(parts[2]!, cursorLimit);
      const mac = decode(parts[3]!, 32);
      const expected = createHmac("sha256", key)
        .update(["heimdall.cursor.v1", parts[1]!, parts[2]!].join("|"), "ascii")
        .digest();
      if (mac.length !== 32 || !timingSafeEqual(mac, expected)) throw new Error("signature");
      return payload;
    } catch {
      throw new HeimdallError("invalid_argument");
    }
  }
}

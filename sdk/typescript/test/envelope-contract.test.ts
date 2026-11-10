import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { CursorSigner, RecordCipher } from "../src/internal/envelopes.js";

type Scope = Parameters<RecordCipher["open"]>[0];
const v: {
  keyHex: string;
  keyId: string;
  scope: Scope;
  plaintextHex: string;
  record: string;
  goRecord: string;
  tsRecord: string;
  payloadHex: string;
  cursor: string;
  badRecords: string[];
  badCursors: string[];
  wrongScopes: Scope[];
} = JSON.parse(
  readFileSync(new URL("../../../contracts/fixtures/crypto/v1.json", import.meta.url), "utf8"),
);
const key = Buffer.from(v.keyHex, "hex");
const cipher = new RecordCipher(v.keyId, { [v.keyId]: key });
const signer = new CursorSigner(v.keyId, { [v.keyId]: key });

describe("authenticated record envelope", () => {
  it.each([v.goRecord, v.tsRecord])("reads actual SDK writer output %j", (record) => {
    expect(cipher.open(v.scope, record).toString("hex")).toBe(v.plaintextHex);
  });
  it("reads shared AES-GCM vector", () => {
    expect(cipher.open(v.scope, v.record).toString("hex")).toBe(v.plaintextHex);
  });
  it("uses random nonces and authenticates fresh writes", () => {
    const a = cipher.seal(v.scope, Buffer.from(v.plaintextHex, "hex"));
    const b = cipher.seal(v.scope, Buffer.from(v.plaintextHex, "hex"));
    expect(a).not.toBe(b);
    expect(cipher.open(v.scope, a).toString("hex")).toBe(v.plaintextHex);
    expect(cipher.open(v.scope, b).toString("hex")).toBe(v.plaintextHex);
  });
  it.each(v.badRecords)("rejects tampering %j", (value) => {
    expect(() => cipher.open(v.scope, value)).toThrow("unauthenticated");
  });
  it.each(v.wrongScopes)("rejects mismatched context %j", (scope) => {
    expect(() => cipher.open(scope, v.record)).toThrow("unauthenticated");
  });
  it("authenticates key IDs even when two IDs contain the same key", () => {
    const alias = new RecordCipher(v.keyId, { [v.keyId]: key, alias: key });
    expect(() => alias.open(v.scope, v.record.replace("." + v.keyId + ".", ".alias."))).toThrow(
      "unauthenticated",
    );
    const aliasSigner = new CursorSigner(v.keyId, { [v.keyId]: key, alias: key });
    expect(() => aliasSigner.verify(v.cursor.replace("." + v.keyId + ".", ".alias."))).toThrow(
      "invalid_argument",
    );
  });
  it("retains old keys for reading but writes with active key", () => {
    const rotated = new RecordCipher("key-2", { [v.keyId]: key, "key-2": Buffer.alloc(32, 42) });
    expect(rotated.open(v.scope, v.record).toString("hex")).toBe(v.plaintextHex);
    expect(rotated.seal(v.scope, Buffer.from("new"))).toMatch(/^hr1\.key-2\./);
    expect(() =>
      new RecordCipher("key-2", { "key-2": Buffer.alloc(32, 42) }).open(v.scope, v.record),
    ).toThrow("unauthenticated");
  });
  it("bounds records and rejects empty plaintext", () => {
    expect(() => cipher.seal(v.scope, Buffer.alloc(1048577))).toThrow("invalid_argument");
    expect(() => cipher.seal(v.scope, Buffer.alloc(0))).toThrow("invalid_argument");
    expect(() => cipher.open(v.scope, "x".repeat(1400001))).toThrow("unauthenticated");
  });
});

describe("signed cursor envelope", () => {
  it("produces exact shared HMAC vector", () => {
    expect(signer.sign(Buffer.from(v.payloadHex, "hex"))).toBe(v.cursor);
    expect(signer.verify(v.cursor).toString("hex")).toBe(v.payloadHex);
  });
  it.each(v.badCursors)("rejects tampering %j", (value) => {
    expect(() => signer.verify(value)).toThrow("invalid_argument");
  });
  it("supports rotation and copies caller-owned key bytes", () => {
    const local = Buffer.from(key);
    const rotated = new CursorSigner("key-2", { [v.keyId]: local, "key-2": Buffer.alloc(32, 42) });
    local.fill(255);
    expect(rotated.verify(v.cursor).toString("hex")).toBe(v.payloadHex);
    expect(rotated.sign(Buffer.from("new"))).toMatch(/^hc1\.key-2\./);
    expect(() =>
      new CursorSigner("key-2", { "key-2": Buffer.alloc(32, 42) }).verify(v.cursor),
    ).toThrow("invalid_argument");
  });
  it("bounds cursor size", () => {
    expect(() => signer.sign(Buffer.alloc(65537))).toThrow("invalid_argument");
    expect(() => signer.sign(Buffer.alloc(0))).toThrow("invalid_argument");
    expect(() => signer.verify("x".repeat(88001))).toThrow("invalid_argument");
  });
});

describe("key configuration", () => {
  it.each([0, 16, 31, 33])("rejects key length %i", (length) => {
    expect(() => new RecordCipher("k", { k: Buffer.alloc(length) })).toThrow("invalid_argument");
    expect(() => new CursorSigner("k", { k: Buffer.alloc(length) })).toThrow("invalid_argument");
  });
  it("requires known active key and safe identifiers", () => {
    expect(() => new RecordCipher("missing", { k: key })).toThrow("invalid_argument");
    expect(() => new RecordCipher("k\n", { "k\n": key })).toThrow("invalid_argument");
    expect(() => new CursorSigner("k", {})).toThrow("invalid_argument");
  });
});

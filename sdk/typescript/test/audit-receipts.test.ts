import { readFileSync } from "node:fs";
import { Ajv2020 } from "ajv/dist/2020.js";
import { describe, expect, it } from "vitest";
import {
  auditDigest,
  validateAuditReceipt,
  validateDeliveryReceipt,
} from "../src/internal/audit-receipts.js";
const read = (name: string): unknown =>
  JSON.parse(
    readFileSync(new URL(`../../../contracts/fixtures/${name}-v1.json`, import.meta.url), "utf8"),
  );
const digests = read("audit-digests") as {
  name: string;
  event: Record<string, unknown>;
  digest: string;
}[];
type Receipt = Parameters<typeof validateAuditReceipt>[2];
const receipts = read("audit-receipts") as {
  name: string;
  expected: Receipt;
  received: Receipt;
  valid: boolean;
}[];
type Delivery = Parameters<typeof validateDeliveryReceipt>[0];
const deliveries = read("audit-delivery") as {
  name: string;
  batch: Delivery;
  receipt: Delivery;
  valid: boolean;
}[];
const shape = new Ajv2020({ strict: true }).compile(
  JSON.parse(
    readFileSync(new URL("../../../contracts/audit-event.schema.json", import.meta.url), "utf8"),
  ) as object,
);
describe("audit fingerprints and receipt binding", () => {
  it.each(digests)("schema precondition $name", (c) => expect(shape(c.event)).toBe(true));
  it.each(digests)("fingerprints $name", (c) => expect(auditDigest(c.event)).toBe(c.digest));
  it.each(receipts.filter((c) => c.valid))("accepts local receipt $name", (c) => {
    expect(() =>
      validateAuditReceipt(c.expected, c.expected.payloadDigest, c.received),
    ).not.toThrow();
  });
  it.each(receipts.filter((c) => !c.valid))("rejects local receipt $name", (c) => {
    expect(() => validateAuditReceipt(c.expected, c.expected.payloadDigest, c.received)).toThrow(
      "audit_unavailable",
    );
  });
  it.each(deliveries.filter((c) => c.valid))("accepts delivery receipt $name", (c) => {
    expect(() => validateDeliveryReceipt(c.batch, c.receipt)).not.toThrow();
  });
  it.each(deliveries.filter((c) => !c.valid))("rejects delivery receipt $name", (c) => {
    expect(() => validateDeliveryReceipt(c.batch, c.receipt)).toThrow("unavailable");
  });
  it.each([
    NaN,
    Infinity,
    9007199254740992,
    undefined,
    "\ud800",
    { "non ascii é": 1 },
    { ["k".repeat(65)]: 1 },
  ])("rejects invalid digest input %#", (value) => {
    expect(() => auditDigest({ value })).toThrow("invalid_argument");
  });
  it("bounds digest input size and recursion", () => {
    expect(() => auditDigest({ value: "x".repeat(1048576) })).toThrow("invalid_argument");
    let value: unknown = null;
    for (let i = 0; i < 17; i++) value = [value];
    expect(() => auditDigest({ value })).toThrow("invalid_argument");
  });
});

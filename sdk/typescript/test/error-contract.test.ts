import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import {
  checkCancellation,
  errorCodes,
  HeimdallError,
  transportFailure,
} from "../src/internal/errors.js";

const fixture = (name: string): unknown =>
  JSON.parse(
    readFileSync(new URL("../../../contracts/fixtures/errors/" + name, import.meta.url), "utf8"),
  );
type Case = {
  name: string;
  reason: "dependency" | "deadline" | "cancelled";
  mutationMayExecute: boolean;
  operationId: string;
  expected: { code: string; operationId: string };
};
const cases = fixture("transport-v1.json") as Case[];

describe("shared error contract", () => {
  it("contains the complete stable code set", () => {
    expect(errorCodes).toEqual(fixture("codes-v1.json"));
  });
  it.each(cases)("$name", (v) => {
    expect(transportFailure(v.reason, v.mutationMayExecute, v.operationId).toJSON()).toEqual(
      v.expected,
    );
  });
  it("keeps a live request usable", () => {
    expect(() => checkCancellation(new AbortController().signal)).not.toThrow();
  });
  it("never retains an arbitrary abort reason", () => {
    const controller = new AbortController();
    controller.abort("Bearer private-token");
    expect(() => checkCancellation(controller.signal)).toThrow("cancelled");
    let captured: unknown;
    try {
      checkCancellation(controller.signal);
    } catch (error) {
      captured = error;
    }
    expect(JSON.stringify(captured)).toBe('{"code":"cancelled"}');
  });
  it("distinguishes a timeout from cancellation", () => {
    const controller = new AbortController();
    controller.abort(new DOMException("private provider text", "TimeoutError"));
    expect(() => checkCancellation(controller.signal)).toThrow("deadline_exceeded");
  });
  it("rejects invalid operation metadata without reflecting it", () => {
    expect(new HeimdallError("outcome_unknown", "Bearer private-token").toJSON()).toEqual({
      code: "invalid_argument",
    });
  });
  it.each(["op-123\n", "op-123\r", "op-123\u2028"])("rejects trailing separators %j", (value) => {
    expect(new HeimdallError("outcome_unknown", value).code).toBe("invalid_argument");
  });
  it("preserves an operation ID for recovery", () => {
    expect(new HeimdallError("outcome_unknown", "op-123").toJSON()).toEqual({
      code: "outcome_unknown",
      operationId: "op-123",
    });
  });
});

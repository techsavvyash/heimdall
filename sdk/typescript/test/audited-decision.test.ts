import { readFileSync } from "node:fs";
import { Ajv2020 } from "ajv/dist/2020.js";
import { expect, it } from "vitest";
import { AuditedDecision } from "../src/internal/audited-decision.js";
import { auditDigest } from "../src/internal/audit-receipts.js";
import { HeimdallError } from "../src/internal/errors.js";
const read = (name: string) =>
  JSON.parse(readFileSync(new URL(`../../../contracts/${name}`, import.meta.url), "utf8"));
const shape = new Ajv2020({ strict: true }).compile(read("audit-event.schema.json"));
const validate = (event: Record<string, unknown>) => {
  if (!shape(event)) throw new HeimdallError("invalid_argument");
};
const fixture = read("fixtures/audit-digests-v1.json")[0].event;
const cases = read("fixtures/audited-decision.json") as {
  name: string;
  value?: boolean;
  outcome: string;
  error: string;
}[];
function receipt(event: Record<string, unknown>) {
  return {
    productId: event.productId as string,
    environment: event.environment as string,
    eventId: event.eventId as string,
    recordedAt: event.recordedAt as number,
    payloadDigest: auditDigest(event),
  };
}
it.each(cases)("durably orders $name", async (c) => {
  const order: string[] = [];
  const events: Record<string, unknown>[] = [];
  const runner = new AuditedDecision(
    "ledger",
    "production",
    async (event) => {
      order.push(event.outcome as string);
      events.push(event);
      return receipt(event);
    },
    validate,
    () => 2000,
  );
  const result = await runner
    .run(fixture, async () => {
      order.push("evaluate");
      if (c.name === "arbitrary error") throw new Error("sensitive token");
      if (c.error)
        throw new HeimdallError(c.error as ConstructorParameters<typeof HeimdallError>[0]);
      return c.value!;
    })
    .then(
      (value) => ({ value }),
      (error) => ({ error: error.code }),
    );
  expect(result).toEqual(c.error ? { error: c.error } : { value: c.value });
  expect(order).toEqual(["requested", "evaluate", c.outcome]);
  expect(events[1]!.decisionId).toBe(events[0]!.decisionId);
  expect(events[0]!.decisionId).not.toBe(fixture.decisionId);
  expect(events[1]!.eventId).not.toBe(events[0]!.eventId);
  expect(JSON.stringify(events)).not.toContain("sensitive token");
});
it.each([1, 2])("blocks results when append %i fails", async (failure) => {
  let calls = 0;
  let evaluated = false;
  const runner = new AuditedDecision(
    "ledger",
    "production",
    async (e) => {
      if (++calls === failure) throw new Error("secret");
      return receipt(e);
    },
    validate,
  );
  await expect(
    runner.run(fixture, async () => {
      evaluated = true;
      return true;
    }),
  ).rejects.toThrow("audit_unavailable");
  expect(evaluated).toBe(failure === 2);
});
it.each(["productId", "environment", "eventId", "payloadDigest", "recordedAt"])(
  "rejects mismatched receipt %s",
  async (field) => {
    let evaluated = false;
    const runner = new AuditedDecision(
      "ledger",
      "production",
      async (e) => ({ ...receipt(e), [field]: field === "recordedAt" ? 0 : "wrong" }),
      validate,
    );
    await expect(
      runner.run(fixture, async () => {
        evaluated = true;
        return true;
      }),
    ).rejects.toThrow("audit_unavailable");
    expect(evaluated).toBe(false);
  },
);
it("persists cancellation without evaluating and uses uncancelled audit contexts", async () => {
  const outcomes: unknown[] = [];
  let evaluated = false;
  const runner = new AuditedDecision(
    "ledger",
    "production",
    async (e, signal) => {
      expect(signal.aborted).toBe(false);
      outcomes.push(e.outcome);
      return receipt(e);
    },
    validate,
  );
  await expect(
    runner.run(
      fixture,
      async () => {
        evaluated = true;
        return true;
      },
      AbortSignal.abort(),
    ),
  ).rejects.toThrow("cancelled");
  expect(evaluated).toBe(false);
  expect(outcomes).toEqual(["requested", "cancelled"]);
});
it("does not release a late positive from a non-cooperative evaluator", async () => {
  const outcomes: unknown[] = [];
  const runner = new AuditedDecision(
    "ledger",
    "production",
    async (e) => {
      outcomes.push(e.outcome);
      return receipt(e);
    },
    validate,
  );
  await expect(
    runner.run(fixture, () => new Promise(() => {}), AbortSignal.timeout(20)),
  ).rejects.toThrow("deadline_exceeded");
  expect(outcomes).toEqual(["requested", "unavailable"]);
});
it("snapshots input and rejects cross-product requests before append", async () => {
  let calls = 0;
  const request = structuredClone(fixture);
  const runner = new AuditedDecision(
    "ledger",
    "production",
    async (e) => {
      calls++;
      request.actor.subject = "changed";
      expect((e.actor as { subject: string }).subject).toBe("maya");
      return receipt(e);
    },
    validate,
  );
  await expect(runner.run(request, async () => true)).resolves.toBe(true);
  await expect(runner.run({ ...fixture, productId: "support" }, async () => true)).rejects.toThrow(
    "invalid_argument",
  );
  expect(calls).toBe(2);
});
it("withholds an allow until its terminal receipt and honors cancellation during commit", async () => {
  let release!: () => void;
  let started!: () => void;
  const terminal = new Promise<void>((resolve) => {
    started = resolve;
  });
  const waiting = new Promise<void>((resolve) => {
    release = resolve;
  });
  const controller = new AbortController();
  let settled = false;
  const runner = new AuditedDecision(
    "ledger",
    "production",
    async (e) => {
      if (e.outcome === "allow") {
        started();
        await waiting;
      }
      return receipt(e);
    },
    validate,
  );
  const running = runner
    .run(fixture, async () => true, controller.signal)
    .finally(() => {
      settled = true;
    });
  await terminal;
  expect(settled).toBe(false);
  controller.abort();
  release();
  await expect(running).rejects.toThrow("cancelled");
});

import { readFileSync } from "node:fs";
import { expect, test } from "vitest";
import { buildRetentionIdentities } from "../src/internal/retention-identities.js";
import { auditDigest } from "../src/internal/audit-receipts.js";
const fixture = JSON.parse(
  readFileSync(
    new URL("../../../contracts/fixtures/retention-identity-validation.json", import.meta.url),
    "utf8",
  ),
);
for (const c of fixture.cases) {
  const prepare = () => {
    const scope = { ...fixture.scope, ...c.scopePatch };
    const sources = c.bases.map((base: string, index: number) => {
      const event = {
        ...structuredClone(fixture.events[base]),
        ...(index === c.patchIndex ? c.eventPatch : {}),
      };
      return {
        event,
        digest: c.badDigest ? "0".repeat(64) : auditDigest(event),
        routing: Object.hasOwn(c, "routing") ? c.routing : ["z-sink", "a-sink"],
        deliveryRows: c.deliveryRows ?? 2,
      };
    });
    return { scope, sources };
  };
  Object.assign(c, prepare());
}
for (const c of fixture.cases.filter((c: { valid: boolean }) => !c.valid))
  test(`retention identities refuse: ${c.name}`, () => {
    const { scope, sources } = c;
    const before = structuredClone(sources);
    expect(() => buildRetentionIdentities(scope, c.resolution, sources)).toThrow(
      "audit_unavailable",
    );
    expect(sources).toEqual(before);
  });
for (const c of fixture.cases.filter((c: { valid: boolean }) => c.valid))
  test(`retention identities: ${c.name}`, () => {
    const { scope, sources } = c;
    const before = structuredClone(sources);
    const result = buildRetentionIdentities(scope, c.resolution, sources);
    expect(result.map((r) => r.eventId)).toEqual(
      sources.map((s: { event: { eventId: string } }) => s.event.eventId).sort(),
    );
    for (const identity of result) {
      const source = sources.find(
        (s: { event: { eventId: string } }) => s.event.eventId === identity.eventId,
      );
      expect(identity).toMatchObject({
        ...scope,
        recordedAt: source.event.recordedAt,
        digest: source.digest,
        routing: ["a-sink", "z-sink"],
      });
      expect(identity.kind).toBe(
        source.event.eventType === "heimdall.authorization.requested"
          ? "request"
          : source.event.eventType === "heimdall.authorization.completed"
            ? "terminal"
            : source.event.eventType === "heimdall.recovery.completed"
              ? "recovery"
              : "ordinary",
      );
      expect(identity.decisionId).toBe(
        identity.kind === "ordinary" ? null : source.event.decisionId,
      );
    }
    result[0]!.routing.push("changed");
    expect(sources).toEqual(before);
  });

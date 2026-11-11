import { validateAuditShape } from "./audit-shape.js";
import { validateAuditEvent } from "./audit.js";
import { auditDigest } from "./audit-receipts.js";
import { bindAuditDecision } from "./audit-decision-binding.js";
import { validateRecoveryPair } from "./audit-recovery.js";
import { HeimdallError } from "./errors.js";

type Scope = { productId: string; environment: string; tenantId: string | null };
type Source = {
  event: Record<string, unknown>;
  digest: string;
  routing: string[];
  deliveryRows: number;
};
const token = (s: unknown, max: number): s is string =>
  typeof s === "string" && s.length > 0 && s.length <= max && !/[^A-Za-z0-9_-]/.test(s);
// Built-in history only. Call after decoding ha1; no storage mutation or purge authorization.
export function buildRetentionIdentities(scope: Scope, resolution: string, sources: Source[]) {
  if (
    !scope ||
    !token(scope.productId, 64) ||
    !token(scope.environment, 64) ||
    (scope.tenantId !== null && !token(scope.tenantId, 128))
  )
    throw new HeimdallError("invalid_argument");
  try {
    const size = { ordinary: 1, pending: 1, completed: 2, recovered: 3 };
    if (
      !Object.hasOwn(size, resolution) ||
      !Array.isArray(sources) ||
      sources.length !== size[resolution as keyof typeof size]
    )
      throw new Error();
    const events: Record<string, unknown>[] = [];
    const ids = new Set<string>();
    const identities = sources.map((source) => {
      validateAuditShape(source.event);
      validateAuditEvent(
        source.event as unknown as Parameters<typeof validateAuditEvent>[0],
        "sdk",
        () => false,
      );
      const e = source.event;
      if (
        e.productId !== scope.productId ||
        e.environment !== scope.environment ||
        (e.tenantId ?? null) !== scope.tenantId ||
        auditDigest(e) !== source.digest ||
        ids.has(e.eventId as string) ||
        !Array.isArray(source.routing) ||
        source.routing.length > 32 ||
        source.deliveryRows !== source.routing.length ||
        source.routing.some((s) => !token(s, 128)) ||
        new Set(source.routing).size !== source.routing.length
      )
        throw new Error();
      ids.add(e.eventId as string);
      events.push(e);
      const kind =
        e.eventType === "heimdall.authorization.requested"
          ? "request"
          : e.eventType === "heimdall.authorization.completed"
            ? "terminal"
            : e.eventType === "heimdall.recovery.completed" && e.action === "authorization.recover"
              ? "recovery"
              : "ordinary";
      return {
        ...scope,
        eventId: e.eventId as string,
        recordedAt: e.recordedAt as number,
        digest: source.digest,
        kind,
        decisionId: kind === "ordinary" ? null : (e.decisionId as string),
        routing: [...source.routing].sort(),
      };
    });
    if (resolution === "ordinary") {
      if (identities[0]!.kind !== "ordinary") throw new Error();
    } else {
      if (identities.some((i) => i.kind === "ordinary")) throw new Error();
      const binding = bindAuditDecision(
        scope.productId,
        scope.environment,
        events.filter((e) => e.eventType !== "heimdall.recovery.completed"),
      );
      if (binding.resolution !== resolution) throw new Error();
      if (resolution === "recovered") {
        const terminal = events.find((e) => e.eventType === "heimdall.authorization.completed")!;
        const admin = events.find((e) => e.eventType === "heimdall.recovery.completed")!;
        validateRecoveryPair(terminal, admin, binding.requestedAt);
      }
    }
    return identities.sort((a, b) => (a.eventId < b.eventId ? -1 : a.eventId > b.eventId ? 1 : 0));
  } catch {
    throw new HeimdallError("audit_unavailable");
  }
}

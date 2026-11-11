import { HeimdallError } from "./errors.js";
export const defaultAuditRetentionMs = 2_592_000_000;
type Scope = { productId: string; environment: string; tenantId?: string | null };
type Event = Scope & {
  eventId: string;
  recordedAt: number;
  deliveries: { sinkId: string; state: string; deliveredAt: number | null }[];
};
type Group = { resolution: string; holdState: string; retryState: string; events: Event[] };
const token = (s: unknown, max: number): s is string =>
  typeof s === "string" && s.length > 0 && s.length <= max && !/[^A-Za-z0-9_-]/.test(s);
const time = (n: unknown): n is number =>
  typeof n === "number" && Number.isSafeInteger(n) && n >= 0;
// Planning only: storage must validate complete history and atomically preserve
// receipts before deleting any eligible group. This is not purge authorization.
export function classifyAuditRetention(
  scope: Scope,
  group: Group,
  now: number,
  retainForMs = defaultAuditRetentionMs,
) {
  if (
    !scope ||
    !token(scope.productId, 64) ||
    !token(scope.environment, 64) ||
    (scope.tenantId !== null && scope.tenantId !== undefined && !token(scope.tenantId, 128)) ||
    !time(now) ||
    !time(retainForMs) ||
    retainForMs === 0
  )
    throw new HeimdallError("invalid_argument");
  const sizes: Record<string, number> = { ordinary: 1, pending: 1, completed: 2, recovered: 3 };
  if (
    !group ||
    !Object.hasOwn(sizes, group.resolution) ||
    !["clear", "held"].includes(group.holdState) ||
    !["clear", "protected"].includes(group.retryState) ||
    !Array.isArray(group.events) ||
    group.events.length !== sizes[group.resolution]
  )
    throw new HeimdallError("audit_unavailable");
  const ids = new Set<string>();
  let anchor = 0,
    undelivered = false;
  for (const event of group.events) {
    if (
      !event ||
      event.productId !== scope.productId ||
      event.environment !== scope.environment ||
      (event.tenantId ?? null) !== (scope.tenantId ?? null) ||
      !token(event.eventId, 128) ||
      ids.has(event.eventId) ||
      !time(event.recordedAt) ||
      !Array.isArray(event.deliveries) ||
      event.deliveries.length > 32
    )
      throw new HeimdallError("audit_unavailable");
    ids.add(event.eventId);
    anchor = Math.max(anchor, event.recordedAt);
    const sinks = new Set<string>();
    for (const delivery of event.deliveries) {
      if (
        !delivery ||
        !token(delivery.sinkId, 128) ||
        sinks.has(delivery.sinkId) ||
        !["pending", "delivered", "quarantined"].includes(delivery.state) ||
        (delivery.state === "delivered"
          ? !time(delivery.deliveredAt)
          : delivery.deliveredAt !== null && delivery.deliveredAt !== undefined)
      )
        throw new HeimdallError("audit_unavailable");
      sinks.add(delivery.sinkId);
      if (delivery.state === "delivered") anchor = Math.max(anchor, delivery.deliveredAt!);
      else undelivered = true;
    }
  }
  if (group.holdState === "held") return "retain_held";
  if (group.resolution === "pending") return "retain_unresolved";
  if (undelivered) return "retain_undelivered";
  if (group.retryState === "protected") return "retain_retry";
  // Subtraction is safe even when anchor + retention would overflow a JSON integer.
  if (anchor > now || now - anchor < retainForMs) return "retain_age";
  return "candidate";
}

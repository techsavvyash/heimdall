import { HeimdallError } from "./errors.js";

type Capacity = { events: number; envelopeBytes: number; deliveries: number };
type State = { used: Capacity; reserved: Capacity };
type Operation = "append" | "request" | "complete" | "recover";
const fields = ["events", "envelopeBytes", "deliveries"] as const;
const reservation: Capacity = { events: 2, envelopeBytes: 3000000, deliveries: 64 };
const zero: Capacity = { events: 0, envelopeBytes: 0, deliveries: 0 };

// Pure accounting only. A persistence transaction must bind one reservation to
// one decision and deduplicate its events before applying this transition.
export function transitionAuditCapacity(
  limits: Capacity,
  state: State,
  operation: Operation,
  charge: Capacity,
): State {
  if (!limits || fields.some((field) => !Number.isSafeInteger(limits[field]) || limits[field] < 1))
    throw new HeimdallError("invalid_argument");
  if (
    !state?.used ||
    !state.reserved ||
    fields.some(
      (field) =>
        !Number.isSafeInteger(state.used[field]) ||
        state.used[field] < 0 ||
        !Number.isSafeInteger(state.reserved[field]) ||
        state.reserved[field] < 0 ||
        state.used[field] > limits[field] ||
        state.reserved[field] > limits[field] - state.used[field],
    )
  )
    throw new HeimdallError("audit_unavailable");
  if (
    state.reserved.events % 2 !== 0 ||
    state.reserved.envelopeBytes % 3000000 !== 0 ||
    state.reserved.deliveries % 64 !== 0 ||
    state.reserved.events / 2 !== state.reserved.envelopeBytes / 3000000 ||
    state.reserved.events / 2 !== state.reserved.deliveries / 64 ||
    state.used.events < state.reserved.events / 2 ||
    state.used.envelopeBytes < state.used.events ||
    Math.ceil(state.used.envelopeBytes / 1500000) > state.used.events ||
    Math.ceil(state.used.deliveries / 32) > state.used.events
  )
    throw new HeimdallError("audit_unavailable");
  if (!["append", "request", "complete", "recover"].includes(operation))
    throw new HeimdallError("invalid_argument");
  const events = operation === "recover" ? 2 : 1;
  if (
    !charge ||
    charge.events !== events ||
    !Number.isSafeInteger(charge.envelopeBytes) ||
    charge.envelopeBytes < events ||
    charge.envelopeBytes > events * 1500000 ||
    !Number.isSafeInteger(charge.deliveries) ||
    charge.deliveries < 0 ||
    charge.deliveries > events * 32
  )
    throw new HeimdallError("invalid_argument");
  const added = operation === "request" ? reservation : zero;
  const removed = operation === "complete" || operation === "recover" ? reservation : zero;
  const next: State = { used: { ...state.used }, reserved: { ...state.reserved } };
  for (const field of fields) {
    if (state.reserved[field] < removed[field]) throw new HeimdallError("audit_unavailable");
    const remaining = state.reserved[field] - removed[field];
    const available = limits[field] - state.used[field] - remaining;
    // Subtraction avoids precision loss when a configured limit is MAX_SAFE_INTEGER.
    if (charge[field] > available || added[field] > available - charge[field])
      throw new HeimdallError("audit_unavailable");
    next.used[field] += charge[field];
    next.reserved[field] = remaining + added[field];
  }
  return next;
}

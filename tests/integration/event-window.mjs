import assert from "node:assert/strict";

// Probe algorithm, not a durable SDK collector. API timestamps retain nanosecond precision.
export function timestampNanos(value) {
  const match = /^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})(?:\.(\d{1,9}))?Z$/.exec(value);
  assert(match, "Expected UTC provider timestamp");
  return BigInt(Date.parse(`${match[1]}Z`)) * 1000000n + BigInt((match[2] ?? "").padEnd(9, "0"));
}
export function formatNanos(value) {
  return `${new Date(Number(value / 1000000000n) * 1000).toISOString().slice(0, 19)}.${String(value % 1000000000n).padStart(9, "0")}Z`;
}
export function eventKey(event) {
  return JSON.stringify([event.aggregate.type.type, event.aggregate.id, event.sequence]);
}
export async function collectWindow(read, since, until, limit, maxRequests = 128) {
  let requests = 0;
  const found = new Map();
  async function visit(lower, upper) {
    assert(
      ++requests <= maxRequests,
      "Event window request budget exhausted; checkpoint unchanged",
    );
    const events = await read({ since: formatNanos(lower), until: formatNanos(upper) }, limit);
    assert(events.length <= limit);
    for (const event of events) {
      const time = timestampNanos(event.creationDate);
      assert(time > lower && time < upper, "Provider violated exclusive time window");
    }
    if (events.length < limit) {
      for (const event of events) found.set(eventKey(event), event);
      return;
    }
    assert(upper - lower > 2000n, "Saturated timestamp boundary; checkpoint unchanged");
    const middle = ((lower + upper) / 2000n) * 1000n;
    // Both API bounds are exclusive. Overlap one microsecond so the midpoint is retained.
    await visit(lower, middle + 1000n);
    await visit(middle, upper);
  }
  await visit(since, until);
  return { events: [...found.values()], requests };
}

import assert from "node:assert/strict";
import { randomUUID } from "node:crypto";
import { setTimeout } from "node:timers/promises";
import type { SessionStore } from "./index.js";

/** Run against two clients of the same disposable test backend. */
export async function verifySessionStore(first: SessionStore, second: SessionStore): Promise<void> {
  const prefix = `heimdall.v1|conformance|test|session|`;
  const key = prefix + randomUUID().replaceAll("-", "").repeat(2);
  const signal = AbortSignal.timeout(15_000);
  const value = Buffer.from([0, 42, 255]);
  const next = Buffer.from([255, 42, 0]);
  const revisions = new Set<string>();
  const recordRevision = (revision: string | null) => {
    assert(
      revision && /^[1-9][0-9]{0,18}$/.test(revision) && BigInt(revision) <= 9223372036854775807n,
      "canonical revision",
    );
    assert(!revisions.has(revision), "revision reuse");
    revisions.add(revision);
    return revision;
  };
  try {
    const expires = Date.now() + 60_000;
    assert.equal(await first.read(key, signal), null, "missing record");
    assert.equal(await first.compareAndSwap(key, "1", value, expires, signal), null, "missing CAS");
    const creates = await Promise.all(
      [first, second].map((store) => store.create(key, value, expires, signal)),
    );
    assert.equal(creates.filter((r) => r !== null).length, 1, "single create winner");
    const initial = recordRevision(creates.find((r) => r !== null) ?? null);
    const row = await second.read(key, signal);
    assert(row && row.revision === initial && row.expiresAt === expires, "cross-client read");
    assert.deepEqual(Buffer.from(row.value), value);
    const swaps = await Promise.all(
      [first, second].map((store) => store.compareAndSwap(key, initial, next, expires, signal)),
    );
    assert.equal(swaps.filter((r) => r !== null).length, 1, "single CAS winner");
    const updated = recordRevision(swaps.find((r) => r !== null) ?? null);
    const latest = await first.read(key, signal);
    assert(
      latest && latest.revision === updated && latest.expiresAt === expires,
      "committed CAS result",
    );
    assert.deepEqual(Buffer.from(latest.value), next);
    await first.delete(key, signal);
    await second.delete(key, signal);
    assert.equal(
      await first.compareAndSwap(key, updated, next, expires, signal),
      null,
      "CAS after delete",
    );
    const shortExpiry = Date.now() + 1000;
    const shortRevision = recordRevision(await first.create(key, value, shortExpiry, signal));
    await setTimeout(Math.max(0, shortExpiry - Date.now()) + 25, undefined, { signal });
    assert.equal(await second.read(key, signal), null, "expired read");
    assert.equal(
      await second.compareAndSwap(key, shortRevision, next, expires, signal),
      null,
      "expired CAS",
    );
    recordRevision(await second.create(key, next, expires, signal));
  } finally {
    await first.delete(key, AbortSignal.timeout(5000));
  }
}

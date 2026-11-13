import { readFileSync } from "node:fs";
import { expect, test } from "vitest";
import { customSessionStore } from "../src/internal/custom-session-store.js";
import type { SessionStore, StoredSessionRecord } from "../src/index.js";

const f = JSON.parse(
  readFileSync(
    new URL("../../../contracts/fixtures/custom-session-store.json", import.meta.url),
    "utf8",
  ),
) as Record<string, string> & { invalidRevisions: string[] };

test("custom browser records encrypt and isolate data with atomic revisions", async () => {
  const rows = new Map<string, StoredSessionRecord>();
  let sequence = 0;
  let reservations = 0;
  const create = async (key: string, value: Uint8Array, expiresAt: number) => {
    const revision = String(++sequence);
    rows.set(key, { value, expiresAt, revision });
    return revision;
  };
  const store: SessionStore = {
    create: async (key, value, expiresAt) => (rows.has(key) ? null : create(key, value, expiresAt)),
    read: async (key) => rows.get(key) ?? null,
    compareAndSwap: async (key, revision, value, expiresAt) =>
      rows.get(key)?.revision === revision ? create(key, value, expiresAt) : null,
    delete: async (key) => {
      rows.delete(key);
    },
  };
  const keys = { old: Buffer.alloc(32, 7), next: Buffer.alloc(32, 8) };
  const options = {
    store,
    keys,
    activeKeyId: "old",
    reserveKeyUsage: async () => {
      reservations++;
    },
  };
  const first = customSessionStore(f.productId!, f.environment!, options);
  const second = customSessionStore(f.productId!, f.environment!, {
    ...options,
    activeKeyId: "next",
  });
  const other = customSessionStore(f.productId!, f.alternateEnvironment!, options);
  const value = Buffer.from(f.secret!);
  const expires = Date.now() + 30_000;
  const key = f.recordKey!;
  const sessionKey = f.sessionStorageKey!;
  await first.create("login", key, value, expires);
  const initial = (await first.createSession(key, value, expires))!;
  expect([...rows.keys()]).toEqual([f.loginStorageKey, sessionKey]);
  expect(Buffer.from(rows.get(sessionKey)!.value).toString()).toMatch(/^hr1\.old\./);
  expect(Buffer.from(rows.get(sessionKey)!.value).includes(value)).toBe(false);
  expect((await second.getSession(key))?.value).toEqual(value);
  expect(await other.getSession(key)).toBeNull();
  const winners = await Promise.all([
    first.compareAndSwapSession(key, initial.revision, value, expires),
    second.compareAndSwapSession(key, initial.revision, value, expires),
  ]);
  expect(winners.filter(Boolean)).toHaveLength(1);
  expect(reservations).toBe(4);
  const stored = rows.get(sessionKey)!;
  for (const revision of f.invalidRevisions) {
    rows.set(sessionKey, { ...stored, revision });
    await expect(first.getSession(key)).rejects.toThrow("unavailable");
  }
  rows.set(sessionKey, { ...stored, expiresAt: stored.expiresAt + 1 });
  await expect(first.getSession(key)).rejects.toThrow("unavailable");
  rows.set(sessionKey, rows.get(f.loginStorageKey!)!);
  await expect(first.getSession(key)).rejects.toThrow("unavailable");
  rows.set(sessionKey, stored);
  await second.deleteSession(key);
  expect(await first.compareAndSwapSession(key, stored.revision, value, expires)).toBeNull();
  const recreated = await second.createSession(key, value, expires);
  expect(recreated?.revision).not.toBe(initial.revision);
  expect(Buffer.from(rows.get(sessionKey)!.value).toString()).toMatch(/^hr1\.next\./);
  expect((await first.getSession(key))?.value).toEqual(value);
  const [{ record }] = JSON.parse(
    readFileSync(new URL("../../../contracts/fixtures/session-v1.json", import.meta.url), "utf8"),
  );
  const payload = Buffer.from(JSON.stringify({ ...record, idleExpiresAt: expires }));
  const session = (await first.createSession("invalidate", payload, expires))!;
  const updated = (await second.compareAndSwapSession(
    "invalidate",
    session.revision,
    payload,
    expires,
  ))!;
  const signal = new AbortController().signal;
  await first.invalidateSessionVersion("invalidate", session.revision, record, signal);
  expect((await second.getSession("invalidate"))?.revision).toBe(updated.revision);
  await first.invalidateSessionVersion("invalidate", updated.revision, record, signal);
  expect(await second.getSession("invalidate")).toBeNull();
});

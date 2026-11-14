import type { SessionStore, StoredSessionRecord } from "../../src/index.js";

export function memorySessionStore() {
  const rows = new Map<string, StoredSessionRecord>();
  let sequence = 0;
  const put = (key: string, value: Uint8Array, expiresAt: number) => {
    const revision = String(++sequence);
    rows.set(key, { value: Uint8Array.from(value), revision, expiresAt });
    return revision;
  };
  const current = (key: string) => {
    const row = rows.get(key);
    return row && row.expiresAt > Date.now() ? row : null;
  };
  const store: SessionStore = {
    create: async (key, value, expiresAt) => (current(key) ? null : put(key, value, expiresAt)),
    read: async (key) => current(key),
    compareAndSwap: async (key, revision, value, expiresAt) =>
      current(key)?.revision === revision ? put(key, value, expiresAt) : null,
    delete: async (key) => {
      rows.delete(key);
    },
  };
  return { store, rows };
}

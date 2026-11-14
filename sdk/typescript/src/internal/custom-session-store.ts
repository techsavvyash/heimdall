import { createHash } from "node:crypto";
import { RecordCipher } from "./envelopes.js";
import { HeimdallError, checkCancellation } from "./errors.js";
import { bounded, known } from "./audited-decision.js";
import { sessionIdentityContext } from "./identity-context.js";
import { readSession } from "./browser-session.js";

type Kind = "login" | "session";
export type StoredSessionRecord = {
  value: Uint8Array;
  revision: string;
  expiresAt: number;
};
export interface SessionStore {
  create(
    key: string,
    value: Uint8Array,
    expiresAt: number,
    signal: AbortSignal,
  ): Promise<string | null>;
  read(key: string, signal: AbortSignal): Promise<StoredSessionRecord | null>;
  compareAndSwap(
    key: string,
    revision: string,
    value: Uint8Array,
    expiresAt: number,
    signal: AbortSignal,
  ): Promise<string | null>;
  delete(key: string, signal: AbortSignal): Promise<void>;
}
export type SessionStoreOptions = {
  store: SessionStore;
  activeKeyId: string;
  keys: Readonly<Record<string, Uint8Array>>;
  reserveKeyUsage(fingerprint: string, signal: AbortSignal): Promise<void>;
};

export function customSessionStore(
  productId: string,
  environment: string,
  options: SessionStoreOptions,
) {
  const { store } = options;
  if (
    typeof store?.create !== "function" ||
    typeof store?.read !== "function" ||
    typeof store?.compareAndSwap !== "function" ||
    typeof store?.delete !== "function" ||
    typeof options.reserveKeyUsage !== "function"
  )
    throw new HeimdallError("invalid_argument");
  const create = store.create.bind(store);
  const read = store.read.bind(store);
  const swap = store.compareAndSwap.bind(store);
  const remove = store.delete.bind(store);
  const reserve = options.reserveKeyUsage.bind(options);
  const cipher = new RecordCipher(options.activeKeyId, options.keys);
  const fingerprint = cipher.activeFingerprint();

  const keyFor = (kind: Kind, key: string) => {
    const recordKey = createHash("sha256").update(key, "ascii").digest("hex");
    return {
      key: `heimdall.v1|${productId}|${environment}|${kind}|${recordKey}`,
      scope: { productId, environment, kind, recordKey },
    };
  };
  const run = async <T>(
    signal: AbortSignal | undefined,
    action: (operation: AbortSignal) => Promise<T>,
    write = false,
  ) => {
    const timeout = AbortSignal.timeout(5000);
    const operation = signal ? AbortSignal.any([signal, timeout]) : timeout;
    checkCancellation(operation);
    try {
      const result = await bounded(action, operation);
      checkCancellation(operation);
      return result;
    } catch (error) {
      if (write && operation.aborted) throw new HeimdallError("outcome_unknown");
      throw known(error);
    }
  };
  const validRevision = (key: string, revision: string, previous?: string) => {
    try {
      sessionIdentityContext(key, revision);
      if (revision === previous) throw new Error();
    } catch {
      throw new HeimdallError("unavailable");
    }
  };
  const write = async (
    kind: Kind,
    key: string,
    value: Uint8Array,
    expiresAt: number,
    previous: string | null,
    signal?: AbortSignal,
  ) => {
    const record = keyFor(kind, key);
    await run(signal, (operation) => reserve(fingerprint, operation));
    if (expiresAt <= Date.now()) return null;
    const plaintext = Buffer.from(
      JSON.stringify([expiresAt, Buffer.from(value).toString("base64url")]),
    );
    const envelope = Buffer.from(cipher.seal(record.scope, plaintext), "ascii");
    const revision = await run(
      signal,
      (operation) =>
        previous === null
          ? create(record.key, envelope, expiresAt, operation)
          : swap(record.key, previous, envelope, expiresAt, operation),
      true,
    );
    if (revision === null) return null;
    validRevision(key, revision, previous ?? undefined);
    return { revision, expiresAt, value: Buffer.from(value) };
  };
  const get = async (kind: Kind, key: string, signal?: AbortSignal) => {
    const record = keyFor(kind, key);
    const stored = await run(signal, (operation) => read(record.key, operation));
    if (stored === null) return null;
    try {
      validRevision(key, stored.revision);
      if (!(stored.value instanceof Uint8Array) || !Number.isSafeInteger(stored.expiresAt))
        throw new Error();
      const decoded: unknown = JSON.parse(
        new TextDecoder("utf-8", { fatal: true }).decode(
          cipher.open(record.scope, Buffer.from(stored.value).toString("latin1")),
        ),
      );
      if (
        !Array.isArray(decoded) ||
        decoded.length !== 2 ||
        decoded[0] !== stored.expiresAt ||
        typeof decoded[1] !== "string"
      )
        throw new Error();
      const value = Buffer.from(decoded[1], "base64url");
      if (value.toString("base64url") !== decoded[1]) throw new Error();
      return stored.expiresAt > Date.now() && value.length ? { ...stored, value } : null;
    } catch {
      throw new HeimdallError("unavailable");
    }
  };
  return {
    async invalidateSessionVersion(
      key: string,
      revision: string,
      identity: { issuer: string; subject: string },
      signal: AbortSignal,
    ) {
      const record = await get("session", key, signal);
      if (!record || record.revision !== revision) return;
      const session = readSession(record.value, record.expiresAt);
      if (session.issuer !== identity.issuer || session.subject !== identity.subject)
        throw new HeimdallError("unavailable");
      // Replacing the observed version cannot delete a concurrent refresh result.
      await write("session", key, new Uint8Array(), record.expiresAt, revision, signal);
    },
    matchesScope: (product: string, env: string) => product === productId && env === environment,
    create: (kind: Kind, key: string, value: Uint8Array, expiresAt: number, signal?: AbortSignal) =>
      write(kind, key, value, expiresAt, null, signal),
    get,
    compareAndSwap: (
      kind: Kind,
      key: string,
      revision: string,
      value: Uint8Array,
      expiresAt: number,
      signal?: AbortSignal,
    ) => write(kind, key, value, expiresAt, revision, signal),
    createSession: (key: string, value: Uint8Array, expiresAt: number, signal?: AbortSignal) =>
      write("session", key, value, expiresAt, null, signal),
    getSession: (key: string, signal?: AbortSignal) => get("session", key, signal),
    compareAndSwapSession: (
      key: string,
      revision: string,
      value: Uint8Array,
      expiresAt: number,
      signal?: AbortSignal,
    ) => write("session", key, value, expiresAt, revision, signal),
    deleteSession: (key: string, signal?: AbortSignal) =>
      run(signal, (operation) => remove(keyFor("session", key).key, operation), true),
  };
}

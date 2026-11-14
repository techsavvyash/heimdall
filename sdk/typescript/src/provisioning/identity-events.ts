import { known } from "../internal/audited-decision.js";
import { checkCancellation, HeimdallError } from "../internal/errors.js";
import { providerId, providerRequest } from "./provider-request.js";

export type IdentityEventReaderOptions = {
  issuer: string;
  organizationId: string;
  token: string;
  transportProfile?: "production" | "loopback";
  pageSize?: number;
};

export type IdentityEvent = {
  organizationId: string;
  aggregateType: string;
  aggregateId: string;
  type: string;
  sequence: string;
  creationDate: string;
};

type ProviderEvent = {
  aggregate?: { type?: { type?: string }; id?: string; resourceOwner?: string };
  type?: { type?: string };
  creationDate?: string;
  sequence?: string;
};

const microsPerSecond = 1000000n;
const eventType = (value: unknown): value is string =>
  typeof value === "string" && /^[A-Za-z0-9_.-]{1,128}(?![\s\S])/.test(value);

function parseTime(value: unknown): bigint | undefined {
  if (typeof value !== "string") return;
  const parts = /^(\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d)(?:\.(\d{1,9}))?Z(?![\s\S])/.exec(value);
  if (!parts) return;
  const millis = Date.parse(parts[1] + "Z");
  if (
    !Number.isFinite(millis) ||
    millis < 0 ||
    new Date(millis).toISOString().slice(0, 19) !== parts[1] ||
    /[1-9]/.test(parts[2]?.slice(6) ?? "")
  )
    return;
  return BigInt(millis) * 1000n + BigInt((parts[2] ?? "").slice(0, 6).padEnd(6, "0"));
}

function formatTime(micros: bigint): string {
  const seconds = new Date(Number(micros / microsPerSecond) * 1000).toISOString().slice(0, 19);
  return seconds + "." + String(micros % microsPerSecond).padStart(6, "0") + "Z";
}

/** Read an explicit, closed historical window using the pinned ZITADEL event API. */
export function createIdentityEventReader(options: IdentityEventReaderOptions) {
  const {
    issuer,
    organizationId,
    token,
    transportProfile = "production",
    pageSize = 100,
  } = options;
  if (!providerId(organizationId) || !Number.isInteger(pageSize) || pageSize < 1 || pageSize > 100)
    throw new HeimdallError("invalid_argument");
  providerRequest(issuer, token, transportProfile, new AbortController().signal);

  return Object.freeze({
    async readWindow(since: string, until: string, signal: AbortSignal) {
      const lower = parseTime(since);
      const upper = parseTime(until);
      if (
        !(signal instanceof AbortSignal) ||
        lower === undefined ||
        upper === undefined ||
        lower >= upper
      )
        throw new HeimdallError("invalid_argument");
      const operation = AbortSignal.any([signal, AbortSignal.timeout(30000)]);
      const request = providerRequest(issuer, token, transportProfile, operation);
      const observed = new Map<string, { event: IdentityEvent; confirmed: boolean }>();
      const pending = [{ lower, upper }];
      let requests = 0;
      try {
        while (pending.length) {
          checkCancellation(operation);
          if (++requests > 128) throw new HeimdallError("unavailable");
          const range = pending.pop()!;
          const page = await request<{ events?: ProviderEvent[] }>("/admin/v1/events/_search", {
            resourceOwner: organizationId,
            asc: true,
            limit: pageSize,
            range: { since: formatTime(range.lower), until: formatTime(range.upper) },
          });
          if (!page || typeof page !== "object" || Array.isArray(page))
            throw new HeimdallError("unavailable");
          // ZITADEL omits an empty protobuf repeated field from JSON.
          const rows = Object.keys(page).length === 0 ? [] : page.events;
          if (!Array.isArray(rows) || rows.length > pageSize)
            throw new HeimdallError("unavailable");
          const complete = rows.length < pageSize;
          const pageKeys = new Set<string>();
          for (const raw of rows) {
            const aggregate = raw?.aggregate;
            const time = parseTime(raw?.creationDate);
            const sequence = raw?.sequence;
            const type = raw?.type?.type;
            if (
              !providerId(aggregate?.resourceOwner) ||
              !providerId(aggregate.id) ||
              !eventType(aggregate.type?.type) ||
              !eventType(type) ||
              typeof sequence !== "string" ||
              !/^[1-9]\d{0,19}(?![\s\S])/.test(sequence) ||
              BigInt(sequence) > 18446744073709551615n ||
              time === undefined ||
              time <= range.lower ||
              time >= range.upper
            )
              throw new HeimdallError("unavailable");
            if (aggregate.resourceOwner !== organizationId) throw new HeimdallError("conflict");
            const aggregateType = aggregate.type.type;
            const key = JSON.stringify([aggregateType, aggregate.id, sequence]);
            if (pageKeys.has(key)) throw new HeimdallError("unavailable");
            pageKeys.add(key);
            const creationDate = formatTime(time);
            const previous = observed.get(key);
            if (
              previous &&
              (previous.event.creationDate !== creationDate || previous.event.type !== type)
            )
              throw new HeimdallError("conflict");
            observed.set(key, {
              event: {
                organizationId,
                aggregateType,
                aggregateId: aggregate.id,
                type,
                sequence,
                creationDate,
              },
              confirmed: complete || previous?.confirmed === true,
            });
          }
          if (!complete) {
            if (range.upper - range.lower <= 2n) throw new HeimdallError("unavailable");
            const middle = (range.lower + range.upper) / 2n;
            // Both bounds are exclusive. Include the midpoint in the left subwindow.
            pending.push(
              { lower: middle, upper: range.upper },
              { lower: range.lower, upper: middle + 1n },
            );
          }
        }
        checkCancellation(operation);
        const events = [...observed.values()].map(({ event, confirmed }) => {
          if (!confirmed) throw new HeimdallError("unavailable");
          return event;
        });
        events.sort((a, b) => {
          const left = [
            a.creationDate,
            a.aggregateType,
            a.aggregateId,
            a.sequence.padStart(20, "0"),
          ].join(" ");
          const right = [
            b.creationDate,
            b.aggregateType,
            b.aggregateId,
            b.sequence.padStart(20, "0"),
          ].join(" ");
          return left < right ? -1 : left > right ? 1 : 0;
        });
        return { events, requests };
      } catch (error) {
        throw known(error);
      }
    },
  });
}

import { readFileSync } from "node:fs";
import { afterEach, expect, it, vi } from "vitest";
import { createIdentityEventReader } from "../src/provisioning/index.js";

const fixture = JSON.parse(
  readFileSync(
    new URL("../../../contracts/fixtures/identity-event-window.json", import.meta.url),
    "utf8",
  ),
) as {
  since: string;
  until: string;
  cases: {
    name: string;
    events: [number, string, string | number, string][];
    expectedSequences?: string[];
    error?: string;
    requests: number;
  }[];
};
const options = {
  issuer: "https://example.test",
  organizationId: "org",
  token: "token",
  pageSize: 3,
};
const timestamp = (micros: number) =>
  fixture.since.replace("000000Z", String(micros).padStart(6, "0") + "Z");
afterEach(() => vi.unstubAllGlobals());

it.each(fixture.cases)("$name", async ({ events, expectedSequences, error, requests }) => {
  const fetch = vi.fn<(url: string, init: RequestInit) => Promise<Response>>(async (_url, init) => {
    const { range, ...request } = JSON.parse(init.body as string);
    expect(request).toEqual({ resourceOwner: "org", asc: true, limit: 3 });
    expect(init.redirect).toBe("error");
    if (events.length === 0) return Response.json({});
    return Response.json({
      events: events
        .map(([micros, id, sequence, resourceOwner]) => ({
          creationDate: timestamp(micros),
          aggregate: { id, resourceOwner, type: { type: "user" } },
          type: { type: "user.added" },
          sequence,
          payload: { neverExport: true },
        }))
        .filter((event) => event.creationDate > range.since && event.creationDate < range.until)
        .slice(0, 3),
    });
  });
  vi.stubGlobal("fetch", fetch);
  const read = createIdentityEventReader(options).readWindow(
    fixture.since,
    fixture.until,
    new AbortController().signal,
  );
  const observed = await read.then(
    (scan) => ({
      requests: scan.requests,
      sequences: scan.events.map((event) => event.sequence),
      fields: scan.events.map((event) => Object.keys(event).sort()),
    }),
    (failure) => ({ error: failure.code }),
  );
  const fields = [
    "aggregateId",
    "aggregateType",
    "creationDate",
    "organizationId",
    "sequence",
    "type",
  ];
  expect(observed).toEqual(
    error
      ? { error }
      : {
          requests,
          sequences: expectedSequences,
          fields: expectedSequences?.map(() => fields),
        },
  );
  expect(fetch).toHaveBeenCalledTimes(requests);
});

it("checks cancellation and timestamp precision before fetching", async () => {
  const fetch = vi.fn<typeof globalThis.fetch>();
  vi.stubGlobal("fetch", fetch);
  const reader = createIdentityEventReader(options);
  await expect(
    reader.readWindow(fixture.since, fixture.until, AbortSignal.abort()),
  ).rejects.toMatchObject({ code: "cancelled" });
  await expect(
    reader.readWindow(
      "2026-11-14T00:00:00.000000001Z",
      fixture.until,
      new AbortController().signal,
    ),
  ).rejects.toMatchObject({ code: "invalid_argument" });
  expect(fetch).not.toHaveBeenCalled();
});

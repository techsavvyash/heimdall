import { readFileSync } from "node:fs";
import { expect, test, vi } from "vitest";
import { createDirectoryReader } from "../src/provisioning/index.js";

type Fixture = {
  name: string;
  user?: string;
  owner?: string;
  scim?: object;
  listStatus?: number;
  expected: string;
  externalId?: string;
};
const { cases } = JSON.parse(
  readFileSync(
    new URL("../../../contracts/fixtures/directory-reader-v1.json", import.meta.url),
    "utf8",
  ),
) as { cases: Fixture[] };
const issuer = "https://zitadel.example";
const directory = issuer + "/scim/v2/org-a/Users";
const errors = new Set(["unavailable", "conflict"]);

test.each(cases)("$name", async (fixture) => {
  vi.stubGlobal("fetch", async (url: string) => {
    if (url === issuer + "/v2/users/employee")
      return fixture.user
        ? Response.json({
            user: {
              userId: "employee",
              details: { resourceOwner: fixture.owner ?? "org-a" },
              state: fixture.user,
              human: {},
            },
          })
        : new Response(null, { status: 404 });
    if (url === directory + "?startIndex=1&count=1")
      return Response.json(
        { startIndex: 1, totalResults: 0, Resources: [] },
        { status: fixture.listStatus ?? 200 },
      );
    if (url === directory + "/employee")
      return fixture.scim ? Response.json(fixture.scim) : new Response(null, { status: 404 });
    throw new Error("unexpected endpoint");
  });
  try {
    const reader = createDirectoryReader({ issuer, organizationId: "org-a", token: "viewer" });
    const result = await reader.read("employee", AbortSignal.timeout(5000)).then(
      (value) => ({
        state: value.state,
        externalId: "externalId" in value ? value.externalId : undefined,
      }),
      (error: { code?: string }) => ({ code: error.code }),
    );
    expect(result).toEqual(
      errors.has(fixture.expected)
        ? { code: fixture.expected }
        : { state: fixture.expected, externalId: fixture.externalId },
    );
  } finally {
    vi.unstubAllGlobals();
  }
});

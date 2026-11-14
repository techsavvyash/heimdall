import { readFileSync } from "node:fs";
import { createServer } from "node:http";
import { afterEach, expect, it } from "vitest";
import { PinnedModel } from "../src/internal/model-binding.js";
import type { compileTenantModel } from "../src/internal/tenant-model.js";
const read = (name: string) =>
  JSON.parse(
    readFileSync(new URL(`../../../contracts/fixtures/${name}.json`, import.meta.url), "utf8"),
  );
const fixture = read("tenant-model-v1")[0] as {
  manifest: Parameters<typeof compileTenantModel>[0];
  binding: Parameters<typeof compileTenantModel>[1];
  expected: ReturnType<typeof compileTenantModel>;
};
const cases = read("model-binding") as { name: string; response: unknown; accepted: boolean }[];
const close: (() => Promise<void>)[] = [];
afterEach(async () => {
  for (const fn of close.splice(0)) await fn();
});
async function server(body: unknown, status = 200) {
  const requests: string[] = [];
  const s = createServer((req, res) => {
    requests.push(`${req.method} ${req.url} ${req.headers.authorization}`);
    res.writeHead(status, { "Content-Type": "application/json" });
    res.end(JSON.stringify(body));
  });
  await new Promise<void>((r) => s.listen(0, "127.0.0.1", r));
  close.push(
    () =>
      new Promise<void>((r, e) => {
        s.close((err) => (err ? e(err) : r()));
        s.closeAllConnections();
      }),
  );
  const address = s.address();
  if (!address || typeof address === "string") throw Error();
  return { url: `http://127.0.0.1:${address.port}`, requests };
}
const config = (url: string) => ({
  url,
  storeId: "store",
  modelId: "pinned-model",
  profile: "loopback" as const,
});
it.each(cases)("checks pinned model: $name", async (c) => {
  const s = await server(c.response);
  const p = new PinnedModel(
    config(s.url),
    fixture.manifest,
    fixture.binding,
    fixture.expected,
    async () => "secret",
  );
  const outcome = await p.verify().then(
    (result) => ({ accepted: true, hash: result.modelHash, frozen: Object.isFrozen(result.roles) }),
    (error) => ({ accepted: false, code: error.code }),
  );
  expect(outcome).toEqual(
    c.accepted
      ? { accepted: true, hash: fixture.expected.modelHash, frozen: true }
      : { accepted: false, code: "unavailable" },
  );
  expect(s.requests).toEqual(["GET /stores/store/authorization-models/pinned-model Bearer secret"]);
});
it("rejects altered local artifacts before loading credentials", () => {
  const artifact = structuredClone(fixture.expected);
  artifact.modelHash = "wrong";
  expect(
    () =>
      new PinnedModel(
        config("http://127.0.0.1"),
        fixture.manifest,
        fixture.binding,
        artifact,
        async () => {
          throw Error("must not load");
        },
      ),
  ).toThrow("invalid_argument");
});
it("honors cancellation and redacts credential errors", async () => {
  const p = new PinnedModel(
    config("http://127.0.0.1"),
    fixture.manifest,
    fixture.binding,
    fixture.expected,
    async () => {
      throw Error("secret");
    },
  );
  await expect(p.verify(AbortSignal.abort())).rejects.toThrow("cancelled");
  await expect(p.verify()).rejects.toThrow("unavailable");
});
it("rejects missing models", async () => {
  const s = await server({}, 404);
  const p = new PinnedModel(
    config(s.url),
    fixture.manifest,
    fixture.binding,
    fixture.expected,
    async () => "secret",
  );
  await expect(p.verify()).rejects.toThrow("unavailable");
});

it("bounds a credential loader that does not settle", async () => {
  const p = new PinnedModel(
    config("http://127.0.0.1"),
    fixture.manifest,
    fixture.binding,
    fixture.expected,
    () => new Promise(() => {}),
  );
  await expect(p.verify(AbortSignal.timeout(20))).rejects.toThrow("deadline_exceeded");
});
it.each([301, 401, 403, 500])("fails closed for HTTP %i", async (status) => {
  const s = await server(cases[0]!.response, status);
  const p = new PinnedModel(
    config(s.url),
    fixture.manifest,
    fixture.binding,
    fixture.expected,
    async () => "secret",
  );
  await expect(p.verify()).rejects.toThrow("unavailable");
});
it("rejects changed role and permission mappings", () => {
  const role = structuredClone(fixture.expected);
  role.roles["viewer"]!.effective = "member";
  const permission = structuredClone(fixture.expected);
  permission.permissions["invoice.read"] = "member";
  for (const artifact of [role, permission])
    expect(
      () =>
        new PinnedModel(
          config("http://127.0.0.1"),
          fixture.manifest,
          fixture.binding,
          artifact,
          async () => "secret",
        ),
    ).toThrow("invalid_argument");
});

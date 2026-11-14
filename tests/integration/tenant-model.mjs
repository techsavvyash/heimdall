import { spawnSync } from "node:child_process";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { randomUUID } from "node:crypto";
const base = process.env.HEIMDALL_PROBE_OPENFGA_URL;
const key = process.env.HEIMDALL_PROBE_OPENFGA_KEY;
assert(base && key && new URL(base).protocol === "https:" && new URL(base).origin === base);
const cases = JSON.parse(
  readFileSync(new URL("../../contracts/fixtures/tenant-model-v1.json", import.meta.url), "utf8"),
);
const runId = randomUUID();
console.error(`Tenant model probe: ${runId}`);
const stores = [];
const observations = [];
async function api(path, body, method = body === undefined ? "GET" : "POST") {
  const r = await fetch(base + path, {
    method,
    headers: { authorization: `Bearer ${key}`, "content-type": "application/json" },
    ...(body === undefined ? {} : { body: JSON.stringify(body) }),
    redirect: "error",
    signal: AbortSignal.timeout(15000),
  });
  assert(r.ok, `${method} ${path}: ${r.status}`);
  return r.status === 204 ? null : r.json();
}
async function runChecks(checks) {
  for (let i = 0; i < checks.length; i += 4) {
    const results = await Promise.allSettled(checks.slice(i, i + 4).map((check) => check()));
    const failed = results.find((result) => result.status === "rejected");
    if (failed) throw failed.reason;
  }
}
try {
  for (const c of cases) {
    const store = await api("/stores", { name: `heimdall-model-${c.name}-${runId}` });
    stores.push(store.id);
    const model = await api(`/stores/${store.id}/authorization-models`, c.expected.model);
    if (process.env.HEIMDALL_PROBE_MODEL_BINDING === "1") {
      const env = {
        ...process.env,
        HEIMDALL_TEST_MODEL_BINDING: JSON.stringify({
          url: base,
          storeId: store.id,
          modelId: model.authorization_model_id,
          name: c.name,
        }),
      };
      for (const [command, args, cwd] of [
        ["pnpm", ["exec", "vitest", "run", "test/model-binding.live.test.ts"], "sdk/typescript"],
        [
          "go",
          ["test", "-race", "./internal/modelbinding", "-run", "^TestLiveBinding$", "-count=1"],
          "sdk/go",
        ],
      ]) {
        const result = spawnSync(command, args, { env, cwd, encoding: "utf8", timeout: 60000 });
        assert(result.status === 0, `SDK binding verification failed: ${command} ${c.name}`);
        observations.push({
          product: c.name,
          sdk: command === "go" ? "Go" : "TypeScript",
          pinnedModelVerified: true,
        });
      }
    }
    const roles = Object.keys(c.manifest.roles).sort();
    const tuples = [];
    const checks = [];
    const user = (mask) =>
      "user:" +
      Buffer.from(JSON.stringify(["https://id.example.invalid", `${c.name}-${mask}`])).toString(
        "base64url",
      );
    const addCheck = (principal, relation, object, allowed) =>
      checks.push(async () => {
        const result = await api(`/stores/${store.id}/check`, {
          authorization_model_id: model.authorization_model_id,
          tuple_key: { user: principal, relation, object },
          consistency: "HIGHER_CONSISTENCY",
        });
        assert.equal(result.allowed, allowed, `${c.name} ${principal} ${relation} ${object}`);
      });
    for (let mask = 0; mask < 2 ** (roles.length + 1); mask++) {
      const member = Boolean(mask & 1);
      const assigned = roles.filter((_, i) => Boolean(mask & (2 ** (i + 1))));
      for (const relation of [
        ...(member ? ["member"] : []),
        ...assigned.map((r) => c.expected.roles[r].assignment),
      ])
        tuples.push({ user: user(mask), relation, object: "workspace:acme" });
      for (const role of roles)
        addCheck(
          user(mask),
          c.expected.roles[role].effective,
          "workspace:acme",
          member && assigned.includes(role),
        );
      for (const target of roles) {
        const allowed =
          member &&
          target !== c.manifest.ownerRole &&
          assigned.some((actor) => (c.manifest.delegation[actor] ?? []).includes(target));
        addCheck(user(mask), c.expected.roles[target].delegate, "workspace:acme", allowed);
        addCheck(user(mask), c.expected.roles[target].delegate, "workspace:globex", false);
      }
      for (const permission of c.manifest.permissions) {
        const relation = c.expected.permissions[permission];
        addCheck(
          user(mask),
          relation,
          "workspace:acme",
          member && assigned.some((role) => c.manifest.roles[role].includes(permission)),
        );
        addCheck(user(mask), relation, "workspace:globex", false);
      }
    }
    for (let i = 0; i < tuples.length; i += 100)
      await api(`/stores/${store.id}/write`, {
        authorization_model_id: model.authorization_model_id,
        writes: { tuple_keys: tuples.slice(i, i + 100) },
      });
    await runChecks(checks);
    const allMask = 2 ** (roles.length + 1) - 1;
    await api(`/stores/${store.id}/write`, {
      authorization_model_id: model.authorization_model_id,
      deletes: {
        tuple_keys: [{ user: user(allMask), relation: "member", object: "workspace:acme" }],
      },
    });
    const before = checks.length;
    for (const relation of [
      ...Object.values(c.expected.permissions),
      ...Object.values(c.expected.roles).flatMap((r) => [r.effective, r.delegate]),
    ])
      addCheck(user(allMask), relation, "workspace:acme", false);
    const otherProduct = c.name === "ledger" ? "support" : "ledger";
    const otherUser =
      "user:" +
      Buffer.from(JSON.stringify(["https://id.example.invalid", `${otherProduct}-3`])).toString(
        "base64url",
      );
    for (const relation of Object.values(c.expected.permissions))
      addCheck(otherUser, relation, "workspace:acme", false);
    await runChecks(checks.slice(before));
    observations.push({
      product: c.name,
      modelHash: c.expected.modelHash,
      combinations: 2 ** (roles.length + 1),
      checks: checks.length,
      staleRoleDenial: true,
      delegationRequiresMembership: true,
      ordinaryOwnerAssignmentDenied: true,
      crossTenantDenial: true,
      crossProductPrincipalDenial: true,
    });
  }
} finally {
  const results = await Promise.allSettled(
    stores.map((id) => api(`/stores/${id}`, undefined, "DELETE")),
  );
  assert(
    results.every((r) => r.status === "fulfilled"),
    `Cleanup failed: ${runId}`,
  );
}
console.log(
  JSON.stringify(
    {
      suite: "tenant-model-v1",
      runId,
      observations,
      cleanup: "owned stores deleted",
      limitations: [
        "internal compiler and optional pinned-model guard only; runtime tuple mutation authorization and model migration not implemented",
      ],
    },
    null,
    2,
  ),
);

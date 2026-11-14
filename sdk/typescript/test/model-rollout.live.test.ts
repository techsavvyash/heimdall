import { execFile } from "node:child_process";
import { randomUUID } from "node:crypto";
import { readFileSync } from "node:fs";
import { promisify } from "node:util";
import { expect, it } from "vitest";
import { parseDeployment } from "../src/internal/configuration.js";
import { PinnedModel } from "../src/internal/model-binding.js";

const fixture = (name: string) =>
  JSON.parse(
    readFileSync(new URL(`../../../contracts/fixtures/${name}.json`, import.meta.url), "utf8"),
  );
const base = process.env.HEIMDALL_PROBE_OPENFGA_URL;
const key = process.env.HEIMDALL_PROBE_OPENFGA_KEY;
const profiles = fixture("model-rollout-v1") as {
  product: string;
  existingRole: string;
  existingPermission: string;
  addedRole: string;
  addedPermission: string;
}[];
const runId = randomUUID();

for (const profile of profiles) {
  it.skipIf(process.env.HEIMDALL_PROBE_MODEL_ROLLOUT !== "1")(
    `qualifies model rollout and rollback for ${profile.product} in both SDKs`,
    async () => {
      expect(new URL(base!).origin).toBe(base);
      expect(new URL(base!).protocol).toBe("https:");
      expect(key).toBeTruthy();
      const api = async (path: string, body?: object, method = "POST", status = 200) => {
        const reply = await fetch(base + path, {
          method,
          headers: { authorization: "Bearer " + key, "content-type": "application/json" },
          ...(body ? { body: JSON.stringify(body) } : {}),
          redirect: "error",
          signal: AbortSignal.timeout(15000),
        });
        expect(reply.status, `${method} ${path}`).toBe(status);
        const data = await reply.text();
        return data ? JSON.parse(data) : null;
      };
      const { id } = await api(
        "/stores",
        { name: `heimdall-rollout-${profile.product}-${runId}` },
        "POST",
        201,
      );
      const path = `/stores/${id}`;
      try {
        const { product, existingRole, existingPermission, addedRole, addedPermission } = profile;
        const original = (
          fixture("tenant-model-v1") as {
            name: string;
            manifest: {
              version: number;
              roles: Record<string, string[]>;
              permissions: string[];
            };
          }[]
        ).find((entry) => entry.name === product)!.manifest;
        const upgraded = {
          ...original,
          version: original.version + 1,
          roles: { ...original.roles, [addedRole]: [addedPermission] },
          permissions: [...original.permissions, addedPermission],
        };
        const cfg = { ...fixture("configuration-v1").config, productId: product };
        const bind = (manifest: typeof original, modelId: string) => {
          const config = {
            ...cfg,
            manifestVersion: manifest.version,
            openfga: { url: base, storeId: id, modelId },
          };
          const deployment = parseDeployment(JSON.stringify(config), JSON.stringify(manifest));
          return {
            config,
            manifest,
            artifact: deployment.artifact,
            client: new PinnedModel(
              { ...deployment.config.openfga, profile: "production" },
              deployment.manifest,
              deployment.config,
              deployment.artifact,
              async () => key!,
            ),
          };
        };
        const publish = async (manifest: typeof original) => {
          const { authorization_model_id: modelId } = await api(
            path + "/authorization-models",
            bind(manifest, "pending").artifact.model as object,
            "POST",
            201,
          );
          return bind(manifest, modelId);
        };
        const old = await publish(original);
        const candidate = bind(upgraded, "pending").artifact;
        for (const [name, relations] of Object.entries(old.artifact.roles))
          expect(candidate.roles[name]).toEqual(relations);
        for (const [name, relation] of Object.entries(old.artifact.permissions))
          expect(candidate.permissions[name]).toBe(relation);
        const reordered = bind(
          {
            ...upgraded,
            roles: { ...original.roles, aaa_reviewer: ["aaa.review"] },
            permissions: [...original.permissions, "aaa.review"],
          },
          "pending",
        ).artifact;
        const firstRole = Object.keys(original.roles).sort()[0]!;
        const firstPermission = [...original.permissions].sort()[0]!;
        expect(reordered.roles.aaa_reviewer?.assignment).toBe(
          old.artifact.roles[firstRole]?.assignment,
        );
        expect(reordered.permissions["aaa.review"]).toBe(old.artifact.permissions[firstPermission]);
        const user =
          "user:" +
          Buffer.from(JSON.stringify(["https://id.example.invalid", "rollout-user"])).toString(
            "base64url",
          );
        const tuple = (relation: string, workspace = "acme") => ({
          user,
          relation,
          object: `workspace:${workspace}`,
        });
        const write = (
          model: typeof old,
          writes: ReturnType<typeof tuple>[],
          deletes: ReturnType<typeof tuple>[] = [],
          status = 200,
        ) =>
          api(
            path + "/write",
            {
              authorization_model_id: model.config.openfga.modelId,
              ...(writes.length ? { writes: { tuple_keys: writes } } : {}),
              ...(deletes.length ? { deletes: { tuple_keys: deletes } } : {}),
            },
            "POST",
            status,
          );
        const cases: {
          config: typeof old.config;
          manifest: typeof old.manifest;
          user: string;
          workspace: string;
          permission: string;
          allowed: boolean;
          unknownRole: boolean;
          membership?: string[];
        }[] = [];
        const check = async (
          model: typeof old,
          workspace: string,
          permission: string,
          allowed: boolean,
          unknownRole = false,
          membership?: string[],
        ) => {
          const { config, manifest, client } = model;
          const signal = AbortSignal.timeout(15000);
          await client.verify(signal);
          expect(await client.check(user, workspace, permission, signal)).toBe(allowed);
          cases.push({
            config,
            manifest,
            user,
            workspace,
            permission,
            allowed,
            unknownRole,
            ...(membership ? { membership } : {}),
          });
        };
        const runGo = async (batch: object[]) => {
          try {
            await promisify(execFile)(
              "go",
              [
                "test",
                "-race",
                "./internal/modelbinding",
                "-run",
                "^TestLiveModelRollout$",
                "-count=1",
              ],
              {
                cwd: new URL("../../go/", import.meta.url),
                timeout: 60000,
                env: { ...process.env, HEIMDALL_TEST_MODEL_ROLLOUT: JSON.stringify(batch) },
              },
            );
          } catch (error) {
            const output = (error as { stdout?: string }).stdout;
            throw new Error(output || "Go live model rollout failed");
          }
        };

        const existing = tuple(old.artifact.roles[existingRole]!.assignment);
        await write(old, [tuple("member"), existing]);
        await check(old, "acme", existingPermission, true);
        const next = await publish(upgraded);
        const wrong = bind(original, next.config.openfga.modelId);
        await expect(wrong.client.verify()).rejects.toMatchObject({ code: "unavailable" });
        const mismatch = { config: wrong.config, manifest: wrong.manifest, reject: true };
        await check(next, "acme", existingPermission, true);
        await check(next, "globex", addedPermission, false);
        // Abort a tuple batch, then confirm neither the old grant nor the new grant changed.
        const newRole = tuple(next.artifact.roles[addedRole]!.assignment);
        await write(next, [newRole, tuple("absent_role")], [], 400);
        await check(next, "acme", addedPermission, false);
        await check(old, "acme", existingPermission, true);
        await runGo([mismatch, ...cases.splice(0)]);

        // Once a new role is assigned, old membership readers must fail closed.
        await write(next, [newRole]);
        await check(next, "acme", addedPermission, true);
        await check(old, "acme", existingPermission, true, true);
        await expect(
          old.client.directMembership(user, "acme", AbortSignal.timeout(5000)),
        ).rejects.toMatchObject({ code: "unavailable" });
        await check(next, "globex", addedPermission, false);
        await runGo(cases.splice(0));

        // Roll back tuple activation before restoring old membership writers.
        await write(next, [], [newRole]);
        await check(next, "acme", addedPermission, false);
        const restoredMembership = ["member", existing.relation];
        await check(old, "acme", existingPermission, true, false, restoredMembership);
        expect(await old.client.directMembership(user, "acme", AbortSignal.timeout(5000))).toEqual(
          new Set(restoredMembership),
        );
        await runGo(cases);
        console.log(
          JSON.stringify({
            runId,
            product,
            oldModelId: old.config.openfga.modelId,
            newModelId: next.config.openfga.modelId,
            checked: ["mixed reads", "atomic rejection", "new role isolation", "rollback"],
          }),
        );
      } finally {
        await api(path, undefined, "DELETE", 204);
      }
    },
    180000,
  );
}

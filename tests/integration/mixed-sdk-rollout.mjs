import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { execFile } from "node:child_process";
import { promisify } from "node:util";

const execute = promisify(execFile);
const profiles = JSON.parse(
  readFileSync(new URL("../../contracts/fixtures/model-rollout-v1.json", import.meta.url), "utf8"),
);

export async function verifyMixedSDKBuilds({
  root,
  config,
  manifest,
  artifact,
  client,
  identity,
  api,
}) {
  const profile = profiles.find(({ product }) => product === config.productId);
  assert(profile);
  const { existingRole, existingPermission, addedRole, addedPermission } = profile;
  const candidateManifest = {
    ...manifest,
    version: manifest.version + 1,
    roles: { ...manifest.roles, [addedRole]: [addedPermission] },
    permissions: [...manifest.permissions, addedPermission],
  };
  const candidateModel = structuredClone(artifact.model);
  const { relations, metadata } = candidateModel.type_definitions.find(
    ({ type }) => type === "workspace",
  );
  const roleIndex = Object.keys(manifest.roles).length;
  const assignment = "r_" + roleIndex;
  const member = { computedUserset: { relation: "member" } };
  const newRole = {
    intersection: { child: [member, { computedUserset: { relation: assignment } }] },
  };
  relations[assignment] = { this: {} };
  metadata.relations[assignment] = structuredClone(metadata.relations.member);
  relations["e_" + roleIndex] = newRole;
  relations["p_" + manifest.permissions.length] = newRole;
  relations["d_" + roleIndex] = {
    intersection: { child: [member, { difference: { base: member, subtract: member } }] },
  };

  const path = "/stores/" + config.openfga.storeId;
  const { authorization_model_id: modelId } = await api(
    path + "/authorization-models",
    candidateModel,
  );
  const next = {
    ...config,
    manifestVersion: candidateManifest.version,
    openfga: { ...config.openfga, modelId },
  };
  const user =
    "user:" + Buffer.from(JSON.stringify([config.issuer, identity.subject])).toString("base64url");
  const tuple = (relation) => ({ user, relation, object: "workspace:acme" });
  const write = (cfg, kind, relations) =>
    api(path + "/write", {
      authorization_model_id: cfg.openfga.modelId,
      [kind]: { tuple_keys: relations.map(tuple) },
    });
  const probe = async (
    build,
    language,
    stage,
    pin,
    policy,
    permission,
    expectedRoles,
    rejectModel = false,
    denyAcme = false,
  ) => {
    const args =
      language === "ts"
        ? [process.execPath, join(root, build, "ts/probe.mjs")]
        : [join(root, build, "go-probe"), "-test.v", "-test.run=^TestLiveRuntime$"];
    const input = {
      config: pin,
      manifest: policy,
      clientSecret: client.clientSecret,
      token: identity.token,
      subject: identity.subject,
      permission,
      expectedRoles,
      rejectModel,
      denyAcme,
    };
    try {
      const { stdout } = await execute(args[0], args.slice(1), {
        timeout: 45000,
        env: { ...process.env, HEIMDALL_TEST_RUNTIME: JSON.stringify(input) },
      });
      if (language === "go") assert.match(stdout, /^--- PASS: TestLiveRuntime /m);
    } catch (error) {
      const codes = [
        ...new Set(
          String(error.stdout + error.stderr).match(
            /\b(?:unavailable|unauthenticated|deadline_exceeded|invalid_argument|audit_unavailable|forbidden)\b/g,
          ) ?? [],
        ),
      ];
      throw new Error(
        `${config.productId} ${build} ${language} ${stage} failed [${codes.join(",")}]`,
      );
    }
  };
  const both = (build, ...args) =>
    Promise.all(["ts", "go"].map((language) => probe(build, language, ...args)));

  await write(config, "writes", ["member", artifact.roles[existingRole].assignment]);
  await Promise.all([
    both("previous", "overlap", config, manifest, existingPermission, [existingRole]),
    both("candidate", "overlap", next, candidateManifest, existingPermission, [existingRole]),
    both("candidate", "previous pin", config, manifest, existingPermission, [existingRole]),
    both(
      "candidate",
      "mismatched pin",
      { ...config, openfga: next.openfga },
      manifest,
      "",
      [],
      true,
    ),
  ]);
  await write(next, "writes", [assignment]);
  await both("candidate", "new role", next, candidateManifest, addedPermission, [
    existingRole,
    addedRole,
  ]);
  await write(next, "deletes", [assignment]);
  await Promise.all([
    both("previous", "rollback", config, manifest, existingPermission, [existingRole]),
    both("candidate", "rollback", next, candidateManifest, existingPermission, [existingRole]),
    both(
      "candidate",
      "removed role grant",
      next,
      candidateManifest,
      addedPermission,
      [existingRole],
      false,
      true,
    ),
  ]);
  return {
    product: config.productId,
    mixedSDKBuilds: {
      previousModelId: config.openfga.modelId,
      candidateModelId: modelId,
      stages: [
        "overlap",
        "previous pin",
        "mismatched pin",
        "new role",
        "rollback",
        "removed role grant",
      ],
    },
  };
}

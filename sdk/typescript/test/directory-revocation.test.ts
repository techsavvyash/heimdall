import { readFileSync } from "node:fs";
import { expect, test } from "vitest";
import { planDirectoryRevocation } from "../src/provisioning/index.js";
import { HeimdallError } from "../src/internal/errors.js";

type Fixture = Pick<Parameters<typeof planDirectoryRevocation>[0], "currentRoles" | "sources"> & {
  name: string;
  error?: string;
  action?: string;
  roles?: string[];
};
const fixtures: Fixture[] = JSON.parse(
  readFileSync(
    new URL("../../../contracts/fixtures/directory-revocation-v1.json", import.meta.url),
    "utf8",
  ),
);

test.each(fixtures)("$name", ({ currentRoles, sources, error, action, roles }) => {
  let result;
  try {
    result = planDirectoryRevocation({
      ruleId: "employees",
      ownerRole: "owner",
      currentRoles,
      sources,
    });
  } catch (failure) {
    if (!(failure instanceof HeimdallError)) throw failure;
    result = { error: failure.code };
  }
  expect(result).toEqual(error ? { error } : { action, roles });
});

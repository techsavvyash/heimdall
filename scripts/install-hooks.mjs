import { execFileSync, spawnSync } from "node:child_process";

if (!process.env.CI) {
  const result = spawnSync("git", ["config", "--get", "core.hooksPath"], {
    encoding: "utf8",
  });
  if (result.error || (result.status !== 0 && result.status !== 1)) {
    throw new Error(
      "Cannot inspect Git hooks. Run pnpm install --frozen-lockfile inside the repository.",
    );
  }
  const existing = result.stdout.trim();
  if (existing && existing !== ".githooks") {
    throw new Error(`Existing core.hooksPath is ${existing}. Integrate .githooks explicitly.`);
  }
  execFileSync("git", ["config", "core.hooksPath", ".githooks"]);
}

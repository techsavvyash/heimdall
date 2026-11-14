import { defineConfig } from "rolldown";

export default defineConfig({
  input: {
    cli: "src/cli.ts",
    index: "src/index.ts",
    "provisioning/index": "src/provisioning/index.ts",
    postgres: "src/postgres.ts",
    sessionstoretest: "src/sessionstoretest.ts",
  },
  platform: "node",
  output: {
    dir: "dist",
    format: "esm",
    sourcemap: true,
    cleanDir: true,
  },
});

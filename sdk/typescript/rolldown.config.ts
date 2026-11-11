import { defineConfig } from "rolldown";

export default defineConfig({
  input: { index: "src/index.ts", postgres: "src/postgres.ts" },
  platform: "node",
  output: {
    dir: "dist",
    format: "esm",
    sourcemap: true,
    cleanDir: true,
  },
});

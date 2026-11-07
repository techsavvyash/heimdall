import { defineConfig } from "vitest/config";

export default defineConfig({
  test: {
    environment: "node",
    include: ["src/**/*.test.ts", "test/**/*.test.ts"],
    // The scaffold has no behavior yet. Remove when the first contract tests land.
    passWithNoTests: true,
    clearMocks: true,
    restoreMocks: true,
  },
});

import { defineConfig } from "vitest/config";

export default defineConfig({
  // Workspace packages (@livepeer-frameworks/api) resolve to their sources,
  // so the tests run without building them first.
  resolve: {
    conditions: ["source"],
  },
  test: {
    include: ["test/**/*.test.ts"],
    environment: "node",
    restoreMocks: true,
    reporters: ["default", "junit"],
    outputFile: {
      junit: "./test-results/junit.xml",
    },
    pool: "forks",
    coverage: {
      provider: "v8",
      reporter: ["text", "lcov"],
      reportsDirectory: "./coverage",
      exclude: ["**/dist/**", "**/*.d.ts", "**/workers/**", "**/src/styles/**"],
    },
  },
});

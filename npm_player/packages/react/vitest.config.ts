import { defineConfig } from "vitest/config";
import path from "path";

export default defineConfig({
  resolve: {
    conditions: ["source"],
    alias: {
      "@livepeer-frameworks/player-core": path.resolve(__dirname, "../core/src/index.ts"),
    },
  },
  test: {
    include: ["test/**/*.test.{ts,tsx}"],
    environment: "jsdom",
    globals: true,
    restoreMocks: true,
    reporters: ["default", "junit"],
    outputFile: {
      junit: "./test-results/junit.xml",
    },
    coverage: {
      provider: "v8",
      reporter: ["text", "lcov"],
      reportsDirectory: "./coverage",
      exclude: ["**/dist/**", "**/*.d.ts"],
    },
  },
});

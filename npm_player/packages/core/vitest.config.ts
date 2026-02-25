import { defineConfig } from "vitest/config";

export default defineConfig({
  test: {
    include: ["test/**/*.test.ts"],
    exclude: [
      "**/node_modules/**",
      "**/.git/**",
      "**/.stryker-tmp/**",
      "**/dist/**",
      "**/coverage/**",
    ],
    environment: "node",
    reporters: ["default", "junit"],
    outputFile: {
      junit: "./test-results/junit.xml",
    },
    pool: "forks",
    coverage: {
      provider: "v8",
      reporter: ["text", "lcov"],
      reportsDirectory: "./coverage",
    },
  },
});

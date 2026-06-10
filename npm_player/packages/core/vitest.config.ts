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
      // Report every source file, not just the ones a test happens to import,
      // so the denominator reflects the real surface area (untouched modules
      // included). Type-only declarations carry no executable statements.
      all: true,
      include: ["src/**/*.ts"],
      exclude: ["src/**/*.d.ts"],
    },
  },
});

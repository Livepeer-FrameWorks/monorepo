import { defineConfig } from "vitest/config";
import { resolve } from "path";

export default defineConfig({
  test: {
    include: ["src/**/*.test.{ts,js}", "test/**/*.test.{ts,js}"],
    exclude: ["**/*.svelte"],
    environment: "node",
    globals: true,
    restoreMocks: true,
    reporters: ["default", "junit"],
    outputFile: {
      junit: "./test-results/junit.xml",
    },
    alias: {
      $lib: resolve(__dirname, "./src/lib"),
    },
    coverage: {
      provider: "v8",
      reporter: ["text", "lcov"],
      reportsDirectory: "./coverage",
      exclude: [
        "**/dist/**",
        "**/*.d.ts",
        "**/$houdini/**",
        "**/src/lib/graphql/generated/**",
        "**/.svelte-kit/**",
        "**/*.svelte",
      ],
    },
  },
});

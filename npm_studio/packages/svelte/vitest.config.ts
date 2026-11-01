import { defineConfig } from "vitest/config";
import { svelte } from "@sveltejs/vite-plugin-svelte";
import { svelteTesting } from "@testing-library/svelte/vite";
import path from "path";

export default defineConfig({
  plugins: [svelte({ hot: false }), svelteTesting()],
  resolve: {
    conditions: ["source"],
    alias: {
      "@livepeer-frameworks/streamcrafter-core": path.resolve(__dirname, "../core/src/index.ts"),
    },
  },
  test: {
    include: ["test/**/*.test.{ts,js}"],
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

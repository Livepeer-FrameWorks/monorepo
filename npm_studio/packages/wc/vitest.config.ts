import { defineConfig } from "vitest/config";
import path from "path";

export default defineConfig({
  resolve: {
    conditions: ["source"],
    alias: {
      "@livepeer-frameworks/streamcrafter-core": path.resolve(__dirname, "../core/src/index.ts"),
    },
  },
  test: {
    include: ["test/**/*.test.ts"],
    environment: "jsdom",
    globals: true,
    restoreMocks: true,
    reporters: ["default"],
    coverage: {
      provider: "v8",
      reporter: ["text", "lcov"],
      reportsDirectory: "./coverage",
      exclude: ["**/dist/**", "**/*.d.ts", "scripts/**"],
    },
    setupFiles: ["./test/setup.ts"],
  },
});

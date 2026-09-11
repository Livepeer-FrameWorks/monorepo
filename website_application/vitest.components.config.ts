import { defineConfig } from "vitest/config";
import { svelte } from "@sveltejs/vite-plugin-svelte";
import { resolve } from "node:path";

export default defineConfig({
  plugins: [svelte()],
  resolve: {
    conditions: ["browser"],
    alias: {
      $lib: resolve(import.meta.dirname, "src/lib"),
      $houdini: resolve(import.meta.dirname, "$houdini"),
      "$app/navigation": resolve(import.meta.dirname, "test/components/navigation.ts"),
    },
  },
  test: {
    include: ["test/components/**/*.test.ts"],
    environment: "jsdom",
    restoreMocks: true,
    reporters: ["default", "junit"],
    outputFile: { junit: "./test-results/components.xml" },
  },
});

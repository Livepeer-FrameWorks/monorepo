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
      "$app/environment": resolve(import.meta.dirname, "test/components/environment.ts"),
      "$app/navigation": resolve(import.meta.dirname, "test/components/navigation.ts"),
      "$app/paths": resolve(import.meta.dirname, "test/components/paths.ts"),
      "$app/state": resolve(import.meta.dirname, "test/components/state.ts"),
    },
  },
  test: {
    include: ["test/components/**/*.test.ts"],
    environment: "jsdom",
    restoreMocks: true,
  },
});

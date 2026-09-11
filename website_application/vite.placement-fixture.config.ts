import { defineConfig } from "vite";
import { svelte } from "@sveltejs/vite-plugin-svelte";
import tailwindcss from "@tailwindcss/vite";
import { resolve } from "node:path";

export default defineConfig({
  root: resolve(import.meta.dirname, "test/browser/placement"),
  plugins: [tailwindcss(), svelte()],
  resolve: {
    alias: {
      "$lib/placement/consent-api": resolve(
        import.meta.dirname,
        "test/browser/placement/consent.ts"
      ),
      "$lib/stores/auth": resolve(import.meta.dirname, "test/browser/placement/auth.ts"),
      "$app/navigation": resolve(import.meta.dirname, "test/browser/placement/navigation.ts"),
      $lib: resolve(import.meta.dirname, "src/lib"),
      $houdini: resolve(import.meta.dirname, "test/browser/placement/options.ts"),
    },
  },
  server: {
    host: "127.0.0.1",
    port: 5179,
    strictPort: true,
    fs: { allow: [resolve(import.meta.dirname, "..")] },
  },
});

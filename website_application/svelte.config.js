import adapter from "@sveltejs/adapter-node";
import { vitePreprocess } from "@sveltejs/vite-plugin-svelte";

// Normalize BASE_PATH: "/" or empty → "", strip trailing slash
function normalizeBasePath(p) {
  if (!p || p === "/") return "";
  return p.endsWith("/") ? p.slice(0, -1) : p;
}

/** @type {import('@sveltejs/kit').Config} */
const config = {
  preprocess: vitePreprocess(),

  kit: {
    adapter: adapter(),
    paths: {
      base: normalizeBasePath(process.env.BASE_PATH),
    },
    alias: {
      $houdini: "./$houdini",
    },
  },
};

export default config;

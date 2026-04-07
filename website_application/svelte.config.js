import adapter from "@sveltejs/adapter-node";
import { vitePreprocess } from "@sveltejs/vite-plugin-svelte";
import { loadEnv } from "vite";

// Derive base path from VITE_APP_URL (same pattern as docs site with VITE_DOCS_SITE_URL)
const env = loadEnv(process.env.NODE_ENV ?? "development", process.cwd(), "");
function extractBasePath(url) {
  if (!url) return "";
  try {
    const path = new URL(url).pathname;
    if (!path || path === "/") return "";
    return path.endsWith("/") ? path.slice(0, -1) : path;
  } catch {
    return "";
  }
}

/** @type {import('@sveltejs/kit').Config} */
const config = {
  preprocess: vitePreprocess(),

  kit: {
    adapter: adapter(),
    paths: {
      base: extractBasePath(env.VITE_APP_URL),
    },
    alias: {
      $houdini: "./$houdini",
    },
  },
};

export default config;

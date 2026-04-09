import { sveltekit } from "@sveltejs/kit/vite";
import houdini from "houdini/vite";
import tailwindcss from "@tailwindcss/vite";
import { defineConfig, loadEnv } from "vite";
import { codecovSvelteKitPlugin } from "@codecov/sveltekit-plugin";

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), "");

  const host = env.HOST ?? "0.0.0.0";
  const port = Number(env.PORT ?? 3000);

  // Get backend URL from VITE_AUTH_URL for proxy target
  const authUrl = env.VITE_AUTH_URL || "http://localhost:18090/auth";
  let backendUrl = "http://localhost:18090";
  try {
    const parsed = new URL(authUrl);
    backendUrl = `${parsed.protocol}//${parsed.host}`;
  } catch {
    // authUrl is relative, use default
  }

  return {
    // IMPORTANT: houdini() must come before sveltekit()
    plugins: [
      houdini(),
      tailwindcss(),
      sveltekit(),
      codecovSvelteKitPlugin({
        enableBundleAnalysis: process.env.CODECOV_TOKEN !== undefined,
        bundleName: "website-application",
        uploadToken: process.env.CODECOV_TOKEN,
      }),
    ],
    server: {
      host,
      port,
      proxy: {
        // Proxy API routes to backend so cookies stay on same origin
        "/auth": {
          target: backendUrl,
          changeOrigin: true,
        },
        "/graphql": {
          target: backendUrl,
          changeOrigin: true,
          ws: true,
        },
        "/assets": {
          target: "http://localhost:18020",
          changeOrigin: true,
        },
      },
      fs: {
        // Allow serving files from the monorepo root (for pkg/graphql/operations/)
        allow: [".."],
      },
    },
    ssr: {
      noExternal: ["graphql", "graphql-ws"],
    },
  };
});

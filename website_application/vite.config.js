import { sveltekit } from "@sveltejs/kit/vite";
import houdini from "houdini/vite";
import tailwindcss from "@tailwindcss/vite";
import { defineConfig, loadEnv } from "vite";
import { codecovSvelteKitPlugin } from "@codecov/sveltekit-plugin";

// devProxy keeps the gateway's routes on the dev server's origin, so cookies and the relative GraphQL URL work as they
// do behind the production ingress.
/** @param {string} proxyGatewayUrl */
export function devProxy(proxyGatewayUrl) {
  return {
    "/auth": {
      target: proxyGatewayUrl,
      changeOrigin: true,
    },
    "/graphql": {
      target: proxyGatewayUrl,
      changeOrigin: true,
      ws: true,
    },
    // Bridge's REST routes, such as the public network observations.
    "/v1": {
      target: proxyGatewayUrl,
      changeOrigin: true,
    },
    "/assets": {
      target: "http://localhost:18020",
      changeOrigin: true,
    },
  };
}

export default defineConfig(({ mode, command }) => {
  const env = loadEnv(mode, process.cwd(), "");

  const host = env.HOST ?? "0.0.0.0";
  const port = Number(env.PORT ?? 3000);

  let devServer;
  if (command === "serve") {
    const proxyGatewayUrl = env.DEV_PROXY_GATEWAY_URL;
    if (!proxyGatewayUrl) {
      throw new Error("DEV_PROXY_GATEWAY_URL is required for website_application dev proxying");
    }
    devServer = {
      host,
      port,
      proxy: devProxy(proxyGatewayUrl),
      fs: {
        // Allow serving files from the monorepo root (for pkg/graphql/operations/)
        allow: [".."],
      },
    };
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
    server: devServer,
    optimizeDeps: {
      exclude: ["maplibre-gl"],
    },
    ssr: {
      noExternal: ["graphql", "graphql-ws"],
    },
  };
});

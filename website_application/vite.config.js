import { sveltekit } from "@sveltejs/kit/vite";
import houdini from "houdini/vite";
import tailwindcss from "@tailwindcss/vite";
import { defineConfig, loadEnv } from "vite";
import { codecovSvelteKitPlugin } from "@codecov/sveltekit-plugin";

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
      proxy: {
        // Proxy API routes to backend so cookies stay on same origin
        "/auth": {
          target: proxyGatewayUrl,
          changeOrigin: true,
        },
        "/graphql": {
          target: proxyGatewayUrl,
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
    ssr: {
      noExternal: ["graphql", "graphql-ws"],
    },
  };
});

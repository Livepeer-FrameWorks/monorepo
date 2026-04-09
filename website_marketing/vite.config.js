import { defineConfig, loadEnv } from "vite";
import path from "path";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import { codecovVitePlugin } from "@codecov/vite-plugin";

export default defineConfig(({ mode, command }) => {
  const env = loadEnv(mode, process.cwd(), "");

  const host = env.HOST ?? "0.0.0.0";
  const port = Number(env.PORT ?? 9004);

  let devServer;
  if (command === "serve") {
    const proxyGatewayUrl = env.DEV_PROXY_GATEWAY_URL;
    if (!proxyGatewayUrl) {
      throw new Error("DEV_PROXY_GATEWAY_URL is required for website_marketing dev proxying");
    }
    devServer = {
      host,
      port,
      proxy: {
        // Proxy API routes to backend to avoid CORS in dev
        "/auth": {
          target: proxyGatewayUrl,
          changeOrigin: true,
        },
        "/graphql": {
          target: proxyGatewayUrl,
          changeOrigin: true,
          ws: true,
        },
        "/mcp": {
          target: proxyGatewayUrl,
          changeOrigin: true,
        },
      },
    };
  }

  return {
    plugins: [
      react(),
      tailwindcss(),
      codecovVitePlugin({
        enableBundleAnalysis: process.env.CODECOV_TOKEN !== undefined,
        bundleName: "website-marketing",
        uploadToken: process.env.CODECOV_TOKEN,
      }),
    ],
    resolve: {
      conditions: ["source"],
      alias: {
        "@": path.resolve(__dirname, "./src"),
      },
    },
    server: devServer,
    build: {
      outDir: "dist",
      sourcemap: false,
    },
  };
});

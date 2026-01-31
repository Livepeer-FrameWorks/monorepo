import { defineConfig, loadEnv } from "vite";
import path from "path";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import { codecovVitePlugin } from "@codecov/vite-plugin";

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), "");

  const host = env.HOST ?? "0.0.0.0";
  const port = Number(env.PORT ?? 9004);

  // Get backend URL for proxy target (same pattern as webapp)
  const backendUrl = env.VITE_BACKEND_URL || "http://localhost:18090";

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
      alias: {
        "@": path.resolve(__dirname, "./src"),
      },
    },
    server: {
      host,
      port,
      proxy: {
        // Proxy API routes to backend to avoid CORS in dev
        "/auth": {
          target: backendUrl,
          changeOrigin: true,
        },
        "/graphql": {
          target: backendUrl,
          changeOrigin: true,
          ws: true,
        },
      },
    },
    build: {
      outDir: "dist",
      sourcemap: false,
    },
  };
});

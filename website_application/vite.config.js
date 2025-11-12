import { sveltekit } from "@sveltejs/kit/vite";
import { defineConfig, loadEnv } from "vite";

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), "");

  const host = env.HOST ?? "0.0.0.0";
  const port = Number(env.PORT ?? 3000);

  return {
    plugins: [sveltekit()],
    server: {
      host,
      port,
    },
    ssr: {
      noExternal: ["@apollo/client", "graphql", "graphql-ws"],
    },
  };
});

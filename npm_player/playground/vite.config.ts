import react from "@vitejs/plugin-react-swc";
import path from "node:path";
import fs from "node:fs";
import { fileURLToPath } from "node:url";
import { defineConfig, Plugin } from "vite";

const __dirname = path.dirname(fileURLToPath(import.meta.url));

// Paths to worker source directories
const WORKER_SOURCE_DIRS = [
  path.resolve(__dirname, "../../npm_studio/packages/core/src/workers"),
  path.resolve(__dirname, "../packages/core/src/workers"),
  // WebCodecs worker lives in a subdirectory, not src/workers/
  path.resolve(__dirname, "../packages/core/src/players/WebCodecsPlayer/worker"),
];

/**
 * Plugin to serve worker files from source directories
 * Intercepts requests to /workers/*.js and serves the compiled .ts files
 */
function workerServePlugin(): Plugin {
  return {
    name: "vite-plugin-worker-serve",
    configureServer(server) {
      // Use pre middleware to intercept before Vite's default handling
      server.middlewares.use(async (req, res, next) => {
        const url = req.url || "";
        const isWorkersPath = url.startsWith("/workers/") && url.endsWith(".js");
        const isFsWorkersPath =
          url.startsWith("/@fs/") && url.includes("/workers/") && url.endsWith(".js");

        // Handle requests to /workers/*.js or /@fs/.../workers/*.js
        if (isWorkersPath || isFsWorkersPath) {
          const workerName = isWorkersPath
            ? url.replace("/workers/", "").replace(".js", "")
            : path.basename(url).replace(".js", "");
          console.log(`[workerServePlugin] Looking for worker: ${workerName}`);

          const candidatePaths: string[] = [];

          if (isFsWorkersPath) {
            const fsPath = decodeURIComponent(url.slice("/@fs".length));
            if (fsPath.endsWith(".js")) {
              candidatePaths.push(fsPath.replace(/\.js$/, ".ts"));
            }
          }

          // Try to find the worker source file
          for (const dir of WORKER_SOURCE_DIRS) {
            const tsPath = path.join(dir, `${workerName}.ts`);
            candidatePaths.push(tsPath);
          }

          for (const tsPath of candidatePaths) {
            const exists = fs.existsSync(tsPath);
            console.log(`[workerServePlugin] Checking ${tsPath} - exists: ${exists}`);
            if (!exists) continue;
            try {
              // Use Vite's transform pipeline to compile the TypeScript
              // Use /@fs/ prefix for absolute paths (required for files outside project root)
              const viteUrl = `/@fs${tsPath}`;
              const result = await server.transformRequest(viteUrl);
              if (result) {
                console.log(`[workerServePlugin] Successfully transformed ${workerName}`);
                res.setHeader("Content-Type", "application/javascript");
                res.setHeader("Cache-Control", "no-cache");
                res.end(result.code);
                return;
              }
            } catch (e) {
              console.error("[workerServePlugin] Failed to transform worker:", e);
            }
          }
          console.log(`[workerServePlugin] Worker not found: ${workerName}`);
        }

        next();
      });
    },
  };
}

/**
 * Plugin to handle web-worker: imports (compatible with rollup-plugin-web-worker-loader)
 * Transforms web-worker:./path/to/worker imports into Vite's native ?worker&inline imports
 */
function webWorkerPlugin(): Plugin {
  return {
    name: "vite-plugin-web-worker",
    enforce: "pre",
    resolveId(source, importer) {
      if (source.startsWith("web-worker:")) {
        const workerPath = source.slice("web-worker:".length);
        // Return a virtual module ID that we'll transform
        return `\0web-worker:${workerPath}?from=${importer}`;
      }
      return null;
    },
    async load(id) {
      if (id.startsWith("\0web-worker:")) {
        // Parse the virtual module ID
        const [workerPart, fromPart] = id.slice("\0web-worker:".length).split("?from=");
        const importer = fromPart || "";

        // Resolve the actual worker path relative to importer
        const importerDir = path.dirname(importer);
        let resolvedPath = workerPart;

        // Handle relative paths
        if (workerPart.startsWith("./") || workerPart.startsWith("../")) {
          resolvedPath = path.resolve(importerDir, workerPart);
        }

        // Add .ts extension if not present
        if (!resolvedPath.endsWith(".ts") && !resolvedPath.endsWith(".js")) {
          resolvedPath += ".ts";
        }

        // Return code that uses Vite's native worker handling
        return `
          import Worker from '${resolvedPath}?worker&inline';
          export default Worker;
        `;
      }
      return null;
    },
  };
}

// Unified playground for both Player and StreamCrafter libraries
// Lives in npm_player/playground/ but accessed via symlink from npm_studio/playground/
export default defineConfig({
  plugins: [workerServePlugin(), webWorkerPlugin(), react()],
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "src"),
      // Player packages (npm_player/packages/*)
      // CSS: wrapper re-exports core, point to source for dev (no build step needed)
      "@livepeer-frameworks/player-react/player.css": path.resolve(
        __dirname,
        "../packages/react/src/player.css"
      ),
      "@livepeer-frameworks/player-svelte/player.css": path.resolve(
        __dirname,
        "../packages/svelte/src/player.css"
      ),
      "@livepeer-frameworks/player-core/player.css": path.resolve(
        __dirname,
        "../packages/core/src/styles/player.css"
      ),
      "@livepeer-frameworks/player-core": path.resolve(__dirname, "../packages/core/src"),
      "@livepeer-frameworks/player-react": path.resolve(__dirname, "../packages/react/src"),
      "@livepeer-frameworks/player-svelte": path.resolve(__dirname, "../packages/svelte/src"),
      // StreamCrafter packages (npm_studio/packages/*)
      "@livepeer-frameworks/streamcrafter-react/streamcrafter.css": path.resolve(
        __dirname,
        "../../npm_studio/packages/react/src/streamcrafter.css"
      ),
      "@livepeer-frameworks/streamcrafter-svelte/streamcrafter.css": path.resolve(
        __dirname,
        "../../npm_studio/packages/svelte/src/streamcrafter.css"
      ),
      "@livepeer-frameworks/streamcrafter-core/streamcrafter.css": path.resolve(
        __dirname,
        "../../npm_studio/packages/core/src/styles/streamcrafter.css"
      ),
      "@livepeer-frameworks/streamcrafter-core": path.resolve(
        __dirname,
        "../../npm_studio/packages/core/src"
      ),
      "@livepeer-frameworks/streamcrafter-react": path.resolve(
        __dirname,
        "../../npm_studio/packages/react/src"
      ),
      // Deduplicate React
      react: path.resolve(__dirname, "node_modules/react"),
      "react-dom": path.resolve(__dirname, "node_modules/react-dom"),
    },
    dedupe: [
      "react",
      "react-dom",
      "@radix-ui/react-context-menu",
      "@radix-ui/react-select",
      "@radix-ui/react-slider",
      "@radix-ui/react-slot",
    ],
  },
  server: {
    port: 4173,
    host: true,
    open: true,
    fs: {
      allow: [path.resolve(__dirname, "..", ".."), path.resolve(__dirname, "../../npm_studio")],
    },
  },
  preview: {
    port: 4174,
  },
  worker: {
    format: "es",
  },
  optimizeDeps: {
    include: ["hls.js", "dashjs"],
    exclude: [
      "@livepeer-frameworks/player-core",
      "@livepeer-frameworks/player-react",
      "@livepeer-frameworks/streamcrafter-core",
      "@livepeer-frameworks/streamcrafter-react",
    ],
  },
});

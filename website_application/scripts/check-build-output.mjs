import { existsSync, readdirSync, statSync } from "node:fs";
import { join } from "node:path";

const assetsDir = join(
  process.cwd(),
  ".svelte-kit",
  "output",
  "client",
  "_app",
  "immutable",
  "assets"
);
const workerAsset = existsSync(assetsDir)
  ? readdirSync(assetsDir).find((name) => /^maplibre-gl-worker\.[^/]+\.mjs$/.test(name))
  : undefined;

if (!workerAsset) {
  console.error("Frontend build output check failed: missing bundled MapLibre worker asset");
  process.exit(1);
}

if (statSync(join(assetsDir, workerAsset)).size < 10_000) {
  console.error(
    `Frontend build output check failed: MapLibre worker is unexpectedly small: ${workerAsset}`
  );
  process.exit(1);
}

console.log("Frontend MapLibre worker output verified.");

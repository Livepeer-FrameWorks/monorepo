import { existsSync, readFileSync, readdirSync, statSync } from "node:fs";
import { join } from "node:path";

const workerDir = join(
  process.cwd(),
  ".svelte-kit",
  "output",
  "client",
  "_app",
  "immutable",
  "workers"
);
const workerAsset = existsSync(workerDir)
  ? readdirSync(workerDir)
      .filter((name) => /^maplibre-gl-worker[.-][^/]+\.js$/.test(name))
      .sort(
        (left, right) =>
          statSync(join(workerDir, right)).size - statSync(join(workerDir, left)).size
      )[0]
  : undefined;

if (!workerAsset) {
  console.error("Frontend build output check failed: missing bundled MapLibre worker asset");
  process.exit(1);
}

if (statSync(join(workerDir, workerAsset)).size < 100_000) {
  console.error(
    `Frontend build output check failed: MapLibre worker is unexpectedly small: ${workerAsset}`
  );
  process.exit(1);
}

if (readFileSync(join(workerDir, workerAsset), "utf8").includes("maplibre-gl-shared.mjs")) {
  console.error(
    `Frontend build output check failed: MapLibre worker references a missing shared module: ${workerAsset}`
  );
  process.exit(1);
}

console.log("Frontend MapLibre worker output verified.");

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

// Route JavaScript budgets. A route's cost is the raw size of the JS files in
// the static import closure of its page node, minus the files the app entries
// and the root layout already load. Limits are the first measured build plus
// 10 percent; raise one only together with the change that needs it.
const routeBudgets = [
  // Measured 36,975 bytes.
  { route: "src/routes/developer/webhooks/+page.svelte", maxBytes: 40_673 },
  // Measured 58,440 bytes.
  { route: "src/routes/developer/webhooks/[id]/+page.svelte", maxBytes: 64_284 },
];

const clientRoot = join(process.cwd(), ".svelte-kit", "output", "client");
const manifestPath = join(clientRoot, ".vite", "manifest.json");
const nodesDir = join(process.cwd(), ".svelte-kit", "generated", "client-optimized", "nodes");
if (!existsSync(manifestPath) || !existsSync(nodesDir)) {
  console.error("Frontend build output check failed: missing Vite manifest or generated nodes");
  process.exit(1);
}
const manifest = JSON.parse(readFileSync(manifestPath, "utf8"));

function importClosure(key, seen = new Set()) {
  if (seen.has(key)) return seen;
  const chunk = manifest[key];
  if (!chunk) return seen;
  seen.add(key);
  for (const imported of chunk.imports ?? []) importClosure(imported, seen);
  return seen;
}

function nodeKeyFor(route) {
  const target = `/${route}";`;
  for (const name of readdirSync(nodesDir)) {
    if (readFileSync(join(nodesDir, name), "utf8").includes(target)) {
      return `.svelte-kit/generated/client-optimized/nodes/${name}`;
    }
  }
  return undefined;
}

const baseline = new Set();
for (const [key, chunk] of Object.entries(manifest)) {
  if (chunk.isEntry && !key.includes("client-optimized/nodes/")) importClosure(key, baseline);
}
const rootLayoutKey = nodeKeyFor("src/routes/+layout.svelte");
if (rootLayoutKey) importClosure(rootLayoutKey, baseline);

let budgetFailed = false;
for (const { route, maxBytes } of routeBudgets) {
  const nodeKey = nodeKeyFor(route);
  if (!nodeKey || !manifest[nodeKey]) {
    console.error(`Frontend build output check failed: no build node for ${route}`);
    budgetFailed = true;
    continue;
  }
  let bytes = 0;
  for (const key of importClosure(nodeKey)) {
    if (baseline.has(key)) continue;
    const file = manifest[key].file;
    if (file.endsWith(".js")) bytes += statSync(join(clientRoot, file)).size;
  }
  const status = bytes > maxBytes ? "over budget" : "ok";
  console.log(`Route JS ${route}: ${bytes} bytes (limit ${maxBytes}) ${status}`);
  if (bytes > maxBytes) budgetFailed = true;
}
if (budgetFailed) {
  console.error("Frontend build output check failed: route JavaScript budget exceeded");
  process.exit(1);
}

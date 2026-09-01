#!/usr/bin/env node

import { chmod, mkdir, rename, writeFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";

function readBasemapConfig(env) {
  const provider = (env.BASEMAP_PROVIDER ?? "").trim();
  const style = (env.BASEMAP_STYLE ?? "").trim();
  const key = (env.CARTO_BASEMAP_BROWSER_KEY ?? "").trim();

  if (provider === "carto" && style === "dark-matter") {
    if (!key) throw new Error("CARTO_BASEMAP_BROWSER_KEY is required for carto/dark-matter");
    return { provider, style, key };
  }
  if (provider === "openfreemap" && style === "dark") {
    return { provider, style };
  }
  throw new Error(
    "BASEMAP_PROVIDER and BASEMAP_STYLE must be carto/dark-matter or openfreemap/dark"
  );
}

function serialize(value) {
  return JSON.stringify(value)
    .replaceAll("<", "\\u003c")
    .replaceAll(">", "\\u003e")
    .replaceAll("&", "\\u0026")
    .replaceAll("\u2028", "\\u2028")
    .replaceAll("\u2029", "\\u2029");
}

export function renderRuntimeConfig(env = process.env) {
  const basemap = readBasemapConfig(env);
  return `globalThis.__FRAMEWORKS_RUNTIME_CONFIG__ = Object.freeze({"version":1,"basemap":Object.freeze(${serialize(basemap)})});\n`;
}

async function main() {
  const targetArg = process.argv[2];
  if (!targetArg) throw new Error("usage: render-public-runtime-config.mjs <target-file>");
  const target = resolve(targetArg);
  const temp = `${target}.tmp-${process.pid}`;
  await mkdir(dirname(target), { recursive: true });
  await writeFile(temp, renderRuntimeConfig(), { encoding: "utf8", mode: 0o644 });
  await chmod(temp, 0o644);
  await rename(temp, target);
  process.stdout.write(`Rendered public runtime configuration at ${target}\n`);
}

if (process.argv[1] && resolve(process.argv[1]) === resolve(new URL(import.meta.url).pathname)) {
  main().catch((error) => {
    process.stderr.write(`Runtime configuration error: ${error.message}\n`);
    process.exitCode = 1;
  });
}

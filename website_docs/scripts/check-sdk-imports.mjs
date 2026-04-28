#!/usr/bin/env node

import { access, readdir, readFile } from "node:fs/promises";
import path from "node:path";
import process from "node:process";

const docsRoot = path.resolve("src/content/docs");
const repoRoot = path.resolve("..");
const packageDirs = new Map([
  ["@livepeer-frameworks/player-core", path.join(repoRoot, "npm_player/packages/core")],
  ["@livepeer-frameworks/player-react", path.join(repoRoot, "npm_player/packages/react")],
  ["@livepeer-frameworks/player-svelte", path.join(repoRoot, "npm_player/packages/svelte")],
  ["@livepeer-frameworks/player-wc", path.join(repoRoot, "npm_player/packages/wc")],
  ["@livepeer-frameworks/streamcrafter-core", path.join(repoRoot, "npm_studio/packages/core")],
  ["@livepeer-frameworks/streamcrafter-react", path.join(repoRoot, "npm_studio/packages/react")],
  ["@livepeer-frameworks/streamcrafter-svelte", path.join(repoRoot, "npm_studio/packages/svelte")],
  ["@livepeer-frameworks/streamcrafter-wc", path.join(repoRoot, "npm_studio/packages/wc")],
]);

const exportCache = new Map();

async function walk(dir) {
  const entries = await readdir(dir, { withFileTypes: true });
  const files = [];
  for (const entry of entries) {
    const fullPath = path.join(dir, entry.name);
    if (entry.isDirectory()) {
      files.push(...(await walk(fullPath)));
    } else if (entry.isFile() && entry.name.endsWith(".mdx")) {
      files.push(fullPath);
    }
  }
  return files;
}

async function exists(file) {
  try {
    await access(file);
    return true;
  } catch {
    return false;
  }
}

async function sourceForSpecifier(specifier) {
  const parts = specifier.split("/");
  const packageName = parts.slice(0, 2).join("/");
  const packageDir = packageDirs.get(packageName);
  if (!packageDir) {
    return null;
  }

  const subpath = parts.length > 2 ? `./${parts.slice(2).join("/")}` : ".";
  const packageJSON = JSON.parse(await readFile(path.join(packageDir, "package.json"), "utf8"));
  const exported = packageJSON.exports?.[subpath];
  const source = typeof exported === "object" ? exported.source : null;
  if (source) {
    return path.join(packageDir, source);
  }

  if (subpath === ".") {
    for (const candidate of ["src/index.ts", "src/index.tsx"]) {
      const file = path.join(packageDir, candidate);
      if (await exists(file)) {
        return file;
      }
    }
  }

  return null;
}

async function resolveRelativeModule(fromFile, specifier) {
  if (!specifier.startsWith(".")) {
    return null;
  }
  const base = path.resolve(path.dirname(fromFile), specifier);
  for (const candidate of [
    `${base}.ts`,
    `${base}.tsx`,
    `${base}.svelte`,
    path.join(base, "index.ts"),
    path.join(base, "index.tsx"),
  ]) {
    if (await exists(candidate)) {
      return candidate;
    }
  }
  return null;
}

function exportedNamesFromList(list) {
  const names = [];
  for (const raw of list.split(",")) {
    const cleaned = raw.trim().replace(/^type\s+/, "");
    if (!cleaned || cleaned === "default") {
      continue;
    }
    const alias = cleaned.match(/\bas\s+([A-Za-z_$][A-Za-z0-9_$]*)$/)?.[1];
    const name = alias ?? cleaned.match(/^([A-Za-z_$][A-Za-z0-9_$]*)/)?.[1];
    if (name) {
      names.push(name);
    }
  }
  return names;
}

async function collectExports(file) {
  const key = path.resolve(file);
  if (exportCache.has(key)) {
    return exportCache.get(key);
  }

  const exports = new Set();
  exportCache.set(key, exports);

  const content = await readFile(file, "utf8");

  for (const match of content.matchAll(
    /export\s+(?:type\s+)?\{([\s\S]*?)\}(?:\s+from\s+["'][^"']+["'])?/g
  )) {
    for (const name of exportedNamesFromList(match[1])) {
      exports.add(name);
    }
  }

  for (const match of content.matchAll(
    /export\s+(?:declare\s+)?(?:interface|type|class|function|const|let|var|enum)\s+([A-Za-z_$][A-Za-z0-9_$]*)/g
  )) {
    exports.add(match[1]);
  }

  for (const match of content.matchAll(/export\s+\*\s+from\s+["']([^"']+)["']/g)) {
    const relative = await resolveRelativeModule(file, match[1]);
    if (!relative) {
      continue;
    }
    for (const name of await collectExports(relative)) {
      exports.add(name);
    }
  }

  return exports;
}

function namedImports(content) {
  const imports = [];
  const importRE =
    /import\s+(?:type\s+)?\{([^}]+)\}\s+from\s+["'](@livepeer-frameworks\/[^"']+)["']/g;
  for (const match of content.matchAll(importRE)) {
    for (const raw of match[1].split(",")) {
      const imported = raw
        .trim()
        .replace(/^type\s+/, "")
        .split(/\s+as\s+/)[0]
        ?.trim();
      if (imported) {
        imports.push({ name: imported, specifier: match[2] });
      }
    }
  }
  return imports;
}

function fencedCodeBlocks(content) {
  const blocks = [];
  const fenceRE = /```(?:ts|tsx|js|jsx|svelte)\s*\n([\s\S]*?)^```/gm;
  for (const match of content.matchAll(fenceRE)) {
    blocks.push(match[1]);
  }
  return blocks;
}

const files = await walk(docsRoot);
const failures = [];
let checked = 0;

for (const file of files) {
  const content = await readFile(file, "utf8");
  for (const block of fencedCodeBlocks(content)) {
    for (const imported of namedImports(block)) {
      const source = await sourceForSpecifier(imported.specifier);
      if (!source) {
        continue;
      }
      checked++;
      const exports = await collectExports(source);
      if (!exports.has(imported.name)) {
        failures.push({
          source: path.relative(process.cwd(), file),
          specifier: imported.specifier,
          name: imported.name,
        });
      }
    }
  }
}

if (failures.length > 0) {
  console.error("Docs import names that are not exported by SDK entrypoints:");
  for (const failure of failures) {
    console.error(`- ${failure.source}: ${failure.name} from ${failure.specifier}`);
  }
  process.exit(1);
}

console.log(`Checked ${checked} named SDK import(s) in docs examples.`);

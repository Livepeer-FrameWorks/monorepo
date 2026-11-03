import assert from "node:assert/strict";
import { existsSync, readFileSync, readdirSync, realpathSync, statSync } from "node:fs";
import { dirname, join, relative } from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";

const siteDir = join(dirname(fileURLToPath(import.meta.url)), "..", "..");
const repoDir = join(siteDir, "..");
const manifest = JSON.parse(readFileSync(join(siteDir, "package.json"), "utf8"));
const declared = new Set([
  ...Object.keys(manifest.dependencies ?? {}),
  ...Object.keys(manifest.devDependencies ?? {}),
]);

const workspaceScopes = ["@livepeer-frameworks/", "@frameworks/"];

function sourceFiles(dir) {
  return readdirSync(dir).flatMap((name) => {
    const path = join(dir, name);
    if (statSync(path).isDirectory()) return sourceFiles(path);
    return /\.(jsx?|mjs)$/.test(name) && !name.endsWith(".test.js") ? [path] : [];
  });
}

// Package name of a bare specifier: "@scope/name/sub/path" -> "@scope/name".
const packageName = (specifier) => specifier.split("/").slice(0, 2).join("/");

test("every workspace package the site imports is a declared dependency", () => {
  const missing = new Set();
  for (const file of [...sourceFiles(join(siteDir, "src")), ...sourceFiles(join(siteDir, "app"))]) {
    // Template literals hold code samples shown on the page, not imports.
    const source = readFileSync(file, "utf8").replace(/`[^`]*`/g, "``");
    const specifiers = [
      ...source.matchAll(/(?:from\s+|import\s*\(\s*|import\s+)["']([^"']+)["']/g),
    ].map((match) => match[1]);
    for (const specifier of specifiers) {
      if (!workspaceScopes.some((scope) => specifier.startsWith(scope))) continue;
      const name = packageName(specifier);
      if (!declared.has(name)) missing.add(`${relative(siteDir, file)} imports ${name}`);
    }
  }
  assert.deepEqual([...missing], []);
});

// The stream tester reads the player registry (globalPlayerManager) that the
// player components register into, so the site, the tester and the React
// player must load the same player-core module.
test("the site, the stream tester and the React player share one player-core", () => {
  const corePath = (packageDir) => {
    const link = join(packageDir, "node_modules", "@livepeer-frameworks", "player-core");
    assert.ok(existsSync(link), `${relative(repoDir, packageDir)} has no installed player-core`);
    return realpathSync(link);
  };
  const expected = realpathSync(join(repoDir, "npm_player", "packages", "core"));
  for (const packageDir of [
    siteDir,
    join(repoDir, "npm_player", "packages", "tester"),
    join(repoDir, "npm_player", "packages", "react"),
  ]) {
    assert.equal(corePath(packageDir), expected, relative(repoDir, packageDir));
  }
  assert.ok(
    existsSync(join(siteDir, "node_modules", "@livepeer-frameworks", "player-tester")),
    "website_marketing has no installed player-tester"
  );
});

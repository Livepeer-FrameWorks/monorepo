import assert from "node:assert/strict";
import { existsSync, readFileSync, readdirSync, statSync } from "node:fs";
import { dirname, join, relative, sep } from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";

import tailwindPostcss from "@tailwindcss/postcss";
import tailwindVite from "@tailwindcss/vite";
import postcss from "postcss";
import { resolveConfig } from "vite";

const siteDir = join(dirname(fileURLToPath(import.meta.url)), "..", "..");
const repoDir = join(siteDir, "..");

// Vite's CSS pipeline, @tailwindcss/vite and @tailwindcss/postcss resolve a CSS @import with
// their own fixed conditions (style, development|production, default) and ignore
// resolve.conditions, so the "source" condition the dev servers set never reaches them. A
// workspace SDK stylesheet that @imports another SDK's CSS export must therefore resolve to
// a tracked file under every condition: dist/ exists only after a build.
const scanRoots = ["npm_player/packages", "npm_studio/packages", "packages"];
const skipDirs = new Set(["node_modules", "dist", "build", ".svelte-kit", "coverage"]);

function cssFiles(dir) {
  if (!existsSync(dir)) return [];
  return readdirSync(dir).flatMap((name) => {
    const path = join(dir, name);
    if (statSync(path).isDirectory()) return skipDirs.has(name) ? [] : cssFiles(path);
    return name.endsWith(".css") ? [path] : [];
  });
}

const importers = scanRoots
  .flatMap((root) => cssFiles(join(repoDir, root)))
  .filter((file) => /@import\s+["']@livepeer-frameworks\//.test(readFileSync(file, "utf8")));

const label = (file) => relative(repoDir, file);

function assertFromSource(file, dependencies) {
  const external = dependencies.filter((dep) => dep !== file && !dep.startsWith(dirname(file)));
  assert.ok(external.length > 0, `${label(file)} resolved none of its SDK imports`);
  for (const dep of external) {
    assert.ok(!dep.split(sep).includes("dist"), `${label(file)} resolved ${label(dep)}`);
    assert.ok(existsSync(dep), `${label(file)} resolved missing ${label(dep)}`);
  }
}

async function viteTailwindDependencies(command, file) {
  const config = await resolveConfig(
    {
      configFile: false,
      root: siteDir,
      logLevel: "silent",
      plugins: [tailwindVite()],
      resolve: { conditions: ["source"] },
    },
    command
  );
  const generate = config.plugins.find(
    (plugin) => plugin.name === `@tailwindcss/vite:generate:${command}`
  );
  assert.ok(generate, `@tailwindcss/vite has no ${command} transform`);
  const dependencies = [];
  const context = { environment: undefined, addWatchFile: (dep) => dependencies.push(dep) };
  await generate.transform.handler.call(context, readFileSync(file, "utf8"), file);
  return dependencies;
}

test("SDK stylesheets import other SDK stylesheets", () => {
  assert.ok(importers.length >= 4, `found ${importers.map(label).join(", ")}`);
});

for (const file of importers) {
  for (const command of ["serve", "build"]) {
    test(`@tailwindcss/vite (${command}) resolves ${label(file)} imports from source`, async () => {
      assertFromSource(file, await viteTailwindDependencies(command, file));
    });
  }

  test(`@tailwindcss/postcss resolves ${label(file)} imports from source`, async () => {
    const result = await postcss([
      tailwindPostcss({ base: dirname(file), optimize: false }),
    ]).process(readFileSync(file, "utf8"), { from: file });
    const dependencies = result.messages
      .filter((message) => message.type === "dependency")
      .map((message) => message.file);
    assertFromSource(file, dependencies);
  });
}

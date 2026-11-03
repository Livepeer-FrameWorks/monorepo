import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";

import { studioSnippets } from "./studioSnippets.js";

const studioDir = join(dirname(fileURLToPath(import.meta.url)), "..", "..", "..", "npm_studio");
const read = (path) => readFileSync(join(studioDir, path), "utf8");
const snippets = studioSnippets("https://bridge.example/graphql");

// Attribute names on the opening tag of `tag` in a code sample.
function tagAttributes(snippet, tag) {
  const open = snippet.match(new RegExp(`<${tag}\\b([^>]*)>`));
  assert.ok(open, `sample has no <${tag}> element`);
  return [...open[1].matchAll(/([\w-]+)=/g)].map((match) => match[1]);
}

// Names a sample imports from a package.
const importedNames = (snippet, pkg) =>
  [...snippet.matchAll(new RegExp(`import \\{([^}]+)\\} from ['"]${pkg}['"]`, "g"))]
    .flatMap((match) => match[1].split(","))
    .map((name) => name.trim());

test("the React sample uses StreamCrafter props the React package declares", () => {
  const component = read("packages/react/src/components/StreamCrafter.tsx");
  assert.deepEqual(importedNames(snippets.react, "@livepeer-frameworks/streamcrafter-react"), [
    "StreamCrafter",
  ]);
  assert.match(read("packages/react/src/index.tsx"), /\bStreamCrafter,/);
  for (const prop of tagAttributes(snippets.react, "StreamCrafter")) {
    assert.match(component, new RegExp(`\\n\\s+${prop}\\?:`), `React prop ${prop}`);
  }
  assert.ok(JSON.parse(read("packages/react/package.json")).exports["./streamcrafter.css"]);
});

test("the Svelte sample uses StreamCrafter props the Svelte package declares", () => {
  const component = read("packages/svelte/src/StreamCrafter.svelte");
  assert.deepEqual(importedNames(snippets.svelte, "@livepeer-frameworks/streamcrafter-svelte"), [
    "StreamCrafter",
  ]);
  assert.match(read("packages/svelte/src/index.ts"), /as StreamCrafter\b/);
  for (const prop of tagAttributes(snippets.svelte, "StreamCrafter")) {
    assert.match(component, new RegExp(`\\n\\s+${prop}\\?:`), `Svelte prop ${prop}`);
  }
  assert.ok(JSON.parse(read("packages/svelte/package.json")).exports["./streamcrafter.css"]);
});

test("the web component sample loads the published IIFE and uses declared attributes", () => {
  const element = read("packages/wc/src/components/fw-streamcrafter.ts");
  for (const attribute of tagAttributes(snippets.wc, "fw-streamcrafter")) {
    assert.ok(element.includes(`attribute: "${attribute}"`), `attribute ${attribute}`);
  }
  const iife = read("packages/wc/rollup.config.js").match(/file: "(dist\/[^"]+\.iife\.js)"/)[1];
  for (const src of snippets.wc.matchAll(/src="([^"]+)"/g)) {
    assert.ok(src[1].endsWith(`/streamcrafter-wc/${iife}`), src[1]);
  }
});

test("the vanilla sample uses the createStreamCrafter config and instance API", () => {
  assert.deepEqual(importedNames(snippets.vanilla, "@livepeer-frameworks/streamcrafter-core"), [
    "createStreamCrafter",
  ]);
  assert.match(read("packages/core/src/index.ts"), /export \{ createStreamCrafter \}/);
  const facade = read("packages/core/src/vanilla/createStreamCrafter.ts");
  const types = read("packages/core/src/types.ts");
  const config = snippets.vanilla.match(/createStreamCrafter\(\{([\s\S]*?)\}\)/)[1];
  for (const [, key] of config.matchAll(/^\s+(\w+):/gm)) {
    assert.ok(
      new RegExp(`\\n\\s+${key}\\??:`).test(facade) || new RegExp(`\\n\\s+${key}\\??:`).test(types),
      `config key ${key}`
    );
  }
  for (const [, method] of snippets.vanilla.matchAll(/studio\.(\w+)\(/g)) {
    assert.match(facade, new RegExp(`\\n\\s+${method}\\(`), `instance method ${method}`);
  }
  assert.match(facade, /on\(\s*event: "stateChange"/);
});

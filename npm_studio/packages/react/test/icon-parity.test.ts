import fs from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";

const PACKAGES = path.resolve(__dirname, "../..");
const BINDINGS = { react: "react/src", svelte: "svelte/src", wc: "wc/src" } as const;
const SHAPE = /<(path|rect|circle|line|polyline|polygon|ellipse)\b([^>]*?)\/?>/g;
const ATTRIBUTE = /([a-zA-Z-]+)=(?:"([^"]*)"|\{"?([^"}]*)"?\})/g;
// Drawing attributes that follow the theme or the state, not the shape.
const IGNORED = new Set(["class", "classname", "style", "stroke", "fill", "opacity", "key"]);

function files(dir: string): string[] {
  return fs.readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
    const full = path.join(dir, entry.name);
    if (entry.isDirectory()) return files(full);
    return /\.(tsx|ts|svelte)$/.test(entry.name) ? [full] : [];
  });
}

/** Every shape drawn inside an SVG icon in a binding, with where it is drawn. */
function shapes(dir: string): Map<string, string> {
  const found = new Map<string, string>();
  for (const file of files(path.join(PACKAGES, dir))) {
    const source = fs.readFileSync(file, "utf8");
    for (const svg of source.matchAll(/<svg\b[\s\S]*?<\/svg>/g)) {
      for (const [, tag, attributes] of svg[0].matchAll(SHAPE)) {
        const kept = [...attributes.matchAll(ATTRIBUTE)]
          .map(([, name, quoted, braced]) => [
            name.replace(/[A-Z]/g, (c) => `-${c.toLowerCase()}`),
            (quoted ?? braced).trim(),
          ])
          .filter(([name]) => !IGNORED.has(name) && !name.startsWith("stroke-"))
          .map(([name, value]) => `${name}=${value}`)
          .sort();
        found.set(`${tag}(${kept.join(" ")})`, path.relative(PACKAGES, file));
      }
    }
  }
  return found;
}

describe("studio icons", () => {
  it("draw the same shapes in React, Svelte and the web components", () => {
    const byBinding = Object.fromEntries(
      Object.entries(BINDINGS).map(([binding, dir]) => [binding, shapes(dir)])
    ) as Record<keyof typeof BINDINGS, Map<string, string>>;
    const all = new Set(Object.values(byBinding).flatMap((found) => [...found.keys()]));

    const missing = [...all].flatMap((shape) => {
      const lacking = Object.keys(byBinding).filter(
        (binding) => !byBinding[binding as keyof typeof BINDINGS].has(shape)
      );
      if (lacking.length === 0) return [];
      const where = Object.values(byBinding)
        .find((found) => found.has(shape))!
        .get(shape);
      return [`${shape} in ${where}, not in ${lacking.join(", ")}`];
    });

    expect(missing).toEqual([]);
  });

  it("remove an overlay with the cross icon, not a text glyph, in every binding", () => {
    for (const file of [
      "react/src/components/StudioOverlayControls.tsx",
      "svelte/src/components/StudioOverlayControls.svelte",
      "wc/src/components/fw-sc-overlay-controls.ts",
    ]) {
      expect(fs.readFileSync(path.join(PACKAGES, file), "utf8"), file).not.toMatch(/>\s*×\s*</);
    }
  });
});

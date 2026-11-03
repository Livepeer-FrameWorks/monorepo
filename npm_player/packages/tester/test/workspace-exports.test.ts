import { existsSync, readFileSync, realpathSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

// vitest.config.ts resolves with the "source" condition so the tests run against the
// workspace SDK sources. A dependency whose "." export has no "source" entry falls
// through to dist/, which exists only after a build, so a clean checkout cannot
// resolve it.
const testerDir = join(dirname(fileURLToPath(import.meta.url)), "..");
const manifest = JSON.parse(readFileSync(join(testerDir, "package.json"), "utf8"));
const workspaceDeps = Object.keys(manifest.dependencies ?? {}).filter((name) =>
  name.startsWith("@livepeer-frameworks/")
);

describe("workspace SDK dependencies", () => {
  it("declares at least one", () => {
    expect(workspaceDeps.length).toBeGreaterThan(0);
  });

  it.each(workspaceDeps)("%s exports its source for the source condition", (name) => {
    const pkgDir = realpathSync(join(testerDir, "node_modules", name));
    const pkg = JSON.parse(readFileSync(join(pkgDir, "package.json"), "utf8"));
    const source = pkg.exports?.["."]?.source;
    expect(source, `${name} exports["."].source`).toBeTypeOf("string");
    expect(existsSync(join(pkgDir, source))).toBe(true);
  });
});

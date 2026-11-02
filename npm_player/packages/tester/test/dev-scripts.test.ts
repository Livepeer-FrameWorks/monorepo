import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const here = dirname(fileURLToPath(import.meta.url));
const repo = resolve(here, "../../../..");
const readJson = (path: string) => JSON.parse(readFileSync(resolve(repo, path), "utf8"));

const playgroundName = readJson("npm_player/playground/package.json").name as string;

describe("SDK workspace dev scripts", () => {
  for (const root of ["npm_player", "npm_studio"]) {
    it(`${root} runs the playground by its package name`, () => {
      const scripts = readJson(`${root}/package.json`).scripts as Record<string, string>;
      for (const name of ["playground:dev", "playground:build"]) {
        expect(scripts[name]).toBe(`pnpm --filter ${playgroundName} run ${name.split(":")[1]}`);
      }
      // The workspace roots are not pnpm workspace members, so their own
      // devDependencies are never installed and cannot provide binaries.
      expect(scripts.dev).toBe("pnpm run playground:dev");
    });
  }
});

import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";
import { PUSH_TARGET_PRESETS } from "./pushTargetPresets";

const multistreamingDoc = readFileSync(
  resolve(
    import.meta.dirname,
    "../../../website_docs/src/content/docs/builders/multistreaming.mdx"
  ),
  "utf8"
);

describe("push target presets", () => {
  it("link every platform preset to the page that shows its stream key", () => {
    for (const [key, preset] of Object.entries(PUSH_TARGET_PRESETS)) {
      if (key === "custom") {
        expect(preset.keyHelp).toBeUndefined();
        continue;
      }
      expect(preset.keyHelp?.url, key).toMatch(/^https:\/\//);
    }
  });

  it("match the ingest URLs and key help links in the multistreaming guide", () => {
    for (const [key, preset] of Object.entries(PUSH_TARGET_PRESETS)) {
      if (key === "custom") continue;
      expect(multistreamingDoc, `${key} ingest URL`).toContain(`\`${preset.ingestUrl}\``);
      expect(multistreamingDoc, `${key} key help`).toContain(`(${preset.keyHelp?.url})`);
    }
  });
});

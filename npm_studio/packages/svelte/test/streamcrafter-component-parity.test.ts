import fs from "node:fs";
import path from "node:path";
import { describe, it, expect } from "vitest";
import {
  STREAMCRAFTER_COMPONENT_PARITY_CALLBACK_MARKERS,
  STREAMCRAFTER_COMPONENT_PARITY_CONTEXT_MENU_I18N_KEYS,
} from "../../test-contract/streamcrafter-wrapper-contract";

describe("StreamCrafter Svelte component parity", () => {
  const streamCrafterPath = path.resolve(__dirname, "../src/StreamCrafter.svelte");
  const source = fs.readFileSync(streamCrafterPath, "utf8");

  it("references shared context-menu i18n keys", () => {
    for (const key of STREAMCRAFTER_COMPONENT_PARITY_CONTEXT_MENU_I18N_KEYS) {
      expect(source).toContain(`"${key}"`);
    }
  });

  it("adds cameras through the picker, with no limit of one", () => {
    expect(source).toContain("<CameraPicker");
    expect(source).not.toContain("hasCamera");
  });

  it("keeps theme colours in tokens", () => {
    const panel = fs.readFileSync(
      path.resolve(__dirname, "../src/components/AdvancedPanel.svelte"),
      "utf8"
    );
    for (const file of [source, panel]) {
      expect(file).not.toMatch(/#(1a1b26|565f89|7aa2f7|a9b1d6|c0caf5|9ece6a|f7768e)/i);
      expect(file).not.toMatch(/hsl\(\d|rgba?\(\d|:\s*(white|black)\b/);
    }
    // text-bright is dark in the light presets; the knob takes the track's foreground.
    expect(panel).not.toContain("--fw-sc-text-bright");
    expect(panel).toContain("--fw-sc-on-accent");
  });

  it("invokes shared callback props from component state flow", () => {
    expect(source).toContain(`${STREAMCRAFTER_COMPONENT_PARITY_CALLBACK_MARKERS.onStateChange}?.(`);
    expect(source).toContain(`${STREAMCRAFTER_COMPONENT_PARITY_CALLBACK_MARKERS.onError}?.(`);
  });
});

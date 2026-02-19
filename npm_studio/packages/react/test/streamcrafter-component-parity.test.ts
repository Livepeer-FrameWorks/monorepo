import fs from "node:fs";
import path from "node:path";
import { describe, it, expect } from "vitest";
import {
  STREAMCRAFTER_COMPONENT_PARITY_CALLBACK_MARKERS,
  STREAMCRAFTER_COMPONENT_PARITY_CONTEXT_MENU_I18N_KEYS,
} from "../../test-contract/streamcrafter-wrapper-contract";

describe("StreamCrafter React component parity", () => {
  const streamCrafterPath = path.resolve(__dirname, "../src/components/StreamCrafter.tsx");
  const source = fs.readFileSync(streamCrafterPath, "utf8");

  it("references shared context-menu i18n keys", () => {
    for (const key of STREAMCRAFTER_COMPONENT_PARITY_CONTEXT_MENU_I18N_KEYS) {
      expect(source).toContain(`"${key}"`);
    }
  });

  it("invokes shared callback props from component state flow", () => {
    expect(source).toContain(`${STREAMCRAFTER_COMPONENT_PARITY_CALLBACK_MARKERS.onStateChange}?.(`);
    expect(source).toContain(`${STREAMCRAFTER_COMPONENT_PARITY_CALLBACK_MARKERS.onError}?.(`);
  });
});

import fs from "node:fs";
import path from "node:path";
import { describe, it, expect } from "vitest";
import {
  PLAYER_COMPONENT_PARITY_CONTEXT_MENU_COMMON_LABELS,
  PLAYER_COMPONENT_PARITY_CONTEXT_MENU_WC_ROLE_MARKERS,
} from "../../test-contract/player-wrapper-contract";

describe("FwPlayer context menu markup parity", () => {
  it("includes checkbox menuitem role markers", () => {
    const playerPath = path.resolve(__dirname, "../src/components/fw-player.ts");
    const source = fs.readFileSync(playerPath, "utf8");

    for (const marker of PLAYER_COMPONENT_PARITY_CONTEXT_MENU_WC_ROLE_MARKERS) {
      expect(source).toContain(marker);
    }
  });

  it("contains shared context-menu labels", () => {
    const playerPath = path.resolve(__dirname, "../src/components/fw-player.ts");
    const source = fs.readFileSync(playerPath, "utf8");

    for (const label of PLAYER_COMPONENT_PARITY_CONTEXT_MENU_COMMON_LABELS) {
      expect(source).toContain(label);
    }
  });
});

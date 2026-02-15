import fs from "node:fs";
import path from "node:path";
import { describe, it, expect } from "vitest";
import {
  PLAYER_COMPONENT_PARITY_CONTEXT_MENU_COMMON_LABELS,
  PLAYER_COMPONENT_PARITY_CONTEXT_MENU_REACT_SVELTE_SELECT_MARKER,
} from "../../test-contract/player-wrapper-contract";

describe("Player context menu handlers", () => {
  it("uses onSelect for ContextMenuItem actions", () => {
    const playerPath = path.resolve(__dirname, "../src/components/Player.tsx");
    const source = fs.readFileSync(playerPath, "utf8");

    expect(source).toContain(PLAYER_COMPONENT_PARITY_CONTEXT_MENU_REACT_SVELTE_SELECT_MARKER);
    expect(source).not.toMatch(/<ContextMenuItem\\s+onClick=/);
  });

  it("contains shared context-menu labels", () => {
    const playerPath = path.resolve(__dirname, "../src/components/Player.tsx");
    const source = fs.readFileSync(playerPath, "utf8");

    for (const label of PLAYER_COMPONENT_PARITY_CONTEXT_MENU_COMMON_LABELS) {
      expect(source).toContain(label);
    }
  });
});

import { describe, it, expect } from "vitest";
import { FwPlayerControls } from "../src/components/fw-player-controls.js";

describe("FwPlayerControls", () => {
  it("is a class that extends HTMLElement", () => {
    expect(FwPlayerControls).toBeDefined();
    expect(FwPlayerControls.prototype instanceof HTMLElement).toBe(true);
  });

  it("has styles array", () => {
    expect(Array.isArray(FwPlayerControls.styles)).toBe(true);
    expect(FwPlayerControls.styles.length).toBeGreaterThan(0);
  });
});

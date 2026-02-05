import { describe, expect, it } from "vitest";

import {
  applyLayout,
  createDefaultLayoutConfig,
  getLayoutPresets,
  getAvailablePresets,
  getMinSourcesForLayout,
  isLayoutAvailable,
  getAvailableLayoutModes,
  createPipLayoutConfig,
  createSideBySideLayoutConfig,
  applyPipLayout,
  LAYOUT_PRESETS,
} from "../src/core/layouts/index";
import type { Layer, LayoutConfig, LayoutMode } from "../src/types";

// Helper: assert all layer coordinates are within [0, 1]
function assertBoundsValid(layers: Layer[]) {
  for (const layer of layers) {
    const { x, y, width, height } = layer.transform;
    expect(x).toBeGreaterThanOrEqual(0);
    expect(y).toBeGreaterThanOrEqual(0);
    expect(width).toBeGreaterThan(0);
    expect(height).toBeGreaterThan(0);
    expect(x + width).toBeLessThanOrEqual(1.001); // floating-point tolerance
    expect(y + height).toBeLessThanOrEqual(1.001);
  }
}

// Helper: assert zIndex ordering is non-negative and sensible
function assertZIndexValid(layers: Layer[]) {
  for (const layer of layers) {
    expect(layer.zIndex).toBeGreaterThanOrEqual(0);
  }
}

describe("Layouts", () => {
  // =========================================================================
  // applyLayout — empty sources
  // =========================================================================
  describe("empty sources", () => {
    it("returns [] for empty sourceIds", () => {
      expect(applyLayout({ mode: "solo" }, [])).toEqual([]);
    });

    it("returns [] for grid with empty sourceIds", () => {
      expect(applyLayout({ mode: "grid" }, [])).toEqual([]);
    });
  });

  // =========================================================================
  // Solo layout
  // =========================================================================
  describe("solo layout", () => {
    it("fills entire canvas with single source", () => {
      const layers = applyLayout({ mode: "solo" }, ["a"]);
      expect(layers).toHaveLength(1);
      expect(layers[0].transform.x).toBe(0);
      expect(layers[0].transform.y).toBe(0);
      expect(layers[0].transform.width).toBe(1);
      expect(layers[0].transform.height).toBe(1);
      expect(layers[0].sourceId).toBe("a");
    });

    it("fullscreen alias behaves identically to solo", () => {
      const solo = applyLayout({ mode: "solo" }, ["a"]);
      const full = applyLayout({ mode: "fullscreen" as LayoutMode }, ["a"]);
      expect(solo[0].transform).toEqual(full[0].transform);
    });

    it("unknown mode falls back to solo", () => {
      const layers = applyLayout({ mode: "bogus" as LayoutMode }, ["a"]);
      expect(layers).toHaveLength(1);
      expect(layers[0].transform.width).toBe(1);
    });
  });

  // =========================================================================
  // PiP layouts — all 4 corners
  // =========================================================================
  describe("pip layouts", () => {
    const PIP_SCALE = 0.25;
    const PIP_PADDING = 0.02;

    it.each([
      ["pip-br", 1 - PIP_SCALE - PIP_PADDING, 1 - PIP_SCALE - PIP_PADDING],
      ["pip-bl", PIP_PADDING, 1 - PIP_SCALE - PIP_PADDING],
      ["pip-tr", 1 - PIP_SCALE - PIP_PADDING, PIP_PADDING],
      ["pip-tl", PIP_PADDING, PIP_PADDING],
    ] as const)("%s places PiP at correct corner", (mode, expectedX, expectedY) => {
      const layers = applyLayout({ mode }, ["main", "pip"]);
      expect(layers).toHaveLength(2);

      // Main is fullscreen
      expect(layers[0].transform.width).toBe(1);
      expect(layers[0].transform.height).toBe(1);

      // PiP position
      expect(layers[1].transform.x).toBeCloseTo(expectedX, 5);
      expect(layers[1].transform.y).toBeCloseTo(expectedY, 5);
      expect(layers[1].transform.width).toBe(PIP_SCALE);
      expect(layers[1].transform.height).toBe(PIP_SCALE);
      expect(layers[1].transform.borderRadius).toBe(8);
    });

    it("pip alias maps to pip-br", () => {
      const pip = applyLayout({ mode: "pip" as LayoutMode }, ["a", "b"]);
      const pipBr = applyLayout({ mode: "pip-br" }, ["a", "b"]);
      expect(pip[1].transform).toEqual(pipBr[1].transform);
    });

    it("respects custom pipScale", () => {
      const layers = applyLayout({ mode: "pip-br", pipScale: 0.4 }, ["a", "b"]);
      expect(layers[1].transform.width).toBe(0.4);
      expect(layers[1].transform.height).toBe(0.4);
    });

    it("falls back to solo with < 2 sources", () => {
      const layers = applyLayout({ mode: "pip-br" }, ["a"]);
      expect(layers).toHaveLength(1);
      expect(layers[0].transform.width).toBe(1);
    });

    it("PiP overlay has higher zIndex than main", () => {
      const layers = applyLayout({ mode: "pip-br" }, ["a", "b"]);
      expect(layers[1].zIndex).toBeGreaterThan(layers[0].zIndex);
    });
  });

  // =========================================================================
  // Split layouts
  // =========================================================================
  describe("split layouts", () => {
    const GAP = 0.005;
    const HALF_GAP = GAP / 2;

    it("split-h creates two side-by-side columns", () => {
      const layers = applyLayout({ mode: "split-h" }, ["a", "b"]);
      expect(layers).toHaveLength(2);
      // Left panel
      expect(layers[0].transform.x).toBe(0);
      expect(layers[0].transform.width).toBeCloseTo(0.5 - HALF_GAP, 5);
      expect(layers[0].transform.height).toBe(1);
      // Right panel
      expect(layers[1].transform.x).toBeCloseTo(0.5 + HALF_GAP, 5);
      expect(layers[1].transform.height).toBe(1);
    });

    it("side-by-side alias maps to split-h", () => {
      const split = applyLayout({ mode: "split-h" }, ["a", "b"]);
      const sbs = applyLayout({ mode: "side-by-side" as LayoutMode }, ["a", "b"]);
      expect(split[0].transform).toEqual(sbs[0].transform);
      expect(split[1].transform).toEqual(sbs[1].transform);
    });

    it("split-v creates two stacked rows", () => {
      const layers = applyLayout({ mode: "split-v" }, ["a", "b"]);
      expect(layers).toHaveLength(2);
      // Top panel
      expect(layers[0].transform.y).toBe(0);
      expect(layers[0].transform.width).toBe(1);
      expect(layers[0].transform.height).toBeCloseTo(0.5 - HALF_GAP, 5);
      // Bottom panel
      expect(layers[1].transform.y).toBeCloseTo(0.5 + HALF_GAP, 5);
    });

    it("split falls back to solo with < 2 sources", () => {
      const layers = applyLayout({ mode: "split-h" }, ["a"]);
      expect(layers).toHaveLength(1);
      expect(layers[0].transform.width).toBe(1);
    });
  });

  // =========================================================================
  // Focus layouts (70/30 split)
  // =========================================================================
  describe("focus layouts", () => {
    it("focus-l gives left 70% of width", () => {
      const layers = applyLayout({ mode: "focus-l" }, ["a", "b"]);
      expect(layers[0].transform.width).toBeCloseTo(0.7 - 0.0025, 4);
    });

    it("focus-r gives left 30% (right gets 70%)", () => {
      const layers = applyLayout({ mode: "focus-r" }, ["a", "b"]);
      expect(layers[0].transform.width).toBeCloseTo(0.3 - 0.0025, 4);
      expect(layers[1].transform.width).toBeCloseTo(1 - 0.3 - 0.0025, 4);
    });
  });

  // =========================================================================
  // Grid layout
  // =========================================================================
  describe("grid layout", () => {
    it("1 source falls back to solo", () => {
      const layers = applyLayout({ mode: "grid" }, ["a"]);
      expect(layers).toHaveLength(1);
      expect(layers[0].transform.width).toBe(1);
    });

    it("2 sources falls back to horizontal split", () => {
      const layers = applyLayout({ mode: "grid" }, ["a", "b"]);
      expect(layers).toHaveLength(2);
      // Both should be half-width
      expect(layers[0].transform.width).toBeCloseTo(layers[1].transform.width, 5);
    });

    it("3 sources: 2 columns, last row centered", () => {
      const layers = applyLayout({ mode: "grid" }, ["a", "b", "c"]);
      expect(layers).toHaveLength(3);

      // First two on top row
      expect(layers[0].transform.y).toBe(layers[1].transform.y);

      // Third item on second row, should be centered (not at x=0)
      expect(layers[2].transform.x).toBeGreaterThan(0);
    });

    it("4 sources: perfect 2x2 grid", () => {
      const layers = applyLayout({ mode: "grid" }, ["a", "b", "c", "d"]);
      expect(layers).toHaveLength(4);

      // All cells should be equal size
      const widths = layers.map((l) => l.transform.width);
      const heights = layers.map((l) => l.transform.height);
      for (let i = 1; i < 4; i++) {
        expect(widths[i]).toBeCloseTo(widths[0], 5);
        expect(heights[i]).toBeCloseTo(heights[0], 5);
      }
    });

    it("5 sources: 3 cols, last row 2 items centered", () => {
      const layers = applyLayout({ mode: "grid" }, ["a", "b", "c", "d", "e"]);
      expect(layers).toHaveLength(5);

      // Last two items (index 3,4) on second row
      // Should be offset from x=0 for centering
      expect(layers[3].transform.x).toBeGreaterThan(0);
    });

    it("all grid cells within [0,1] bounds", () => {
      for (const count of [2, 3, 4, 5, 6, 7, 8, 9]) {
        const ids = Array.from({ length: count }, (_, i) => `s${i}`);
        const layers = applyLayout({ mode: "grid" }, ids);
        assertBoundsValid(layers);
      }
    });
  });

  // =========================================================================
  // Stack layout
  // =========================================================================
  describe("stack layout", () => {
    it("0 sources returns []", () => {
      expect(applyLayout({ mode: "stack" }, [])).toEqual([]);
    });

    it("1 source falls back to solo", () => {
      const layers = applyLayout({ mode: "stack" }, ["a"]);
      expect(layers).toHaveLength(1);
      expect(layers[0].transform.width).toBe(1);
      expect(layers[0].transform.height).toBe(1);
    });

    it("2 sources: equal vertical division", () => {
      const layers = applyLayout({ mode: "stack" }, ["a", "b"]);
      expect(layers).toHaveLength(2);
      expect(layers[0].transform.width).toBe(1);
      expect(layers[1].transform.width).toBe(1);
      expect(layers[0].transform.height).toBeCloseTo(layers[1].transform.height, 5);
    });

    it("3 sources: each gets ~1/3 height", () => {
      const layers = applyLayout({ mode: "stack" }, ["a", "b", "c"]);
      expect(layers).toHaveLength(3);
      const gap = 0.005;
      const expected = (1 - gap * 2) / 3;
      for (const l of layers) {
        expect(l.transform.height).toBeCloseTo(expected, 5);
        expect(l.transform.width).toBe(1);
      }
    });

    it("all stack cells within bounds", () => {
      const ids = Array.from({ length: 5 }, (_, i) => `s${i}`);
      const layers = applyLayout({ mode: "stack" }, ids);
      assertBoundsValid(layers);
    });
  });

  // =========================================================================
  // 3-source layouts
  // =========================================================================
  describe("3-source layouts", () => {
    describe("pip-dual-br", () => {
      it("creates main + 2 PiPs in bottom-right", () => {
        const layers = applyLayout({ mode: "pip-dual-br" }, ["a", "b", "c"]);
        expect(layers).toHaveLength(3);
        expect(layers[0].transform.width).toBe(1); // main fullscreen
        expect(layers[1].zIndex).toBe(1);
        expect(layers[2].zIndex).toBe(2);
        // Both PiPs on right side
        expect(layers[1].transform.x).toBeGreaterThan(0.5);
        expect(layers[2].transform.x).toBeGreaterThan(0.5);
        // Second PiP below first
        expect(layers[2].transform.y).toBeGreaterThan(layers[1].transform.y);
      });

      it("falls back to pip-br with < 3 sources", () => {
        const layers = applyLayout({ mode: "pip-dual-br" }, ["a", "b"]);
        expect(layers).toHaveLength(2);
      });
    });

    describe("pip-dual-bl", () => {
      it("creates main + 2 PiPs in bottom-left", () => {
        const layers = applyLayout({ mode: "pip-dual-bl" }, ["a", "b", "c"]);
        expect(layers).toHaveLength(3);
        // Both PiPs on left side
        expect(layers[1].transform.x).toBeLessThan(0.5);
        expect(layers[2].transform.x).toBeLessThan(0.5);
      });
    });

    describe("split-pip-l", () => {
      it("creates split + PiP on left half", () => {
        const layers = applyLayout({ mode: "split-pip-l" }, ["a", "b", "c"]);
        expect(layers).toHaveLength(3);
        // Two split panels
        expect(layers[0].transform.height).toBe(1);
        expect(layers[1].transform.height).toBe(1);
        // PiP on left side
        expect(layers[2].transform.x + layers[2].transform.width).toBeLessThan(0.5);
        expect(layers[2].zIndex).toBe(1);
      });

      it("falls back to split with < 3 sources", () => {
        const layers = applyLayout({ mode: "split-pip-l" }, ["a", "b"]);
        expect(layers).toHaveLength(2);
      });
    });

    describe("split-pip-r", () => {
      it("creates split + PiP on right half", () => {
        const layers = applyLayout({ mode: "split-pip-r" }, ["a", "b", "c"]);
        expect(layers).toHaveLength(3);
        // PiP on right side
        expect(layers[2].transform.x).toBeGreaterThan(0.5);
      });
    });
  });

  // =========================================================================
  // Featured layouts
  // =========================================================================
  describe("featured layouts", () => {
    it("featured (bottom): main takes ~80%, thumbnails in bottom strip", () => {
      const layers = applyLayout({ mode: "featured" }, ["a", "b", "c"]);
      expect(layers).toHaveLength(3);

      // Main at top, spanning full width
      expect(layers[0].transform.x).toBe(0);
      expect(layers[0].transform.y).toBe(0);
      expect(layers[0].transform.width).toBe(1);
      expect(layers[0].transform.height).toBeGreaterThan(0.7);

      // Thumbnails below main
      expect(layers[1].transform.y).toBeGreaterThan(0.7);
      expect(layers[2].transform.y).toBeGreaterThan(0.7);
    });

    it("featured-r (right): main on left, thumbnails in right strip", () => {
      const layers = applyLayout({ mode: "featured-r" }, ["a", "b", "c"]);
      expect(layers).toHaveLength(3);

      // Main on left, spanning full height
      expect(layers[0].transform.x).toBe(0);
      expect(layers[0].transform.height).toBe(1);
      expect(layers[0].transform.width).toBeGreaterThan(0.7);

      // Thumbnails to the right of main
      expect(layers[1].transform.x).toBeGreaterThan(0.7);
      expect(layers[2].transform.x).toBeGreaterThan(0.7);
    });

    it("featured falls back to split with 2 sources", () => {
      const layers = applyLayout({ mode: "featured" }, ["a", "b"]);
      expect(layers).toHaveLength(2);
      // Should be a 75/25 vertical split
      expect(layers[0].transform.height).toBeCloseTo(0.75 - 0.0025, 4);
    });

    it("featured handles 4+ thumbnails", () => {
      const layers = applyLayout({ mode: "featured" }, ["a", "b", "c", "d", "e"]);
      expect(layers).toHaveLength(5);
      // All thumbnails in the bottom strip
      for (let i = 1; i < 5; i++) {
        expect(layers[i].transform.y).toBeGreaterThan(0.7);
      }
      assertBoundsValid(layers);
    });
  });

  // =========================================================================
  // Scaling mode propagation
  // =========================================================================
  describe("scaling mode", () => {
    it("defaults to letterbox", () => {
      const layers = applyLayout({ mode: "solo" }, ["a"]);
      expect(layers[0].scalingMode).toBe("letterbox");
    });

    it("propagates custom scalingMode to all layers", () => {
      const layers = applyLayout({ mode: "grid", scalingMode: "crop" }, ["a", "b", "c", "d"]);
      for (const l of layers) {
        expect(l.scalingMode).toBe("crop");
      }
    });

    it("stretch mode applies to pip layers", () => {
      const layers = applyLayout({ mode: "pip-br", scalingMode: "stretch" }, ["a", "b"]);
      expect(layers[0].scalingMode).toBe("stretch");
      expect(layers[1].scalingMode).toBe("stretch");
    });
  });

  // =========================================================================
  // Layer structure invariants
  // =========================================================================
  describe("invariants", () => {
    const allModes: LayoutMode[] = [
      "solo",
      "pip-br",
      "pip-bl",
      "pip-tr",
      "pip-tl",
      "split-h",
      "split-v",
      "focus-l",
      "focus-r",
      "grid",
      "stack",
      "pip-dual-br",
      "pip-dual-bl",
      "split-pip-l",
      "split-pip-r",
      "featured",
      "featured-r",
    ];

    it("every layout produces layers with valid zIndex", () => {
      for (const mode of allModes) {
        const min = LAYOUT_PRESETS.find((p) => p.mode === mode)?.minSources ?? 1;
        const ids = Array.from({ length: Math.max(min, 3) }, (_, i) => `s${i}`);
        const layers = applyLayout({ mode }, ids);
        assertZIndexValid(layers);
      }
    });

    it("every layout produces layers within [0,1] bounds", () => {
      for (const mode of allModes) {
        const min = LAYOUT_PRESETS.find((p) => p.mode === mode)?.minSources ?? 1;
        const ids = Array.from({ length: Math.max(min, 3) }, (_, i) => `s${i}`);
        const layers = applyLayout({ mode }, ids);
        assertBoundsValid(layers);
      }
    });

    it("every layer has required fields", () => {
      const layers = applyLayout({ mode: "grid" }, ["a", "b", "c"]);
      for (const l of layers) {
        expect(l).toHaveProperty("id");
        expect(l).toHaveProperty("sourceId");
        expect(l).toHaveProperty("visible", true);
        expect(l).toHaveProperty("locked", false);
        expect(typeof l.zIndex).toBe("number");
        expect(l.transform).toHaveProperty("opacity", 1);
        expect(l.transform).toHaveProperty("rotation", 0);
      }
    });
  });

  // =========================================================================
  // Utility functions
  // =========================================================================
  describe("getAvailablePresets", () => {
    it("returns only solo for 1 source", () => {
      const presets = getAvailablePresets(1);
      expect(presets.every((p) => p.minSources <= 1 && p.maxSources >= 1)).toBe(true);
      expect(presets.some((p) => p.mode === "solo")).toBe(true);
      expect(presets.some((p) => p.mode === "pip-br")).toBe(false);
    });

    it("returns 2-source layouts for 2 sources", () => {
      const presets = getAvailablePresets(2);
      const modes = presets.map((p) => p.mode);
      expect(modes).toContain("pip-br");
      expect(modes).toContain("split-h");
      expect(modes).toContain("grid");
      expect(modes).not.toContain("solo");
      expect(modes).not.toContain("pip-dual-br");
    });

    it("returns 3-source layouts for 3 sources", () => {
      const presets = getAvailablePresets(3);
      const modes = presets.map((p) => p.mode);
      expect(modes).toContain("pip-dual-br");
      expect(modes).toContain("split-pip-l");
      expect(modes).toContain("featured");
      expect(modes).toContain("grid");
    });

    it("returns 0 for 0 sources", () => {
      expect(getAvailablePresets(0)).toEqual([]);
    });
  });

  describe("isLayoutAvailable", () => {
    it("solo available for 1 source", () => {
      expect(isLayoutAvailable("solo", 1)).toBe(true);
    });

    it("solo not available for 2 sources", () => {
      expect(isLayoutAvailable("solo", 2)).toBe(false);
    });

    it("pip-br available for 2 sources", () => {
      expect(isLayoutAvailable("pip-br", 2)).toBe(true);
    });

    it("pip-br not available for 1 source", () => {
      expect(isLayoutAvailable("pip-br", 1)).toBe(false);
    });

    it("grid available for high source counts", () => {
      expect(isLayoutAvailable("grid", 50)).toBe(true);
    });

    it("returns false for unknown mode", () => {
      expect(isLayoutAvailable("nonexistent" as LayoutMode, 5)).toBe(false);
    });
  });

  describe("getMinSourcesForLayout", () => {
    it("solo requires 1", () => {
      expect(getMinSourcesForLayout("solo")).toBe(1);
    });

    it("pip-br requires 2", () => {
      expect(getMinSourcesForLayout("pip-br")).toBe(2);
    });

    it("pip-dual-br requires 3", () => {
      expect(getMinSourcesForLayout("pip-dual-br")).toBe(3);
    });

    it("unknown mode returns 1", () => {
      expect(getMinSourcesForLayout("unknown" as LayoutMode)).toBe(1);
    });
  });

  describe("getLayoutPresets", () => {
    it("returns all presets", () => {
      const presets = getLayoutPresets();
      expect(presets.length).toBe(LAYOUT_PRESETS.length);
      expect(presets).toBe(LAYOUT_PRESETS);
    });
  });

  describe("getAvailableLayoutModes", () => {
    it("returns all mode strings", () => {
      const modes = getAvailableLayoutModes();
      expect(modes).toContain("solo");
      expect(modes).toContain("grid");
      expect(modes).toContain("featured-r");
      expect(modes.length).toBe(LAYOUT_PRESETS.length);
    });
  });

  describe("createDefaultLayoutConfig", () => {
    it("returns solo + letterbox", () => {
      const config = createDefaultLayoutConfig();
      expect(config.mode).toBe("solo");
      expect(config.scalingMode).toBe("letterbox");
    });
  });

  // =========================================================================
  // Legacy factory functions
  // =========================================================================
  describe("legacy factories", () => {
    it("createPipLayoutConfig default is bottom-right", () => {
      const config = createPipLayoutConfig();
      expect(config.mode).toBe("pip-br");
      expect(config.pipScale).toBe(0.25);
    });

    it.each([
      ["top-left", "pip-tl"],
      ["top-right", "pip-tr"],
      ["bottom-left", "pip-bl"],
      ["bottom-right", "pip-br"],
    ] as const)("createPipLayoutConfig(%s) → %s", (position, expectedMode) => {
      const config = createPipLayoutConfig(position);
      expect(config.mode).toBe(expectedMode);
    });

    it("createPipLayoutConfig accepts custom scale", () => {
      const config = createPipLayoutConfig("bottom-right", 0.4);
      expect(config.pipScale).toBe(0.4);
    });

    it("createSideBySideLayoutConfig default is horizontal", () => {
      const config = createSideBySideLayoutConfig();
      expect(config.mode).toBe("split-h");
      expect(config.splitRatio).toBe(0.5);
    });

    it("createSideBySideLayoutConfig vertical", () => {
      const config = createSideBySideLayoutConfig(0.6, "vertical");
      expect(config.mode).toBe("split-v");
      expect(config.splitRatio).toBe(0.6);
    });
  });

  // =========================================================================
  // Direct applyPipLayout export
  // =========================================================================
  describe("applyPipLayout direct", () => {
    it("works with explicit corner", () => {
      const layers = applyPipLayout(["a", "b"], "tl");
      expect(layers).toHaveLength(2);
      expect(layers[1].transform.x).toBeCloseTo(0.02, 5);
      expect(layers[1].transform.y).toBeCloseTo(0.02, 5);
    });

    it("accepts custom pip scale", () => {
      const layers = applyPipLayout(["a", "b"], "br", 0.3);
      expect(layers[1].transform.width).toBe(0.3);
    });
  });
});

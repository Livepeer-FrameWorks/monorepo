import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const { MOCK_DEFAULT_BLUEPRINTS, MOCK_DEFAULT_STRUCTURE } = vi.hoisted(() => ({
  MOCK_DEFAULT_BLUEPRINTS: { play: () => null, pause: () => null } as Record<string, Function>,
  MOCK_DEFAULT_STRUCTURE: { type: "container", children: [] },
}));

vi.mock("../src/vanilla/defaultBlueprints", () => ({
  DEFAULT_BLUEPRINTS: MOCK_DEFAULT_BLUEPRINTS,
}));

vi.mock("../src/vanilla/defaultStructure", () => ({
  DEFAULT_STRUCTURE: MOCK_DEFAULT_STRUCTURE,
}));

import {
  FwSkins,
  registerSkin,
  resolveSkin,
  type SkinDefinition,
} from "../src/vanilla/SkinRegistry";

describe("SkinRegistry", () => {
  const origSkins: Record<string, SkinDefinition> = {};

  beforeEach(() => {
    // Save built-in skins registered at import
    Object.assign(origSkins, FwSkins);
  });

  afterEach(() => {
    // Restore to original state
    for (const key of Object.keys(FwSkins)) {
      if (!(key in origSkins)) delete FwSkins[key];
    }
    Object.assign(FwSkins, origSkins);
    vi.restoreAllMocks();
  });

  describe("registerSkin", () => {
    it("adds a skin to FwSkins", () => {
      registerSkin("mytheme", { tokens: { "--fw-accent": "red" } });
      expect(FwSkins["mytheme"]).toEqual({ tokens: { "--fw-accent": "red" } });
    });

    it("overwrites existing skin", () => {
      registerSkin("test", { tokens: { a: "1" } });
      registerSkin("test", { tokens: { a: "2" } });
      expect(FwSkins["test"]!.tokens!.a).toBe("2");
    });
  });

  describe("resolveSkin", () => {
    it("returns defaults for unknown skin", () => {
      const result = resolveSkin("nonexistent");
      expect(result.structure).toBe(MOCK_DEFAULT_STRUCTURE);
      expect(result.blueprints).toEqual(MOCK_DEFAULT_BLUEPRINTS);
      expect(result.icons).toEqual({});
      expect(result.tokens).toEqual({});
      expect(result.css).toBe("");
    });

    it("resolves a single skin with structure override", () => {
      const customStructure = { type: "custom", children: [] };
      registerSkin("withstructure", { structure: { main: customStructure as any } });
      const result = resolveSkin("withstructure");
      expect(result.structure).toBe(customStructure);
    });

    it("resolves a single skin with blueprint overrides", () => {
      const customPlay = () => null;
      registerSkin("withblueprints", { blueprints: { play: customPlay } });
      const result = resolveSkin("withblueprints");
      expect(result.blueprints.play).toBe(customPlay);
      // Original pause should still be there
      expect(result.blueprints.pause).toBe(MOCK_DEFAULT_BLUEPRINTS.pause);
    });

    it("resolves a single skin with icons", () => {
      registerSkin("withicons", { icons: { play: { svg: "<svg/>", size: 24 } } });
      const result = resolveSkin("withicons");
      expect(result.icons.play).toEqual({ svg: "<svg/>", size: 24 });
    });

    it("resolves a single skin with tokens", () => {
      registerSkin("withtokens", { tokens: { "--fw-accent": "#ff0" } });
      const result = resolveSkin("withtokens");
      expect(result.tokens["--fw-accent"]).toBe("#ff0");
    });

    it("resolves a single skin with CSS", () => {
      registerSkin("withcss", { css: { skin: ".custom { color: red; }" } });
      const result = resolveSkin("withcss");
      expect(result.css).toContain(".custom { color: red; }");
    });

    it("resolves inheritance chain — child overrides parent", () => {
      registerSkin("parent", {
        tokens: { "--a": "parent-a", "--b": "parent-b" },
        icons: { play: { svg: "<parent/>" } },
      });
      registerSkin("child", {
        inherit: "parent",
        tokens: { "--a": "child-a" },
        icons: { pause: { svg: "<child/>" } },
      });

      const result = resolveSkin("child");
      expect(result.tokens["--a"]).toBe("child-a");
      expect(result.tokens["--b"]).toBe("parent-b");
      expect(result.icons.play).toEqual({ svg: "<parent/>" });
      expect(result.icons.pause).toEqual({ svg: "<child/>" });
    });

    it("resolves multi-level inheritance", () => {
      registerSkin("grandparent", { tokens: { "--gp": "1" } });
      registerSkin("parent2", { inherit: "grandparent", tokens: { "--p": "2" } });
      registerSkin("child2", { inherit: "parent2", tokens: { "--c": "3" } });

      const result = resolveSkin("child2");
      expect(result.tokens["--gp"]).toBe("1");
      expect(result.tokens["--p"]).toBe("2");
      expect(result.tokens["--c"]).toBe("3");
    });

    it("detects circular inheritance and warns", () => {
      const warnSpy = vi.spyOn(console, "warn").mockImplementation(() => {});
      registerSkin("a", { inherit: "b" });
      registerSkin("b", { inherit: "a" });

      const result = resolveSkin("a");
      expect(warnSpy).toHaveBeenCalledWith(expect.stringContaining("Circular inheritance"));
      // Should still return valid result
      expect(result.structure).toBe(MOCK_DEFAULT_STRUCTURE);
    });

    it("concatenates CSS from chain", () => {
      registerSkin("base", { css: { skin: "base-css" } });
      registerSkin("overlay", { inherit: "base", css: { skin: "overlay-css" } });

      const result = resolveSkin("overlay");
      expect(result.css).toContain("base-css");
      expect(result.css).toContain("overlay-css");
    });

    it("child structure overrides parent structure", () => {
      const parentStruct = { type: "parent" } as any;
      const childStruct = { type: "child" } as any;
      registerSkin("sp", { structure: { main: parentStruct } });
      registerSkin("sc", { inherit: "sp", structure: { main: childStruct } });

      const result = resolveSkin("sc");
      expect(result.structure).toBe(childStruct);
    });
  });

  describe("built-in default skin", () => {
    it("is registered on module load", () => {
      expect(FwSkins["default"]).toBeDefined();
    });

    it("has structure and blueprints", () => {
      const def = FwSkins["default"]!;
      expect(def.structure).toBeDefined();
      expect(def.blueprints).toBeDefined();
    });
  });
});

import { describe, expect, it, vi } from "vitest";

import {
  resolveStudioTheme,
  getAvailableStudioThemes,
  applyStudioTheme,
  applyStudioThemeOverrides,
  clearStudioTheme,
  studioThemeOverridesToStyle,
  type FwThemePreset,
  type StudioThemeOverrides,
} from "../src/StudioThemeManager";

function createMockElement(): HTMLElement {
  const styles: Record<string, string> = {};
  const attrs: Record<string, string> = {};
  return {
    style: {
      setProperty: vi.fn((prop: string, value: string) => {
        styles[prop] = value;
      }),
      removeProperty: vi.fn((prop: string) => {
        delete styles[prop];
      }),
      _styles: styles,
    },
    setAttribute: vi.fn((name: string, value: string) => {
      attrs[name] = value;
    }),
    removeAttribute: vi.fn((name: string) => {
      delete attrs[name];
    }),
    _attrs: attrs,
  } as unknown as HTMLElement;
}

describe("StudioThemeManager", () => {
  // ===========================================================================
  // resolveStudioTheme
  // ===========================================================================
  describe("resolveStudioTheme", () => {
    it("returns null for default theme (CSS-only)", () => {
      expect(resolveStudioTheme("default")).toBeNull();
    });

    it("returns overrides for tokyo-night", () => {
      const theme = resolveStudioTheme("tokyo-night");
      expect(theme).not.toBeNull();
      expect(theme!.surface).toBeDefined();
      expect(theme!.accent).toBeDefined();
    });

    it("returns overrides for dracula", () => {
      const theme = resolveStudioTheme("dracula");
      expect(theme).not.toBeNull();
      expect(theme!.surfaceDeep).toBeDefined();
    });

    it("returns null for unknown preset", () => {
      expect(resolveStudioTheme("nonexistent" as FwThemePreset)).toBeNull();
    });

    it("returns complete overrides with all 14 tokens for known themes", () => {
      const tokenKeys: (keyof StudioThemeOverrides)[] = [
        "surfaceDeep",
        "surface",
        "surfaceRaised",
        "text",
        "textMuted",
        "textFaint",
        "border",
        "accent",
        "accentSecondary",
        "success",
        "danger",
        "warning",
        "info",
        "live",
      ];

      const theme = resolveStudioTheme("nord");
      expect(theme).not.toBeNull();
      for (const key of tokenKeys) {
        expect(theme![key]).toBeDefined();
      }
    });
  });

  // ===========================================================================
  // getAvailableStudioThemes
  // ===========================================================================
  describe("getAvailableStudioThemes", () => {
    it("includes default theme", () => {
      const themes = getAvailableStudioThemes();
      expect(themes).toContain("default");
    });

    it("includes known presets", () => {
      const themes = getAvailableStudioThemes();
      expect(themes).toContain("tokyo-night");
      expect(themes).toContain("dracula");
      expect(themes).toContain("nord");
      expect(themes).toContain("catppuccin");
      expect(themes).toContain("gruvbox");
      expect(themes).toContain("solarized");
    });

    it("returns at least 10 themes", () => {
      expect(getAvailableStudioThemes().length).toBeGreaterThanOrEqual(10);
    });
  });

  // ===========================================================================
  // applyStudioTheme
  // ===========================================================================
  describe("applyStudioTheme", () => {
    it("sets data-theme attribute for themed presets", () => {
      const el = createMockElement();
      applyStudioTheme(el, "dracula");
      expect(el.setAttribute).toHaveBeenCalledWith("data-theme", "dracula");
    });

    it("sets CSS custom properties for preset", () => {
      const el = createMockElement();
      applyStudioTheme(el, "nord");
      expect(el.style.setProperty).toHaveBeenCalledWith("--fw-sc-surface", expect.any(String));
      expect(el.style.setProperty).toHaveBeenCalledWith("--fw-sc-accent", expect.any(String));
    });

    it("removes data-theme for default preset", () => {
      const el = createMockElement();
      applyStudioTheme(el, "default");
      expect(el.removeAttribute).toHaveBeenCalledWith("data-theme");
    });

    it("clears previous CSS properties before applying new theme", () => {
      const el = createMockElement();
      applyStudioTheme(el, "dracula");
      // removeProperty should be called for all SC props before setting new ones
      expect(el.style.removeProperty).toHaveBeenCalledWith("--fw-sc-surface");
    });
  });

  // ===========================================================================
  // applyStudioThemeOverrides
  // ===========================================================================
  describe("applyStudioThemeOverrides", () => {
    it("sets CSS custom properties for provided overrides", () => {
      const el = createMockElement();
      applyStudioThemeOverrides(el, {
        accent: "200 80% 60%",
        danger: "0 100% 50%",
      });
      expect(el.style.setProperty).toHaveBeenCalledWith("--fw-sc-accent", "200 80% 60%");
      expect(el.style.setProperty).toHaveBeenCalledWith("--fw-sc-danger", "0 100% 50%");
    });

    it("only sets properties that are defined in overrides", () => {
      const el = createMockElement();
      applyStudioThemeOverrides(el, { accent: "200 80% 60%" });
      // Should not set surface since it wasn't in overrides
      const setCalls = (el.style.setProperty as any).mock.calls;
      const setProps = setCalls.map((c: string[]) => c[0]);
      expect(setProps).toContain("--fw-sc-accent");
      expect(setProps).not.toContain("--fw-sc-surface");
    });
  });

  // ===========================================================================
  // clearStudioTheme
  // ===========================================================================
  describe("clearStudioTheme", () => {
    it("removes data-theme attribute", () => {
      const el = createMockElement();
      clearStudioTheme(el);
      expect(el.removeAttribute).toHaveBeenCalledWith("data-theme");
    });

    it("removes all --fw-sc-* CSS properties", () => {
      const el = createMockElement();
      clearStudioTheme(el);
      expect(el.style.removeProperty).toHaveBeenCalledWith("--fw-sc-surface");
      expect(el.style.removeProperty).toHaveBeenCalledWith("--fw-sc-accent");
      expect(el.style.removeProperty).toHaveBeenCalledWith("--fw-sc-text");
      expect(el.style.removeProperty).toHaveBeenCalledWith("--fw-sc-danger");
    });
  });

  // ===========================================================================
  // studioThemeOverridesToStyle
  // ===========================================================================
  describe("studioThemeOverridesToStyle", () => {
    it("converts overrides to CSS property object", () => {
      const style = studioThemeOverridesToStyle({
        accent: "200 80% 60%",
        surface: "220 10% 15%",
      });
      expect(style["--fw-sc-accent"]).toBe("200 80% 60%");
      expect(style["--fw-sc-surface"]).toBe("220 10% 15%");
    });

    it("returns empty object for empty overrides", () => {
      const style = studioThemeOverridesToStyle({});
      expect(Object.keys(style)).toHaveLength(0);
    });
  });
});

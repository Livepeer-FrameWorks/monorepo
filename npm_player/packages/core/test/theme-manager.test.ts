// @vitest-environment jsdom
import { describe, it, expect, beforeEach } from "vitest";
import {
  resolveTheme,
  getThemeDisplayName,
  getAvailableThemes,
  themeOverridesToStyle,
  applyTheme,
  applyThemeOverrides,
  clearTheme,
  type FwThemeOverrides,
} from "../src/core/ThemeManager";

describe("resolveTheme", () => {
  it("returns overrides for a JS-defined preset", () => {
    const t = resolveTheme("tokyo-night");
    expect(t).not.toBeNull();
    expect(typeof t!.accent).toBe("string");
  });

  it("returns null for CSS-only presets (default/light/neutral-dark)", () => {
    // These ship their tokens via the stylesheet, not inline overrides — the
    // null is the signal callers use to skip inline custom-property writes.
    expect(resolveTheme("default")).toBeNull();
    expect(resolveTheme("light")).toBeNull();
    expect(resolveTheme("neutral-dark")).toBeNull();
  });

  it("returns null for an unknown preset", () => {
    expect(resolveTheme("does-not-exist" as never)).toBeNull();
  });
});

describe("getThemeDisplayName", () => {
  it("maps preset ids to human labels", () => {
    expect(getThemeDisplayName("tokyo-night")).toBe("Tokyo Night");
    expect(getThemeDisplayName("rose-pine")).toBe("Rosé Pine");
  });

  it("echoes an unknown preset id", () => {
    expect(getThemeDisplayName("mystery" as never)).toBe("mystery");
  });
});

describe("getAvailableThemes", () => {
  it("leads with the three CSS-only presets then the JS presets", () => {
    const themes = getAvailableThemes();
    expect(themes.slice(0, 3)).toEqual(["default", "light", "neutral-dark"]);
    expect(themes).toContain("tokyo-night");
    expect(themes).toContain("ayu-mirage");
    // 3 CSS-only + 14 JS-defined presets.
    expect(themes).toHaveLength(17);
  });
});

describe("themeOverridesToStyle", () => {
  it("emits only the defined keys, mapped to --fw-* custom properties", () => {
    const overrides: FwThemeOverrides = { accent: "1 2% 3%", radius: "8px" };
    expect(themeOverridesToStyle(overrides)).toEqual({
      "--fw-accent": "1 2% 3%",
      "--fw-radius": "8px",
    });
  });

  it("omits undefined keys entirely", () => {
    const style = themeOverridesToStyle({ text: "0 0% 100%" });
    expect(Object.keys(style)).toEqual(["--fw-text"]);
  });
});

describe("DOM application (jsdom)", () => {
  let root: HTMLElement;
  beforeEach(() => {
    root = document.createElement("div");
  });

  it("applyTheme('default') removes data-theme and clears inline props", () => {
    root.setAttribute("data-theme", "tokyo-night");
    root.style.setProperty("--fw-accent", "1 2% 3%");
    applyTheme(root, "default");
    expect(root.hasAttribute("data-theme")).toBe(false);
    expect(root.style.getPropertyValue("--fw-accent")).toBe("");
  });

  it("applyTheme(CSS-only) sets data-theme but writes no inline tokens", () => {
    applyTheme(root, "light");
    expect(root.getAttribute("data-theme")).toBe("light");
    expect(root.style.getPropertyValue("--fw-accent")).toBe("");
  });

  it("applyTheme(JS preset) sets data-theme AND inline custom properties", () => {
    applyTheme(root, "tokyo-night");
    expect(root.getAttribute("data-theme")).toBe("tokyo-night");
    expect(root.style.getPropertyValue("--fw-accent").length).toBeGreaterThan(0);
  });

  it("applyTheme replaces the previous preset's inline props", () => {
    applyThemeOverrides(root, { accent: "9 9% 9%", danger: "1 1% 1%" });
    expect(root.style.getPropertyValue("--fw-danger")).toBe("1 1% 1%");
    // Switching to a preset that does not define `danger` must clear the stale value.
    applyTheme(root, "tokyo-night");
    expect(root.style.getPropertyValue("--fw-accent")).not.toBe("9 9% 9%");
  });

  it("applyThemeOverrides writes each defined token; clearTheme round-trips", () => {
    applyThemeOverrides(root, { accent: "1 2% 3%", radius: "4px" });
    expect(root.style.getPropertyValue("--fw-accent")).toBe("1 2% 3%");
    expect(root.style.getPropertyValue("--fw-radius")).toBe("4px");

    root.setAttribute("data-theme", "x");
    clearTheme(root);
    expect(root.hasAttribute("data-theme")).toBe(false);
    expect(root.style.getPropertyValue("--fw-accent")).toBe("");
    expect(root.style.getPropertyValue("--fw-radius")).toBe("");
  });
});

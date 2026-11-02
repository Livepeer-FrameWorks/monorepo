// @vitest-environment jsdom
import { readFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { afterEach, describe, expect, it } from "vitest";
import { readPlayerThemeTokens } from "../src/theme";

const themesDir = path.resolve(
  path.dirname(fileURLToPath(import.meta.url)),
  "../../core/src/styles/themes"
);
const themeCss = (name: string) => readFileSync(path.join(themesDir, `${name}.css`), "utf8");

describe("readPlayerThemeTokens", () => {
  afterEach(() => {
    document.head.innerHTML = "";
    document.body.innerHTML = "";
  });

  it("resolves the CSS-only light and neutral-dark themes from their stylesheets", () => {
    const style = document.createElement("style");
    style.textContent = themeCss("light") + themeCss("neutral-dark");
    document.head.appendChild(style);

    const light = readPlayerThemeTokens("light");
    expect(light.values["--fw-surface-deep"]).toBe("0 0% 97%");
    expect(light.values["--fw-accent"]).toBe("218 80% 50%");
    expect(light.light).toBe(true);

    const neutral = readPlayerThemeTokens("neutral-dark");
    expect(neutral.values["--fw-surface-deep"]).toBeTruthy();
    expect(neutral.light).toBe(false);
    expect(document.querySelector(".fw-player-surface")).toBeNull();
  });

  it("resolves a JS preset from its inline tokens", () => {
    const night = readPlayerThemeTokens("tokyo-night");
    expect(night.values["--fw-surface-deep"]).toBe("229 26% 16%");
    expect(night.values["--fw-accent"]).toBe("221 89% 72%");
    expect(night.light).toBe(false);
  });
});

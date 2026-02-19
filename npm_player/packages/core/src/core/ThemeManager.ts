/**
 * Theme Manager — apply presets and custom overrides to a player root element.
 *
 * All color tokens use bare HSL triplets ("210 100% 50%") consumed via
 * hsl(var(--fw-accent) / alpha) in the stylesheet.
 */

export type FwThemePreset =
  | "default"
  | "light"
  | "neutral-dark"
  | "tokyo-night"
  | "tokyo-night-light"
  | "dracula"
  | "nord"
  | "catppuccin"
  | "catppuccin-light"
  | "gruvbox"
  | "gruvbox-light"
  | "one-dark"
  | "github-dark"
  | "rose-pine"
  | "solarized"
  | "solarized-light"
  | "ayu-mirage";

export interface FwThemeOverrides {
  accent?: string;
  accentSecondary?: string;
  surfaceDeep?: string;
  surface?: string;
  surfaceRaised?: string;
  surfaceActive?: string;
  text?: string;
  textBright?: string;
  textMuted?: string;
  textFaint?: string;
  success?: string;
  danger?: string;
  warning?: string;
  info?: string;
  live?: string;
  shadowColor?: string;
  onAccent?: string;
  onLive?: string;
  radius?: string;
}

const OVERRIDE_TO_PROP: Record<keyof FwThemeOverrides, string> = {
  accent: "--fw-accent",
  accentSecondary: "--fw-accent-secondary",
  surfaceDeep: "--fw-surface-deep",
  surface: "--fw-surface",
  surfaceRaised: "--fw-surface-raised",
  surfaceActive: "--fw-surface-active",
  text: "--fw-text",
  textBright: "--fw-text-bright",
  textMuted: "--fw-text-muted",
  textFaint: "--fw-text-faint",
  success: "--fw-success",
  danger: "--fw-danger",
  warning: "--fw-warning",
  info: "--fw-info",
  live: "--fw-live",
  shadowColor: "--fw-shadow-color",
  onAccent: "--fw-on-accent",
  onLive: "--fw-on-live",
  radius: "--fw-radius",
};

const ALL_FW_PROPS = Object.values(OVERRIDE_TO_PROP);

// CSS-only themes (defined in separate CSS files via data-theme selectors).
// These don't need JS preset data — the CSS handles everything.
const CSS_ONLY_THEMES = new Set<string>(["default", "light", "neutral-dark"]);

/**
 * All theme presets with full FwThemeOverrides.
 * HSL values converted from upstream MistPlayer hex palette.
 * CSS-only themes (default, light, neutral-dark) are not included here —
 * they use data-theme CSS selectors instead.
 */
const THEME_PRESETS: Record<string, FwThemeOverrides> = {
  "tokyo-night": {
    surfaceDeep: "229 26% 16%",
    surface: "235 19% 13%",
    surfaceRaised: "232 22% 20%",
    surfaceActive: "232 22% 25%",
    text: "229 73% 86%",
    textBright: "220 13% 91%",
    textMuted: "224 16% 53%",
    textFaint: "228 15% 45%",
    accent: "221 89% 72%",
    accentSecondary: "221 89% 79%",
    success: "95 53% 55%",
    danger: "348 74% 64%",
    warning: "35 79% 64%",
    info: "197 95% 74%",
    live: "348 80% 48%",
    shadowColor: "235 30% 5%",
    onAccent: "235 19% 13%",
    onLive: "0 0% 100%",
    radius: "0",
  },
  "tokyo-night-light": {
    surfaceDeep: "230 8% 85%",
    surface: "230 11% 89%",
    surfaceRaised: "230 11% 93%",
    surfaceActive: "230 11% 97%",
    text: "229 24% 31%",
    textBright: "229 24% 20%",
    textMuted: "225 19% 38%",
    textFaint: "228 3% 72%",
    accent: "215 81% 55%",
    accentSecondary: "214 81% 64%",
    success: "140 60% 35%",
    danger: "0 70% 50%",
    warning: "35 80% 45%",
    info: "197 80% 55%",
    live: "0 80% 48%",
    shadowColor: "230 11% 50%",
    onAccent: "0 0% 100%",
    onLive: "0 0% 100%",
    radius: "0",
  },
  dracula: {
    surfaceDeep: "232 14% 31%",
    surface: "231 15% 18%",
    surfaceRaised: "231 15% 24%",
    surfaceActive: "231 15% 30%",
    text: "60 30% 96%",
    textBright: "60 30% 99%",
    textMuted: "225 27% 51%",
    textFaint: "225 27% 40%",
    accent: "265 89% 78%",
    accentSecondary: "326 100% 74%",
    success: "135 94% 65%",
    danger: "0 100% 67%",
    warning: "65 92% 76%",
    info: "191 97% 77%",
    live: "348 80% 48%",
    shadowColor: "231 20% 5%",
    onAccent: "231 15% 18%",
    onLive: "0 0% 100%",
    radius: "0",
  },
  nord: {
    surfaceDeep: "222 16% 28%",
    surface: "220 16% 22%",
    surfaceRaised: "220 16% 26%",
    surfaceActive: "220 16% 36%",
    text: "218 27% 94%",
    textBright: "218 27% 98%",
    textMuted: "219 28% 88%",
    textFaint: "220 16% 36%",
    accent: "193 43% 67%",
    accentSecondary: "179 25% 65%",
    success: "92 28% 65%",
    danger: "354 42% 56%",
    warning: "40 71% 73%",
    info: "193 43% 67%",
    live: "348 80% 48%",
    shadowColor: "220 20% 5%",
    onAccent: "220 16% 22%",
    onLive: "0 0% 100%",
    radius: "0",
  },
  catppuccin: {
    surfaceDeep: "237 16% 23%",
    surface: "240 21% 15%",
    surfaceRaised: "240 21% 20%",
    surfaceActive: "233 12% 39%",
    text: "226 64% 88%",
    textBright: "226 64% 95%",
    textMuted: "228 24% 72%",
    textFaint: "233 12% 39%",
    accent: "267 84% 81%",
    accentSecondary: "316 72% 86%",
    success: "105 48% 72%",
    danger: "347 87% 68%",
    warning: "22 99% 52%",
    info: "189 71% 73%",
    live: "348 80% 48%",
    shadowColor: "240 25% 5%",
    onAccent: "240 21% 15%",
    onLive: "0 0% 100%",
    radius: "0",
  },
  "catppuccin-light": {
    surfaceDeep: "223 16% 83%",
    surface: "220 23% 95%",
    surfaceRaised: "220 23% 98%",
    surfaceActive: "220 23% 100%",
    text: "234 16% 35%",
    textBright: "234 16% 20%",
    textMuted: "233 13% 41%",
    textFaint: "228 11% 65%",
    accent: "266 85% 58%",
    accentSecondary: "0 60% 67%",
    success: "109 58% 40%",
    danger: "347 87% 44%",
    warning: "22 99% 42%",
    info: "189 71% 55%",
    live: "0 80% 48%",
    shadowColor: "223 16% 50%",
    onAccent: "0 0% 100%",
    onLive: "0 0% 100%",
    radius: "0",
  },
  gruvbox: {
    surfaceDeep: "20 5% 22%",
    surface: "0 0% 16%",
    surfaceRaised: "0 0% 20%",
    surfaceActive: "0 0% 28%",
    text: "43 59% 81%",
    textBright: "43 59% 92%",
    textMuted: "30 12% 51%",
    textFaint: "30 12% 40%",
    accent: "40 73% 49%",
    accentSecondary: "42 95% 58%",
    success: "60 71% 35%",
    danger: "6 96% 59%",
    warning: "27 99% 55%",
    info: "175 60% 53%",
    live: "348 80% 48%",
    shadowColor: "0 0% 5%",
    onAccent: "0 0% 16%",
    onLive: "0 0% 100%",
    radius: "0",
  },
  "gruvbox-light": {
    surfaceDeep: "43 59% 81%",
    surface: "48 87% 88%",
    surfaceRaised: "48 87% 92%",
    surfaceActive: "48 87% 96%",
    text: "20 5% 22%",
    textBright: "20 5% 12%",
    textMuted: "22 7% 29%",
    textFaint: "35 17% 59%",
    accent: "40 73% 49%",
    accentSecondary: "37 80% 39%",
    success: "60 71% 35%",
    danger: "6 96% 45%",
    warning: "27 99% 42%",
    info: "175 60% 40%",
    live: "0 80% 48%",
    shadowColor: "40 30% 40%",
    onAccent: "48 87% 88%",
    onLive: "0 0% 100%",
    radius: "0",
  },
  "one-dark": {
    surfaceDeep: "219 14% 20%",
    surface: "220 13% 18%",
    surfaceRaised: "220 13% 22%",
    surfaceActive: "221 13% 28%",
    text: "219 14% 71%",
    textBright: "219 14% 88%",
    textMuted: "219 10% 53%",
    textFaint: "221 13% 28%",
    accent: "207 82% 66%",
    accentSecondary: "220 100% 66%",
    success: "95 38% 62%",
    danger: "355 65% 65%",
    warning: "39 67% 69%",
    info: "187 47% 55%",
    live: "348 80% 48%",
    shadowColor: "220 15% 5%",
    onAccent: "220 13% 18%",
    onLive: "0 0% 100%",
    radius: "0",
  },
  "github-dark": {
    surfaceDeep: "215 21% 11%",
    surface: "216 28% 7%",
    surfaceRaised: "215 21% 14%",
    surfaceActive: "212 12% 21%",
    text: "208 35% 93%",
    textBright: "208 35% 98%",
    textMuted: "210 18% 65%",
    textFaint: "212 12% 21%",
    accent: "212 100% 67%",
    accentSecondary: "208 100% 74%",
    success: "137 66% 43%",
    danger: "0 78% 63%",
    warning: "39 100% 67%",
    info: "212 100% 67%",
    live: "348 80% 48%",
    shadowColor: "216 30% 3%",
    onAccent: "216 28% 7%",
    onLive: "0 0% 100%",
    radius: "0",
  },
  "rose-pine": {
    surfaceDeep: "247 23% 15%",
    surface: "249 22% 12%",
    surfaceRaised: "248 22% 18%",
    surfaceActive: "249 12% 47%",
    text: "245 50% 91%",
    textBright: "245 50% 97%",
    textMuted: "249 12% 47%",
    textFaint: "249 15% 35%",
    accent: "267 57% 78%",
    accentSecondary: "2 55% 83%",
    success: "197 49% 38%",
    danger: "343 76% 68%",
    warning: "20 69% 74%",
    info: "189 43% 73%",
    live: "348 80% 48%",
    shadowColor: "249 25% 5%",
    onAccent: "249 22% 12%",
    onLive: "0 0% 100%",
    radius: "0",
  },
  solarized: {
    surfaceDeep: "192 81% 14%",
    surface: "192 100% 11%",
    surfaceRaised: "192 81% 17%",
    surfaceActive: "194 14% 40%",
    text: "186 8% 55%",
    textBright: "180 7% 60%",
    textMuted: "194 14% 40%",
    textFaint: "194 14% 30%",
    accent: "175 59% 40%",
    accentSecondary: "205 69% 49%",
    success: "68 100% 30%",
    danger: "1 71% 52%",
    warning: "18 89% 67%",
    info: "175 59% 40%",
    live: "348 80% 48%",
    shadowColor: "192 100% 3%",
    onAccent: "192 100% 11%",
    onLive: "0 0% 100%",
    radius: "0",
  },
  "solarized-light": {
    surfaceDeep: "46 42% 88%",
    surface: "44 87% 94%",
    surfaceRaised: "44 87% 97%",
    surfaceActive: "44 87% 99%",
    text: "196 13% 45%",
    textBright: "194 14% 40%",
    textMuted: "194 14% 40%",
    textFaint: "180 7% 60%",
    accent: "175 59% 40%",
    accentSecondary: "205 69% 49%",
    success: "68 100% 30%",
    danger: "1 71% 52%",
    warning: "18 89% 50%",
    info: "175 59% 40%",
    live: "0 80% 48%",
    shadowColor: "44 40% 50%",
    onAccent: "44 87% 94%",
    onLive: "0 0% 100%",
    radius: "0",
  },
  "ayu-mirage": {
    surfaceDeep: "222 22% 15%",
    surface: "227 20% 18%",
    surfaceRaised: "227 20% 22%",
    surfaceActive: "228 13% 39%",
    text: "48 9% 78%",
    textBright: "48 9% 90%",
    textMuted: "228 13% 39%",
    textFaint: "228 13% 30%",
    accent: "28 100% 70%",
    accentSecondary: "27 85% 65%",
    success: "95 53% 55%",
    danger: "355 75% 65%",
    warning: "45 100% 70%",
    info: "200 80% 70%",
    live: "348 80% 48%",
    shadowColor: "222 25% 5%",
    onAccent: "227 20% 18%",
    onLive: "0 0% 100%",
    radius: "0",
  },
};

/** Resolve a preset name to its FwThemeOverrides, or null if CSS-only / unknown. */
export function resolveTheme(preset: FwThemePreset): FwThemeOverrides | null {
  if (CSS_ONLY_THEMES.has(preset)) return null;
  return THEME_PRESETS[preset] ?? null;
}

const THEME_DISPLAY_NAMES: Record<FwThemePreset, string> = {
  default: "Default",
  light: "Light",
  "neutral-dark": "Neutral Dark",
  "tokyo-night": "Tokyo Night",
  "tokyo-night-light": "Tokyo Night Light",
  dracula: "Dracula",
  nord: "Nord",
  catppuccin: "Catppuccin",
  "catppuccin-light": "Catppuccin Light",
  gruvbox: "Gruvbox",
  "gruvbox-light": "Gruvbox Light",
  "one-dark": "One Dark",
  "github-dark": "GitHub Dark",
  "rose-pine": "Rosé Pine",
  solarized: "Solarized",
  "solarized-light": "Solarized Light",
  "ayu-mirage": "Ayu Mirage",
};

/** Get the human-readable display name for a theme preset. */
export function getThemeDisplayName(preset: FwThemePreset): string {
  return THEME_DISPLAY_NAMES[preset] ?? preset;
}

/** Get all available theme preset names. */
export function getAvailableThemes(): FwThemePreset[] {
  return ["default", "light", "neutral-dark", ...(Object.keys(THEME_PRESETS) as FwThemePreset[])];
}

/**
 * Apply a preset theme. CSS-only themes use `data-theme`; JS presets apply
 * overrides as inline custom properties and also set `data-theme` for styling hooks.
 */
export function applyTheme(root: HTMLElement, preset: FwThemePreset): void {
  // Clear previous inline theme properties
  for (const prop of ALL_FW_PROPS) {
    root.style.removeProperty(prop);
  }

  if (preset === "default") {
    root.removeAttribute("data-theme");
    return;
  }

  const overrides = resolveTheme(preset);
  if (overrides) {
    // JS-defined preset: apply as inline styles + data-theme for styling hooks
    root.setAttribute("data-theme", preset);
    for (const [key, prop] of Object.entries(OVERRIDE_TO_PROP)) {
      const value = overrides[key as keyof FwThemeOverrides];
      if (value !== undefined) {
        root.style.setProperty(prop, value);
      }
    }
  } else {
    // CSS-only preset (light, neutral-dark): just set data-theme, CSS handles the rest
    root.setAttribute("data-theme", preset);
  }
}

/** Set individual CSS custom properties on the root element. */
export function applyThemeOverrides(root: HTMLElement, overrides: FwThemeOverrides): void {
  for (const [key, prop] of Object.entries(OVERRIDE_TO_PROP)) {
    const value = overrides[key as keyof FwThemeOverrides];
    if (value !== undefined) {
      root.style.setProperty(prop, value);
    }
  }
}

/** Remove data-theme and all inline --fw-* custom properties. */
export function clearTheme(root: HTMLElement): void {
  root.removeAttribute("data-theme");
  for (const prop of ALL_FW_PROPS) {
    root.style.removeProperty(prop);
  }
}

/** Convert FwThemeOverrides into a plain style object (for React/Svelte). */
export function themeOverridesToStyle(overrides: FwThemeOverrides): Record<string, string> {
  const style: Record<string, string> = {};
  for (const [key, prop] of Object.entries(OVERRIDE_TO_PROP)) {
    const value = overrides[key as keyof FwThemeOverrides];
    if (value !== undefined) {
      style[prop] = value;
    }
  }
  return style;
}

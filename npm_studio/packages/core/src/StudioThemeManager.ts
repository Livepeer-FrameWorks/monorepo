/**
 * StudioThemeManager — Theme presets for StreamCrafter.
 *
 * Maps the same 16+ presets used by the player to studio-specific
 * `--fw-sc-*` CSS tokens. The CSS file (streamcrafter.css) consumes these
 * via `hsl(var(--fw-sc-surface) / alpha)`.
 *
 * The studio uses a more complex token set than the player due to its
 * richer UI (VU meters, compositor, multi-source panels, settings flyouts).
 */

// Re-export the preset type so consumers don't need player-core
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

/** Studio-specific theme overrides (maps to --fw-sc-* CSS custom properties). */
export interface StudioThemeOverrides {
  surfaceDeep?: string;
  surface?: string;
  surfaceRaised?: string;
  surfaceActive?: string;
  text?: string;
  textBright?: string;
  textMuted?: string;
  textFaint?: string;
  border?: string;
  accent?: string;
  accentSecondary?: string;
  success?: string;
  danger?: string;
  warning?: string;
  info?: string;
  live?: string;
  shadowColor?: string;
  onAccent?: string;
}

const OVERRIDE_TO_PROP: Record<keyof StudioThemeOverrides, string> = {
  surfaceDeep: "--fw-sc-surface-deep",
  surface: "--fw-sc-surface",
  surfaceRaised: "--fw-sc-surface-raised",
  surfaceActive: "--fw-sc-surface-active",
  text: "--fw-sc-text",
  textBright: "--fw-sc-text-bright",
  textMuted: "--fw-sc-text-muted",
  textFaint: "--fw-sc-text-faint",
  border: "--fw-sc-border",
  accent: "--fw-sc-accent",
  accentSecondary: "--fw-sc-accent-secondary",
  success: "--fw-sc-success",
  danger: "--fw-sc-danger",
  warning: "--fw-sc-warning",
  info: "--fw-sc-info",
  live: "--fw-sc-live",
  shadowColor: "--fw-sc-shadow-color",
  onAccent: "--fw-sc-on-accent",
};

const ALL_SC_PROPS = Object.values(OVERRIDE_TO_PROP);

/**
 * Theme presets derived from the same palettes as the player.
 * Each maps to the 14 studio semantic tokens.
 */
const STUDIO_PRESETS: Record<string, StudioThemeOverrides> = {
  "tokyo-night": {
    surfaceDeep: "240 15% 10%",
    surface: "235 19% 13%",
    surfaceRaised: "229 24% 19%",
    surfaceActive: "232 27% 25%",
    text: "229 73% 86%",
    textBright: "220 13% 91%",
    textMuted: "229 35% 75%",
    textFaint: "229 23% 44%",
    border: "229 24% 31%",
    accent: "225 86% 70%",
    accentSecondary: "264 85% 74%",
    success: "89 51% 61%",
    danger: "349 89% 72%",
    warning: "36 66% 64%",
    info: "197 95% 74%",
    live: "89 51% 61%",
    shadowColor: "235 30% 5%",
    onAccent: "235 19% 13%",
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
    border: "228 11% 78%",
    accent: "215 81% 55%",
    accentSecondary: "264 85% 58%",
    success: "140 60% 35%",
    danger: "0 70% 50%",
    warning: "35 80% 45%",
    info: "197 80% 45%",
    live: "140 60% 35%",
    shadowColor: "230 11% 50%",
    onAccent: "0 0% 100%",
  },
  dracula: {
    surfaceDeep: "231 15% 14%",
    surface: "231 15% 18%",
    surfaceRaised: "232 14% 31%",
    surfaceActive: "231 15% 30%",
    text: "60 30% 96%",
    textBright: "60 30% 99%",
    textMuted: "225 27% 51%",
    textFaint: "225 27% 40%",
    border: "225 27% 35%",
    accent: "265 89% 78%",
    accentSecondary: "326 100% 74%",
    success: "135 94% 65%",
    danger: "0 100% 67%",
    warning: "65 92% 76%",
    info: "191 97% 77%",
    live: "135 94% 65%",
    shadowColor: "231 20% 5%",
    onAccent: "231 15% 18%",
  },
  nord: {
    surfaceDeep: "220 16% 18%",
    surface: "220 16% 22%",
    surfaceRaised: "222 16% 28%",
    surfaceActive: "220 16% 36%",
    text: "218 27% 94%",
    textBright: "218 27% 98%",
    textMuted: "219 28% 88%",
    textFaint: "220 16% 36%",
    border: "220 16% 32%",
    accent: "193 43% 67%",
    accentSecondary: "179 25% 65%",
    success: "92 28% 65%",
    danger: "354 42% 56%",
    warning: "40 71% 73%",
    info: "193 43% 67%",
    live: "92 28% 65%",
    shadowColor: "220 20% 5%",
    onAccent: "220 16% 22%",
  },
  catppuccin: {
    surfaceDeep: "240 21% 12%",
    surface: "240 21% 15%",
    surfaceRaised: "237 16% 23%",
    surfaceActive: "233 12% 39%",
    text: "226 64% 88%",
    textBright: "226 64% 95%",
    textMuted: "228 24% 72%",
    textFaint: "233 12% 39%",
    border: "233 12% 32%",
    accent: "267 84% 81%",
    accentSecondary: "316 72% 86%",
    success: "105 48% 72%",
    danger: "347 87% 68%",
    warning: "22 99% 52%",
    info: "189 71% 73%",
    live: "105 48% 72%",
    shadowColor: "240 25% 5%",
    onAccent: "240 21% 15%",
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
    border: "228 11% 75%",
    accent: "266 85% 58%",
    accentSecondary: "0 60% 67%",
    success: "109 58% 40%",
    danger: "347 87% 44%",
    warning: "22 99% 42%",
    info: "183 74% 35%",
    live: "109 58% 40%",
    shadowColor: "223 16% 50%",
    onAccent: "0 0% 100%",
  },
  gruvbox: {
    surfaceDeep: "0 0% 12%",
    surface: "0 0% 16%",
    surfaceRaised: "20 5% 22%",
    surfaceActive: "0 0% 28%",
    text: "43 59% 81%",
    textBright: "43 59% 92%",
    textMuted: "30 12% 51%",
    textFaint: "30 12% 40%",
    border: "30 12% 30%",
    accent: "40 73% 49%",
    accentSecondary: "42 95% 58%",
    success: "60 71% 35%",
    danger: "6 96% 59%",
    warning: "27 99% 55%",
    info: "175 42% 40%",
    live: "60 71% 35%",
    shadowColor: "0 0% 5%",
    onAccent: "0 0% 16%",
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
    border: "35 17% 70%",
    accent: "40 73% 49%",
    accentSecondary: "37 80% 39%",
    success: "60 71% 35%",
    danger: "6 96% 45%",
    warning: "27 99% 42%",
    info: "175 42% 35%",
    live: "60 71% 35%",
    shadowColor: "40 30% 40%",
    onAccent: "48 87% 88%",
  },
  "one-dark": {
    surfaceDeep: "220 13% 15%",
    surface: "220 13% 18%",
    surfaceRaised: "219 14% 20%",
    surfaceActive: "221 13% 28%",
    text: "219 14% 71%",
    textBright: "219 14% 88%",
    textMuted: "219 10% 53%",
    textFaint: "221 13% 28%",
    border: "221 13% 24%",
    accent: "207 82% 66%",
    accentSecondary: "220 100% 66%",
    success: "95 38% 62%",
    danger: "355 65% 65%",
    warning: "39 67% 69%",
    info: "187 47% 55%",
    live: "95 38% 62%",
    shadowColor: "220 15% 5%",
    onAccent: "220 13% 18%",
  },
  "github-dark": {
    surfaceDeep: "216 28% 5%",
    surface: "216 28% 7%",
    surfaceRaised: "215 21% 14%",
    surfaceActive: "212 12% 21%",
    text: "208 35% 93%",
    textBright: "208 35% 98%",
    textMuted: "210 18% 65%",
    textFaint: "212 12% 21%",
    border: "212 12% 18%",
    accent: "212 100% 67%",
    accentSecondary: "208 100% 74%",
    success: "137 66% 43%",
    danger: "0 78% 63%",
    warning: "39 100% 67%",
    info: "192 72% 50%",
    live: "137 66% 43%",
    shadowColor: "216 30% 3%",
    onAccent: "216 28% 7%",
  },
  "rose-pine": {
    surfaceDeep: "249 22% 10%",
    surface: "249 22% 12%",
    surfaceRaised: "247 23% 15%",
    surfaceActive: "249 12% 47%",
    text: "245 50% 91%",
    textBright: "245 50% 97%",
    textMuted: "249 12% 47%",
    textFaint: "249 15% 35%",
    border: "249 15% 28%",
    accent: "267 57% 78%",
    accentSecondary: "2 55% 83%",
    success: "197 49% 38%",
    danger: "343 76% 68%",
    warning: "20 69% 74%",
    info: "189 43% 73%",
    live: "197 49% 38%",
    shadowColor: "249 25% 5%",
    onAccent: "249 22% 12%",
  },
  solarized: {
    surfaceDeep: "192 100% 8%",
    surface: "192 100% 11%",
    surfaceRaised: "192 81% 14%",
    surfaceActive: "194 14% 40%",
    text: "186 8% 55%",
    textBright: "180 7% 60%",
    textMuted: "194 14% 40%",
    textFaint: "194 14% 30%",
    border: "194 14% 25%",
    accent: "175 59% 40%",
    accentSecondary: "205 69% 49%",
    success: "68 100% 30%",
    danger: "1 71% 52%",
    warning: "18 89% 67%",
    info: "175 59% 40%",
    live: "68 100% 30%",
    shadowColor: "192 100% 3%",
    onAccent: "192 100% 11%",
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
    border: "180 7% 72%",
    accent: "175 59% 40%",
    accentSecondary: "205 69% 49%",
    success: "68 100% 30%",
    danger: "1 71% 52%",
    warning: "18 89% 50%",
    info: "175 59% 40%",
    live: "68 100% 30%",
    shadowColor: "44 40% 50%",
    onAccent: "44 87% 94%",
  },
  "ayu-mirage": {
    surfaceDeep: "222 22% 12%",
    surface: "227 20% 18%",
    surfaceRaised: "222 22% 15%",
    surfaceActive: "228 13% 39%",
    text: "48 9% 78%",
    textBright: "48 9% 90%",
    textMuted: "228 13% 39%",
    textFaint: "228 13% 30%",
    border: "228 13% 25%",
    accent: "28 100% 70%",
    accentSecondary: "27 85% 65%",
    success: "95 53% 55%",
    danger: "355 75% 65%",
    warning: "45 100% 70%",
    info: "190 74% 56%",
    live: "95 53% 55%",
    shadowColor: "222 25% 5%",
    onAccent: "227 20% 18%",
  },
};

// CSS-only themes (handled by data-theme attribute in CSS, no JS overrides)
const CSS_ONLY_THEMES = new Set<string>(["default"]);

// ============================================================================
// Public API
// ============================================================================

/** Resolve a preset name to its StudioThemeOverrides, or null if CSS-only / unknown. */
export function resolveStudioTheme(preset: FwThemePreset): StudioThemeOverrides | null {
  if (CSS_ONLY_THEMES.has(preset)) return null;
  return STUDIO_PRESETS[preset] ?? null;
}

/** Get all available theme preset names. */
export function getAvailableStudioThemes(): FwThemePreset[] {
  return ["default", ...(Object.keys(STUDIO_PRESETS) as FwThemePreset[])];
}

/** Apply a preset theme to a studio root element. */
export function applyStudioTheme(root: HTMLElement, preset: FwThemePreset): void {
  for (const prop of ALL_SC_PROPS) {
    root.style.removeProperty(prop);
  }

  if (preset === "default") {
    root.removeAttribute("data-theme");
    return;
  }

  const overrides = resolveStudioTheme(preset);
  if (overrides) {
    root.setAttribute("data-theme", preset);
    for (const [key, prop] of Object.entries(OVERRIDE_TO_PROP)) {
      const value = overrides[key as keyof StudioThemeOverrides];
      if (value !== undefined) {
        root.style.setProperty(prop, value);
      }
    }
  } else {
    root.setAttribute("data-theme", preset);
  }
}

/** Apply custom overrides on top of a preset. */
export function applyStudioThemeOverrides(
  root: HTMLElement,
  overrides: StudioThemeOverrides
): void {
  for (const [key, prop] of Object.entries(OVERRIDE_TO_PROP)) {
    const value = overrides[key as keyof StudioThemeOverrides];
    if (value !== undefined) {
      root.style.setProperty(prop, value);
    }
  }
}

/** Remove all studio theme custom properties. */
export function clearStudioTheme(root: HTMLElement): void {
  root.removeAttribute("data-theme");
  for (const prop of ALL_SC_PROPS) {
    root.style.removeProperty(prop);
  }
}

/** Convert overrides to a plain style object (for React/Svelte inline styles). */
export function studioThemeOverridesToStyle(
  overrides: StudioThemeOverrides
): Record<string, string> {
  const style: Record<string, string> = {};
  for (const [key, prop] of Object.entries(OVERRIDE_TO_PROP)) {
    const value = overrides[key as keyof StudioThemeOverrides];
    if (value !== undefined) {
      style[prop] = value;
    }
  }
  return style;
}

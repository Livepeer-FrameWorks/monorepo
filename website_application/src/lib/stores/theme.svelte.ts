import { browser } from "$app/environment";
import {
  THEME_PALETTES,
  type ThemeId,
  type ThemeMode,
  type ThemePalette,
} from "$lib/themes/palettes";

// --- Storage ---

const STORAGE_KEY = "frameworks-theme-prefs";

interface ThemePreferences {
  themeId: ThemeId;
  mode: ThemeMode;
}

const DEFAULT_PREFS: ThemePreferences = {
  themeId: "tokyo-night",
  mode: "dark",
};

function loadFromStorage(): ThemePreferences {
  if (!browser) return DEFAULT_PREFS;

  try {
    const stored = localStorage.getItem(STORAGE_KEY);
    if (stored) {
      const parsed = JSON.parse(stored);
      const themeId =
        parsed.themeId && parsed.themeId in THEME_PALETTES ? parsed.themeId : DEFAULT_PREFS.themeId;
      const mode =
        parsed.mode === "dark" || parsed.mode === "light" ? parsed.mode : DEFAULT_PREFS.mode;
      return { themeId, mode };
    }
  } catch {
    // Ignore parse errors, use defaults
  }

  // System preference fallback for first visit
  if (window.matchMedia("(prefers-color-scheme: light)").matches) {
    return { ...DEFAULT_PREFS, mode: "light" };
  }
  return DEFAULT_PREFS;
}

function saveToStorage(prefs: ThemePreferences): void {
  if (!browser) return;

  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(prefs));
  } catch {
    // Ignore storage errors (quota exceeded, etc.)
  }
}

// --- Hex/Color conversion ---

function hexToHsl(hex: string): string {
  const r = parseInt(hex.slice(1, 3), 16) / 255;
  const g = parseInt(hex.slice(3, 5), 16) / 255;
  const b = parseInt(hex.slice(5, 7), 16) / 255;
  const max = Math.max(r, g, b);
  const min = Math.min(r, g, b);
  let h = 0;
  let s = 0;
  const l = (max + min) / 2;

  if (max !== min) {
    const d = max - min;
    s = l > 0.5 ? d / (2 - max - min) : d / (max + min);
    switch (max) {
      case r:
        h = ((g - b) / d + (g < b ? 6 : 0)) / 6;
        break;
      case g:
        h = ((b - r) / d + 2) / 6;
        break;
      case b:
        h = ((r - g) / d + 4) / 6;
        break;
    }
  }

  return `${Math.round(h * 360)} ${Math.round(s * 100)}% ${Math.round(l * 100)}%`;
}

function hexToRgb(hex: string): string {
  return `${parseInt(hex.slice(1, 3), 16)} ${parseInt(hex.slice(3, 5), 16)} ${parseInt(hex.slice(5, 7), 16)}`;
}

// --- DOM application ---

const VAR_MAP: Record<keyof ThemePalette, string> = {
  bg: "--tn-bg",
  bgDark: "--tn-bg-dark",
  bgHighlight: "--tn-bg-highlight",
  bgVisual: "--tn-bg-visual",
  fg: "--tn-fg",
  fgDark: "--tn-fg-dark",
  fgGutter: "--tn-fg-gutter",
  terminalBlack: "--tn-terminal-black",
  comment: "--tn-comment",
  terminal: "--tn-terminal",
  blue: "--tn-blue",
  cyan: "--tn-cyan",
  teal: "--tn-teal",
  green: "--tn-green",
  yellow: "--tn-yellow",
  orange: "--tn-orange",
  red: "--tn-red",
  magenta: "--tn-magenta",
  purple: "--tn-purple",
  accentSoft: "--brand-accent-soft",
};

function applyToDocument(id: ThemeId, mode: ThemeMode): void {
  if (!browser) return;

  const themeDef = THEME_PALETTES[id];
  if (!themeDef) return;

  const palette = themeDef[mode];
  if (!palette) return;

  const root = document.documentElement;

  // Set all --tn-* vars
  for (const [key, cssVar] of Object.entries(VAR_MAP)) {
    const hex = palette[key as keyof ThemePalette];
    if (hex) {
      root.style.setProperty(cssVar, hexToHsl(hex));
    }
  }

  // Derived tokens used by existing CSS
  root.style.setProperty("--accent-rgb", hexToRgb(palette.blue));

  // dark/light class for Tailwind dark: variants
  root.classList.toggle("dark", mode === "dark");
  root.classList.toggle("light", mode === "light");

  // Native form controls, scrollbars
  root.style.setProperty("color-scheme", mode);
}

// --- Store ---

function getModesForTheme(id: ThemeId): ThemeMode[] {
  const def = THEME_PALETTES[id];
  if (!def) return ["dark"];
  const modes: ThemeMode[] = [];
  if (def.dark) modes.push("dark");
  if (def.light) modes.push("light");
  return modes;
}

function createThemeStore() {
  let themeId = $state<ThemeId>(DEFAULT_PREFS.themeId);
  let mode = $state<ThemeMode>(DEFAULT_PREFS.mode);
  let initialized = $state(false);

  if (browser) {
    const prefs = loadFromStorage();
    themeId = prefs.themeId;
    // Validate mode is available for this theme
    const available = getModesForTheme(prefs.themeId);
    mode = available.includes(prefs.mode) ? prefs.mode : available[0];
    applyToDocument(themeId, mode);
    initialized = true;
  }

  function persist() {
    saveToStorage({ themeId, mode });
    applyToDocument(themeId, mode);
  }

  return {
    get themeId() {
      return themeId;
    },

    get mode() {
      return mode;
    },

    get initialized() {
      return initialized;
    },

    get isDark() {
      return mode === "dark";
    },

    get isLight() {
      return mode === "light";
    },

    get availableModes(): ThemeMode[] {
      return getModesForTheme(themeId);
    },

    get hasMultipleModes(): boolean {
      return getModesForTheme(themeId).length > 1;
    },

    get themeName(): string {
      return THEME_PALETTES[themeId]?.name ?? "Tokyo Night";
    },

    setTheme(id: ThemeId) {
      themeId = id;
      const available = getModesForTheme(id);
      if (!available.includes(mode)) {
        mode = available[0];
      }
      persist();
    },

    setMode(m: ThemeMode) {
      const available = getModesForTheme(themeId);
      if (!available.includes(m)) return;
      mode = m;
      persist();
    },

    toggleMode() {
      const available = getModesForTheme(themeId);
      if (available.length < 2) return;
      mode = mode === "dark" ? "light" : "dark";
      persist();
    },

    // Player preset name: dark themes use the ID directly, light variants append "-light"
    get playerTheme(): string {
      if (mode === "light") {
        // tokyo-night light → "tokyo-night-light", catppuccin light → "catppuccin-light", etc.
        return `${themeId}-light`;
      }
      return themeId;
    },

    get logoPath(): string {
      return mode === "dark"
        ? "/frameworks-dark-horizontal-lockup-transparent.svg"
        : "/frameworks-light-horizontal-lockup.svg";
    },

    get logoMarkPath(): string {
      return mode === "dark"
        ? "/frameworks-dark-logomark-transparent.svg"
        : "/frameworks-light-logomark.svg";
    },
  };
}

export const themeStore = createThemeStore();

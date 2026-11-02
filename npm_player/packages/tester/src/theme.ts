import { applyTheme, type FwThemePreset } from "@livepeer-frameworks/player-core";

/** Player tokens a host page can mirror to match the player theme. */
export const PLAYER_THEME_TOKENS = [
  "--fw-surface-deep",
  "--fw-surface",
  "--fw-surface-active",
  "--fw-text",
  "--fw-text-muted",
  "--fw-accent",
  "--fw-on-accent",
  "--fw-danger",
] as const;

export type PlayerThemeToken = (typeof PLAYER_THEME_TOKENS)[number];

export interface PlayerThemeTokens {
  /** Bare HSL triplets ("220 10% 94%") keyed by player token. */
  values: Partial<Record<PlayerThemeToken, string>>;
  /** Whether the preset's deepest surface is light. */
  light: boolean;
}

/**
 * Resolve a preset's tokens on a hidden player surface, so JS presets and the
 * CSS-only themes (light, neutral-dark) both come from the stylesheets the player uses.
 */
export function readPlayerThemeTokens(
  preset: FwThemePreset,
  doc: Document = document
): PlayerThemeTokens {
  const probe = doc.createElement("div");
  probe.className = "fw-player-surface";
  probe.hidden = true;
  doc.body.appendChild(probe);
  try {
    applyTheme(probe, preset);
    const computed = doc.defaultView!.getComputedStyle(probe);
    const values: PlayerThemeTokens["values"] = {};
    for (const token of PLAYER_THEME_TOKENS) {
      const value = computed.getPropertyValue(token).trim();
      if (value) values[token] = value;
    }
    const lightness = Number.parseFloat(values["--fw-surface-deep"]?.split(/\s+/)[2] ?? "");
    return { values, light: lightness > 50 };
  } finally {
    probe.remove();
  }
}

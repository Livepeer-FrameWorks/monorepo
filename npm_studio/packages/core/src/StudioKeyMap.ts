/**
 * StudioKeyMap — configurable keyboard shortcuts for StreamCrafter.
 *
 * Consumers can override any binding via `keyMap: Partial<StudioKeyMap>`.
 * Keys use the `KeyboardEvent.key` value (case-sensitive).
 */

export interface StudioKeyMap {
  /** Toggle streaming (go live / stop). Default: Shift+Enter */
  toggleStream: string[];
  /** Toggle master mute. Default: m */
  toggleMute: string[];
  /** Add camera source. Default: c */
  addCamera: string[];
  /** Start screen share. Default: s */
  shareScreen: string[];
  /** Toggle settings panel. Default: , */
  toggleSettings: string[];
  /** Next scene. Default: ] */
  nextScene: string[];
  /** Previous scene. Default: [ */
  prevScene: string[];
  /** Toggle advanced panel. Default: a */
  toggleAdvanced: string[];
}

export const DEFAULT_STUDIO_KEY_MAP: StudioKeyMap = {
  toggleStream: ["Shift+Enter"],
  toggleMute: ["m", "M"],
  addCamera: ["c", "C"],
  shareScreen: ["s", "S"],
  toggleSettings: [","],
  nextScene: ["]"],
  prevScene: ["["],
  toggleAdvanced: ["a", "A"],
};

/**
 * Build a reverse lookup: key string → action name.
 * Handles compound keys like "Shift+Enter" by checking modifiers.
 */
export function buildStudioKeyLookup(keyMap: StudioKeyMap): Map<string, keyof StudioKeyMap> {
  const map = new Map<string, keyof StudioKeyMap>();
  for (const [action, keys] of Object.entries(keyMap)) {
    for (const key of keys) {
      map.set(key, action as keyof StudioKeyMap);
    }
  }
  return map;
}

/**
 * Match a KeyboardEvent against the studio key lookup.
 * Returns the action name or undefined if no match.
 */
export function matchStudioKey(
  e: KeyboardEvent,
  lookup: Map<string, keyof StudioKeyMap>
): keyof StudioKeyMap | undefined {
  // Build compound key string matching modifiers
  const parts: string[] = [];
  if (e.ctrlKey) parts.push("Ctrl");
  if (e.altKey) parts.push("Alt");
  if (e.shiftKey) parts.push("Shift");
  if (e.metaKey) parts.push("Meta");
  parts.push(e.key);
  const compound = parts.join("+");

  return lookup.get(compound) ?? lookup.get(e.key);
}

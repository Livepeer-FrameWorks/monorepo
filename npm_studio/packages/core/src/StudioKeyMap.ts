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

/** Whether a key press is typing into a field, which no shortcut may take. */
export function isTypingTarget(e: KeyboardEvent): boolean {
  const target = (e.composedPath?.()[0] ?? e.target) as HTMLElement | null;
  if (!target) return false;
  const tag = target.tagName;
  return tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT" || !!target.isContentEditable;
}

/** The scene after (1) or before (-1) the active one, or null at either end. */
export function adjacentSceneId(
  scenes: readonly { id: string }[],
  activeSceneId: string | null | undefined,
  direction: 1 | -1
): string | null {
  const index = scenes.findIndex((scene) => scene.id === activeSceneId);
  if (index < 0) return null;
  return scenes[index + direction]?.id ?? null;
}

interface SceneHotkeyTarget {
  getAllScenes(): readonly { id: string }[];
  getActiveScene(): { id: string } | null | undefined;
  setActiveScene(sceneId: string): void;
}

const DEFAULT_LOOKUP = buildStudioKeyLookup(DEFAULT_STUDIO_KEY_MAP);

/**
 * Next scene (`]`) and previous scene (`[`) for the drop-in studios. Returns
 * whether the key switched the scene.
 */
export function handleStudioSceneHotkey(
  e: KeyboardEvent,
  sceneManager: SceneHotkeyTarget | null | undefined,
  lookup: Map<string, keyof StudioKeyMap> = DEFAULT_LOOKUP
): boolean {
  if (!sceneManager || isTypingTarget(e)) return false;
  const action = matchStudioKey(e, lookup);
  if (action !== "nextScene" && action !== "prevScene") return false;
  const next = adjacentSceneId(
    sceneManager.getAllScenes(),
    sceneManager.getActiveScene()?.id,
    action === "nextScene" ? 1 : -1
  );
  if (!next) return false;
  e.preventDefault();
  sceneManager.setActiveScene(next);
  return true;
}

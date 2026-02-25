/**
 * Player Registry
 *
 * Lazy registration of player implementations. Capabilities are declared
 * statically so MIME-based filtering happens without importing any player
 * modules. Only the players matching the stream's source types are loaded.
 */

import { PlayerManager } from "./PlayerManager";
import type { IPlayer, PlayerCapability } from "./PlayerInterface";

// ============================================================================
// Lazy Player Entry
// ============================================================================

interface LazyPlayerEntry {
  capability: PlayerCapability;
  load: () => Promise<IPlayer>;
}

/**
 * Static capability registry. Each entry declares its capabilities (MIME types,
 * priority) and a lazy loader that dynamically imports the implementation.
 * No player code is loaded until `load()` is called.
 */
const PLAYER_ENTRIES: LazyPlayerEntry[] = [
  {
    capability: {
      name: "Native Player",
      shortname: "native",
      priority: 1,
      mimes: [
        "html5/video/mp4",
        "html5/video/webm",
        "html5/video/ogg",
        "html5/audio/mp3",
        "html5/audio/webm",
        "html5/audio/ogg",
        "html5/audio/wav",
        "html5/application/vnd.apple.mpegurl",
        "html5/application/vnd.apple.mpegurl;version=7",
        "whep",
      ],
      notes: {
        "html5/application/vnd.apple.mpegurl":
          "No extra JS needed. Native on Safari/iOS. Chromium 142+: experimental.",
        whep: "Sub-second latency via WHEP. No seeking. B-frames may cause stutters.",
        "html5/video/mp4": "Progressive, <5s latency. Broadest device support of any protocol.",
        "html5/video/webm": "Progressive, <5s latency. Best on Chromium. Firefox: VP8/VP9 only.",
      },
    },
    load: () => import("../players/NativePlayer").then((m) => new m.NativePlayerImpl()),
  },
  {
    capability: {
      name: "WebCodecs Player",
      shortname: "webcodecs",
      priority: 0,
      mimes: ["ws/video/raw", "wss/video/raw", "ws/video/h264", "wss/video/h264"],
      notes: {
        "ws/video/raw": "Ultra-low latency (<100ms). Raw frames decoded via WebCodecs API.",
        "ws/video/h264": "Ultra-low latency (<100ms). H.264 decoded via WebCodecs API.",
      },
    },
    load: () => import("../players/WebCodecsPlayer").then((m) => new m.WebCodecsPlayerImpl()),
  },
  {
    capability: {
      name: "MistServer WebRTC",
      shortname: "mist-webrtc",
      priority: 2,
      mimes: ["webrtc", "mist/webrtc"],
      notes: {
        webrtc:
          "Sub-second latency. MistServer-native signaling (not WHEP-interoperable). No seeking.",
      },
    },
    load: () => import("../players/MistWebRTCPlayer").then((m) => new m.MistWebRTCPlayerImpl()),
  },
  {
    capability: {
      name: "Video.js Player",
      shortname: "videojs",
      priority: 2,
      mimes: [
        "html5/application/vnd.apple.mpegurl",
        "html5/application/vnd.apple.mpegurl;version=7",
      ],
      notes: {
        "html5/application/vnd.apple.mpegurl":
          "HLS via VHS engine. Adaptive bitrate. Heavier bundle but battle-tested.",
      },
    },
    load: () => import("../players/VideoJsPlayer").then((m) => new m.VideoJsPlayerImpl()),
  },
  {
    capability: {
      name: "HLS.js Player",
      shortname: "hlsjs",
      priority: 3,
      mimes: [
        "html5/application/vnd.apple.mpegurl",
        "html5/application/vnd.apple.mpegurl;version=7",
      ],
      notes: {
        "html5/application/vnd.apple.mpegurl":
          "Lightweight HLS via MSE. Good adaptive bitrate. Not used on Safari (native HLS instead).",
      },
    },
    load: () => import("../players/HlsJsPlayer").then((m) => new m.HlsJsPlayerImpl()),
  },
  {
    capability: {
      name: "Dash.js Player",
      shortname: "dashjs",
      priority: 100,
      mimes: ["dash/video/mp4"],
      notes: {
        "dash/video/mp4": "Adaptive bitrate via DASH. 2-15s latency. No Apple/iOS support.",
      },
    },
    load: () => import("../players/DashJsPlayer").then((m) => new m.DashJsPlayerImpl()),
  },
  {
    capability: {
      name: "Legacy",
      shortname: "mist-legacy",
      priority: 99,
      mimes: ["mist/legacy"],
      notes: {
        "mist/legacy": "MistServer embedded player. Fallback when no other player works.",
      },
    },
    load: () => import("../players/MistPlayer").then((m) => new m.MistPlayerImpl()),
  },
  {
    capability: {
      name: "MEWS WebSocket Player",
      shortname: "mews",
      priority: 2,
      mimes: ["ws/video/mp4", "wss/video/mp4", "ws/video/webm", "wss/video/webm"],
      notes: {
        "ws/video/mp4":
          "WebSocket MP4, <3s latency. Server-side bitrate adjustment. No Safari/iOS.",
      },
    },
    load: () => import("../players/MewsWsPlayer").then((m) => new m.MewsWsPlayerImpl()),
  },
];

// ============================================================================
// Loading State
// ============================================================================

/** Merge registry-defined notes into a loaded player's capability */
function mergeRegistryNotes(player: IPlayer, entry: LazyPlayerEntry): void {
  if (entry.capability.notes && !player.capability.notes) {
    (player.capability as any).notes = entry.capability.notes;
  }
}

/** Track which players have been loaded per manager */
const loadedPlayersMap = new WeakMap<PlayerManager, Set<string>>();

function getLoadedSet(manager: PlayerManager): Set<string> {
  let set = loadedPlayersMap.get(manager);
  if (!set) {
    set = new Set();
    loadedPlayersMap.set(manager, set);
  }
  return set;
}

/**
 * Load only players whose capabilities match the given source MIME types.
 * Already-loaded players are skipped. Safe to call multiple times with
 * different MIME types — new players are added incrementally.
 */
async function loadMatchingPlayers(manager: PlayerManager, sourceMimes: string[]): Promise<void> {
  const loaded = getLoadedSet(manager);

  const toLoad = PLAYER_ENTRIES.filter((entry) => {
    if (loaded.has(entry.capability.shortname)) return false;
    return entry.capability.mimes.some((m) => sourceMimes.includes(m));
  });

  if (toLoad.length === 0) return;

  const players = await Promise.all(
    toLoad.map(async (entry) => {
      const player = await entry.load();
      mergeRegistryNotes(player, entry);
      return player;
    })
  );
  for (const player of players) {
    loaded.add(player.capability.shortname);
    const alreadyRegistered = manager
      .getRegisteredPlayers()
      .some((p) => p.capability.shortname === player.capability.shortname);
    if (!alreadyRegistered) {
      manager.registerPlayer(player);
    }
  }
}

/**
 * Load a specific player by shortname. Used for forcePlayer support.
 */
async function loadPlayerByShortname(manager: PlayerManager, shortname: string): Promise<void> {
  const loaded = getLoadedSet(manager);
  if (loaded.has(shortname)) return;

  const entry = PLAYER_ENTRIES.find((e) => e.capability.shortname === shortname);
  if (!entry) return;

  const player = await entry.load();
  mergeRegistryNotes(player, entry);
  loaded.add(player.capability.shortname);
  const alreadyRegistered = manager
    .getRegisteredPlayers()
    .some((p) => p.capability.shortname === player.capability.shortname);
  if (!alreadyRegistered) {
    manager.registerPlayer(player);
  }
}

/**
 * Load all players. Used for backwards compatibility (createPlayerManager,
 * registerAllPlayers) and when no MIME filter is available.
 */
async function loadAllPlayers(manager: PlayerManager): Promise<void> {
  const allMimes = PLAYER_ENTRIES.flatMap((e) => e.capability.mimes);
  return loadMatchingPlayers(manager, allMimes);
}

// ============================================================================
// Public API
// ============================================================================

/** Deduplication promises for full registration (loadAllPlayers) */
const fullRegistrationPromises = new WeakMap<PlayerManager, Promise<void>>();

/**
 * Ensure all players are registered. Backwards-compatible API that loads
 * every player module. Prefer the MIME-filtered path via initializePlayer.
 */
export function ensurePlayersRegistered(
  manager: PlayerManager = globalPlayerManager
): Promise<void> {
  const loaded = getLoadedSet(manager);
  if (loaded.size === PLAYER_ENTRIES.length) {
    return Promise.resolve();
  }

  const existing = fullRegistrationPromises.get(manager);
  if (existing) return existing;

  const promise = loadAllPlayers(manager).catch((error) => {
    console.error("[PlayerRegistry] Registration failed:", error);
    fullRegistrationPromises.delete(manager);
    throw error;
  });

  fullRegistrationPromises.set(manager, promise);
  return promise;
}

/**
 * Monkey-patch initializePlayer to lazily load only matching players.
 * Extracts source MIME types from streamInfo and loads the subset of
 * player modules that can handle those types.
 */
const originalInitialize = PlayerManager.prototype.initializePlayer;
PlayerManager.prototype.initializePlayer = async function (
  ...args: Parameters<PlayerManager["initializePlayer"]>
) {
  const [_container, streamInfo, _playerOptions, managerOptions] = args;
  const sourceMimes = streamInfo?.source?.map((s) => s.type) ?? [];

  if (sourceMimes.length > 0) {
    // Load only players matching the stream's source types
    await loadMatchingPlayers(this, sourceMimes);

    // Also load a forced player if specified
    if (managerOptions?.forcePlayer) {
      await loadPlayerByShortname(this, managerOptions.forcePlayer);
    }
  } else {
    // No source info — fall back to loading all
    await ensurePlayersRegistered(this);
  }

  return originalInitialize.apply(this, args);
};

// ============================================================================
// Global Instance
// ============================================================================

const isDev = (() => {
  try {
    const g = globalThis as Record<string, unknown>;
    const p = g.process as Record<string, unknown> | undefined;
    const env = p?.env as Record<string, unknown> | undefined;
    return env?.NODE_ENV === "development";
  } catch {
    return false;
  }
})();

export const globalPlayerManager = new PlayerManager({
  debug: isDev,
  autoFallback: true,
  maxFallbackAttempts: 3,
});

/**
 * Register all available players (async for backwards compatibility)
 */
export async function registerAllPlayers(
  manager: PlayerManager = globalPlayerManager
): Promise<void> {
  await ensurePlayersRegistered(manager);
}

/**
 * Create a new PlayerManager instance with all players registered
 */
export function createPlayerManager(
  options?: ConstructorParameters<typeof PlayerManager>[0]
): PlayerManager {
  const manager = new PlayerManager(options);
  ensurePlayersRegistered(manager).catch((error) => {
    if (isDev) {
      console.warn("Player registration failed:", error);
    }
  });
  return manager;
}

/**
 * Get the static capability registry (for UI display without loading modules)
 */
export function getAvailablePlayerCapabilities(): PlayerCapability[] {
  return PLAYER_ENTRIES.map((e) => e.capability);
}

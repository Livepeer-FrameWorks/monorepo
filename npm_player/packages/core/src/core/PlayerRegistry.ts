/**
 * Player Registry
 *
 * Central registration of all available player implementations
 */

import { PlayerManager } from "./PlayerManager";
import type { IPlayer } from "./PlayerInterface";

/**
 * Shared registration state per manager instance. Ensures that we only
 * dynamically import and attach transport adapters once per manager.
 */
const managerRegistrationPromises = new WeakMap<PlayerManager, Promise<void>>();

async function registerPlayersForManager(manager: PlayerManager): Promise<void> {
  console.log("[PlayerRegistry] registerPlayersForManager starting...");
  if (manager.getRegisteredPlayers().length > 0) {
    console.log("[PlayerRegistry] Players already registered");
    return;
  }

  console.log("[PlayerRegistry] Dynamically importing player modules...");
  const [
    nativeModule,
    videoModule,
    hlsModule,
    dashModule,
    mistModule,
    mewsModule,
    mistWebRTCModule,
    webCodecsModule,
  ] = await Promise.all([
    import("../players/NativePlayer"),
    import("../players/VideoJsPlayer"),
    import("../players/HlsJsPlayer"),
    import("../players/DashJsPlayer"),
    import("../players/MistPlayer"),
    import("../players/MewsWsPlayer"),
    import("../players/MistWebRTCPlayer"),
    import("../players/WebCodecsPlayer"),
  ]);

  console.log("[PlayerRegistry] All player modules imported, instantiating...");
  const instantiatedPlayers: IPlayer[] = [
    new nativeModule.NativePlayerImpl(),
    new webCodecsModule.WebCodecsPlayerImpl(), // Priority 1 - lowest latency
    new mistWebRTCModule.MistWebRTCPlayerImpl(), // Priority 2
    new videoModule.VideoJsPlayerImpl(),
    new hlsModule.HlsJsPlayerImpl(),
    new dashModule.DashJsPlayerImpl(),
    new mistModule.MistPlayerImpl(),
    new mewsModule.MewsWsPlayerImpl(),
  ];

  for (const player of instantiatedPlayers) {
    const alreadyRegistered = manager
      .getRegisteredPlayers()
      .some((existing) => existing.capability.shortname === player.capability.shortname);

    if (!alreadyRegistered) {
      manager.registerPlayer(player);
    }
  }
  console.log(
    `[PlayerRegistry] Registration complete. ${manager.getRegisteredPlayers().length} players registered.`
  );
}

export function ensurePlayersRegistered(
  manager: PlayerManager = globalPlayerManager
): Promise<void> {
  console.log("[PlayerRegistry] ensurePlayersRegistered called");
  if (manager.getRegisteredPlayers().length > 0) {
    console.log("[PlayerRegistry] Already registered, returning");
    return Promise.resolve();
  }

  const existing = managerRegistrationPromises.get(manager);
  if (existing) {
    console.log("[PlayerRegistry] Using existing registration promise");
    return existing;
  }

  console.log("[PlayerRegistry] Starting new registration...");
  const registrationPromise = registerPlayersForManager(manager).catch((error) => {
    console.error("[PlayerRegistry] Registration failed:", error);
    managerRegistrationPromises.delete(manager);
    throw error;
  });

  managerRegistrationPromises.set(manager, registrationPromise);
  return registrationPromise;
}

const originalInitialize = PlayerManager.prototype.initializePlayer;
PlayerManager.prototype.initializePlayer = async function (
  ...args: Parameters<PlayerManager["initializePlayer"]>
) {
  console.log("[PlayerRegistry] initializePlayer wrapper - calling ensurePlayersRegistered...");
  await ensurePlayersRegistered(this);
  console.log(
    "[PlayerRegistry] ensurePlayersRegistered done, calling original initializePlayer..."
  );
  return originalInitialize.apply(this, args);
};

/**
 * Global PlayerManager instance with deferred registration
 */
const isDev = (() => {
  try {
    // In browser builds, process may be undefined; guard access
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
 * Export individual player classes for direct use
 */
export { NativePlayerImpl, DirectPlaybackPlayerImpl } from "../players/NativePlayer";
export { HlsJsPlayerImpl } from "../players/HlsJsPlayer";
export { DashJsPlayerImpl } from "../players/DashJsPlayer";
export { VideoJsPlayerImpl } from "../players/VideoJsPlayer";
export { MistPlayerImpl } from "../players/MistPlayer";
export { MewsWsPlayerImpl } from "../players/MewsWsPlayer";
export { MistWebRTCPlayerImpl } from "../players/MistWebRTCPlayer";
export { WebCodecsPlayerImpl } from "../players/WebCodecsPlayer";

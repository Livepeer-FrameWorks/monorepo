/**
 * Player Registry
 *
 * Central registration of all available player implementations
 */

import { PlayerManager } from './PlayerManager';
import type { IPlayer } from './PlayerInterface';

/**
 * Shared registration state per manager instance. Ensures that we only
 * dynamically import and attach transport adapters once per manager.
 */
const managerRegistrationPromises = new WeakMap<PlayerManager, Promise<void>>();

async function registerPlayersForManager(manager: PlayerManager): Promise<void> {
  if (manager.getRegisteredPlayers().length > 0) return;

  const [
    directModule,
    videoModule,
    hlsModule,
    dashModule,
    mistModule,
    mewsModule
  ] = await Promise.all([
    import('../components/players/DirectPlaybackPlayer'),
    import('../components/players/VideoJsPlayer'),
    import('../components/players/HlsJsPlayer'),
    import('../components/players/DashJsPlayer'),
    import('../components/players/MistPlayer'),
    import('../components/players/MewsWsPlayer')
  ]);

  const instantiatedPlayers: IPlayer[] = [
    new directModule.DirectPlaybackPlayerImpl(),
    new videoModule.VideoJsPlayerImpl(),
    new hlsModule.HlsJsPlayerImpl(),
    new dashModule.DashJsPlayerImpl(),
    new mistModule.MistPlayerImpl(),
    new mewsModule.MewsWsPlayerImpl()
  ];

  for (const player of instantiatedPlayers) {
    const alreadyRegistered = manager
      .getRegisteredPlayers()
      .some(existing => existing.capability.shortname === player.capability.shortname);

    if (!alreadyRegistered) {
      manager.registerPlayer(player);
    }
  }
}

export function ensurePlayersRegistered(manager: PlayerManager = globalPlayerManager): Promise<void> {
  if (manager.getRegisteredPlayers().length > 0) {
    return Promise.resolve();
  }

  const existing = managerRegistrationPromises.get(manager);
  if (existing) {
    return existing;
  }

  const registrationPromise = registerPlayersForManager(manager).catch(error => {
    managerRegistrationPromises.delete(manager);
    throw error;
  });

  managerRegistrationPromises.set(manager, registrationPromise);
  return registrationPromise;
}

const originalInitialize = PlayerManager.prototype.initializePlayer;
PlayerManager.prototype.initializePlayer = async function (...args: Parameters<PlayerManager['initializePlayer']>) {
  await ensurePlayersRegistered(this);
  return originalInitialize.apply(this, args);
};

/**
 * Global PlayerManager instance with deferred registration
 */
const isDev = (() => {
  try {
    // In browser builds, process may be undefined; guard access
    // @ts-ignore
    return typeof process !== 'undefined' && process && process.env && process.env.NODE_ENV === 'development';
  } catch {
    return false;
  }
})();

export const globalPlayerManager = new PlayerManager({
  debug: isDev,
  autoFallback: true,
  maxFallbackAttempts: 3
});

/**
 * Register all available players (async for backwards compatibility)
 */
export async function registerAllPlayers(manager: PlayerManager = globalPlayerManager): Promise<void> {
  await ensurePlayersRegistered(manager);
}

/**
 * Create a new PlayerManager instance with all players registered
 */
export function createPlayerManager(options?: ConstructorParameters<typeof PlayerManager>[0]): PlayerManager {
  const manager = new PlayerManager(options);
  ensurePlayersRegistered(manager).catch(error => {
    if (isDev) {
      console.warn('Player registration failed:', error);
    }
  });
  return manager;
}

/**
 * Export individual player classes for direct use
 */
export { DirectPlaybackPlayerImpl } from '../components/players/DirectPlaybackPlayer';
export { HlsJsPlayerImpl } from '../components/players/HlsJsPlayer';
export { DashJsPlayerImpl } from '../components/players/DashJsPlayer';
export { VideoJsPlayerImpl } from '../components/players/VideoJsPlayer';
export { MistPlayerImpl } from '../components/players/MistPlayer';
export { MewsWsPlayerImpl } from '../components/players/MewsWsPlayer';

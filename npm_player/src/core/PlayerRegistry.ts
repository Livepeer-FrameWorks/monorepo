/**
 * Player Registry
 * 
 * Central registration of all available player implementations
 */

import { PlayerManager } from './PlayerManager';
import { Html5NativePlayerImpl } from '../components/players/Html5NativePlayer';
import { HlsJsPlayerImpl } from '../components/players/HlsJsPlayer';
import { DashJsPlayerImpl } from '../components/players/DashJsPlayer';
import { VideoJsPlayerImpl } from '../components/players/VideoJsPlayer';
import { MistPlayerImpl } from '../components/players/MistPlayer';
import { MewsWsPlayerImpl } from '../components/players/MewsWsPlayer';

/**
 * Global PlayerManager instance with all players registered
 */
const isDev = (() => {
  try {
    // In browser builds, process may be undefined; guard access
    // @ts-ignore
    return typeof process !== 'undefined' && process && process.env && process.env.NODE_ENV === 'development';
  } catch { return false; }
})();

export const globalPlayerManager = new PlayerManager({
  debug: isDev,
  autoFallback: true,
  maxFallbackAttempts: 3
});

/**
 * Register all available players
 */
export function registerAllPlayers(): void {
  // Register players in priority order (lower priority = higher preference)
  
  // HTML5 Native handles MP4/WEBM and WHEP hybrid
  globalPlayerManager.registerPlayer(new Html5NativePlayerImpl());
  
  // VideoJS - very compatible, good fallback
  globalPlayerManager.registerPlayer(new VideoJsPlayerImpl());
  
  // HLS.js - specialized HLS support
  globalPlayerManager.registerPlayer(new HlsJsPlayerImpl());
  
  // DASH.js - specialized DASH support
  globalPlayerManager.registerPlayer(new DashJsPlayerImpl());
  
  // MistPlayer - MistServer integration
  globalPlayerManager.registerPlayer(new MistPlayerImpl());
  
  // MEWS WebSocket - specialized WebSocket support
  globalPlayerManager.registerPlayer(new MewsWsPlayerImpl());
}

/**
 * Create a new PlayerManager instance with all players registered
 */
export function createPlayerManager(options?: ConstructorParameters<typeof PlayerManager>[0]): PlayerManager {
  const manager = new PlayerManager(options);
  
  // Register all players
  manager.registerPlayer(new Html5NativePlayerImpl());
  manager.registerPlayer(new VideoJsPlayerImpl());
  manager.registerPlayer(new HlsJsPlayerImpl());
  manager.registerPlayer(new DashJsPlayerImpl());
  manager.registerPlayer(new MistPlayerImpl());
  manager.registerPlayer(new MewsWsPlayerImpl());
  
  return manager;
}

// Auto-register on import
registerAllPlayers();

/**
 * Export individual player classes for direct use
 */
export {
  Html5NativePlayerImpl,
  HlsJsPlayerImpl,
  DashJsPlayerImpl,
  VideoJsPlayerImpl,
  MistPlayerImpl,
  MewsWsPlayerImpl
};
/**
 * Player Selection Algorithm
 * Ported from MistMetaPlayer v3.1.0
 *
 * Implements sophisticated player selection based on:
 * - Source priority (from MistServer)
 * - Player capability scoring
 * - Browser compatibility checks
 * - Weighted scoring (track types, player priority, source order)
 */

import { scorePlayer } from './scorer';

export interface StreamSource {
  url: string;
  type: string;
  index?: number;
}

export interface StreamTrack {
  type: 'video' | 'audio' | 'meta';
  codec: string;
  codecstring?: string;
}

export interface StreamInfo {
  source: StreamSource[];
  meta: {
    tracks: StreamTrack[];
  };
}

export interface Player {
  name: string;
  shortname: string;
  priority: number;
  mimes: string[];
  isMimeSupported: (mimetype: string) => boolean;
  isBrowserSupported: (mimetype: string, source: StreamSource, info: StreamInfo) => boolean | string[];
}

export interface PlayerSelection {
  score: number;
  player: string;
  source: StreamSource;
  source_index: number;
}

export interface SelectionOptions {
  forcePlayer?: string;
  forceSource?: number;
  forceType?: string;
  forcePriority?: {
    source?: ((a: StreamSource) => number)[];
    player?: ((a: Player) => number)[];
    first?: 'source' | 'player';
  };
  startCombo?: {
    player?: string | number;
    source?: string | number;
  };
}

/**
 * Main player selection algorithm
 * Returns the best player+source combination
 */
export function selectPlayer(
  players: Player[],
  info: StreamInfo,
  options: SelectionOptions = {}
): PlayerSelection | false {
  // Filter sources based on options
  let sources: StreamSource[];
  if (options.forceSource !== undefined) {
    sources = [info.source[options.forceSource]];
  } else if (options.forceType) {
    sources = info.source.filter(s => s.type === options.forceType);
  } else {
    sources = [...info.source]; // Clone array
  }

  // Add original index to sources for sorting
  sources.forEach((source, index) => {
    if (!('origIndex' in source)) {
      source.index = info.source.indexOf(source);
    }
  });

  // Filter players based on options
  let filteredPlayers: Player[];
  if (options.forcePlayer && players.find(p => p.shortname === options.forcePlayer)) {
    filteredPlayers = players.filter(p => p.shortname === options.forcePlayer);
  } else {
    filteredPlayers = [...players];
  }

  // Apply custom sorting if specified
  if (options.forcePriority) {
    if (options.forcePriority.source) {
      // Sort sources using custom functions
      sources.sort((a, b) => {
        for (const sortFn of options.forcePriority!.source!) {
          const diff = sortFn(a) - sortFn(b);
          if (diff !== 0) return diff;
        }
        return 0;
      });
    }
    
    if (options.forcePriority.player) {
      // Sort players using custom functions
      filteredPlayers.sort((a, b) => {
        for (const sortFn of options.forcePriority!.player!) {
          const diff = sortFn(a) - sortFn(b);
          if (diff !== 0) return diff;
        }
        return 0;
      });
    }
  }

  // Calculate max priority for normalization
  const maxPriority = Math.max(...filteredPlayers.map(p => p.priority), 1);
  const totalSources = sources.length;

  let best: PlayerSelection = {
    score: 0,
    source_index: -1,
    player: '',
    source: sources[0]
  };

  // Determine loop order (player first or source first)
  const outerLoop = options.forcePriority?.first === 'player' ? filteredPlayers : sources;
  const innerLoop = options.forcePriority?.first === 'player' ? sources : filteredPlayers;

  for (const outer of outerLoop) {
    for (const inner of innerLoop) {
      const source = options.forcePriority?.first === 'player' ? inner as StreamSource : outer as StreamSource;
      const player = options.forcePriority?.first === 'player' ? outer as Player : inner as Player;

      // Check if player supports this MIME type
      if (!player.isMimeSupported(source.type)) {
        continue;
      }

      // Check browser compatibility
      const tracktypes = player.isBrowserSupported(source.type, source, info);
      if (!tracktypes) {
        continue;
      }

      // Use weighted scoring from scorer.ts
      const sourceIndex = source.index ?? 0;
      const playerScore = scorePlayer(tracktypes, player.priority, sourceIndex, {
        maxPriority,
        totalSources,
      });

      if (playerScore.total > best.score) {
        best = {
          score: playerScore.total,
          player: player.shortname,
          source,
          source_index: sourceIndex
        };
      }
    }
  }

  return best.score > 0 ? best : false;
}

/**
 * Simple player registration system
 */
export class PlayerRegistry {
  private players: Map<string, Player> = new Map();

  register(player: Player): void {
    this.players.set(player.shortname, player);
  }

  unregister(shortname: string): void {
    this.players.delete(shortname);
  }

  getPlayer(shortname: string): Player | undefined {
    return this.players.get(shortname);
  }

  getAllPlayers(): Player[] {
    return Array.from(this.players.values())
      .sort((a, b) => a.priority - b.priority);
  }

  selectBest(info: StreamInfo, options?: SelectionOptions): PlayerSelection | false {
    return selectPlayer(this.getAllPlayers(), info, options);
  }
}
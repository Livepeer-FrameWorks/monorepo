/**
 * Player Selection Algorithm
 * Ported from MistMetaPlayer v3.1.0
 * 
 * Implements sophisticated player selection based on:
 * - Source priority (from MistServer)
 * - Player capability scoring
 * - Browser compatibility checks
 */

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
 * Calculate score for a player+source combination
 */
function calcScore(tracktypes: boolean | string[]): number {
  if (tracktypes === true) {
    return 1.9; // Something will play, but player doesn't tell us what
  }
  
  if (tracktypes === false || !Array.isArray(tracktypes)) {
    return 0;
  }

  const scores = {
    video: 2,
    audio: 1,
    subtitle: 0.5
  };

  let score = 0;
  for (const tracktype of tracktypes) {
    score += scores[tracktype as keyof typeof scores] || 0;
  }
  return score;
}

/**
 * Calculate maximum possible score for this stream
 */
function getMaxScore(info: StreamInfo): number {
  const trackTypes: Record<string, boolean> = {};
  
  for (const track of info.meta.tracks) {
    if (track.type === 'meta') {
      trackTypes[track.codec] = true;
    } else {
      trackTypes[track.type] = true;
    }
  }
  
  return calcScore(Object.keys(trackTypes));
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

  const maxScore = getMaxScore(info);
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

      const score = calcScore(tracktypes);
      if (score > best.score) {
        best = {
          score,
          player: player.shortname,
          source,
          source_index: source.index || 0
        };

        // Early exit if we found the maximum possible score
        if (best.score === maxScore) {
          return best;
        }
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
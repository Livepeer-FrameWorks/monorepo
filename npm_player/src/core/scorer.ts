/**
 * Player Scoring System
 * Ported from MistMetaPlayer v3.1.0
 * 
 * Implements the scoring algorithm for player selection
 */

export interface TrackScore {
  video: number;
  audio: number;
  subtitle: number;
}

export interface PlayerScore {
  base: number;
  trackTypes: string[];
  total: number;
}

/**
 * Default track type scores
 */
export const DEFAULT_TRACK_SCORES: TrackScore = {
  video: 2.0,
  audio: 1.0,
  subtitle: 0.5
};

/**
 * Calculate score for supported track types
 */
export function calculateTrackScore(
  supportedTracks: string[] | boolean,
  scores: TrackScore = DEFAULT_TRACK_SCORES
): number {
  if (supportedTracks === true) {
    // Player supports something but doesn't specify what
    return 1.9; // Slightly less than perfect video score
  }

  if (supportedTracks === false || supportedTracks.length === 0) {
    return 0;
  }

  let totalScore = 0;
  for (const trackType of supportedTracks) {
    const score = scores[trackType as keyof TrackScore];
    if (score !== undefined) {
      totalScore += score;
    }
  }

  return totalScore;
}

/**
 * Calculate maximum possible score for a stream
 */
export function calculateMaxScore(
  availableTracks: string[],
  scores: TrackScore = DEFAULT_TRACK_SCORES
): number {
  return calculateTrackScore(availableTracks, scores);
}

/**
 * Player priority scoring
 */
export function calculatePriorityScore(priority: number, maxPriority: number): number {
  // Lower priority number = higher score
  // Normalize to 0-1 range, then invert
  return 1 - (priority / Math.max(maxPriority, 1));
}

/**
 * Source preference scoring based on MistServer ordering
 */
export function calculateSourceScore(
  sourceIndex: number,
  totalSources: number
): number {
  // Earlier sources (lower index) get higher scores
  return 1 - (sourceIndex / Math.max(totalSources - 1, 1));
}

/**
 * Bandwidth/quality scoring
 */
export function calculateQualityScore(
  bandwidth?: number,
  targetBandwidth?: number
): number {
  if (!bandwidth || !targetBandwidth) {
    return 1.0; // Neutral score if no bandwidth info
  }

  // Score based on how close to target bandwidth
  const ratio = Math.min(bandwidth, targetBandwidth) / Math.max(bandwidth, targetBandwidth);
  return ratio;
}

/**
 * Comprehensive player scoring
 */
export function scorePlayer(
  supportedTracks: string[] | boolean,
  priority: number,
  sourceIndex: number,
  options: {
    maxPriority?: number;
    totalSources?: number;
    trackScores?: Partial<TrackScore>;
    bandwidth?: number;
    targetBandwidth?: number;
    weights?: {
      tracks: number;
      priority: number;
      source: number;
      quality: number;
    };
  } = {}
): PlayerScore {
  const {
    maxPriority = 10,
    totalSources = 1,
    trackScores = {},
    bandwidth,
    targetBandwidth,
    weights = {
      tracks: 0.7,    // Track support is most important
      priority: 0.15, // Player priority matters
      source: 0.10,   // Source order matters
      quality: 0.05   // Quality/bandwidth matching
    }
  } = options;

  const finalTrackScores = { ...DEFAULT_TRACK_SCORES, ...trackScores };
  
  // Individual component scores
  const trackScore = calculateTrackScore(supportedTracks, finalTrackScores);
  const priorityScore = calculatePriorityScore(priority, maxPriority);
  const sourceScore = calculateSourceScore(sourceIndex, totalSources);
  const qualityScore = calculateQualityScore(bandwidth, targetBandwidth);

  // Weighted total score
  const total = 
    trackScore * weights.tracks +
    priorityScore * weights.priority +
    sourceScore * weights.source +
    qualityScore * weights.quality;

  return {
    base: trackScore,
    trackTypes: Array.isArray(supportedTracks) ? supportedTracks : [],
    total
  };
}

/**
 * Compare two player scores
 */
export function compareScores(a: PlayerScore, b: PlayerScore): number {
  return b.total - a.total; // Higher scores first
}

/**
 * Batch score multiple players and return sorted by best score
 */
export function scoreAndRankPlayers<T extends { priority: number }>(
  players: Array<{
    player: T;
    supportedTracks: string[] | boolean;
    sourceIndex: number;
  }>,
  options?: Parameters<typeof scorePlayer>[3]
): Array<{
  player: T;
  score: PlayerScore;
}> {
  const scoredPlayers = players.map(({ player, supportedTracks, sourceIndex }) => ({
    player,
    score: scorePlayer(supportedTracks, player.priority, sourceIndex, options)
  }));

  return scoredPlayers.sort((a, b) => compareScores(a.score, b.score));
}

/**
 * Utility to check if a score meets minimum thresholds
 */
export function meetsMinimumScore(
  score: PlayerScore,
  requirements: {
    minTotal?: number;
    requireVideo?: boolean;
    requireAudio?: boolean;
    minTrackTypes?: number;
  }
): boolean {
  const {
    minTotal = 0,
    requireVideo = false,
    requireAudio = false,
    minTrackTypes = 0
  } = requirements;

  // Check total score
  if (score.total < minTotal) {
    return false;
  }

  // Check track type requirements
  if (requireVideo && !score.trackTypes.includes('video')) {
    return false;
  }

  if (requireAudio && !score.trackTypes.includes('audio')) {
    return false;
  }

  if (score.trackTypes.length < minTrackTypes) {
    return false;
  }

  return true;
}
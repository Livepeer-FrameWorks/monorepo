/**
 * PlayerManager
 *
 * Central orchestrator for player selection and lifecycle management.
 * Single source of truth for all scoring logic.
 *
 * Architecture:
 * - `getAllCombinations()` is THE single function that computes player+source scores
 * - Results are cached by content (source types + track codecs), not object identity
 * - Events fire only when selection actually changes (no render spam)
 * - `selectBestPlayer()` returns cached winner without recomputation
 */

import { getBrowserInfo, getBrowserCompatibility } from "./detector";
import type {
  IPlayer,
  StreamSource,
  StreamInfo,
  PlayerOptions,
  ErrorHandlingEvents,
} from "./PlayerInterface";
import { ErrorCode } from "./PlayerInterface";
import { ErrorClassifier, type RecoveryAction } from "./ErrorClassifier";
import { scorePlayer, isProtocolBlacklisted } from "./scorer";
import type { PlaybackMode } from "../types";

// ============================================================================
// Types
// ============================================================================

export interface PlayerSelection {
  score: number;
  player: string;
  source: StreamSource;
  source_index: number;
}

export interface PlayerManagerOptions {
  /** Force a specific player */
  forcePlayer?: string;
  /** Force a specific source index */
  forceSource?: number;
  /** Force a specific MIME type */
  forceType?: string;
  /** Enable debug logging (logs selection changes only, not every render) */
  debug?: boolean;
  /** Automatic fallback on player failure */
  autoFallback?: boolean;
  /** Maximum fallback attempts */
  maxFallbackAttempts?: number;
  /** Playback mode for protocol selection */
  playbackMode?: PlaybackMode;
}

export interface PlayerManagerEvents {
  playerSelected: { player: string; source: StreamSource; score: number };
  playerInitialized: { player: IPlayer; videoElement: HTMLVideoElement };
  fallbackAttempted: { fromPlayer: string; toPlayer: string };
  /** Fires when selection changes (different player+source than before) */
  "selection-changed": PlayerSelection | null;
  /** Fires when combinations are recomputed (cache miss) */
  "combinations-updated": PlayerCombination[];
  /** Tier 1: Silent recovery attempted (for telemetry) */
  recoveryAttempted: ErrorHandlingEvents["recoveryAttempted"];
  /** Tier 2: Protocol swapped - UI should show toast */
  protocolSwapped: ErrorHandlingEvents["protocolSwapped"];
  /** Tier 3: Quality changed - UI should show toast */
  qualityChanged: ErrorHandlingEvents["qualityChanged"];
  /** Tier 4: All options exhausted - UI should show modal */
  playbackFailed: ErrorHandlingEvents["playbackFailed"];
}

/** Full combination info including scoring breakdown */
export interface PlayerCombination {
  player: string;
  playerName: string;
  source: StreamSource;
  sourceIndex: number;
  sourceType: string;
  score: number;
  compatible: boolean;
  incompatibleReason?: string;
  /** True when player supports MIME but codec is incompatible */
  codecIncompatible?: boolean;
  scoreBreakdown?: {
    trackScore: number;
    trackTypes: string[];
    priorityScore: number;
    sourceScore: number;
    reliabilityScore?: number;
    modeBonus?: number;
    routingBonus?: number;
    weights: {
      tracks: number;
      priority: number;
      source: number;
      reliability?: number;
      mode?: number;
      routing?: number;
    };
  };
}

// ============================================================================
// PlayerManager Class
// ============================================================================

export class PlayerManager {
  private players: Map<string, IPlayer> = new Map();
  private currentPlayer: IPlayer | null = null;
  private listeners: Map<string, Set<Function>> = new Map();
  private fallbackAttempts = 0;
  private options: PlayerManagerOptions;

  // Error handling
  private errorClassifier: ErrorClassifier;

  // Caching: prevents recalculation on every render
  private cachedCombinations: PlayerCombination[] | null = null;
  private cachedSelection: PlayerSelection | null = null;
  private cacheKey: string | null = null;
  private lastLoggedWinner: string | null = null;

  // Fallback state
  private lastContainer: HTMLElement | null = null;
  private lastStreamInfo: StreamInfo | null = null;
  private lastPlayerOptions: PlayerOptions = {};
  private lastManagerOptions: PlayerManagerOptions = {};
  private excludedPlayers: Set<string> = new Set();

  // Serializes lifecycle operations to prevent race conditions
  private opQueue: Promise<void> = Promise.resolve();

  constructor(options: PlayerManagerOptions = {}) {
    this.options = {
      debug: false,
      autoFallback: true,
      maxFallbackAttempts: 3,
      ...options,
    };

    this.errorClassifier = new ErrorClassifier({
      alternativesCount: 0,
      debug: this.options.debug,
    });

    // Forward error classifier events to manager events
    this.errorClassifier.on("recoveryAttempted", (data) => this.emit("recoveryAttempted", data));
    this.errorClassifier.on("protocolSwapped", (data) => this.emit("protocolSwapped", data));
    this.errorClassifier.on("qualityChanged", (data) => this.emit("qualityChanged", data));
    this.errorClassifier.on("playbackFailed", (data) => this.emit("playbackFailed", data));
  }

  // ==========================================================================
  // Player Registration
  // ==========================================================================

  registerPlayer(player: IPlayer): void {
    this.players.set(player.capability.shortname, player);
    this.invalidateCache();
    this.log(`Registered player: ${player.capability.name}`);
  }

  unregisterPlayer(shortname: string): void {
    const player = this.players.get(shortname);
    if (player) {
      player.destroy();
      this.players.delete(shortname);
      this.invalidateCache();
      this.log(`Unregistered player: ${shortname}`);
    }
  }

  getRegisteredPlayers(): IPlayer[] {
    return Array.from(this.players.values());
  }

  // ==========================================================================
  // Caching
  // ==========================================================================

  /**
   * Compute cache key based on CONTENT, not object identity.
   * Prevents recalculation when streamInfo is a new object with same data.
   */
  private computeCacheKey(streamInfo: StreamInfo, mode: PlaybackMode): string {
    return JSON.stringify({
      sources: streamInfo.source.map((s) => s.type).sort(),
      tracks: streamInfo.meta?.tracks?.map((t) => t.codec).sort() ?? [],
      mode,
      forcePlayer: this.options.forcePlayer,
      forceSource: this.options.forceSource,
      forceType: this.options.forceType,
    });
  }

  /** Invalidate cache (called when player registrations change) */
  invalidateCache(): void {
    this.cachedCombinations = null;
    this.cachedSelection = null;
    this.cacheKey = null;
  }

  /** Get cached selection without recomputing */
  getCurrentSelection(): PlayerSelection | null {
    return this.cachedSelection;
  }

  /** Get cached combinations without recomputing */
  getCachedCombinations(): PlayerCombination[] | null {
    return this.cachedCombinations;
  }

  // ==========================================================================
  // Selection Logic (Single Source of Truth)
  // ==========================================================================

  /**
   * THE single source of truth for player+source scoring.
   * Returns ALL combinations (compatible and incompatible) with scores.
   * Results are cached - won't recompute if source types/tracks haven't changed.
   */
  getAllCombinations(streamInfo: StreamInfo, playbackMode?: PlaybackMode): PlayerCombination[] {
    // Determine effective playback mode
    const explicitMode = playbackMode || this.options.playbackMode;
    const effectiveMode: PlaybackMode =
      explicitMode && explicitMode !== "auto"
        ? explicitMode
        : streamInfo.type === "vod"
          ? "vod"
          : "auto";

    // Check cache
    const key = this.computeCacheKey(streamInfo, effectiveMode);
    if (key === this.cacheKey && this.cachedCombinations) {
      return this.cachedCombinations;
    }

    // Cache miss - compute all combinations
    const combinations = this.computeAllCombinations(streamInfo, effectiveMode);

    // Update cache
    this.cachedCombinations = combinations;
    this.cacheKey = key;

    // Update selection and emit events if changed
    const newSelection = this.pickBestFromCombinations(combinations);
    const selectionChanged = this.hasSelectionChanged(newSelection);

    if (selectionChanged) {
      this.cachedSelection = newSelection;

      // Log only on actual change
      if (this.options.debug && newSelection) {
        const winnerKey = `${newSelection.player}:${newSelection.source?.type}`;
        if (winnerKey !== this.lastLoggedWinner) {
          console.log(
            `[PlayerManager] Selection: ${newSelection.player} + ${newSelection.source?.type} (score: ${newSelection.score.toFixed(3)})`
          );
          this.lastLoggedWinner = winnerKey;
        }
      }

      this.emit("selection-changed", newSelection);
    }

    this.emit("combinations-updated", combinations);
    return combinations;
  }

  /**
   * Select the best player for given stream info.
   * Uses cached combinations - won't recompute if data hasn't changed.
   */
  selectBestPlayer(
    streamInfo: StreamInfo,
    options?: PlayerManagerOptions
  ): PlayerSelection | false {
    // Merge options
    const mergedOptions = { ...this.options, ...options };

    // Special handling for Legacy player - bypass normal selection
    if (mergedOptions.forcePlayer === "mist-legacy" || mergedOptions.forceType === "mist/legacy") {
      const legacyPlayer = this.players.get("mist-legacy");
      if (legacyPlayer && streamInfo.source.length > 0) {
        const firstSource = streamInfo.source[0];
        const legacySource: StreamSource = {
          url: firstSource.url,
          type: "mist/legacy",
          streamName: firstSource.streamName,
          mistPlayerUrl: firstSource.mistPlayerUrl,
        };
        const result: PlayerSelection = {
          score: 0.1,
          player: "mist-legacy",
          source: legacySource,
          source_index: 0,
        };
        this.emit("playerSelected", {
          player: result.player,
          source: result.source,
          score: result.score,
        });
        return result;
      }
    }

    // Get combinations (will use cache if available)
    const combinations = this.getAllCombinations(streamInfo, mergedOptions.playbackMode);

    // Apply force filters
    let filtered = combinations.filter((c) => c.compatible);

    if (mergedOptions.forcePlayer) {
      filtered = filtered.filter((c) => c.player === mergedOptions.forcePlayer);
    }
    if (mergedOptions.forceType) {
      filtered = filtered.filter((c) => c.sourceType === mergedOptions.forceType);
    }
    if (mergedOptions.forceSource !== undefined) {
      filtered = filtered.filter((c) => c.sourceIndex === mergedOptions.forceSource);
    }

    if (filtered.length === 0) {
      this.log("No suitable player found");
      return false;
    }

    const best = filtered[0];
    const result: PlayerSelection = {
      score: best.score,
      player: best.player,
      source: best.source,
      source_index: best.sourceIndex,
    };

    this.emit("playerSelected", {
      player: result.player,
      source: result.source,
      score: result.score,
    });

    return result;
  }

  /**
   * Internal: compute all combinations (no caching)
   */
  private computeAllCombinations(
    streamInfo: StreamInfo,
    effectiveMode: PlaybackMode
  ): PlayerCombination[] {
    const combinations: PlayerCombination[] = [];
    const players = Array.from(this.players.values());
    const maxPriority = Math.max(...players.map((p) => p.capability.priority), 1);

    // Filter blacklisted sources for scoring index calculation
    const selectionSources = streamInfo.source.filter((s) => !isProtocolBlacklisted(s.type));
    const selectionIndexBySource = new Map<StreamSource, number>();
    selectionSources.forEach((s, idx) => selectionIndexBySource.set(s, idx));
    const totalSources = selectionSources.length;

    const requiredTracks: Array<"video" | "audio"> = [];
    if (streamInfo.meta.tracks.some((t) => t.type === "video")) {
      requiredTracks.push("video");
    }
    if (streamInfo.meta.tracks.some((t) => t.type === "audio")) {
      requiredTracks.push("audio");
    }

    // Track seen player+sourceType pairs to avoid duplicates
    const seenPairs = new Set<string>();

    for (const player of players) {
      for (let sourceIndex = 0; sourceIndex < streamInfo.source.length; sourceIndex++) {
        const source = streamInfo.source[sourceIndex];
        const pairKey = `${player.capability.shortname}:${source.type}`;

        // Skip duplicate player+sourceType combinations
        if (seenPairs.has(pairKey)) continue;
        seenPairs.add(pairKey);

        // Blacklisted protocols: show as incompatible
        const sourceListIndex = selectionIndexBySource.get(source);
        if (sourceListIndex === undefined) {
          combinations.push({
            player: player.capability.shortname,
            playerName: player.capability.name,
            source,
            sourceIndex,
            sourceType: source.type,
            score: 0,
            compatible: false,
            incompatibleReason: `Protocol "${source.type}" is blacklisted`,
          });
          continue;
        }

        // Check MIME support
        const mimeSupported = player.isMimeSupported(source.type);
        if (!mimeSupported) {
          combinations.push({
            player: player.capability.shortname,
            playerName: player.capability.name,
            source,
            sourceIndex,
            sourceType: source.type,
            score: 0,
            compatible: false,
            incompatibleReason: `MIME type "${source.type}" not supported`,
          });
          continue;
        }

        // Check browser/codec compatibility
        const tracktypes = player.isBrowserSupported(source.type, source, streamInfo);
        if (!tracktypes) {
          // Codec incompatible - still score for UI display
          const priorityScore = 1 - player.capability.priority / Math.max(maxPriority, 1);
          const sourceScore = 1 - sourceListIndex / Math.max(totalSources - 1, 1);
          const playerScore = scorePlayer(
            ["video", "audio"],
            player.capability.priority,
            sourceListIndex,
            {
              maxPriority,
              totalSources,
              playerShortname: player.capability.shortname,
              mimeType: source.type,
              playbackMode: effectiveMode,
            }
          );

          combinations.push({
            player: player.capability.shortname,
            playerName: player.capability.name,
            source,
            sourceIndex,
            sourceType: source.type,
            score: playerScore.total,
            compatible: false,
            codecIncompatible: true,
            incompatibleReason: "Codec not supported by browser",
            scoreBreakdown: {
              trackScore: 0,
              trackTypes: [],
              priorityScore,
              sourceScore,
              weights: { tracks: 0.5, priority: 0.1, source: 0.05 },
            },
          });
          continue;
        }

        if (Array.isArray(tracktypes) && requiredTracks.length > 0) {
          const missing = requiredTracks.filter((t) => !tracktypes.includes(t));
          if (missing.length > 0) {
            const priorityScore = 1 - player.capability.priority / Math.max(maxPriority, 1);
            const sourceScore = 1 - sourceListIndex / Math.max(totalSources - 1, 1);
            const playerScore = scorePlayer(
              tracktypes,
              player.capability.priority,
              sourceListIndex,
              {
                maxPriority,
                totalSources,
                playerShortname: player.capability.shortname,
                mimeType: source.type,
                playbackMode: effectiveMode,
              }
            );

            combinations.push({
              player: player.capability.shortname,
              playerName: player.capability.name,
              source,
              sourceIndex,
              sourceType: source.type,
              score: playerScore.total,
              compatible: false,
              incompatibleReason: `Missing required tracks: ${missing.join(", ")}`,
              scoreBreakdown: {
                trackScore: 0,
                trackTypes: tracktypes,
                priorityScore,
                sourceScore,
                weights: { tracks: 0.5, priority: 0.1, source: 0.05 },
              },
            });
            continue;
          }
        }

        // Compatible - calculate full score
        const trackScore = Array.isArray(tracktypes)
          ? tracktypes.reduce(
              (sum, t) => sum + ({ video: 2.0, audio: 1.0, subtitle: 0.5 }[t] || 0),
              0
            )
          : 1.9;
        const priorityScore = 1 - player.capability.priority / Math.max(maxPriority, 1);
        const sourceScore = 1 - sourceListIndex / Math.max(totalSources - 1, 1);

        const playerScore = scorePlayer(tracktypes, player.capability.priority, sourceListIndex, {
          maxPriority,
          totalSources,
          playerShortname: player.capability.shortname,
          mimeType: source.type,
          playbackMode: effectiveMode,
        });

        combinations.push({
          player: player.capability.shortname,
          playerName: player.capability.name,
          source,
          sourceIndex,
          sourceType: source.type,
          score: playerScore.total,
          compatible: true,
          scoreBreakdown: {
            trackScore,
            trackTypes: Array.isArray(tracktypes) ? tracktypes : ["video", "audio"],
            priorityScore,
            sourceScore,
            reliabilityScore: playerScore.breakdown?.reliabilityScore ?? 0,
            modeBonus: playerScore.breakdown?.modeBonus ?? 0,
            routingBonus: playerScore.breakdown?.routingBonus ?? 0,
            weights: {
              tracks: 0.5,
              priority: 0.1,
              source: 0.05,
              reliability: 0.1,
              mode: 0.12,
              routing: 0.08,
            },
          },
        });
      }
    }

    // Add Legacy player option
    const legacyPlayer = this.players.get("mist-legacy");
    if (legacyPlayer && streamInfo.source.length > 0) {
      const firstSource = streamInfo.source[0];
      const legacySource: StreamSource = {
        url: firstSource.url,
        type: "mist/legacy",
        streamName: firstSource.streamName,
        mistPlayerUrl: firstSource.mistPlayerUrl,
      };

      combinations.push({
        player: legacyPlayer.capability.shortname,
        playerName: legacyPlayer.capability.name,
        source: legacySource,
        sourceIndex: 0,
        sourceType: "mist/legacy",
        score: 0.1,
        compatible: true,
        scoreBreakdown: {
          trackScore: 2.0,
          trackTypes: ["video", "audio"],
          priorityScore: 0,
          sourceScore: 0,
          weights: { tracks: 0.5, priority: 0.1, source: 0.05 },
        },
      });
    }

    // Sort: compatible first by score descending, then incompatible alphabetically
    return combinations.sort((a, b) => {
      if (a.compatible !== b.compatible) return a.compatible ? -1 : 1;
      if (a.compatible) return b.score - a.score;
      return a.playerName.localeCompare(b.playerName);
    });
  }

  /**
   * Pick best compatible combination
   */
  private pickBestFromCombinations(combinations: PlayerCombination[]): PlayerSelection | null {
    const compatible = combinations.filter((c) => c.compatible);
    if (compatible.length === 0) return null;

    const best = compatible[0];
    return {
      score: best.score,
      player: best.player,
      source: best.source,
      source_index: best.sourceIndex,
    };
  }

  /**
   * Check if selection changed
   */
  private hasSelectionChanged(newSelection: PlayerSelection | null): boolean {
    if (!this.cachedSelection && !newSelection) return false;
    if (!this.cachedSelection || !newSelection) return true;
    return (
      this.cachedSelection.player !== newSelection.player ||
      this.cachedSelection.source?.type !== newSelection.source?.type
    );
  }

  // ==========================================================================
  // Player Initialization
  // ==========================================================================

  private enqueueOp<T>(op: () => Promise<T>): Promise<T> {
    const run = this.opQueue.then(op, op);
    this.opQueue = run.then(
      () => undefined,
      () => undefined
    );
    return run;
  }

  async initializePlayer(
    container: HTMLElement,
    streamInfo: StreamInfo,
    playerOptions: PlayerOptions = {},
    managerOptions?: PlayerManagerOptions
  ): Promise<HTMLVideoElement> {
    this.log("initializePlayer() called");
    return this.enqueueOp(async () => {
      this.log("Inside enqueueOp - starting");
      this.fallbackAttempts = 0;
      this.excludedPlayers.clear();
      this.errorClassifier.reset();

      // Save for fallback (strip force settings - they're one-shot, not for fallback)
      this.lastContainer = container;
      this.lastStreamInfo = streamInfo;
      this.lastPlayerOptions = playerOptions;
      // Keep playback mode (persistent preference) but clear force settings
      this.lastManagerOptions = {
        playbackMode: managerOptions?.playbackMode,
        debug: managerOptions?.debug,
        autoFallback: managerOptions?.autoFallback,
        maxFallbackAttempts: managerOptions?.maxFallbackAttempts,
        // forcePlayer, forceType, forceSource are intentionally NOT saved
        // They are one-shot selections that shouldn't persist through fallback
      };

      return this.tryInitializePlayer(container, streamInfo, playerOptions, managerOptions);
    });
  }

  private async tryInitializePlayer(
    container: HTMLElement,
    streamInfo: StreamInfo,
    playerOptions: PlayerOptions,
    managerOptions?: PlayerManagerOptions,
    excludePlayers: Set<string> = new Set()
  ): Promise<HTMLVideoElement> {
    this.log("tryInitializePlayer() starting");

    // Clean up previous player
    if (this.currentPlayer) {
      this.log("Cleaning up previous player...");
      await Promise.resolve(this.currentPlayer.destroy());
      this.currentPlayer = null;
    }
    container.innerHTML = "";

    // Update classifier with current alternatives count
    const compatibleCombos = this.getAllCombinations(
      streamInfo,
      managerOptions?.playbackMode
    ).filter((c) => c.compatible && !excludePlayers.has(c.player));
    this.errorClassifier.setAlternativesRemaining(Math.max(0, compatibleCombos.length - 1));

    // Filter excluded players
    const availableSources = streamInfo.source.filter((_, index) => {
      if (excludePlayers.size === 0) return true;
      const selection = this.selectBestPlayer(
        { ...streamInfo, source: [streamInfo.source[index]] },
        managerOptions
      );
      return selection && !excludePlayers.has(selection.player);
    });

    if (availableSources.length === 0) {
      this.log("No available sources after filtering");
      const action = this.errorClassifier.classify(ErrorCode.ALL_PROTOCOLS_EXHAUSTED);
      if (action.type === "fatal") {
        throw new Error("No available players after fallback attempts");
      }
      throw new Error("No available players after fallback attempts");
    }

    this.log(`Available sources: ${availableSources.length}`);
    const modifiedStreamInfo = { ...streamInfo, source: availableSources };
    const selection = this.selectBestPlayer(modifiedStreamInfo, managerOptions);

    if (!selection) {
      this.log("No suitable player selected");
      this.errorClassifier.classify(ErrorCode.ALL_PROTOCOLS_EXHAUSTED);
      throw new Error("No suitable player found for stream");
    }

    this.log(`Selected: ${selection.player} for ${selection.source.type}`);
    const player = this.players.get(selection.player);
    if (!player) {
      this.log(`Player ${selection.player} not registered`);
      throw new Error(`Player ${selection.player} not found`);
    }

    this.log(`Calling ${selection.player}.initialize()...`);
    try {
      const videoElement = await player.initialize(
        container,
        selection.source,
        playerOptions,
        streamInfo
      );
      this.log(`${selection.player}.initialize() completed successfully`);
      this.currentPlayer = player;
      this.errorClassifier.reset();
      this.emit("playerInitialized", { player, videoElement });
      return videoElement;
    } catch (error: unknown) {
      return this.handleInitError(
        error,
        selection,
        container,
        streamInfo,
        playerOptions,
        managerOptions,
        excludePlayers
      );
    }
  }

  /**
   * Handle initialization error using ErrorClassifier to determine recovery action.
   */
  private async handleInitError(
    error: unknown,
    selection: PlayerSelection,
    container: HTMLElement,
    streamInfo: StreamInfo,
    playerOptions: PlayerOptions,
    managerOptions: PlayerManagerOptions | undefined,
    excludePlayers: Set<string>
  ): Promise<HTMLVideoElement> {
    const errorCode = ErrorClassifier.mapErrorToCode(
      error instanceof Error ? error : new Error(String(error))
    );
    const action = this.errorClassifier.classify(
      errorCode,
      error instanceof Error ? error : String(error)
    );

    this.log(`Error classified: ${errorCode}, action: ${action.type}`);

    switch (action.type) {
      case "retry": {
        this.log(`Retrying in ${action.delayMs}ms...`);
        await this.delay(action.delayMs);
        return this.tryInitializePlayer(
          container,
          streamInfo,
          playerOptions,
          managerOptions,
          excludePlayers
        );
      }

      case "swap": {
        const maxAttempts = this.options.maxFallbackAttempts || 3;
        if (!this.options.autoFallback || this.fallbackAttempts >= maxAttempts) {
          this.errorClassifier.classify(ErrorCode.ALL_PROTOCOLS_EXHAUSTED);
          throw error;
        }

        this.fallbackAttempts++;
        const previousPlayer = selection.player;
        const previousProtocol = selection.source.type;
        excludePlayers.add(selection.player);

        this.log(
          `Swapping from ${previousPlayer} (attempt ${this.fallbackAttempts}/${maxAttempts})`
        );

        try {
          const result = await this.tryInitializePlayer(
            container,
            streamInfo,
            playerOptions,
            managerOptions,
            excludePlayers
          );

          // Notify classifier and emit toast event for successful swap
          const newPlayer = this.currentPlayer?.capability.shortname || "unknown";
          const newProtocol = this.cachedSelection?.source.type || "unknown";
          this.errorClassifier.notifyProtocolSwap(
            previousPlayer,
            newPlayer,
            previousProtocol,
            newProtocol,
            action.reason
          );

          this.emit("fallbackAttempted", {
            fromPlayer: previousPlayer,
            toPlayer: newPlayer,
          });

          return result;
        } catch (swapError) {
          throw swapError;
        }
      }

      case "fatal":
      default:
        throw error;
    }
  }

  private delay(ms: number): Promise<void> {
    return new Promise((resolve) => setTimeout(resolve, ms));
  }

  // ==========================================================================
  // Fallback Management
  // ==========================================================================

  async tryPlaybackFallback(): Promise<boolean> {
    return this.enqueueOp(async () => {
      if (!this.lastContainer || !this.lastStreamInfo) {
        this.log("Cannot attempt fallback: no previous init params");
        return false;
      }

      const maxAttempts = this.options.maxFallbackAttempts || 3;
      if (this.fallbackAttempts >= maxAttempts) {
        this.log(`Fallback exhausted (${this.fallbackAttempts}/${maxAttempts})`);
        this.errorClassifier.classify(ErrorCode.ALL_PROTOCOLS_EXHAUSTED);
        return false;
      }

      const previousPlayer = this.currentPlayer?.capability.shortname || "unknown";
      const previousProtocol = this.cachedSelection?.source.type || "unknown";

      if (this.currentPlayer) {
        this.excludedPlayers.add(this.currentPlayer.capability.shortname);
        await Promise.resolve(this.currentPlayer.destroy());
        this.currentPlayer = null;
      }

      this.fallbackAttempts++;
      this.lastContainer.innerHTML = "";

      try {
        await this.tryInitializePlayer(
          this.lastContainer,
          this.lastStreamInfo,
          this.lastPlayerOptions,
          this.lastManagerOptions,
          this.excludedPlayers
        );

        const current = this.getCurrentPlayer();
        const newPlayer = current?.capability.shortname || "unknown";
        const newProtocol = this.cachedSelection?.source.type || "unknown";

        this.errorClassifier.notifyProtocolSwap(
          previousPlayer,
          newPlayer,
          previousProtocol,
          newProtocol,
          "Playback fallback"
        );

        this.emit("fallbackAttempted", {
          fromPlayer: previousPlayer,
          toPlayer: newPlayer,
        });

        return true;
      } catch {
        this.log("Playback fallback failed");
        return false;
      }
    });
  }

  /**
   * Report an error from a player for classification and potential recovery.
   * Players should call this instead of emitting errors directly.
   */
  reportError(error: Error | string): RecoveryAction {
    const errorCode = ErrorClassifier.mapErrorToCode(error);
    return this.errorClassifier.classify(errorCode, error);
  }

  /**
   * Report a quality change (for ABR quality drops).
   * UI layer can call this to trigger toast notification.
   */
  reportQualityChange(direction: "up" | "down", reason: string): void {
    this.emit("qualityChanged", { direction, reason });
  }

  /**
   * Get the error classifier for direct access (advanced use).
   */
  getErrorClassifier(): ErrorClassifier {
    return this.errorClassifier;
  }

  getRemainingFallbackAttempts(): number {
    return Math.max(0, (this.options.maxFallbackAttempts || 3) - this.fallbackAttempts);
  }

  canAttemptFallback(): boolean {
    return this.getRemainingFallbackAttempts() > 0 && this.lastStreamInfo !== null;
  }

  getCurrentPlayer(): IPlayer | null {
    return this.currentPlayer;
  }

  // ==========================================================================
  // Browser Capabilities
  // ==========================================================================

  getBrowserCapabilities() {
    const browser = getBrowserInfo();
    const compatibility = getBrowserCompatibility();

    return {
      browser,
      compatibility,
      supportedMimeTypes: this.getSupportedMimeTypes(),
      availablePlayers: this.getAvailablePlayerInfo(),
    };
  }

  private getSupportedMimeTypes(): string[] {
    const mimes = new Set<string>();
    for (const player of this.players.values()) {
      player.capability.mimes.forEach((mime) => mimes.add(mime));
    }
    return Array.from(mimes).sort();
  }

  private getAvailablePlayerInfo() {
    return Array.from(this.players.values())
      .map((player) => ({
        name: player.capability.name,
        shortname: player.capability.shortname,
        priority: player.capability.priority,
        mimes: player.capability.mimes,
      }))
      .sort((a, b) => a.priority - b.priority);
  }

  // ==========================================================================
  // Lifecycle
  // ==========================================================================

  async destroy(): Promise<void> {
    await this.enqueueOp(async () => {
      if (this.currentPlayer) {
        await Promise.resolve(this.currentPlayer.destroy());
        this.currentPlayer = null;
      }
    });
  }

  removeAllListeners(): void {
    this.listeners.clear();
  }

  // ==========================================================================
  // Event System
  // ==========================================================================

  on<K extends keyof PlayerManagerEvents>(
    event: K,
    listener: (data: PlayerManagerEvents[K]) => void
  ): () => void {
    if (!this.listeners.has(event)) {
      this.listeners.set(event, new Set());
    }
    this.listeners.get(event)!.add(listener);

    // Return unsubscribe function
    return () => this.off(event, listener);
  }

  off<K extends keyof PlayerManagerEvents>(
    event: K,
    listener: (data: PlayerManagerEvents[K]) => void
  ): void {
    this.listeners.get(event)?.delete(listener);
  }

  private emit<K extends keyof PlayerManagerEvents>(event: K, data: PlayerManagerEvents[K]): void {
    this.listeners.get(event)?.forEach((listener) => {
      try {
        listener(data);
      } catch (e) {
        console.error(`Error in PlayerManager ${event} listener:`, e);
      }
    });
  }

  // ==========================================================================
  // Logging
  // ==========================================================================

  private log(message: string): void {
    if (this.options.debug) {
      console.log(`[PlayerManager] ${message}`);
    }
  }

  // ==========================================================================
  // Testing
  // ==========================================================================

  async testSource(
    source: StreamSource,
    streamInfo: StreamInfo
  ): Promise<{ canPlay: boolean; players: string[] }> {
    const testStreamInfo = { ...streamInfo, source: [source] };
    const selection = this.selectBestPlayer(testStreamInfo);

    if (!selection) {
      return { canPlay: false, players: [] };
    }

    const capablePlayers: string[] = [];
    for (const player of this.players.values()) {
      if (player.isMimeSupported(source.type)) {
        const browserSupport = player.isBrowserSupported(source.type, source, streamInfo);
        if (browserSupport) {
          capablePlayers.push(player.capability.shortname);
        }
      }
    }

    return { canPlay: true, players: capablePlayers };
  }
}

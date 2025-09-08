/**
 * PlayerManager
 * 
 * Central orchestrator that uses the MistMetaPlayer selection algorithm
 * to choose the best player for a given source and stream info
 */

import { selectPlayer, PlayerSelection, SelectionOptions } from './selector';
import { getBrowserInfo, getBrowserCompatibility } from './detector';
import { IPlayer, StreamSource, StreamInfo, PlayerOptions } from './PlayerInterface';

export interface PlayerManagerOptions {
  /** Force a specific player */
  forcePlayer?: string;
  /** Force a specific source index */
  forceSource?: number;
  /** Force a specific MIME type */
  forceType?: string;
  /** Custom priority functions */
  forcePriority?: SelectionOptions['forcePriority'];
  /** Starting combo for testing */
  startCombo?: SelectionOptions['startCombo'];
  /** Enable debug logging */
  debug?: boolean;
  /** Automatic fallback on player failure */
  autoFallback?: boolean;
  /** Maximum fallback attempts */
  maxFallbackAttempts?: number;
}

export interface PlayerManagerEvents {
  playerSelected: { player: string; source: StreamSource; score: number };
  playerInitialized: { player: IPlayer; videoElement: HTMLVideoElement };
  playerFailed: { player: string; error: string };
  fallbackAttempted: { fromPlayer: string; toPlayer: string };
}

export class PlayerManager {
  private players: Map<string, IPlayer> = new Map();
  private currentPlayer: IPlayer | null = null;
  private listeners: Map<string, Set<Function>> = new Map();
  private fallbackAttempts = 0;
  private options: PlayerManagerOptions;

  constructor(options: PlayerManagerOptions = {}) {
    this.options = {
      debug: false,
      autoFallback: true,
      maxFallbackAttempts: 3,
      ...options
    };
  }

  /**
   * Register a player implementation
   */
  registerPlayer(player: IPlayer): void {
    this.players.set(player.capability.shortname, player);
    this.log(`Registered player: ${player.capability.name}`);
  }

  /**
   * Unregister a player
   */
  unregisterPlayer(shortname: string): void {
    const player = this.players.get(shortname);
    if (player) {
      player.destroy();
      this.players.delete(shortname);
      this.log(`Unregistered player: ${shortname}`);
    }
  }

  /**
   * Get all registered players
   */
  getRegisteredPlayers(): IPlayer[] {
    return Array.from(this.players.values());
  }

  /**
   * Select the best player for given stream info
   */
  selectBestPlayer(streamInfo: StreamInfo, options?: PlayerManagerOptions): PlayerSelection | false {
    const combinedOptions: SelectionOptions = {
      forcePlayer: options?.forcePlayer || this.options.forcePlayer,
      forceSource: options?.forceSource ?? this.options.forceSource,
      forceType: options?.forceType || this.options.forceType,
      forcePriority: options?.forcePriority || this.options.forcePriority,
      startCombo: options?.startCombo || this.options.startCombo
    };

    // Convert our players to the selector format
    const selectorPlayers = Array.from(this.players.values()).map(player => ({
      name: player.capability.name,
      shortname: player.capability.shortname,
      priority: player.capability.priority,
      mimes: player.capability.mimes,
      isMimeSupported: player.isMimeSupported.bind(player),
      isBrowserSupported: player.isBrowserSupported.bind(player)
    }));

    const result = selectPlayer(selectorPlayers, streamInfo, combinedOptions);
    
    if (result) {
      this.log(`Selected player: ${result.player} with score ${result.score} for source ${result.source.url}`);
      this.emit('playerSelected', {
        player: result.player,
        source: result.source,
        score: result.score
      });
    } else {
      this.log('No suitable player found');
    }

    return result;
  }

  /**
   * Initialize the best player for given stream
   */
  async initializePlayer(
    container: HTMLElement,
    streamInfo: StreamInfo,
    playerOptions: PlayerOptions = {},
    managerOptions?: PlayerManagerOptions
  ): Promise<HTMLVideoElement> {
    this.fallbackAttempts = 0;
    return this.tryInitializePlayer(container, streamInfo, playerOptions, managerOptions);
  }

  /**
   * Internal method that handles fallback logic
   */
  private async tryInitializePlayer(
    container: HTMLElement,
    streamInfo: StreamInfo,
    playerOptions: PlayerOptions,
    managerOptions?: PlayerManagerOptions,
    excludePlayers: Set<string> = new Set()
  ): Promise<HTMLVideoElement> {
    
    // Clean up previous player
    if (this.currentPlayer) {
      this.currentPlayer.destroy();
      this.currentPlayer = null;
    }

    // Filter out excluded players for fallback attempts
    const availableSources = streamInfo.source.filter((_, index) => {
      if (excludePlayers.size === 0) return true;
      
      // For fallback, try different sources or different players
      const selection = this.selectBestPlayer({
        ...streamInfo,
        source: [streamInfo.source[index]]
      }, managerOptions);
      
      return selection && !excludePlayers.has(selection.player);
    });

    if (availableSources.length === 0) {
      throw new Error('No available players after fallback attempts');
    }

    const modifiedStreamInfo = { ...streamInfo, source: availableSources };
    const selection = this.selectBestPlayer(modifiedStreamInfo, managerOptions);
    
    if (!selection) {
      throw new Error('No suitable player found for stream');
    }

    const player = this.players.get(selection.player);
    if (!player) {
      throw new Error(`Player ${selection.player} not found`);
    }

    try {
      this.log(`Initializing ${player.capability.name} for ${selection.source.url}`);
      
      const videoElement = await player.initialize(container, selection.source, playerOptions);
      this.currentPlayer = player;
      
      this.emit('playerInitialized', { player, videoElement });
      return videoElement;

    } catch (error: any) {
      const errorMessage = error.message || String(error);
      this.log(`Player ${selection.player} failed: ${errorMessage}`);
      this.emit('playerFailed', { player: selection.player, error: errorMessage });

      // Attempt fallback if enabled
      if (
        this.options.autoFallback && 
        this.fallbackAttempts < (this.options.maxFallbackAttempts || 3)
      ) {
        this.fallbackAttempts++;
        excludePlayers.add(selection.player);
        
        this.log(`Attempting fallback (attempt ${this.fallbackAttempts})`);
        this.emit('fallbackAttempted', { 
          fromPlayer: selection.player, 
          toPlayer: 'auto' 
        });

        return this.tryInitializePlayer(
          container, 
          streamInfo, 
          playerOptions, 
          managerOptions, 
          excludePlayers
        );
      }

      throw error;
    }
  }

  /**
   * Get browser compatibility info
   */
  getBrowserCapabilities() {
    const browser = getBrowserInfo();
    const compatibility = getBrowserCompatibility();
    
    return {
      browser,
      compatibility,
      supportedMimeTypes: this.getSupportedMimeTypes(),
      availablePlayers: this.getAvailablePlayerInfo()
    };
  }

  /**
   * Get all MIME types supported by registered players
   */
  private getSupportedMimeTypes(): string[] {
    const mimes = new Set<string>();
    
    for (const player of this.players.values()) {
      player.capability.mimes.forEach(mime => mimes.add(mime));
    }
    
    return Array.from(mimes).sort();
  }

  /**
   * Get info about available players
   */
  private getAvailablePlayerInfo() {
    return Array.from(this.players.values()).map(player => ({
      name: player.capability.name,
      shortname: player.capability.shortname,
      priority: player.capability.priority,
      mimes: player.capability.mimes
    })).sort((a, b) => a.priority - b.priority);
  }

  /**
   * Destroy current player and clean up
   */
  destroy(): void {
    if (this.currentPlayer) {
      this.currentPlayer.destroy();
      this.currentPlayer = null;
    }
    
    // Destroy all registered players
    for (const player of this.players.values()) {
      player.destroy();
    }
    
    this.players.clear();
    this.listeners.clear();
  }

  /**
   * Get current active player
   */
  getCurrentPlayer(): IPlayer | null {
    return this.currentPlayer;
  }

  /**
   * Event listener management
   */
  on<K extends keyof PlayerManagerEvents>(
    event: K, 
    listener: (data: PlayerManagerEvents[K]) => void
  ): void {
    if (!this.listeners.has(event)) {
      this.listeners.set(event, new Set());
    }
    this.listeners.get(event)!.add(listener);
  }

  off<K extends keyof PlayerManagerEvents>(
    event: K, 
    listener: (data: PlayerManagerEvents[K]) => void
  ): void {
    const eventListeners = this.listeners.get(event);
    if (eventListeners) {
      eventListeners.delete(listener);
    }
  }

  private emit<K extends keyof PlayerManagerEvents>(
    event: K, 
    data: PlayerManagerEvents[K]
  ): void {
    const eventListeners = this.listeners.get(event);
    if (eventListeners) {
      eventListeners.forEach(listener => {
        try {
          listener(data);
        } catch (e) {
          console.error(`Error in PlayerManager ${event} listener:`, e);
        }
      });
    }
  }

  private log(message: string): void {
    if (this.options.debug) {
      console.log(`[PlayerManager] ${message}`);
    }
  }

  /**
   * Test if a specific source can be played
   */
  async testSource(
    source: StreamSource, 
    streamInfo: StreamInfo
  ): Promise<{ canPlay: boolean; players: string[] }> {
    const testStreamInfo = { ...streamInfo, source: [source] };
    const selection = this.selectBestPlayer(testStreamInfo);
    
    if (!selection) {
      return { canPlay: false, players: [] };
    }

    // Get all players that could handle this source
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
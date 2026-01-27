/**
 * Svelte store for player instance context sharing.
 *
 * Port of PlayerContext.tsx React context to Svelte 5 stores.
 */

import { writable, derived, type Readable } from "svelte/store";
import { getContext, setContext } from "svelte";
import {
  globalPlayerManager,
  type StreamInfo,
  type IPlayer,
} from "@livepeer-frameworks/player-core";

// Context key
const PLAYER_CONTEXT_KEY = Symbol("player-context");

export interface PlayerContextState {
  /** Current video element (if available) */
  videoElement: HTMLVideoElement | null;
  /** Current player instance */
  player: IPlayer | null;
  /** Player name */
  playerName: string | null;
  /** Player shortname */
  playerShortname: string | null;
  /** Current source type */
  sourceType: string | null;
  /** Current source URL */
  sourceUrl: string | null;
  /** Whether player is ready */
  isReady: boolean;
  /** Current stream info */
  streamInfo: StreamInfo | null;
}

export interface PlayerContextStore extends Readable<PlayerContextState> {
  setVideoElement: (el: HTMLVideoElement | null) => void;
  setPlayer: (player: IPlayer | null) => void;
  setSource: (type: string | null, url: string | null) => void;
  setStreamInfo: (info: StreamInfo | null) => void;
  setReady: (ready: boolean) => void;
  reset: () => void;
}

const initialState: PlayerContextState = {
  videoElement: null,
  player: null,
  playerName: null,
  playerShortname: null,
  sourceType: null,
  sourceUrl: null,
  isReady: false,
  streamInfo: null,
};

/**
 * Create a player context store for sharing player state across components.
 *
 * @example
 * ```svelte
 * <!-- Parent component -->
 * <script>
 *   import { createPlayerContext, setPlayerContext } from './stores/playerContext';
 *   const playerContext = createPlayerContext();
 *   setPlayerContext(playerContext);
 * </script>
 *
 * <!-- Child component -->
 * <script>
 *   import { getPlayerContext } from './stores/playerContext';
 *   const playerContext = getPlayerContext();
 *   $: videoEl = $playerContext.videoElement;
 * </script>
 * ```
 */
export function createPlayerContext(): PlayerContextStore {
  const store = writable<PlayerContextState>(initialState);

  function setVideoElement(el: HTMLVideoElement | null) {
    store.update((s) => ({ ...s, videoElement: el }));
  }

  function setPlayer(player: IPlayer | null) {
    store.update((s) => ({
      ...s,
      player,
      playerName: player?.capability.name ?? null,
      playerShortname: player?.capability.shortname ?? null,
    }));
  }

  function setSource(type: string | null, url: string | null) {
    store.update((s) => ({ ...s, sourceType: type, sourceUrl: url }));
  }

  function setStreamInfo(info: StreamInfo | null) {
    store.update((s) => ({ ...s, streamInfo: info }));
  }

  function setReady(ready: boolean) {
    store.update((s) => ({ ...s, isReady: ready }));
  }

  function reset() {
    store.set(initialState);
  }

  return {
    subscribe: store.subscribe,
    setVideoElement,
    setPlayer,
    setSource,
    setStreamInfo,
    setReady,
    reset,
  };
}

/**
 * Set player context in Svelte context (call in parent component)
 */
export function setPlayerContextInComponent(context: PlayerContextStore) {
  setContext(PLAYER_CONTEXT_KEY, context);
}

/**
 * Get player context from Svelte context (call in child components)
 */
export function getPlayerContextFromComponent(): PlayerContextStore | undefined {
  return getContext<PlayerContextStore>(PLAYER_CONTEXT_KEY);
}

/**
 * Get player context or create a fallback that uses globalPlayerManager
 */
export function getPlayerContextOrFallback(): PlayerContextStore {
  const context = getContext<PlayerContextStore>(PLAYER_CONTEXT_KEY);
  if (context) return context;

  // Fallback: create a derived context from globalPlayerManager
  return createFallbackContext();
}

/**
 * Create a fallback context that reads from globalPlayerManager
 * Uses event-driven updates instead of polling
 */
function createFallbackContext(): PlayerContextStore {
  const store = writable<PlayerContextState>(initialState);

  // Sync state from globalPlayerManager
  function syncState() {
    const player = globalPlayerManager.getCurrentPlayer();
    const videoEl = player?.getVideoElement() ?? null;

    store.update((s) => ({
      ...s,
      videoElement: videoEl,
      player: player ?? null,
      playerName: player?.capability.name ?? null,
      playerShortname: player?.capability.shortname ?? null,
      isReady: !!videoEl,
    }));
  }

  // Event handlers
  const handleInitialized = () => syncState();
  const handleSelectionChanged = () => syncState();

  // Start/stop event listeners based on subscriber count
  const originalSubscribe = store.subscribe;
  let subscribers = 0;

  const subscribe: typeof originalSubscribe = (run, invalidate) => {
    subscribers++;
    if (subscribers === 1) {
      // Initial sync
      syncState();
      // Subscribe to events
      globalPlayerManager.on("playerInitialized", handleInitialized);
      globalPlayerManager.on("selection-changed", handleSelectionChanged);
    }

    const unsubscribe = originalSubscribe(run, invalidate);

    return () => {
      unsubscribe();
      subscribers--;
      if (subscribers === 0) {
        // Unsubscribe from events
        globalPlayerManager.off("playerInitialized", handleInitialized);
        globalPlayerManager.off("selection-changed", handleSelectionChanged);
      }
    };
  };

  return {
    subscribe,
    setVideoElement: () => {},
    setPlayer: () => {},
    setSource: () => {},
    setStreamInfo: () => {},
    setReady: () => {},
    reset: () => {},
  };
}

// Convenience derived stores
export function createDerivedVideoElement(store: PlayerContextStore) {
  return derived(store, ($state) => $state.videoElement);
}

export function createDerivedIsReady(store: PlayerContextStore) {
  return derived(store, ($state) => $state.isReady);
}

export function createDerivedPlayerInfo(store: PlayerContextStore) {
  return derived(store, ($state) => ({
    name: $state.playerName,
    shortname: $state.playerShortname,
  }));
}

export default createPlayerContext;

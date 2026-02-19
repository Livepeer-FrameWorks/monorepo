/**
 * ReactiveState — Per-property reactive subscriptions for the vanilla player.
 *
 * Usage:
 * ```ts
 * const unsub = player.state.on('currentTime', (value) => {
 *   console.log('Time:', value);
 * });
 * // Fires immediately with current value, then on every change.
 * unsub(); // Unsubscribe
 * ```
 */

import type { PlayerController, PlayerControllerEvents } from "../core/PlayerController";

export type ReactiveStateProperty =
  | "paused"
  | "playing"
  | "currentTime"
  | "duration"
  | "volume"
  | "muted"
  | "playbackRate"
  | "loop"
  | "buffering"
  | "fullscreen"
  | "pip"
  | "tracks"
  | "streamState"
  | "error"
  | "loading"
  | "ended"
  | "seeking";

export interface ReactiveState {
  /** Subscribe to a property. Fires immediately with current value, then on every change. Returns unsubscribe function. */
  on(prop: ReactiveStateProperty, cb: (value: unknown) => void): () => void;
  /** Get current value of a property */
  get(prop: ReactiveStateProperty): unknown;
  /** Unsubscribe all listeners for a property, or all if no prop given */
  off(prop?: ReactiveStateProperty): void;
}

type Getter = (ctrl: PlayerController) => unknown;

interface PropMapping {
  events: (keyof PlayerControllerEvents)[];
  getter: Getter;
}

const PROP_MAP: Record<ReactiveStateProperty, PropMapping> = {
  paused: {
    events: ["stateChange"],
    getter: (c) => c.isPaused(),
  },
  playing: {
    events: ["stateChange"],
    getter: (c) => c.isPlaying(),
  },
  currentTime: {
    events: ["timeUpdate"],
    getter: (c) => c.getCurrentTime(),
  },
  duration: {
    events: ["timeUpdate"],
    getter: (c) => c.getDuration(),
  },
  volume: {
    events: ["volumeChange"],
    getter: (c) => c.getVolume(),
  },
  muted: {
    events: ["volumeChange"],
    getter: (c) => c.isMuted(),
  },
  playbackRate: {
    events: ["stateChange"],
    getter: (c) => c.getPlaybackRate(),
  },
  loop: {
    events: ["loopChange"],
    getter: (c) => c.isLoopEnabled(),
  },
  buffering: {
    events: ["stateChange"],
    getter: (c) => c.isBuffering(),
  },
  fullscreen: {
    events: ["fullscreenChange"],
    getter: (c) => c.isFullscreen(),
  },
  pip: {
    events: ["pipChange"],
    getter: (c) => c.isPiPActive(),
  },
  tracks: {
    events: ["ready", "playerSelected"],
    getter: (c) => c.getTracks(),
  },
  streamState: {
    events: ["streamStateChange"],
    getter: (c) => c.getStreamState(),
  },
  error: {
    events: ["error", "errorCleared"],
    getter: (c) => c.getError(),
  },
  loading: {
    events: ["stateChange"],
    getter: (c) =>
      c.getState() === "booting" ||
      c.getState() === "gateway_loading" ||
      c.getState() === "connecting" ||
      c.getState() === "selecting_player",
  },
  ended: {
    events: ["stateChange"],
    getter: (c) => c.getState() === "ended",
  },
  seeking: {
    events: ["timeUpdate"],
    getter: (c) => c.getVideoElement()?.seeking ?? false,
  },
};

export function createReactiveState(ctrl: PlayerController): ReactiveState {
  const listeners = new Map<ReactiveStateProperty, Set<(value: unknown) => void>>();
  const controllerUnsubs = new Map<string, () => void>();
  const lastValues = new Map<ReactiveStateProperty, unknown>();

  function getValue(prop: ReactiveStateProperty): unknown {
    const mapping = PROP_MAP[prop];
    if (!mapping) return undefined;
    return mapping.getter(ctrl);
  }

  function notify(prop: ReactiveStateProperty) {
    const subs = listeners.get(prop);
    if (!subs || subs.size === 0) return;
    const value = getValue(prop);
    const last = lastValues.get(prop);
    if (value === last) return;
    lastValues.set(prop, value);
    for (const cb of subs) {
      try {
        cb(value);
      } catch {
        /* subscriber error */
      }
    }
  }

  function ensureEventSubscription(eventName: keyof PlayerControllerEvents) {
    const key = eventName as string;
    if (controllerUnsubs.has(key)) return;
    const unsub = ctrl.on(eventName, () => {
      for (const [prop, mapping] of Object.entries(PROP_MAP)) {
        if (mapping.events.includes(eventName)) {
          notify(prop as ReactiveStateProperty);
        }
      }
    });
    controllerUnsubs.set(key, unsub);
  }

  return {
    on(prop: ReactiveStateProperty, cb: (value: unknown) => void): () => void {
      const mapping = PROP_MAP[prop];
      if (!mapping) return () => {};

      if (!listeners.has(prop)) {
        listeners.set(prop, new Set());
      }
      listeners.get(prop)!.add(cb);

      // Subscribe to controller events
      for (const evt of mapping.events) {
        ensureEventSubscription(evt);
      }

      // Fire immediately with current value
      const value = getValue(prop);
      lastValues.set(prop, value);
      try {
        cb(value);
      } catch {
        /* subscriber error */
      }

      return () => {
        listeners.get(prop)?.delete(cb);
      };
    },

    get(prop: ReactiveStateProperty): unknown {
      return getValue(prop);
    },

    off(prop?: ReactiveStateProperty) {
      if (prop) {
        listeners.delete(prop);
      } else {
        listeners.clear();
        for (const unsub of controllerUnsubs.values()) {
          unsub();
        }
        controllerUnsubs.clear();
        lastValues.clear();
      }
    },
  };
}

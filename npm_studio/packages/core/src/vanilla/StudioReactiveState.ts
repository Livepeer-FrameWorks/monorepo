/**
 * StudioReactiveState — per-property reactive subscriptions for createStreamCrafter().
 *
 * Subscribers receive the current value immediately upon subscribing, then on every change.
 * Shallow equality check prevents redundant notifications.
 *
 * @example
 * ```ts
 * const studio = createStreamCrafter({ ... });
 * const unsub = studio.state.on('streaming', (isLive) => {
 *   goLiveBtn.textContent = isLive ? 'Stop' : 'Go Live';
 * });
 * ```
 */

import type { IngestControllerV2 } from "../core/IngestControllerV2";
import type { IngestState, IngestStateContextV2, MediaSource, QualityProfile } from "../types";

// ---------------------------------------------------------------------------
// Public types
// ---------------------------------------------------------------------------

export interface StudioReactiveState {
  /** Subscribe to a property. Fires immediately with current value, then on change. */
  on<K extends keyof StudioStateMap>(prop: K, cb: (value: StudioStateMap[K]) => void): () => void;
  /** Read the current value of a property. */
  get<K extends keyof StudioStateMap>(prop: K): StudioStateMap[K];
  /** Stop all subscriptions and event listeners. */
  destroy(): void;
}

export interface StudioStateMap {
  state: IngestState;
  stateContext: IngestStateContextV2;
  streaming: boolean;
  capturing: boolean;
  reconnecting: boolean;
  sources: MediaSource[];
  primaryVideo: MediaSource | null;
  masterVolume: number;
  profile: QualityProfile;
  error: { error: string; recoverable: boolean } | null;
  compositorEnabled: boolean;
  webCodecsActive: boolean;
  recording: boolean;
  codecFamily: string;
  currentBitrate: number | null;
  congestionLevel: string | null;
}

// ---------------------------------------------------------------------------
// Property descriptors — maps each property to its getter and the controller
// events that may change it.
// ---------------------------------------------------------------------------

type Getter<V> = (ctrl: IngestControllerV2) => V;

interface PropDescriptor<V> {
  getter: Getter<V>;
  events: string[];
}

const PROP_DESCRIPTORS: { [K in keyof StudioStateMap]: PropDescriptor<StudioStateMap[K]> } = {
  state: {
    getter: (c) => c.getState(),
    events: ["stateChange"],
  },
  stateContext: {
    getter: (c) => c.getStateContext(),
    events: ["stateChange"],
  },
  streaming: {
    getter: (c) => c.isStreaming(),
    events: ["stateChange"],
  },
  capturing: {
    getter: (c) => c.isCapturing(),
    events: ["stateChange"],
  },
  reconnecting: {
    getter: (c) => c.isReconnecting(),
    events: ["stateChange", "reconnectionAttempt", "reconnectionSuccess", "reconnectionFailed"],
  },
  sources: {
    getter: (c) => c.getSources(),
    events: ["sourceAdded", "sourceRemoved", "sourceUpdated"],
  },
  primaryVideo: {
    getter: (c) => c.getPrimaryVideoSource(),
    events: ["sourceAdded", "sourceRemoved", "sourceUpdated"],
  },
  masterVolume: {
    getter: (c) => c.getMasterVolume(),
    events: ["stateChange"],
  },
  profile: {
    getter: (c) => c.getQualityProfile(),
    events: ["qualityChanged"],
  },
  error: {
    getter: (c) => {
      const ctx = c.getStateContext();
      if (ctx.error) return { error: ctx.error, recoverable: c.getState() !== "error" };
      return null;
    },
    events: ["error", "stateChange"],
  },
  compositorEnabled: {
    getter: (c) => c.isCompositorEnabled(),
    events: ["stateChange"],
  },
  webCodecsActive: {
    getter: (c) => c.isWebCodecsActive(),
    events: ["webCodecsActive"],
  },
  recording: {
    getter: (c) => c.isRecordingActive(),
    events: ["recordingStarted", "recordingStopped", "recordingPaused", "recordingResumed"],
  },
  codecFamily: {
    getter: (c) => c.getVideoCodecFamily(),
    events: ["webCodecsActive"],
  },
  currentBitrate: {
    getter: (c) => c.getCurrentBitrate(),
    events: ["bitrateChanged"],
  },
  congestionLevel: {
    getter: (c) => c.getCongestionLevel(),
    events: ["congestionChanged"],
  },
};

// ---------------------------------------------------------------------------
// Shallow equality — handles primitives, null, and flat arrays/objects
// ---------------------------------------------------------------------------

function shallowEqual(a: unknown, b: unknown): boolean {
  if (a === b) return true;
  if (a == null || b == null) return false;
  if (Array.isArray(a) && Array.isArray(b)) {
    if (a.length !== b.length) return false;
    for (let i = 0; i < a.length; i++) {
      if (a[i] !== b[i]) return false;
    }
    return true;
  }
  if (typeof a === "object" && typeof b === "object") {
    const ka = Object.keys(a as Record<string, unknown>);
    const kb = Object.keys(b as Record<string, unknown>);
    if (ka.length !== kb.length) return false;
    for (const k of ka) {
      if ((a as any)[k] !== (b as any)[k]) return false;
    }
    return true;
  }
  return false;
}

// ---------------------------------------------------------------------------
// Factory
// ---------------------------------------------------------------------------

export function createStudioReactiveState(ctrl: IngestControllerV2): StudioReactiveState {
  const subs = new Map<string, Set<(value: any) => void>>();
  const cache = new Map<string, unknown>();
  const teardowns: (() => void)[] = [];

  // Deduplicate event subscriptions — one handler per controller event,
  // shared across all properties that depend on it.
  const eventToPropKeys = new Map<string, Set<keyof StudioStateMap>>();
  for (const [prop, desc] of Object.entries(PROP_DESCRIPTORS) as [
    keyof StudioStateMap,
    PropDescriptor<any>,
  ][]) {
    for (const evt of desc.events) {
      let set = eventToPropKeys.get(evt);
      if (!set) {
        set = new Set();
        eventToPropKeys.set(evt, set);
      }
      set.add(prop);
    }
  }

  for (const [evt, propKeys] of eventToPropKeys) {
    const handler = () => {
      for (const prop of propKeys) {
        const listeners = subs.get(prop);
        if (!listeners || listeners.size === 0) continue;
        const desc = PROP_DESCRIPTORS[prop];
        const newVal = desc.getter(ctrl);
        const prev = cache.get(prop);
        if (shallowEqual(prev, newVal)) continue;
        cache.set(prop, newVal);
        for (const cb of listeners) {
          try {
            cb(newVal);
          } catch {
            /* subscriber errors shouldn't break others */
          }
        }
      }
    };
    const unsub = ctrl.on(evt as any, handler);
    teardowns.push(unsub);
  }

  return {
    on<K extends keyof StudioStateMap>(
      prop: K,
      cb: (value: StudioStateMap[K]) => void
    ): () => void {
      let set = subs.get(prop);
      if (!set) {
        set = new Set();
        subs.set(prop, set);
      }
      set.add(cb as any);

      // Fire immediately with current value
      const desc = PROP_DESCRIPTORS[prop];
      if (desc) {
        const current = desc.getter(ctrl);
        cache.set(prop, current);
        try {
          cb(current);
        } catch {
          /* ignore */
        }
      }

      return () => {
        set!.delete(cb as any);
      };
    },

    get<K extends keyof StudioStateMap>(prop: K): StudioStateMap[K] {
      const desc = PROP_DESCRIPTORS[prop];
      return desc ? desc.getter(ctrl) : (undefined as any);
    },

    destroy() {
      for (const fn of teardowns) fn();
      teardowns.length = 0;
      subs.clear();
      cache.clear();
    },
  };
}

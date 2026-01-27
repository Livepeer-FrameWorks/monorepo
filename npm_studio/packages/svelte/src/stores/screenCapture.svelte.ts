/**
 * Screen Capture Store
 * Svelte 5 store for screen capture
 */

import { ScreenCapture, type ScreenCaptureOptions } from "@livepeer-frameworks/streamcrafter-core";

export interface ScreenCaptureState {
  stream: MediaStream | null;
  isActive: boolean;
  hasAudio: boolean;
  error: string | null;
}

export interface ScreenCaptureStore {
  subscribe: (fn: (state: ScreenCaptureState) => void) => () => void;
  start: (options?: ScreenCaptureOptions) => Promise<MediaStream | null>;
  stop: () => void;
  destroy: () => void;
}

export function createScreenCaptureStore(): ScreenCaptureStore {
  let state = $state<ScreenCaptureState>({
    stream: null,
    isActive: false,
    hasAudio: false,
    error: null,
  });

  const subscribers = new Set<(state: ScreenCaptureState) => void>();
  let screenCapture: ScreenCapture | null = null;

  function notify() {
    const snapshot = { ...state };
    subscribers.forEach((fn) => fn(snapshot));
  }

  function init() {
    if (screenCapture) return;

    screenCapture = new ScreenCapture();

    screenCapture.on("started", (event) => {
      state.stream = event.stream;
      state.isActive = true;
      state.hasAudio = event.stream.getAudioTracks().length > 0;
      notify();
    });

    screenCapture.on("ended", () => {
      state.stream = null;
      state.isActive = false;
      state.hasAudio = false;
      notify();
    });

    screenCapture.on("error", (event) => {
      state.error = event.message;
      notify();
    });
  }

  return {
    subscribe(fn) {
      init();
      subscribers.add(fn);
      fn({ ...state });
      return () => {
        subscribers.delete(fn);
      };
    },

    async start(options?: ScreenCaptureOptions) {
      if (!screenCapture) {
        init();
      }
      state.error = null;
      notify();
      try {
        return await screenCapture!.start(options);
      } catch (err) {
        state.error = err instanceof Error ? err.message : String(err);
        notify();
        return null;
      }
    },

    stop() {
      screenCapture?.stop();
    },

    destroy() {
      screenCapture?.destroy();
      screenCapture = null;
      subscribers.clear();
    },
  };
}

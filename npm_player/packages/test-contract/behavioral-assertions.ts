/**
 * Behavioral test helpers for wrapper parity.
 * Each wrapper test suite imports these and executes against its adapter.
 */
import {
  WRAPPER_PARITY_INITIAL_STATE,
  WRAPPER_PARITY_EVENT_NAMES,
  WRAPPER_PARITY_ACTION_METHODS,
} from "./player-wrapper-contract";

/** Expected payload shapes for key events (used for structural assertions) */
export const WRAPPER_PARITY_EVENT_PAYLOAD_SHAPES = {
  timeUpdate: { currentTime: "number", duration: "number" },
  volumeChange: { volume: "number", muted: "boolean" },
  stateChange: { state: "string" },
  error: { message: "string" },
} as const;

export interface WrapperAdapter {
  getState: () => Record<string, unknown>;
  getActionMethods: () => string[];
  getSubscribableEvents: () => string[];
}

/** Assert initial state matches the parity contract */
export function assertInitialState(adapter: WrapperAdapter): void {
  const state = adapter.getState();
  for (const [key, expected] of Object.entries(WRAPPER_PARITY_INITIAL_STATE)) {
    if (key in state) {
      if (expected === null) {
        if (state[key] !== null && state[key] !== undefined) {
          throw new Error(
            `Initial state "${key}": expected null/undefined, got ${JSON.stringify(state[key])}`
          );
        }
      } else if (typeof expected === "number" && isNaN(expected as number)) {
        if (!isNaN(state[key] as number)) {
          throw new Error(`Initial state "${key}": expected NaN, got ${state[key]}`);
        }
      } else if (state[key] !== expected) {
        throw new Error(
          `Initial state "${key}": expected ${JSON.stringify(expected)}, got ${JSON.stringify(state[key])}`
        );
      }
    }
  }
}

/** Assert all required action methods exist */
export function assertActionMethods(adapter: WrapperAdapter): void {
  const methods = adapter.getActionMethods();
  for (const method of WRAPPER_PARITY_ACTION_METHODS) {
    if (!methods.includes(method)) {
      throw new Error(`Missing action method: "${method}"`);
    }
  }
}

/** Assert all required events are subscribable */
export function assertSubscribableEvents(adapter: WrapperAdapter): void {
  const events = adapter.getSubscribableEvents();
  for (const event of WRAPPER_PARITY_EVENT_NAMES) {
    if (!events.includes(event)) {
      throw new Error(`Missing subscribable event: "${event}"`);
    }
  }
}

/** Assert time values use milliseconds (not seconds) */
export function assertMillisecondUnits(
  seekFn: (ms: number) => void,
  getVideoCurrentTime: () => number,
  getReportedCurrentTime: () => number
): void {
  seekFn(10_000); // 10 seconds in ms
  const videoTime = getVideoCurrentTime(); // should be ~10 (seconds, raw API)
  const reportedTime = getReportedCurrentTime(); // should be ~10000 (ms)
  if (reportedTime < 1000 && videoTime > 1) {
    throw new Error(
      `Time unit mismatch: video.currentTime=${videoTime}, reported=${reportedTime}. ` +
        "Expected reported time in milliseconds."
    );
  }
}

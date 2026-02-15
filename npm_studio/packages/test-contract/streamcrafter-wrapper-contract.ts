export const STREAMCRAFTER_WRAPPER_DEFAULT_QUALITY_PROFILE = "broadcast" as const;

export const STREAMCRAFTER_WRAPPER_CONTROLLER_NOT_INITIALIZED_ERROR =
  "Controller not initialized" as const;

export const STREAMCRAFTER_WRAPPER_PARITY_EVENT_NAMES = [
  "stateChange",
  "statsUpdate",
  "error",
  "sourceAdded",
  "sourceRemoved",
  "sourceUpdated",
  "qualityChanged",
  "reconnectionAttempt",
  "webCodecsActive",
] as const;

export const STREAMCRAFTER_WRAPPER_PARITY_INITIAL_STATE = {
  state: "idle",
  stateContext: {},
  isStreaming: false,
  isCapturing: false,
  isReconnecting: false,
  error: null,
  mediaStream: null,
  sources: [],
  qualityProfile: STREAMCRAFTER_WRAPPER_DEFAULT_QUALITY_PROFILE,
  reconnectionState: null,
  stats: null,
  useWebCodecs: false,
  isWebCodecsActive: false,
  encoderStats: null,
} as const;

export const STREAMCRAFTER_WRAPPER_PARITY_INITIAL_STATE_REACT_EXT = {
  isWebCodecsAvailable: false,
} as const;

export const STREAMCRAFTER_WRAPPER_PARITY_INITIAL_STATE_WC_EXT = {
  isWebCodecsAvailable: false,
  audioLevel: 0,
  peakAudioLevel: 0,
} as const;

export const STREAMCRAFTER_WRAPPER_PARITY_STATE_CHANGE_CASES = [
  {
    state: "streaming",
    context: {},
    expected: { isStreaming: true, isCapturing: true, isReconnecting: false },
  },
  {
    state: "capturing",
    context: {},
    expected: { isStreaming: false, isCapturing: true, isReconnecting: false },
  },
  {
    state: "reconnecting",
    context: { reconnection: { attempt: 2, maxAttempts: 5 } },
    expected: { isStreaming: false, isCapturing: false, isReconnecting: true },
  },
] as const;

export const STREAMCRAFTER_WRAPPER_PARITY_ACTION_METHODS = [
  "startCamera",
  "startScreenShare",
  "addCustomSource",
  "removeSource",
  "stopCapture",
  "setSourceVolume",
  "setSourceMuted",
  "setSourceActive",
  "setPrimaryVideoSource",
  "setMasterVolume",
  "getMasterVolume",
  "setQualityProfile",
  "startStreaming",
  "stopStreaming",
  "getDevices",
  "switchVideoDevice",
  "switchAudioDevice",
  "getStats",
  "setUseWebCodecs",
  "setEncoderOverrides",
  "getController",
] as const;

export const STREAMCRAFTER_COMPONENT_PARITY_CONTEXT_MENU_LABELS = {
  copyWhipUrl: "Copy WHIP URL",
  copyStreamInfo: "Copy Stream Info",
  advanced: "Advanced",
  hideAdvanced: "Hide Advanced",
} as const;

export const STREAMCRAFTER_COMPONENT_PARITY_CALLBACK_MARKERS = {
  onStateChange: "onStateChange",
  onError: "onError",
} as const;

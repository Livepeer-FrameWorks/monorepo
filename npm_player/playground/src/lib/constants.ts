// Storage keys
export const STORAGE_KEYS = {
  baseUrl: "fw.player.playground.baseUrl",
  viewerPath: "fw.player.playground.viewerPath",
  streamName: "fw.player.playground.streamName",
  thumbnailUrl: "fw.player.playground.thumbnailUrl"
} as const;

// Default configuration for local MistServer
export const DEFAULTS = {
  baseUrl: "http://localhost:8080",
  viewerPath: "",
  streamName: "live",
  thumbnailUrl: "",
  autoplayMuted: true
} as const;

// Standard MistServer ports
export const MIST_PORTS = {
  http: 8080,
  rtmp: 1935,
  srt: 9000
} as const;

// Polling intervals
export const POLL_INTERVAL_MS = 5000;
export const COPY_FEEDBACK_DURATION_MS = 1500;

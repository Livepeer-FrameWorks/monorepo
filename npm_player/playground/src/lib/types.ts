import type { ContentEndpoints } from "@livepeer-frameworks/player-core";

// Re-export for convenience
export type { ContentEndpoints };

// Ingest endpoint types (generated client-side)
export type IngestEndpointId = "whip" | "rtmp" | "srt";

export type IngestUris = {
  rtmp: string;
  srt: string;
  whip: string;
};

// MistServer JSON response types (polled from server)
export type MistSource = {
  url: string;
  type: string;
  hrn: string; // Human-readable name
  priority: number;
  simul_tracks?: number;
  total_matches?: number;
};

export type MistTrack = {
  codec: string;
  type: string;
  bps?: number;
  channels?: number;
  rate?: number;
  trackid?: number;
  width?: number;
  height?: number;
  fpks?: number;
};

export type MistJsonResponse = {
  source: MistSource[];
  width?: number;
  height?: number;
  type?: string;
  tracks?: Record<string, MistTrack>;
  error?: string;
};

// Available player implementations
export type PlayerType = "auto" | "direct" | "hlsjs" | "dashjs" | "videojs" | "mist";

// Playground state types
export type PlaygroundState = {
  // Connection config
  baseUrl: string;
  viewerPath: string;
  streamName: string;

  // Derived
  viewerBase: string;
  host: string;
  ingestUris: IngestUris;

  // Polled playback
  playbackSources: MistSource[];
  playbackLoading: boolean;
  playbackError: string | null;

  // Player config
  thumbnailUrl: string;
  autoplayMuted: boolean;

  // Protocol/Player selection
  selectedProtocol: string | null; // null = auto-select
  selectedPlayer: PlayerType;
};

export type PlaygroundActions = {
  setBaseUrl: (url: string) => void;
  setViewerPath: (path: string) => void;
  setStreamName: (name: string) => void;
  setThumbnailUrl: (url: string) => void;
  setAutoplayMuted: (muted: boolean) => void;
  pollSources: () => void;
  setSelectedProtocol: (protocol: string | null) => void;
  setSelectedPlayer: (player: PlayerType) => void;
};

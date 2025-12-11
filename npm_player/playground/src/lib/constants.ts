import type { MistSettings, MistEndpointDefinition, MistEndpointId, MockStream } from "./types";

export const STORAGE_KEYS = {
  theme: "fw.player.playground.theme",
  networkOptIn: "fw.player.playground.network"
} as const;

export const MIST_STORAGE_KEYS = {
  settings: "fw.player.playground.mist.settings",
  overrides: "fw.player.playground.mist.overrides",
  profiles: "fw.player.playground.mist.profiles",
  selectedProfile: "fw.player.playground.mist.selectedProfile"
} as const;

export const DEFAULT_MIST_SETTINGS: MistSettings = {
  baseUrl: "https://mist.dev.local",
  viewerPath: "/",
  streamName: "demo-stream",
  label: "Local Mist"
};

export const MIST_ENDPOINTS: MistEndpointDefinition[] = [
  {
    id: "whip",
    label: "WHIP ingest",
    category: "ingest",
    hint: "Use for browser capture or tools that publish via WebRTC ingest.",
    build: ({ apiBase, streamName, authToken }) => {
      const base = `${apiBase}/whip/${streamName}`;
      return authToken ? `${base}?token=${authToken}` : base;
    }
  },
  {
    id: "rtmp",
    label: "RTMP ingest",
    category: "ingest",
    hint: "OBS / FFmpeg; append ?token= if your Mist node expects auth.",
    build: ({ host, ingestApp, streamName, authToken }) => {
      const app = ingestApp ? ingestApp.replace(/^\//, "") : "live";
      const url = `rtmp://${host ?? "localhost"}/${app}/${streamName}`;
      return authToken ? `${url}?token=${authToken}` : url;
    }
  },
  {
    id: "srt",
    label: "SRT ingest",
    category: "ingest",
    hint: "Use with FFmpeg SRT caller mode.",
    build: ({ host, streamName, authToken }) => {
      const base = `srt://${host ?? "localhost"}:9000?streamid=#!::r=${streamName}`;
      return authToken ? `${base},token=${authToken}` : base;
    }
  },
  {
    id: "mistHtml",
    label: "Mist HTML player",
    category: "playback",
    hint: "Embed-safe HTML wrapper; ideal for copying into the APP for manual QA.",
    build: ({ viewerBase, streamName, authToken }) => {
      const base = `${viewerBase}/view/${streamName}.html`;
      return authToken ? `${base}?token=${authToken}` : base;
    }
  },
  {
    id: "playerJs",
    label: "player.js script",
    category: "playback",
    hint: "Use if you need raw Mist player JS for custom embeds.",
    build: ({ viewerBase, streamName, authToken }) => {
      const base = `${viewerBase}/player.js?stream=${streamName}`;
      return authToken ? `${base}&token=${authToken}` : base;
    }
  },
  {
    id: "whep",
    label: "WHEP playback",
    category: "playback",
    hint: "Feeds the upgraded player's WebRTC path.",
    build: ({ apiBase, streamName, authToken }) => {
      const base = `${apiBase}/whep/${streamName}`;
      return authToken ? `${base}?token=${authToken}` : base;
    }
  },
  {
    id: "hls",
    label: "HLS manifest",
    category: "playback",
    hint: "Good for simple HTML5 testing or proxying through CDN.",
    build: ({ viewerBase, streamName, authToken }) => {
      const base = `${viewerBase}/hls/${streamName}/index.m3u8`;
      return authToken ? `${base}?token=${authToken}` : base;
    }
  },
  {
    id: "dash",
    label: "MPEG-DASH manifest",
    category: "playback",
    hint: "For players that prefer DASH (Shaka, Exo).",
    build: ({ viewerBase, streamName, authToken }) => {
      const base = `${viewerBase}/dash/${streamName}/manifest.mpd`;
      return authToken ? `${base}?token=${authToken}` : base;
    }
  },
  {
    id: "mp4",
    label: "Progressive MP4",
    category: "playback",
    hint: "Great for quick sanity checks on recorded VOD clips.",
    build: ({ viewerBase, streamName, authToken }) => {
      const base = `${viewerBase}/vod/${streamName}.mp4`;
      return authToken ? `${base}?token=${authToken}` : base;
    }
  }
];

export const MIST_ENDPOINTS_BY_ID = MIST_ENDPOINTS.reduce<Record<MistEndpointId, MistEndpointDefinition>>((acc, def) => {
  acc[def.id] = def;
  return acc;
}, {} as Record<MistEndpointId, MistEndpointDefinition>);

export const MOCK_STREAMS: MockStream[] = [
  {
    id: "mux-hls",
    label: "Mux Test HLS",
    description: "Public x36xhzz multi-bitrate HLS demo feed served by Mux.",
    contentId: "mux-demo-hls",
    contentType: "live",
    endpoints: {
      primary: {
        nodeId: "mock-mux-hls",
        protocol: "HLS",
        url: "https://test-streams.mux.dev/x36xhzz/x36xhzz.m3u8",
        outputs: {
          HLS: {
            protocol: "HLS",
            url: "https://test-streams.mux.dev/x36xhzz/x36xhzz.m3u8",
            capabilities: {
              supportsSeek: true,
              supportsQualitySwitch: true,
              hasAudio: true,
              hasVideo: true,
              codecs: ["avc1.4d001f", "mp4a.40.2"]
            }
          }
        }
      },
      fallbacks: []
    }
  },
  {
    id: "akamai-dash",
    label: "Akamai DASH Big Buck Bunny",
    description: "Reference DASH stream used for QA. Helpful for manifest inspection.",
    contentId: "bbb-dash",
    contentType: "clip",
    endpoints: {
      primary: {
        nodeId: "mock-akamai-dash",
        protocol: "DASH",
        url: "https://dash.akamaized.net/envivio/EnvivioDash3/manifest.mpd",
        outputs: {
          DASH: {
            protocol: "DASH",
            url: "https://dash.akamaized.net/envivio/EnvivioDash3/manifest.mpd",
            capabilities: {
              supportsSeek: true,
              supportsQualitySwitch: true,
              hasAudio: true,
              hasVideo: true,
              codecs: ["avc1.4d4028", "mp4a.40.2"]
            }
          }
        }
      },
      fallbacks: []
    }
  }
];

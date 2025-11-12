import type { ContentEndpoints } from "@livepeer-frameworks/player";

export type MockStream = {
  id: string;
  label: string;
  description: string;
  endpoints: ContentEndpoints;
  contentId: string;
  contentType: "live" | "dvr" | "clip";
};

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

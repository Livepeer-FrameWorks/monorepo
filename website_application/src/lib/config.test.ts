import { beforeEach, describe, expect, it, vi } from "vitest";

let streamingConfig: unknown = null;

vi.mock("$lib/stores/streaming-config.svelte", () => ({
  getStreamingConfig: () => streamingConfig,
}));

async function loadConfig() {
  vi.resetModules();
  return import("$lib/config");
}

describe("streaming URL config", () => {
  beforeEach(() => {
    streamingConfig = null;
    vi.unstubAllEnvs();
    vi.stubEnv("VITE_STREAMING_INGEST_URL", "https://ingest.example.com");
    vi.stubEnv("VITE_STREAMING_PLAY_URL", "https://play.example.com/view");
    vi.stubEnv("VITE_STREAMING_EDGE_URL", "https://edge.example.com");
  });

  it("does not synthesize TLS schemes for direct Mist ingest protocols", async () => {
    streamingConfig = {
      tenantIngestDomain: "tenant-ingest.example.com",
      tenantPlayDomain: "tenant-play.example.com",
      tenantEdgeDomain: "tenant-edge.example.com",
      rtmpPort: 1935,
      srtPort: 8889,
    };

    const { getIngestUrls, getRtmpServerUrl } = await loadConfig();

    expect(getIngestUrls("stream-key")).toMatchObject({
      rtmp: "rtmp://tenant-ingest.example.com:1935/live/stream-key",
      srt: "srt://tenant-ingest.example.com:8889?streamid=stream-key&latency=200&mode=caller",
      whip: "https://tenant-ingest.example.com/webrtc/stream-key",
    });
    expect(getRtmpServerUrl()).toBe("rtmp://tenant-ingest.example.com:1935/live");
  });

  it("does not synthesize TLS schemes for direct Mist playback protocols", async () => {
    streamingConfig = {
      tenantIngestDomain: "tenant-ingest.example.com",
      tenantPlayDomain: "tenant-play.example.com",
      tenantEdgeDomain: "tenant-edge.example.com",
      rtmpPort: 1935,
      srtPort: 8889,
    };

    const { getContentDeliveryUrls } = await loadConfig();

    const urls = getContentDeliveryUrls("playback-id", "live");
    expect(urls.primary.srt).toBe("srt://tenant-edge.example.com:8889?streamid=playback-id");
    expect(urls.additional.rtmp).toBe("rtmp://tenant-edge.example.com:1935/play/playback-id");
    expect(urls.additional.rtsp).toBe("rtsp://tenant-edge.example.com:554/play/playback-id");
    expect(urls.additional.dtsc).toBe("dtsc://tenant-edge.example.com:4200/play/playback-id");
    expect(urls.additional.wsmp4).toBe("wss://tenant-edge.example.com:8080/play/playback-id.mp4");
  });
});

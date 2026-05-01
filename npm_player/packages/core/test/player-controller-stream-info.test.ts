import { describe, expect, it, vi } from "vitest";

import {
  PlayerController,
  buildQualityLevelsFromMistTracks,
  buildStreamInfoFromEndpoints,
} from "../src/core/PlayerController";
import { normalizeMistSourceUrls } from "../src/core/MistSourceUrls";
import type { ContentEndpoints } from "../src/types";

describe("buildStreamInfoFromEndpoints", () => {
  it("splits combined capability codec strings into distinct audio and video tracks", () => {
    const endpoints: ContentEndpoints = {
      primary: {
        nodeId: "node-1",
        protocol: "WHEP",
        url: "https://example.com/live/whep",
        outputs: {
          WHEP: {
            protocol: "WHEP",
            url: "https://example.com/live/whep",
            capabilities: {
              supportsSeek: true,
              supportsQualitySwitch: false,
              hasAudio: true,
              hasVideo: true,
              codecs: ["avc1.42E01E,mp4a.40.2", "avc1.42E01E,opus"],
            },
          },
        },
      },
      fallbacks: [],
      metadata: { isLive: true },
    };

    const info = buildStreamInfoFromEndpoints(endpoints, "live-stream");
    expect(info).not.toBeNull();

    const tracks = info?.meta.tracks ?? [];
    expect(tracks).toEqual(
      expect.arrayContaining([
        expect.objectContaining({ type: "video", codec: "H264", codecstring: "avc1.42E01E" }),
        expect.objectContaining({ type: "audio", codec: "AAC", codecstring: "mp4a.40.2" }),
        expect.objectContaining({ type: "audio", codec: "Opus", codecstring: "opus" }),
      ])
    );
  });
});

describe("normalizeMistSourceUrls", () => {
  it("pins relative and host-only Mist source URLs to the selected edge origin", () => {
    const sources = normalizeMistSourceUrls(
      [
        { type: "html5/video/mp4", url: "/view/live%2Bdemo.mp4?tkn=1" },
        { type: "html5/video/mp4", url: "http://localhost/view/live%2Bdemo.mp4?tkn=1" },
        { type: "ws/video/raw", url: "ws://localhost/view/live%2Bdemo.raw?tkn=1" },
        { type: "ws/video/mp4", url: "/view/live%2Bdemo.mp4?tkn=1" },
      ],
      "http://localhost:18090"
    );

    expect(sources?.map((s) => s.url)).toEqual([
      "http://localhost:18090/view/live%2Bdemo.mp4?tkn=1",
      "http://localhost:18090/view/live%2Bdemo.mp4?tkn=1",
      "ws://localhost:18090/view/live%2Bdemo.raw?tkn=1",
      "ws://localhost:18090/view/live%2Bdemo.mp4?tkn=1",
    ]);
  });
});

describe("PlayerController Mist edge hydration", () => {
  it("uses gateway metadata only to pick the Mist edge stream name and base URL", async () => {
    const controller = new PlayerController({
      contentId: "pb_demo_live_001",
      contentType: "live",
      playerManager: {
        on: vi.fn(() => () => {}),
      } as any,
    });
    (controller as any).endpoints = {
      primary: {
        nodeId: "edge-1",
        protocol: "MP4",
        url: "http://localhost:18090/view/live%2Bdemo_live_stream_001.mp4?tkn=1",
        outputs: {
          MP4: {
            protocol: "MP4",
            url: "http://localhost/view/live%2Bdemo_live_stream_001.mp4?tkn=1",
          },
        },
      },
      fallbacks: [],
      metadata: {
        contentId: "live+demo_live_stream_001",
        thumbnailAssets: { assetKey: "asset-1", posterUrl: "https://cdn/poster.jpg" },
      },
    };
    (controller as any).setMetadataSeed((controller as any).endpoints.metadata);

    const requested: string[] = [];
    (controller as any).fetchMistStreamInfo = vi.fn(
      async (_baseUrl: string, streamName: string) => {
        requested.push(streamName);
        if (streamName !== "live+demo_live_stream_001") {
          throw new Error("wrong stream");
        }
        return {
          type: "live",
          capa: { datachannels: true },
          source: [
            {
              type: "html5/video/mp4",
              url: "http://localhost/view/live%2Bdemo_live_stream_001.mp4?tkn=1",
            },
            {
              type: "html5/application/vnd.apple.mpegurl",
              url: "/view/hls/live%2Bdemo_live_stream_001/index.m3u8?tkn=1",
            },
            { type: "whep", url: "/view/webrtc/live%2Bdemo_live_stream_001?tkn=1" },
          ],
          meta: {
            tracks: {
              video: { type: "video", codec: "H264", width: 800, height: 600, idx: 1 },
              audio: { type: "audio", codec: "AAC", idx: 2 },
            },
          },
        };
      }
    );

    await (controller as any).hydrateFromSelectedMistEdge("pb_demo_live_001");

    expect(requested).toEqual(["live+demo_live_stream_001"]);
    expect((controller as any).streamInfo.source.map((s: { type: string }) => s.type)).toEqual([
      "html5/video/mp4",
      "html5/application/vnd.apple.mpegurl",
      "whep",
    ]);
    expect((controller as any).streamInfo.source[0].url).toBe(
      "http://localhost:18090/view/live%2Bdemo_live_stream_001.mp4?tkn=1"
    );
    expect((controller as any)._thumbnailAssets).toEqual(
      expect.objectContaining({ assetKey: "asset-1" })
    );
  });
});

describe("buildQualityLevelsFromMistTracks", () => {
  it("hides JPEG preview tracks when real video is available", () => {
    const qualities = buildQualityLevelsFromMistTracks({
      preview: { type: "video", codec: "JPEG", lang: "pre", width: 320, height: 180 },
      thumb: { type: "video", codec: "JPEG", lang: "thumbnails", width: 160, height: 90 },
      main: {
        type: "video",
        codec: "H264",
        width: 1920,
        height: 1080,
        fpks: 30_000,
        bps: 4_000_000,
      },
      audio: { type: "audio", codec: "AAC" },
    });

    expect(qualities).toEqual([
      {
        id: "main",
        label: "1920x1080@30fps 4.0 Mbps",
        width: 1920,
        height: 1080,
        bitrate: 4_000_000,
      },
    ]);
  });

  it("keeps JPEG tracks when no real video exists", () => {
    const qualities = buildQualityLevelsFromMistTracks({
      preview: { type: "video", codec: "JPEG", lang: "pre", width: 320, height: 180 },
    });

    expect(qualities).toEqual([
      {
        id: "preview",
        label: "320x180",
        width: 320,
        height: 180,
        bitrate: undefined,
      },
    ]);
  });
});

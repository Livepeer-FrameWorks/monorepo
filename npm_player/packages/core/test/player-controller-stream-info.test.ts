import { describe, expect, it } from "vitest";

import {
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

import { describe, expect, it } from "vitest";

import { buildStreamInfoFromEndpoints } from "../src/core/PlayerController";
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

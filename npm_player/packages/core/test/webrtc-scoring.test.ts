import { afterEach, describe, expect, it, vi } from "vitest";

import { PlayerManager } from "../src/core/PlayerManager";
import type { StreamInfo } from "../src/core/PlayerInterface";
import { MistWebRTCPlayerImpl } from "../src/players/MistWebRTCPlayer";

describe("WebRTC scoring", () => {
  const originalWindow = globalThis.window;

  afterEach(() => {
    Object.defineProperty(globalThis, "window", {
      value: originalWindow,
      configurable: true,
      writable: true,
    });
    vi.restoreAllMocks();
  });

  it("does not penalize WebRTC audio when Opus is available alongside AAC", () => {
    Object.defineProperty(globalThis, "window", {
      value: {
        RTCPeerConnection: vi.fn(),
        WebSocket: vi.fn(),
      },
      configurable: true,
      writable: true,
    });

    const manager = new PlayerManager();
    manager.registerPlayer(new MistWebRTCPlayerImpl());

    const streamInfo: StreamInfo = {
      source: [{ url: "wss://mist.example.com/ws", type: "mist/webrtc" }],
      meta: {
        tracks: [
          { type: "video", codec: "H264" },
          { type: "audio", codec: "AAC" },
          { type: "audio", codec: "Opus" },
        ],
      },
      type: "live",
    };

    const [combo] = manager.getAllCombinations(streamInfo);

    expect(combo.compatible).toBe(true);
    expect(combo.missingTracks).toBeUndefined();
    expect(combo.scoreBreakdown?.trackTypes).toEqual(["video", "audio"]);
  });
});

// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";

// Every built-in player module counts its loads and fails to load, the way a
// chunk fails when the network drops or a deploy replaced the bundle.
const { playerModuleLoads, failingPlayerModule } = vi.hoisted(() => {
  const loads = vi.fn();
  return {
    playerModuleLoads: loads,
    failingPlayerModule: (name: string) => () => {
      loads(name);
      throw new Error(`chunk load failed: ${name}`);
    },
  };
});
vi.mock("../src/players/NativePlayer", failingPlayerModule("native"));
vi.mock("../src/players/WebCodecsPlayer", failingPlayerModule("webcodecs"));
vi.mock("../src/players/MistWebRTCPlayer", failingPlayerModule("mist-webrtc"));
vi.mock("../src/players/VideoJsPlayer", failingPlayerModule("videojs"));
vi.mock("../src/players/HlsJsPlayer", failingPlayerModule("hlsjs"));
vi.mock("../src/players/DashJsPlayer", failingPlayerModule("dashjs"));
vi.mock("../src/players/MistPlayer", failingPlayerModule("mist-legacy"));
vi.mock("../src/players/MewsWsPlayer", failingPlayerModule("mews"));

import { PlayerController } from "../src/core/PlayerController";

const mediaUrl = "https://edge.example/hls/stream/index.m3u8";

function makeController() {
  const video = document.createElement("video");
  const manager = {
    on: vi.fn(() => () => {}),
    off: vi.fn(),
    destroy: vi.fn(),
    getCurrentPlayer: vi.fn(() => null),
    getRegisteredPlayers: vi.fn(() => []),
    canAttemptFallback: vi.fn(() => false),
    initializePlayer: vi.fn(async () => video),
  };
  const controller = new PlayerController({
    contentId: "stream",
    endpoints: {
      primary: {
        nodeId: "edge",
        protocol: "HLS",
        url: mediaUrl,
        outputs: { HLS: { protocol: "HLS", url: mediaUrl } },
      },
      fallbacks: [],
    },
    playerManager: manager as any,
  });
  return { controller, manager };
}

async function settle(): Promise<void> {
  for (let i = 0; i < 20; i++) await new Promise((resolve) => setTimeout(resolve, 0));
}

describe("PlayerController player module loading", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it("leaves no player module load running after attach and destroy", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("", { status: 404 })));
    const unhandled: unknown[] = [];
    const onUnhandled = (reason: unknown) => unhandled.push(reason);
    process.on("unhandledRejection", onUnhandled);
    try {
      const { controller, manager } = makeController();
      await controller.attach(document.createElement("div"));
      controller.destroy();
      await settle();

      // The controller's own manager chooses and loads players inside
      // initializePlayer; attach itself starts no load that outlives it.
      expect(manager.initializePlayer).toHaveBeenCalledOnce();
      expect(playerModuleLoads).not.toHaveBeenCalled();
      expect(unhandled).toEqual([]);
    } finally {
      process.off("unhandledRejection", onUnhandled);
    }
  });
});

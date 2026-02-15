import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { FwDevModePanel } from "../src/components/fw-dev-mode-panel.js";
import { globalPlayerManager } from "@livepeer-frameworks/player-core";

const combos = [
  {
    player: "native",
    playerName: "Native",
    source: { url: "https://a.test/stream.m3u8", type: "html5/application/vnd.apple.mpegurl" },
    sourceIndex: 0,
    sourceType: "html5/application/vnd.apple.mpegurl",
    score: 2.1,
    compatible: true,
  },
  {
    player: "webrtc",
    playerName: "WebRTC",
    source: { url: "whep://a.test/live", type: "whep" },
    sourceIndex: 1,
    sourceType: "whep",
    score: 1.8,
    compatible: true,
  },
];

function createMockPc() {
  return {
    s: {
      streamInfo: { source: [] },
      currentPlayerInfo: { name: "Native", shortname: "native" },
      currentSourceInfo: {
        url: "https://a.test/stream.m3u8",
        type: "html5/application/vnd.apple.mpegurl",
      },
      endpoints: { primary: { protocol: "hls", nodeId: "node-1" } },
      streamState: { streamInfo: { type: "live", meta: { tracks: {} } } },
      videoElement: null,
    },
    setDevModeOptions: vi.fn().mockResolvedValue(undefined),
    clearError: vi.fn(),
    reload: vi.fn().mockResolvedValue(undefined),
    getStats: vi.fn().mockResolvedValue(null),
  };
}

describe("FwDevModePanel", () => {
  const combinationsSpy = vi.spyOn(globalPlayerManager, "getAllCombinations");

  beforeEach(() => {
    document.body.innerHTML = "";
    combinationsSpy.mockReturnValue(combos as any);
  });

  afterEach(() => {
    vi.clearAllMocks();
  });

  it("cycles to the next compatible combo", async () => {
    const pc = createMockPc();
    const el = new FwDevModePanel();
    el.pc = pc as any;
    el.playbackMode = "auto";

    document.body.appendChild(el);
    await el.updateComplete;

    vi.spyOn(el as any, "_getCompatibleCombinations").mockReturnValue(combos as any);
    vi.spyOn(el as any, "_getActiveComboIndex").mockReturnValue(0);

    (el as unknown as { _handleNextCombo: () => void })._handleNextCombo();

    expect(pc.setDevModeOptions).toHaveBeenCalledWith({
      forcePlayer: "webrtc",
      forceType: "whep",
      forceSource: 1,
    });
  });

  it("emits playback mode change events", async () => {
    const pc = createMockPc();
    const el = new FwDevModePanel();
    el.pc = pc as any;

    document.body.appendChild(el);
    await el.updateComplete;

    const modeEvents: string[] = [];
    el.addEventListener("fw-playback-mode-change", (event: Event) => {
      const typed = event as CustomEvent<{ mode: string }>;
      modeEvents.push(typed.detail.mode);
    });

    (
      el as unknown as { _handleModeChange: (mode: "auto" | "low-latency" | "quality") => void }
    )._handleModeChange("low-latency");

    expect(pc.setDevModeOptions).toHaveBeenCalledWith({ playbackMode: "low-latency" });
    expect(modeEvents).toEqual(["low-latency"]);
    expect(el.playbackMode).toBe("low-latency");
  });
});

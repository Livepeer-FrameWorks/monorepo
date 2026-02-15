import { describe, it, expect, vi, beforeEach } from "vitest";
import { FwSettingsMenu } from "../src/components/fw-settings-menu.js";

function createMockPc() {
  const video = document.createElement("video");
  video.playbackRate = 1;

  return {
    s: {
      videoElement: video,
      qualities: [
        { id: "auto", label: "Auto", active: true },
        { id: "720p", label: "720p", active: false },
      ],
      textTracks: [
        { id: "en", label: "English", active: true },
        { id: "es", label: "Spanish", active: false },
      ],
    },
    setDevModeOptions: vi.fn(),
    setPlaybackRate: vi.fn(),
    selectQuality: vi.fn(),
    selectTextTrack: vi.fn(),
  };
}

describe("FwSettingsMenu", () => {
  beforeEach(() => {
    document.body.innerHTML = "";
  });

  it("emits mode change and close events while updating playback mode", async () => {
    const pc = createMockPc();
    const el = new FwSettingsMenu();
    el.pc = pc as any;
    el.open = true;
    el.isContentLive = true;

    document.body.appendChild(el);
    await el.updateComplete;

    const modeEvents: string[] = [];
    let closeCount = 0;

    el.addEventListener("fw-mode-change", (event: Event) => {
      const typed = event as CustomEvent<{ mode: string }>;
      modeEvents.push(typed.detail.mode);
    });
    el.addEventListener("fw-close", () => {
      closeCount += 1;
    });

    (
      el as unknown as { _handleModeChange: (mode: "auto" | "low-latency" | "quality") => void }
    )._handleModeChange("quality");

    expect(pc.setDevModeOptions).toHaveBeenCalledWith({ playbackMode: "quality" });
    expect(modeEvents).toEqual(["quality"]);
    expect(closeCount).toBe(1);
  });

  it("routes captions off selection to null track id", async () => {
    const pc = createMockPc();
    const el = new FwSettingsMenu();
    el.pc = pc as any;
    el.open = true;

    document.body.appendChild(el);
    await el.updateComplete;

    (el as unknown as { _handleCaptionChange: (id: string) => void })._handleCaptionChange("none");

    expect(pc.selectTextTrack).toHaveBeenCalledWith(null);
  });

  it("renders quality options from stream tracks when controller qualities are unavailable", async () => {
    const pc = createMockPc() as any;
    pc.s.qualities = [];
    pc.s.textTracks = [];
    pc.s.streamState = {
      streamInfo: {
        meta: {
          tracks: {
            video_main: { type: "video", height: 1080, width: 1920, bps: 5_000_000 },
            video_backup: { type: "video", height: 720, width: 1280, bps: 2_800_000 },
            audio_main: { type: "audio", codec: "aac" },
          },
        },
      },
    };

    const el = new FwSettingsMenu();
    el.pc = pc;
    el.open = true;

    document.body.appendChild(el);
    await el.updateComplete;

    const qualityItems = Array.from(el.renderRoot.querySelectorAll(".fw-settings-list-item")).map(
      (node) => node.textContent?.trim() ?? ""
    );

    expect(qualityItems).toContain("Auto");
    expect(qualityItems).toContain("1080p");
    expect(qualityItems).toContain("720p");
  });
});

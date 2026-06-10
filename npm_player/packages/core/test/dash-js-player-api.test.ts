import { describe, expect, it, vi } from "vitest";

import { DashJsPlayerImpl } from "../src/players/DashJsPlayer";

// Thin delegation over the dash.js MediaPlayer + native element. We inject fakes
// and drive streamType directly (isLiveStream() reads it) instead of booting dash.js.

function inject(
  p: DashJsPlayerImpl,
  opts: { dash?: unknown; video?: unknown; streamType?: "live" | "vod" | "unknown" } = {}
) {
  if (opts.dash !== undefined) (p as any).dashPlayer = opts.dash;
  if (opts.video !== undefined) (p as any).videoElement = opts.video;
  if (opts.streamType) (p as any).streamType = opts.streamType;
}

const seekable = (startSec: number, endSec: number) => ({
  length: 1,
  start: () => startSec,
  end: () => endSec,
});

describe("DashJsPlayerImpl — capability", () => {
  it("isMimeSupported matches the dash mime only", () => {
    const p = new DashJsPlayerImpl();
    expect(p.isMimeSupported("dash/video/mp4")).toBe(true);
    expect(p.isMimeSupported("html5/video/mp4")).toBe(false);
  });
});

describe("DashJsPlayerImpl — stats & source", () => {
  it("getStats returns undefined without a dash player or video", async () => {
    await expect(new DashJsPlayerImpl().getStats()).resolves.toBeUndefined();
  });

  it("getStats maps dashjs v5 representations (bandwidth) into the stats shape", async () => {
    const p = new DashJsPlayerImpl();
    inject(p, {
      dash: {
        getCurrentRepresentationForType: () => ({ id: "r2", bandwidth: 2_500_000 }),
        getRepresentationsByType: () => [
          { id: "r1", bandwidth: 1_000_000, width: 640, height: 360 },
          { id: "r2", bandwidth: 2_500_000, width: 1280, height: 720 },
        ],
        getBufferLength: () => 12,
      },
      video: { playbackRate: 1 },
    });
    await expect(p.getStats()).resolves.toMatchObject({
      type: "dash",
      currentQuality: 1, // index of r2
      bufferLevel: 12,
      currentBitrate: 2_500_000,
    });
  });

  it("setPlaybackRate writes through to the element; setSource attaches a new URL", () => {
    const p = new DashJsPlayerImpl();
    const attachSource = vi.fn();
    const video = { playbackRate: 1 };
    inject(p, { dash: { attachSource }, video });
    p.setPlaybackRate(1.5);
    expect(video.playbackRate).toBe(1.5);
    p.setSource("https://edge.example/stream.mpd");
    expect(attachSource).toHaveBeenCalledWith("https://edge.example/stream.mpd");
  });
});

describe("DashJsPlayerImpl — live vs VoD coordinate routing", () => {
  it("getDuration uses the native seekable end for live, element duration for VoD", () => {
    const live = new DashJsPlayerImpl();
    inject(live, {
      video: { seekable: seekable(10, 610), duration: Infinity },
      streamType: "live",
    });
    expect(live.getDuration()).toBe(610_000);

    const vod = new DashJsPlayerImpl();
    inject(vod, { video: { duration: 120, seekable: seekable(0, 0) }, streamType: "vod" });
    expect(vod.getDuration()).toBe(120_000);
  });

  it("getSeekableRange returns the native window for live", () => {
    const p = new DashJsPlayerImpl();
    inject(p, { video: { seekable: seekable(10, 610) }, streamType: "live" });
    expect(p.getSeekableRange()).toEqual({ start: 10_000, end: 610_000 });
  });

  it("seek routes live through dash.js seekToPresentationTime (presentation seconds)", () => {
    const p = new DashJsPlayerImpl();
    const seekToPresentationTime = vi.fn();
    inject(p, { dash: { seekToPresentationTime }, video: {}, streamType: "live" });
    p.seek(30_000);
    expect(seekToPresentationTime).toHaveBeenCalledWith(30);
  });

  it("jumpToLive uses dash.js seekToOriginalLive for live and no-ops otherwise", () => {
    const p = new DashJsPlayerImpl();
    const seekToOriginalLive = vi.fn();
    inject(p, { dash: { seekToOriginalLive }, video: {}, streamType: "live" });
    p.jumpToLive();
    expect(seekToOriginalLive).toHaveBeenCalledOnce();

    const vod = new DashJsPlayerImpl();
    const spy = vi.fn();
    inject(vod, { dash: { seekToOriginalLive: spy }, video: {}, streamType: "vod" });
    vod.jumpToLive();
    expect(spy).not.toHaveBeenCalled();
  });

  it("getLiveLatency prefers dash.js metric, else seekable fallback, else 0", () => {
    const p = new DashJsPlayerImpl();
    expect(p.getLiveLatency()).toBe(0); // no video / not live

    const metric = new DashJsPlayerImpl();
    inject(metric, {
      dash: { getCurrentLiveLatency: () => 2.5 },
      video: { currentTime: 0 },
      streamType: "live",
    });
    expect(metric.getLiveLatency()).toBe(2500);

    const fallback = new DashJsPlayerImpl();
    inject(fallback, {
      dash: {},
      video: { currentTime: 600, seekable: seekable(0, 610) },
      streamType: "live",
    });
    expect(fallback.getLiveLatency()).toBeCloseTo((610 - 600) * 1000);
  });
});

describe("DashJsPlayerImpl — quality API", () => {
  it("getQualities returns [] without a dash player or video", () => {
    expect(new DashJsPlayerImpl().getQualities()).toEqual([]);
  });

  it("getQualities leads with Auto (reflecting autoSwitchBitrate) then representations", () => {
    const p = new DashJsPlayerImpl();
    inject(p, {
      dash: {
        getRepresentationsByType: () => [{ width: 1280, height: 720, bandwidth: 2_500_000 }],
        getSettings: () => ({ streaming: { abr: { autoSwitchBitrate: { video: true } } } }),
      },
      video: {},
    });
    const qs = p.getQualities();
    expect(qs[0]).toMatchObject({ id: "auto", isAuto: true, active: true });
    expect(qs[1]).toMatchObject({ id: "0", bitrate: 2_500_000, height: 720 });
  });

  it("selectQuality toggles ABR auto-switch and pins a representation index", () => {
    const p = new DashJsPlayerImpl();
    const updateSettings = vi.fn();
    const setRepresentationForTypeByIndex = vi.fn();
    inject(p, { dash: { updateSettings, setRepresentationForTypeByIndex } });

    p.selectQuality("auto");
    expect(updateSettings).toHaveBeenCalledWith({
      streaming: { abr: { autoSwitchBitrate: { video: true } } },
    });

    p.selectQuality("2");
    expect(updateSettings).toHaveBeenCalledWith({
      streaming: { abr: { autoSwitchBitrate: { video: false } } },
    });
    expect(setRepresentationForTypeByIndex).toHaveBeenCalledWith("video", 2);
  });
});

describe("DashJsPlayerImpl — text tracks", () => {
  it("getTextTracks maps native text tracks", () => {
    const p = new DashJsPlayerImpl();
    inject(p, {
      dash: {},
      video: { textTracks: [{ label: "English", language: "en", mode: "showing" }] },
    });
    expect(p.getTextTracks()[0]).toMatchObject({
      id: "0",
      label: "English",
      lang: "en",
      active: true,
    });
  });

  it("defers text-track selection until subtitles have loaded", () => {
    const p = new DashJsPlayerImpl();
    inject(p, { dash: {}, video: { textTracks: [] } });
    (p as any).subsLoaded = false;
    p.selectTextTrack("1");
    expect((p as any).pendingSubtitleId).toBe("1");
  });

  it("uses the dash.js text API once loaded (index and disable)", () => {
    const p = new DashJsPlayerImpl();
    const setTextTrack = vi.fn();
    inject(p, {
      dash: { getTracksFor: () => [{}, {}], setTextTrack },
      video: { textTracks: [] },
    });
    (p as any).subsLoaded = true;

    p.selectTextTrack("1");
    expect(setTextTrack).toHaveBeenCalledWith(1);

    p.selectTextTrack(null);
    expect(setTextTrack).toHaveBeenCalledWith(-1);
  });
});

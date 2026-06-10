import { describe, expect, it, vi } from "vitest";

import { VideoJsPlayerImpl } from "../src/players/VideoJsPlayer";

// Thin delegation over the video.js player instance + native element — driven by
// injecting fakes rather than booting video.js.

function inject(p: VideoJsPlayerImpl, vjs: unknown, video?: unknown) {
  (p as any).videojsPlayer = vjs;
  if (video !== undefined) (p as any).videoElement = video;
}

const callPrivate = (p: VideoJsPlayerImpl, name: string, ...args: unknown[]) =>
  (p as any)[name](...args);

describe("VideoJsPlayerImpl — capability + type mapping", () => {
  it("isMimeSupported matches the HLS mimes only", () => {
    const p = new VideoJsPlayerImpl();
    expect(p.isMimeSupported("html5/application/vnd.apple.mpegurl")).toBe(true);
    expect(p.isMimeSupported("html5/video/mp4")).toBe(false);
  });

  it("getVideoJsType maps our mimes to video.js source types", () => {
    const p = new VideoJsPlayerImpl();
    const t = (m?: string) => callPrivate(p, "getVideoJsType", m);
    expect(t(undefined)).toBe("application/x-mpegURL");
    expect(t("html5/application/vnd.apple.mpegurl")).toBe("application/x-mpegURL");
    expect(t("application/dash+xml")).toBe("application/dash+xml");
    expect(t("html5/video/mp4")).toBe("video/mp4");
    expect(t("html5/video/webm")).toBe("video/webm");
    expect(t("html5/audio/ogg")).toBe("audio/ogg"); // default: strips html5/
  });
});

describe("VideoJsPlayerImpl — playback delegation", () => {
  it("setPlaybackRate forwards to the video.js player", () => {
    const p = new VideoJsPlayerImpl();
    const playbackRate = vi.fn();
    inject(p, { playbackRate }, {});
    p.setPlaybackRate(1.5);
    expect(playbackRate).toHaveBeenCalledWith(1.5);
  });

  it("seekInBuffer prefers the video.js player, falling back to the element", () => {
    const p = new VideoJsPlayerImpl();
    const currentTime = vi.fn();
    inject(p, { currentTime });
    callPrivate(p, "seekInBuffer", 42);
    expect(currentTime).toHaveBeenCalledWith(42);

    const p2 = new VideoJsPlayerImpl();
    const video = { currentTime: 0 };
    inject(p2, null, video);
    callPrivate(p2, "seekInBuffer", 7);
    expect(video.currentTime).toBe(7);
  });
});

describe("VideoJsPlayerImpl — stats, duration, latency, jumpToLive", () => {
  it("getStats returns undefined without a video element", async () => {
    await expect(new VideoJsPlayerImpl().getStats()).resolves.toBeUndefined();
  });

  it("getStats reports buffered-ahead and element state", async () => {
    const p = new VideoJsPlayerImpl();
    inject(p, null, {
      currentTime: 50,
      duration: 120,
      readyState: 4,
      networkState: 2,
      playbackRate: 1,
      buffered: { length: 1, start: () => 48, end: () => 65 },
    });
    await expect(p.getStats()).resolves.toMatchObject({
      type: "videojs",
      buffered: 15, // 65 - 50
      currentTime: 50,
      duration: 120,
    });
  });

  it("getDuration converts a finite element duration to ms", () => {
    const p = new VideoJsPlayerImpl();
    inject(p, null, { duration: 120, seekable: { length: 0 } });
    expect(p.getDuration()).toBe(120_000);
  });

  it("getLiveLatency uses the live tracker when live", () => {
    const p = new VideoJsPlayerImpl();
    expect(p.getLiveLatency()).toBe(0); // no video

    inject(
      p,
      { liveTracker: { isLive: () => true, liveCurrentTime: () => 60 } },
      { currentTime: 52 }
    );
    expect(p.getLiveLatency()).toBeCloseTo((60 - 52) * 1000);
  });

  it("jumpToLive seeks to the live edge and resumes when the tracker is live", () => {
    const p = new VideoJsPlayerImpl();
    const seekToLiveEdge = vi.fn();
    const play = vi.fn();
    inject(p, {
      liveTracker: { isLive: () => true, seekToLiveEdge },
      play,
    });
    p.jumpToLive();
    expect(seekToLiveEdge).toHaveBeenCalledOnce();
    expect(play).toHaveBeenCalledOnce();
  });
});

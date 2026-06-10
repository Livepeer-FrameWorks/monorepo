import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { MewsWsPlayerImpl } from "../src/players/MewsWsPlayer/index";
import type { StreamInfo } from "../src/core/PlayerInterface";
import { stubGlobalMediaSource } from "./_fixtures/FakeMediaSource";

function fakeVideo(props: Partial<{ currentTime: number; duration: number }> = {}) {
  return {
    currentTime: 0,
    duration: 0,
    playbackRate: 1,
    play: vi.fn(async () => {}),
    ...props,
  };
}

describe("MewsWsPlayerImpl — capability & codec gating", () => {
  let typeSupported: ReturnType<typeof stubGlobalMediaSource>;

  // A Chrome-on-Linux environment that satisfies every MEWS precondition.
  function stubSupportedEnv() {
    typeSupported = stubGlobalMediaSource(() => true);
    vi.stubGlobal("window", {
      WebSocket: class {},
      MediaSource: (globalThis as any).MediaSource,
      Promise,
      location: { protocol: "https:" },
    });
    vi.stubGlobal("navigator", {
      userAgent:
        "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120 Safari/537.36",
      platform: "Linux x86_64",
    });
  }

  const stream: StreamInfo = {
    source: [{ type: "ws/video/mp4", url: "wss://x/stream" }],
    meta: { tracks: [{ type: "video", codec: "H264", idx: 0 } as never] },
    type: "live",
  };

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("isMimeSupported accepts the ws(s) mp4/webm mimes only", () => {
    const p = new MewsWsPlayerImpl();
    expect(p.isMimeSupported("ws/video/mp4")).toBe(true);
    expect(p.isMimeSupported("wss/video/webm")).toBe(true);
    expect(p.isMimeSupported("html5/video/mp4")).toBe(false);
  });

  it("isBrowserSupported returns the playable track types in a supported env", () => {
    stubSupportedEnv();
    const p = new MewsWsPlayerImpl();
    expect(p.isBrowserSupported("ws/video/mp4", stream.source[0], stream)).toEqual(["video"]);
  });

  it("returns false on macOS (MSE breaks MEWS there)", () => {
    stubSupportedEnv();
    vi.stubGlobal("navigator", { userAgent: "Mozilla/5.0 (Macintosh)", platform: "MacIntel" });
    const p = new MewsWsPlayerImpl();
    expect(p.isBrowserSupported("ws/video/mp4", stream.source[0], stream)).toBe(false);
  });

  it("returns false when MediaSource is absent from window", () => {
    stubSupportedEnv();
    vi.stubGlobal("window", { WebSocket: class {}, Promise, location: { protocol: "https:" } });
    const p = new MewsWsPlayerImpl();
    expect(p.isBrowserSupported("ws/video/mp4", stream.source[0], stream)).toBe(false);
  });

  it("returns false when no advertised codec is MSE-supported", () => {
    stubSupportedEnv();
    typeSupported.mockReturnValue(false);
    const p = new MewsWsPlayerImpl();
    expect(p.isBrowserSupported("ws/video/mp4", stream.source[0], stream)).toBe(false);
  });
});

describe("MewsWsPlayerImpl — timeline getters", () => {
  it("getCurrentTime converts the element's seconds to ms", () => {
    const p = new MewsWsPlayerImpl();
    (p as any).videoElement = fakeVideo({ currentTime: 3 });
    expect(p.getCurrentTime()).toBe(3000);
  });

  it("getDuration prefers a finite lastDuration, else the element, preserving Infinity", () => {
    const p = new MewsWsPlayerImpl();
    (p as any).videoElement = fakeVideo({ duration: 30 });
    expect(p.getDuration()).toBe(30000); // lastDuration defaults to Infinity → element

    (p as any).lastDuration = 42;
    expect(p.getDuration()).toBe(42000);
  });

  it("getSeekableRange reflects on_time begin/end once both are known", () => {
    const p = new MewsWsPlayerImpl();
    expect(p.getSeekableRange()).toBeNull();
    (p as any).seekableBeginMs = 1000;
    (p as any).seekableEndMs = 9000;
    expect(p.getSeekableRange()).toEqual({ start: 1000, end: 9000 });
  });

  it("isLive tracks the detected stream type", () => {
    const p = new MewsWsPlayerImpl();
    expect(p.isLive()).toBe(false);
    (p as any).streamType = "live";
    expect(p.isLive()).toBe(true);
  });

  it("getStats reports current bitrate, waiting count and liveness", async () => {
    const p = new MewsWsPlayerImpl();
    (p as any).currentBps = 2_500_000;
    (p as any).nWaiting = 4;
    (p as any).streamType = "live";
    await expect(p.getStats()).resolves.toMatchObject({
      currentBps: 2_500_000,
      waitingEvents: 4,
      isLive: true,
    });
  });
});

describe("MewsWsPlayerImpl — control commands", () => {
  function playerWithWs() {
    const p = new MewsWsPlayerImpl();
    const send = vi.fn();
    (p as any).wsManager = { send };
    return { p, send };
  }

  it("getQualities leads with an active Auto entry", () => {
    const { p } = playerWithWs();
    (p as any).streamInfoRef = {
      meta: { tracks: [{ type: "video", idx: 0, width: 1280, height: 720 }] },
    };
    const qs = p.getQualities();
    expect(qs[0]).toMatchObject({ id: "auto", isAuto: true, active: true });
    expect(qs.some((q) => q.id === "0")).toBe(true);
  });

  it("selectQuality sends a tracks command and tracks the selection", () => {
    const { p, send } = playerWithWs();
    p.selectQuality("auto");
    expect(send).toHaveBeenCalledWith({ type: "tracks" });
    expect((p as any).selectedTrack).toBe("auto");

    p.selectQuality("0");
    expect(send).toHaveBeenCalledWith({ type: "tracks", video: "0" });
    expect((p as any).selectedTrack).toBe("0");
  });

  it("setTracks ignores an empty object but forwards a populated one", () => {
    const { p, send } = playerWithWs();
    p.setTracks({});
    expect(send).not.toHaveBeenCalled();
    p.setTracks({ audio: "2" });
    expect(send).toHaveBeenCalledWith({ type: "tracks", audio: "2" });
  });

  it("selectTextTrack maps null to 'none' and an id through", () => {
    const { p, send } = playerWithWs();
    p.selectTextTrack(null);
    expect(send).toHaveBeenCalledWith({ type: "tracks", subtitle: "none" });
    p.selectTextTrack("3");
    expect(send).toHaveBeenCalledWith({ type: "tracks", subtitle: "3" });
  });

  it("setPlaybackRate maps rate 1 to 'auto' and other rates through", () => {
    const { p, send } = playerWithWs();
    (p as any).videoElement = fakeVideo();
    p.setPlaybackRate(1);
    expect(send).toHaveBeenCalledWith({ type: "set_speed", play_rate: "auto" });
    p.setPlaybackRate(1.5);
    expect(send).toHaveBeenCalledWith({ type: "set_speed", play_rate: 1.5 });
  });

  it("jumpToLive seeks to the live edge only for live streams", () => {
    const { p, send } = playerWithWs();
    const video = fakeVideo();
    (p as any).videoElement = video;

    p.jumpToLive(); // streamType still "unknown" → no-op
    expect(send).not.toHaveBeenCalled();

    (p as any).streamType = "live";
    p.jumpToLive();
    expect(send).toHaveBeenCalledWith({ type: "play", seek_time: "live" });
    expect(video.play).toHaveBeenCalled();
  });
});

describe("MewsWsPlayerImpl — resume on reconnect", () => {
  // A fresh WebSocket carries no playback state, so after a transient drop the
  // server won't send media until we re-ask. resumePlaybackAfterReconnect() must
  // re-issue play even though the video element is still "playing" off residual
  // buffer (so play() would early-return). Mirrors mews.js reopen → api.play().
  function playerWithWs(streamType: "live" | "vod", currentTime = 0) {
    const p = new MewsWsPlayerImpl();
    const send = vi.fn();
    (p as any).wsManager = { send };
    (p as any).sbManager = { paused: false };
    (p as any).streamType = streamType;
    (p as any).videoElement = fakeVideo({ currentTime });
    return { p, send };
  }

  const resume = (p: MewsWsPlayerImpl) =>
    (p as unknown as { resumePlaybackAfterReconnect(): void }).resumePlaybackAfterReconnect();

  it("re-pins live to the edge when the element is at 0", () => {
    const { p, send } = playerWithWs("live", 0);
    resume(p);
    expect(send).toHaveBeenCalledWith({ type: "play", seek_time: "live" });
    expect((p as any).sbManager.paused).toBe(false);
  });

  it("resumes plain play for live mid-stream (no re-pin to edge)", () => {
    const { p, send } = playerWithWs("live", 12);
    resume(p);
    expect(send).toHaveBeenCalledWith({ type: "play" });
  });

  it("resumes plain play for VoD", () => {
    const { p, send } = playerWithWs("vod", 30);
    resume(p);
    expect(send).toHaveBeenCalledWith({ type: "play" });
  });

  it("does not resume when the user had paused (download held)", () => {
    const { p, send } = playerWithWs("live", 0);
    (p as any).sbManager.paused = true;
    resume(p);
    expect(send).not.toHaveBeenCalled();
  });
});

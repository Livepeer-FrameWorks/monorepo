import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { BasePlayer } from "../src/core/PlayerInterface";

class TestPlayer extends BasePlayer {
  readonly capability = {
    name: "Test",
    shortname: "test",
    priority: 1,
    mimes: ["test/video"],
  };

  isMimeSupported(mime: string) {
    return mime === "test/video";
  }

  isBrowserSupported() {
    return ["video" as const];
  }

  async initialize() {
    return this.videoElement!;
  }

  destroy() {}

  setVideoElement(el: HTMLVideoElement | null) {
    this.videoElement = el;
  }

  callSetupVideoEventListeners(v: HTMLVideoElement, o: any) {
    this.setupVideoEventListeners(v, o);
  }

  callEmit(event: string, data: any) {
    this.emit(event as any, data);
  }
}

function createMockVideo() {
  const listeners = new Map<string, Function[]>();
  return {
    currentTime: 0,
    duration: 100,
    paused: false,
    muted: false,
    volume: 1,
    playbackRate: 1,
    videoWidth: 1920,
    videoHeight: 1080,
    error: null as any,
    textTracks: [] as any,
    buffered: { length: 1, start: () => 0, end: () => 60 },
    seekable: { length: 1, start: () => 0, end: () => 100 },
    style: { width: "", height: "" },
    play: vi.fn(async () => {}),
    pause: vi.fn(),
    addEventListener: vi.fn((event: string, handler: Function) => {
      if (!listeners.has(event)) listeners.set(event, []);
      listeners.get(event)!.push(handler);
    }),
    removeEventListener: vi.fn(),
    requestPictureInPicture: vi.fn(async () => {}),
    getVideoPlaybackQuality: vi.fn(() => ({ totalVideoFrames: 100, droppedVideoFrames: 2 })),
    _fire(event: string) {
      listeners.get(event)?.forEach((h) => h());
    },
    _listeners: listeners,
  } as unknown as HTMLVideoElement;
}

describe("BasePlayer", () => {
  let origDocument: typeof globalThis.document;

  beforeEach(() => {
    origDocument = globalThis.document;
    (globalThis as any).document = {
      pictureInPictureElement: null,
      exitPictureInPicture: vi.fn(async () => {}),
    };
  });

  afterEach(() => {
    (globalThis as any).document = origDocument;
    vi.restoreAllMocks();
  });

  it("on/off/emit manages listeners and isolates errors", () => {
    const player = new TestPlayer();
    const goodListener = vi.fn();
    const badListener = vi.fn(() => {
      throw new Error("boom");
    });
    const errorSpy = vi.spyOn(console, "error").mockImplementation(() => {});

    player.on("play", badListener);
    player.on("play", goodListener);

    player.callEmit("play", undefined);

    expect(goodListener).toHaveBeenCalledTimes(1);
    expect(errorSpy).toHaveBeenCalledWith("Error in play listener:", expect.any(Error));

    player.off("play", goodListener);
    player.callEmit("play", undefined);

    expect(goodListener).toHaveBeenCalledTimes(1);
  });

  it("setupVideoEventListeners wires events and fires onReady last", () => {
    const player = new TestPlayer();
    const video = createMockVideo();
    const order: string[] = [];
    const callbacks = {
      onPlay: vi.fn(),
      onPause: vi.fn(),
      onEnded: vi.fn(),
      onWaiting: vi.fn(),
      onPlaying: vi.fn(),
      onCanPlay: vi.fn(),
      onDurationChange: vi.fn(),
      onTimeUpdate: vi.fn(),
      onError: vi.fn(),
      onReady: vi.fn(() => order.push("onReady")),
    };

    player.on("ready", () => order.push("emit-ready"));
    const onPlayEmit = vi.fn();
    const onPauseEmit = vi.fn();
    const onEndedEmit = vi.fn();
    const onTimeUpdateEmit = vi.fn();
    const onErrorEmit = vi.fn();

    player.on("play", onPlayEmit);
    player.on("pause", onPauseEmit);
    player.on("ended", onEndedEmit);
    player.on("timeupdate", onTimeUpdateEmit);
    player.on("error", onErrorEmit);

    player.callSetupVideoEventListeners(video, callbacks);

    expect(order).toEqual(["emit-ready", "onReady"]);
    expect(video._listeners.has("play")).toBe(true);
    expect(video._listeners.has("pause")).toBe(true);
    expect(video._listeners.has("ended")).toBe(true);
    expect(video._listeners.has("waiting")).toBe(true);
    expect(video._listeners.has("playing")).toBe(true);
    expect(video._listeners.has("canplay")).toBe(true);
    expect(video._listeners.has("durationchange")).toBe(true);
    expect(video._listeners.has("timeupdate")).toBe(true);
    expect(video._listeners.has("error")).toBe(true);

    video._fire("play");
    expect(callbacks.onPlay).toHaveBeenCalledTimes(1);
    expect(onPlayEmit).toHaveBeenCalledTimes(1);

    video._fire("pause");
    expect(callbacks.onPause).toHaveBeenCalledTimes(1);
    expect(onPauseEmit).toHaveBeenCalledTimes(1);

    video._fire("ended");
    expect(callbacks.onEnded).toHaveBeenCalledTimes(1);
    expect(onEndedEmit).toHaveBeenCalledTimes(1);

    video._fire("waiting");
    expect(callbacks.onWaiting).toHaveBeenCalledTimes(1);

    video._fire("playing");
    expect(callbacks.onPlaying).toHaveBeenCalledTimes(1);

    video._fire("canplay");
    expect(callbacks.onCanPlay).toHaveBeenCalledTimes(1);

    video.currentTime = 12.5;
    video._fire("timeupdate");
    expect(callbacks.onTimeUpdate).toHaveBeenCalledWith(12500);
    expect(onTimeUpdateEmit).toHaveBeenCalledWith(12500);

    video.error = { message: "nope" } as any;
    video._fire("error");
    expect(callbacks.onError).toHaveBeenCalledWith("Video error: nope");
    expect(onErrorEmit).toHaveBeenCalledWith("Video error: nope");

    video.duration = 75;
    video._fire("durationchange");
    expect(callbacks.onDurationChange).toHaveBeenCalledWith(75000);
  });

  it("returns playback state defaults when no video element", async () => {
    const player = new TestPlayer();

    expect(player.getCurrentTime()).toBe(0);
    expect(player.getDuration()).toBe(0);
    expect(player.isPaused()).toBe(true);
    expect(player.isMuted()).toBe(false);

    await expect(player.play()).resolves.toBeUndefined();
    player.pause();
    player.seek(10);
    player.setVolume(0.5);
    player.setMuted(true);
    player.setPlaybackRate(1.5);
    player.setSize(100, 200);
  });

  it("delegates playback controls to video element", async () => {
    const player = new TestPlayer();
    const video = createMockVideo();
    player.setVideoElement(video);

    expect(player.getCurrentTime()).toBe(0);
    expect(player.getDuration()).toBe(100000);
    expect(player.isPaused()).toBe(false);
    expect(player.isMuted()).toBe(false);

    await player.play();
    expect(video.play).toHaveBeenCalledTimes(1);

    player.pause();
    expect(video.pause).toHaveBeenCalledTimes(1);

    player.seek(33000);
    expect(video.currentTime).toBe(33);

    player.setVolume(2);
    expect(video.volume).toBe(1);
    player.setVolume(-1);
    expect(video.volume).toBe(0);

    player.setMuted(true);
    expect(video.muted).toBe(true);

    player.setPlaybackRate(1.25);
    expect(video.playbackRate).toBe(1.25);
  });

  it("maps text tracks and selects by id", () => {
    const player = new TestPlayer();
    const video = createMockVideo();
    const tracks = [
      { label: "", language: "en", mode: "disabled" },
      { label: "English", language: "en", mode: "showing" },
    ];
    (video as any).textTracks = tracks;
    player.setVideoElement(video);

    const mapped = player.getTextTracks();
    expect(mapped).toEqual([
      { id: "0", label: "CC 1", lang: "en", active: false },
      { id: "1", label: "English", lang: "en", active: true },
    ]);

    player.selectTextTrack("0");
    expect(tracks[0].mode).toBe("showing");
    expect(tracks[1].mode).toBe("disabled");

    player.selectTextTrack(null);
    expect(tracks[0].mode).toBe("disabled");
    expect(tracks[1].mode).toBe("disabled");
  });

  it("detects live streams and jumps to live edge", () => {
    const player = new TestPlayer();
    const video = createMockVideo();
    player.setVideoElement(video);

    video.duration = Infinity;
    expect(player.isLive()).toBe(true);

    video.duration = 120;
    expect(player.isLive()).toBe(false);

    video.seekable = { length: 1, start: () => 0, end: () => 88 } as any;
    player.jumpToLive();
    expect(video.currentTime).toBe(88);
  });

  it("handles Picture-in-Picture requests", async () => {
    const player = new TestPlayer();
    const video = createMockVideo();
    player.setVideoElement(video);

    await player.requestPiP();
    expect(video.requestPictureInPicture).toHaveBeenCalledTimes(1);

    (globalThis as any).document.pictureInPictureElement = video;
    await player.requestPiP();
    expect((globalThis as any).document.exitPictureInPicture).toHaveBeenCalledTimes(1);

    (video as any).requestPictureInPicture = undefined;
    (video as any).webkitSetPresentationMode = vi.fn();
    (globalThis as any).document.pictureInPictureElement = null;
    await player.requestPiP();
    expect((video as any).webkitSetPresentationMode).toHaveBeenCalledWith("picture-in-picture");
  });

  it("sets size and returns optional stats defaults", async () => {
    const player = new TestPlayer();
    const video = createMockVideo();
    player.setVideoElement(video);

    player.setSize(320, 240);
    expect(video.style.width).toBe("320px");
    expect(video.style.height).toBe("240px");

    await expect(player.getStats()).resolves.toBeUndefined();
    await expect(player.getLatency()).resolves.toBeUndefined();
  });
});

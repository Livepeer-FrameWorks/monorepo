import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { FrameWorksPlayer } from "../src/vanilla/FrameWorksPlayer";

class MockElement {}

const { MockPlayerController } = vi.hoisted(() => {
  class MockPlayerController {
    static instances: MockPlayerController[] = [];

    config: any;
    attach = vi.fn().mockResolvedValue(undefined);
    destroy = vi.fn();
    play = vi.fn().mockResolvedValue(undefined);
    pause = vi.fn();
    seek = vi.fn();
    setVolume = vi.fn();
    setMuted = vi.fn();
    setPlaybackRate = vi.fn();
    jumpToLive = vi.fn();
    requestFullscreen = vi.fn().mockResolvedValue(undefined);
    requestPiP = vi.fn().mockResolvedValue(undefined);
    getState = vi.fn().mockReturnValue("idle");
    getStreamState = vi.fn().mockReturnValue(null);
    getVideoElement = vi.fn().mockReturnValue(null);
    isReady = vi.fn().mockReturnValue(false);
    getCurrentTime = vi.fn().mockReturnValue(0);
    getDuration = vi.fn().mockReturnValue(0);
    isPaused = vi.fn().mockReturnValue(true);
    isMuted = vi.fn().mockReturnValue(false);
    retry = vi.fn().mockResolvedValue(undefined);
    getMetadata = vi.fn().mockReturnValue({});
    getStats = vi.fn().mockResolvedValue({});
    getLatency = vi.fn().mockResolvedValue({});
    on = vi.fn((_event: string, _listener: Function) => vi.fn());

    constructor(config: any) {
      this.config = config;
      MockPlayerController.instances.push(this);
    }
  }

  return { MockPlayerController };
});

vi.mock("../src/core/PlayerController", () => ({
  PlayerController: MockPlayerController,
}));

describe("FrameWorksPlayer", () => {
  let origDocument: typeof globalThis.document;
  let origHTMLElement: any;

  beforeEach(() => {
    origDocument = globalThis.document;
    origHTMLElement = (globalThis as any).HTMLElement;
    (globalThis as any).HTMLElement = MockElement;

    (globalThis as any).document = {
      querySelector: vi.fn(() => new MockElement()),
    };
  });

  afterEach(() => {
    (globalThis as any).document = origDocument;
    (globalThis as any).HTMLElement = origHTMLElement;
    MockPlayerController.instances = [];
    vi.restoreAllMocks();
  });

  it("constructs with selector and normalizes options", () => {
    const player = new FrameWorksPlayer("#player", {
      contentId: "content",
      contentType: "vod",
      gatewayUrl: "https://gateway.test",
      autoplay: false,
      muted: false,
      controls: false,
      poster: "poster.png",
      debug: true,
    });

    const instance = MockPlayerController.instances[0];
    expect(instance.config).toEqual({
      contentId: "content",
      contentType: "vod",
      endpoints: undefined,
      gatewayUrl: "https://gateway.test",
      authToken: undefined,
      autoplay: false,
      muted: false,
      controls: false,
      poster: "poster.png",
      debug: true,
    });

    expect(player.getState()).toBe("idle");
  });

  it("supports legacy options format", () => {
    new FrameWorksPlayer(new MockElement() as any, {
      contentId: "legacy",
      contentType: "live",
      thumbnailUrl: "thumb.png",
      options: {
        gatewayUrl: "https://legacy",
        autoplay: true,
        muted: true,
        controls: true,
        debug: false,
        authToken: "token",
      },
    });

    const instance = MockPlayerController.instances[0];
    expect(instance.config.gatewayUrl).toBe("https://legacy");
    expect(instance.config.poster).toBe("thumb.png");
    expect(instance.config.authToken).toBe("token");
  });

  it("throws when container is invalid", () => {
    (globalThis as any).document.querySelector = vi.fn(() => null);
    expect(
      () =>
        new FrameWorksPlayer("#missing", {
          contentId: "content",
          contentType: "live",
        })
    ).toThrow("Container element not found");
  });

  it("wires up callbacks and delegates controls", async () => {
    const onStateChange = vi.fn();
    const onStreamStateChange = vi.fn();
    const onTimeUpdate = vi.fn();
    const onError = vi.fn();
    const onReady = vi.fn();

    const player = new FrameWorksPlayer("#player", {
      contentId: "content",
      contentType: "vod",
      onStateChange,
      onStreamStateChange,
      onTimeUpdate,
      onError,
      onReady,
    });

    const instance = MockPlayerController.instances[0];
    expect(instance.on).toHaveBeenCalledWith("stateChange", expect.any(Function));
    expect(instance.on).toHaveBeenCalledWith("streamStateChange", expect.any(Function));
    expect(instance.on).toHaveBeenCalledWith("timeUpdate", expect.any(Function));
    expect(instance.on).toHaveBeenCalledWith("error", expect.any(Function));
    expect(instance.on).toHaveBeenCalledWith("ready", expect.any(Function));

    await player.play();
    player.pause();
    player.seek(10);
    player.setVolume(0.5);
    player.setMuted(true);
    player.setPlaybackRate(1.5);
    player.jumpToLive();
    await player.requestFullscreen();
    await player.requestPiP();

    expect(instance.play).toHaveBeenCalledTimes(1);
    expect(instance.pause).toHaveBeenCalledTimes(1);
    expect(instance.seek).toHaveBeenCalledWith(10);
    expect(instance.setVolume).toHaveBeenCalledWith(0.5);
    expect(instance.setMuted).toHaveBeenCalledWith(true);
    expect(instance.setPlaybackRate).toHaveBeenCalledWith(1.5);
    expect(instance.jumpToLive).toHaveBeenCalledTimes(1);
    expect(instance.requestFullscreen).toHaveBeenCalledTimes(1);
    expect(instance.requestPiP).toHaveBeenCalledTimes(1);

    player.retry();
    player.getMetadata();
    player.getStats();
    player.getLatency();

    expect(instance.retry).toHaveBeenCalledTimes(1);
    expect(instance.getMetadata).toHaveBeenCalledTimes(1);
    expect(instance.getStats).toHaveBeenCalledTimes(1);
    expect(instance.getLatency).toHaveBeenCalledTimes(1);

    player.destroy();
    player.destroy();

    expect(instance.destroy).toHaveBeenCalledTimes(1);
  });
});

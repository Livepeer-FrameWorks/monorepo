import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { FrameWorksPlayer } from "../src/vanilla/FrameWorksPlayer";

class MockElement {
  querySelector = vi.fn(() => null);
  style = { setProperty: vi.fn() };
}

const mockApplyTheme = vi.fn();
const mockApplyThemeOverrides = vi.fn();
const mockClearTheme = vi.fn();

vi.mock("../src/core/ThemeManager", () => ({
  applyTheme: (...args: any[]) => mockApplyTheme(...args),
  applyThemeOverrides: (...args: any[]) => mockApplyThemeOverrides(...args),
  clearTheme: (...args: any[]) => mockClearTheme(...args),
}));

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
    getStreamState = vi.fn().mockReturnValue("live");
    getVideoElement = vi.fn().mockReturnValue({ seeking: false });
    isReady = vi.fn().mockReturnValue(true);
    getCurrentTime = vi.fn().mockReturnValue(12345);
    getDuration = vi.fn().mockReturnValue(99000);
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
    mockApplyTheme.mockClear();
    mockApplyThemeOverrides.mockClear();
    mockClearTheme.mockClear();
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

  it("throws when container is null", () => {
    expect(
      () =>
        new FrameWorksPlayer(null, {
          contentId: "content",
          contentType: "live",
        })
    ).toThrow("Container element not found");
  });

  describe("state getters delegate to controller", () => {
    it("getStreamState returns controller value", () => {
      const player = new FrameWorksPlayer("#player", {
        contentId: "content",
        contentType: "live",
      });
      expect(player.getStreamState()).toBe("live");
    });

    it("getVideoElement returns controller value", () => {
      const player = new FrameWorksPlayer("#player", {
        contentId: "content",
        contentType: "live",
      });
      expect(player.getVideoElement()).toEqual({ seeking: false });
    });

    it("isReady returns controller value", () => {
      const player = new FrameWorksPlayer("#player", {
        contentId: "content",
        contentType: "live",
      });
      expect(player.isReady()).toBe(true);
    });

    it("getCurrentTime returns controller value", () => {
      const player = new FrameWorksPlayer("#player", {
        contentId: "content",
        contentType: "live",
      });
      expect(player.getCurrentTime()).toBe(12345);
    });

    it("getDuration returns controller value", () => {
      const player = new FrameWorksPlayer("#player", {
        contentId: "content",
        contentType: "live",
      });
      expect(player.getDuration()).toBe(99000);
    });

    it("isPaused returns controller value", () => {
      const player = new FrameWorksPlayer("#player", {
        contentId: "content",
        contentType: "live",
      });
      expect(player.isPaused()).toBe(true);
    });

    it("isMuted returns controller value", () => {
      const player = new FrameWorksPlayer("#player", {
        contentId: "content",
        contentType: "live",
      });
      expect(player.isMuted()).toBe(false);
    });
  });

  describe("theming", () => {
    it("setTheme applies theme to container", () => {
      const player = new FrameWorksPlayer(new MockElement() as any, {
        contentId: "content",
        contentType: "live",
      });
      player.setTheme("dark");
      expect(mockApplyTheme).toHaveBeenCalledWith(expect.anything(), "dark");
    });

    it("setThemeOverrides applies overrides", () => {
      const player = new FrameWorksPlayer(new MockElement() as any, {
        contentId: "content",
        contentType: "live",
      });
      const overrides = { "--fw-accent": "#ff0" };
      player.setThemeOverrides(overrides);
      expect(mockApplyThemeOverrides).toHaveBeenCalledWith(expect.anything(), overrides);
    });

    it("clearTheme clears theme", () => {
      const player = new FrameWorksPlayer(new MockElement() as any, {
        contentId: "content",
        contentType: "live",
      });
      player.clearTheme();
      expect(mockClearTheme).toHaveBeenCalled();
    });
  });

  describe("on() subscription", () => {
    it("returns unsubscribe function from controller.on", () => {
      const unsubFn = vi.fn();
      const player = new FrameWorksPlayer("#player", {
        contentId: "content",
        contentType: "live",
      });
      const instance = MockPlayerController.instances[0];
      instance.on.mockReturnValue(unsubFn);

      const unsub = player.on("stateChange", vi.fn());
      expect(typeof unsub).toBe("function");
    });
  });

  describe("cleanup callbacks in destroy", () => {
    it("calls cleanup functions registered by callbacks", () => {
      const unsubFns: ReturnType<typeof vi.fn>[] = [];

      const player = new FrameWorksPlayer("#player", {
        contentId: "content",
        contentType: "live",
        onStateChange: vi.fn(),
        onStreamStateChange: vi.fn(),
        onTimeUpdate: vi.fn(),
        onError: vi.fn(),
        onReady: vi.fn(),
      });

      const instance = MockPlayerController.instances[MockPlayerController.instances.length - 1];
      // Collect unsub fns returned by on() calls during construction
      for (const call of instance.on.mock.results) {
        if (call.type === "return" && typeof call.value === "function") {
          unsubFns.push(call.value);
        }
      }

      expect(unsubFns.length).toBeGreaterThan(0);
      player.destroy();
      for (const unsub of unsubFns) {
        expect(unsub).toHaveBeenCalled();
      }
    });
  });
});

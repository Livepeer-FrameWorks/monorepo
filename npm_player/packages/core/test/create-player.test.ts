import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

// Hoisted mocks
const {
  MockPlayerController,
  mockReactiveState,
  mockApplyTheme,
  mockApplyThemeOverrides,
  mockClearTheme,
  mockBuildStructure,
  mockResolveSkin,
  mockRegisterSkin,
  mockCreateTranslator,
} = vi.hoisted(() => {
  class MockPlayerController {
    static instances: MockPlayerController[] = [];

    config: any;
    attach = vi.fn().mockResolvedValue(undefined);
    destroy = vi.fn();
    play = vi.fn().mockResolvedValue(undefined);
    pause = vi.fn();
    seek = vi.fn();
    seekBy = vi.fn();
    skipForward = vi.fn();
    skipBack = vi.fn();
    jumpToLive = vi.fn();
    setVolume = vi.fn();
    setMuted = vi.fn();
    setPlaybackRate = vi.fn();
    setLoopEnabled = vi.fn();
    setABRMode = vi.fn();
    setRotation = vi.fn();
    setMirror = vi.fn();
    getState = vi.fn().mockReturnValue("idle");
    getStreamState = vi.fn().mockReturnValue(null);
    getEndpoints = vi.fn().mockReturnValue(null);
    getMetadata = vi.fn().mockReturnValue(null);
    getStreamInfo = vi.fn().mockReturnValue(null);
    getVideoElement = vi.fn().mockReturnValue(null);
    isReady = vi.fn().mockReturnValue(false);
    getCurrentTime = vi.fn().mockReturnValue(0);
    getDuration = vi.fn().mockReturnValue(0);
    getVolume = vi.fn().mockReturnValue(1);
    isMuted = vi.fn().mockReturnValue(false);
    isPaused = vi.fn().mockReturnValue(true);
    isPlaying = vi.fn().mockReturnValue(false);
    isBuffering = vi.fn().mockReturnValue(false);
    hasPlaybackStarted = vi.fn().mockReturnValue(false);
    getPlaybackRate = vi.fn().mockReturnValue(1);
    isLoopEnabled = vi.fn().mockReturnValue(false);
    isEffectivelyLive = vi.fn().mockReturnValue(false);
    isNearLive = vi.fn().mockReturnValue(false);
    isFullscreen = vi.fn().mockReturnValue(false);
    isPiPActive = vi.fn().mockReturnValue(false);
    isPiPSupported = vi.fn().mockReturnValue(false);
    getError = vi.fn().mockReturnValue(null);
    getPlaybackQuality = vi.fn().mockReturnValue(null);
    getABRMode = vi.fn().mockReturnValue("auto");
    getCurrentPlayerInfo = vi.fn().mockReturnValue(null);
    getCurrentSourceInfo = vi.fn().mockReturnValue(null);
    canSeekStream = vi.fn().mockReturnValue(true);
    canAdjustPlaybackRate = vi.fn().mockReturnValue(true);
    hasAudioTrack = vi.fn().mockReturnValue(true);
    isDirectRendering = vi.fn().mockReturnValue(false);
    getQualities = vi.fn().mockReturnValue([]);
    selectQuality = vi.fn();
    getTextTracks = vi.fn().mockReturnValue([]);
    selectTextTrack = vi.fn();
    getAudioTracks = vi.fn().mockReturnValue([]);
    selectAudioTrack = vi.fn();
    getTracks = vi.fn().mockReturnValue([]);
    togglePlay = vi.fn();
    toggleMute = vi.fn();
    toggleLoop = vi.fn();
    toggleFullscreen = vi.fn().mockResolvedValue(undefined);
    togglePictureInPicture = vi.fn().mockResolvedValue(undefined);
    requestFullscreen = vi.fn().mockResolvedValue(undefined);
    requestPiP = vi.fn().mockResolvedValue(undefined);
    retry = vi.fn().mockResolvedValue(undefined);
    retryWithFallback = vi.fn().mockResolvedValue(false);
    reload = vi.fn().mockResolvedValue(undefined);
    clearError = vi.fn();
    getStats = vi.fn().mockResolvedValue({});
    snapshot = vi.fn().mockReturnValue(null);
    on = vi.fn((_event: string, _listener: Function) => vi.fn());

    constructor(config: any) {
      this.config = config;
      MockPlayerController.instances.push(this);
    }
  }

  const mockReactiveState = {
    on: vi.fn(() => () => {}),
    get: vi.fn(),
    off: vi.fn(),
  };

  return {
    MockPlayerController,
    mockReactiveState,
    mockApplyTheme: vi.fn(),
    mockApplyThemeOverrides: vi.fn(),
    mockClearTheme: vi.fn(),
    mockBuildStructure: vi.fn(() => null),
    mockResolveSkin: vi.fn(() => ({
      structure: { type: "container" },
      blueprints: {},
      icons: {},
      tokens: {},
      css: "",
    })),
    mockRegisterSkin: vi.fn(),
    mockCreateTranslator: vi.fn(() => (key: string, fallback?: string) => fallback ?? key),
  };
});

vi.mock("../src/core/PlayerController", () => ({
  PlayerController: MockPlayerController,
}));

vi.mock("../src/vanilla/ReactiveState", () => ({
  createReactiveState: () => mockReactiveState,
}));

vi.mock("../src/core/ThemeManager", () => ({
  applyTheme: (...args: any[]) => mockApplyTheme(...args),
  applyThemeOverrides: (...args: any[]) => mockApplyThemeOverrides(...args),
  clearTheme: (...args: any[]) => mockClearTheme(...args),
}));

vi.mock("../src/vanilla/StructureBuilder", () => ({
  buildStructure: (...args: any[]) => mockBuildStructure(...args),
}));

vi.mock("../src/vanilla/SkinRegistry", () => ({
  resolveSkin: (...args: any[]) => mockResolveSkin(...args),
  registerSkin: (...args: any[]) => mockRegisterSkin(...args),
}));

vi.mock("../src/core/I18n", () => ({
  createTranslator: (...args: any[]) => mockCreateTranslator(...args),
}));

vi.mock("../src/vanilla/defaultBlueprints", () => ({
  DEFAULT_BLUEPRINTS: { play: () => null },
}));

vi.mock("../src/vanilla/defaultStructure", () => ({
  DEFAULT_STRUCTURE: { type: "container" },
}));

import { createPlayer, type CreatePlayerConfig } from "../src/vanilla/createPlayer";

describe("createPlayer", () => {
  let origDocument: any;
  let origWindow: any;
  let mockContainer: any;

  function makeContainer() {
    return {
      querySelector: vi.fn(() => null),
      firstChild: null,
      appendChild: vi.fn(),
      style: { setProperty: vi.fn() },
      clientWidth: 800,
      clientHeight: 600,
    };
  }

  beforeEach(() => {
    origDocument = (globalThis as any).document;
    origWindow = (globalThis as any).window;

    mockContainer = makeContainer();

    (globalThis as any).document = {
      querySelector: vi.fn(() => mockContainer),
      createElement: vi.fn((tag: string) => ({
        tagName: tag,
        textContent: "",
        style: { setProperty: vi.fn() },
        appendChild: vi.fn(),
      })),
      fullscreenEnabled: true,
      exitFullscreen: vi.fn(),
    };

    (globalThis as any).window = {
      setTimeout: vi.fn((fn: Function) => 1),
      clearTimeout: vi.fn(),
      setInterval: vi.fn((fn: Function) => 2),
      clearInterval: vi.fn(),
    };

    MockPlayerController.instances = [];
    mockApplyTheme.mockClear();
    mockApplyThemeOverrides.mockClear();
    mockClearTheme.mockClear();
    mockBuildStructure.mockClear();
    mockResolveSkin.mockClear();
    mockRegisterSkin.mockClear();
    mockReactiveState.on.mockClear();
    mockReactiveState.off.mockClear();
  });

  afterEach(() => {
    (globalThis as any).document = origDocument;
    (globalThis as any).window = origWindow;
    vi.restoreAllMocks();
  });

  function makeConfig(overrides: Partial<CreatePlayerConfig> = {}): CreatePlayerConfig {
    return {
      target: "#player",
      contentId: "test-stream",
      ...overrides,
    };
  }

  describe("container resolution", () => {
    it("resolves container from CSS selector", () => {
      const player = createPlayer(makeConfig());
      expect((globalThis as any).document.querySelector).toHaveBeenCalledWith("#player");
      expect(player).toBeDefined();
    });

    it("throws when selector not found", () => {
      (globalThis as any).document.querySelector = vi.fn(() => null);
      expect(() => createPlayer(makeConfig())).toThrow("element not found");
    });

    it("accepts HTMLElement directly", () => {
      const el = makeContainer();
      const player = createPlayer(makeConfig({ target: el as any }));
      expect(player).toBeDefined();
    });
  });

  describe("controller config", () => {
    it("creates PlayerController with normalized config", () => {
      createPlayer(
        makeConfig({
          contentId: "my-stream",
          contentType: "live",
          gatewayUrl: "https://gw.test",
          mistUrl: "https://mist.test",
          authToken: "token123",
          autoplay: false,
          muted: true,
          controls: false,
          poster: "poster.png",
          debug: true,
          playbackMode: "low-latency",
        })
      );

      const ctrl = MockPlayerController.instances[0];
      expect(ctrl.config).toEqual({
        contentId: "my-stream",
        contentType: "live",
        endpoints: undefined,
        gatewayUrl: "https://gw.test",
        mistUrl: "https://mist.test",
        authToken: "token123",
        autoplay: false,
        muted: true,
        controls: false,
        poster: "poster.png",
        debug: true,
        playbackMode: "low-latency",
      });
    });

    it("applies defaults: autoplay true, muted false, controls true", () => {
      createPlayer(makeConfig());
      const ctrl = MockPlayerController.instances[0];
      expect(ctrl.config.autoplay).toBe(true);
      expect(ctrl.config.muted).toBe(false);
      expect(ctrl.config.controls).toBe(true);
    });
  });

  describe("getter properties", () => {
    it("playerState delegates to getState", () => {
      const player = createPlayer(makeConfig());
      MockPlayerController.instances[0].getState.mockReturnValue("playing");
      expect(player.playerState).toBe("playing");
    });

    it("state delegates to getState (deprecated)", () => {
      const player = createPlayer(makeConfig());
      MockPlayerController.instances[0].getState.mockReturnValue("buffering");
      expect(player.state).toBe("buffering");
    });

    it("subscribe returns reactiveState", () => {
      const player = createPlayer(makeConfig());
      expect(player.subscribe).toBe(mockReactiveState);
    });

    it("streamState delegates to getStreamState", () => {
      const player = createPlayer(makeConfig());
      MockPlayerController.instances[0].getStreamState.mockReturnValue("active");
      expect(player.streamState).toBe("active");
    });

    it("endpoints delegates to getEndpoints", () => {
      const player = createPlayer(makeConfig());
      const ep = { ingest: "url" };
      MockPlayerController.instances[0].getEndpoints.mockReturnValue(ep);
      expect(player.endpoints).toBe(ep);
    });

    it("metadata delegates to getMetadata", () => {
      const player = createPlayer(makeConfig());
      const md = { title: "test" };
      MockPlayerController.instances[0].getMetadata.mockReturnValue(md);
      expect(player.metadata).toBe(md);
    });

    it("streamInfo delegates to getStreamInfo", () => {
      const player = createPlayer(makeConfig());
      MockPlayerController.instances[0].getStreamInfo.mockReturnValue({ sources: [] });
      expect(player.streamInfo).toEqual({ sources: [] });
    });

    it("videoElement delegates to getVideoElement", () => {
      const player = createPlayer(makeConfig());
      const video = {};
      MockPlayerController.instances[0].getVideoElement.mockReturnValue(video);
      expect(player.videoElement).toBe(video);
    });

    it("ready delegates to isReady", () => {
      const player = createPlayer(makeConfig());
      MockPlayerController.instances[0].isReady.mockReturnValue(true);
      expect(player.ready).toBe(true);
    });

    it("currentTime delegates to getCurrentTime", () => {
      const player = createPlayer(makeConfig());
      MockPlayerController.instances[0].getCurrentTime.mockReturnValue(5000);
      expect(player.currentTime).toBe(5000);
    });

    it("duration delegates to getDuration", () => {
      const player = createPlayer(makeConfig());
      MockPlayerController.instances[0].getDuration.mockReturnValue(60000);
      expect(player.duration).toBe(60000);
    });

    it("volume getter delegates to getVolume", () => {
      const player = createPlayer(makeConfig());
      MockPlayerController.instances[0].getVolume.mockReturnValue(0.7);
      expect(player.volume).toBe(0.7);
    });

    it("muted getter delegates to isMuted", () => {
      const player = createPlayer(makeConfig());
      MockPlayerController.instances[0].isMuted.mockReturnValue(true);
      expect(player.muted).toBe(true);
    });

    it("paused delegates to isPaused", () => {
      const player = createPlayer(makeConfig());
      MockPlayerController.instances[0].isPaused.mockReturnValue(false);
      expect(player.paused).toBe(false);
    });

    it("playing delegates to isPlaying", () => {
      const player = createPlayer(makeConfig());
      MockPlayerController.instances[0].isPlaying.mockReturnValue(true);
      expect(player.playing).toBe(true);
    });

    it("buffering delegates to isBuffering", () => {
      const player = createPlayer(makeConfig());
      MockPlayerController.instances[0].isBuffering.mockReturnValue(true);
      expect(player.buffering).toBe(true);
    });

    it("started delegates to hasPlaybackStarted", () => {
      const player = createPlayer(makeConfig());
      MockPlayerController.instances[0].hasPlaybackStarted.mockReturnValue(true);
      expect(player.started).toBe(true);
    });

    it("playbackRate getter delegates to getPlaybackRate", () => {
      const player = createPlayer(makeConfig());
      MockPlayerController.instances[0].getPlaybackRate.mockReturnValue(2);
      expect(player.playbackRate).toBe(2);
    });

    it("loop getter delegates to isLoopEnabled", () => {
      const player = createPlayer(makeConfig());
      MockPlayerController.instances[0].isLoopEnabled.mockReturnValue(true);
      expect(player.loop).toBe(true);
    });

    it("live delegates to isEffectivelyLive", () => {
      const player = createPlayer(makeConfig());
      MockPlayerController.instances[0].isEffectivelyLive.mockReturnValue(true);
      expect(player.live).toBe(true);
    });

    it("nearLive delegates to isNearLive", () => {
      const player = createPlayer(makeConfig());
      MockPlayerController.instances[0].isNearLive.mockReturnValue(true);
      expect(player.nearLive).toBe(true);
    });

    it("fullscreen delegates to isFullscreen", () => {
      const player = createPlayer(makeConfig());
      MockPlayerController.instances[0].isFullscreen.mockReturnValue(true);
      expect(player.fullscreen).toBe(true);
    });

    it("pip delegates to isPiPActive", () => {
      const player = createPlayer(makeConfig());
      MockPlayerController.instances[0].isPiPActive.mockReturnValue(true);
      expect(player.pip).toBe(true);
    });

    it("error delegates to getError", () => {
      const player = createPlayer(makeConfig());
      MockPlayerController.instances[0].getError.mockReturnValue("oops");
      expect(player.error).toBe("oops");
    });

    it("quality delegates to getPlaybackQuality", () => {
      const player = createPlayer(makeConfig());
      const q = { bitrate: 5000 };
      MockPlayerController.instances[0].getPlaybackQuality.mockReturnValue(q);
      expect(player.quality).toBe(q);
    });

    it("abrMode getter delegates to getABRMode", () => {
      const player = createPlayer(makeConfig());
      MockPlayerController.instances[0].getABRMode.mockReturnValue("manual");
      expect(player.abrMode).toBe("manual");
    });

    it("playerInfo delegates to getCurrentPlayerInfo", () => {
      const player = createPlayer(makeConfig());
      const info = { name: "HLS", shortname: "hls" };
      MockPlayerController.instances[0].getCurrentPlayerInfo.mockReturnValue(info);
      expect(player.playerInfo).toBe(info);
    });

    it("sourceInfo delegates to getCurrentSourceInfo", () => {
      const player = createPlayer(makeConfig());
      const src = { url: "http://test", type: "hls" };
      MockPlayerController.instances[0].getCurrentSourceInfo.mockReturnValue(src);
      expect(player.sourceInfo).toBe(src);
    });

    it("directRendering delegates to isDirectRendering", () => {
      const player = createPlayer(makeConfig());
      MockPlayerController.instances[0].isDirectRendering.mockReturnValue(true);
      expect(player.directRendering).toBe(true);
    });

    it("size returns video element dimensions", () => {
      const player = createPlayer(makeConfig());
      MockPlayerController.instances[0].getVideoElement.mockReturnValue({
        clientWidth: 1920,
        clientHeight: 1080,
      });
      expect(player.size).toEqual({ width: 1920, height: 1080 });
    });

    it("size falls back to container dimensions", () => {
      const player = createPlayer(makeConfig());
      MockPlayerController.instances[0].getVideoElement.mockReturnValue(null);
      expect(player.size).toEqual({ width: 800, height: 600 });
    });
  });

  describe("setter properties", () => {
    it("volume setter delegates to setVolume", () => {
      const player = createPlayer(makeConfig());
      player.volume = 0.3;
      expect(MockPlayerController.instances[0].setVolume).toHaveBeenCalledWith(0.3);
    });

    it("muted setter delegates to setMuted", () => {
      const player = createPlayer(makeConfig());
      player.muted = true;
      expect(MockPlayerController.instances[0].setMuted).toHaveBeenCalledWith(true);
    });

    it("playbackRate setter delegates to setPlaybackRate", () => {
      const player = createPlayer(makeConfig());
      player.playbackRate = 2;
      expect(MockPlayerController.instances[0].setPlaybackRate).toHaveBeenCalledWith(2);
    });

    it("loop setter delegates to setLoopEnabled", () => {
      const player = createPlayer(makeConfig());
      player.loop = true;
      expect(MockPlayerController.instances[0].setLoopEnabled).toHaveBeenCalledWith(true);
    });

    it("abrMode setter delegates to setABRMode", () => {
      const player = createPlayer(makeConfig());
      player.abrMode = "manual";
      expect(MockPlayerController.instances[0].setABRMode).toHaveBeenCalledWith("manual");
    });
  });

  describe("theme setter", () => {
    it("applies theme when set to non-default", () => {
      const player = createPlayer(makeConfig());
      player.theme = "dark";
      expect(mockClearTheme).toHaveBeenCalled();
      expect(mockApplyTheme).toHaveBeenCalledWith(expect.anything(), "dark");
    });

    it("only clears when set to default", () => {
      const player = createPlayer(makeConfig());
      player.theme = "default";
      expect(mockClearTheme).toHaveBeenCalled();
      expect(mockApplyTheme).not.toHaveBeenCalled();
    });

    it("only clears when set to undefined", () => {
      const player = createPlayer(makeConfig());
      player.theme = undefined;
      expect(mockClearTheme).toHaveBeenCalled();
      expect(mockApplyTheme).not.toHaveBeenCalled();
    });
  });

  describe("mutation methods", () => {
    it("play delegates", async () => {
      const player = createPlayer(makeConfig());
      await player.play();
      expect(MockPlayerController.instances[0].play).toHaveBeenCalled();
    });

    it("pause delegates", () => {
      const player = createPlayer(makeConfig());
      player.pause();
      expect(MockPlayerController.instances[0].pause).toHaveBeenCalled();
    });

    it("seek delegates", () => {
      const player = createPlayer(makeConfig());
      player.seek(5000);
      expect(MockPlayerController.instances[0].seek).toHaveBeenCalledWith(5000);
    });

    it("seekBy delegates", () => {
      const player = createPlayer(makeConfig());
      player.seekBy(-3000);
      expect(MockPlayerController.instances[0].seekBy).toHaveBeenCalledWith(-3000);
    });

    it("jumpToLive delegates", () => {
      const player = createPlayer(makeConfig());
      player.jumpToLive();
      expect(MockPlayerController.instances[0].jumpToLive).toHaveBeenCalled();
    });

    it("skipForward delegates", () => {
      const player = createPlayer(makeConfig());
      player.skipForward(5000);
      expect(MockPlayerController.instances[0].skipForward).toHaveBeenCalledWith(5000);
    });

    it("skipBack delegates", () => {
      const player = createPlayer(makeConfig());
      player.skipBack(5000);
      expect(MockPlayerController.instances[0].skipBack).toHaveBeenCalledWith(5000);
    });

    it("togglePlay delegates", () => {
      const player = createPlayer(makeConfig());
      player.togglePlay();
      expect(MockPlayerController.instances[0].togglePlay).toHaveBeenCalled();
    });

    it("toggleMute delegates", () => {
      const player = createPlayer(makeConfig());
      player.toggleMute();
      expect(MockPlayerController.instances[0].toggleMute).toHaveBeenCalled();
    });

    it("toggleLoop delegates", () => {
      const player = createPlayer(makeConfig());
      player.toggleLoop();
      expect(MockPlayerController.instances[0].toggleLoop).toHaveBeenCalled();
    });

    it("toggleFullscreen delegates", async () => {
      const player = createPlayer(makeConfig());
      await player.toggleFullscreen();
      expect(MockPlayerController.instances[0].toggleFullscreen).toHaveBeenCalled();
    });

    it("togglePiP delegates", async () => {
      const player = createPlayer(makeConfig());
      await player.togglePiP();
      expect(MockPlayerController.instances[0].togglePictureInPicture).toHaveBeenCalled();
    });

    it("requestFullscreen delegates", async () => {
      const player = createPlayer(makeConfig());
      await player.requestFullscreen();
      expect(MockPlayerController.instances[0].requestFullscreen).toHaveBeenCalled();
    });

    it("requestPiP delegates", async () => {
      const player = createPlayer(makeConfig());
      await player.requestPiP();
      expect(MockPlayerController.instances[0].requestPiP).toHaveBeenCalled();
    });

    it("selectQuality delegates", () => {
      const player = createPlayer(makeConfig());
      player.selectQuality("720p");
      expect(MockPlayerController.instances[0].selectQuality).toHaveBeenCalledWith("720p");
    });

    it("selectTextTrack delegates", () => {
      const player = createPlayer(makeConfig());
      player.selectTextTrack("en");
      expect(MockPlayerController.instances[0].selectTextTrack).toHaveBeenCalledWith("en");
    });

    it("selectAudioTrack delegates", () => {
      const player = createPlayer(makeConfig());
      player.selectAudioTrack("stereo");
      expect(MockPlayerController.instances[0].selectAudioTrack).toHaveBeenCalledWith("stereo");
    });

    it("retry delegates", async () => {
      const player = createPlayer(makeConfig());
      await player.retry();
      expect(MockPlayerController.instances[0].retry).toHaveBeenCalled();
    });

    it("retryWithFallback delegates", async () => {
      const player = createPlayer(makeConfig());
      await player.retryWithFallback();
      expect(MockPlayerController.instances[0].retryWithFallback).toHaveBeenCalled();
    });

    it("reload delegates", async () => {
      const player = createPlayer(makeConfig());
      await player.reload();
      expect(MockPlayerController.instances[0].reload).toHaveBeenCalled();
    });

    it("clearError delegates", () => {
      const player = createPlayer(makeConfig());
      player.clearError();
      expect(MockPlayerController.instances[0].clearError).toHaveBeenCalled();
    });

    it("snapshot delegates", () => {
      const player = createPlayer(makeConfig());
      player.snapshot("jpeg", 0.9);
      expect(MockPlayerController.instances[0].snapshot).toHaveBeenCalledWith("jpeg", 0.9);
    });

    it("setRotation delegates", () => {
      const player = createPlayer(makeConfig());
      player.setRotation(90);
      expect(MockPlayerController.instances[0].setRotation).toHaveBeenCalledWith(90);
    });

    it("setMirror delegates", () => {
      const player = createPlayer(makeConfig());
      player.setMirror(true);
      expect(MockPlayerController.instances[0].setMirror).toHaveBeenCalledWith(true);
    });

    it("setThemeOverrides delegates", () => {
      const player = createPlayer(makeConfig());
      player.setThemeOverrides({ "--fw-accent": "#f00" });
      expect(mockApplyThemeOverrides).toHaveBeenCalled();
    });

    it("clearTheme resets and delegates", () => {
      const player = createPlayer(makeConfig());
      player.clearTheme();
      expect(mockClearTheme).toHaveBeenCalled();
    });
  });

  describe("subscriptions", () => {
    it("on delegates to controller.on", () => {
      const player = createPlayer(makeConfig());
      const listener = vi.fn();
      player.on("stateChange", listener);
      expect(MockPlayerController.instances[0].on).toHaveBeenCalledWith("stateChange", listener);
    });
  });

  describe("destroy", () => {
    it("cleans up reactive state, theme, and controller", () => {
      const player = createPlayer(makeConfig());
      player.destroy();

      expect(mockReactiveState.off).toHaveBeenCalled();
      expect(mockClearTheme).toHaveBeenCalled();
      expect(MockPlayerController.instances[0].destroy).toHaveBeenCalled();
    });

    it("is idempotent", () => {
      const player = createPlayer(makeConfig());
      player.destroy();
      player.destroy();

      expect(MockPlayerController.instances[0].destroy).toHaveBeenCalledTimes(1);
    });
  });

  describe("skin rendering", () => {
    it("skin=false skips rendering", () => {
      createPlayer(makeConfig({ skin: false }));
      expect(mockResolveSkin).not.toHaveBeenCalled();
    });

    it("controls=false skips rendering", () => {
      createPlayer(makeConfig({ controls: false }));
      expect(mockResolveSkin).not.toHaveBeenCalled();
    });

    it("resolves named skin", () => {
      createPlayer(makeConfig({ skin: "dark" }));
      expect(mockResolveSkin).toHaveBeenCalledWith("dark");
    });

    it("resolves default skin when no skin specified", () => {
      createPlayer(makeConfig());
      expect(mockResolveSkin).toHaveBeenCalledWith("default");
    });

    it("handles inline skin definition with inherit", () => {
      const skinDef = { inherit: "default", tokens: { "--fw-accent": "#f00" } };
      createPlayer(makeConfig({ skin: skinDef }));
      expect(mockRegisterSkin).toHaveBeenCalledWith(expect.stringContaining("__inline_"), skinDef);
      expect(mockResolveSkin).toHaveBeenCalled();
    });

    it("handles inline skin definition without inherit", () => {
      const skinDef = { tokens: { "--fw-accent": "#f00" }, blueprints: {} };
      createPlayer(makeConfig({ skin: skinDef as any }));
      // Should NOT call resolveSkin for non-inheriting inline skins
      expect(mockResolveSkin).not.toHaveBeenCalled();
    });
  });

  describe("capabilities", () => {
    it("returns capability object", () => {
      const player = createPlayer(makeConfig());
      const caps = player.capabilities;
      expect(caps).toHaveProperty("fullscreen");
      expect(caps).toHaveProperty("pip");
      expect(caps).toHaveProperty("seeking");
      expect(caps).toHaveProperty("playbackRate");
      expect(caps).toHaveProperty("audio");
      expect(caps).toHaveProperty("qualitySelection");
      expect(caps).toHaveProperty("textTracks");
    });
  });

  describe("track getters", () => {
    it("getQualities delegates", () => {
      const player = createPlayer(makeConfig());
      player.getQualities();
      expect(MockPlayerController.instances[0].getQualities).toHaveBeenCalled();
    });

    it("getTextTracks delegates", () => {
      const player = createPlayer(makeConfig());
      player.getTextTracks();
      expect(MockPlayerController.instances[0].getTextTracks).toHaveBeenCalled();
    });

    it("getAudioTracks delegates", () => {
      const player = createPlayer(makeConfig());
      player.getAudioTracks();
      expect(MockPlayerController.instances[0].getAudioTracks).toHaveBeenCalled();
    });

    it("getTracks delegates", () => {
      const player = createPlayer(makeConfig());
      player.getTracks();
      expect(MockPlayerController.instances[0].getTracks).toHaveBeenCalled();
    });

    it("getStats delegates", async () => {
      const player = createPlayer(makeConfig());
      await player.getStats();
      expect(MockPlayerController.instances[0].getStats).toHaveBeenCalled();
    });
  });
});

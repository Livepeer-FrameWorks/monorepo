import { describe, it, expect, vi, beforeEach } from "vitest";
import { renderHook, act } from "@testing-library/react";
import { usePlayerController } from "../src/hooks/usePlayerController";

// ---------------------------------------------------------------------------
// Event-capturing mock
// ---------------------------------------------------------------------------

let eventHandlers: Map<string, Function[]>;

const mockAttach = vi.fn().mockResolvedValue(undefined);
const mockDestroy = vi.fn();
const mockPlay = vi.fn().mockResolvedValue(undefined);
const mockPause = vi.fn();
const mockTogglePlay = vi.fn();
const mockSeek = vi.fn();
const mockSeekBy = vi.fn();
const mockSetVolume = vi.fn();
const mockToggleMute = vi.fn();
const mockToggleLoop = vi.fn();
const mockToggleFullscreen = vi.fn().mockResolvedValue(undefined);
const mockTogglePictureInPicture = vi.fn().mockResolvedValue(undefined);
const mockToggleSubtitles = vi.fn();
const mockClearError = vi.fn();
const mockRetry = vi.fn().mockResolvedValue(undefined);
const mockReload = vi.fn().mockResolvedValue(undefined);
const mockGetQualities = vi.fn().mockReturnValue([]);
const mockSelectQuality = vi.fn();
const mockHandleMouseEnter = vi.fn();
const mockHandleMouseLeave = vi.fn();
const mockHandleMouseMove = vi.fn();
const mockHandleTouchStart = vi.fn();
const mockSetDevModeOptions = vi.fn().mockResolvedValue(undefined);
const mockJumpToLive = vi.fn();
const mockIsPlaying = vi.fn().mockReturnValue(false);
const mockIsPaused = vi.fn().mockReturnValue(true);
const mockIsBuffering = vi.fn().mockReturnValue(false);
const mockIsMuted = vi.fn().mockReturnValue(true);
const mockGetVolume = vi.fn().mockReturnValue(1);
const mockHasPlaybackStarted = vi.fn().mockReturnValue(false);
const mockShouldShowControls = vi.fn().mockReturnValue(false);
const mockShouldShowIdleScreen = vi.fn().mockReturnValue(true);
const mockGetPlaybackQuality = vi.fn().mockReturnValue(null);
const mockIsLoopEnabled = vi.fn().mockReturnValue(false);
const mockIsSubtitlesEnabled = vi.fn().mockReturnValue(false);
const mockIsPassiveError = vi.fn().mockReturnValue(false);
const mockGetStreamInfo = vi.fn().mockReturnValue(null);
const mockIsEffectivelyLive = vi.fn().mockReturnValue(false);
const mockGetEndpoints = vi.fn().mockReturnValue(null);
const mockGetMetadata = vi.fn().mockReturnValue(null);
const mockGetCurrentPlayerInfo = vi.fn().mockReturnValue(null);
const mockGetCurrentSourceInfo = vi.fn().mockReturnValue(null);
const mockShouldSuppressVideoEvents = vi.fn().mockReturnValue(false);

const mockOn = vi.fn((event: string, handler: Function) => {
  if (!eventHandlers.has(event)) eventHandlers.set(event, []);
  eventHandlers.get(event)!.push(handler);
  return () => {
    const handlers = eventHandlers.get(event);
    if (handlers) {
      const idx = handlers.indexOf(handler);
      if (idx >= 0) handlers.splice(idx, 1);
    }
  };
});

vi.mock("@livepeer-frameworks/player-core", () => ({
  PlayerController: vi.fn().mockImplementation(function (this: Record<string, unknown>) {
    Object.assign(this, {
      attach: mockAttach,
      destroy: mockDestroy,
      play: mockPlay,
      pause: mockPause,
      togglePlay: mockTogglePlay,
      seek: mockSeek,
      seekBy: mockSeekBy,
      setVolume: mockSetVolume,
      toggleMute: mockToggleMute,
      toggleLoop: mockToggleLoop,
      toggleFullscreen: mockToggleFullscreen,
      togglePictureInPicture: mockTogglePictureInPicture,
      toggleSubtitles: mockToggleSubtitles,
      clearError: mockClearError,
      retry: mockRetry,
      reload: mockReload,
      getQualities: mockGetQualities,
      selectQuality: mockSelectQuality,
      handleMouseEnter: mockHandleMouseEnter,
      handleMouseLeave: mockHandleMouseLeave,
      handleMouseMove: mockHandleMouseMove,
      handleTouchStart: mockHandleTouchStart,
      setDevModeOptions: mockSetDevModeOptions,
      jumpToLive: mockJumpToLive,
      on: mockOn,
      isPlaying: mockIsPlaying,
      isPaused: mockIsPaused,
      isBuffering: mockIsBuffering,
      isMuted: mockIsMuted,
      getVolume: mockGetVolume,
      hasPlaybackStarted: mockHasPlaybackStarted,
      shouldShowControls: mockShouldShowControls,
      shouldShowIdleScreen: mockShouldShowIdleScreen,
      getPlaybackQuality: mockGetPlaybackQuality,
      isLoopEnabled: mockIsLoopEnabled,
      isSubtitlesEnabled: mockIsSubtitlesEnabled,
      isPassiveError: mockIsPassiveError,
      getStreamInfo: mockGetStreamInfo,
      isEffectivelyLive: mockIsEffectivelyLive,
      getEndpoints: mockGetEndpoints,
      getMetadata: mockGetMetadata,
      getCurrentPlayerInfo: mockGetCurrentPlayerInfo,
      getCurrentSourceInfo: mockGetCurrentSourceInfo,
      shouldSuppressVideoEvents: mockShouldSuppressVideoEvents,
    });
  }),
  cn: (...args: string[]) => args.filter(Boolean).join(" "),
}));

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function fire(event: string, data?: unknown): void {
  const handlers = eventHandlers.get(event);
  if (handlers) handlers.forEach((h) => h(data));
}

/**
 * Creates the hook and triggers the controller-creation effect by:
 * 1. Rendering with an initial contentId (effect runs but returns early — no container)
 * 2. Setting containerRef.current to a real div
 * 3. Re-rendering with a new contentId so the effect re-runs (now with a container)
 */
function renderWithController() {
  const hook = renderHook(
    ({ contentId }: { contentId: string }) =>
      usePlayerController({ contentId, contentType: "live" }),
    { initialProps: { contentId: "stream-1" } }
  );

  // Set containerRef to a real DOM element
  (hook.result.current.containerRef as React.MutableRefObject<HTMLDivElement>).current =
    document.createElement("div");

  // Force effect re-run by changing a dependency
  hook.rerender({ contentId: "stream-2" });

  return hook;
}

beforeEach(() => {
  vi.clearAllMocks();
  eventHandlers = new Map();
});

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("usePlayerController", () => {
  it("returns initial state before mount", () => {
    const { result } = renderHook(() =>
      usePlayerController({
        contentId: "test-stream",
        contentType: "live",
      })
    );

    expect(result.current.state.state).toBe("booting");
    expect(result.current.state.isPlaying).toBe(false);
    expect(result.current.state.isPaused).toBe(true);
    expect(result.current.state.isMuted).toBe(true);
    expect(result.current.state.volume).toBe(1);
    expect(result.current.state.error).toBeNull();
    expect(result.current.state.videoElement).toBeNull();
    expect(result.current.state.shouldShowIdleScreen).toBe(true);
  });

  it("provides stable action callbacks", () => {
    const { result, rerender } = renderHook(() =>
      usePlayerController({
        contentId: "test-stream",
        contentType: "live",
      })
    );

    const firstPlay = result.current.play;
    const firstPause = result.current.pause;
    const firstTogglePlay = result.current.togglePlay;

    rerender();

    expect(result.current.play).toBe(firstPlay);
    expect(result.current.pause).toBe(firstPause);
    expect(result.current.togglePlay).toBe(firstTogglePlay);
  });

  it("provides a containerRef", () => {
    const { result } = renderHook(() =>
      usePlayerController({
        contentId: "test-stream",
        contentType: "live",
      })
    );

    expect(result.current.containerRef).toBeDefined();
    expect(result.current.containerRef.current).toBeNull();
  });

  it("does not create controller when disabled", async () => {
    const { PlayerController } = await import("@livepeer-frameworks/player-core");

    vi.mocked(PlayerController).mockClear();

    renderHook(() =>
      usePlayerController({
        contentId: "test-stream",
        contentType: "live",
        enabled: false,
      })
    );

    expect(PlayerController).not.toHaveBeenCalled();
  });

  it("dismissToast clears toast from state", () => {
    const { result } = renderHook(() =>
      usePlayerController({
        contentId: "test-stream",
        contentType: "live",
      })
    );

    act(() => {
      result.current.dismissToast();
    });

    expect(result.current.state.toast).toBeNull();
  });

  it("clearError clears error state", () => {
    const { result } = renderHook(() =>
      usePlayerController({
        contentId: "test-stream",
        contentType: "live",
      })
    );

    act(() => {
      result.current.clearError();
    });

    expect(result.current.state.error).toBeNull();
    expect(result.current.state.errorDetails).toBeNull();
    expect(result.current.state.isPassiveError).toBe(false);
  });

  it("getQualities returns empty array when no controller", () => {
    const { result } = renderHook(() =>
      usePlayerController({
        contentId: "test-stream",
        contentType: "live",
      })
    );

    const qualities = result.current.getQualities();
    expect(qualities).toEqual([]);
  });

  it("creates controller when containerRef has element", () => {
    renderWithController();

    expect(mockOn).toHaveBeenCalled();
    const eventNames = mockOn.mock.calls.map((call: unknown[]) => call[0]);
    expect(eventNames).toContain("stateChange");
    expect(eventNames).toContain("timeUpdate");
    expect(eventNames).toContain("error");
    expect(eventNames).toContain("volumeChange");
    expect(eventNames).toContain("ready");
  });

  it("attaches controller to container element", () => {
    renderWithController();

    expect(mockAttach).toHaveBeenCalledWith(expect.any(HTMLDivElement));
  });

  it("exposes all expected action methods", () => {
    const { result } = renderHook(() =>
      usePlayerController({
        contentId: "test-stream",
        contentType: "live",
      })
    );

    expect(typeof result.current.play).toBe("function");
    expect(typeof result.current.pause).toBe("function");
    expect(typeof result.current.togglePlay).toBe("function");
    expect(typeof result.current.seek).toBe("function");
    expect(typeof result.current.seekBy).toBe("function");
    expect(typeof result.current.setVolume).toBe("function");
    expect(typeof result.current.toggleMute).toBe("function");
    expect(typeof result.current.toggleLoop).toBe("function");
    expect(typeof result.current.toggleFullscreen).toBe("function");
    expect(typeof result.current.togglePiP).toBe("function");
    expect(typeof result.current.toggleSubtitles).toBe("function");
    expect(typeof result.current.retry).toBe("function");
    expect(typeof result.current.reload).toBe("function");
    expect(typeof result.current.jumpToLive).toBe("function");
    expect(typeof result.current.getQualities).toBe("function");
    expect(typeof result.current.selectQuality).toBe("function");
    expect(typeof result.current.handleMouseEnter).toBe("function");
    expect(typeof result.current.handleMouseLeave).toBe("function");
    expect(typeof result.current.handleMouseMove).toBe("function");
    expect(typeof result.current.handleTouchStart).toBe("function");
    expect(typeof result.current.setDevModeOptions).toBe("function");
  });
});

// ===========================================================================
// Event -> State: Fire events and verify hook state updates
// ===========================================================================
describe("event -> state updates", () => {
  it("stateChange updates state field", () => {
    const { result } = renderWithController();

    act(() => {
      fire("stateChange", { state: "playing" });
    });

    expect(result.current.state.state).toBe("playing");
  });

  it("timeUpdate updates currentTime and duration", () => {
    const { result } = renderWithController();

    act(() => {
      fire("timeUpdate", { currentTime: 30.5, duration: 120 });
    });

    expect(result.current.state.currentTime).toBe(30.5);
    expect(result.current.state.duration).toBe(120);
  });

  it("error updates error and isPassiveError", () => {
    mockIsPassiveError.mockReturnValue(true);
    const { result } = renderWithController();

    act(() => {
      fire("error", { error: "Network timeout" });
    });

    expect(result.current.state.error).toBe("Network timeout");
    expect(result.current.state.isPassiveError).toBe(true);
  });

  it("errorCleared resets error fields", () => {
    const { result } = renderWithController();

    act(() => {
      fire("error", { error: "Something broke" });
    });
    expect(result.current.state.error).toBe("Something broke");

    act(() => {
      fire("errorCleared");
    });
    expect(result.current.state.error).toBeNull();
    expect(result.current.state.isPassiveError).toBe(false);
  });

  it("volumeChange updates volume and isMuted", () => {
    const { result } = renderWithController();

    act(() => {
      fire("volumeChange", { volume: 0.5, muted: false });
    });

    expect(result.current.state.volume).toBe(0.5);
    expect(result.current.state.isMuted).toBe(false);
  });

  it("loopChange updates isLoopEnabled", () => {
    const { result } = renderWithController();

    act(() => {
      fire("loopChange", { isLoopEnabled: true });
    });

    expect(result.current.state.isLoopEnabled).toBe(true);
  });

  it("fullscreenChange updates isFullscreen", () => {
    const { result } = renderWithController();

    act(() => {
      fire("fullscreenChange", { isFullscreen: true });
    });

    expect(result.current.state.isFullscreen).toBe(true);
  });

  it("pipChange updates isPiPActive", () => {
    const { result } = renderWithController();

    act(() => {
      fire("pipChange", { isPiP: true });
    });

    expect(result.current.state.isPiPActive).toBe(true);
  });

  it("holdSpeedStart updates isHoldingSpeed and holdSpeed", () => {
    const { result } = renderWithController();

    act(() => {
      fire("holdSpeedStart", { speed: 3 });
    });

    expect(result.current.state.isHoldingSpeed).toBe(true);
    expect(result.current.state.holdSpeed).toBe(3);
  });

  it("holdSpeedEnd resets isHoldingSpeed", () => {
    const { result } = renderWithController();

    act(() => {
      fire("holdSpeedStart", { speed: 2 });
    });
    expect(result.current.state.isHoldingSpeed).toBe(true);

    act(() => {
      fire("holdSpeedEnd");
    });
    expect(result.current.state.isHoldingSpeed).toBe(false);
  });

  it("hoverStart sets isHovering and shouldShowControls", () => {
    const { result } = renderWithController();

    act(() => {
      fire("hoverStart");
    });

    expect(result.current.state.isHovering).toBe(true);
    expect(result.current.state.shouldShowControls).toBe(true);
  });

  it("hoverEnd resets isHovering and reads shouldShowControls from controller", () => {
    mockShouldShowControls.mockReturnValue(false);
    const { result } = renderWithController();

    act(() => {
      fire("hoverStart");
    });
    expect(result.current.state.isHovering).toBe(true);

    act(() => {
      fire("hoverEnd");
    });
    expect(result.current.state.isHovering).toBe(false);
    expect(result.current.state.shouldShowControls).toBe(false);
  });

  it("captionsChange updates subtitlesEnabled", () => {
    const { result } = renderWithController();

    act(() => {
      fire("captionsChange", { enabled: true });
    });

    expect(result.current.state.subtitlesEnabled).toBe(true);
  });

  it("protocolSwapped sets toast message", () => {
    const { result } = renderWithController();

    act(() => {
      fire("protocolSwapped", {
        fromPlayer: "webrtc",
        toPlayer: "hls",
        fromProtocol: "WebRTC",
        toProtocol: "HLS",
        reason: "quality",
      });
    });

    expect(result.current.state.toast).not.toBeNull();
    expect(result.current.state.toast!.message).toContain("HLS");
  });

  it("playbackFailed sets error and errorDetails", () => {
    const { result } = renderWithController();

    act(() => {
      fire("playbackFailed", {
        code: "PLAYBACK_EXHAUSTED",
        message: "All players failed",
        details: { attempts: 3 },
      });
    });

    expect(result.current.state.error).toBe("All players failed");
    expect(result.current.state.errorDetails).toEqual({ attempts: 3 });
    expect(result.current.state.isPassiveError).toBe(false);
  });

  it("streamStateChange updates streamState and metadata", () => {
    mockGetMetadata.mockReturnValue({ title: "Live Stream" });
    mockIsEffectivelyLive.mockReturnValue(true);
    mockShouldShowIdleScreen.mockReturnValue(false);

    const { result } = renderWithController();

    act(() => {
      fire("streamStateChange", { state: "online" });
    });

    expect(result.current.state.streamState).toBe("online");
    expect(result.current.state.metadata).toEqual({ title: "Live Stream" });
    expect(result.current.state.isEffectivelyLive).toBe(true);
    expect(result.current.state.shouldShowIdleScreen).toBe(false);
  });

  it("ready sets videoElement and refreshes state from controller", () => {
    const fakeVideo = document.createElement("video");
    mockGetEndpoints.mockReturnValue({ playback: "https://example.com" });
    mockGetMetadata.mockReturnValue({ title: "Test" });
    mockGetStreamInfo.mockReturnValue({ sources: [] });
    mockIsEffectivelyLive.mockReturnValue(false);
    mockShouldShowIdleScreen.mockReturnValue(false);
    mockGetCurrentPlayerInfo.mockReturnValue({ name: "HLS.js", shortname: "hls" });
    mockGetCurrentSourceInfo.mockReturnValue({ url: "https://cdn.com/stream.m3u8", type: "hls" });

    const { result } = renderWithController();

    act(() => {
      fire("ready", { videoElement: fakeVideo });
    });

    expect(result.current.state.videoElement).toBe(fakeVideo);
    expect(result.current.state.endpoints).toEqual({ playback: "https://example.com" });
    expect(result.current.state.currentPlayerInfo).toEqual({ name: "HLS.js", shortname: "hls" });
    expect(result.current.state.currentSourceInfo).toEqual({
      url: "https://cdn.com/stream.m3u8",
      type: "hls",
    });
  });

  it("playerSelected updates currentPlayerInfo and currentSourceInfo", () => {
    mockGetCurrentPlayerInfo.mockReturnValue({ name: "WebRTC", shortname: "webrtc" });
    mockGetQualities.mockReturnValue([{ id: "auto", label: "Auto" }]);

    const { result } = renderWithController();

    act(() => {
      fire("playerSelected", {
        player: { name: "WebRTC" },
        source: { url: "https://cdn.com/whip", type: "webrtc" },
      });
    });

    expect(result.current.state.currentPlayerInfo).toEqual({
      name: "WebRTC",
      shortname: "webrtc",
    });
    expect(result.current.state.currentSourceInfo).toEqual({
      url: "https://cdn.com/whip",
      type: "webrtc",
    });
  });
});

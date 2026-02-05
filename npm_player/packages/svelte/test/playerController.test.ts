import { describe, it, expect, vi, beforeEach } from "vitest";
import { get } from "svelte/store";
import {
  createPlayerControllerStore,
  createDerivedState,
  createDerivedIsPlaying,
  createDerivedCurrentTime,
  createDerivedDuration,
  createDerivedError,
} from "../src/stores/playerController";

// ---------------------------------------------------------------------------
// Event-capturing mock for PlayerController
// ---------------------------------------------------------------------------

// Captured event handlers — fire these in tests to verify state updates
let eventHandlers: Map<string, Function>;

const mockAttach = vi.fn().mockResolvedValue(undefined);
const mockDestroy = vi.fn();
const mockDetach = vi.fn();
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

// Event-capturing `on()` — stores handlers for programmatic firing
const mockOn = vi.fn((event: string, handler: Function) => {
  eventHandlers.set(event, handler);
  return () => eventHandlers.delete(event);
});

vi.mock("@livepeer-frameworks/player-core", () => ({
  PlayerController: vi.fn().mockImplementation(function (this: Record<string, unknown>) {
    Object.assign(this, {
      attach: mockAttach,
      destroy: mockDestroy,
      detach: mockDetach,
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
}));

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function fire(event: string, data?: unknown): void {
  const handler = eventHandlers.get(event);
  if (handler) handler(data);
}

async function createAttachedStore() {
  const store = createPlayerControllerStore({
    contentId: "test-stream",
    contentType: "live",
  });
  const container = document.createElement("div");
  await store.attach(container);
  return store;
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

beforeEach(() => {
  vi.clearAllMocks();
  eventHandlers = new Map();
});

describe("createPlayerControllerStore", () => {
  it("creates store with initial state", () => {
    const store = createPlayerControllerStore({
      contentId: "test-stream",
      contentType: "live",
    });

    const state = get(store);
    expect(state.state).toBe("booting");
    expect(state.isPlaying).toBe(false);
    expect(state.isPaused).toBe(true);
    expect(state.isMuted).toBe(true);
    expect(state.volume).toBe(1);
    expect(state.error).toBeNull();
    expect(state.videoElement).toBeNull();
    expect(state.shouldShowIdleScreen).toBe(true);
  });

  it("attaches controller to container", async () => {
    const { PlayerController } = await import("@livepeer-frameworks/player-core");
    const store = createPlayerControllerStore({
      contentId: "test-stream",
      contentType: "live",
    });

    const container = document.createElement("div");
    await store.attach(container);

    expect(PlayerController).toHaveBeenCalledWith(
      expect.objectContaining({ contentId: "test-stream", contentType: "live" })
    );
    expect(mockAttach).toHaveBeenCalledWith(container);
    expect(mockOn).toHaveBeenCalled();
  });

  it("subscribes to controller events on attach", async () => {
    const store = await createAttachedStore();

    const eventNames = mockOn.mock.calls.map((call: unknown[]) => call[0]);
    expect(eventNames).toContain("stateChange");
    expect(eventNames).toContain("streamStateChange");
    expect(eventNames).toContain("timeUpdate");
    expect(eventNames).toContain("error");
    expect(eventNames).toContain("errorCleared");
    expect(eventNames).toContain("ready");
    expect(eventNames).toContain("playerSelected");
    expect(eventNames).toContain("volumeChange");
    expect(eventNames).toContain("loopChange");
    expect(eventNames).toContain("fullscreenChange");
    expect(eventNames).toContain("pipChange");
    expect(eventNames).toContain("holdSpeedStart");
    expect(eventNames).toContain("holdSpeedEnd");
    expect(eventNames).toContain("hoverStart");
    expect(eventNames).toContain("hoverEnd");
    expect(eventNames).toContain("captionsChange");
    expect(eventNames).toContain("protocolSwapped");
    expect(eventNames).toContain("playbackFailed");
  });

  it("destroy cleans up controller and resets state", async () => {
    const store = await createAttachedStore();
    store.destroy();

    expect(mockDestroy).toHaveBeenCalled();
    expect(store.getController()).toBeNull();

    const state = get(store);
    expect(state.state).toBe("booting");
  });

  it("detach resets state but controller remains", async () => {
    const store = await createAttachedStore();
    store.detach();

    expect(mockDetach).toHaveBeenCalled();
    const state = get(store);
    expect(state.state).toBe("booting");
  });

  it("forwards action methods to controller", async () => {
    const store = await createAttachedStore();

    store.pause();
    expect(mockPause).toHaveBeenCalled();

    store.togglePlay();
    expect(mockTogglePlay).toHaveBeenCalled();

    store.seek(30);
    expect(mockSeek).toHaveBeenCalledWith(30);

    store.seekBy(10);
    expect(mockSeekBy).toHaveBeenCalledWith(10);

    store.setVolume(0.5);
    expect(mockSetVolume).toHaveBeenCalledWith(0.5);

    store.toggleMute();
    expect(mockToggleMute).toHaveBeenCalled();

    store.toggleLoop();
    expect(mockToggleLoop).toHaveBeenCalled();

    store.selectQuality("720p");
    expect(mockSelectQuality).toHaveBeenCalledWith("720p");

    store.handleMouseEnter();
    expect(mockHandleMouseEnter).toHaveBeenCalled();

    store.handleMouseLeave();
    expect(mockHandleMouseLeave).toHaveBeenCalled();

    store.handleMouseMove();
    expect(mockHandleMouseMove).toHaveBeenCalled();

    store.handleTouchStart();
    expect(mockHandleTouchStart).toHaveBeenCalled();
  });

  it("clearError updates store state", async () => {
    const store = await createAttachedStore();

    store.clearError();
    expect(mockClearError).toHaveBeenCalled();

    const state = get(store);
    expect(state.error).toBeNull();
    expect(state.errorDetails).toBeNull();
    expect(state.isPassiveError).toBe(false);
  });

  it("dismissToast clears toast from state", () => {
    const store = createPlayerControllerStore({
      contentId: "test-stream",
      contentType: "live",
    });

    store.dismissToast();
    const state = get(store);
    expect(state.toast).toBeNull();
  });

  it("getQualities returns empty array before attach", () => {
    const store = createPlayerControllerStore({
      contentId: "test-stream",
      contentType: "live",
    });

    expect(store.getQualities()).toEqual([]);
  });

  it("does not attach when disabled", async () => {
    const { PlayerController } = await import("@livepeer-frameworks/player-core");
    vi.mocked(PlayerController).mockClear();

    const store = createPlayerControllerStore({
      contentId: "test-stream",
      contentType: "live",
      enabled: false,
    });

    const container = document.createElement("div");
    await store.attach(container);

    expect(PlayerController).not.toHaveBeenCalled();
    expect(mockAttach).not.toHaveBeenCalled();
  });
});

// ===========================================================================
// Event → State: Fire events and verify store updates
// ===========================================================================
describe("event → state updates", () => {
  it("stateChange updates state field", async () => {
    const store = await createAttachedStore();
    fire("stateChange", { state: "playing" });
    expect(get(store).state).toBe("playing");
  });

  it("timeUpdate updates currentTime and duration", async () => {
    const store = await createAttachedStore();
    fire("timeUpdate", { currentTime: 42.5, duration: 120 });
    expect(get(store).currentTime).toBe(42.5);
    expect(get(store).duration).toBe(120);
  });

  it("error event updates error and isPassiveError", async () => {
    mockIsPassiveError.mockReturnValue(true);
    const store = await createAttachedStore();
    fire("error", { error: "Decode failed" });

    const state = get(store);
    expect(state.error).toBe("Decode failed");
    expect(state.isPassiveError).toBe(true);
  });

  it("errorCleared resets error fields", async () => {
    const store = await createAttachedStore();
    fire("error", { error: "some error" });
    fire("errorCleared");

    const state = get(store);
    expect(state.error).toBeNull();
    expect(state.isPassiveError).toBe(false);
  });

  it("volumeChange updates volume and isMuted", async () => {
    const store = await createAttachedStore();
    fire("volumeChange", { volume: 0.7, muted: false });

    const state = get(store);
    expect(state.volume).toBe(0.7);
    expect(state.isMuted).toBe(false);
  });

  it("loopChange updates isLoopEnabled", async () => {
    const store = await createAttachedStore();
    fire("loopChange", { isLoopEnabled: true });
    expect(get(store).isLoopEnabled).toBe(true);
  });

  it("fullscreenChange updates isFullscreen", async () => {
    const store = await createAttachedStore();
    fire("fullscreenChange", { isFullscreen: true });
    expect(get(store).isFullscreen).toBe(true);
  });

  it("pipChange updates isPiPActive", async () => {
    const store = await createAttachedStore();
    fire("pipChange", { isPiP: true });
    expect(get(store).isPiPActive).toBe(true);
  });

  it("holdSpeedStart sets isHoldingSpeed and holdSpeed", async () => {
    const store = await createAttachedStore();
    fire("holdSpeedStart", { speed: 3 });

    const state = get(store);
    expect(state.isHoldingSpeed).toBe(true);
    expect(state.holdSpeed).toBe(3);
  });

  it("holdSpeedEnd resets isHoldingSpeed", async () => {
    const store = await createAttachedStore();
    fire("holdSpeedStart", { speed: 2 });
    fire("holdSpeedEnd");
    expect(get(store).isHoldingSpeed).toBe(false);
  });

  it("hoverStart sets isHovering and shouldShowControls", async () => {
    const store = await createAttachedStore();
    fire("hoverStart");

    const state = get(store);
    expect(state.isHovering).toBe(true);
    expect(state.shouldShowControls).toBe(true);
  });

  it("hoverEnd resets isHovering", async () => {
    const store = await createAttachedStore();
    fire("hoverStart");
    fire("hoverEnd");
    expect(get(store).isHovering).toBe(false);
  });

  it("captionsChange updates subtitlesEnabled", async () => {
    const store = await createAttachedStore();
    fire("captionsChange", { enabled: true });
    expect(get(store).subtitlesEnabled).toBe(true);
  });

  it("protocolSwapped sets toast", async () => {
    const store = await createAttachedStore();
    fire("protocolSwapped", { toProtocol: "WebRTC" });

    const toast = get(store).toast;
    expect(toast).not.toBeNull();
    expect(toast!.message).toContain("WebRTC");
  });

  it("playbackFailed sets error and errorDetails", async () => {
    const store = await createAttachedStore();
    fire("playbackFailed", {
      message: "No compatible player",
      details: { code: "PLAYBACK_FAILED" },
    });

    const state = get(store);
    expect(state.error).toBe("No compatible player");
    expect(state.errorDetails).toEqual({ code: "PLAYBACK_FAILED" });
    expect(state.isPassiveError).toBe(false);
  });
});

// ===========================================================================
// Derived stores
// ===========================================================================
describe("derived stores", () => {
  it("createDerivedState reflects stateChange event", async () => {
    const store = await createAttachedStore();
    const stateStore = createDerivedState(store);

    expect(get(stateStore)).toBe("booting");
    fire("stateChange", { state: "ready" });
    expect(get(stateStore)).toBe("ready");
  });

  it("createDerivedIsPlaying returns isPlaying value", () => {
    const store = createPlayerControllerStore({
      contentId: "test",
      contentType: "live",
    });
    const isPlayingStore = createDerivedIsPlaying(store);
    expect(get(isPlayingStore)).toBe(false);
  });

  it("createDerivedCurrentTime reflects timeUpdate event", async () => {
    const store = await createAttachedStore();
    const currentTimeStore = createDerivedCurrentTime(store);

    expect(get(currentTimeStore)).toBe(0);
    fire("timeUpdate", { currentTime: 55, duration: 120 });
    expect(get(currentTimeStore)).toBe(55);
  });

  it("createDerivedDuration reflects timeUpdate event", async () => {
    const store = await createAttachedStore();
    const durationStore = createDerivedDuration(store);

    fire("timeUpdate", { currentTime: 0, duration: 180 });
    expect(get(durationStore)).toBe(180);
  });

  it("createDerivedError reflects error event", async () => {
    const store = await createAttachedStore();
    const errorStore = createDerivedError(store);

    expect(get(errorStore)).toBeNull();
    fire("error", { error: "network timeout" });
    expect(get(errorStore)).toBe("network timeout");
  });
});

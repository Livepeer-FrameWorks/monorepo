import { describe, it, expect, vi, beforeEach } from "vitest";
import { get } from "svelte/store";
import { createStreamCrafterContextV2 } from "../src/stores/streamCrafterContextV2";
import {
  STREAMCRAFTER_WRAPPER_CONTROLLER_NOT_INITIALIZED_ERROR,
  STREAMCRAFTER_WRAPPER_PARITY_EVENT_NAMES,
  STREAMCRAFTER_WRAPPER_PARITY_INITIAL_STATE,
  STREAMCRAFTER_WRAPPER_PARITY_STATE_CHANGE_CASES,
} from "../../test-contract/streamcrafter-wrapper-contract";

// ---------------------------------------------------------------------------
// Event-capturing mock
// ---------------------------------------------------------------------------

let eventHandlers: Map<string, Function>;

const mockDestroy = vi.fn();
const mockStartCamera = vi.fn().mockResolvedValue({ id: "cam-1", type: "camera" });
const mockStartScreenShare = vi.fn().mockResolvedValue({ id: "screen-1", type: "screen" });
const mockStopCapture = vi.fn().mockResolvedValue(undefined);
const mockStartStreaming = vi.fn().mockResolvedValue(undefined);
const mockStopStreaming = vi.fn().mockResolvedValue(undefined);
const mockGetDevices = vi.fn().mockResolvedValue([]);
const mockSwitchVideoDevice = vi.fn().mockResolvedValue(undefined);
const mockSwitchAudioDevice = vi.fn().mockResolvedValue(undefined);
const mockGetStats = vi.fn().mockResolvedValue(null);
const mockGetMediaStream = vi.fn().mockReturnValue(null);
const mockGetSources = vi.fn().mockReturnValue([]);
const mockSetSourceVolume = vi.fn();
const mockSetSourceMuted = vi.fn();
const mockSetSourceActive = vi.fn();
const mockSetPrimaryVideoSource = vi.fn();
const mockSetMasterVolume = vi.fn();
const mockGetMasterVolume = vi.fn().mockReturnValue(1);
const mockSetQualityProfile = vi.fn().mockResolvedValue(undefined);
const mockAddCustomSource = vi.fn().mockReturnValue({ id: "custom-1" });
const mockRemoveSource = vi.fn();
const mockSetUseWebCodecs = vi.fn();
const mockSetEncoderOverrides = vi.fn();
const mockIsWebCodecsActive = vi.fn().mockReturnValue(false);
const mockGetReconnectionManager = vi.fn().mockReturnValue({ getState: () => null });
const mockGetEncoderManager = vi.fn().mockReturnValue(null);

const mockOn = vi.fn((event: string, handler: Function) => {
  eventHandlers.set(event, handler);
  return () => eventHandlers.delete(event);
});

vi.mock("@livepeer-frameworks/streamcrafter-core", () => ({
  IngestControllerV2: vi.fn().mockImplementation(function (this: Record<string, unknown>) {
    Object.assign(this, {
      destroy: mockDestroy,
      startCamera: mockStartCamera,
      startScreenShare: mockStartScreenShare,
      stopCapture: mockStopCapture,
      startStreaming: mockStartStreaming,
      stopStreaming: mockStopStreaming,
      getDevices: mockGetDevices,
      switchVideoDevice: mockSwitchVideoDevice,
      switchAudioDevice: mockSwitchAudioDevice,
      getStats: mockGetStats,
      getMediaStream: mockGetMediaStream,
      getSources: mockGetSources,
      setSourceVolume: mockSetSourceVolume,
      setSourceMuted: mockSetSourceMuted,
      setSourceActive: mockSetSourceActive,
      setPrimaryVideoSource: mockSetPrimaryVideoSource,
      setMasterVolume: mockSetMasterVolume,
      getMasterVolume: mockGetMasterVolume,
      setQualityProfile: mockSetQualityProfile,
      addCustomSource: mockAddCustomSource,
      removeSource: mockRemoveSource,
      setUseWebCodecs: mockSetUseWebCodecs,
      setEncoderOverrides: mockSetEncoderOverrides,
      isWebCodecsActive: mockIsWebCodecsActive,
      getReconnectionManager: mockGetReconnectionManager,
      getEncoderManager: mockGetEncoderManager,
      on: mockOn,
    });
  }),
  detectCapabilities: vi.fn().mockReturnValue({ recommended: "native" }),
}));

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function fire(event: string, data?: unknown): void {
  const handler = eventHandlers.get(event);
  if (handler) handler(data);
}

function createInitializedStore() {
  const store = createStreamCrafterContextV2();
  store.initialize({ whipUrl: "https://example.com/whip" });
  return store;
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

beforeEach(() => {
  vi.clearAllMocks();
  eventHandlers = new Map();
});

describe("createStreamCrafterContextV2", () => {
  it("creates store with initial state", () => {
    const store = createStreamCrafterContextV2();
    const state = get(store);

    for (const [key, expected] of Object.entries(STREAMCRAFTER_WRAPPER_PARITY_INITIAL_STATE)) {
      expect(state[key as keyof typeof state]).toEqual(expected);
    }
  });

  it("initializes controller", async () => {
    const { IngestControllerV2 } = await import("@livepeer-frameworks/streamcrafter-core");
    vi.mocked(IngestControllerV2).mockClear();

    const store = createStreamCrafterContextV2();
    store.initialize({ whipUrl: "https://example.com/whip" });

    expect(IngestControllerV2).toHaveBeenCalledWith(
      expect.objectContaining({ whipUrl: "https://example.com/whip" })
    );
  });

  it("subscribes to controller events on initialize", () => {
    createInitializedStore();

    const eventNames = mockOn.mock.calls.map((call: unknown[]) => call[0]);
    for (const eventName of STREAMCRAFTER_WRAPPER_PARITY_EVENT_NAMES) {
      expect(eventNames).toContain(eventName);
    }
  });

  it("throws when calling actions before initialize", async () => {
    const store = createStreamCrafterContextV2();

    await expect(store.startCamera()).rejects.toThrow(
      STREAMCRAFTER_WRAPPER_CONTROLLER_NOT_INITIALIZED_ERROR
    );
    await expect(store.startScreenShare()).rejects.toThrow(
      STREAMCRAFTER_WRAPPER_CONTROLLER_NOT_INITIALIZED_ERROR
    );
    await expect(store.startStreaming()).rejects.toThrow(
      STREAMCRAFTER_WRAPPER_CONTROLLER_NOT_INITIALIZED_ERROR
    );
    expect(() => store.addCustomSource({} as MediaStream, "test")).toThrow(
      STREAMCRAFTER_WRAPPER_CONTROLLER_NOT_INITIALIZED_ERROR
    );
  });

  it("forwards actions to controller after initialize", async () => {
    const store = createInitializedStore();

    await store.startCamera();
    expect(mockStartCamera).toHaveBeenCalled();

    await store.startScreenShare();
    expect(mockStartScreenShare).toHaveBeenCalled();

    store.addCustomSource({} as MediaStream, "custom-source");
    expect(mockAddCustomSource).toHaveBeenCalledWith({} as MediaStream, "custom-source");

    store.setSourceVolume("src-1", 0.5);
    expect(mockSetSourceVolume).toHaveBeenCalledWith("src-1", 0.5);

    store.setSourceMuted("src-1", true);
    expect(mockSetSourceMuted).toHaveBeenCalledWith("src-1", true);

    store.setSourceActive("src-1", false);
    expect(mockSetSourceActive).toHaveBeenCalledWith("src-1", false);

    store.setPrimaryVideoSource("src-2");
    expect(mockSetPrimaryVideoSource).toHaveBeenCalledWith("src-2");

    store.setMasterVolume(0.8);
    expect(mockSetMasterVolume).toHaveBeenCalledWith(0.8);
    expect(store.getMasterVolume()).toBe(1);

    await store.setQualityProfile("professional");
    expect(mockSetQualityProfile).toHaveBeenCalledWith("professional");

    await store.startStreaming();
    expect(mockStartStreaming).toHaveBeenCalled();

    await store.stopStreaming();
    expect(mockStopStreaming).toHaveBeenCalled();

    await store.getDevices();
    expect(mockGetDevices).toHaveBeenCalled();

    await store.switchVideoDevice("video-1");
    expect(mockSwitchVideoDevice).toHaveBeenCalledWith("video-1");

    await store.switchAudioDevice("audio-1");
    expect(mockSwitchAudioDevice).toHaveBeenCalledWith("audio-1");

    await store.getStats();
    expect(mockGetStats).toHaveBeenCalled();

    await store.stopCapture();
    expect(mockStopCapture).toHaveBeenCalled();

    store.setUseWebCodecs(true);
    expect(mockSetUseWebCodecs).toHaveBeenCalledWith(true);

    store.setEncoderOverrides({ maxBitrate: 2_000_000 });
    expect(mockSetEncoderOverrides).toHaveBeenCalledWith({ maxBitrate: 2_000_000 });

    store.removeSource("src-1");
    expect(mockRemoveSource).toHaveBeenCalledWith("src-1");
  });

  it("destroy cleans up controller", () => {
    const store = createInitializedStore();

    store.destroy();
    expect(mockDestroy).toHaveBeenCalled();
    expect(store.getController()).toBeNull();
  });

  it("getController returns null before initialize", () => {
    const store = createStreamCrafterContextV2();
    expect(store.getController()).toBeNull();
  });

  it("getMasterVolume returns 1 before initialize", () => {
    const store = createStreamCrafterContextV2();
    expect(store.getMasterVolume()).toBe(1);
  });

  it("setUseWebCodecs updates store state", () => {
    const store = createStreamCrafterContextV2();
    store.setUseWebCodecs(true);

    const state = get(store);
    expect(state.useWebCodecs).toBe(true);
  });
});

// ===========================================================================
// Event → State: Fire events and verify store updates
// ===========================================================================
describe("event → state updates", () => {
  it("stateChange updates derived fields for parity cases", () => {
    const store = createInitializedStore();

    mockGetReconnectionManager.mockReturnValue({
      getState: () => ({ attempt: 2, maxAttempts: 5 }),
    });

    for (const testCase of STREAMCRAFTER_WRAPPER_PARITY_STATE_CHANGE_CASES) {
      fire("stateChange", { state: testCase.state, context: testCase.context });

      const state = get(store);
      expect(state.state).toBe(testCase.state);
      for (const [key, expected] of Object.entries(testCase.expected)) {
        expect(state[key as keyof typeof state]).toEqual(expected);
      }
    }
  });

  it("statsUpdate updates stats", () => {
    const store = createInitializedStore();
    const stats = { bitrate: 4500000, fps: 30, rtt: 50 };

    fire("statsUpdate", stats);
    expect(get(store).stats).toEqual(stats);
  });

  it("error updates error field", () => {
    const store = createInitializedStore();

    fire("error", { error: "WHIP negotiation failed" });
    expect(get(store).error).toBe("WHIP negotiation failed");
  });

  it("sourceAdded refreshes sources from controller", () => {
    const store = createInitializedStore();
    const sources = [{ id: "cam-1", type: "camera" }];
    mockGetSources.mockReturnValue(sources);

    fire("sourceAdded", { source: sources[0] });
    expect(get(store).sources).toEqual(sources);
  });

  it("sourceRemoved refreshes sources from controller", () => {
    const store = createInitializedStore();
    mockGetSources.mockReturnValue([]);

    fire("sourceRemoved", { sourceId: "cam-1" });
    expect(get(store).sources).toEqual([]);
  });

  it("sourceUpdated refreshes sources from controller", () => {
    const store = createInitializedStore();
    const sources = [{ id: "cam-1", type: "camera", active: false }];
    mockGetSources.mockReturnValue(sources);

    fire("sourceUpdated", { source: sources[0] });
    expect(get(store).sources).toEqual(sources);
  });

  it("qualityChanged updates qualityProfile", () => {
    const store = createInitializedStore();

    fire("qualityChanged", { profile: "professional" });
    expect(get(store).qualityProfile).toBe("professional");
  });

  it("reconnectionAttempt updates reconnectionState", () => {
    const store = createInitializedStore();
    const reconnState = { attempt: 3, maxAttempts: 5, delay: 4000 };
    mockGetReconnectionManager.mockReturnValue({ getState: () => reconnState });

    fire("reconnectionAttempt");
    expect(get(store).reconnectionState).toEqual(reconnState);
  });

  it("webCodecsActive updates isWebCodecsActive", () => {
    const store = createInitializedStore();

    fire("webCodecsActive", { active: true });
    expect(get(store).isWebCodecsActive).toBe(true);
  });
});

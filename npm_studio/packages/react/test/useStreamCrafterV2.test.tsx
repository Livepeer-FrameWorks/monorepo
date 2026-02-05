import { describe, it, expect, vi, beforeEach } from "vitest";
import { renderHook, act } from "@testing-library/react";
import { useStreamCrafterV2 } from "../src/hooks/useStreamCrafterV2";

// ---------------------------------------------------------------------------
// Event-capturing mock
// ---------------------------------------------------------------------------

let eventHandlers: Map<string, Function[]>;

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
  isWebCodecsEncodingPathSupported: vi.fn().mockReturnValue(false),
}));

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function fire(event: string, data?: unknown): void {
  const handlers = eventHandlers.get(event);
  if (handlers) handlers.forEach((h) => h(data));
}

beforeEach(() => {
  vi.clearAllMocks();
  eventHandlers = new Map();
});

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("useStreamCrafterV2", () => {
  const baseConfig = {
    whipUrl: "https://example.com/whip",
  };

  it("returns initial idle state", () => {
    const { result } = renderHook(() => useStreamCrafterV2(baseConfig));

    expect(result.current.state).toBe("idle");
    expect(result.current.isStreaming).toBe(false);
    expect(result.current.isCapturing).toBe(false);
    expect(result.current.isReconnecting).toBe(false);
    expect(result.current.error).toBeNull();
    expect(result.current.mediaStream).toBeNull();
    expect(result.current.sources).toEqual([]);
    expect(result.current.stats).toBeNull();
  });

  it("creates IngestControllerV2 on mount", async () => {
    const { IngestControllerV2 } = await import("@livepeer-frameworks/streamcrafter-core");

    renderHook(() => useStreamCrafterV2(baseConfig));

    expect(IngestControllerV2).toHaveBeenCalledWith(
      expect.objectContaining({ whipUrl: "https://example.com/whip" })
    );
  });

  it("subscribes to controller events", () => {
    renderHook(() => useStreamCrafterV2(baseConfig));

    const eventNames = mockOn.mock.calls.map((call: unknown[]) => call[0]);
    expect(eventNames).toContain("stateChange");
    expect(eventNames).toContain("statsUpdate");
    expect(eventNames).toContain("error");
    expect(eventNames).toContain("sourceAdded");
    expect(eventNames).toContain("sourceRemoved");
    expect(eventNames).toContain("sourceUpdated");
    expect(eventNames).toContain("qualityChanged");
    expect(eventNames).toContain("reconnectionAttempt");
    expect(eventNames).toContain("webCodecsActive");
  });

  it("destroys controller on unmount", () => {
    const { unmount } = renderHook(() => useStreamCrafterV2(baseConfig));

    unmount();
    expect(mockDestroy).toHaveBeenCalled();
  });

  it("provides stable action callbacks", () => {
    const { result, rerender } = renderHook(() => useStreamCrafterV2(baseConfig));

    const firstStartCamera = result.current.startCamera;
    const firstStartStreaming = result.current.startStreaming;
    const firstStopStreaming = result.current.stopStreaming;

    rerender();

    expect(result.current.startCamera).toBe(firstStartCamera);
    expect(result.current.startStreaming).toBe(firstStartStreaming);
    expect(result.current.stopStreaming).toBe(firstStopStreaming);
  });

  it("getController returns controller instance", () => {
    const { result } = renderHook(() => useStreamCrafterV2(baseConfig));

    const controller = result.current.getController();
    expect(controller).not.toBeNull();
  });

  it("exposes all expected action methods", () => {
    const { result } = renderHook(() => useStreamCrafterV2(baseConfig));

    expect(typeof result.current.startCamera).toBe("function");
    expect(typeof result.current.startScreenShare).toBe("function");
    expect(typeof result.current.addCustomSource).toBe("function");
    expect(typeof result.current.removeSource).toBe("function");
    expect(typeof result.current.stopCapture).toBe("function");
    expect(typeof result.current.setSourceVolume).toBe("function");
    expect(typeof result.current.setSourceMuted).toBe("function");
    expect(typeof result.current.setSourceActive).toBe("function");
    expect(typeof result.current.setPrimaryVideoSource).toBe("function");
    expect(typeof result.current.setMasterVolume).toBe("function");
    expect(typeof result.current.getMasterVolume).toBe("function");
    expect(typeof result.current.setQualityProfile).toBe("function");
    expect(typeof result.current.startStreaming).toBe("function");
    expect(typeof result.current.stopStreaming).toBe("function");
    expect(typeof result.current.getDevices).toBe("function");
    expect(typeof result.current.switchVideoDevice).toBe("function");
    expect(typeof result.current.switchAudioDevice).toBe("function");
    expect(typeof result.current.getStats).toBe("function");
    expect(typeof result.current.setUseWebCodecs).toBe("function");
    expect(typeof result.current.setEncoderOverrides).toBe("function");
  });

  it("defaults qualityProfile to broadcast", () => {
    const { result } = renderHook(() => useStreamCrafterV2(baseConfig));
    expect(result.current.qualityProfile).toBe("broadcast");
  });
});

// ===========================================================================
// Event -> State: Fire events and verify hook state updates
// ===========================================================================
describe("event -> state updates", () => {
  const baseConfig = { whipUrl: "https://example.com/whip" };

  it("stateChange to streaming updates state and derived fields", () => {
    const { result } = renderHook(() => useStreamCrafterV2(baseConfig));

    act(() => {
      fire("stateChange", { state: "streaming", context: {} });
    });

    expect(result.current.state).toBe("streaming");
    expect(result.current.isStreaming).toBe(true);
    expect(result.current.isCapturing).toBe(true);
    expect(result.current.isReconnecting).toBe(false);
  });

  it("stateChange to capturing sets isCapturing but not isStreaming", () => {
    const { result } = renderHook(() => useStreamCrafterV2(baseConfig));

    act(() => {
      fire("stateChange", { state: "capturing", context: {} });
    });

    expect(result.current.state).toBe("capturing");
    expect(result.current.isCapturing).toBe(true);
    expect(result.current.isStreaming).toBe(false);
  });

  it("stateChange to reconnecting sets isReconnecting", () => {
    const { result } = renderHook(() => useStreamCrafterV2(baseConfig));

    act(() => {
      fire("stateChange", {
        state: "reconnecting",
        context: { reconnection: { attempt: 2, maxAttempts: 5 } },
      });
    });

    expect(result.current.state).toBe("reconnecting");
    expect(result.current.isReconnecting).toBe(true);
    expect(result.current.reconnectionState).toEqual({ attempt: 2, maxAttempts: 5 });
  });

  it("stateChange refreshes sources and mediaStream from controller", () => {
    const sources = [{ id: "cam-1", type: "camera" }];
    mockGetSources.mockReturnValue(sources);
    mockGetMediaStream.mockReturnValue("mock-stream");

    const { result } = renderHook(() => useStreamCrafterV2(baseConfig));

    act(() => {
      fire("stateChange", { state: "capturing", context: {} });
    });

    expect(result.current.sources).toEqual(sources);
    expect(result.current.mediaStream).toBe("mock-stream");
  });

  it("stateChange stores context", () => {
    const { result } = renderHook(() => useStreamCrafterV2(baseConfig));

    act(() => {
      fire("stateChange", { state: "streaming", context: { foo: "bar" } });
    });

    expect(result.current.stateContext).toEqual({ foo: "bar" });
  });

  it("statsUpdate updates stats", () => {
    const { result } = renderHook(() => useStreamCrafterV2(baseConfig));
    const stats = { bitrate: 4500000, fps: 30, rtt: 50 };

    act(() => {
      fire("statsUpdate", stats);
    });

    expect(result.current.stats).toEqual(stats);
  });

  it("error updates error field", () => {
    const { result } = renderHook(() => useStreamCrafterV2(baseConfig));

    act(() => {
      fire("error", { error: "WHIP negotiation failed" });
    });

    expect(result.current.error).toBe("WHIP negotiation failed");
  });

  it("sourceAdded refreshes sources and mediaStream from controller", () => {
    const sources = [{ id: "cam-1", type: "camera" }];
    mockGetSources.mockReturnValue(sources);
    mockGetMediaStream.mockReturnValue("stream-after-add");

    const { result } = renderHook(() => useStreamCrafterV2(baseConfig));

    act(() => {
      fire("sourceAdded", { source: sources[0] });
    });

    expect(result.current.sources).toEqual(sources);
    expect(result.current.mediaStream).toBe("stream-after-add");
  });

  it("sourceRemoved refreshes sources from controller", () => {
    mockGetSources.mockReturnValue([]);
    mockGetMediaStream.mockReturnValue(null);

    const { result } = renderHook(() => useStreamCrafterV2(baseConfig));

    act(() => {
      fire("sourceRemoved", { sourceId: "cam-1" });
    });

    expect(result.current.sources).toEqual([]);
    expect(result.current.mediaStream).toBeNull();
  });

  it("sourceUpdated refreshes sources from controller", () => {
    const sources = [{ id: "cam-1", type: "camera", active: false }];
    mockGetSources.mockReturnValue(sources);
    mockGetMediaStream.mockReturnValue("updated-stream");

    const { result } = renderHook(() => useStreamCrafterV2(baseConfig));

    act(() => {
      fire("sourceUpdated", { source: sources[0] });
    });

    expect(result.current.sources).toEqual(sources);
    expect(result.current.mediaStream).toBe("updated-stream");
  });

  it("qualityChanged updates qualityProfile", () => {
    const { result } = renderHook(() => useStreamCrafterV2(baseConfig));

    act(() => {
      fire("qualityChanged", { profile: "professional" });
    });

    expect(result.current.qualityProfile).toBe("professional");
  });

  it("reconnectionAttempt updates reconnectionState from controller", () => {
    const reconnState = { attempt: 3, maxAttempts: 5, delay: 4000 };
    mockGetReconnectionManager.mockReturnValue({ getState: () => reconnState });

    const { result } = renderHook(() => useStreamCrafterV2(baseConfig));

    act(() => {
      fire("reconnectionAttempt");
    });

    expect(result.current.reconnectionState).toEqual(reconnState);
  });

  it("webCodecsActive updates isWebCodecsActive", () => {
    const { result } = renderHook(() => useStreamCrafterV2(baseConfig));

    act(() => {
      fire("webCodecsActive", { active: true });
    });

    expect(result.current.isWebCodecsActive).toBe(true);
  });

  it("stateChange to idle resets encoder state", () => {
    const { result } = renderHook(() => useStreamCrafterV2(baseConfig));

    act(() => {
      fire("webCodecsActive", { active: true });
    });
    expect(result.current.isWebCodecsActive).toBe(true);

    act(() => {
      fire("stateChange", { state: "idle", context: {} });
    });
    expect(result.current.isWebCodecsActive).toBe(false);
    expect(result.current.encoderStats).toBeNull();
  });
});

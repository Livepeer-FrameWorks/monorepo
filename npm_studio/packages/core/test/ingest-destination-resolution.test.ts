import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { IngestControllerV2 } from "../src/core/IngestControllerV2";
import type { IngestControllerConfigV2 } from "../src/types";

const whip = vi.hoisted(() => ({
  constructed: vi.fn(),
  connect: vi.fn(),
  disconnect: vi.fn(),
  destroy: vi.fn(),
}));
vi.mock("../src/core/WhipClient", () => ({
  WhipClient: class {
    constructor(config: unknown) {
      whip.constructed(config);
    }
    on() {
      return () => {};
    }
    connect = whip.connect;
    disconnect = whip.disconnect;
    destroy = whip.destroy;
  },
}));

class FakeMediaStream {
  constructor(private tracks: MediaStreamTrack[] = []) {}
  getTracks() {
    return this.tracks;
  }
  getVideoTracks() {
    return this.tracks.filter((track) => track.kind === "video");
  }
  getAudioTracks() {
    return this.tracks.filter((track) => track.kind === "audio");
  }
}

const controllers: IngestControllerV2[] = [];
function create(resolveWhipUrl?: IngestControllerConfigV2["resolveWhipUrl"]) {
  const controller = new IngestControllerV2({
    whipUrl: "https://stale.example/webrtc/key",
    whipUrls: ["https://unapproved.example/webrtc/key"],
    resolveWhipUrl,
    useWebCodecs: false,
  });
  controllers.push(controller);
  const track = {
    kind: "video",
    id: "track",
    enabled: true,
    stop: vi.fn(),
    addEventListener: vi.fn(),
  } as unknown as MediaStreamTrack;
  const source = controller.addCustomSource(
    new FakeMediaStream([track]) as unknown as MediaStream,
    "Camera fixture"
  );
  return { controller, track, source };
}

beforeEach(() => {
  vi.clearAllMocks();
  vi.stubGlobal("MediaStream", FakeMediaStream);
});
afterEach(() => {
  controllers.splice(0).forEach((controller) => controller.destroy());
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("connection-time ingest placement", () => {
  it("resolves the fresh node before connecting while preserving captured sources", async () => {
    const resolve = vi.fn(async () => "https://us.example/webrtc/key");
    const { controller, source, track } = create(resolve);
    const output = controller.getMediaStream();
    await controller.startStreaming();
    expect(resolve).toHaveBeenCalledTimes(1);
    expect(whip.constructed.mock.calls[0][0].whipUrl).toBe("https://us.example/webrtc/key");
    expect(whip.connect).toHaveBeenCalledWith(output);
    expect(controller.getSources()[0].id).toBe(source.id);
    expect(track.stop).not.toHaveBeenCalled();
  });

  it("does not use any stale fallback when resolution is denied", async () => {
    const { controller, track } = create(async () => {
      throw new Error("No permitted destination");
    });
    await expect(controller.startStreaming()).rejects.toThrow("No permitted destination");
    expect(whip.constructed).not.toHaveBeenCalled();
    expect(controller.getState()).toBe("error");
    expect(track.stop).not.toHaveBeenCalled();
  });

  it("refreshes again for a later connection instead of caching the first choice", async () => {
    const resolve = vi
      .fn()
      .mockResolvedValueOnce("https://first.example/key")
      .mockResolvedValueOnce("https://second.example/key");
    const { controller } = create(resolve);
    await controller.startStreaming();
    await controller.stopStreaming();
    await controller.startStreaming();
    expect(whip.constructed.mock.calls.map(([config]) => config.whipUrl)).toEqual([
      "https://first.example/key",
      "https://second.example/key",
    ]);
  });

  it("aborts pending resolution on stop and cannot connect after its late completion", async () => {
    let complete!: (url: string) => void;
    let signal!: AbortSignal;
    const { controller } = create((value) => {
      signal = value;
      return new Promise((resolve) => (complete = resolve));
    });
    const pending = controller.startStreaming();
    const rejected = expect(pending).rejects.toMatchObject({ name: "AbortError" });
    await controller.stopStreaming();
    complete("https://too-late.example/key");
    await rejected;
    expect(signal.aborted).toBe(true);
    expect(whip.constructed).not.toHaveBeenCalled();
    expect(controller.getState()).toBe("capturing");
  });

  it("bounds a resolver that ignores cancellation and prevents double start", async () => {
    vi.useFakeTimers();
    const { controller } = create(() => new Promise(() => {}));
    const pending = controller.startStreaming();
    const rejected = expect(pending).rejects.toThrow("resolution timed out");
    await expect(controller.startStreaming()).rejects.toThrow("current state");
    await vi.advanceTimersByTimeAsync(5000);
    await rejected;
    expect(whip.constructed).not.toHaveBeenCalled();
    expect(controller.getState()).toBe("error");
  });

  it("validates the resolved protocol without exposing the rejected URL", async () => {
    const { controller } = create(async () => "javascript:secret-value");
    await expect(controller.startStreaming()).rejects.toThrow("invalid destination");
    expect(whip.constructed).not.toHaveBeenCalled();
    expect(controller.getStateContext().error).not.toContain("secret-value");
  });
});

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { IngestControllerV2 } from "../src/core/IngestControllerV2";
import type { IngestState } from "../src/types";

vi.mock("../src/core/WhipClient", () => ({
  WhipClient: class {
    on() {
      return () => {};
    }
    connect = vi.fn();
    disconnect = vi.fn();
    destroy = vi.fn();
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

function fakeStream(label: string) {
  const track = {
    kind: "video",
    id: label,
    label,
    enabled: true,
    stop: vi.fn(),
    addEventListener: vi.fn(),
  } as unknown as MediaStreamTrack;
  return new FakeMediaStream([track]) as unknown as MediaStream;
}

const controllers: IngestControllerV2[] = [];
function create(state: IngestState) {
  const controller = new IngestControllerV2({
    whipUrl: "https://edge.example/webrtc/key",
    useWebCodecs: false,
    audioMixing: false,
  });
  controllers.push(controller);
  const internals = controller as unknown as {
    setState(next: IngestState): void;
    deviceManager: { getUserMedia: (...args: unknown[]) => Promise<MediaStream> };
    screenCapture: { start: (...args: unknown[]) => Promise<MediaStream | null> };
  };
  internals.setState(state);
  const seen: IngestState[] = [];
  controller.on("stateChange", (event) => seen.push(event.state));
  return { controller, internals, seen };
}

beforeEach(() => {
  vi.stubGlobal("MediaStream", FakeMediaStream);
});
afterEach(() => {
  controllers.splice(0).forEach((controller) => controller.destroy());
  vi.unstubAllGlobals();
});

describe.each<IngestState>(["connecting", "streaming", "reconnecting"])(
  "adding a source during an active %s session",
  (state) => {
    it("keeps the session state through a screen share", async () => {
      const { controller, internals, seen } = create(state);
      internals.screenCapture.start = vi.fn(async () => fakeStream("Screen"));

      const source = await controller.startScreenShare();

      expect(source?.type).toBe("screen");
      expect(controller.getState()).toBe(state);
      expect(seen).not.toContain("requesting_permissions");
      expect(seen).not.toContain("capturing");
    });

    it("keeps the session state through a camera capture", async () => {
      const { controller, internals, seen } = create(state);
      internals.deviceManager.getUserMedia = vi.fn(async () => fakeStream("Camera"));

      const source = await controller.startCamera();

      expect(source.type).toBe("camera");
      expect(controller.getState()).toBe(state);
      expect(seen).not.toContain("capturing");
    });

    it("keeps the session state when the screen picker is cancelled", async () => {
      const { controller, internals, seen } = create(state);
      internals.screenCapture.start = vi.fn(async () => null);

      await expect(controller.startScreenShare()).resolves.toBeNull();

      expect(controller.getState()).toBe(state);
      expect(seen).not.toContain("idle");
    });

    it("keeps the session state when capture permission is refused", async () => {
      const { controller, internals, seen } = create(state);
      internals.deviceManager.getUserMedia = vi.fn(async () => {
        throw new Error("Permission denied");
      });

      await expect(controller.startCamera()).rejects.toThrow("Permission denied");

      expect(controller.getState()).toBe(state);
      expect(seen).not.toContain("error");
    });
  }
);

describe("adding a source with no active session", () => {
  it("moves through capture states from idle", async () => {
    const { controller, internals, seen } = create("idle");
    internals.screenCapture.start = vi.fn(async () => fakeStream("Screen"));

    await controller.startScreenShare();

    expect(seen).toContain("requesting_permissions");
    expect(controller.getState()).toBe("capturing");
  });

  it("reports a refused capture as an error from idle", async () => {
    const { controller, internals } = create("idle");
    internals.deviceManager.getUserMedia = vi.fn(async () => {
      throw new Error("Permission denied");
    });

    await expect(controller.startCamera()).rejects.toThrow("Permission denied");

    expect(controller.getState()).toBe("error");
  });
});

describe("a session that changes state during capture", () => {
  it("keeps a session that started reconnecting during the permission prompt", async () => {
    const { controller, internals } = create("streaming");
    internals.screenCapture.start = vi.fn(async () => {
      internals.setState("reconnecting");
      return fakeStream("Screen");
    });

    await controller.startScreenShare();

    expect(controller.getState()).toBe("reconnecting");
  });
});

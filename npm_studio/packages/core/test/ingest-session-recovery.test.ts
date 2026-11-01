import { afterEach, describe, expect, it, vi } from "vitest";
import { IngestControllerV2 } from "../src/core/IngestControllerV2";
import type { IngestState, MediaSource } from "../src/types";

vi.mock("../src/core/WhipClient", () => ({
  WhipClient: class {
    on() {
      return () => {};
    }
    connect = vi.fn();
    disconnect = vi.fn();
    destroy = vi.fn();
    getPeerConnection = vi.fn(() => null);
  },
}));

const controllers: IngestControllerV2[] = [];

function create({ withSource }: { withSource: boolean }) {
  const controller = new IngestControllerV2({
    whipUrl: "https://edge.example/webrtc/key",
    useWebCodecs: false,
    audioMixing: false,
  });
  controllers.push(controller);
  const internals = controller as unknown as {
    setState(next: IngestState): void;
    sources: Map<string, MediaSource>;
    outputStream: MediaStream | null;
    reconnectionManager: { emit(event: "exhausted", payload: unknown): void };
  };
  if (withSource) {
    const stream = { getTracks: () => [] } as unknown as MediaStream;
    internals.sources.set("camera", { id: "camera", stream } as MediaSource);
    internals.outputStream = stream;
  }
  internals.setState("reconnecting");
  return { controller, internals };
}

afterEach(() => {
  controllers.splice(0).forEach((controller) => controller.destroy());
});

describe("a broadcast whose reconnection attempts run out", () => {
  it("keeps its sources ready to go live again and reports the error", () => {
    const { controller, internals } = create({ withSource: true });

    internals.reconnectionManager.emit("exhausted", { attempts: 5 });

    expect(controller.getState()).toBe("capturing");
    expect(controller.getStateContext().error).toBe("Connection lost - reconnection failed");
  });

  it("clears the reported error when the next session starts", async () => {
    const { controller, internals } = create({ withSource: true });
    internals.reconnectionManager.emit("exhausted", { attempts: 5 });

    await controller.startStreaming().catch(() => {});

    expect(controller.getStateContext().error).toBeUndefined();
  });

  it("ends in the error state when nothing is left to publish", () => {
    const { controller, internals } = create({ withSource: false });

    internals.reconnectionManager.emit("exhausted", { attempts: 5 });

    expect(controller.getState()).toBe("error");
  });
});

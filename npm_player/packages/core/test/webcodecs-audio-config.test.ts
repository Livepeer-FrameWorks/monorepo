import { afterEach, describe, expect, it, vi } from "vitest";
import type { MainToWorkerMessage } from "../src/players/WebCodecsPlayer/worker/types";

afterEach(() => {
  vi.clearAllTimers();
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.resetModules();
});

describe("worker audio configuration", () => {
  it.each(["EAC-3", "EAC3", "EC3"])(
    "configures %s using the registered WebCodecs name",
    async (codec) => {
      vi.useFakeTimers();
      const configure = vi.fn();
      const scope = {
        postMessage: vi.fn(),
        onmessage: null as null | ((event: { data: MainToWorkerMessage }) => void),
      };
      vi.stubGlobal("self", scope);
      vi.stubGlobal(
        "AudioDecoder",
        class {
          state = "configured";
          configure = configure;
          close() {
            this.state = "closed";
          }
        }
      );
      await import("../src/players/WebCodecsPlayer/worker/decoder.worker");
      scope.onmessage!({
        data: {
          type: "create",
          idx: 1,
          uid: 1,
          track: { type: "audio", codec, rate: 48000, channels: 6 },
          opts: { optimizeForLatency: false },
        },
      });
      scope.onmessage!({ data: { type: "configure", idx: 1, uid: 2, header: new Uint8Array() } });
      await Promise.resolve();
      expect(configure).toHaveBeenCalledWith(
        expect.objectContaining({ codec: "ec-3", sampleRate: 48000, numberOfChannels: 6 })
      );
      scope.onmessage!({ data: { type: "close", idx: 1, uid: 3 } });
    }
  );
});

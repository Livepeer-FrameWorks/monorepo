import { afterEach, expect, it, vi } from "vitest";
import { ScreenCapture } from "../src/core/ScreenCapture";

afterEach(() => {
  vi.unstubAllGlobals();
});

it("stops a screen granted after teardown without retaining tracks or listeners", async () => {
  let grant!: (stream: MediaStream) => void;
  vi.stubGlobal("navigator", {
    mediaDevices: {
      getDisplayMedia: () =>
        new Promise<MediaStream>((resolve) => {
          grant = resolve;
        }),
    },
  });
  const track = { stop: vi.fn(), addEventListener: vi.fn(), kind: "video", label: "screen" };
  const stream = {
    getTracks: () => [track],
    getVideoTracks: () => [track],
  } as unknown as MediaStream;
  const capture = new ScreenCapture();
  const events = vi.fn();
  capture.on("started", events);
  const pending = capture.start();
  capture.destroy();
  grant(stream);
  await pending;
  expect(track.stop).toHaveBeenCalledOnce();
  expect(track.addEventListener).not.toHaveBeenCalled();
  expect(capture.getCaptures()).toEqual([]);
  expect(events).not.toHaveBeenCalled();
});

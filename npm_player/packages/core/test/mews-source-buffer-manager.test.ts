import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { SourceBufferManager } from "../src/players/MewsWsPlayer/SourceBufferManager";
import {
  FakeMediaSource,
  FakeSourceBuffer,
  makeFakeVideo,
  makeBuffered,
  stubGlobalMediaSource,
} from "./_fixtures/FakeMediaSource";

function u8(...bytes: number[]): Uint8Array {
  return new Uint8Array(bytes);
}

describe("SourceBufferManager", () => {
  let ms: FakeMediaSource;
  let video: ReturnType<typeof makeFakeVideo>;
  let onError: ReturnType<typeof vi.fn>;
  let typeSupported: ReturnType<typeof stubGlobalMediaSource>;

  function make(container: "mp4" | "webm" = "mp4") {
    return new SourceBufferManager({
      mediaSource: ms as unknown as MediaSource,
      videoElement: video as unknown as HTMLVideoElement,
      container,
      onError,
    });
  }

  beforeEach(() => {
    ms = new FakeMediaSource();
    video = makeFakeVideo();
    onError = vi.fn();
    typeSupported = stubGlobalMediaSource(() => true);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  describe("initWithCodecs", () => {
    it("creates a segments-mode SourceBuffer for a supported codec", () => {
      const sbm = make();
      expect(sbm.initWithCodecs(["avc1.640028"])).toBe(true);
      expect(ms.addSourceBuffer).toHaveBeenCalledWith('video/mp4;codecs="avc1.640028"');
      expect(ms.buffers[0].mode).toBe("segments");
      expect(sbm.getCodecs()).toEqual(["avc1.640028"]);
    });

    it("rejects an empty codec list", () => {
      const sbm = make();
      expect(sbm.initWithCodecs([])).toBe(false);
      expect(onError).toHaveBeenCalledWith("No codecs provided");
    });

    it("rejects an unsupported MSE codec", () => {
      typeSupported.mockReturnValue(false);
      const sbm = make();
      expect(sbm.initWithCodecs(["bogus"])).toBe(false);
      expect(onError).toHaveBeenCalledWith(expect.stringContaining("Unsupported MSE codec"));
    });

    it("is idempotent once a buffer exists", () => {
      const sbm = make();
      sbm.initWithCodecs(["avc1"]);
      expect(sbm.initWithCodecs(["avc1"])).toBe(true);
      expect(ms.addSourceBuffer).toHaveBeenCalledOnce();
    });
  });

  describe("append queue", () => {
    it("buffers data until the SourceBuffer exists, then flushes it on init", () => {
      const sbm = make();
      sbm.append(u8(1, 2, 3)); // no buffer yet → queued
      expect(ms.buffers).toHaveLength(0);

      sbm.initWithCodecs(["avc1"]);
      // flushQueue ran during init → the queued fragment was appended.
      expect(ms.buffers[0].appended).toHaveLength(1);
    });

    it("ignores empty fragments", () => {
      const sbm = make();
      sbm.initWithCodecs(["avc1"]);
      sbm.append(u8()); // zero-length → dropped
      expect(ms.buffers[0].appended).toHaveLength(0);
    });

    it("queues while the buffer is updating and drains one per updateend", () => {
      const sbm = make();
      sbm.initWithCodecs(["avc1"]);
      const sb = ms.buffers[0];

      sbm.append(u8(1)); // direct append → updating becomes true
      sbm.append(u8(2)); // busy/updating → queued
      sbm.append(u8(3)); // queued
      expect(sb.appended).toHaveLength(1);

      sb.fireUpdateEnd(); // drains one
      expect(sb.appended).toHaveLength(2);
      sb.fireUpdateEnd(); // drains the next
      expect(sb.appended).toHaveLength(3);
    });
  });

  describe("changeCodecs", () => {
    it("is a no-op when the codec set is unchanged (order-independent)", () => {
      const sbm = make();
      sbm.initWithCodecs(["avc1", "mp4a"]);
      sbm.changeCodecs(["mp4a", "avc1"]); // same set, different order
      expect(ms.addSourceBuffer).toHaveBeenCalledOnce();
      expect(sbm.hasActiveMessageQueue()).toBe(false);
    });

    it("rejects a switch to an unsupported codec", () => {
      const sbm = make();
      sbm.initWithCodecs(["avc1"]);
      typeSupported.mockReturnValue(false);
      sbm.changeCodecs(["bogus"]);
      expect(onError).toHaveBeenCalledWith(expect.stringContaining("Unsupported codec for switch"));
    });

    it("opens a message queue for an immediate switch and routes data into it", () => {
      const sbm = make();
      sbm.initWithCodecs(["avc1"]);
      sbm.changeCodecs(["hev1"]); // no switchPoint → immediate clearAndReinit
      expect(sbm.hasActiveMessageQueue()).toBe(true);
    });
  });

  describe("cleanup", () => {
    it("_clean removes everything older than the keepaway once the buffer is idle", () => {
      video = makeFakeVideo({ currentTime: 200, buffered: makeBuffered([[0, 200]]) });
      const sbm = make();
      sbm.initWithCodecs(["avc1"]);
      const sb = ms.buffers[0];
      sb.fireUpdateEnd(); // ensure idle (not updating, not busy)

      sbm._clean(10); // keep last 10s
      expect(sb.removed).toContainEqual([0, 190]);
    });

    it("_clean is a no-op when current time is within the keepaway window", () => {
      video = makeFakeVideo({ currentTime: 5, buffered: makeBuffered([[0, 5]]) });
      const sbm = make();
      sbm.initWithCodecs(["avc1"]);
      const sb = ms.buffers[0];
      sb.fireUpdateEnd();
      sbm._clean(180);
      expect(sb.removed).toHaveLength(0);
    });

    it("clearBuffer removes the full range", () => {
      const sbm = make();
      sbm.initWithCodecs(["avc1"]);
      const sb = ms.buffers[0];
      sb.fireUpdateEnd();
      sbm.clearBuffer();
      expect(sb.removed).toContainEqual([0, Infinity]);
    });
  });

  describe("findBufferIndex", () => {
    it("returns the range containing the position, or false", () => {
      video = makeFakeVideo({
        buffered: makeBuffered([
          [0, 5],
          [10, 20],
        ]),
      });
      const sbm = make();
      expect(sbm.findBufferIndex(3)).toBe(0);
      expect(sbm.findBufferIndex(15)).toBe(1);
      expect(sbm.findBufferIndex(7)).toBe(false);
    });
  });

  describe("destroy", () => {
    it("aborts the buffer and resets queued state", () => {
      const sbm = make();
      sbm.initWithCodecs(["avc1"]);
      const sb = ms.buffers[0] as FakeSourceBuffer;
      sbm.append(u8(1));
      sbm.append(u8(2)); // queued
      sbm.destroy();
      expect(sb.aborted).toBe(true);
      expect(sbm.hasActiveMessageQueue()).toBe(false);
      expect(sbm.paused).toBe(false);
    });
  });
});

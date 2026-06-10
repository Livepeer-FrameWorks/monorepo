import { describe, expect, it, vi } from "vitest";

import { SubtitleManager } from "../src/core/SubtitleManager";

// correctSubtitleSync re-times VTT cues by the seek offset — needed for WebRTC
// where video.currentTime doesn't match the stream position. Driven via a fake
// video carrying a text track with cues.

type Cue = { startTime: number; endTime: number };

function fakeTextTrack(cues: Cue[]) {
  return {
    cues,
    currentOffset: 0,
    removeCue: vi.fn(),
    addCue: vi.fn(),
  };
}

function fakeVideo(textTrack?: ReturnType<typeof fakeTextTrack>) {
  const listeners = new Map<string, Set<() => void>>();
  const textTracks = textTrack ? [textTrack] : [];
  return {
    textTracks,
    querySelectorAll: () => [] as unknown[],
    addEventListener: vi.fn((t: string, cb: () => void) => {
      let s = listeners.get(t);
      if (!s) listeners.set(t, (s = new Set()));
      s.add(cb);
    }),
    removeEventListener: vi.fn((t: string, cb: () => void) => listeners.get(t)?.delete(cb)),
    _fire: (t: string) => listeners.get(t)?.forEach((cb) => cb()),
  };
}

describe("SubtitleManager — correctSubtitleSync", () => {
  it("shifts cue timings by the seek offset and records the applied offset", () => {
    const tt = fakeTextTrack([
      { startTime: 10, endTime: 15 },
      { startTime: 20, endTime: 25 },
    ]);
    const video = fakeVideo(tt);
    const mgr = new SubtitleManager();
    mgr.attach(video as never);

    mgr.setSeekOffset(5); // > 1 → triggers a resync

    expect(tt.cues[0]).toMatchObject({ startTime: 5, endTime: 10 });
    expect(tt.cues[1]).toMatchObject({ startTime: 15, endTime: 20 });
    expect(tt.removeCue).toHaveBeenCalledTimes(2);
    expect(tt.addCue).toHaveBeenCalledTimes(2);
    expect(tt.currentOffset).toBe(5);
  });

  it("re-times against the original cue timing, not the already-shifted values", () => {
    const tt = fakeTextTrack([{ startTime: 30, endTime: 40 }]);
    const video = fakeVideo(tt);
    const mgr = new SubtitleManager();
    mgr.attach(video as never);

    mgr.setSeekOffset(5); // orig{30,40} → 25..35
    expect(tt.cues[0]).toMatchObject({ startTime: 25, endTime: 35 });
    mgr.setSeekOffset(10); // re-applied from orig, not from 25 → 20..30
    expect(tt.cues[0]).toMatchObject({ startTime: 20, endTime: 30 });
  });

  it("does not resync for a sub-second offset change", () => {
    const tt = fakeTextTrack([{ startTime: 10, endTime: 15 }]);
    const video = fakeVideo(tt);
    const mgr = new SubtitleManager();
    mgr.attach(video as never);

    mgr.setSeekOffset(0.5); // |0 - 0.5| not > 1 → no correction
    expect(tt.removeCue).not.toHaveBeenCalled();
    expect(tt.cues[0]).toMatchObject({ startTime: 10, endTime: 15 });
  });

  it("is a no-op when the track has no cues", () => {
    const tt = { cues: null, removeCue: vi.fn(), addCue: vi.fn(), currentOffset: 0 };
    const video = fakeVideo(tt as never);
    const mgr = new SubtitleManager();
    mgr.attach(video as never);
    expect(() => mgr.setSeekOffset(5)).not.toThrow();
    expect(tt.removeCue).not.toHaveBeenCalled();
  });

  it("resyncs on the seeked event once an offset is set", () => {
    const tt = fakeTextTrack([{ startTime: 100, endTime: 105 }]);
    const video = fakeVideo(tt);
    const mgr = new SubtitleManager();
    mgr.attach(video as never);
    (mgr as any).seekOffset = 8;

    video._fire("seeked");
    expect(tt.cues[0]).toMatchObject({ startTime: 92, endTime: 97 });
    expect(tt.currentOffset).toBe(8);
  });
});

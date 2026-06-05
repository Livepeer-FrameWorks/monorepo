import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { SessionQoeReporter, type SessionQoeDelta } from "../src/core/SessionQoeReporter";

/** Mutable monotonic clock the reporter reads via performance.now(). */
let clock = 0;

interface Range {
  start: number;
  end: number;
}

function makeVideo(
  opts: {
    frames?: { decoded: number; dropped: number; corrupted?: number };
    duration?: number;
  } = {}
) {
  const listeners = new Map<string, Function[]>();
  let played: Range[] = [];
  const video = {
    paused: false,
    currentTime: 0,
    duration: opts.duration ?? NaN, // NaN = live / unknown
    error: null as { code: number; message?: string } | null,
    get played() {
      return {
        length: played.length,
        start: (i: number) => played[i].start,
        end: (i: number) => played[i].end,
      };
    },
    getVideoPlaybackQuality: opts.frames
      ? vi.fn(() => ({
          totalVideoFrames: opts.frames!.decoded,
          droppedVideoFrames: opts.frames!.dropped,
          corruptedVideoFrames: opts.frames!.corrupted ?? 0,
        }))
      : undefined,
    addEventListener: (event: string, handler: Function) => {
      if (!listeners.has(event)) listeners.set(event, []);
      listeners.get(event)!.push(handler);
    },
    removeEventListener: (event: string, handler: Function) => {
      listeners.set(
        event,
        (listeners.get(event) || []).filter((h) => h !== handler)
      );
    },
    _fire(event: string) {
      listeners.get(event)?.forEach((h) => h());
    },
    _setPlayed(ranges: Range[]) {
      played = ranges;
    },
  };
  return video as unknown as HTMLVideoElement & {
    _fire: (e: string) => void;
    _setPlayed: (r: Range[]) => void;
    paused: boolean;
    error: { code: number } | null;
  };
}

function makeReporter(onFlush: (d: SessionQoeDelta) => void) {
  return new SessionQoeReporter({
    contentId: "demo",
    sessionId: "sess-1",
    contentType: "vod",
    isLive: false,
    playerType: "hlsjs",
    protocol: "hls",
    onFlush,
  });
}

describe("SessionQoeReporter", () => {
  beforeEach(() => {
    clock = 0;
    vi.stubGlobal("performance", { now: () => clock });
    // pagehide/visibility wiring must not throw under node; storage not needed here.
    vi.stubGlobal("window", { addEventListener: vi.fn(), removeEventListener: vi.fn() });
    vi.stubGlobal("document", {
      hidden: false,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    });
    vi.stubGlobal("navigator", {});
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it("counts genuine rebuffers and excludes initial buffering, seeks, and pauses", () => {
    let delta: SessionQoeDelta | null = null;
    const reporter = makeReporter((d) => (delta = d));
    const video = makeVideo();
    reporter.attach(video);

    // Pre-first-frame waiting = initial buffering → not a rebuffer.
    clock = 100;
    video._fire("waiting");

    // First frame.
    clock = 200;
    video._fire("playing");

    // Genuine rebuffer: waiting → (500ms) → playing.
    clock = 1000;
    video._fire("waiting");
    clock = 1500;
    video._fire("playing");

    // Seek-induced wait: seeking → waiting → (300ms) → seeked. Not a rebuffer.
    clock = 2000;
    video._fire("seeking");
    clock = 2100;
    video._fire("waiting");
    clock = 2400;
    video._fire("seeked");

    // Pause then waiting = not a stall.
    (video as { paused: boolean }).paused = true;
    clock = 3000;
    video._fire("waiting");
    (video as { paused: boolean }).paused = false;

    reporter.finalize("ended");

    expect(delta).not.toBeNull();
    const d = delta!;
    expect(d.rebufferCount).toBe(1);
    expect(d.rebufferMs).toBe(500);
    expect(d.seekWaitMs).toBe(300);
    expect(d.isFinal).toBe(true);
    expect(d.beaconSeq).toBe(0);
    expect(d.flushReason).toBe("ended");
    expect(d.firstFrame).toBe(true);
  });

  it("uses the union of video.played as watch time, not wall-clock", () => {
    let delta: SessionQoeDelta | null = null;
    const reporter = makeReporter((d) => (delta = d));
    const video = makeVideo();
    reporter.attach(video);

    clock = 100;
    video._fire("playing");

    // Two watched segments totalling 42s, despite a long wall-clock session.
    video._setPlayed([
      { start: 0, end: 30 },
      { start: 50, end: 62 },
    ]);
    clock = 999999;
    reporter.finalize("ended");

    expect(delta!.playedMs).toBe(42000);
  });

  it("emits frame deltas and marks frame stats unsupported when nothing decodes", () => {
    // Supported: decoded > 0.
    let supported: SessionQoeDelta | null = null;
    const r1 = makeReporter((d) => (supported = d));
    const v1 = makeVideo({ frames: { decoded: 2500, dropped: 7, corrupted: 1 } });
    r1.attach(v1);
    v1._fire("playing");
    r1.finalize("ended");
    expect(supported!.frameStatsSupported).toBe(true);
    expect(supported!.framesDecoded).toBe(2500);
    expect(supported!.framesDropped).toBe(7);
    expect(supported!.framesCorrupted).toBe(1);

    // Unsupported: API present but never reports frames (0/0 ≠ perfect).
    let unsupported: SessionQoeDelta | null = null;
    const r2 = makeReporter((d) => (unsupported = d));
    const v2 = makeVideo({ frames: { decoded: 0, dropped: 0 } });
    r2.attach(v2);
    v2._fire("playing");
    r2.finalize("ended");
    expect(unsupported!.frameStatsSupported).toBe(false);
  });

  it("does NOT mark fatal on a raw media error event (fatal is centralized on the terminal path)", () => {
    let delta: SessionQoeDelta | null = null;
    const reporter = makeReporter((d) => (delta = d));
    const video = makeVideo();
    reporter.attach(video);
    video._fire("playing");

    // A raw media-element 'error' may still recover via player fallback, so the
    // reporter must NOT mark fatal from it — only the controller's terminal
    // (fallback-exhausted) path calls recordFatalError.
    (video as { error: { code: number } | null }).error = { code: 3 };
    video._fire("error");
    reporter.finalize("ended");
    expect(delta!.fatalError).toBe(false);
  });

  it("emits exactly one final beacon and is idempotent across finalize calls", () => {
    const flushes: SessionQoeDelta[] = [];
    const reporter = makeReporter((d) => flushes.push(d));
    const video = makeVideo();
    reporter.attach(video);
    video._fire("playing");

    reporter.finalize("pagehide");
    reporter.finalize("unmount"); // second call must be a no-op
    expect(flushes).toHaveLength(1);
    expect(reporter.isCompleted()).toBe(true);
  });

  it("marks EBVS when play was intended but no first frame arrived", () => {
    let delta: SessionQoeDelta | null = null;
    const reporter = makeReporter((d) => (delta = d));
    const video = makeVideo();
    reporter.attach(video);
    reporter.markPlayIntent();
    // No "playing" event — playback never started.
    reporter.finalize("unmount");
    expect(delta!.playIntent).toBe(true);
    expect(delta!.firstFrame).toBe(false); // read layer: playIntent && !firstFrame = EBVS
  });

  it("integrates bitrate over played time and counts ABR switches by direction", () => {
    let delta: SessionQoeDelta | null = null;
    const reporter = makeReporter((d) => (delta = d));
    const video = makeVideo();
    reporter.attach(video);
    video._fire("playing");

    video._setPlayed([{ start: 0, end: 10 }]); // 10s watched at 1 Mbps
    reporter.sampleBitrate(1_000_000);
    video._setPlayed([{ start: 0, end: 20 }]); // +10s, then switch up to 2 Mbps
    reporter.sampleBitrate(2_000_000);
    video._setPlayed([{ start: 0, end: 25 }]); // +5s at 2 Mbps
    reporter.finalize("ended");

    // 1e6×10 + 2e6×5 = 20,000,000 bps·s
    expect(delta!.bitrateBpsSeconds).toBe(20_000_000);
    expect(delta!.abrUpswitchCount).toBe(1);
    expect(delta!.abrDownswitchCount).toBe(0);
  });

  it("emits periodic heartbeat deltas that reset between beacons", () => {
    vi.useFakeTimers();
    vi.spyOn(Math, "random").mockReturnValue(0); // no jitter → tick starts at 0
    const flushes: SessionQoeDelta[] = [];
    const reporter = makeReporter((d) => flushes.push(d));
    const video = makeVideo();
    reporter.attach(video);
    video._fire("playing");

    // 10s watched, then advance 30s of wall-clock → one heartbeat (6 ticks × 5s).
    video._setPlayed([{ start: 0, end: 10 }]);
    vi.advanceTimersByTime(30_000);
    expect(flushes).toHaveLength(1);
    expect(flushes[0].isFinal).toBe(false);
    expect(flushes[0].flushReason).toBe("heartbeat");
    expect(flushes[0].beaconSeq).toBe(0);
    expect(flushes[0].playedMs).toBe(10_000);

    // 5 more seconds watched, then finalize → the final delta is only the new 5s.
    video._setPlayed([{ start: 0, end: 15 }]);
    reporter.finalize("ended");
    expect(flushes).toHaveLength(2);
    expect(flushes[1].isFinal).toBe(true);
    expect(flushes[1].beaconSeq).toBe(1);
    expect(flushes[1].playedMs).toBe(5_000); // delta, not cumulative

    vi.useRealTimers();
  });

  it("emits a sparse VOD retention histogram of watched-seconds deltas", () => {
    let delta: SessionQoeDelta | null = null;
    const reporter = makeReporter((d) => (delta = d));
    const video = makeVideo({ duration: 300 }); // 5 min → 2s buckets
    reporter.attach(video);
    video._fire("playing");

    // Watched 0–7s (buckets 0,1,2 full + bucket 3 partial) and 20–24s (buckets 10,11).
    video._setPlayed([
      { start: 0, end: 7 },
      { start: 20, end: 24 },
    ]);
    reporter.finalize("ended");

    expect(delta!.bucketWidthS).toBe(2);
    expect(delta!.assetDurationS).toBe(300);
    // Buckets: 0:[0,2)=2, 1:[2,4)=2, 2:[4,6)=2, 3:[6,7)=1, 10:[20,22)=2, 11:[22,24)=2
    const map = new Map<number, number>();
    delta!.retentionBuckets!.forEach((b, i) => map.set(b, delta!.retentionSecondsWatched![i]));
    expect(map.get(0)).toBe(2);
    expect(map.get(3)).toBe(1);
    expect(map.get(10)).toBe(2);
    expect(map.has(5)).toBe(false); // unwatched buckets are omitted (sparse)
  });

  it("reports maxBucketReached from the furthest playhead, independent of watch density", () => {
    let delta: SessionQoeDelta | null = null;
    const reporter = makeReporter((d) => (delta = d));
    const video = makeVideo({ duration: 300 }); // 2s buckets
    reporter.attach(video);
    video._fire("playing");

    // Seek to 120s (bucket 60) WITHOUT watching the middle — reach advances, density doesn't.
    (video as { currentTime: number }).currentTime = 120;
    video._fire("seeked");
    reporter.finalize("ended");

    expect(delta!.maxBucketReached).toBe(60); // floor(120 / 2)
    // No played ranges set → no watch-density histogram, proving the two are independent.
    expect(delta!.retentionBuckets).toBeUndefined();
  });

  it("re-attaches on element swap, flushing then banking the old element's watch time", () => {
    const flushes: SessionQoeDelta[] = [];
    const reporter = makeReporter((d) => flushes.push(d));
    const a = makeVideo({ duration: 300 });
    reporter.attach(a);
    a._fire("playing");
    a._setPlayed([{ start: 0, end: 10 }]); // 10s watched on element A

    // Player fallback swaps in a new element; the reporter must flush A's pending
    // data (so its watch density isn't lost) then keep measuring B.
    const b = makeVideo({ duration: 300 });
    reporter.attach(b);
    b._fire("playing");
    b._setPlayed([{ start: 0, end: 5 }]); // +5s on element B

    reporter.finalize("ended");

    // Swap flush carries A's 10s; final carries B's 5s — summable, total 15s, B not deaf.
    expect(flushes.map((f) => f.flushReason)).toEqual(["element_swap", "ended"]);
    expect(flushes[0].playedMs).toBe(10000);
    expect(flushes[1].playedMs).toBe(5000);
    expect(flushes.reduce((s, f) => s + f.playedMs, 0)).toBe(15000);
  });

  it("banks frame counters across an element swap (no clamp-to-zero on the new element)", () => {
    const flushes: SessionQoeDelta[] = [];
    const reporter = makeReporter((d) => flushes.push(d));
    const a = makeVideo({ duration: 300, frames: { decoded: 1000, dropped: 5 } });
    reporter.attach(a);
    a._fire("playing");
    a._setPlayed([{ start: 0, end: 10 }]);

    // Swap to a fresh element whose getVideoPlaybackQuality restarts near zero.
    const b = makeVideo({ duration: 300, frames: { decoded: 100, dropped: 1 } });
    reporter.attach(b);
    b._fire("playing");
    b._setPlayed([{ start: 0, end: 5 }]);
    reporter.finalize("ended");

    // Swap beacon banks A's 1000; final adds B's 100 — summable to 1100, not clamped
    // to 0 on B (which would happen if B's raw 100 were diffed against A's 1000).
    expect(flushes[0].framesDecoded).toBe(1000);
    expect(flushes[flushes.length - 1].framesDecoded).toBe(100);
    expect(flushes.reduce((s, f) => s + f.framesDecoded, 0)).toBe(1100);
    expect(flushes.reduce((s, f) => s + f.framesDropped, 0)).toBe(6);
  });

  it("records a player-level fatal error only after first frame (recordFatalError)", () => {
    // Pre-first-frame: belongs to the boot trace, ignored here.
    let pre: SessionQoeDelta | null = null;
    const r1 = makeReporter((d) => (pre = d));
    const v1 = makeVideo({ duration: 300 });
    r1.attach(v1);
    r1.recordFatalError("hls_fatal_pre");
    r1.finalize("ended");
    expect(pre!.fatalError).toBe(false);

    // Post-first-frame: a mid-stream player fatal.
    let post: SessionQoeDelta | null = null;
    const r2 = makeReporter((d) => (post = d));
    const v2 = makeVideo({ duration: 300 });
    r2.attach(v2);
    v2._fire("playing");
    r2.recordFatalError("hls_fatal_mid");
    r2.finalize("ended");
    expect(post!.fatalError).toBe(true);
    expect(post!.errorCode).toBe("hls_fatal_mid");
  });

  it("omits the retention histogram for live content", () => {
    let delta: SessionQoeDelta | null = null;
    const reporter = new SessionQoeReporter({
      contentId: "live-1",
      sessionId: "s",
      contentType: "live",
      isLive: true,
      onFlush: (d) => (delta = d),
    });
    const video = makeVideo(); // duration NaN
    reporter.attach(video);
    video._fire("playing");
    video._setPlayed([{ start: 0, end: 30 }]);
    reporter.finalize("ended");
    expect(delta!.retentionBuckets).toBeUndefined();
    expect(delta!.bucketWidthS).toBeUndefined();
  });

  it("heartbeats an EBVS session (play intended, no first frame) so it survives a lost final", () => {
    vi.useFakeTimers();
    vi.spyOn(Math, "random").mockReturnValue(0);
    const flushes: SessionQoeDelta[] = [];
    const reporter = makeReporter((d) => flushes.push(d));
    const video = makeVideo();
    reporter.attach(video);
    reporter.markPlayIntent();
    // No "playing", no played time — but the intent must still beacon.
    vi.advanceTimersByTime(30_000);
    expect(flushes).toHaveLength(1);
    expect(flushes[0].flushReason).toBe("heartbeat");
    expect(flushes[0].playIntent).toBe(true);
    expect(flushes[0].firstFrame).toBe(false);
    reporter.finalize("unmount");
    vi.useRealTimers();
  });

  it("flushes the first-frame transition after an intent beacon (corrects a false EBVS on lost final)", () => {
    vi.useFakeTimers();
    vi.spyOn(Math, "random").mockReturnValue(0);
    const flushes: SessionQoeDelta[] = [];
    const reporter = makeReporter((d) => flushes.push(d));
    const video = makeVideo();
    reporter.attach(video);
    reporter.markPlayIntent();
    vi.advanceTimersByTime(30_000); // intent heartbeat: firstFrame=false
    expect(flushes).toHaveLength(1);
    expect(flushes[0].firstFrame).toBe(false);

    // First frame arrives — must flush NOW so a crash before the final can't leave
    // the read layer counting EBVS (a beacon already reported firstFrame=false).
    video._fire("playing");
    expect(flushes).toHaveLength(2);
    expect(flushes[1].flushReason).toBe("first_frame");
    expect(flushes[1].firstFrame).toBe(true);
    vi.useRealTimers();
  });

  it("flushes a terminal fatal immediately so it survives a lost final", () => {
    const flushes: SessionQoeDelta[] = [];
    const reporter = makeReporter((d) => flushes.push(d));
    const video = makeVideo();
    reporter.attach(video);
    video._fire("playing");
    reporter.recordFatalError("hls_fatal_mid");
    // Emitted before any finalize — durable on crash.
    expect(flushes.some((f) => f.flushReason === "error" && f.fatalError)).toBe(true);
  });

  it("heartbeats a seek-only VOD session when reach advances without watch density", () => {
    vi.useFakeTimers();
    vi.spyOn(Math, "random").mockReturnValue(0);
    const flushes: SessionQoeDelta[] = [];
    const reporter = makeReporter((d) => flushes.push(d));
    const video = makeVideo({ duration: 300 }); // 2s buckets
    reporter.attach(video);
    video._fire("playing");
    // Seek far without watching (no played ranges) — reach advances, density doesn't.
    (video as { currentTime: number }).currentTime = 120;
    video._fire("seeked");
    vi.advanceTimersByTime(30_000);
    expect(flushes).toHaveLength(1);
    expect(flushes[0].maxBucketReached).toBe(60); // floor(120/2)
    reporter.finalize("unmount");
    vi.useRealTimers();
  });

  it("suppresses idle heartbeats once nothing new has happened since the last flush", () => {
    vi.useFakeTimers();
    vi.spyOn(Math, "random").mockReturnValue(0);
    const flushes: SessionQoeDelta[] = [];
    const reporter = makeReporter((d) => flushes.push(d));
    const video = makeVideo();
    reporter.attach(video);
    video._fire("playing");
    // First frame is meaningful → exactly one heartbeat carries it.
    vi.advanceTimersByTime(30_000);
    expect(flushes).toHaveLength(1);
    expect(flushes[0].firstFrame).toBe(true);
    // Then truly idle (paused/backgrounded, no progress) → no further heartbeats.
    vi.advanceTimersByTime(60_000);
    expect(flushes).toHaveLength(1);
    reporter.finalize("unmount");
    vi.useRealTimers();
  });
});

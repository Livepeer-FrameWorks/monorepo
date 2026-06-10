import { afterEach, describe, expect, it, vi } from "vitest";

import { MewsWsPlayerImpl } from "../src/players/MewsWsPlayer/index";

// These exercise the WS message router and the on_time/on_stop/pause/seek
// handlers directly, by injecting fakes for the collaborators that initialize()
// would otherwise wire up. This covers the protocol-handling core of the player
// without standing up a real MediaSource/WebSocket.

type Listener = (e?: Event) => void;

function fakeVideo(props: Partial<{ currentTime: number; duration: number }> = {}) {
  const listeners = new Map<string, Set<Listener>>();
  return {
    currentTime: 0,
    duration: 0,
    playbackRate: 1,
    buffered: { length: 0, start: () => 0, end: () => 0 },
    play: vi.fn(async () => {}),
    pause: vi.fn(),
    addEventListener: vi.fn((t: string, cb: Listener) => {
      let s = listeners.get(t);
      if (!s) listeners.set(t, (s = new Set()));
      s.add(cb);
    }),
    removeEventListener: vi.fn((t: string, cb: Listener) => listeners.get(t)?.delete(cb)),
    _fire(t: string) {
      for (const cb of listeners.get(t) ?? []) cb(new Event(t));
    },
    ...props,
  };
}

function fakeSbManager() {
  return {
    paused: false,
    append: vi.fn(),
    findBufferIndex: vi.fn(() => false as number | false),
    scheduleAfterUpdate: vi.fn(),
    getCodecs: vi.fn(() => [] as string[]),
    changeCodecs: vi.fn(),
    clearBuffer: vi.fn(),
  };
}

function makePlayer() {
  const p = new MewsWsPlayerImpl();
  const send = vi.fn();
  const sb = fakeSbManager();
  const video = fakeVideo();
  const deliveryPolicy = { ingestOnTime: vi.fn(), ingestSetSpeedAck: vi.fn() };
  const bufferProbe = { updateServerState: vi.fn(), sample: () => ({ jitterMs: 0 }) };
  Object.assign(p as any, {
    wsManager: {
      send,
      notifyListeners: vi.fn(),
      addListener: vi.fn(),
      removeListener: vi.fn(),
      disableReconnection: vi.fn(),
    },
    sbManager: sb,
    videoElement: video,
    deliveryPolicy,
    bufferProbe,
    requestBuffer: { getDesiredMs: () => 600 },
    desiredBuffer: { getDesiredMs: () => 600 },
  });
  const call = (name: string, ...args: unknown[]) => (p as any)[name](...args);
  return { p, send, sb, video, deliveryPolicy, bufferProbe, call };
}

afterEach(() => {
  vi.useRealTimers();
});

describe("handleMessage routing", () => {
  it("appends binary frames to the source buffer", () => {
    const { sb, call } = makePlayer();
    const buf = new Uint8Array([1, 2, 3, 4]).buffer;
    call("handleMessage", buf);
    expect(sb.append).toHaveBeenCalledOnce();
    expect(sb.append.mock.calls[0][0]).toBeInstanceOf(Uint8Array);
  });

  it("parses JSON control messages and forwards them to listeners", () => {
    const { p, call } = makePlayer();
    call("handleMessage", JSON.stringify({ type: "on_time", data: { current: 0, end: 0 } }));
    expect((p as any).wsManager.notifyListeners).toHaveBeenCalled();
  });

  it("swallows a malformed control message", () => {
    const { call } = makePlayer();
    expect(() => call("handleMessage", "{ not json")).not.toThrow();
  });

  it("computes a current bitrate from successive binary frames", () => {
    vi.useFakeTimers();
    const { p, call } = makePlayer();
    call("handleMessage", new Uint8Array(1000).buffer);
    vi.advanceTimersByTime(100);
    call("handleMessage", new Uint8Array(1000).buffer);
    expect((p as any).currentBps).toBeGreaterThan(0);
  });
});

describe("handleOnTime", () => {
  it("records the seekable range, clears the paused flag and feeds the delivery policy", () => {
    const { sb, deliveryPolicy, bufferProbe, call } = makePlayer();
    sb.paused = true;
    call("handleOnTime", {
      type: "on_time",
      data: { current: 5000, end: 60000, begin: 1000, jitter: 12 },
    });
    expect(deliveryPolicy.ingestOnTime).toHaveBeenCalled();
    expect(bufferProbe.updateServerState).toHaveBeenCalled();
    expect(sb.paused).toBe(false);
  });

  it("exposes begin/end through getSeekableRange and updates duration", () => {
    const { p, call } = makePlayer();
    call("handleOnTime", { type: "on_time", data: { current: 5000, end: 90000, begin: 2000 } });
    expect(p.getSeekableRange()).toEqual({ start: 2000, end: 90000 });
    expect(p.getDuration()).toBe(90000); // lastDuration = end/1000 → *1000
  });
});

describe("handleControlMessage routing", () => {
  it("on_stop marks VoD, disables reconnection and ends after the buffer drains", () => {
    const { p, sb, video, call } = makePlayer();
    const ended = vi.fn();
    p.on("ended", ended);

    call("handleControlMessage", { type: "on_stop" });
    expect(p.isLive()).toBe(false); // streamType → "vod"
    expect((p as any).wsManager.disableReconnection).toHaveBeenCalledOnce();

    video._fire("waiting"); // buffer exhausted
    expect(sb.paused).toBe(true);
    expect(ended).toHaveBeenCalledOnce();
  });

  it("set_speed acks feed the delivery policy", () => {
    const { deliveryPolicy, call } = makePlayer();
    call("handleControlMessage", {
      type: "set_speed",
      data: { play_rate_curr: 1.5, play_rate_prev: "auto" },
    });
    expect(deliveryPolicy.ingestSetSpeedAck).toHaveBeenCalled();
  });
});

describe("handlePause — dead-point recovery", () => {
  it("just holds the buffer when the pause is not a dead point", () => {
    const { sb, send, call } = makePlayer();
    call("handlePause", { type: "pause", data: { reason: "user" } });
    expect(sb.paused).toBe(true);
    expect(send).not.toHaveBeenCalled();
  });

  it("seeks past a dead point at normal rate (no speed reset)", () => {
    const { send, call } = makePlayer();
    call("handlePause", { type: "pause", data: { reason: "at_dead_point", begin: 10000 } });
    // begin + 5000 at rate 1.
    expect(send).toHaveBeenCalledWith({ type: "seek", seek_time: 15000 });
    expect(send).not.toHaveBeenCalledWith({ type: "set_speed", play_rate: "auto" });
  });

  it("resets a slowed rate to auto before seeking past a dead point", () => {
    const { p, send, video, call } = makePlayer();
    (p as any).requestedRate = 0.5; // slowed → seekTo = begin + 1000, resetSpeedToAuto
    call("handlePause", { type: "pause", data: { reason: "at_dead_point", begin: 10000 } });
    expect(send).toHaveBeenCalledWith({ type: "set_speed", play_rate: "auto" });
    expect(send).toHaveBeenCalledWith({ type: "seek", seek_time: 11000 });
    expect((p as any).requestedRate).toBe(1);
    expect(video.playbackRate).toBe(1);
  });
});

describe("seek", () => {
  it("sends a delay-compensated seek and sets the element time as a fallback", () => {
    const { send, video, call } = makePlayer();
    call("seek", 10000);
    // seek_time = timeMs - (250 + serverDelay); with no samples getServerDelay()
    // returns its 500ms floor → 10000 - 750 = 9250.
    expect(send).toHaveBeenCalledWith({ type: "seek", seek_time: 9250 });
    expect(video.currentTime).toBe(10); // ms → s fallback
  });

  it("ignores invalid seek targets", () => {
    const { send, call } = makePlayer();
    call("seek", NaN);
    call("seek", -5);
    expect(send).not.toHaveBeenCalled();
  });
});

describe("applyLocalPlaybackRate", () => {
  it("sets the element rate directly in direct mode", () => {
    const { p, video, call } = makePlayer();
    call("applyLocalPlaybackRate", 1.08);
    expect(video.playbackRate).toBeCloseTo(1.08);
    expect((p as any).requestedRate).toBeCloseTo(1.08);
  });

  it("scales relative to the requested rate in multiplicative mode", () => {
    const { p, video, call } = makePlayer();
    (p as any).rateAdjustmentMode = "multiplicative";
    (p as any).requestedRate = 2;
    video.playbackRate = 2;
    call("applyLocalPlaybackRate", 1); // 2 * (1/2) = 1
    expect(video.playbackRate).toBeCloseTo(1);
  });
});

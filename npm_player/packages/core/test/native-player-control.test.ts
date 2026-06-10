import { describe, expect, it, vi } from "vitest";

import { NativePlayerImpl } from "../src/players/NativePlayer";

// Exercises the keep-away rate controller, WHEP control-channel routing, dynamic
// source switching and WebRTC stats parsing without a live peer connection.

function fakeVideo(
  props: Partial<{
    currentTime: number;
    paused: boolean;
    playbackRate: number;
    buffered: { length: number; start: (i: number) => number; end: (i: number) => number };
  }> = {}
) {
  return {
    currentTime: 0,
    paused: false,
    playbackRate: 1,
    src: "",
    buffered: { length: 0, start: () => 0, end: () => 0 },
    load: vi.fn(),
    play: vi.fn(async () => {}),
    pause: vi.fn(),
    ...props,
  };
}

const buffered = (startSec: number, endSec: number) => ({
  length: 1,
  start: () => startSec,
  end: () => endSec,
});

const callPrivate = (p: NativePlayerImpl, name: string, ...args: unknown[]) =>
  (p as any)[name](...args);

describe("setLiveKeepAwayMs", () => {
  it("derives the latency target from the reported keepaway with a jitter cushion", () => {
    const p = new NativePlayerImpl();
    p.setLiveKeepAwayMs(2000); // max(1000, round(2000*1.5)+500) = 3500
    expect((p as any).targetLatencyMs).toBe(3500);
  });

  it("floors a non-positive keepaway", () => {
    const p = new NativePlayerImpl();
    p.setLiveKeepAwayMs(0);
    expect((p as any).targetLatencyMs).toBe(1000);
  });
});

describe("onLiveEdgeUpdated — keep-away rate control", () => {
  function player(mime: string, video: ReturnType<typeof fakeVideo>) {
    const p = new NativePlayerImpl();
    (p as any).currentMimeType = mime;
    (p as any).videoElement = video;
    return p;
  }

  it("never steers WHEP (it owns its own pacing)", () => {
    const v = fakeVideo({ playbackRate: 1, buffered: buffered(100, 105), currentTime: 100 });
    callPrivate(player("whep", v), "onLiveEdgeUpdated");
    expect(v.playbackRate).toBe(1);
  });

  it("does nothing while paused or with no buffer", () => {
    const paused = fakeVideo({ paused: true, buffered: buffered(100, 105), currentTime: 100 });
    callPrivate(player("html5/video/mp4", paused), "onLiveEdgeUpdated");
    expect(paused.playbackRate).toBe(1);

    const empty = fakeVideo({ currentTime: 100 }); // buffered.length 0
    callPrivate(player("html5/video/mp4", empty), "onLiveEdgeUpdated");
    expect(empty.playbackRate).toBe(1);
  });

  it("slows toward the rebuild rate when behind on latency with a healthy buffer", () => {
    // Not anchored → getLiveLatencyMs()=0 < target → decision rebuildRate (0.99).
    const v = fakeVideo({ playbackRate: 1, currentTime: 100, buffered: buffered(100, 105) });
    callPrivate(player("html5/video/mp4", v), "onLiveEdgeUpdated");
    expect(v.playbackRate).toBeCloseTo(0.99);
  });

  it("forces a slowdown when read-ahead is critically thin", () => {
    // 0.5s read-ahead (< 750ms critical) → rate clamped to <= rebuildRate.
    const v = fakeVideo({ playbackRate: 1.5, currentTime: 100, buffered: buffered(100, 100.5) });
    callPrivate(player("html5/video/mp4", v), "onLiveEdgeUpdated");
    expect(v.playbackRate).toBeLessThan(1);
  });
});

describe("WHEP control-channel routing", () => {
  function whepPlayer() {
    const p = new NativePlayerImpl();
    const controlChannel = { isOpen: true, seek: vi.fn(), play: vi.fn(), hold: vi.fn() };
    const video = fakeVideo({ currentTime: 5 });
    (p as any).currentMimeType = "whep";
    (p as any).controlChannel = controlChannel;
    (p as any).videoElement = video;
    return { p, controlChannel, video };
  }

  it("seek routes through the data channel and records the offset", () => {
    const { p, controlChannel, video } = whepPlayer();
    p.seek(10_000); // 10s target, element at 5s → offset 5
    expect(video.pause).toHaveBeenCalled();
    expect((p as any).whepSeekOffset).toBe(5);
    expect(controlChannel.seek).toHaveBeenCalledWith(10_000);
  });

  it("seek is a no-op for MP3 sources", () => {
    const { p, controlChannel } = whepPlayer();
    (p as any).isMP3Source = true;
    p.seek(10_000);
    expect(controlChannel.seek).not.toHaveBeenCalled();
  });

  it("play/pause set the request flags and drive the control channel", async () => {
    const { p, controlChannel } = whepPlayer();
    await p.play();
    expect((p as any).whepPlayRequested).toBe(true);
    expect(controlChannel.play).toHaveBeenCalled();

    p.pause();
    expect((p as any).whepHoldRequested).toBe(true);
    expect(controlChannel.hold).toHaveBeenCalled();
  });
});

describe("setSource", () => {
  it("swaps the element source and reloads", () => {
    const p = new NativePlayerImpl();
    const video = fakeVideo();
    (p as any).videoElement = video;
    p.setSource("https://edge.example/clip.mp4");
    expect(video.src).toBe("https://edge.example/clip.mp4");
    expect(video.load).toHaveBeenCalledOnce();
    expect((p as any).currentSourceUrl).toBe("https://edge.example/clip.mp4");
  });
});

describe("getStats — WebRTC inbound-rtp parsing", () => {
  it("returns undefined without a peer connection", async () => {
    await expect(new NativePlayerImpl().getStats()).resolves.toBeUndefined();
  });

  it("maps inbound-rtp video/audio and the nominated candidate pair", async () => {
    const reports = [
      {
        type: "inbound-rtp",
        kind: "video",
        bytesReceived: 100_000,
        packetsReceived: 990,
        packetsLost: 10,
        jitter: 0.02,
        framesDecoded: 495,
        framesDropped: 5,
        frameWidth: 1280,
        frameHeight: 720,
        framesPerSecond: 30,
      },
      {
        type: "inbound-rtp",
        kind: "audio",
        bytesReceived: 5_000,
        packetsReceived: 495,
        packetsLost: 5,
        jitter: 0.01,
      },
      {
        type: "candidate-pair",
        nominated: true,
        currentRoundTripTime: 0.05,
        availableOutgoingBitrate: 1_000_000,
        bytesSent: 2_000,
        bytesReceived: 105_000,
      },
    ];
    const p = new NativePlayerImpl();
    (p as any).peerConnection = {
      getStats: async () => ({ forEach: (cb: (r: unknown) => void) => reports.forEach(cb) }),
    };

    const stats = await p.getStats();
    expect(stats!.type).toBe("webrtc");
    expect(stats!.video).toMatchObject({ frameWidth: 1280, frameHeight: 720, framesPerSecond: 30 });
    expect(stats!.video!.packetLossRate).toBeCloseTo((10 / 1000) * 100); // 1%
    expect(stats!.video!.jitter).toBeCloseTo(20); // 0.02s → ms
    expect(stats!.audio).toMatchObject({ packetsReceived: 495 });
    expect(stats!.network!.rtt).toBeCloseTo(50); // 0.05s → ms
  });
});

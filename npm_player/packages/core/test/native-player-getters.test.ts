import { afterEach, describe, expect, it, vi } from "vitest";

import { NativePlayerImpl } from "../src/players/NativePlayer";
import type { StreamInfo } from "../src/core/PlayerInterface";

// These tests exercise the cheap, branch-heavy accessors without standing up a
// full WHEP session: the meaningful contract is how each getter switches between
// the MistControl-channel timeline and the BasePlayer (browser-local) fallback.

type FakeControlChannel = {
  isOpen: boolean;
  seek: ReturnType<typeof vi.fn>;
  setTracks: ReturnType<typeof vi.fn>;
};

function fakeControlChannel(isOpen: boolean): FakeControlChannel {
  return { isOpen, seek: vi.fn(), setTracks: vi.fn() };
}

function fakeVideo(
  props: Partial<{ currentTime: number; duration: number; srcObject: unknown }> = {}
) {
  return { currentTime: 0, duration: 0, srcObject: null, pause: vi.fn(), ...props };
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("isMimeSupported", () => {
  it("accepts the declared capability mimes and rejects others", () => {
    const p = new NativePlayerImpl();
    expect(p.isMimeSupported("whep")).toBe(true);
    expect(p.isMimeSupported("html5/video/mp4")).toBe(true);
    expect(p.isMimeSupported("html5/application/vnd.apple.mpegurl")).toBe(true);
    expect(p.isMimeSupported("dash/video/mp4")).toBe(false);
    expect(p.isMimeSupported("ws/video/mp4")).toBe(false);
  });
});

describe("getAndroidVersion", () => {
  const callPrivate = (p: NativePlayerImpl) =>
    (p as unknown as { getAndroidVersion(): number | null }).getAndroidVersion();

  it("parses major and minor from the UA, returns null off Android", () => {
    const p = new NativePlayerImpl();
    vi.stubGlobal("navigator", { userAgent: "Mozilla/5.0 (Linux; Android 6) ..." });
    expect(callPrivate(p)).toBe(6);
    vi.stubGlobal("navigator", { userAgent: "... Android 7.1 ..." });
    expect(callPrivate(p)).toBeCloseTo(7.1);
    vi.stubGlobal("navigator", { userAgent: "Mozilla/5.0 (iPhone) ..." });
    expect(callPrivate(p)).toBeNull();
  });
});

describe("canSeek", () => {
  it("returns false without a video element", () => {
    expect(new NativePlayerImpl().canSeek()).toBe(false);
  });

  it("defers to the control channel's open state when one exists", () => {
    const p = new NativePlayerImpl();
    (p as any).videoElement = fakeVideo();
    (p as any).controlChannel = fakeControlChannel(true);
    expect(p.canSeek()).toBe(true);
    (p as any).controlChannel = fakeControlChannel(false);
    expect(p.canSeek()).toBe(false);
  });

  it("rejects MediaStream sources that have no control channel", () => {
    class FakeMediaStream {}
    vi.stubGlobal("MediaStream", FakeMediaStream);
    const p = new NativePlayerImpl();
    (p as any).videoElement = fakeVideo({ srcObject: new FakeMediaStream() });
    expect(p.canSeek()).toBe(false);
  });

  it("allows regular (non-MediaStream) sources without a control channel", () => {
    vi.stubGlobal("MediaStream", class {});
    const p = new NativePlayerImpl();
    (p as any).videoElement = fakeVideo({ srcObject: null });
    expect(p.canSeek()).toBe(true);
  });
});

describe("getQualities / selectQuality", () => {
  const whepStream: StreamInfo = {
    source: [{ type: "whep", url: "x" }],
    meta: { tracks: [{ type: "video", idx: 0, width: 1280, height: 720 } as never] },
    type: "live",
  };

  it("returns [] unless the active mime is whep", () => {
    const p = new NativePlayerImpl();
    expect(p.getQualities()).toEqual([]);
  });

  it("leads with an Auto entry marked active and lists video tracks", () => {
    const p = new NativePlayerImpl();
    (p as any).currentMimeType = "whep";
    (p as any).streamInfoRef = whepStream;
    const qs = p.getQualities();
    expect(qs[0]).toMatchObject({ id: "auto", isAuto: true, active: true });
    expect(qs.some((q) => q.id === "0")).toBe(true);
  });

  it("selectQuality is a no-op without whep + control channel", () => {
    const p = new NativePlayerImpl();
    (p as any).currentMimeType = "html5/video/mp4";
    (p as any).controlChannel = fakeControlChannel(true);
    p.selectQuality("0");
    expect((p as any).controlChannel.setTracks).not.toHaveBeenCalled();
  });

  it("maps 'auto' to an empty track set and a concrete id to a video track", () => {
    const p = new NativePlayerImpl();
    (p as any).currentMimeType = "whep";
    const cc = fakeControlChannel(true);
    (p as any).controlChannel = cc;

    p.selectQuality("auto");
    expect(cc.setTracks).toHaveBeenCalledWith({});
    expect((p as any).selectedTrack).toBe("auto");

    p.selectQuality("720p");
    expect(cc.setTracks).toHaveBeenCalledWith({ video: "720p" });
    expect((p as any).selectedTrack).toBe("720p");
  });
});

describe("timeline getters — control-channel vs browser-local fallback", () => {
  it("getCurrentTime uses the WHEP offset only while the channel is open", () => {
    const p = new NativePlayerImpl();
    (p as any).videoElement = fakeVideo({ currentTime: 10 });
    (p as any).whepSeekOffset = 5;

    (p as any).controlChannel = fakeControlChannel(true);
    expect(p.getCurrentTime()).toBe((5 + 10) * 1000);

    (p as any).controlChannel = fakeControlChannel(false);
    expect(p.getCurrentTime()).toBe(10 * 1000); // BasePlayer fallback
  });

  it("getDuration returns whepDurationMs only when the channel is open and it is positive", () => {
    const p = new NativePlayerImpl();
    (p as any).videoElement = fakeVideo({ duration: 30 });
    (p as any).whepDurationMs = 42000;

    (p as any).controlChannel = fakeControlChannel(true);
    expect(p.getDuration()).toBe(42000);

    (p as any).controlChannel = fakeControlChannel(false);
    expect(p.getDuration()).toBe(30 * 1000); // BasePlayer fallback
  });

  it("getSeekableRange returns the WHEP window when open with a buffer, else the BasePlayer null", () => {
    const p = new NativePlayerImpl();
    (p as any).videoElement = fakeVideo();
    (p as any).whepBufferWindow = 8000;
    (p as any).whepBeginMs = 1000;
    (p as any).whepEndMs = 9000;

    (p as any).controlChannel = fakeControlChannel(true);
    expect(p.getSeekableRange()).toEqual({ start: 1000, end: 9000 });

    (p as any).controlChannel = fakeControlChannel(false);
    expect(p.getSeekableRange()).toBeNull(); // live seek not enabled → BasePlayer null
  });

  it("getBufferWindow reflects the stored WHEP window", () => {
    const p = new NativePlayerImpl();
    (p as any).whepBufferWindow = 12345;
    expect(p.getBufferWindow()).toBe(12345);
  });
});

import { afterEach, describe, expect, it, vi } from "vitest";

import { HlsJsPlayerImpl } from "../src/players/HlsJsPlayer";
import type { StreamInfo, StreamSource } from "../src/core/PlayerInterface";

// The quality/track/stats/latency surface is thin delegation over the hls.js
// instance, so we drive it by injecting a fake `hls` + video element rather than
// standing up a real manifest.

function inject(player: HlsJsPlayerImpl, hls: unknown, video?: unknown) {
  (player as any).hls = hls;
  if (video !== undefined) (player as any).videoElement = video;
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("HlsJsPlayerImpl — quality API", () => {
  it("returns [] without an hls instance or video", () => {
    const p = new HlsJsPlayerImpl();
    expect(p.getQualities()).toEqual([]);
  });

  it("leads with an Auto entry and lists levels with active flags", () => {
    const p = new HlsJsPlayerImpl();
    inject(
      p,
      {
        levels: [
          { width: 1280, height: 720, bitrate: 2_500_000 },
          { width: 1920, height: 1080, bitrate: 6_000_000 },
        ],
        autoLevelEnabled: false,
        currentLevel: 1,
      },
      {}
    );
    const qs = p.getQualities();
    expect(qs[0]).toMatchObject({ id: "auto", isAuto: true, active: false });
    expect(qs).toHaveLength(3);
    expect(qs[2]).toMatchObject({ id: "1", active: true, height: 1080 });
  });

  it("selectQuality maps 'auto' to -1 and an index through, ignoring junk", () => {
    const p = new HlsJsPlayerImpl();
    const hls = { currentLevel: 99 };
    inject(p, hls);
    p.selectQuality("auto");
    expect(hls.currentLevel).toBe(-1);
    p.selectQuality("2");
    expect(hls.currentLevel).toBe(2);
    p.selectQuality("nope");
    expect(hls.currentLevel).toBe(2); // unchanged
  });
});

describe("HlsJsPlayerImpl — text & audio tracks", () => {
  it("reads native textTracks and toggles their mode", () => {
    const p = new HlsJsPlayerImpl();
    const textTracks = [
      { label: "English", language: "en", mode: "disabled" },
      { label: "", language: "es", mode: "disabled" },
    ];
    inject(p, {}, { textTracks });
    const tracks = p.getTextTracks();
    expect(tracks[0]).toMatchObject({ id: "0", label: "English", lang: "en", active: false });
    expect(tracks[1].label).toBe("CC 2"); // fallback label

    p.selectTextTrack("1");
    expect(textTracks[1].mode).toBe("showing");
    expect(textTracks[0].mode).toBe("disabled");

    p.selectTextTrack(null); // disable all
    expect(textTracks.every((t) => t.mode === "disabled")).toBe(true);
  });

  it("reads hls.js audioTracks and selects within bounds", () => {
    const p = new HlsJsPlayerImpl();
    const hls = {
      audioTracks: [
        { name: "English", lang: "en" },
        { name: "", lang: "es" },
      ],
      audioTrack: 0,
    };
    inject(p, hls);
    const tracks = p.getAudioTracks();
    expect(tracks[0]).toMatchObject({ id: "0", label: "English", active: true });
    expect(tracks[1].label).toBe("es"); // falls back to lang

    p.selectAudioTrack("1");
    expect(hls.audioTrack).toBe(1);
    p.selectAudioTrack("9"); // out of range → ignored
    expect(hls.audioTrack).toBe(1);
  });
});

describe("HlsJsPlayerImpl — stats, latency, duration", () => {
  it("getStats returns undefined without an hls instance", async () => {
    await expect(new HlsJsPlayerImpl().getStats()).resolves.toBeUndefined();
  });

  it("getStats reports level data, buffered-ahead and live latency", async () => {
    const p = new HlsJsPlayerImpl();
    inject(
      p,
      {
        levels: [{ bitrate: 2_500_000, width: 1280, height: 720 }],
        currentLevel: 0,
        loadLevel: 0,
        bandwidthEstimate: 4_000_000,
        liveSyncPosition: 58,
      },
      {
        currentTime: 50,
        duration: Infinity,
        buffered: { length: 1, start: () => 48, end: () => 60 },
      }
    );
    const stats = await p.getStats();
    expect(stats).toMatchObject({
      type: "hls",
      bandwidthEstimate: 4_000_000,
      currentBitrate: 2_500_000,
      buffered: 10, // 60 - 50
    });
    expect(stats!.latency).toBeCloseTo((58 - 50) * 1000);
  });

  it("getLiveLatency uses liveSyncPosition, else native range, else 0", () => {
    const p = new HlsJsPlayerImpl();
    expect(p.getLiveLatency()).toBe(0); // no video

    inject(p, { liveSyncPosition: 58 }, { currentTime: 50 });
    expect(p.getLiveLatency()).toBeCloseTo((58 - 50) * 1000);
  });

  it("getDuration converts a finite element duration to ms", () => {
    const p = new HlsJsPlayerImpl();
    inject(p, null, { duration: 64, seekable: { length: 0 } });
    expect(p.getDuration()).toBe(64_000);
  });
});

describe("HlsJsPlayerImpl — isBrowserSupported", () => {
  const source: StreamSource = {
    type: "html5/application/vnd.apple.mpegurl",
    url: "https://edge.example/live/index.m3u8",
  };
  const stream = (tracks: unknown[]): StreamInfo => ({
    source: [source],
    meta: { tracks: tracks as never },
    type: "live",
  });

  function stubDesktopChrome(withMediaSource = true) {
    const win: Record<string, unknown> = {
      location: { protocol: "https:" },
      RTCPeerConnection: class {},
      WebSocket: class {},
    };
    if (withMediaSource) win.MediaSource = (globalThis as any).MediaSource;
    vi.stubGlobal("window", win);
    vi.stubGlobal("navigator", {
      userAgent: "Mozilla/5.0 (X11; Linux x86_64) Chrome/120 Safari/537.36",
    });
    vi.stubGlobal(
      "MediaSource",
      Object.assign(function () {}, { isTypeSupported: () => true })
    );
  }

  it("assumes standard tracks when no codec info is available yet", () => {
    stubDesktopChrome();
    const p = new HlsJsPlayerImpl();
    expect(p.isBrowserSupported(source.type, source, stream([]))).toEqual(["video", "audio"]);
  });

  it("drops HLS-incompatible audio (OPUS) but keeps a supported video track", () => {
    stubDesktopChrome();
    const p = new HlsJsPlayerImpl();
    const result = p.isBrowserSupported(
      source.type,
      source,
      stream([
        { type: "video", codec: "H264" },
        { type: "audio", codec: "OPUS" },
      ])
    );
    expect(result).toEqual(["video"]);
  });

  it("rejects a protocol mismatch (https page, http source)", () => {
    stubDesktopChrome();
    const p = new HlsJsPlayerImpl();
    const httpSource = { ...source, url: "http://edge.example/live/index.m3u8" };
    expect(p.isBrowserSupported(httpSource.type, httpSource, stream([]))).toBe(false);
  });

  it("defers to VideoJS on mobile Android", () => {
    stubDesktopChrome();
    vi.stubGlobal("navigator", {
      userAgent: "Mozilla/5.0 (Linux; Android 10; Mobile) Chrome/120 Mobile Safari/537.36",
    });
    const p = new HlsJsPlayerImpl();
    expect(p.isBrowserSupported(source.type, source, stream([]))).toBe(false);
  });

  it("falls back to native HLS when MSE is unavailable", () => {
    stubDesktopChrome(false); // no MediaSource in window
    vi.stubGlobal("document", {
      createElement: () => ({ canPlayType: () => "maybe" }),
    });
    const p = new HlsJsPlayerImpl();
    expect(p.isBrowserSupported(source.type, source, stream([]))).toEqual(["video", "audio"]);
  });
});

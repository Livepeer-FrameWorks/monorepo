import { afterEach, describe, expect, it, vi } from "vitest";

import { NativePlayerImpl } from "../src/players/NativePlayer";
import type { StreamInfo, StreamSource } from "../src/core/PlayerInterface";

// isBrowserSupported branches: WHEP codec compatibility, header gating, native
// HLS, and Safari/WebM. Driven through stubbed browser globals (no RTCRtpReceiver
// in node, so the WebRTC codec check uses the static compatibility list).

function stubEnv(opts: { ua?: string; rtc?: boolean } = {}) {
  const ua = opts.ua ?? "Mozilla/5.0 (X11; Linux x86_64) Chrome/120 Safari/537.36";
  const win: Record<string, unknown> = { location: { protocol: "https:" }, fetch: () => {} };
  if (opts.rtc !== false) win.RTCPeerConnection = class {};
  vi.stubGlobal("window", win);
  vi.stubGlobal("navigator", { userAgent: ua });
  vi.stubGlobal("document", { createElement: () => ({ canPlayType: () => "maybe" }) });
}

const stream = (tracks: unknown[]): StreamInfo => ({
  source: [],
  meta: { tracks: tracks as never },
  type: "live",
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("NativePlayerImpl — isBrowserSupported", () => {
  it("rejects non-WHEP sources that carry custom headers", () => {
    // First gate — no globals needed.
    const p = new NativePlayerImpl();
    const source = {
      type: "html5/video/mp4",
      url: "https://x/v.mp4",
      headers: { Authorization: "Bearer x" },
    } as StreamSource;
    expect(p.isBrowserSupported("html5/video/mp4", source, stream([]))).toBe(false);
  });

  it("accepts WHEP with a WebRTC-compatible video codec", () => {
    stubEnv();
    const p = new NativePlayerImpl();
    const source = { type: "whep", url: "https://x/whep" } as StreamSource;
    expect(
      p.isBrowserSupported("whep", source, stream([{ type: "video", codec: "H264" }]))
    ).toEqual(["video"]);
  });

  it("rejects WHEP when RTCPeerConnection is unavailable", () => {
    stubEnv({ rtc: false });
    const p = new NativePlayerImpl();
    const source = { type: "whep", url: "https://x/whep" } as StreamSource;
    expect(p.isBrowserSupported("whep", source, stream([{ type: "video", codec: "H264" }]))).toBe(
      false
    );
  });

  it("rejects WHEP when every codec is WebRTC-incompatible", () => {
    stubEnv();
    const p = new NativePlayerImpl();
    const source = { type: "whep", url: "https://x/whep" } as StreamSource;
    expect(p.isBrowserSupported("whep", source, stream([{ type: "video", codec: "H265" }]))).toBe(
      false
    );
    // B-frames also make an otherwise-fine codec incompatible.
    expect(
      p.isBrowserSupported(
        "whep",
        source,
        stream([{ type: "video", codec: "H264", bframes: true }])
      )
    ).toBe(false);
  });

  it("accepts native HLS on a desktop browser", () => {
    stubEnv();
    const p = new NativePlayerImpl();
    const source = {
      type: "html5/application/vnd.apple.mpegurl",
      url: "https://x/index.m3u8",
    } as StreamSource;
    expect(p.isBrowserSupported(source.type, source, stream([]))).toEqual(["video", "audio"]);
  });

  it("rejects WebM on Safari", () => {
    stubEnv({ ua: "Mozilla/5.0 (Macintosh) Version/17.0 Safari/605.1.15" });
    const p = new NativePlayerImpl();
    const source = { type: "html5/video/webm", url: "https://x/v.webm" } as StreamSource;
    expect(p.isBrowserSupported("html5/video/webm", source, stream([]))).toBe(false);
  });
});

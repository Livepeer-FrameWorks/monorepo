import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";

import {
  getBrowserInfo,
  testCodecSupport,
  getCodecSupport,
  checkTrackPlayability,
  checkProtocolMismatch,
  isFileProtocol,
  getAndroidVersion,
  getBrowserCompatibility,
  checkWebRTCCodecCompatibility,
  checkMSECodecCompatibility,
  WEBRTC_COMPATIBLE_CODECS,
  WEBRTC_INCOMPATIBLE_CODECS,
} from "../src/core/detector";

// Save originals
let origNavigator: typeof globalThis.navigator;
let origWindow: typeof globalThis.window;
let origMediaSource: typeof globalThis.MediaSource;

function stubNavigator(ua: string) {
  Object.defineProperty(globalThis, "navigator", {
    value: { userAgent: ua },
    configurable: true,
    writable: true,
  });
}

function stubWindow(overrides: Record<string, unknown> = {}) {
  // Build window object — only include keys that are explicitly provided
  // so that `"MediaSource" in window` returns false when not provided
  const windowObj: Record<string, unknown> = {
    location: { protocol: "https:" },
  };

  // Only set browser API keys if explicitly provided (truthy or object)
  for (const key of ["MediaSource", "RTCPeerConnection", "WebSocket", "RTCRtpReceiver"]) {
    if (key in overrides) {
      windowObj[key] = overrides[key];
    }
  }

  // Merge remaining overrides
  for (const [key, val] of Object.entries(overrides)) {
    if (!["MediaSource", "RTCPeerConnection", "WebSocket", "RTCRtpReceiver"].includes(key)) {
      windowObj[key] = val;
    }
  }

  Object.defineProperty(globalThis, "window", {
    value: windowObj,
    configurable: true,
    writable: true,
  });

  // Also set/clear global MediaSource since source code uses bare MediaSource reference
  if (overrides.MediaSource !== undefined) {
    (globalThis as any).MediaSource = overrides.MediaSource;
  } else {
    delete (globalThis as any).MediaSource;
  }
}

describe("detector", () => {
  beforeEach(() => {
    origNavigator = globalThis.navigator;
    origWindow = globalThis.window;
    origMediaSource = (globalThis as any).MediaSource;
  });

  afterEach(() => {
    Object.defineProperty(globalThis, "navigator", {
      value: origNavigator,
      configurable: true,
      writable: true,
    });
    Object.defineProperty(globalThis, "window", {
      value: origWindow,
      configurable: true,
      writable: true,
    });
    if (origMediaSource !== undefined) {
      (globalThis as any).MediaSource = origMediaSource;
    } else {
      delete (globalThis as any).MediaSource;
    }
    vi.restoreAllMocks();
  });

  // =========================================================================
  // getBrowserInfo — UA parsing
  // =========================================================================
  describe("getBrowserInfo", () => {
    it.each([
      [
        "Chrome",
        "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
        { isChrome: true, isFirefox: false, isSafari: false, isEdge: false },
      ],
      [
        "Firefox",
        "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:120.0) Gecko/20100101 Firefox/120.0",
        { isChrome: false, isFirefox: true, isSafari: false, isEdge: false },
      ],
      [
        "Safari",
        "Mozilla/5.0 (Macintosh; Intel Mac OS X 14_0) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Safari/605.1.15",
        { isChrome: false, isFirefox: false, isSafari: true, isEdge: false },
      ],
      [
        "Edge",
        "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36 Edg/120.0.0.0",
        { isChrome: false, isFirefox: false, isSafari: false, isEdge: true },
      ],
    ])("%s detection", (_name, ua, expected) => {
      stubNavigator(ua);
      stubWindow();

      const info = getBrowserInfo();
      expect(info.isChrome).toBe(expected.isChrome);
      expect(info.isFirefox).toBe(expected.isFirefox);
      expect(info.isSafari).toBe(expected.isSafari);
      expect(info.isEdge).toBe(expected.isEdge);
    });

    it("detects Android", () => {
      stubNavigator(
        "Mozilla/5.0 (Linux; Android 14) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Mobile Safari/537.36"
      );
      stubWindow();

      const info = getBrowserInfo();
      expect(info.isAndroid).toBe(true);
      expect(info.isMobile).toBe(true);
    });

    it("detects iOS", () => {
      stubNavigator(
        "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1"
      );
      stubWindow();

      const info = getBrowserInfo();
      expect(info.isIOS).toBe(true);
      expect(info.isMobile).toBe(true);
    });

    it("detects MediaSource support", () => {
      stubNavigator("Chrome");
      stubWindow({ MediaSource: class {} });

      const info = getBrowserInfo();
      expect(info.supportsMediaSource).toBe(true);
    });

    it("detects WebRTC support", () => {
      stubNavigator("Chrome");
      stubWindow({ RTCPeerConnection: class {} });

      const info = getBrowserInfo();
      expect(info.supportsWebRTC).toBe(true);
    });
  });

  // =========================================================================
  // testCodecSupport
  // =========================================================================
  describe("testCodecSupport", () => {
    it("returns false when MediaSource not in window", () => {
      stubWindow();
      expect(testCodecSupport("video/mp4", "avc1.42E01E")).toBe(false);
    });

    it("returns true when isTypeSupported is not available", () => {
      stubWindow({ MediaSource: {} }); // no isTypeSupported method
      expect(testCodecSupport("video/mp4", "avc1.42E01E")).toBe(true);
    });

    it("delegates to MediaSource.isTypeSupported", () => {
      const isTypeSupported = vi.fn().mockReturnValue(true);
      stubWindow({ MediaSource: { isTypeSupported } });

      expect(testCodecSupport("video/mp4", "avc1.42E01E")).toBe(true);
      expect(isTypeSupported).toHaveBeenCalledWith('video/mp4;codecs="avc1.42E01E"');
    });
  });

  // =========================================================================
  // getCodecSupport
  // =========================================================================
  describe("getCodecSupport", () => {
    it("returns all false when no MediaSource", () => {
      stubWindow();
      stubNavigator("Chrome");

      const support = getCodecSupport();
      expect(support.h264).toBe(false);
      expect(support.vp8).toBe(false);
      expect(support.opus).toBe(false);
    });

    it("delegates to isTypeSupported for each codec", () => {
      const isTypeSupported = vi.fn().mockImplementation((mime: string) => {
        return mime.includes("avc1") || mime.includes("opus");
      });
      stubWindow({ MediaSource: { isTypeSupported } });
      stubNavigator("Chrome");

      const support = getCodecSupport();
      expect(support.h264).toBe(true);
      expect(support.opus).toBe(true);
      expect(support.vp8).toBe(false);
    });
  });

  // =========================================================================
  // checkTrackPlayability
  // =========================================================================
  describe("checkTrackPlayability", () => {
    it("skips meta tracks", () => {
      stubWindow();
      stubNavigator("Chrome");

      const result = checkTrackPlayability(
        [
          { type: "meta", codec: "JSON" },
          { type: "video", codec: "H264" },
        ],
        "video/mp4"
      );

      // No MediaSource → nothing supported, but meta was skipped
      expect(result.playable).toEqual([]);
      expect(result.supported).toEqual([]);
    });

    it("identifies playable track types when codec is supported", () => {
      const isTypeSupported = vi.fn().mockReturnValue(true);
      stubWindow({ MediaSource: { isTypeSupported } });
      stubNavigator("Chrome");

      const result = checkTrackPlayability(
        [
          { type: "video", codec: "H264" },
          { type: "audio", codec: "AAC" },
        ],
        "video/mp4"
      );

      expect(result.playable).toContain("video");
      expect(result.playable).toContain("audio");
      expect(result.supported).toContain("H264");
      expect(result.supported).toContain("AAC");
    });

    it("handles mixed support (some codecs unsupported)", () => {
      const isTypeSupported = vi.fn().mockImplementation((mime: string) => {
        return mime.includes("avc1"); // Only H264 supported
      });
      stubWindow({ MediaSource: { isTypeSupported } });
      stubNavigator("Chrome");

      const result = checkTrackPlayability(
        [
          { type: "video", codec: "H264" },
          { type: "video", codec: "VP9" },
        ],
        "video/mp4"
      );

      expect(result.playable).toContain("video");
      expect(result.supported).toContain("H264");
      expect(result.supported).not.toContain("VP9");
    });
  });

  // =========================================================================
  // checkProtocolMismatch
  // =========================================================================
  describe("checkProtocolMismatch", () => {
    it("no mismatch for same protocol", () => {
      stubWindow({ location: { protocol: "https:" } });
      expect(checkProtocolMismatch("https://example.com/stream")).toBe(false);
    });

    it("mismatch for http page + https source", () => {
      stubWindow({ location: { protocol: "http:" } });
      expect(checkProtocolMismatch("https://example.com/stream")).toBe(true);
    });

    it("mismatch for https page + http source", () => {
      stubWindow({ location: { protocol: "https:" } });
      expect(checkProtocolMismatch("http://example.com/stream")).toBe(true);
    });

    it("file:// page accessing http:// is not a mismatch", () => {
      stubWindow({ location: { protocol: "file:" } });
      expect(checkProtocolMismatch("http://example.com/stream")).toBe(false);
    });

    it("file:// page accessing https:// is a mismatch", () => {
      stubWindow({ location: { protocol: "file:" } });
      expect(checkProtocolMismatch("https://example.com/stream")).toBe(true);
    });
  });

  // =========================================================================
  // isFileProtocol
  // =========================================================================
  describe("isFileProtocol", () => {
    it("true for file://", () => {
      stubWindow({ location: { protocol: "file:" } });
      expect(isFileProtocol()).toBe(true);
    });

    it("false for https://", () => {
      stubWindow({ location: { protocol: "https:" } });
      expect(isFileProtocol()).toBe(false);
    });
  });

  // =========================================================================
  // getAndroidVersion
  // =========================================================================
  describe("getAndroidVersion", () => {
    it.each([
      ["Android 14.0", 14.0],
      ["Android 13", 13.0],
      ["Android 7.1.2", 7.1],
      ["Android 5.0.1", 5.0],
    ])('"%s" → %s', (uaPart, expected) => {
      stubNavigator(`Mozilla/5.0 (Linux; ${uaPart}) AppleWebKit/537.36`);
      expect(getAndroidVersion()).toBeCloseTo(expected, 1);
    });

    it("returns null for non-Android UA", () => {
      stubNavigator("Mozilla/5.0 (Macintosh; Intel Mac OS X 14_0) AppleWebKit/605.1.15");
      expect(getAndroidVersion()).toBeNull();
    });
  });

  // =========================================================================
  // checkWebRTCCodecCompatibility
  // =========================================================================
  describe("checkWebRTCCodecCompatibility", () => {
    // Without RTCRtpReceiver, falls back to static lists

    it("H264 video + OPUS audio is compatible", () => {
      const result = checkWebRTCCodecCompatibility([
        { type: "video", codec: "H264" },
        { type: "audio", codec: "OPUS" },
      ]);
      expect(result.compatible).toBe(true);
      expect(result.videoCompatible).toBe(true);
      expect(result.audioCompatible).toBe(true);
      expect(result.incompatibleCodecs).toEqual([]);
    });

    it("HEVC video is incompatible but stream is partially compatible", () => {
      const result = checkWebRTCCodecCompatibility([
        { type: "video", codec: "HEVC" },
        { type: "audio", codec: "OPUS" },
      ]);
      expect(result.videoCompatible).toBe(false);
      expect(result.audioCompatible).toBe(true);
      expect(result.compatible).toBe(true);
      expect(result.incompatibleCodecs).toContain("video:HEVC");
    });

    it("AAC audio is incompatible but stream is partially compatible", () => {
      const result = checkWebRTCCodecCompatibility([
        { type: "video", codec: "H264" },
        { type: "audio", codec: "AAC" },
      ]);
      expect(result.videoCompatible).toBe(true);
      expect(result.audioCompatible).toBe(false);
      expect(result.compatible).toBe(true);
    });

    it("audio-only stream (no video) is compatible if audio codec is ok", () => {
      const result = checkWebRTCCodecCompatibility([{ type: "audio", codec: "OPUS" }]);
      expect(result.videoCompatible).toBe(true); // no video tracks = compatible
      expect(result.audioCompatible).toBe(true);
      expect(result.compatible).toBe(true);
    });

    it("video-only stream (no audio) is compatible if video codec is ok", () => {
      const result = checkWebRTCCodecCompatibility([{ type: "video", codec: "VP8" }]);
      expect(result.compatible).toBe(true);
    });

    it("reports details correctly", () => {
      const result = checkWebRTCCodecCompatibility([
        { type: "video", codec: "H264" },
        { type: "video", codec: "VP8" },
        { type: "audio", codec: "OPUS" },
        { type: "audio", codec: "MP3" },
      ]);
      expect(result.details.videoTracks).toBe(2);
      expect(result.details.audioTracks).toBe(2);
      expect(result.details.compatibleVideoCodecs).toContain("H264");
      expect(result.details.compatibleVideoCodecs).toContain("VP8");
      expect(result.details.compatibleAudioCodecs).toContain("OPUS");
      expect(result.incompatibleCodecs).toContain("audio:MP3");
    });

    it("uses RTCRtpReceiver.getCapabilities when available", () => {
      const origRTC = (globalThis as any).RTCRtpReceiver;
      (globalThis as any).RTCRtpReceiver = {
        getCapabilities: vi.fn().mockImplementation((type: string) => {
          if (type === "video") {
            return { codecs: [{ mimeType: "video/H264" }] };
          }
          if (type === "audio") {
            return { codecs: [{ mimeType: "audio/opus" }] };
          }
          return null;
        }),
      };

      const result = checkWebRTCCodecCompatibility([
        { type: "video", codec: "H264" },
        { type: "video", codec: "VP8" },
        { type: "audio", codec: "opus" },
      ]);

      expect(result.details.compatibleVideoCodecs).toContain("H264");
      // VP8 not in mock capabilities
      expect(result.incompatibleCodecs).toContain("video:VP8");

      (globalThis as any).RTCRtpReceiver = origRTC;
    });
  });

  // =========================================================================
  // checkMSECodecCompatibility
  // =========================================================================
  describe("checkMSECodecCompatibility", () => {
    it("returns all compatible when MediaSource supports everything", () => {
      const isTypeSupported = vi.fn().mockReturnValue(true);
      stubWindow({ MediaSource: { isTypeSupported } });
      stubNavigator("Chrome");

      const result = checkMSECodecCompatibility([
        { type: "video", codec: "H264" },
        { type: "audio", codec: "AAC" },
      ]);

      expect(result.compatible).toBe(true);
      expect(result.unsupportedCodecs).toEqual([]);
    });

    it("marks unsupported codecs", () => {
      const isTypeSupported = vi.fn().mockImplementation((mime: string) => {
        return mime.includes("avc1"); // Only H264
      });
      stubWindow({ MediaSource: { isTypeSupported } });
      stubNavigator("Chrome");

      const result = checkMSECodecCompatibility([
        { type: "video", codec: "H264" },
        { type: "audio", codec: "AAC" },
      ]);

      expect(result.videoCompatible).toBe(true);
      expect(result.audioCompatible).toBe(false);
      expect(result.unsupportedCodecs).toContain("audio:AAC");
    });

    it("video-only streams: audio is compatible (no audio tracks)", () => {
      stubWindow();
      stubNavigator("Chrome");

      const result = checkMSECodecCompatibility([{ type: "video", codec: "H264" }]);

      expect(result.audioCompatible).toBe(true);
    });

    it("uses audio/mpeg container for MP3", () => {
      const isTypeSupported = vi.fn().mockImplementation((mime: string) => {
        return mime.includes("audio/mpeg"); // Only audio/mpeg container
      });
      stubWindow({ MediaSource: { isTypeSupported } });
      stubNavigator("Chrome");

      const result = checkMSECodecCompatibility([{ type: "audio", codec: "MP3" }]);

      expect(result.audioCompatible).toBe(true);
    });
  });
});

import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";

import {
  detectCapabilities,
  isWebCodecsSupported,
  isWebRTCSupported,
  isMediaDevicesSupported,
  isScreenCaptureSupported,
  isRTCRtpScriptTransformSupported,
  isWebCodecsEncodingPathSupported,
  getRecommendedPath,
  isVideoCodecSupported,
  isAudioCodecSupported,
  getSupportedVideoCodecs,
  getSupportedAudioCodecs,
} from "../src/core/FeatureDetection";

// Stash originals for cleanup
const originals: Record<string, unknown> = {};

function stubGlobal(name: string, value: unknown) {
  originals[name] = (globalThis as Record<string, unknown>)[name];
  (globalThis as Record<string, unknown>)[name] = value;
}

function clearGlobal(name: string) {
  if (name in originals) {
    if (originals[name] === undefined) {
      delete (globalThis as Record<string, unknown>)[name];
    } else {
      (globalThis as Record<string, unknown>)[name] = originals[name];
    }
  } else {
    delete (globalThis as Record<string, unknown>)[name];
  }
}

const ALL_GLOBALS = [
  "VideoEncoder",
  "AudioEncoder",
  "MediaStreamTrackProcessor",
  "MediaStreamTrackGenerator",
  "RTCPeerConnection",
  "RTCRtpSender",
  "RTCRtpScriptTransform",
];

function clearAllStubs() {
  for (const name of ALL_GLOBALS) {
    clearGlobal(name);
  }
  delete originals["navigator"];
}

function stubAllWebCodecs() {
  stubGlobal("VideoEncoder", { isConfigSupported: vi.fn() });
  stubGlobal("AudioEncoder", { isConfigSupported: vi.fn() });
  stubGlobal("MediaStreamTrackProcessor", class {});
  stubGlobal("MediaStreamTrackGenerator", class {});
}

function stubAllWebRTC() {
  stubGlobal("RTCPeerConnection", class {});
  stubGlobal("RTCRtpSender", {
    prototype: { replaceTrack: vi.fn(), createEncodedStreams: vi.fn() },
  });
  stubGlobal("RTCRtpScriptTransform", class {});
}

describe("FeatureDetection", () => {
  beforeEach(() => {
    // Clear all browser API globals to start from a clean state
    for (const name of ALL_GLOBALS) {
      originals[name] = (globalThis as Record<string, unknown>)[name];
      delete (globalThis as Record<string, unknown>)[name];
    }
  });

  afterEach(() => {
    clearAllStubs();
    vi.restoreAllMocks();
  });

  // =========================================================================
  // detectCapabilities — all present
  // =========================================================================
  describe("detectCapabilities", () => {
    it("detects full support when all globals present", () => {
      stubAllWebCodecs();
      stubAllWebRTC();

      const caps = detectCapabilities();
      expect(caps.webcodecs.videoEncoder).toBe(true);
      expect(caps.webcodecs.audioEncoder).toBe(true);
      expect(caps.webcodecs.mediaStreamTrackProcessor).toBe(true);
      expect(caps.webcodecs.mediaStreamTrackGenerator).toBe(true);
      expect(caps.webrtc.peerConnection).toBe(true);
      expect(caps.webrtc.replaceTrack).toBe(true);
      expect(caps.webrtc.insertableStreams).toBe(true);
      expect(caps.webrtc.scriptTransform).toBe(true);
      expect(caps.recommended).toBe("webcodecs");
    });

    it("detects no support when nothing present", () => {
      const caps = detectCapabilities();
      expect(caps.webcodecs.videoEncoder).toBe(false);
      expect(caps.webcodecs.audioEncoder).toBe(false);
      expect(caps.webrtc.peerConnection).toBe(false);
      expect(caps.recommended).toBe("mediastream");
    });

    it("recommends mediastream when only partial WebCodecs", () => {
      stubGlobal("VideoEncoder", {});
      stubGlobal("AudioEncoder", {});
      // Missing MediaStreamTrackProcessor/Generator

      const caps = detectCapabilities();
      expect(caps.webcodecs.videoEncoder).toBe(true);
      expect(caps.webcodecs.audioEncoder).toBe(true);
      expect(caps.webcodecs.mediaStreamTrackProcessor).toBe(false);
      expect(caps.recommended).toBe("mediastream");
    });

    it("detects mediaDevices capabilities", () => {
      // navigator.mediaDevices is available in Node via stubs
      const origNav = globalThis.navigator;
      Object.defineProperty(globalThis, "navigator", {
        value: {
          mediaDevices: {
            getUserMedia: vi.fn(),
            getDisplayMedia: vi.fn(),
            enumerateDevices: vi.fn(),
          },
        },
        configurable: true,
        writable: true,
      });

      const caps = detectCapabilities();
      expect(caps.mediaDevices.getUserMedia).toBe(true);
      expect(caps.mediaDevices.getDisplayMedia).toBe(true);
      expect(caps.mediaDevices.enumerateDevices).toBe(true);

      Object.defineProperty(globalThis, "navigator", {
        value: origNav,
        configurable: true,
        writable: true,
      });
    });
  });

  // =========================================================================
  // Boolean helpers
  // =========================================================================
  describe("isWebCodecsSupported", () => {
    it("true when all 4 globals present", () => {
      stubAllWebCodecs();
      expect(isWebCodecsSupported()).toBe(true);
    });

    it("false when any global missing", () => {
      stubGlobal("VideoEncoder", {});
      stubGlobal("AudioEncoder", {});
      stubGlobal("MediaStreamTrackProcessor", class {});
      // Missing MediaStreamTrackGenerator
      expect(isWebCodecsSupported()).toBe(false);
    });

    it("false when none present", () => {
      expect(isWebCodecsSupported()).toBe(false);
    });
  });

  describe("isWebRTCSupported", () => {
    it("true when RTCPeerConnection present", () => {
      stubGlobal("RTCPeerConnection", class {});
      expect(isWebRTCSupported()).toBe(true);
    });

    it("false when not present", () => {
      expect(isWebRTCSupported()).toBe(false);
    });
  });

  describe("isMediaDevicesSupported", () => {
    it("false when navigator undefined", () => {
      // In Node test env, navigator may not have mediaDevices
      const origNav = globalThis.navigator;
      Object.defineProperty(globalThis, "navigator", {
        value: undefined,
        configurable: true,
        writable: true,
      });
      expect(isMediaDevicesSupported()).toBe(false);
      Object.defineProperty(globalThis, "navigator", {
        value: origNav,
        configurable: true,
        writable: true,
      });
    });
  });

  describe("isScreenCaptureSupported", () => {
    it("true when getDisplayMedia available", () => {
      const origNav = globalThis.navigator;
      Object.defineProperty(globalThis, "navigator", {
        value: { mediaDevices: { getDisplayMedia: vi.fn() } },
        configurable: true,
        writable: true,
      });
      expect(isScreenCaptureSupported()).toBe(true);
      Object.defineProperty(globalThis, "navigator", {
        value: origNav,
        configurable: true,
        writable: true,
      });
    });

    it("false when getDisplayMedia not available", () => {
      const origNav = globalThis.navigator;
      Object.defineProperty(globalThis, "navigator", {
        value: { mediaDevices: {} },
        configurable: true,
        writable: true,
      });
      expect(isScreenCaptureSupported()).toBe(false);
      Object.defineProperty(globalThis, "navigator", {
        value: origNav,
        configurable: true,
        writable: true,
      });
    });
  });

  describe("isRTCRtpScriptTransformSupported", () => {
    it("true when present", () => {
      stubGlobal("RTCRtpScriptTransform", class {});
      expect(isRTCRtpScriptTransformSupported()).toBe(true);
    });

    it("false when not present", () => {
      expect(isRTCRtpScriptTransformSupported()).toBe(false);
    });
  });

  describe("isWebCodecsEncodingPathSupported", () => {
    it("true when both WebCodecs + ScriptTransform present", () => {
      stubAllWebCodecs();
      stubGlobal("RTCRtpScriptTransform", class {});
      expect(isWebCodecsEncodingPathSupported()).toBe(true);
    });

    it("false when WebCodecs but no ScriptTransform", () => {
      stubAllWebCodecs();
      expect(isWebCodecsEncodingPathSupported()).toBe(false);
    });

    it("false when ScriptTransform but no WebCodecs", () => {
      stubGlobal("RTCRtpScriptTransform", class {});
      expect(isWebCodecsEncodingPathSupported()).toBe(false);
    });
  });

  // =========================================================================
  // getRecommendedPath
  // =========================================================================
  describe("getRecommendedPath", () => {
    it("returns webcodecs when fully supported", () => {
      stubAllWebCodecs();
      expect(getRecommendedPath()).toBe("webcodecs");
    });

    it("returns mediastream when not fully supported", () => {
      expect(getRecommendedPath()).toBe("mediastream");
    });
  });

  // =========================================================================
  // Async codec checks
  // =========================================================================
  describe("isVideoCodecSupported", () => {
    it("returns false when WebCodecs not available", async () => {
      expect(await isVideoCodecSupported("avc1.42E01E")).toBe(false);
    });

    it("returns true when codec supported", async () => {
      stubAllWebCodecs();
      (VideoEncoder as any).isConfigSupported = vi.fn().mockResolvedValue({ supported: true });
      expect(await isVideoCodecSupported("avc1.42E01E")).toBe(true);
    });

    it("returns false when codec not supported", async () => {
      stubAllWebCodecs();
      (VideoEncoder as any).isConfigSupported = vi.fn().mockResolvedValue({ supported: false });
      expect(await isVideoCodecSupported("vp8")).toBe(false);
    });

    it("returns false when isConfigSupported throws", async () => {
      stubAllWebCodecs();
      (VideoEncoder as any).isConfigSupported = vi.fn().mockRejectedValue(new Error("fail"));
      expect(await isVideoCodecSupported("bad")).toBe(false);
    });
  });

  describe("isAudioCodecSupported", () => {
    it("returns false when WebCodecs not available", async () => {
      expect(await isAudioCodecSupported("opus")).toBe(false);
    });

    it("returns true when codec supported", async () => {
      stubAllWebCodecs();
      (AudioEncoder as any).isConfigSupported = vi.fn().mockResolvedValue({ supported: true });
      expect(await isAudioCodecSupported("opus")).toBe(true);
    });

    it("returns false when isConfigSupported throws", async () => {
      stubAllWebCodecs();
      (AudioEncoder as any).isConfigSupported = vi.fn().mockRejectedValue(new Error("fail"));
      expect(await isAudioCodecSupported("bad")).toBe(false);
    });
  });

  // =========================================================================
  // getSupportedVideoCodecs / getSupportedAudioCodecs
  // =========================================================================
  describe("getSupportedVideoCodecs", () => {
    it("returns empty when WebCodecs not available", async () => {
      expect(await getSupportedVideoCodecs()).toEqual([]);
    });

    it("returns only supported codecs", async () => {
      stubAllWebCodecs();
      (VideoEncoder as any).isConfigSupported = vi.fn().mockImplementation(async (config: any) => {
        return { supported: config.codec === "vp8" || config.codec === "avc1.42E01E" };
      });
      const codecs = await getSupportedVideoCodecs();
      expect(codecs).toContain("avc1.42E01E");
      expect(codecs).toContain("vp8");
      expect(codecs).not.toContain("vp09.00.10.08");
    });
  });

  describe("getSupportedAudioCodecs", () => {
    it("returns empty when WebCodecs not available", async () => {
      expect(await getSupportedAudioCodecs()).toEqual([]);
    });

    it("returns only supported codecs", async () => {
      stubAllWebCodecs();
      (AudioEncoder as any).isConfigSupported = vi.fn().mockImplementation(async (config: any) => {
        return { supported: config.codec === "opus" };
      });
      const codecs = await getSupportedAudioCodecs();
      expect(codecs).toEqual(["opus"]);
    });
  });
});

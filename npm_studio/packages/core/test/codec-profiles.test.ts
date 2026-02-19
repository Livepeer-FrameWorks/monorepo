import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";

import {
  selectH264Codec,
  selectVP9Codec,
  selectAV1Codec,
  selectCodecString,
  getDefaultVideoSettings,
  getDefaultAudioSettings,
  getKeyframeInterval,
  createEncoderConfig,
  mimeToCodecFamily,
  detectEncoderCapabilities,
  type VideoCodecFamily,
} from "../src/core/CodecProfiles";

describe("CodecProfiles", () => {
  // ===========================================================================
  // selectH264Codec
  // ===========================================================================
  describe("selectH264Codec", () => {
    it("returns Level 3.1 for sub-720p", () => {
      expect(selectH264Codec(640, 480, 30)).toBe("avc1.64001f");
      expect(selectH264Codec(320, 240, 24)).toBe("avc1.64001f");
    });

    it("returns Level 4.0 for 720p@30", () => {
      expect(selectH264Codec(1280, 720, 30)).toBe("avc1.640028");
    });

    it("returns Level 4.2 for 720p@60", () => {
      expect(selectH264Codec(1280, 720, 60)).toBe("avc1.64002a");
    });

    it("returns Level 4.2 for 1080p@30", () => {
      expect(selectH264Codec(1920, 1080, 30)).toBe("avc1.64002a");
    });

    it("returns Level 5.0 for 1080p@60", () => {
      expect(selectH264Codec(1920, 1080, 60)).toBe("avc1.640032");
    });

    it("returns Level 5.0 for 1440p@30", () => {
      expect(selectH264Codec(2560, 1440, 30)).toBe("avc1.640032");
    });

    it("returns Level 5.1 for 1440p@60", () => {
      expect(selectH264Codec(2560, 1440, 60)).toBe("avc1.640033");
    });

    it("returns Level 5.1 for 4K@30", () => {
      expect(selectH264Codec(3840, 2160, 30)).toBe("avc1.640033");
    });

    it("returns Level 5.2 for 4K@60", () => {
      expect(selectH264Codec(3840, 2160, 60)).toBe("avc1.640034");
    });
  });

  // ===========================================================================
  // selectVP9Codec
  // ===========================================================================
  describe("selectVP9Codec", () => {
    it("returns Level 2.0 for sub-720p", () => {
      expect(selectVP9Codec(640, 480, 30)).toBe("vp09.00.20.08");
    });

    it("returns Level 3.0 for 720p@30", () => {
      expect(selectVP9Codec(1280, 720, 30)).toBe("vp09.00.30.08");
    });

    it("returns Level 3.1 for 720p@60", () => {
      expect(selectVP9Codec(1280, 720, 60)).toBe("vp09.00.31.08");
    });

    it("returns Level 4.0 for 1080p@30", () => {
      expect(selectVP9Codec(1920, 1080, 30)).toBe("vp09.00.40.08");
    });

    it("returns Level 4.1 for 1080p@60", () => {
      expect(selectVP9Codec(1920, 1080, 60)).toBe("vp09.00.41.08");
    });

    it("returns Level 4.1 for 1440p@30", () => {
      expect(selectVP9Codec(2560, 1440, 30)).toBe("vp09.00.41.08");
    });

    it("returns Level 5.0 for 1440p@60", () => {
      expect(selectVP9Codec(2560, 1440, 60)).toBe("vp09.00.50.08");
    });

    it("returns Level 5.0 for 4K@30", () => {
      expect(selectVP9Codec(3840, 2160, 30)).toBe("vp09.00.50.08");
    });

    it("returns Level 5.1 for 4K@60", () => {
      expect(selectVP9Codec(3840, 2160, 60)).toBe("vp09.00.51.08");
    });
  });

  // ===========================================================================
  // selectAV1Codec
  // ===========================================================================
  describe("selectAV1Codec", () => {
    it("returns Level 2.1 for sub-720p", () => {
      expect(selectAV1Codec(640, 480, 30)).toBe("av01.0.05M.08");
    });

    it("returns Level 3.0 for 720p@30", () => {
      expect(selectAV1Codec(1280, 720, 30)).toBe("av01.0.08M.08");
    });

    it("returns Level 3.1 for 720p@60", () => {
      expect(selectAV1Codec(1280, 720, 60)).toBe("av01.0.09M.08");
    });

    it("returns Level 3.1 for 1080p@30", () => {
      expect(selectAV1Codec(1920, 1080, 30)).toBe("av01.0.09M.08");
    });

    it("returns Level 4.0 for 1080p@60", () => {
      expect(selectAV1Codec(1920, 1080, 60)).toBe("av01.0.12M.08");
    });

    it("returns Level 4.1 for 1440p@30", () => {
      expect(selectAV1Codec(2560, 1440, 30)).toBe("av01.0.13M.08");
    });

    it("returns Level 5.0 for 1440p@60", () => {
      expect(selectAV1Codec(2560, 1440, 60)).toBe("av01.0.16M.08");
    });

    it("returns Level 5.0 for 4K@30", () => {
      expect(selectAV1Codec(3840, 2160, 30)).toBe("av01.0.16M.08");
    });

    it("returns Level 5.1 for 4K@60", () => {
      expect(selectAV1Codec(3840, 2160, 60)).toBe("av01.0.17M.08");
    });
  });

  // ===========================================================================
  // selectCodecString (dispatcher)
  // ===========================================================================
  describe("selectCodecString", () => {
    it("dispatches to H.264 selector", () => {
      expect(selectCodecString("h264", 1920, 1080, 30)).toBe(selectH264Codec(1920, 1080, 30));
    });

    it("dispatches to VP9 selector", () => {
      expect(selectCodecString("vp9", 1920, 1080, 30)).toBe(selectVP9Codec(1920, 1080, 30));
    });

    it("dispatches to AV1 selector", () => {
      expect(selectCodecString("av1", 1920, 1080, 30)).toBe(selectAV1Codec(1920, 1080, 30));
    });

    it("defaults to H.264 for unknown family", () => {
      expect(selectCodecString("unknown" as VideoCodecFamily, 1280, 720, 30)).toBe(
        selectH264Codec(1280, 720, 30)
      );
    });
  });

  // ===========================================================================
  // getDefaultVideoSettings
  // ===========================================================================
  describe("getDefaultVideoSettings", () => {
    it("returns correct H.264 broadcast settings", () => {
      const s = getDefaultVideoSettings("broadcast", "h264");
      expect(s.width).toBe(1920);
      expect(s.height).toBe(1080);
      expect(s.framerate).toBe(30);
      expect(s.bitrate).toBe(4_500_000);
      expect(s.codec).toBe(selectH264Codec(1920, 1080, 30));
    });

    it("returns lower bitrate for VP9 at same profile", () => {
      const h264 = getDefaultVideoSettings("broadcast", "h264");
      const vp9 = getDefaultVideoSettings("broadcast", "vp9");
      expect(vp9.bitrate).toBeLessThan(h264.bitrate);
    });

    it("returns lower bitrate for AV1 than VP9", () => {
      const vp9 = getDefaultVideoSettings("broadcast", "vp9");
      const av1 = getDefaultVideoSettings("broadcast", "av1");
      expect(av1.bitrate).toBeLessThan(vp9.bitrate);
    });

    it("uses conference resolution for conference profile", () => {
      const s = getDefaultVideoSettings("conference", "h264");
      expect(s.width).toBe(1280);
      expect(s.height).toBe(720);
    });

    it("uses low resolution for low profile", () => {
      const s = getDefaultVideoSettings("low", "vp9");
      expect(s.width).toBe(640);
      expect(s.height).toBe(480);
      expect(s.framerate).toBe(24);
    });

    it("falls back to broadcast for unknown profile", () => {
      const s = getDefaultVideoSettings("nonexistent", "h264");
      expect(s.width).toBe(1920);
      expect(s.bitrate).toBe(4_500_000);
    });
  });

  // ===========================================================================
  // getDefaultAudioSettings
  // ===========================================================================
  describe("getDefaultAudioSettings", () => {
    it("always uses opus at 48kHz stereo", () => {
      const profiles = ["professional", "broadcast", "conference", "low"];
      for (const profile of profiles) {
        const s = getDefaultAudioSettings(profile);
        expect(s.codec).toBe("opus");
        expect(s.sampleRate).toBe(48000);
        expect(s.numberOfChannels).toBe(2);
      }
    });

    it("uses profile-specific bitrate", () => {
      expect(getDefaultAudioSettings("professional").bitrate).toBe(192_000);
      expect(getDefaultAudioSettings("broadcast").bitrate).toBe(128_000);
      expect(getDefaultAudioSettings("conference").bitrate).toBe(96_000);
      expect(getDefaultAudioSettings("low").bitrate).toBe(64_000);
    });

    it("falls back to broadcast bitrate for unknown profile", () => {
      expect(getDefaultAudioSettings("nonexistent").bitrate).toBe(128_000);
    });
  });

  // ===========================================================================
  // getKeyframeInterval
  // ===========================================================================
  describe("getKeyframeInterval", () => {
    it("returns 60 for H.264", () => {
      expect(getKeyframeInterval("h264")).toBe(60);
    });

    it("returns 120 for VP9", () => {
      expect(getKeyframeInterval("vp9")).toBe(120);
    });

    it("returns 120 for AV1", () => {
      expect(getKeyframeInterval("av1")).toBe(120);
    });

    it("falls back to 60 for unknown codec", () => {
      expect(getKeyframeInterval("unknown" as VideoCodecFamily)).toBe(60);
    });
  });

  // ===========================================================================
  // createEncoderConfig
  // ===========================================================================
  describe("createEncoderConfig", () => {
    it("creates default broadcast H.264 config", () => {
      const config = createEncoderConfig();
      expect(config.video.codec).toContain("avc1");
      expect(config.video.width).toBe(1920);
      expect(config.video.height).toBe(1080);
      expect(config.video.bitrate).toBe(4_500_000);
      expect(config.audio.codec).toBe("opus");
    });

    it("creates VP9 config with lower bitrate", () => {
      const config = createEncoderConfig("broadcast", "vp9");
      expect(config.video.codec).toContain("vp09");
      expect(config.video.bitrate).toBe(3_000_000);
    });

    it("creates AV1 config", () => {
      const config = createEncoderConfig("broadcast", "av1");
      expect(config.video.codec).toContain("av01");
      expect(config.video.bitrate).toBe(2_200_000);
    });

    it("applies video resolution overrides and re-selects codec", () => {
      const config = createEncoderConfig("broadcast", "h264", {
        video: { width: 3840, height: 2160 },
      });
      expect(config.video.width).toBe(3840);
      expect(config.video.height).toBe(2160);
      // Should select a higher level codec for 4K
      expect(config.video.codec).toBe(selectH264Codec(3840, 2160, 30));
    });

    it("applies video bitrate override", () => {
      const config = createEncoderConfig("broadcast", "h264", {
        video: { bitrate: 10_000_000 },
      });
      expect(config.video.bitrate).toBe(10_000_000);
    });

    it("applies framerate override and re-selects codec", () => {
      const config = createEncoderConfig("broadcast", "vp9", {
        video: { framerate: 60 },
      });
      expect(config.video.framerate).toBe(60);
      expect(config.video.codec).toBe(selectVP9Codec(1920, 1080, 60));
    });

    it("applies audio overrides", () => {
      const config = createEncoderConfig("broadcast", "h264", {
        audio: { bitrate: 256_000, sampleRate: 44100, numberOfChannels: 1 },
      });
      expect(config.audio.bitrate).toBe(256_000);
      expect(config.audio.sampleRate).toBe(44100);
      expect(config.audio.numberOfChannels).toBe(1);
    });

    it("does not override audio when no audio overrides given", () => {
      const config = createEncoderConfig("professional", "h264", {
        video: { width: 1280, height: 720 },
      });
      expect(config.audio.bitrate).toBe(192_000);
      expect(config.audio.sampleRate).toBe(48000);
    });
  });

  // ===========================================================================
  // mimeToCodecFamily
  // ===========================================================================
  describe("mimeToCodecFamily", () => {
    it("detects VP9", () => {
      expect(mimeToCodecFamily("video/vp9")).toBe("vp9");
      expect(mimeToCodecFamily("video/VP9")).toBe("vp9");
    });

    it("detects AV1 variants", () => {
      expect(mimeToCodecFamily("video/av1")).toBe("av1");
      expect(mimeToCodecFamily("video/AV1")).toBe("av1");
      expect(mimeToCodecFamily("video/av01")).toBe("av1");
    });

    it("detects H.264 variants", () => {
      expect(mimeToCodecFamily("video/h264")).toBe("h264");
      expect(mimeToCodecFamily("video/H264")).toBe("h264");
      expect(mimeToCodecFamily("video/avc")).toBe("h264");
    });

    it("returns null for unrecognized MIME", () => {
      expect(mimeToCodecFamily("video/unknown")).toBeNull();
      expect(mimeToCodecFamily("audio/opus")).toBeNull();
      expect(mimeToCodecFamily("")).toBeNull();
    });
  });

  // ===========================================================================
  // detectEncoderCapabilities
  // ===========================================================================
  describe("detectEncoderCapabilities", () => {
    afterEach(() => {
      vi.restoreAllMocks();
    });

    it("returns H.264 fallback when VideoEncoder is not available", async () => {
      const original = (globalThis as any).VideoEncoder;
      delete (globalThis as any).VideoEncoder;
      try {
        const caps = await detectEncoderCapabilities();
        expect(caps.video).toEqual(["h264"]);
        expect(caps.audio).toEqual(["opus"]);
        expect(caps.recommended).toBe("h264");
      } finally {
        if (original !== undefined) {
          (globalThis as any).VideoEncoder = original;
        }
      }
    });

    it("detects all three codecs when all supported", async () => {
      (globalThis as any).VideoEncoder = {
        isConfigSupported: vi.fn().mockResolvedValue({ supported: true }),
      };
      try {
        const caps = await detectEncoderCapabilities();
        expect(caps.video).toContain("h264");
        expect(caps.video).toContain("vp9");
        expect(caps.video).toContain("av1");
        // AV1 is the top recommendation when available
        expect(caps.recommended).toBe("av1");
      } finally {
        delete (globalThis as any).VideoEncoder;
      }
    });

    it("recommends VP9 when AV1 is not available", async () => {
      let callCount = 0;
      (globalThis as any).VideoEncoder = {
        isConfigSupported: vi.fn().mockImplementation(async (config: any) => {
          if (config.codec.startsWith("av01")) return { supported: false };
          return { supported: true };
        }),
      };
      try {
        const caps = await detectEncoderCapabilities();
        expect(caps.video).toContain("h264");
        expect(caps.video).toContain("vp9");
        expect(caps.video).not.toContain("av1");
        expect(caps.recommended).toBe("vp9");
      } finally {
        delete (globalThis as any).VideoEncoder;
      }
    });

    it("falls back to H.264 array when all checks throw", async () => {
      (globalThis as any).VideoEncoder = {
        isConfigSupported: vi.fn().mockRejectedValue(new Error("not supported")),
      };
      try {
        const caps = await detectEncoderCapabilities();
        expect(caps.video).toEqual(["h264"]);
        expect(caps.recommended).toBe("h264");
      } finally {
        delete (globalThis as any).VideoEncoder;
      }
    });
  });
});

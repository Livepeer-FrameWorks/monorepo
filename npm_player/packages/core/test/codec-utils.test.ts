import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";

import {
  translateCodec,
  isCodecSupported,
  getBestSupportedTrack,
  type TrackInfo,
} from "../src/core/CodecUtils";

describe("CodecUtils", () => {
  // =========================================================================
  // translateCodec — audio codecs
  // =========================================================================
  describe("translateCodec — audio", () => {
    it.each([
      ["AAC", "mp4a.40.2"],
      ["MP4A", "mp4a.40.2"],
      ["MP3", "mp4a.40.34"],
      ["AC3", "ac-3"],
      ["AC-3", "ac-3"],
      ["EAC3", "ec-3"],
      ["EC3", "ec-3"],
      ["E-AC3", "ec-3"],
      ["EC-3", "ec-3"],
      ["OPUS", "opus"],
      ["VORBIS", "vorbis"],
      ["FLAC", "flac"],
      ["PCM", "pcm"],
      ["PCMS16LE", "pcm"],
    ])("%s → %s", (codec, expected) => {
      expect(translateCodec({ codec, type: "audio" })).toBe(expected);
    });

    it("unknown audio codec returns lowercase", () => {
      expect(translateCodec({ codec: "WEIRDCODEC", type: "audio" })).toBe("weirdcodec");
    });

    it("case insensitive (codec is uppercased internally)", () => {
      expect(translateCodec({ codec: "aac", type: "audio" })).toBe("mp4a.40.2");
      expect(translateCodec({ codec: "Opus", type: "audio" })).toBe("opus");
    });
  });

  // =========================================================================
  // translateCodec — video codecs
  // =========================================================================
  describe("translateCodec — video", () => {
    it.each([
      ["H264", "avc1.42E01E"],
      ["AVC", "avc1.42E01E"],
      ["AVC1", "avc1.42E01E"],
      ["VP8", "vp8"],
      ["VP9", "vp09.00.10.08"],
      ["AV1", "av01.0.01M.08"],
      ["THEORA", "theora"],
    ])("%s → %s (no init data)", (codec, expected) => {
      expect(translateCodec({ codec, type: "video" })).toBe(expected);
    });

    it("H265/HEVC/HEV1/HVC1 returns default HEVC profile without init data", () => {
      for (const codec of ["H265", "HEVC", "HEV1", "HVC1"]) {
        expect(translateCodec({ codec, type: "video" })).toBe("hev1.1.6.L93.B0");
      }
    });

    it("unknown video codec returns lowercase", () => {
      expect(translateCodec({ codec: "MPEG2", type: "video" })).toBe("mpeg2");
    });

    it("prefers codecstring when available", () => {
      expect(translateCodec({ codec: "H264", type: "video", codecstring: "avc1.64001f" })).toBe(
        "avc1.64001f"
      );
    });
  });

  // =========================================================================
  // translateCodec — H264 init data parsing
  // =========================================================================
  describe("translateCodec — H264 with init data", () => {
    it("extracts profile from raw format init data", () => {
      // Raw bytes: profileIdc=0x64 (100=High), constraintFlags=0x00, levelIdc=0x1F (31=3.1)
      const init = btoa(String.fromCharCode(0x64, 0x00, 0x1f, 0x00));
      const result = translateCodec({ codec: "H264", type: "video", init });
      expect(result).toBe("avc1.64001F");
    });

    it("extracts profile from NAL start code format", () => {
      // NAL start code (0x00 0x00 0x01) + SPS type (0x67) + profile(0x42) + constraint(0xE0) + level(0x1E)
      const bytes = new Uint8Array([0x00, 0x00, 0x01, 0x67, 0x42, 0xe0, 0x1e]);
      const init = btoa(String.fromCharCode(...bytes));
      const result = translateCodec({ codec: "H264", type: "video", init });
      expect(result).toBe("avc1.42E01E");
    });

    it("returns default when init data is invalid", () => {
      const result = translateCodec({ codec: "H264", type: "video", init: "!!invalid!!" });
      expect(result).toBe("avc1.42E01E");
    });

    it("returns default when init data is empty", () => {
      const result = translateCodec({ codec: "H264", type: "video", init: "" });
      expect(result).toBe("avc1.42E01E");
    });
  });

  // =========================================================================
  // translateCodec — HEVC init data parsing
  // =========================================================================
  describe("translateCodec — HEVC with init data", () => {
    it("extracts profile from valid init data", () => {
      // Profile IDC 1 (Main), next byte used as levelIdc
      const bytes = new Uint8Array([1, 60, 0, 0]);
      const init = btoa(String.fromCharCode(...bytes));
      const result = translateCodec({ codec: "H265", type: "video", init });
      expect(result).toBe("hev1.1.6.L60.B0");
    });

    it("returns default for empty init data", () => {
      const result = translateCodec({ codec: "H265", type: "video", init: "" });
      expect(result).toBe("hev1.1.6.L93.B0");
    });

    it("returns default for invalid init data", () => {
      const result = translateCodec({ codec: "H265", type: "video", init: "!!!" });
      expect(result).toBe("hev1.1.6.L93.B0");
    });
  });

  // =========================================================================
  // translateCodec — non-video/audio type
  // =========================================================================
  describe("translateCodec — other types", () => {
    it("non-video/audio type returns codec lowercase", () => {
      expect(translateCodec({ codec: "SRT", type: "subtitle" })).toBe("srt");
    });
  });

  // =========================================================================
  // isCodecSupported
  // =========================================================================
  describe("isCodecSupported", () => {
    it("returns false when MediaSource is undefined", () => {
      expect(isCodecSupported("avc1.42E01E")).toBe(false);
    });

    it("returns true when MediaSource.isTypeSupported returns true", () => {
      const origMS = globalThis.MediaSource;
      (globalThis as any).MediaSource = {
        isTypeSupported: vi.fn().mockReturnValue(true),
      };

      expect(isCodecSupported("avc1.42E01E")).toBe(true);
      expect((globalThis as any).MediaSource.isTypeSupported).toHaveBeenCalledWith(
        'video/mp4; codecs="avc1.42E01E"'
      );

      (globalThis as any).MediaSource = origMS;
    });

    it("returns false when MediaSource.isTypeSupported returns false", () => {
      const origMS = globalThis.MediaSource;
      (globalThis as any).MediaSource = {
        isTypeSupported: vi.fn().mockReturnValue(false),
      };

      expect(isCodecSupported("theora")).toBe(false);

      (globalThis as any).MediaSource = origMS;
    });

    it("uses custom container type", () => {
      const origMS = globalThis.MediaSource;
      (globalThis as any).MediaSource = {
        isTypeSupported: vi.fn().mockReturnValue(true),
      };

      isCodecSupported("vp8", "video/webm");
      expect((globalThis as any).MediaSource.isTypeSupported).toHaveBeenCalledWith(
        'video/webm; codecs="vp8"'
      );

      (globalThis as any).MediaSource = origMS;
    });
  });

  // =========================================================================
  // getBestSupportedTrack
  // =========================================================================
  describe("getBestSupportedTrack", () => {
    it("returns null when no MediaSource", () => {
      const tracks: TrackInfo[] = [
        { type: "video", codec: "H264" },
        { type: "audio", codec: "AAC" },
      ];
      expect(getBestSupportedTrack(tracks, "video")).toBeNull();
    });

    it("returns first supported track", () => {
      const origMS = globalThis.MediaSource;
      (globalThis as any).MediaSource = {
        isTypeSupported: vi.fn().mockImplementation((mime: string) => {
          return mime.includes("avc1");
        }),
      };

      const tracks: TrackInfo[] = [
        { type: "video", codec: "VP9" },
        { type: "video", codec: "H264" },
        { type: "audio", codec: "AAC" },
      ];

      const result = getBestSupportedTrack(tracks, "video");
      expect(result?.codec).toBe("H264");

      (globalThis as any).MediaSource = origMS;
    });

    it("filters by type", () => {
      const origMS = globalThis.MediaSource;
      (globalThis as any).MediaSource = {
        isTypeSupported: vi.fn().mockReturnValue(true),
      };

      const tracks: TrackInfo[] = [
        { type: "video", codec: "H264" },
        { type: "audio", codec: "AAC" },
      ];

      const result = getBestSupportedTrack(tracks, "audio");
      expect(result?.codec).toBe("AAC");

      (globalThis as any).MediaSource = origMS;
    });

    it("returns null when no tracks of requested type", () => {
      const origMS = globalThis.MediaSource;
      (globalThis as any).MediaSource = {
        isTypeSupported: vi.fn().mockReturnValue(true),
      };

      const tracks: TrackInfo[] = [{ type: "video", codec: "H264" }];
      expect(getBestSupportedTrack(tracks, "audio")).toBeNull();

      (globalThis as any).MediaSource = origMS;
    });

    it("returns null when empty tracks array", () => {
      expect(getBestSupportedTrack([], "video")).toBeNull();
    });
  });
});

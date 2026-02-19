import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";

import {
  translateCodec,
  isCodecSupported,
  getBestSupportedTrack,
  buildDescription,
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

  // =========================================================================
  // buildDescription
  // =========================================================================
  describe("buildDescription", () => {
    describe("H264", () => {
      it("returns null for empty data", () => {
        expect(buildDescription("H264", new Uint8Array(0))).toBeNull();
      });

      it("passes through valid AVCC (starts with 0x01)", () => {
        // Minimal AVCC: version=1, profile, compat, level, flags, 0 SPS, 0 PPS
        const avcc = new Uint8Array([0x01, 0x64, 0x00, 0x1f, 0xff, 0xe0, 0x00]);
        const result = buildDescription("H264", avcc);
        expect(result).toBe(avcc);
      });

      it("converts Annex B with 4-byte start codes to AVCC", () => {
        // SPS NAL type 7: 0x67 = 0b01100111 → nalType = 7
        const sps = new Uint8Array([0x67, 0x64, 0x00, 0x1f, 0xac, 0xd9]);
        // PPS NAL type 8: 0x68
        const pps = new Uint8Array([0x68, 0xce, 0x38, 0x80]);

        // Build Annex B: start_code + SPS + start_code + PPS
        const annexB = new Uint8Array(4 + sps.length + 4 + pps.length);
        annexB.set([0x00, 0x00, 0x00, 0x01], 0);
        annexB.set(sps, 4);
        annexB.set([0x00, 0x00, 0x00, 0x01], 4 + sps.length);
        annexB.set(pps, 4 + sps.length + 4);

        const result = buildDescription("H264", annexB);
        expect(result).not.toBeNull();
        // Verify AVCC structure
        expect(result![0]).toBe(0x01); // configurationVersion
        expect(result![1]).toBe(0x64); // profile_idc (High)
        expect(result![2]).toBe(0x00); // constraint_set_flags
        expect(result![3]).toBe(0x1f); // level_idc
        expect(result![4]).toBe(0xff); // reserved + lengthSizeMinusOne
        expect(result![5] & 0x1f).toBe(1); // numSPS = 1
      });

      it("converts Annex B with 3-byte start codes to AVCC", () => {
        const sps = new Uint8Array([0x67, 0x42, 0xe0, 0x1e]);
        const pps = new Uint8Array([0x68, 0xce]);

        const annexB = new Uint8Array(3 + sps.length + 3 + pps.length);
        annexB.set([0x00, 0x00, 0x01], 0);
        annexB.set(sps, 3);
        annexB.set([0x00, 0x00, 0x01], 3 + sps.length);
        annexB.set(pps, 3 + sps.length + 3);

        const result = buildDescription("H264", annexB);
        expect(result).not.toBeNull();
        expect(result![0]).toBe(0x01);
        expect(result![1]).toBe(0x42); // Baseline profile
      });

      it("returns null for Annex B with no SPS", () => {
        // PPS only (NAL type 8)
        const ppsOnly = new Uint8Array([0x00, 0x00, 0x00, 0x01, 0x68, 0xce, 0x38]);
        const result = buildDescription("H264", ppsOnly);
        expect(result).toBeNull();
      });

      it("works with AVC and AVC1 aliases", () => {
        const avcc = new Uint8Array([0x01, 0x64, 0x00, 0x1f, 0xff, 0xe0, 0x00]);
        expect(buildDescription("AVC", avcc)).toBe(avcc);
        expect(buildDescription("AVC1", avcc)).toBe(avcc);
      });
    });

    describe("HEVC", () => {
      it("returns null for empty data", () => {
        expect(buildDescription("HEVC", new Uint8Array(0))).toBeNull();
      });

      it("passes through valid HVCC (starts with 0x01, >= 23 bytes)", () => {
        const hvcc = new Uint8Array(23);
        hvcc[0] = 0x01;
        const result = buildDescription("HEVC", hvcc);
        expect(result).toBe(hvcc);
      });

      it("converts Annex B with VPS+SPS+PPS to HVCC", () => {
        // VPS: NAL type 32 → (32 << 1) | 0 = 0x40, second byte = 0x01
        const vps = new Uint8Array([0x40, 0x01, 0x0c, 0x01, 0xff, 0xff]);
        // SPS: NAL type 33 → (33 << 1) | 0 = 0x42, second byte = 0x01
        const sps = new Uint8Array([
          0x42, 0x01, 0x01, 0x01, 0x60, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x5d,
        ]);
        // PPS: NAL type 34 → (34 << 1) | 0 = 0x44, second byte = 0x01
        const pps = new Uint8Array([0x44, 0x01, 0xc1, 0x72]);

        const annexB = new Uint8Array(4 + vps.length + 4 + sps.length + 4 + pps.length);
        let off = 0;
        annexB.set([0x00, 0x00, 0x00, 0x01], off);
        off += 4;
        annexB.set(vps, off);
        off += vps.length;
        annexB.set([0x00, 0x00, 0x00, 0x01], off);
        off += 4;
        annexB.set(sps, off);
        off += sps.length;
        annexB.set([0x00, 0x00, 0x00, 0x01], off);
        off += 4;
        annexB.set(pps, off);

        const result = buildDescription("HEVC", annexB);
        expect(result).not.toBeNull();
        expect(result![0]).toBe(0x01); // configurationVersion
        // Last byte should be numOfArrays = 3 (VPS, SPS, PPS)
        expect(result![22]).toBe(3);
      });

      it("returns null for Annex B with no SPS", () => {
        // VPS only
        const vpsOnly = new Uint8Array([0x00, 0x00, 0x00, 0x01, 0x40, 0x01, 0x0c]);
        const result = buildDescription("HEVC", vpsOnly);
        expect(result).toBeNull();
      });

      it("works with H265/HEV1/HVC1 aliases", () => {
        const hvcc = new Uint8Array(23);
        hvcc[0] = 0x01;
        expect(buildDescription("H265", hvcc)).toBe(hvcc);
        expect(buildDescription("HEV1", hvcc)).toBe(hvcc);
        expect(buildDescription("HVC1", hvcc)).toBe(hvcc);
      });
    });

    describe("Vorbis", () => {
      it("returns null for empty data", () => {
        expect(buildDescription("VORBIS", new Uint8Array(0))).toBeNull();
      });

      it("validates correct Xiph extradata format", () => {
        // Build valid Xiph extradata:
        // byte[0] = 2 (3 headers)
        // lacing: header1Size=7, header2Size=5
        // header1: 0x01 + "vorbis" (identification)
        // header2: 0x03 + "vorb" + 0x00 (comment, simplified)
        // header3: 0x05 + "vorbi" (setup, simplified)
        const header1 = new Uint8Array([0x01, 0x76, 0x6f, 0x72, 0x62, 0x69, 0x73]); // \x01vorbis
        const header2 = new Uint8Array([0x03, 0x76, 0x6f, 0x72, 0x62]); // comment
        const header3 = new Uint8Array([0x05, 0x76, 0x6f, 0x72, 0x62, 0x69]); // setup

        const xiph = new Uint8Array(1 + 1 + 1 + header1.length + header2.length + header3.length);
        xiph[0] = 2; // num_headers - 1
        xiph[1] = header1.length; // lacing value for header 1
        xiph[2] = header2.length; // lacing value for header 2
        xiph.set(header1, 3);
        xiph.set(header2, 3 + header1.length);
        xiph.set(header3, 3 + header1.length + header2.length);

        const result = buildDescription("VORBIS", xiph);
        expect(result).toBe(xiph);
      });

      it("returns null for invalid header count", () => {
        const bad = new Uint8Array([0x01, 0x07, 0x05, 0x01, 0x76, 0x6f, 0x72, 0x62, 0x69, 0x73]);
        expect(buildDescription("VORBIS", bad)).toBeNull();
      });

      it("returns null for missing vorbis magic", () => {
        const bad = new Uint8Array([0x02, 0x07, 0x05, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00]);
        expect(buildDescription("VORBIS", bad)).toBeNull();
      });
    });

    describe("AAC", () => {
      it("returns null for empty data", () => {
        expect(buildDescription("AAC", new Uint8Array(0))).toBeNull();
      });

      it("passes through valid AudioSpecificConfig (2 bytes)", () => {
        // AAC-LC, 44100Hz, stereo: 0x1210
        const asc = new Uint8Array([0x12, 0x10]);
        expect(buildDescription("AAC", asc)).toBe(asc);
      });

      it("returns null for single byte", () => {
        expect(buildDescription("AAC", new Uint8Array([0x12]))).toBeNull();
      });

      it("works with MP4A alias", () => {
        const asc = new Uint8Array([0x12, 0x10]);
        expect(buildDescription("MP4A", asc)).toBe(asc);
      });
    });

    describe("codecs without description", () => {
      it.each(["VP8", "MP3", "PCM", "PCMS16LE", "THEORA"])(
        "returns null for %s even with data",
        (codec) => {
          const data = new Uint8Array([0x01, 0x02, 0x03]);
          expect(buildDescription(codec, data)).toBeNull();
        }
      );
    });

    describe("optional description codecs", () => {
      it.each(["OPUS", "FLAC"])("passes through %s init data", (codec) => {
        const data = new Uint8Array([0x4f, 0x70, 0x75, 0x73]);
        expect(buildDescription(codec, data)).toBe(data);
      });

      it.each(["OPUS", "FLAC"])("returns null for %s with empty data", (codec) => {
        expect(buildDescription(codec, new Uint8Array(0))).toBeNull();
      });
    });
  });
});

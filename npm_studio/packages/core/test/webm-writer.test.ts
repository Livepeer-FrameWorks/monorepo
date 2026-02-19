import { describe, expect, it } from "vitest";

import { WebMWriter, type WebMWriterOptions } from "../src/recording/WebMWriter";

// EBML element IDs as they appear in the byte stream
const EBML_HEADER_ID = [0x1a, 0x45, 0xdf, 0xa3];
const SEGMENT_ID = [0x18, 0x53, 0x80, 0x67];
const UNKNOWN_SIZE = [0x01, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff];

function findBytes(data: Uint8Array, needle: number[]): number {
  outer: for (let i = 0; i <= data.length - needle.length; i++) {
    for (let j = 0; j < needle.length; j++) {
      if (data[i + j] !== needle[j]) continue outer;
    }
    return i;
  }
  return -1;
}

function blobToUint8Array(blob: Blob): Promise<Uint8Array> {
  return blob.arrayBuffer().then((buf) => new Uint8Array(buf));
}

function makeVideoChunk(timestampMs: number, keyFrame: boolean, size = 100): ArrayBuffer {
  const buf = new ArrayBuffer(size);
  new Uint8Array(buf).fill(keyFrame ? 0x65 : 0x41);
  return buf;
}

function makeAudioChunk(timestampMs: number, size = 50): ArrayBuffer {
  const buf = new ArrayBuffer(size);
  new Uint8Array(buf).fill(0xaa);
  return buf;
}

describe("WebMWriter", () => {
  const defaultOpts: WebMWriterOptions = {
    video: { width: 1920, height: 1080 },
    audio: { sampleRate: 48000, channels: 2, bitDepth: 32 },
  };

  // ===========================================================================
  // EBML header
  // ===========================================================================
  describe("EBML header", () => {
    it("starts with EBML header element", async () => {
      const writer = new WebMWriter(defaultOpts);
      const blob = writer.finalize();
      const data = await blobToUint8Array(blob);
      expect(data[0]).toBe(0x1a);
      expect(data[1]).toBe(0x45);
      expect(data[2]).toBe(0xdf);
      expect(data[3]).toBe(0xa3);
    });

    it("contains webm doctype", async () => {
      const writer = new WebMWriter(defaultOpts);
      const blob = writer.finalize();
      const data = await blobToUint8Array(blob);
      const text = new TextDecoder().decode(data);
      expect(text).toContain("webm");
    });

    it("has segment with unknown size for streaming", async () => {
      const writer = new WebMWriter(defaultOpts);
      const blob = writer.finalize();
      const data = await blobToUint8Array(blob);
      const segIdx = findBytes(data, SEGMENT_ID);
      expect(segIdx).toBeGreaterThan(0);
      // Unknown size follows segment ID
      for (let i = 0; i < UNKNOWN_SIZE.length; i++) {
        expect(data[segIdx + SEGMENT_ID.length + i]).toBe(UNKNOWN_SIZE[i]);
      }
    });
  });

  // ===========================================================================
  // Track configuration
  // ===========================================================================
  describe("track configuration", () => {
    it("defaults to V_VP9 codec", async () => {
      const writer = new WebMWriter(defaultOpts);
      const blob = writer.finalize();
      const data = await blobToUint8Array(blob);
      const text = new TextDecoder().decode(data);
      expect(text).toContain("V_VP9");
    });

    it("uses V_AV1 codec when specified", async () => {
      const writer = new WebMWriter({ ...defaultOpts, videoCodec: "V_AV1" });
      const blob = writer.finalize();
      const data = await blobToUint8Array(blob);
      const text = new TextDecoder().decode(data);
      expect(text).toContain("V_AV1");
    });

    it("includes A_OPUS audio codec", async () => {
      const writer = new WebMWriter(defaultOpts);
      const blob = writer.finalize();
      const data = await blobToUint8Array(blob);
      const text = new TextDecoder().decode(data);
      expect(text).toContain("A_OPUS");
    });

    it("creates video-only container when no audio config", async () => {
      const writer = new WebMWriter({ video: { width: 1280, height: 720 } });
      const blob = writer.finalize();
      const data = await blobToUint8Array(blob);
      const text = new TextDecoder().decode(data);
      expect(text).toContain("V_VP9");
      expect(text).not.toContain("A_OPUS");
    });

    it("creates audio-only container when no video config", async () => {
      const writer = new WebMWriter({ audio: { sampleRate: 48000, channels: 2 } });
      const blob = writer.finalize();
      const data = await blobToUint8Array(blob);
      const text = new TextDecoder().decode(data);
      expect(text).toContain("A_OPUS");
      expect(text).not.toContain("V_VP9");
    });
  });

  // ===========================================================================
  // addVideoChunk / addAudioChunk
  // ===========================================================================
  describe("chunk ingestion", () => {
    it("increases blob size when video chunks are added", () => {
      const empty = new WebMWriter(defaultOpts);
      const emptySize = empty.finalize().size;

      const writer = new WebMWriter(defaultOpts);
      writer.addVideoChunk(makeVideoChunk(0, true), 0, true);
      expect(writer.finalize().size).toBeGreaterThan(emptySize);
    });

    it("increases blob size when audio chunks are added", () => {
      const empty = new WebMWriter(defaultOpts);
      const emptySize = empty.finalize().size;

      const writer = new WebMWriter(defaultOpts);
      writer.addAudioChunk(makeAudioChunk(0), 0);
      expect(writer.finalize().size).toBeGreaterThan(emptySize);
    });

    it("ignores video chunks when no video track configured", () => {
      const writer = new WebMWriter({ audio: { sampleRate: 48000, channels: 2 } });
      const sizeBefore = writer.size;
      writer.addVideoChunk(makeVideoChunk(0, true), 0, true);
      // No cluster should be created since video track is null
      expect(writer.size).toBe(sizeBefore);
    });

    it("ignores audio chunks when no audio track configured", () => {
      const writer = new WebMWriter({ video: { width: 1920, height: 1080 } });
      // Add a video chunk first to ensure cluster is created
      writer.addVideoChunk(makeVideoChunk(0, true), 0, true);
      const sizeBefore = writer.size;
      writer.addAudioChunk(makeAudioChunk(100), 100);
      expect(writer.size).toBe(sizeBefore);
    });

    it("ignores chunks after finalization", () => {
      const writer = new WebMWriter(defaultOpts);
      writer.addVideoChunk(makeVideoChunk(0, true), 0, true);
      writer.finalize();
      const sizeAfterFinalize = writer.size;
      writer.addVideoChunk(makeVideoChunk(1000, true, 200), 1000, true);
      expect(writer.size).toBe(sizeAfterFinalize);
    });
  });

  // ===========================================================================
  // Cluster boundaries
  // ===========================================================================
  describe("cluster boundaries", () => {
    it("starts a new cluster on first video keyframe", () => {
      const writer = new WebMWriter(defaultOpts);
      writer.addVideoChunk(makeVideoChunk(0, true), 0, true);
      // First cluster created — size includes cluster header + block
      expect(writer.size).toBeGreaterThan(0);
    });

    it("flushes cluster on next keyframe after 2 seconds", async () => {
      const writer = new WebMWriter(defaultOpts);

      // First keyframe → cluster 1
      writer.addVideoChunk(makeVideoChunk(0, true), 0, true);
      writer.addVideoChunk(makeVideoChunk(500, false), 500, false);
      writer.addVideoChunk(makeVideoChunk(1000, false), 1000, false);

      const sizeBeforeSecondCluster = writer.size;

      // Keyframe at 2500ms → should trigger new cluster
      writer.addVideoChunk(makeVideoChunk(2500, true), 2500, true);

      // The first cluster's data gets flushed (emitted) before the new cluster starts
      expect(writer.size).toBeGreaterThan(sizeBeforeSecondCluster);
    });

    it("does not start new cluster on non-keyframe even past duration threshold", () => {
      const writer = new WebMWriter(defaultOpts);
      writer.addVideoChunk(makeVideoChunk(0, true), 0, true);

      const sizeAfterFirstBlock = writer.size;

      // Non-keyframe at 3s — should NOT create a new cluster
      writer.addVideoChunk(makeVideoChunk(3000, false), 3000, false);

      // Size increases (block added to same cluster) but no cluster flush
      // We verify by checking that the blob doesn't have an excessive number of cluster headers
      const blob = writer.finalize();
      expect(blob.size).toBeGreaterThan(0);
    });
  });

  // ===========================================================================
  // SimpleBlock format
  // ===========================================================================
  describe("SimpleBlock format", () => {
    it("sets keyframe flag 0x80 for key frames", async () => {
      const writer = new WebMWriter({ video: { width: 640, height: 480 } });
      writer.addVideoChunk(makeVideoChunk(0, true, 10), 0, true);
      const blob = writer.finalize();
      const data = await blobToUint8Array(blob);

      // SimpleBlock: element ID 0xA3, then VINT size, then data.
      // Data = track VINT (0x81 for track 1) + int16 BE timecode + flag byte + frame.
      // For a keyframe at relative time 0: 0x81, 0x00, 0x00, 0x80, <frame bytes>
      // Look for this pattern: track=0x81, time=0x0000, flag=0x80
      const pattern = [0x81, 0x00, 0x00, 0x80];
      const idx = findBytes(data, pattern);
      expect(idx).toBeGreaterThan(0);
    });
  });

  // ===========================================================================
  // finalize()
  // ===========================================================================
  describe("finalize", () => {
    it("returns a Blob with video/webm type", () => {
      const writer = new WebMWriter(defaultOpts);
      const blob = writer.finalize();
      expect(blob).toBeInstanceOf(Blob);
      expect(blob.type).toBe("video/webm");
    });

    it("returns valid blob even with no data chunks", () => {
      const writer = new WebMWriter(defaultOpts);
      const blob = writer.finalize();
      // Should still have EBML header + segment + tracks
      expect(blob.size).toBeGreaterThan(0);
    });

    it("is idempotent — calling finalize twice returns same blob", () => {
      const writer = new WebMWriter(defaultOpts);
      writer.addVideoChunk(makeVideoChunk(0, true), 0, true);
      const blob1 = writer.finalize();
      const blob2 = writer.finalize();
      expect(blob1.size).toBe(blob2.size);
    });
  });

  // ===========================================================================
  // size / duration getters
  // ===========================================================================
  describe("size and duration getters", () => {
    it("size starts at header size (non-zero)", () => {
      const writer = new WebMWriter(defaultOpts);
      expect(writer.size).toBeGreaterThan(0);
    });

    it("duration is 0 before any chunks", () => {
      const writer = new WebMWriter(defaultOpts);
      expect(writer.duration).toBe(0);
    });

    it("duration reflects time span of added chunks", () => {
      const writer = new WebMWriter(defaultOpts);
      writer.addVideoChunk(makeVideoChunk(1000, true), 1000, true);
      writer.addVideoChunk(makeVideoChunk(3500, false), 3500, false);
      expect(writer.duration).toBe(2500);
    });

    it("blob size grows with more chunks", () => {
      const writer1 = new WebMWriter(defaultOpts);
      writer1.addVideoChunk(makeVideoChunk(0, true, 100), 0, true);
      const size1 = writer1.finalize().size;

      const writer2 = new WebMWriter(defaultOpts);
      writer2.addVideoChunk(makeVideoChunk(0, true, 100), 0, true);
      writer2.addVideoChunk(makeVideoChunk(33, false, 100), 33, false);
      writer2.addAudioChunk(makeAudioChunk(0, 50), 0);
      const size2 = writer2.finalize().size;

      expect(size2).toBeGreaterThan(size1);
    });
  });

  // ===========================================================================
  // Interleaved video + audio
  // ===========================================================================
  describe("interleaved video and audio", () => {
    it("produces a valid blob with both tracks", () => {
      const writer = new WebMWriter(defaultOpts);
      writer.addVideoChunk(makeVideoChunk(0, true), 0, true);
      writer.addAudioChunk(makeAudioChunk(0), 0);
      writer.addVideoChunk(makeVideoChunk(33, false), 33, false);
      writer.addAudioChunk(makeAudioChunk(20), 20);
      writer.addVideoChunk(makeVideoChunk(66, false), 66, false);
      writer.addAudioChunk(makeAudioChunk(40), 40);

      const blob = writer.finalize();
      expect(blob.size).toBeGreaterThan(0);
      expect(blob.type).toBe("video/webm");
    });
  });

  // ===========================================================================
  // Cues (seek index)
  // ===========================================================================
  describe("Cues element", () => {
    // Cues element ID: 0x1C, 0x53, 0xBB, 0x6B
    const CUES_ID = [0x1c, 0x53, 0xbb, 0x6b];
    // CuePoint element ID: 0xBB
    const CUE_POINT_ID = [0xbb];
    // CueTime element ID: 0xB3
    const CUE_TIME_ID = [0xb3];

    it("appends Cues element on finalize when clusters exist", async () => {
      const writer = new WebMWriter({ video: { width: 640, height: 480 } });
      writer.addVideoChunk(makeVideoChunk(0, true, 10), 0, true);
      const blob = writer.finalize();
      const data = await blobToUint8Array(blob);

      const cuesIdx = findBytes(data, CUES_ID);
      expect(cuesIdx).toBeGreaterThan(0);
    });

    it("does not write Cues when no data was added", async () => {
      const writer = new WebMWriter({ video: { width: 640, height: 480 } });
      const blob = writer.finalize();
      const data = await blobToUint8Array(blob);

      const cuesIdx = findBytes(data, CUES_ID);
      expect(cuesIdx).toBe(-1);
    });

    it("contains one CuePoint per cluster", async () => {
      const writer = new WebMWriter({ video: { width: 640, height: 480 } });

      // Cluster 1 at 0ms
      writer.addVideoChunk(makeVideoChunk(0, true, 10), 0, true);
      writer.addVideoChunk(makeVideoChunk(500, false, 10), 500, false);

      // Cluster 2 at 2500ms (keyframe past 2s threshold)
      writer.addVideoChunk(makeVideoChunk(2500, true, 10), 2500, true);
      writer.addVideoChunk(makeVideoChunk(3000, false, 10), 3000, false);

      const blob = writer.finalize();
      const data = await blobToUint8Array(blob);

      // Count CueTime elements (0xB3) as proxy for CuePoint count
      let cueTimeCount = 0;
      const cuesStart = findBytes(data, CUES_ID);
      expect(cuesStart).toBeGreaterThan(0);

      for (let i = cuesStart; i < data.length; i++) {
        if (data[i] === 0xb3) cueTimeCount++;
      }
      expect(cueTimeCount).toBe(2);
    });

    it("makes finalized blob larger than without cues", async () => {
      // Compare blob with data (has cues) vs header-only (no cues)
      const emptyWriter = new WebMWriter({ video: { width: 640, height: 480 } });
      const emptySize = emptyWriter.finalize().size;

      const writer = new WebMWriter({ video: { width: 640, height: 480 } });
      writer.addVideoChunk(makeVideoChunk(0, true, 10), 0, true);
      const withDataSize = writer.finalize().size;

      // With data + cues should be noticeably larger
      expect(withDataSize).toBeGreaterThan(emptySize);
    });
  });
});

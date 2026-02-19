/**
 * WebM Writer
 * Minimal clean-room EBML/Matroska muxer for WebM containers.
 * Produces valid WebM files from encoded VP9/AV1 video + Opus audio chunks.
 * H.264 is NOT valid in WebM containers — use VP9 or AV1 for recording.
 *
 * Structure:
 *   EBML Header → Segment (Info + Tracks + Cluster[])
 *   Clusters are flushed every ~2 seconds for seekability.
 *   SimpleBlock format (flag 0x80) for frames within clusters.
 */

// ============================================================================
// EBML primitives
// ============================================================================

function ebmlEncodeId(id: number): Uint8Array {
  if (id <= 0x7f) return new Uint8Array([id]);
  if (id <= 0x3fff) return new Uint8Array([(id >> 8) & 0xff, id & 0xff]);
  if (id <= 0x1fffff) return new Uint8Array([(id >> 16) & 0xff, (id >> 8) & 0xff, id & 0xff]);
  return new Uint8Array([(id >> 24) & 0xff, (id >> 16) & 0xff, (id >> 8) & 0xff, id & 0xff]);
}

function ebmlEncodeSize(size: number): Uint8Array {
  if (size < 0x7f) return new Uint8Array([size | 0x80]);
  if (size < 0x3fff) return new Uint8Array([((size >> 8) & 0x3f) | 0x40, size & 0xff]);
  if (size < 0x1fffff) {
    return new Uint8Array([((size >> 16) & 0x1f) | 0x20, (size >> 8) & 0xff, size & 0xff]);
  }
  if (size < 0x0fffffff) {
    return new Uint8Array([
      ((size >> 24) & 0x0f) | 0x10,
      (size >> 16) & 0xff,
      (size >> 8) & 0xff,
      size & 0xff,
    ]);
  }
  // 8-byte size for large segments
  return new Uint8Array([
    0x01,
    (size / 0x100000000) & 0xff,
    (size >> 24) & 0xff,
    (size >> 16) & 0xff,
    (size >> 8) & 0xff,
    size & 0xff,
    0,
    0,
  ]);
}

// Unknown/streaming size (VINT with all data bits set)
const EBML_UNKNOWN_SIZE = new Uint8Array([0x01, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff]);

function ebmlElement(id: number, data: Uint8Array): Uint8Array {
  const idBytes = ebmlEncodeId(id);
  const sizeBytes = ebmlEncodeSize(data.length);
  const result = new Uint8Array(idBytes.length + sizeBytes.length + data.length);
  result.set(idBytes, 0);
  result.set(sizeBytes, idBytes.length);
  result.set(data, idBytes.length + sizeBytes.length);
  return result;
}

function ebmlUint(value: number): Uint8Array {
  if (value <= 0xff) return new Uint8Array([value]);
  if (value <= 0xffff) return new Uint8Array([(value >> 8) & 0xff, value & 0xff]);
  if (value <= 0xffffff) {
    return new Uint8Array([(value >> 16) & 0xff, (value >> 8) & 0xff, value & 0xff]);
  }
  return new Uint8Array([
    (value >> 24) & 0xff,
    (value >> 16) & 0xff,
    (value >> 8) & 0xff,
    value & 0xff,
  ]);
}

function ebmlFloat64(value: number): Uint8Array {
  const buf = new ArrayBuffer(8);
  new DataView(buf).setFloat64(0, value, false);
  return new Uint8Array(buf);
}

function ebmlString(value: string): Uint8Array {
  return new TextEncoder().encode(value);
}

function concat(...arrays: Uint8Array[]): Uint8Array {
  let totalLen = 0;
  for (const arr of arrays) totalLen += arr.length;
  const result = new Uint8Array(totalLen);
  let offset = 0;
  for (const arr of arrays) {
    result.set(arr, offset);
    offset += arr.length;
  }
  return result;
}

// ============================================================================
// EBML Element IDs (Matroska/WebM subset)
// ============================================================================

const EBML_ID = 0x1a45dfa3;
const EBML_VERSION = 0x4286;
const EBML_READ_VERSION = 0x42f7;
const EBML_MAX_ID_LENGTH = 0x42f2;
const EBML_MAX_SIZE_LENGTH = 0x42f3;
const DOC_TYPE = 0x4282;
const DOC_TYPE_VERSION = 0x4287;
const DOC_TYPE_READ_VERSION = 0x4285;

const SEGMENT = 0x18538067;
const SEGMENT_INFO = 0x1549a966;
const TIMECODE_SCALE = 0x2ad7b1;
const MUXING_APP = 0x4d80;
const WRITING_APP = 0x5741;
const _DURATION = 0x4489;

const TRACKS = 0x1654ae6b;
const TRACK_ENTRY = 0xae;
const TRACK_NUMBER = 0xd7;
const TRACK_UID = 0x73c5;
const TRACK_TYPE = 0x83;
const CODEC_ID = 0x86;
const FLAG_LACING = 0x9c;

const VIDEO = 0xe0;
const PIXEL_WIDTH = 0xb0;
const PIXEL_HEIGHT = 0xba;

const AUDIO = 0xe1;
const SAMPLING_FREQ = 0xb5;
const CHANNELS = 0x9f;
const BIT_DEPTH = 0x6264;

const CLUSTER = 0x1f43b675;
const CLUSTER_TIMECODE = 0xe7;
const SIMPLE_BLOCK = 0xa3;

// Cues (seek index)
const CUES = 0x1c53bb6b;
const CUE_POINT = 0xbb;
const CUE_TIME = 0xb3;
const CUE_TRACK_POSITIONS = 0xb7;
const CUE_TRACK = 0xf7;
const CUE_CLUSTER_POSITION = 0xf1;

// ============================================================================
// Types
// ============================================================================

export interface WebMTrackConfig {
  width?: number;
  height?: number;
  sampleRate?: number;
  channels?: number;
  bitDepth?: number;
}

export interface WebMWriterOptions {
  video?: WebMTrackConfig;
  audio?: WebMTrackConfig;
  videoCodec?: "V_VP9" | "V_AV1";
}

interface PendingBlock {
  trackNumber: number;
  data: ArrayBuffer;
  timestampMs: number;
  keyFrame: boolean;
}

// ============================================================================
// WebMWriter
// ============================================================================

export class WebMWriter {
  private parts: Uint8Array[] = [];
  private bytesWritten = 0;
  private videoTrackNumber: number | null = null;
  private audioTrackNumber: number | null = null;
  private clusterStartMs = -1;
  private clusterBlocks: Uint8Array[] = [];
  private startTimestampMs: number | null = null;
  private lastTimestampMs = 0;
  private finalized = false;

  // Cues tracking: byte offset of each cluster relative to Segment data start
  private segmentDataOffset = 0;
  private clusterCues: Array<{ timestampMs: number; byteOffset: number }> = [];

  // Cluster every 2 seconds for seekability
  private readonly CLUSTER_DURATION_MS = 2000;

  constructor(private opts: WebMWriterOptions) {
    this.writeHeader();
  }

  /**
   * Total bytes written so far.
   */
  get size(): number {
    return this.bytesWritten;
  }

  /**
   * Duration in milliseconds from first to last frame.
   */
  get duration(): number {
    if (this.startTimestampMs === null) return 0;
    return this.lastTimestampMs - this.startTimestampMs;
  }

  // ==========================================================================
  // Header
  // ==========================================================================

  private writeHeader(): void {
    // EBML Header
    const ebmlHeader = ebmlElement(
      EBML_ID,
      concat(
        ebmlElement(EBML_VERSION, ebmlUint(1)),
        ebmlElement(EBML_READ_VERSION, ebmlUint(1)),
        ebmlElement(EBML_MAX_ID_LENGTH, ebmlUint(4)),
        ebmlElement(EBML_MAX_SIZE_LENGTH, ebmlUint(8)),
        ebmlElement(DOC_TYPE, ebmlString("webm")),
        ebmlElement(DOC_TYPE_VERSION, ebmlUint(4)),
        ebmlElement(DOC_TYPE_READ_VERSION, ebmlUint(2))
      )
    );
    this.emit(ebmlHeader);

    // Segment (unknown size — streaming compatible)
    const segId = ebmlEncodeId(SEGMENT);
    this.emit(concat(segId, EBML_UNKNOWN_SIZE));

    // All byte offsets in Cues are relative to this point
    this.segmentDataOffset = this.bytesWritten;

    // Segment Info
    const info = ebmlElement(
      SEGMENT_INFO,
      concat(
        ebmlElement(TIMECODE_SCALE, ebmlUint(1_000_000)), // 1ms timescale
        ebmlElement(MUXING_APP, ebmlString("FrameWorks StreamCrafter")),
        ebmlElement(WRITING_APP, ebmlString("FrameWorks StreamCrafter"))
      )
    );
    this.emit(info);

    // Tracks
    this.writeTracks();
  }

  private writeTracks(): void {
    const trackEntries: Uint8Array[] = [];
    let trackNum = 1;

    if (this.opts.video) {
      this.videoTrackNumber = trackNum;
      const codecId = this.opts.videoCodec === "V_AV1" ? "V_AV1" : "V_VP9";
      trackEntries.push(
        ebmlElement(
          TRACK_ENTRY,
          concat(
            ebmlElement(TRACK_NUMBER, ebmlUint(trackNum)),
            ebmlElement(TRACK_UID, ebmlUint(trackNum)),
            ebmlElement(TRACK_TYPE, ebmlUint(1)), // video
            ebmlElement(CODEC_ID, ebmlString(codecId)),
            ebmlElement(FLAG_LACING, ebmlUint(0)),
            ebmlElement(
              VIDEO,
              concat(
                ebmlElement(PIXEL_WIDTH, ebmlUint(this.opts.video.width ?? 1920)),
                ebmlElement(PIXEL_HEIGHT, ebmlUint(this.opts.video.height ?? 1080))
              )
            )
          )
        )
      );
      trackNum++;
    }

    if (this.opts.audio) {
      this.audioTrackNumber = trackNum;
      trackEntries.push(
        ebmlElement(
          TRACK_ENTRY,
          concat(
            ebmlElement(TRACK_NUMBER, ebmlUint(trackNum)),
            ebmlElement(TRACK_UID, ebmlUint(trackNum)),
            ebmlElement(TRACK_TYPE, ebmlUint(2)), // audio
            ebmlElement(CODEC_ID, ebmlString("A_OPUS")),
            ebmlElement(FLAG_LACING, ebmlUint(0)),
            ebmlElement(
              AUDIO,
              concat(
                ebmlElement(SAMPLING_FREQ, ebmlFloat64(this.opts.audio.sampleRate ?? 48000)),
                ebmlElement(CHANNELS, ebmlUint(this.opts.audio.channels ?? 2)),
                ebmlElement(BIT_DEPTH, ebmlUint(this.opts.audio.bitDepth ?? 32))
              )
            )
          )
        )
      );
    }

    this.emit(ebmlElement(TRACKS, concat(...trackEntries)));
  }

  // ==========================================================================
  // Public API
  // ==========================================================================

  addVideoChunk(data: ArrayBuffer, timestampMs: number, keyFrame: boolean): void {
    if (this.finalized || this.videoTrackNumber === null) return;
    this.addBlock({ trackNumber: this.videoTrackNumber, data, timestampMs, keyFrame });
  }

  addAudioChunk(data: ArrayBuffer, timestampMs: number): void {
    if (this.finalized || this.audioTrackNumber === null) return;
    this.addBlock({ trackNumber: this.audioTrackNumber, data, timestampMs, keyFrame: true });
  }

  /**
   * Finalize the WebM file and return as a Blob.
   * Flushes any remaining cluster data.
   */
  finalize(): Blob {
    if (!this.finalized) {
      this.flushCluster();
      this.writeCues();
      this.finalized = true;
    }
    return new Blob(this.parts as BlobPart[], { type: "video/webm" });
  }

  // ==========================================================================
  // Cluster management
  // ==========================================================================

  private addBlock(block: PendingBlock): void {
    // Normalize timestamps relative to recording start
    if (this.startTimestampMs === null) {
      this.startTimestampMs = block.timestampMs;
    }
    const relativeMs = block.timestampMs - this.startTimestampMs;
    this.lastTimestampMs = block.timestampMs;

    // Start new cluster on first block, video keyframe after duration threshold,
    // or when cluster has exceeded max duration
    const needNewCluster =
      this.clusterStartMs < 0 ||
      (block.keyFrame &&
        block.trackNumber === this.videoTrackNumber &&
        relativeMs - this.clusterStartMs >= this.CLUSTER_DURATION_MS);

    if (needNewCluster) {
      this.flushCluster();
      this.clusterStartMs = relativeMs;

      // Cluster header with timecode (unknown size for streaming)
      const clusterId = ebmlEncodeId(CLUSTER);
      this.clusterBlocks.push(concat(clusterId, EBML_UNKNOWN_SIZE));
      this.clusterBlocks.push(ebmlElement(CLUSTER_TIMECODE, ebmlUint(relativeMs)));
    }

    // SimpleBlock:
    //   Track number (VINT), relative timecode (int16 BE), flags, data
    const relTimecode = relativeMs - this.clusterStartMs;
    const clamped = Math.max(-32768, Math.min(32767, relTimecode));

    const trackVint = ebmlEncodeSize(block.trackNumber);
    // Override the VINT coding — SimpleBlock track number uses EBML VINT
    const flags = block.keyFrame ? 0x80 : 0x00; // 0x80 = keyframe flag in SimpleBlock

    const headerSize = trackVint.length + 2 + 1; // track + timecode + flags
    const simpleBlockData = new Uint8Array(headerSize + block.data.byteLength);
    simpleBlockData.set(trackVint, 0);
    const dv = new DataView(simpleBlockData.buffer, simpleBlockData.byteOffset);
    dv.setInt16(trackVint.length, clamped, false);
    simpleBlockData[trackVint.length + 2] = flags;
    simpleBlockData.set(new Uint8Array(block.data), headerSize);

    this.clusterBlocks.push(ebmlElement(SIMPLE_BLOCK, simpleBlockData));
  }

  private flushCluster(): void {
    if (this.clusterBlocks.length === 0) return;

    // Record this cluster's position for the Cues index
    if (this.clusterStartMs >= 0) {
      this.clusterCues.push({
        timestampMs: this.clusterStartMs,
        byteOffset: this.bytesWritten - this.segmentDataOffset,
      });
    }

    for (const block of this.clusterBlocks) {
      this.emit(block);
    }
    this.clusterBlocks = [];
  }

  private writeCues(): void {
    if (this.clusterCues.length === 0) return;

    const videoTrack = this.videoTrackNumber ?? 1;
    const cuePoints = this.clusterCues.map((cue) =>
      ebmlElement(
        CUE_POINT,
        concat(
          ebmlElement(CUE_TIME, ebmlUint(cue.timestampMs)),
          ebmlElement(
            CUE_TRACK_POSITIONS,
            concat(
              ebmlElement(CUE_TRACK, ebmlUint(videoTrack)),
              ebmlElement(CUE_CLUSTER_POSITION, ebmlUint(cue.byteOffset))
            )
          )
        )
      )
    );

    this.emit(ebmlElement(CUES, concat(...cuePoints)));
  }

  private emit(data: Uint8Array): void {
    this.parts.push(data);
    this.bytesWritten += data.length;
  }
}

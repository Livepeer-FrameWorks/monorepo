/**
 * CodecUtils - Codec string translation utilities
 *
 * Based on MistMetaPlayer's MistUtil.tracks.translateCodec functionality.
 * Translates MistServer codec names to browser-compatible codec strings.
 */

export interface TrackInfo {
  type: string; // 'video' | 'audio' | 'meta' - loosened for compatibility
  codec: string;
  init?: string;
  codecstring?: string;
  width?: number;
  height?: number;
  bps?: number;
  fpks?: number;
}

/**
 * Translate a MistServer codec name to a browser-compatible codec string
 *
 * @param track - Track info from MistServer
 * @returns Browser-compatible codec string (e.g., "avc1.64001f")
 *
 * @example
 * ```ts
 * translateCodec({ codec: 'H264', type: 'video' })
 * // => 'avc1.42E01E' (baseline profile, level 3.0 default)
 *
 * translateCodec({ codec: 'AAC', type: 'audio' })
 * // => 'mp4a.40.2'
 * ```
 */
export function translateCodec(track: TrackInfo): string {
  const codec = track.codec.toUpperCase();

  // Use codecstring if available (MistServer provides this for some tracks)
  if (track.codecstring) {
    return track.codecstring;
  }

  // Audio codecs
  if (track.type === "audio") {
    switch (codec) {
      case "AAC":
      case "MP4A":
        return "mp4a.40.2"; // AAC-LC
      case "MP3":
        return "mp4a.40.34"; // MP3 in MP4 container
      case "AC3":
      case "AC-3":
        return "ac-3";
      case "EAC3":
      case "EC3":
      case "E-AC3":
      case "EC-3":
        return "ec-3";
      case "OPUS":
        return "opus";
      case "VORBIS":
        return "vorbis";
      case "FLAC":
        return "flac";
      case "PCM":
      case "PCMS16LE":
        return "pcm";
      default:
        return codec.toLowerCase();
    }
  }

  // Video codecs
  if (track.type === "video") {
    switch (codec) {
      case "H264":
      case "AVC":
      case "AVC1": {
        // Try to extract profile/level from init data
        const profileLevel = extractH264Profile(track.init);
        return profileLevel || "avc1.42E01E"; // Default: Baseline Profile, Level 3.0
      }
      case "H265":
      case "HEVC":
      case "HEV1":
      case "HVC1": {
        // Try to extract profile/level from init data
        const profileLevel = extractHEVCProfile(track.init);
        return profileLevel || "hev1.1.6.L93.B0"; // Default: Main Profile, Level 3.1
      }
      case "VP8":
        return "vp8";
      case "VP9":
        return "vp09.00.10.08"; // Profile 0, Level 1.0, 8-bit
      case "AV1":
        return "av01.0.01M.08"; // Main Profile, Level 2.1, 8-bit
      case "THEORA":
        return "theora";
      default:
        return codec.toLowerCase();
    }
  }

  return codec.toLowerCase();
}

/**
 * Extract H264 profile and level from init data (SPS)
 * The init data contains the SPS NAL unit which has profile/level info
 *
 * @param init - Base64 encoded init data from MistServer
 * @returns Codec string like "avc1.64001f" or null
 */
function extractH264Profile(init?: string): string | null {
  if (!init) return null;

  try {
    // Decode base64 init data
    const bytes = base64ToBytes(init);

    // Look for SPS NAL unit (starts with 0x67 for H264)
    // Format: NAL type (1 byte) + profile_idc (1 byte) + constraint flags (1 byte) + level_idc (1 byte)
    for (let i = 0; i < bytes.length - 4; i++) {
      // Check for NAL start code (0x00 0x00 0x01 or 0x00 0x00 0x00 0x01)
      if (bytes[i] === 0x00 && bytes[i + 1] === 0x00) {
        let offset = i + 2;
        if (bytes[offset] === 0x00) offset++;
        if (bytes[offset] === 0x01) offset++;

        // Check if this is SPS (NAL type 7)
        const nalType = bytes[offset] & 0x1f;
        if (nalType === 7 && offset + 3 < bytes.length) {
          const profileIdc = bytes[offset + 1];
          const constraintFlags = bytes[offset + 2];
          const levelIdc = bytes[offset + 3];

          return `avc1.${toHex(profileIdc)}${toHex(constraintFlags)}${toHex(levelIdc)}`;
        }
      }
    }

    // If no NAL start code found, try raw format
    if (bytes.length >= 4) {
      // Assume first bytes are profile/constraint/level
      const profileIdc = bytes[0];
      const constraintFlags = bytes[1];
      const levelIdc = bytes[2];

      // Validate reasonable values
      if (profileIdc > 0 && profileIdc < 255 && levelIdc > 0 && levelIdc < 100) {
        return `avc1.${toHex(profileIdc)}${toHex(constraintFlags)}${toHex(levelIdc)}`;
      }
    }
  } catch {
    // Ignore parsing errors
  }

  return null;
}

/**
 * Extract HEVC profile and level from init data (VPS/SPS)
 *
 * @param init - Base64 encoded init data from MistServer
 * @returns Codec string like "hev1.1.6.L93.B0" or null
 */
function extractHEVCProfile(init?: string): string | null {
  if (!init) return null;

  try {
    // Decode base64 init data
    const bytes = base64ToBytes(init);

    // HEVC profile/level extraction is more complex
    // For now, return a sensible default based on data presence
    if (bytes.length > 0) {
      // Look for profile_tier_level in VPS/SPS
      // This is a simplified extraction - full parsing would be more complex
      for (let i = 0; i < bytes.length - 3; i++) {
        // Look for general_profile_idc (usually in first few bytes after NAL header)
        const profileIdc = bytes[i];
        if (profileIdc >= 1 && profileIdc <= 5) {
          // Valid profile IDC (1=Main, 2=Main10, 3=MainStill, 4=Range Extensions, 5=High Throughput)
          // tierFlag assumed to be 0 (main tier)
          const levelIdc = bytes[i + 1] || 93; // Default to level 3.1

          // Format: hev1.{profile}.{tier_flag}{compatibility}.L{level}.{constraints}
          return `hev1.${profileIdc}.6.L${levelIdc}.B0`;
        }
      }
    }
  } catch {
    // Ignore parsing errors
  }

  return null;
}

/**
 * Convert byte to 2-digit hex string
 */
function toHex(byte: number): string {
  return byte.toString(16).padStart(2, "0").toUpperCase();
}

/**
 * Decode base64 string to Uint8Array
 */
function base64ToBytes(base64: string): Uint8Array {
  const binaryString = atob(base64);
  const bytes = new Uint8Array(binaryString.length);
  for (let i = 0; i < binaryString.length; i++) {
    bytes[i] = binaryString.charCodeAt(i);
  }
  return bytes;
}

/**
 * Check if a codec is supported by the browser via MediaSource
 *
 * @param codecString - Codec string to check
 * @param containerType - Container type (default: 'video/mp4')
 * @returns true if supported
 */
export function isCodecSupported(codecString: string, containerType = "video/mp4"): boolean {
  if (typeof MediaSource === "undefined" || !MediaSource.isTypeSupported) {
    return false;
  }

  const mimeType = `${containerType}; codecs="${codecString}"`;
  return MediaSource.isTypeSupported(mimeType);
}

/**
 * Get the best supported codec from a list of tracks
 *
 * @param tracks - Array of track info
 * @param type - Track type to filter ('video' or 'audio')
 * @returns Best supported track or null
 */
export function getBestSupportedTrack(
  tracks: TrackInfo[],
  type: "video" | "audio"
): TrackInfo | null {
  const filteredTracks = tracks.filter((t) => t.type === type);

  for (const track of filteredTracks) {
    const codecString = translateCodec(track);
    if (isCodecSupported(codecString)) {
      return track;
    }
  }

  return null;
}

// ============================================================================
// Description Buffer Building
// ============================================================================

/**
 * Build a WebCodecs-compatible description buffer from raw init data.
 * Detects the format of the init data and converts if needed (e.g., Annex B → AVCC).
 * Returns null if the codec doesn't need a description or the data is invalid/missing.
 *
 * @param codec - MistServer codec name (e.g., "H264", "AAC", "vorbis")
 * @param initData - Raw init bytes (from str2bin or WebSocket INIT frame)
 * @returns Properly formatted description buffer, or null
 */
export function buildDescription(codec: string, initData: Uint8Array): Uint8Array | null {
  if (!initData || initData.length === 0) {
    return null;
  }

  switch (codec.toUpperCase()) {
    case "H264":
    case "AVC":
    case "AVC1":
      // AVCC starts with configurationVersion = 0x01
      if (initData[0] === 0x01 && initData.length >= 7) {
        return initData;
      }
      if (isAnnexBFormat(initData)) {
        return annexBToAvcc(initData);
      }
      // Best effort: return as-is
      return initData;

    case "H265":
    case "HEVC":
    case "HEV1":
    case "HVC1":
      // HVCC starts with configurationVersion = 0x01
      if (initData[0] === 0x01 && initData.length >= 23) {
        return initData;
      }
      if (isAnnexBFormat(initData)) {
        return annexBToHvcc(initData);
      }
      return initData;

    case "VORBIS":
      return validateVorbisDescription(initData);

    case "AAC":
    case "MP4A":
      // AudioSpecificConfig is at least 2 bytes
      if (initData.length >= 2) {
        return initData;
      }
      return null;

    case "OPUS":
    case "FLAC":
      // Optional descriptions — pass through if present
      return initData;

    case "VP8":
    case "MP3":
    case "PCM":
    case "PCMS16LE":
    case "THEORA":
      // No description needed
      return null;

    default:
      return initData;
  }
}

/**
 * Check if data is in Annex B format (starts with NAL start code)
 */
function isAnnexBFormat(data: Uint8Array): boolean {
  if (data.length < 4) return false;
  // 4-byte start code: 00 00 00 01
  if (data[0] === 0 && data[1] === 0 && data[2] === 0 && data[3] === 1) return true;
  // 3-byte start code: 00 00 01
  if (data[0] === 0 && data[1] === 0 && data[2] === 1) return true;
  return false;
}

/**
 * Split Annex B bitstream into individual NAL units (without start codes)
 */
function splitNalUnits(data: Uint8Array): Uint8Array[] {
  const units: Uint8Array[] = [];
  let i = 0;

  while (i < data.length) {
    // Look for start code
    if (i + 2 < data.length && data[i] === 0 && data[i + 1] === 0) {
      let scLen: number;
      if (i + 3 < data.length && data[i + 2] === 0 && data[i + 3] === 1) {
        scLen = 4;
      } else if (data[i + 2] === 1) {
        scLen = 3;
      } else {
        i++;
        continue;
      }

      const nalStart = i + scLen;

      // Find next start code or end of data
      let nalEnd = data.length;
      for (let j = nalStart + 1; j < data.length - 2; j++) {
        if (
          data[j] === 0 &&
          data[j + 1] === 0 &&
          (data[j + 2] === 1 || (j + 3 < data.length && data[j + 2] === 0 && data[j + 3] === 1))
        ) {
          nalEnd = j;
          break;
        }
      }

      if (nalEnd > nalStart) {
        units.push(data.subarray(nalStart, nalEnd));
      }
      i = nalEnd;
    } else {
      i++;
    }
  }

  return units;
}

/**
 * Convert Annex B H264 init data (SPS/PPS) to AVCDecoderConfigurationRecord
 * Per ISO/IEC 14496-15 §5.3.3.1.2
 */
function annexBToAvcc(data: Uint8Array): Uint8Array | null {
  const nalUnits = splitNalUnits(data);
  const sps: Uint8Array[] = [];
  const pps: Uint8Array[] = [];

  for (const nal of nalUnits) {
    if (nal.length === 0) continue;
    const nalType = nal[0] & 0x1f;
    if (nalType === 7) sps.push(nal);
    else if (nalType === 8) pps.push(nal);
  }

  if (sps.length === 0) return null;

  const firstSps = sps[0];
  if (firstSps.length < 4) return null;

  // Calculate total size
  let totalSize = 6; // header bytes
  for (const s of sps) totalSize += 2 + s.length;
  totalSize += 1; // PPS count byte
  for (const p of pps) totalSize += 2 + p.length;

  const record = new Uint8Array(totalSize);
  const view = new DataView(record.buffer);
  let offset = 0;

  record[offset++] = 0x01; // configurationVersion
  record[offset++] = firstSps[1]; // profile_idc
  record[offset++] = firstSps[2]; // constraint_set_flags
  record[offset++] = firstSps[3]; // level_idc
  record[offset++] = 0xff; // reserved(6) + lengthSizeMinusOne(2) = 3
  record[offset++] = 0xe0 | (sps.length & 0x1f); // reserved(3) + numSPS

  for (const s of sps) {
    view.setUint16(offset, s.length);
    offset += 2;
    record.set(s, offset);
    offset += s.length;
  }

  record[offset++] = pps.length & 0xff;

  for (const p of pps) {
    view.setUint16(offset, p.length);
    offset += 2;
    record.set(p, offset);
    offset += p.length;
  }

  return record;
}

/**
 * Convert Annex B HEVC init data (VPS/SPS/PPS) to HEVCDecoderConfigurationRecord
 * Per ISO/IEC 14496-15 §8.3.3.1.2
 */
function annexBToHvcc(data: Uint8Array): Uint8Array | null {
  const nalUnits = splitNalUnits(data);
  const vps: Uint8Array[] = [];
  const sps: Uint8Array[] = [];
  const pps: Uint8Array[] = [];

  for (const nal of nalUnits) {
    if (nal.length < 2) continue;
    const nalType = (nal[0] >> 1) & 0x3f;
    if (nalType === 32) vps.push(nal);
    else if (nalType === 33) sps.push(nal);
    else if (nalType === 34) pps.push(nal);
  }

  if (sps.length === 0) return null;

  // Extract profile_tier_level from SPS
  // HEVC SPS: 2 bytes NAL header, then sps_video_parameter_set_id (4 bits),
  // sps_max_sub_layers_minus1 (3 bits), sps_temporal_id_nesting_flag (1 bit),
  // then profile_tier_level structure (12 bytes of byte-aligned data)
  const firstSps = sps[0];
  let generalProfileIdc = 1; // Main profile default
  let generalTierFlag = 0;
  let generalProfileCompatibility = 0x60000000; // Main profile compat
  const generalConstraintBytes = new Uint8Array(6);
  let generalLevelIdc = 93; // Level 3.1 default

  if (firstSps.length >= 14) {
    // After 2-byte NAL header + 1-byte VPS ID/max_sub_layers/nesting
    const ptlOffset = 3;
    generalProfileIdc = firstSps[ptlOffset] & 0x1f;
    generalTierFlag = (firstSps[ptlOffset] >> 5) & 0x01;
    // 4 bytes profile compatibility flags
    generalProfileCompatibility =
      (firstSps[ptlOffset + 1] << 24) |
      (firstSps[ptlOffset + 2] << 16) |
      (firstSps[ptlOffset + 3] << 8) |
      firstSps[ptlOffset + 4];
    // 6 bytes constraint flags
    for (let ci = 0; ci < 6 && ptlOffset + 5 + ci < firstSps.length; ci++) {
      generalConstraintBytes[ci] = firstSps[ptlOffset + 5 + ci];
    }
    if (ptlOffset + 11 < firstSps.length) {
      generalLevelIdc = firstSps[ptlOffset + 11];
    }
  }

  // Count NAL arrays that have content
  const arrays: { type: number; nals: Uint8Array[] }[] = [];
  if (vps.length > 0) arrays.push({ type: 32, nals: vps });
  if (sps.length > 0) arrays.push({ type: 33, nals: sps });
  if (pps.length > 0) arrays.push({ type: 34, nals: pps });

  // Calculate total size: 23-byte header + NAL arrays
  let totalSize = 23;
  for (const arr of arrays) {
    totalSize += 3; // completeness(1) + count(2)
    for (const nal of arr.nals) {
      totalSize += 2 + nal.length; // length(2) + data
    }
  }

  const record = new Uint8Array(totalSize);
  const view = new DataView(record.buffer);
  let offset = 0;

  record[offset++] = 0x01; // configurationVersion
  record[offset++] = (generalTierFlag << 5) | (generalProfileIdc & 0x1f);
  view.setUint32(offset, generalProfileCompatibility);
  offset += 4;
  record.set(generalConstraintBytes, offset);
  offset += 6;
  record[offset++] = generalLevelIdc;
  // min_spatial_segmentation_idc with reserved bits
  view.setUint16(offset, 0xf000);
  offset += 2;
  record[offset++] = 0xfc; // parallelismType = 0 with reserved
  record[offset++] = 0xfc; // chromaFormatIdc = 0 with reserved
  record[offset++] = 0xf8; // bitDepthLumaMinus8 = 0 with reserved
  record[offset++] = 0xf8; // bitDepthChromaMinus8 = 0 with reserved
  view.setUint16(offset, 0); // avgFrameRate = 0
  offset += 2;
  // constantFrameRate(2)=0 | numTemporalLayers(3)=1 | temporalIdNested(1)=1 | lengthSizeMinusOne(2)=3
  record[offset++] = 0x0f;
  record[offset++] = arrays.length;

  for (const arr of arrays) {
    record[offset++] = 0x80 | (arr.type & 0x3f); // array_completeness=1 | NAL_unit_type
    view.setUint16(offset, arr.nals.length);
    offset += 2;
    for (const nal of arr.nals) {
      view.setUint16(offset, nal.length);
      offset += 2;
      record.set(nal, offset);
      offset += nal.length;
    }
  }

  return record;
}

/**
 * Validate Vorbis description (Xiph extradata format)
 * Format: byte[0] = 2 (num_headers-1), then lacing values, then 3 concatenated header packets
 * The first header must start with 0x01 followed by "vorbis" (7 bytes total)
 */
function validateVorbisDescription(data: Uint8Array): Uint8Array | null {
  if (data.length < 10) return null;

  // First byte must be 2 (three headers: identification, comment, setup)
  if (data[0] !== 2) return null;

  // Parse lacing values (Xiph-style: sum of consecutive 255 values + final non-255)
  let offset = 1;
  let header1Size = 0;
  while (offset < data.length && data[offset] === 255) {
    header1Size += 255;
    offset++;
  }
  if (offset >= data.length) return null;
  header1Size += data[offset++];

  let header2Size = 0;
  while (offset < data.length && data[offset] === 255) {
    header2Size += 255;
    offset++;
  }
  if (offset >= data.length) return null;
  header2Size += data[offset++];

  // Verify identification header starts with 0x01 + "vorbis"
  const headerStart = offset;
  if (headerStart + 7 > data.length) return null;
  if (data[headerStart] !== 0x01) return null;
  const magic = String.fromCharCode(
    data[headerStart + 1],
    data[headerStart + 2],
    data[headerStart + 3],
    data[headerStart + 4],
    data[headerStart + 5],
    data[headerStart + 6]
  );
  if (magic !== "vorbis") return null;

  // Verify total size is consistent
  const header3Size = data.length - offset - header1Size - header2Size;
  if (header3Size <= 0) return null;

  return data;
}

export default {
  translateCodec,
  isCodecSupported,
  getBestSupportedTrack,
  buildDescription,
};

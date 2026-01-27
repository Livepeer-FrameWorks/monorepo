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

export default {
  translateCodec,
  isCodecSupported,
  getBestSupportedTrack,
};

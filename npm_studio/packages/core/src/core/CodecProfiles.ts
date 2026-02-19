/**
 * Codec Profiles
 * Multi-codec encoding support with VP9, AV1, and H.264
 * Each codec has resolution-aware profile/level selection and
 * bitrate targets tuned for its compression efficiency.
 */

import type {
  VideoEncoderSettings,
  AudioEncoderSettings,
  EncoderConfig,
  EncoderOverrides,
} from "../types";

// ============================================================================
// Types
// ============================================================================

export type VideoCodecFamily = "h264" | "vp9" | "av1";

export interface CodecCapabilities {
  video: VideoCodecFamily[];
  audio: string[];
  recommended: VideoCodecFamily;
}

// ============================================================================
// H.264 Profile/Level Selection
// ============================================================================

/**
 * Select H.264 codec string based on resolution and framerate.
 * Uses High Profile with the minimum level that supports the given parameters.
 *
 * H.264 Level capabilities (High Profile):
 * - Level 3.1 (64001f): 1280x720 @ 30fps, 14 Mbps
 * - Level 4.0 (640028): 1920x1080 @ 30fps or 1280x720 @ 60fps, 20 Mbps
 * - Level 4.2 (64002a): 1920x1080 @ 60fps, 50 Mbps
 * - Level 5.0 (640032): 2560x1440 @ 30fps, 25 Mbps
 * - Level 5.1 (640033): 2560x1440 @ 60fps or 3840x2160 @ 30fps, 40 Mbps
 * - Level 5.2 (640034): 3840x2160 @ 60fps, 60 Mbps
 */
export function selectH264Codec(width: number, height: number, framerate: number): string {
  const pixels = width * height;

  if (pixels >= 3840 * 2160) {
    return framerate > 30 ? "avc1.640034" : "avc1.640033";
  }
  if (pixels >= 2560 * 1440) {
    return framerate > 30 ? "avc1.640033" : "avc1.640032";
  }
  if (pixels >= 1920 * 1080) {
    return framerate > 30 ? "avc1.640032" : "avc1.64002a";
  }
  if (pixels >= 1280 * 720) {
    return framerate > 30 ? "avc1.64002a" : "avc1.640028";
  }
  return "avc1.64001f";
}

// ============================================================================
// VP9 Profile/Level Selection
// ============================================================================

/**
 * Select VP9 codec string based on resolution and framerate.
 * Format: vp09.PP.LL.DD
 *   PP = Profile (00 = 4:2:0 8-bit)
 *   LL = Level (10=1.0, 20=2.0, 30=3.0, 31=3.1, 40=4.0, 41=4.1, 50=5.0, 51=5.1)
 *   DD = Bit depth (08 = 8-bit)
 *
 * VP9 Level capabilities:
 * - Level 3.0: 1280x720 @ 30fps
 * - Level 3.1: 1280x720 @ 60fps / 1920x1080 @ 30fps
 * - Level 4.0: 1920x1080 @ 30fps (higher bitrate headroom)
 * - Level 4.1: 1920x1080 @ 60fps / 2560x1440 @ 30fps
 * - Level 5.0: 2560x1440 @ 60fps / 3840x2160 @ 30fps
 * - Level 5.1: 3840x2160 @ 60fps
 */
export function selectVP9Codec(width: number, height: number, framerate: number): string {
  const pixels = width * height;

  if (pixels >= 3840 * 2160) {
    return framerate > 30 ? "vp09.00.51.08" : "vp09.00.50.08";
  }
  if (pixels >= 2560 * 1440) {
    return framerate > 30 ? "vp09.00.50.08" : "vp09.00.41.08";
  }
  if (pixels >= 1920 * 1080) {
    return framerate > 30 ? "vp09.00.41.08" : "vp09.00.40.08";
  }
  if (pixels >= 1280 * 720) {
    return framerate > 30 ? "vp09.00.31.08" : "vp09.00.30.08";
  }
  return "vp09.00.20.08";
}

// ============================================================================
// AV1 Profile/Level Selection
// ============================================================================

/**
 * Select AV1 codec string based on resolution and framerate.
 * Format: av01.P.LLT.DD
 *   P = Profile (0 = Main)
 *   LL = Level (04=2.0, 05=2.1, 08=3.0, 09=3.1, 12=4.0, 13=4.1, 16=5.0, 17=5.1)
 *   T = Tier (M = Main, H = High)
 *   DD = Bit depth (08 = 8-bit)
 *
 * AV1 Level capabilities:
 * - Level 3.0 (08M): 1280x720 @ 30fps
 * - Level 3.1 (09M): 1280x720 @ 60fps / 1920x1080 @ 30fps
 * - Level 4.0 (12M): 1920x1080 @ 60fps
 * - Level 4.1 (13M): 2560x1440 @ 30fps
 * - Level 5.0 (16M): 2560x1440 @ 60fps / 3840x2160 @ 30fps
 * - Level 5.1 (17M): 3840x2160 @ 60fps
 */
export function selectAV1Codec(width: number, height: number, framerate: number): string {
  const pixels = width * height;

  if (pixels >= 3840 * 2160) {
    return framerate > 30 ? "av01.0.17M.08" : "av01.0.16M.08";
  }
  if (pixels >= 2560 * 1440) {
    return framerate > 30 ? "av01.0.16M.08" : "av01.0.13M.08";
  }
  if (pixels >= 1920 * 1080) {
    return framerate > 30 ? "av01.0.12M.08" : "av01.0.09M.08";
  }
  if (pixels >= 1280 * 720) {
    return framerate > 30 ? "av01.0.09M.08" : "av01.0.08M.08";
  }
  return "av01.0.05M.08";
}

// ============================================================================
// Codec selection dispatcher
// ============================================================================

export function selectCodecString(
  family: VideoCodecFamily,
  width: number,
  height: number,
  framerate: number
): string {
  switch (family) {
    case "vp9":
      return selectVP9Codec(width, height, framerate);
    case "av1":
      return selectAV1Codec(width, height, framerate);
    case "h264":
    default:
      return selectH264Codec(width, height, framerate);
  }
}

// ============================================================================
// Per-codec bitrate tables
// ============================================================================

// VP9 achieves ~30% better compression than H.264 at equivalent quality.
// AV1 achieves ~50% better compression than H.264.

const BITRATE_TABLES: Record<VideoCodecFamily, Record<string, { video: number; audio: number }>> = {
  h264: {
    professional: { video: 8_000_000, audio: 192_000 },
    broadcast: { video: 4_500_000, audio: 128_000 },
    conference: { video: 2_500_000, audio: 96_000 },
    low: { video: 1_000_000, audio: 64_000 },
  },
  vp9: {
    professional: { video: 5_500_000, audio: 192_000 },
    broadcast: { video: 3_000_000, audio: 128_000 },
    conference: { video: 1_800_000, audio: 96_000 },
    low: { video: 700_000, audio: 64_000 },
  },
  av1: {
    professional: { video: 4_000_000, audio: 192_000 },
    broadcast: { video: 2_200_000, audio: 128_000 },
    conference: { video: 1_300_000, audio: 96_000 },
    low: { video: 500_000, audio: 64_000 },
  },
};

// Resolution presets per quality profile
const RESOLUTION_PRESETS: Record<string, { width: number; height: number; framerate: number }> = {
  professional: { width: 1920, height: 1080, framerate: 30 },
  broadcast: { width: 1920, height: 1080, framerate: 30 },
  conference: { width: 1280, height: 720, framerate: 30 },
  low: { width: 640, height: 480, framerate: 24 },
};

// Keyframe interval varies by codec (in frames, not seconds)
const KEYFRAME_INTERVALS: Record<VideoCodecFamily, number> = {
  h264: 60, // ~2s at 30fps
  vp9: 120, // ~4s at 30fps (VP9 keyframes are more expensive)
  av1: 120, // ~4s at 30fps (AV1 keyframes are also expensive)
};

export function getKeyframeInterval(family: VideoCodecFamily): number {
  return KEYFRAME_INTERVALS[family] ?? 60;
}

// ============================================================================
// Default settings per codec family
// ============================================================================

export function getDefaultVideoSettings(
  profile: string,
  family: VideoCodecFamily
): VideoEncoderSettings {
  const resolution = RESOLUTION_PRESETS[profile] ?? RESOLUTION_PRESETS.broadcast;
  const bitrates = BITRATE_TABLES[family]?.[profile] ?? BITRATE_TABLES.h264.broadcast;

  return {
    codec: selectCodecString(family, resolution.width, resolution.height, resolution.framerate),
    width: resolution.width,
    height: resolution.height,
    bitrate: bitrates.video,
    framerate: resolution.framerate,
  };
}

export function getDefaultAudioSettings(profile: string): AudioEncoderSettings {
  // Audio bitrate is codec-independent (Opus for all)
  const bitrates = BITRATE_TABLES.h264[profile] ?? BITRATE_TABLES.h264.broadcast;
  return {
    codec: "opus",
    sampleRate: 48000,
    numberOfChannels: 2,
    bitrate: bitrates.audio,
  };
}

// ============================================================================
// Codec support detection
// ============================================================================

/**
 * Check which video codecs the browser's WebCodecs API can encode.
 * Returns a capabilities object with supported codecs and a recommendation.
 */
export async function detectEncoderCapabilities(): Promise<CodecCapabilities> {
  const supported: VideoCodecFamily[] = [];

  if (typeof VideoEncoder === "undefined") {
    return { video: ["h264"], audio: ["opus"], recommended: "h264" };
  }

  // Test H.264 (baseline — should pass on all WebCodecs implementations)
  try {
    const h264 = await VideoEncoder.isConfigSupported({
      codec: "avc1.42001e",
      width: 1280,
      height: 720,
      bitrate: 2_000_000,
      framerate: 30,
    });
    if (h264.supported) supported.push("h264");
  } catch {
    // H.264 not available
  }

  // Test VP9
  try {
    const vp9 = await VideoEncoder.isConfigSupported({
      codec: "vp09.00.30.08",
      width: 1280,
      height: 720,
      bitrate: 2_000_000,
      framerate: 30,
    });
    if (vp9.supported) supported.push("vp9");
  } catch {
    // VP9 not available
  }

  // Test AV1
  try {
    const av1 = await VideoEncoder.isConfigSupported({
      codec: "av01.0.08M.08",
      width: 1280,
      height: 720,
      bitrate: 2_000_000,
      framerate: 30,
    });
    if (av1.supported) supported.push("av1");
  } catch {
    // AV1 not available
  }

  // Recommendation: prefer VP9 (royalty-free + good compression), then AV1, then H.264
  let recommended: VideoCodecFamily = "h264";
  if (supported.includes("vp9")) recommended = "vp9";
  if (supported.includes("av1")) recommended = "av1";

  return {
    video: supported.length > 0 ? supported : ["h264"],
    audio: ["opus"],
    recommended,
  };
}

// ============================================================================
// Encoder config factory
// ============================================================================

/**
 * Create encoder config for a quality profile and codec family.
 * Optionally merge with encoder overrides from UI.
 */
export function createEncoderConfig(
  profile: string = "broadcast",
  codecFamily: VideoCodecFamily = "h264",
  overrides?: EncoderOverrides
): EncoderConfig {
  const baseVideo = getDefaultVideoSettings(profile, codecFamily);
  const baseAudio = getDefaultAudioSettings(profile);

  const finalWidth = overrides?.video?.width ?? baseVideo.width;
  const finalHeight = overrides?.video?.height ?? baseVideo.height;
  const finalFramerate = overrides?.video?.framerate ?? baseVideo.framerate;

  // Re-select codec string for the final resolution/framerate
  const codec = selectCodecString(codecFamily, finalWidth, finalHeight, finalFramerate);

  return {
    video: {
      ...baseVideo,
      codec,
      width: finalWidth,
      height: finalHeight,
      framerate: finalFramerate,
      ...(overrides?.video?.bitrate !== undefined && { bitrate: overrides.video.bitrate }),
    },
    audio: {
      ...baseAudio,
      ...(overrides?.audio?.bitrate !== undefined && { bitrate: overrides.audio.bitrate }),
      ...(overrides?.audio?.sampleRate !== undefined && { sampleRate: overrides.audio.sampleRate }),
      ...(overrides?.audio?.numberOfChannels !== undefined && {
        numberOfChannels: overrides.audio.numberOfChannels,
      }),
    },
  };
}

/**
 * Map a WebRTC MIME type (from SDP negotiation) to a codec family.
 * Returns null if unrecognized.
 */
export function mimeToCodecFamily(mime: string): VideoCodecFamily | null {
  const lower = mime.toLowerCase();
  if (lower.includes("vp9")) return "vp9";
  if (lower.includes("av1") || lower.includes("av01")) return "av1";
  if (lower.includes("h264") || lower.includes("avc")) return "h264";
  return null;
}

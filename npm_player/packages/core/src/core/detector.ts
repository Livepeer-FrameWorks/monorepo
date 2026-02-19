/**
 * Browser and Codec Detection
 * Ported from MistMetaPlayer v3.1.0
 *
 * Detects browser capabilities and codec support
 * Removes legacy IE/Flash detection code
 */

import { translateCodec } from "./CodecUtils";

export interface BrowserInfo {
  isChrome: boolean;
  isFirefox: boolean;
  isSafari: boolean;
  isEdge: boolean;
  isAndroid: boolean;
  isIOS: boolean;
  isMobile: boolean;
  supportsMediaSource: boolean;
  supportsWebRTC: boolean;
  supportsWebSocket: boolean;
}

export interface CodecSupport {
  h264: boolean;
  h265: boolean;
  vp8: boolean;
  vp9: boolean;
  av1: boolean;
  aac: boolean;
  mp3: boolean;
  opus: boolean;
}

/**
 * Detect browser information
 */
export function getBrowserInfo(): BrowserInfo {
  const ua = navigator.userAgent.toLowerCase();

  return {
    isChrome: /chrome|crios/.test(ua) && !/edge|edg/.test(ua),
    isFirefox: /firefox/.test(ua),
    isSafari: /safari/.test(ua) && !/chrome|crios/.test(ua),
    isEdge: /edge|edg/.test(ua),
    isAndroid: /android/.test(ua),
    isIOS: /iphone|ipad|ipod/.test(ua),
    isMobile: /mobile|android|iphone|ipad|ipod/.test(ua),
    supportsMediaSource: "MediaSource" in window,
    supportsWebRTC: "RTCPeerConnection" in window,
    supportsWebSocket: "WebSocket" in window,
  };
}

// Re-export translateCodec from CodecUtils for backwards compatibility
export { translateCodec };

/**
 * Test codec support using MediaSource API
 */
export function testCodecSupport(mimeType: string, codec: string): boolean {
  if (!("MediaSource" in window)) {
    return false;
  }

  if (!MediaSource.isTypeSupported) {
    return true; // Can't test, assume it works
  }

  const fullType = `${mimeType};codecs="${codec}"`;
  return MediaSource.isTypeSupported(fullType);
}

/**
 * Get comprehensive codec support info
 */
export function getCodecSupport(): CodecSupport {
  return {
    h264: testCodecSupport("video/mp4", "avc1.42E01E"),
    h265: testCodecSupport("video/mp4", "hev1.1.6.L93.90"),
    vp8: testCodecSupport("video/webm", "vp8"),
    vp9: testCodecSupport("video/webm", "vp09.00.10.08"),
    av1: testCodecSupport("video/mp4", "av01.0.04M.08"),
    aac: testCodecSupport("video/mp4", "mp4a.40.2"),
    mp3: testCodecSupport("audio/mpeg", "mp3"),
    opus: testCodecSupport("audio/webm", "opus"),
  };
}

/**
 * Check if tracks are playable by testing codecs
 */
export function checkTrackPlayability(
  tracks: Array<{ type: string; codec: string; codecstring?: string; init?: string }>,
  containerType: string
): { playable: string[]; supported: string[] } {
  const playable: string[] = [];
  const supported: string[] = [];

  const tracksByType: Record<string, typeof tracks> = {};

  // Group tracks by type
  for (const track of tracks) {
    if (track.type === "meta") continue;

    if (!tracksByType[track.type]) {
      tracksByType[track.type] = [];
    }
    tracksByType[track.type].push(track);
  }

  // Test each track type
  for (const [trackType, typeTracks] of Object.entries(tracksByType)) {
    let hasPlayableTrack = false;

    for (const track of typeTracks) {
      const codecString = translateCodec(track);
      if (testCodecSupport(containerType, codecString)) {
        supported.push(track.codec);
        hasPlayableTrack = true;
      }
    }

    if (hasPlayableTrack) {
      playable.push(trackType);
    }
  }

  return { playable, supported };
}

/**
 * Check protocol/scheme mismatch (http/https)
 */
export function checkProtocolMismatch(sourceUrl: string): boolean {
  const pageProtocol = window.location.protocol;
  const sourceProtocol = new URL(sourceUrl).protocol;

  // Allow file:// to access http://
  if (pageProtocol === "file:" && sourceProtocol === "http:") {
    return false; // No mismatch
  }

  return pageProtocol !== sourceProtocol;
}

/**
 * Check if current page is loaded over file://
 */
export function isFileProtocol(): boolean {
  return window.location.protocol === "file:";
}

/**
 * Detect iPad with broken HEVC MSE support.
 * Older iPads (iPadOS < 17) report HEVC as supported via MediaSource.isTypeSupported()
 * but fail in practice. Native HLS handles HEVC fine via hardware decoder.
 *
 * Note: Modern iPads masquerade as Mac in user agent, detectable via touch support.
 */
export function isIPadWithBrokenHEVC(): boolean {
  const ua = navigator.userAgent;
  const isIPad = /iPad/.test(ua) || (/Macintosh/.test(ua) && "ontouchend" in document);
  if (!isIPad) return false;

  const match = ua.match(/OS (\d+)/);
  if (!match) return false;
  return parseInt(match[1], 10) < 17;
}

/**
 * Get Android version (returns null if not Android)
 */
export function getAndroidVersion(): number | null {
  const match = navigator.userAgent.match(/Android (\d+)(?:\.(\d+))?(?:\.(\d+))*/i);
  if (!match) return null;

  const major = parseInt(match[1], 10);
  const minor = match[2] ? parseInt(match[2], 10) : 0;

  return major + minor / 10;
}

/**
 * Browser-specific compatibility checks
 */
export function getBrowserCompatibility() {
  const browser = getBrowserInfo();
  const android = getAndroidVersion();

  return {
    // Native HLS support
    supportsNativeHLS: browser.isSafari || browser.isIOS || (android && android >= 7),

    // MSE support
    supportsMSE: browser.supportsMediaSource,

    // WebSocket support
    supportsWebSocket: browser.supportsWebSocket,

    // WebRTC support
    supportsWebRTC: browser.supportsWebRTC && "RTCRtpReceiver" in window,

    // Specific player recommendations
    preferVideoJs: android && android < 7, // VideoJS better for older Android
    avoidMEWSOnMac: browser.isSafari, // MEWS breaks often on Safari/macOS

    // File protocol limitations
    fileProtocolLimitations: isFileProtocol(),
  };
}

// ============================================================================
// WebRTC Codec Compatibility
// ============================================================================

/**
 * Codecs that are compatible with WebRTC (WHEP/MistServer native)
 * These are the only codecs that can be used in RTP streams
 */
export const WEBRTC_COMPATIBLE_CODECS = {
  video: ["H264", "VP8", "VP9", "AV1"],
  // Note: AAC is NOT natively supported in browser WebRTC - OPUS is standard
  // MistServer may transcode to OPUS for WebRTC output
  audio: ["OPUS", "PCMU", "PCMA", "G711", "G722"],
} as const;

/**
 * Codecs that are explicitly incompatible with WebRTC
 */
export const WEBRTC_INCOMPATIBLE_CODECS = {
  video: ["HEVC", "H265", "THEORA", "MPEG2"],
  audio: ["AC3", "EAC3", "EC3", "MP3", "FLAC", "VORBIS", "DTS"],
} as const;

export interface WebRTCCodecCompatibility {
  /** Whether at least one video track is WebRTC-compatible */
  videoCompatible: boolean;
  /** Whether at least one audio track is WebRTC-compatible (or no audio tracks) */
  audioCompatible: boolean;
  /** Overall compatibility - both video and audio must be compatible */
  compatible: boolean;
  /** List of incompatible codecs found */
  incompatibleCodecs: string[];
  /** Detailed breakdown per track type */
  details: {
    videoTracks: number;
    audioTracks: number;
    compatibleVideoCodecs: string[];
    compatibleAudioCodecs: string[];
  };
}

/**
 * Check if browser supports a codec via WebRTC
 * Uses RTCRtpReceiver.getCapabilities() for dynamic browser detection (reference: webrtc.js:38-46)
 * Falls back to static list if API unavailable
 */
function isBrowserWebRTCCodecSupported(type: "video" | "audio", codec: string): boolean {
  // Try dynamic browser API first (reference: webrtc.js:39)
  if (typeof RTCRtpReceiver !== "undefined" && RTCRtpReceiver.getCapabilities) {
    try {
      const capabilities = RTCRtpReceiver.getCapabilities(type);
      if (capabilities?.codecs) {
        const targetMime = `${type}/${codec}`.toLowerCase();
        return capabilities.codecs.some((c) => c.mimeType.toLowerCase() === targetMime);
      }
    } catch {
      // Fall through to static list
    }
  }

  // Fallback to static list for older browsers/SSR
  const compatibleCodecs = WEBRTC_COMPATIBLE_CODECS[type] as readonly string[];
  return compatibleCodecs.includes(codec.toUpperCase());
}

/**
 * Check if stream tracks are compatible with WebRTC playback
 *
 * Uses RTCRtpReceiver.getCapabilities() to dynamically query browser support,
 * matching the reference MistServer implementation (webrtc.js:38-46).
 *
 * @param tracks - Array of track metadata from MistServer
 * @returns Compatibility assessment
 */
export function checkWebRTCCodecCompatibility(
  tracks: Array<{ type: string; codec: string }>
): WebRTCCodecCompatibility {
  const videoTracks = tracks.filter((t) => t.type === "video");
  const audioTracks = tracks.filter((t) => t.type === "audio");

  const compatibleVideoCodecs: string[] = [];
  const compatibleAudioCodecs: string[] = [];
  const incompatibleCodecs: string[] = [];

  // Check video tracks using dynamic browser detection
  for (const track of videoTracks) {
    if (isBrowserWebRTCCodecSupported("video", track.codec)) {
      compatibleVideoCodecs.push(track.codec);
    } else {
      incompatibleCodecs.push(`video:${track.codec}`);
    }
  }

  // Check audio tracks using dynamic browser detection
  for (const track of audioTracks) {
    if (isBrowserWebRTCCodecSupported("audio", track.codec)) {
      compatibleAudioCodecs.push(track.codec);
    } else {
      incompatibleCodecs.push(`audio:${track.codec}`);
    }
  }

  // Video is compatible if there's at least one compatible video codec
  // (or no video tracks at all - audio-only streams are fine)
  const videoCompatible = videoTracks.length === 0 || compatibleVideoCodecs.length > 0;

  // Audio is compatible if there's at least one compatible audio codec
  // (or no audio tracks at all - video-only streams are fine)
  const audioCompatible = audioTracks.length === 0 || compatibleAudioCodecs.length > 0;

  return {
    videoCompatible,
    audioCompatible,
    compatible: videoCompatible && audioCompatible,
    incompatibleCodecs,
    details: {
      videoTracks: videoTracks.length,
      audioTracks: audioTracks.length,
      compatibleVideoCodecs,
      compatibleAudioCodecs,
    },
  };
}

// ============================================================================
// MSE Codec Compatibility (for HLS.js, DASH.js)
// ============================================================================

export interface MSECodecCompatibility {
  /** Whether at least one video track is MSE-compatible in this browser */
  videoCompatible: boolean;
  /** Whether at least one audio track is MSE-compatible in this browser */
  audioCompatible: boolean;
  /** Overall compatibility */
  compatible: boolean;
  /** Codecs that failed browser support test */
  unsupportedCodecs: string[];
}

/**
 * Check if stream tracks are compatible with MSE playback in this browser
 *
 * Unlike WebRTC, MSE compatibility varies by browser. HEVC works in Safari
 * but not Firefox. This function actually tests MediaSource.isTypeSupported().
 *
 * @param tracks - Array of track metadata
 * @param containerType - MIME type (e.g., 'video/mp4')
 */
export function checkMSECodecCompatibility(
  tracks: Array<{ type: string; codec: string; codecstring?: string; init?: string }>,
  containerType = "video/mp4"
): MSECodecCompatibility {
  const videoTracks = tracks.filter((t) => t.type === "video");
  const audioTracks = tracks.filter((t) => t.type === "audio");
  const unsupportedCodecs: string[] = [];

  let hasCompatibleVideo = videoTracks.length === 0;
  let hasCompatibleAudio = audioTracks.length === 0;

  // Test each video track
  for (const track of videoTracks) {
    const codecString = translateCodec(track);
    if (testCodecSupport(containerType, codecString)) {
      hasCompatibleVideo = true;
    } else {
      unsupportedCodecs.push(`video:${track.codec}`);
    }
  }

  // Test each audio track
  for (const track of audioTracks) {
    const codecString = translateCodec(track);
    const audioContainer = track.codec === "MP3" ? "audio/mpeg" : containerType;
    if (testCodecSupport(audioContainer, codecString)) {
      hasCompatibleAudio = true;
    } else {
      unsupportedCodecs.push(`audio:${track.codec}`);
    }
  }

  return {
    videoCompatible: hasCompatibleVideo,
    audioCompatible: hasCompatibleAudio,
    compatible: hasCompatibleVideo && hasCompatibleAudio,
    unsupportedCodecs,
  };
}

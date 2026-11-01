/**
 * Browser Feature Detection
 * Detects WebCodecs, WebRTC, and MediaDevices capabilities
 */

import type { BrowserCapabilities, StudioSupport } from "../types";

/**
 * Detect all browser capabilities for streaming
 */
export function detectCapabilities(): BrowserCapabilities {
  const webcodecs = {
    videoEncoder: typeof VideoEncoder !== "undefined",
    audioEncoder: typeof AudioEncoder !== "undefined",
    mediaStreamTrackProcessor: typeof MediaStreamTrackProcessor !== "undefined",
    mediaStreamTrackGenerator: typeof MediaStreamTrackGenerator !== "undefined",
  };

  const webrtc = {
    peerConnection: typeof RTCPeerConnection !== "undefined",
    replaceTrack: typeof RTCRtpSender !== "undefined" && "replaceTrack" in RTCRtpSender.prototype,
    insertableStreams:
      typeof RTCRtpSender !== "undefined" && "createEncodedStreams" in RTCRtpSender.prototype,
    scriptTransform: typeof RTCRtpScriptTransform !== "undefined",
  };

  const mediaDevices = {
    getUserMedia:
      typeof navigator !== "undefined" &&
      typeof navigator.mediaDevices !== "undefined" &&
      typeof navigator.mediaDevices.getUserMedia === "function",
    getDisplayMedia:
      typeof navigator !== "undefined" &&
      typeof navigator.mediaDevices !== "undefined" &&
      typeof navigator.mediaDevices.getDisplayMedia === "function",
    enumerateDevices:
      typeof navigator !== "undefined" &&
      typeof navigator.mediaDevices !== "undefined" &&
      typeof navigator.mediaDevices.enumerateDevices === "function",
  };

  // Determine recommended path
  const webCodecsFullSupport =
    webcodecs.videoEncoder &&
    webcodecs.audioEncoder &&
    webcodecs.mediaStreamTrackProcessor &&
    webcodecs.mediaStreamTrackGenerator;

  const recommended: "webcodecs" | "mediastream" = webCodecsFullSupport
    ? "webcodecs"
    : "mediastream";

  return {
    webcodecs,
    webrtc,
    mediaDevices,
    recommended,
  };
}

/**
 * Check if WebCodecs is fully supported
 */
export function isWebCodecsSupported(): boolean {
  return (
    typeof VideoEncoder !== "undefined" &&
    typeof AudioEncoder !== "undefined" &&
    typeof MediaStreamTrackProcessor !== "undefined" &&
    typeof MediaStreamTrackGenerator !== "undefined"
  );
}

/**
 * Check if basic WebRTC is supported
 */
export function isWebRTCSupported(): boolean {
  return typeof RTCPeerConnection !== "undefined";
}

/**
 * Check if media devices API is available
 */
export function isMediaDevicesSupported(): boolean {
  return (
    typeof navigator !== "undefined" &&
    typeof navigator.mediaDevices !== "undefined" &&
    typeof navigator.mediaDevices.getUserMedia === "function"
  );
}

/**
 * Check if screen capture is supported
 */
export function isScreenCaptureSupported(): boolean {
  return (
    typeof navigator !== "undefined" &&
    typeof navigator.mediaDevices !== "undefined" &&
    typeof navigator.mediaDevices.getDisplayMedia === "function"
  );
}

/**
 * Check if a media file can be played into the stream, which MediaFileSource
 * does through the element's captureStream()
 */
export function isMediaFileCaptureSupported(): boolean {
  return typeof HTMLMediaElement !== "undefined" && "captureStream" in HTMLMediaElement.prototype;
}

/**
 * The state to render before a page has reached a browser. Nothing is known
 * during server rendering, so controls start available and detectStudioSupport()
 * corrects them once mounted.
 */
export const FULL_STUDIO_SUPPORT: StudioSupport = {
  camera: true,
  screen: true,
  publish: true,
  mediaFile: true,
};

/**
 * What this browser lets a broadcaster do. Each capability stands alone, so one
 * missing never takes the others down with it.
 */
export function detectStudioSupport(): StudioSupport {
  return {
    camera: isMediaDevicesSupported(),
    screen: isScreenCaptureSupported(),
    publish: isWebRTCSupported(),
    mediaFile: isMediaFileCaptureSupported(),
  };
}

/**
 * Check if RTCRtpScriptTransform is supported
 * This is required for Path C WebCodecs integration (replacing browser-encoded frames)
 */
export function isRTCRtpScriptTransformSupported(): boolean {
  return typeof RTCRtpScriptTransform !== "undefined";
}

/**
 * Check if full WebCodecs encoding path is available
 * Requires both WebCodecs support AND RTCRtpScriptTransform for Path C
 */
export function isWebCodecsEncodingPathSupported(): boolean {
  return isWebCodecsSupported() && isRTCRtpScriptTransformSupported();
}

/**
 * Get the recommended streaming path based on browser capabilities
 */
export function getRecommendedPath(): "webcodecs" | "mediastream" {
  return isWebCodecsSupported() ? "webcodecs" : "mediastream";
}

/**
 * Check if a specific video codec is supported for encoding
 */
export async function isVideoCodecSupported(codec: string): Promise<boolean> {
  if (!isWebCodecsSupported()) {
    return false;
  }

  try {
    const support = await VideoEncoder.isConfigSupported({
      codec,
      width: 1920,
      height: 1080,
      bitrate: 2_000_000,
      framerate: 30,
    });
    return support.supported === true;
  } catch {
    return false;
  }
}

/**
 * Check if a specific audio codec is supported for encoding
 */
export async function isAudioCodecSupported(codec: string): Promise<boolean> {
  if (!isWebCodecsSupported()) {
    return false;
  }

  try {
    const support = await AudioEncoder.isConfigSupported({
      codec,
      sampleRate: 48000,
      numberOfChannels: 2,
      bitrate: 128_000,
    });
    return support.supported === true;
  } catch {
    return false;
  }
}

/**
 * Get list of likely supported video codecs
 */
export async function getSupportedVideoCodecs(): Promise<string[]> {
  const codecs = [
    "avc1.42E01E", // H.264 Baseline
    "avc1.4D401E", // H.264 Main
    "avc1.64001E", // H.264 High
    "vp8",
    "vp09.00.10.08", // VP9 Profile 0
  ];

  const supported: string[] = [];
  for (const codec of codecs) {
    if (await isVideoCodecSupported(codec)) {
      supported.push(codec);
    }
  }

  return supported;
}

/**
 * Get list of likely supported audio codecs
 */
export async function getSupportedAudioCodecs(): Promise<string[]> {
  const codecs = ["opus", "mp4a.40.2"]; // Opus, AAC-LC

  const supported: string[] = [];
  for (const codec of codecs) {
    if (await isAudioCodecSupported(codec)) {
      supported.push(codec);
    }
  }

  return supported;
}

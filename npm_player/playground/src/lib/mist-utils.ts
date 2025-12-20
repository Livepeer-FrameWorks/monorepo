import type { IngestUris, MistSource, ContentEndpoints } from "./types";
import { DEFAULTS, MIST_PORTS } from "./constants";

/**
 * Sanitize a base URL - ensure protocol prefix, strip trailing slashes.
 */
export function sanitizeBaseUrl(input: string): string {
  const trimmed = input.trim();
  if (!trimmed) return DEFAULTS.baseUrl;
  if (!/^https?:\/\//i.test(trimmed)) {
    return `http://${trimmed.replace(/^\/+/, "")}`;
  }
  return trimmed.replace(/\/+$/, "");
}

/**
 * Normalize viewer path - ensure leading slash, no trailing slash.
 */
export function normalizeViewerPath(path: string): string {
  const trimmed = path.trim();
  if (!trimmed || trimmed === "/") return "";
  const withoutLeading = trimmed.replace(/^\/+/, "");
  const withoutTrailing = withoutLeading.replace(/\/+$/, "");
  return withoutTrailing ? `/${withoutTrailing}` : "";
}

/**
 * Extract hostname from a URL (without port).
 */
export function extractHostname(url: string): string {
  try {
    return new URL(url).hostname;
  } catch {
    return "localhost";
  }
}

/**
 * Build the viewer base URL from base URL and viewer path.
 */
export function buildViewerBase(baseUrl: string, viewerPath: string): string {
  const sanitized = sanitizeBaseUrl(baseUrl);
  const normalized = normalizeViewerPath(viewerPath);
  return `${sanitized}${normalized}`;
}

/**
 * Build the MistServer JSON endpoint URL for a stream.
 * Format: {viewerBase}/json_{streamName}.js
 */
export function buildMistJsonUrl(viewerBase: string, streamName: string): string {
  const encoded = encodeURIComponent(streamName);
  return `${viewerBase}/json_${encoded}.js`;
}

/**
 * Generate ingest URIs from base URL and stream name.
 * Uses default MistServer ports for RTMP/SRT.
 */
export function buildIngestUris(baseUrl: string, streamName: string): IngestUris {
  const hostname = extractHostname(baseUrl);
  const sanitized = sanitizeBaseUrl(baseUrl);

  return {
    rtmp: `rtmp://${hostname}:${MIST_PORTS.rtmp}/live/${streamName}`,
    srt: `srt://${hostname}:${MIST_PORTS.srt}?streamid=${streamName}`,
    whip: `${sanitized}/webrtc/${streamName}`
  };
}

// Local storage utilities
export function loadString(key: string): string | null {
  if (typeof window === "undefined") return null;
  return window.localStorage.getItem(key);
}

export function saveString(key: string, value: string): void {
  if (typeof window === "undefined") return;
  window.localStorage.setItem(key, value);
}

/**
 * Map MistServer type string to protocol identifier.
 * Note: MistServer's "webrtc" type uses ws:// (WebSocket), NOT WHEP (HTTP).
 * WHEP is a separate protocol that uses HTTP POST/DELETE for signaling.
 */
export function mapMistTypeToProtocol(mistType: string): string {
  // Check WebSocket protocols FIRST - these have type like "ws/video/mp4"
  // Must be before content-type matching to avoid collision with HTTP versions
  if (mistType.startsWith("ws/") || mistType.startsWith("wss/")) return "MEWS_WS";
  if (mistType.includes("webrtc")) return "MIST_WEBRTC"; // MistServer ws:// WebRTC signaling

  // HTTP-based protocols
  if (mistType.includes("mpegurl") || mistType.includes("m3u8")) return "HLS";
  if (mistType.includes("dash") || mistType.includes("mpd")) return "DASH";
  if (mistType.includes("whep")) return "WHEP";
  if (mistType.includes("mp4")) return "MP4";
  if (mistType.includes("webm")) return "WEBM";
  if (mistType.includes("rtsp")) return "RTSP";
  return mistType;
}

/**
 * Protocol priority for auto-selection (lower = higher priority).
 * HTTP-based protocols are preferred as they work with standard fetch/video.
 */
const PROTOCOL_PRIORITY: Record<string, number> = {
  HLS: 1,
  DASH: 2,
  MP4: 3,
  WEBM: 4,
  WHEP: 5,
  RTSP: 10,
  MIST_WS: 99, // ws:// requires special handling, deprioritize
};

/**
 * Map protocol identifier to MIME type for player forceType option.
 */
export const PROTOCOL_TO_MIME: Record<string, string> = {
  HLS: "html5/application/vnd.apple.mpegurl",
  DASH: "dash/video/mp4",
  MP4: "html5/video/mp4",
  WEBM: "html5/video/webm",
  WHEP: "whep",
};

/**
 * Build ContentEndpoints from MistServer JSON sources.
 * @param sources - MistServer JSON source array
 * @param streamName - Stream identifier
 * @param viewerBase - MistServer viewer base URL (e.g., "http://localhost:8080")
 * @param selectedProtocol - Optional protocol to force (e.g., "HLS", "DASH", "WHEP")
 */
export function buildContentEndpointsFromSources(
  sources: MistSource[],
  streamName: string,
  viewerBase: string,
  selectedProtocol?: string | null
): ContentEndpoints | null {
  if (!sources || sources.length === 0) return null;

  // Build outputs map from all sources (for player fallback)
  const outputs: Record<string, { protocol: string; url: string }> = {};
  for (const source of sources) {
    const protocol = mapMistTypeToProtocol(source.type);
    if (!outputs[protocol]) {
      outputs[protocol] = { protocol, url: source.url };
    }
  }

  // Find primary source
  let primarySource: MistSource | undefined;

  if (selectedProtocol) {
    // User selected a specific protocol - find matching source
    primarySource = sources.find(s => mapMistTypeToProtocol(s.type) === selectedProtocol);
  }

  if (!primarySource) {
    // Auto-select: filter out ws:// sources, sort by our protocol priority
    const httpSources = sources.filter(s => !s.url.startsWith("ws://"));

    if (httpSources.length > 0) {
      // Sort by protocol priority (HLS > DASH > MP4 > etc.)
      primarySource = httpSources.sort((a, b) => {
        const protoA = mapMistTypeToProtocol(a.type);
        const protoB = mapMistTypeToProtocol(b.type);
        const priorityA = PROTOCOL_PRIORITY[protoA] ?? 50;
        const priorityB = PROTOCOL_PRIORITY[protoB] ?? 50;
        return priorityA - priorityB;
      })[0];
    } else {
      // Fallback to first source if all are ws://
      primarySource = sources[0];
    }
  }

  if (!primarySource) return null;

  const primary = {
    nodeId: `mist-${streamName}`,
    protocol: mapMistTypeToProtocol(primarySource.type),
    url: primarySource.url,
    baseUrl: viewerBase,
    outputs
  };

  return {
    primary,
    fallbacks: []
  };
}

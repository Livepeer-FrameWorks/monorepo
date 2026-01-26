// Parse a URL and extract components for building protocol-specific URLs
interface ParsedStreamingUrl {
  hostname: string;
  port: string;
  useTls: boolean;
}

function parseStreamingUrl(url?: string): ParsedStreamingUrl {
  if (!url) {
    return { hostname: "", port: "", useTls: false };
  }
  try {
    const parsed = new URL(url);
    return {
      hostname: parsed.hostname,
      port: parsed.port,
      useTls: parsed.protocol === "https:",
    };
  } catch {
    // Fallback for malformed URLs
    return { hostname: "", port: "", useTls: false };
  }
}

// Raw config from environment - these are base URLs that we parse to construct protocol-specific URLs
const rawConfig = {
  gatewayBaseUrl: import.meta.env.VITE_GATEWAY_URL,
  ingestUrl: import.meta.env.VITE_STREAMING_INGEST_URL,
  playUrl: import.meta.env.VITE_STREAMING_PLAY_URL, // Foghorn for HTTP 307 redirects
  edgeUrl: import.meta.env.VITE_STREAMING_EDGE_URL, // Direct edge for non-HTTP protocols
  graphqlUrl: import.meta.env.VITE_GRAPHQL_HTTP_URL,
  rtmpPort: import.meta.env.VITE_STREAMING_RTMP_PORT || "1935",
  srtPort: import.meta.env.VITE_STREAMING_SRT_PORT || "8889",
  rtmpPath: import.meta.env.VITE_STREAMING_RTMP_PATH || "/live",
  hlsPath: import.meta.env.VITE_STREAMING_HLS_PATH || "/hls",
  webrtcPath: import.meta.env.VITE_STREAMING_WEBRTC_PATH || "/webrtc",
  embedPath: import.meta.env.VITE_STREAMING_EMBED_PATH || "/",
  marketingSiteUrl: import.meta.env.VITE_MARKETING_SITE_URL,
  docsSiteUrl: import.meta.env.VITE_DOCS_SITE_URL,
  githubUrl: import.meta.env.VITE_GITHUB_URL,
};

// Parsed URLs for deriving hostnames and TLS mode
const ingest = parseStreamingUrl(rawConfig.ingestUrl);
const play = parseStreamingUrl(rawConfig.playUrl); // Foghorn for HTTP protocols
const edge = parseStreamingUrl(rawConfig.edgeUrl); // Direct edge for non-HTTP protocols

// Derived config that components can use
interface Config {
  // Ingest endpoints
  ingestHostname: string;
  ingestUseTls: boolean;
  rtmpPort: string;
  srtPort: string;
  rtmpPath: string;
  // Foghorn (HTTP protocol 307 redirects)
  playHostname: string;
  playPort: string;
  playUseTls: boolean;
  // Edge/delivery endpoints (direct for non-HTTP protocols)
  edgeHostname: string;
  edgePort: string;
  edgeUseTls: boolean;
  hlsPath: string;
  webrtcPath: string;
  embedPath: string;
  // Other
  marketingSiteUrl: string;
  docsSiteUrl: string;
  githubUrl: string;
}

const config: Config = {
  // Ingest
  ingestHostname: ingest.hostname,
  ingestUseTls: ingest.useTls,
  rtmpPort: rawConfig.rtmpPort,
  srtPort: rawConfig.srtPort,
  rtmpPath: rawConfig.rtmpPath,
  // Foghorn (play)
  playHostname: play.hostname,
  playPort: play.port,
  playUseTls: play.useTls,
  // Edge (direct)
  edgeHostname: edge.hostname,
  edgePort: edge.port,
  edgeUseTls: edge.useTls,
  hlsPath: rawConfig.hlsPath,
  webrtcPath: rawConfig.webrtcPath,
  embedPath: rawConfig.embedPath,
  // Other
  marketingSiteUrl: rawConfig.marketingSiteUrl ?? "",
  docsSiteUrl: rawConfig.docsSiteUrl ?? "",
  githubUrl: rawConfig.githubUrl ?? "",
};

// Determine if we're in development
const isDev = import.meta.env.DEV;

// Build full RTMP URL: rtmp(s)://hostname:port/path
function buildRtmpUrl(): string {
  if (!config.ingestHostname) return "";
  const proto = config.ingestUseTls ? "rtmps" : "rtmp";
  return `${proto}://${config.ingestHostname}:${config.rtmpPort}${config.rtmpPath}`;
}

// Build full SRT URL: srt://hostname:port
function buildSrtBaseUrl(): string {
  if (!config.ingestHostname) return "";
  return `srt://${config.ingestHostname}:${config.srtPort}`;
}

// Build HTTP(S) base URL for edge delivery (direct non-HTTP protocols)
function _buildEdgeBaseUrl(): string {
  if (!config.edgeHostname) return "";
  const proto = config.edgeUseTls ? "https" : "http";
  const portPart = config.edgePort ? `:${config.edgePort}` : "";
  return `${proto}://${config.edgeHostname}${portPart}`;
}

// Build HTTP(S) base URL for Foghorn (HTTP protocol 307 redirects)
function buildPlayBaseUrl(): string {
  if (!config.playHostname) return "";
  const proto = config.playUseTls ? "https" : "http";
  const portPart = config.playPort ? `:${config.playPort}` : "";
  return `${proto}://${config.playHostname}${portPart}`;
}

// Build WHIP/WHEP base URL (same host as ingest, uses HTTP(S))
function buildWhipBaseUrl(): string {
  if (!config.ingestHostname) return "";
  const proto = config.ingestUseTls ? "https" : "http";
  const parsed = parseStreamingUrl(rawConfig.ingestUrl);
  const portPart = parsed.port ? `:${parsed.port}` : "";
  return `${proto}://${config.ingestHostname}${portPart}`;
}

function joinGatewayPath(path: string): string {
  const base = (rawConfig.gatewayBaseUrl || "").replace(/\/$/, "");
  const suffix = path.startsWith("/") ? path : `/${path}`;
  return `${base}${suffix}`;
}

interface IngestUrls {
  rtmp: string;
  srt: string;
  whip: string;
}

interface DeliveryUrls {
  hls: string;
  webrtc: string;
  webm: string;
  mkv: string;
  mp4: string;
  embed: string;
}

export function getIngestUrls(streamKey: string): Partial<IngestUrls> {
  if (!streamKey) return {};

  const rtmpBase = buildRtmpUrl();
  const srtBase = buildSrtBaseUrl();
  const whipBase = buildWhipBaseUrl();

  return {
    rtmp: `${rtmpBase}/${streamKey}`,
    srt: `${srtBase}?streamid=${streamKey}&latency=200&mode=caller`,
    whip: `${whipBase}${config.webrtcPath}/${streamKey}`,
  };
}

export function getDeliveryUrls(playbackId: string): Partial<DeliveryUrls> {
  if (!playbackId) return {};

  const playBase = buildPlayBaseUrl(); // HTTP protocols via Foghorn

  return {
    hls: `${playBase}${config.hlsPath}/${playbackId}/index.m3u8`,
    webrtc: `${playBase}${config.webrtcPath}/${playbackId}`,
    webm: `${playBase}/${playbackId}.webm`,
    mkv: `${playBase}/${playbackId}.mkv`,
    mp4: `${playBase}/${playbackId}.mp4`,
    embed: `${playBase}${config.embedPath}/${playbackId}`,
  };
}

// =============================================================================
// UNIFIED CONTENT DELIVERY URLs (via Foghorn /play/ path)
// Works for all content types: live/clip/dvr/vod (playbackId)
// =============================================================================

export type ContentType = "live" | "clip" | "dvr" | "vod";

/** Primary protocols shown by default in the UI */
export interface PrimaryProtocolUrls {
  /** Unified play page - returns JSON with all protocols */
  play: string;
  /** HLS (TS segments) - best compatibility */
  hls: string;
  /** HLS (CMAF) - lower latency variant */
  hlsCmaf: string;
  /** DASH - MPEG-DASH adaptive streaming */
  dash: string;
  /** WebRTC (WHEP) - ultra-low latency */
  webrtc: string;
  /** MP4 - progressive download */
  mp4: string;
  /** WebM - open format (VP8/VP9) */
  webm: string;
  /** SRT - low-latency contribution/delivery */
  srt: string;
}

/** Additional protocols available via expandable UI */
export interface AdditionalProtocolUrls {
  /** RTSP - IP cameras, VLC, ffmpeg */
  rtsp: string;
  /** RTMP - legacy Flash/OBS playback */
  rtmp: string;
  /** MPEG-TS - transport stream, DVB compatible */
  ts: string;
  /** FLV - legacy Flash video */
  flv: string;
  /** MKV - Matroska container */
  mkv: string;
  /** AAC - audio-only stream */
  aac: string;
  /** Smooth Streaming - Microsoft format */
  smoothStreaming: string;
  /** HDS - Adobe HTTP Dynamic Streaming */
  hds: string;
  /** SDP - Session Description Protocol */
  sdp: string;
  /** Raw H264 elementary stream */
  rawH264: string;
  /** MP4 over WebSocket */
  wsmp4: string;
  /** WebRTC over WebSocket */
  wsWebrtc: string;
  /** DTSC - MistServer internal protocol */
  dtsc: string;
}

export interface ContentDeliveryUrls {
  primary: PrimaryProtocolUrls;
  additional: AdditionalProtocolUrls;
  embed: string;
  share: string;
}

/**
 * Generate a shareable view URL for any content type.
 * Uses the /view route with only the content id (type is resolved server-side).
 */
export function getShareUrl(contentId: string): string {
  if (!contentId) return "";
  const base = typeof window !== "undefined" ? window.location.origin : "";
  return `${base}/view?id=${contentId}`;
}

/**
 * Generate playback URLs for any content type using Foghorn's unified /play/ path.
 * Works for live streams, clips, DVR recordings, and VOD assets (playbackId).
 *
 * All URLs route through Foghorn which:
 * - Resolves content type automatically
 * - Load balances across edge nodes
 * - Returns 307 redirects to the correct edge node
 * - MistServer handles on-the-fly muxing for container formats
 */
export function getContentDeliveryUrls(
  contentId: string,
  contentType: ContentType,
): ContentDeliveryUrls {
  if (!contentId) {
    return {
      primary: {} as PrimaryProtocolUrls,
      additional: {} as AdditionalProtocolUrls,
      embed: "",
      share: "",
    };
  }

  const playBase = buildPlayBaseUrl(); // Foghorn for HTTP protocols (307 redirects)
  const proto = config.edgeUseTls ? "s" : ""; // for srt/rtsp/rtmp/dtsc secure variants
  const wsProto = config.edgeUseTls ? "wss" : "ws";

  // Primary protocols - HTTP via Foghorn (307 redirects), non-HTTP direct to edge
  const primary: PrimaryProtocolUrls = {
    play: `${playBase}/play/${contentId}`,
    hls: `${playBase}/play/${contentId}/hls/index.m3u8`,
    hlsCmaf: `${playBase}/play/${contentId}/cmaf/index.m3u8`,
    dash: `${playBase}/play/${contentId}/cmaf/index.mpd`,
    webrtc: `${playBase}/play/${contentId}/webrtc`,
    mp4: `${playBase}/play/${contentId}.mp4`,
    webm: `${playBase}/play/${contentId}.webm`,
    srt: `srt${proto}://${config.edgeHostname}:${config.srtPort}?streamid=${contentId}`, // Direct edge (UDP)
  };

  // Additional protocols - HTTP via Foghorn, non-HTTP direct to edge
  const additional: AdditionalProtocolUrls = {
    rtsp: `rtsp${proto}://${config.edgeHostname}:${config.edgePort || "554"}/play/${contentId}`, // Direct edge
    rtmp: `rtmp${proto}://${config.edgeHostname}:${config.rtmpPort}/play/${contentId}`, // Direct edge
    ts: `${playBase}/play/${contentId}.ts`,
    flv: `${playBase}/play/${contentId}.flv`,
    mkv: `${playBase}/play/${contentId}.mkv`,
    aac: `${playBase}/play/${contentId}.aac`,
    smoothStreaming: `${playBase}/play/${contentId}/cmaf/Manifest`,
    hds: `${playBase}/play/${contentId}/dynamic/manifest.f4m`,
    sdp: `${playBase}/play/${contentId}.sdp`,
    rawH264: `${playBase}/play/${contentId}.h264`,
    wsmp4: `${wsProto}://${config.edgeHostname}:${config.edgePort || "8080"}/play/${contentId}.mp4`, // WebSocket direct edge
    wsWebrtc: `${wsProto}://${config.edgeHostname}:${config.edgePort || "8080"}/play/webrtc/${contentId}`, // WebSocket direct edge
    dtsc: `dtsc${proto}://${config.edgeHostname}:${config.edgePort || "4200"}/play/${contentId}`, // Direct edge (MistServer internal)
  };

  const embed = getEmbedCodeForContent(contentId, contentType);
  const share = getShareUrl(contentId);

  return { primary, additional, embed, share };
}

/**
 * Generate embed code snippet for a given content type
 */
function getEmbedCodeForContent(
  contentId: string,
  contentType: ContentType,
): string {
  return `import { Player } from '@livepeer-frameworks/player-react';
import '@livepeer-frameworks/player-react/player.css';

<Player
  contentType="${contentType}"
  contentId="${contentId}"
  options={{
    gatewayUrl: '${rawConfig.graphqlUrl}',
    autoplay: true,
    muted: true,
  }}
/>`;
}

/** Protocol metadata for UI display */
export interface ProtocolInfo {
  key: keyof PrimaryProtocolUrls | keyof AdditionalProtocolUrls;
  label: string;
  description: string;
  category: "primary" | "additional";
}

/** Protocol information for building UI selectors */
export const PROTOCOL_INFO: ProtocolInfo[] = [
  // Primary protocols
  {
    key: "play",
    label: "Play Page",
    description: "Universal endpoint - returns all protocols",
    category: "primary",
  },
  {
    key: "hls",
    label: "HLS",
    description: "HTTP Live Streaming - best compatibility",
    category: "primary",
  },
  {
    key: "hlsCmaf",
    label: "HLS (CMAF)",
    description: "Lower latency HLS variant",
    category: "primary",
  },
  {
    key: "dash",
    label: "DASH",
    description: "MPEG-DASH adaptive streaming",
    category: "primary",
  },
  {
    key: "webrtc",
    label: "WebRTC",
    description: "Ultra-low latency (~0.5s)",
    category: "primary",
  },
  {
    key: "mp4",
    label: "MP4",
    description: "Progressive download",
    category: "primary",
  },
  {
    key: "webm",
    label: "WebM",
    description: "Open format (VP8/VP9)",
    category: "primary",
  },
  {
    key: "srt",
    label: "SRT",
    description: "Secure Reliable Transport",
    category: "primary",
  },
  // Additional protocols
  {
    key: "rtsp",
    label: "RTSP",
    description: "IP cameras, VLC, ffmpeg",
    category: "additional",
  },
  {
    key: "rtmp",
    label: "RTMP",
    description: "Legacy Flash/OBS playback",
    category: "additional",
  },
  {
    key: "ts",
    label: "MPEG-TS",
    description: "Transport stream, DVB compatible",
    category: "additional",
  },
  {
    key: "flv",
    label: "FLV",
    description: "Flash Video (legacy)",
    category: "additional",
  },
  {
    key: "mkv",
    label: "MKV",
    description: "Matroska container",
    category: "additional",
  },
  {
    key: "aac",
    label: "AAC",
    description: "Audio-only stream",
    category: "additional",
  },
  {
    key: "smoothStreaming",
    label: "Smooth Streaming",
    description: "Microsoft format",
    category: "additional",
  },
  {
    key: "hds",
    label: "HDS",
    description: "Adobe HTTP Dynamic Streaming",
    category: "additional",
  },
  {
    key: "sdp",
    label: "SDP",
    description: "Session Description Protocol",
    category: "additional",
  },
  {
    key: "rawH264",
    label: "Raw H264",
    description: "Elementary video stream",
    category: "additional",
  },
  {
    key: "wsmp4",
    label: "WS/MP4",
    description: "MP4 over WebSocket",
    category: "additional",
  },
  {
    key: "wsWebrtc",
    label: "WS/WebRTC",
    description: "WebRTC over WebSocket",
    category: "additional",
  },
  {
    key: "dtsc",
    label: "DTSC",
    description: "MistServer internal",
    category: "additional",
  },
];

// Convenience function to get just the RTMP server URL (without stream key)
export function getRtmpServerUrl(): string {
  return buildRtmpUrl();
}

export function getIngestUrl(streamKey: string): string {
  const urls = getIngestUrls(streamKey);
  return urls.rtmp || "";
}

export function getPlaybackUrl(playbackId: string): string {
  const urls = getDeliveryUrls(playbackId);
  return urls.hls || "";
}

export function getEmbedCode(playbackId: string): string {
  if (!playbackId) return "";
  const graphqlUrl = rawConfig.graphqlUrl ?? "";
  // Return NPM package usage snippet instead of iframe
  return `import { Player } from '@livepeer-frameworks/player-react';
import '@livepeer-frameworks/player-react/player.css';

<Player
  contentType="live"
  contentId="${playbackId}"
  options={{
    gatewayUrl: '${graphqlUrl}',
    autoplay: true,
    muted: true,
  }}
/>`;
}

export function getMarketingSiteUrl(): string {
  return config.marketingSiteUrl;
}

export function getDocsSiteUrl(): string {
  return config.docsSiteUrl;
}

export function getGithubUrl(): string {
  return config.githubUrl;
}

export function getGraphqlHttpUrl(): string {
  return rawConfig.graphqlUrl ?? "";
}

export function getMcpEndpoint(): string {
  if (!rawConfig.gatewayBaseUrl) return "";
  return joinGatewayPath("/mcp");
}

export { config, isDev };

import { getStreamingConfig } from "$lib/stores/streaming-config";

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

// Resolved cluster-aware endpoints. When streamingConfig is available (user
// authenticated + Quartermaster routing), cluster domains override env vars.
// Cluster domains always use TLS.
interface ResolvedEndpoints {
  ingestHostname: string;
  ingestUseTls: boolean;
  playHostname: string;
  playUseTls: boolean;
  edgeHostname: string;
  edgeUseTls: boolean;
  srtPort: string;
  rtmpPort: string;
}

function resolveEndpoints(): ResolvedEndpoints {
  const sc = getStreamingConfig();
  if (sc?.ingestDomain) {
    return {
      ingestHostname: sc.ingestDomain,
      ingestUseTls: true,
      playHostname: sc.playDomain ?? config.playHostname,
      playUseTls: true,
      edgeHostname: sc.edgeDomain ?? config.edgeHostname,
      edgeUseTls: true,
      srtPort: sc.srtPort != null ? String(sc.srtPort) : config.srtPort,
      rtmpPort: sc.rtmpPort != null ? String(sc.rtmpPort) : config.rtmpPort,
    };
  }
  return {
    ingestHostname: config.ingestHostname,
    ingestUseTls: config.ingestUseTls,
    playHostname: config.playHostname,
    playUseTls: config.playUseTls,
    edgeHostname: config.edgeHostname,
    edgeUseTls: config.edgeUseTls,
    srtPort: config.srtPort,
    rtmpPort: config.rtmpPort,
  };
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

  const ep = resolveEndpoints();
  if (!ep.ingestHostname) return {};

  const rtmpProto = ep.ingestUseTls ? "rtmps" : "rtmp";
  const httpProto = ep.ingestUseTls ? "https" : "http";
  const ingestPortPart = parseStreamingUrl(rawConfig.ingestUrl).port;
  const whipPort = ingestPortPart ? `:${ingestPortPart}` : "";

  return {
    rtmp: `${rtmpProto}://${ep.ingestHostname}:${ep.rtmpPort}${config.rtmpPath}/${streamKey}`,
    srt: `srt://${ep.ingestHostname}:${ep.srtPort}?streamid=${streamKey}&latency=200&mode=caller`,
    whip: `${httpProto}://${ep.ingestHostname}${whipPort}${config.webrtcPath}/${streamKey}`,
  };
}

export function getDeliveryUrls(playbackId: string): Partial<DeliveryUrls> {
  if (!playbackId) return {};

  const ep = resolveEndpoints();
  if (!ep.playHostname) return {};

  const proto = ep.playUseTls ? "https" : "http";
  const portPart = config.playPort ? `:${config.playPort}` : "";
  const playBase = `${proto}://${ep.playHostname}${portPart}`;

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
  contentType: ContentType
): ContentDeliveryUrls {
  if (!contentId) {
    return {
      primary: {} as PrimaryProtocolUrls,
      additional: {} as AdditionalProtocolUrls,
      embed: "",
      share: "",
    };
  }

  const ep = resolveEndpoints();
  const playProto = ep.playUseTls ? "https" : "http";
  const playPortPart = config.playPort ? `:${config.playPort}` : "";
  const playBase = `${playProto}://${ep.playHostname}${playPortPart}`;

  const secureSuffix = ep.edgeUseTls ? "s" : "";
  const wsProto = ep.edgeUseTls ? "wss" : "ws";

  // Primary protocols — HTTP via Foghorn (307 redirects), non-HTTP direct to edge
  const primary: PrimaryProtocolUrls = {
    play: `${playBase}/play/${contentId}`,
    hls: `${playBase}/play/${contentId}/hls/index.m3u8`,
    hlsCmaf: `${playBase}/play/${contentId}/cmaf/index.m3u8`,
    dash: `${playBase}/play/${contentId}/cmaf/index.mpd`,
    webrtc: `${playBase}/play/${contentId}/webrtc`,
    mp4: `${playBase}/play/${contentId}.mp4`,
    webm: `${playBase}/play/${contentId}.webm`,
    srt: `srt${secureSuffix}://${ep.edgeHostname}:${ep.srtPort}?streamid=${contentId}`,
  };

  // Additional protocols — HTTP via Foghorn, non-HTTP direct to edge
  const additional: AdditionalProtocolUrls = {
    rtsp: `rtsp${secureSuffix}://${ep.edgeHostname}:${config.edgePort || "554"}/play/${contentId}`,
    rtmp: `rtmp${secureSuffix}://${ep.edgeHostname}:${ep.rtmpPort}/play/${contentId}`,
    ts: `${playBase}/play/${contentId}.ts`,
    flv: `${playBase}/play/${contentId}.flv`,
    mkv: `${playBase}/play/${contentId}.mkv`,
    aac: `${playBase}/play/${contentId}.aac`,
    smoothStreaming: `${playBase}/play/${contentId}/cmaf/Manifest`,
    hds: `${playBase}/play/${contentId}/dynamic/manifest.f4m`,
    sdp: `${playBase}/play/${contentId}.sdp`,
    rawH264: `${playBase}/play/${contentId}.h264`,
    wsmp4: `${wsProto}://${ep.edgeHostname}:${config.edgePort || "8080"}/play/${contentId}.mp4`,
    wsWebrtc: `${wsProto}://${ep.edgeHostname}:${config.edgePort || "8080"}/play/webrtc/${contentId}`,
    dtsc: `dtsc${secureSuffix}://${ep.edgeHostname}:${config.edgePort || "4200"}/play/${contentId}`,
  };

  const embed = getEmbedCodeForContent(contentId, contentType);
  const share = getShareUrl(contentId);

  return { primary, additional, embed, share };
}

/**
 * Generate embed code snippet for a given content type
 */
function getEmbedCodeForContent(contentId: string, contentType: ContentType): string {
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
  const ep = resolveEndpoints();
  if (!ep.ingestHostname) return "";
  const proto = ep.ingestUseTls ? "rtmps" : "rtmp";
  return `${proto}://${ep.ingestHostname}:${ep.rtmpPort}${config.rtmpPath}`;
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

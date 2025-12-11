// Parse a URL and extract components for building protocol-specific URLs
interface ParsedStreamingUrl {
  hostname: string;
  port: string;
  useTls: boolean;
}

function parseStreamingUrl(url: string): ParsedStreamingUrl {
  try {
    const parsed = new URL(url);
    return {
      hostname: parsed.hostname,
      port: parsed.port,
      useTls: parsed.protocol === "https:",
    };
  } catch {
    // Fallback for malformed URLs
    return { hostname: "localhost", port: "", useTls: false };
  }
}

// Raw config from environment - these are base URLs that we parse to construct protocol-specific URLs
const rawConfig = {
  ingestUrl: import.meta.env.VITE_STREAMING_INGEST_URL || "http://localhost:8080",
  edgeUrl: import.meta.env.VITE_STREAMING_EDGE_URL || "http://localhost:8080",
  rtmpPort: import.meta.env.VITE_STREAMING_RTMP_PORT || "1935",
  srtPort: import.meta.env.VITE_STREAMING_SRT_PORT || "8889",
  rtmpPath: import.meta.env.VITE_STREAMING_RTMP_PATH || "/live",
  hlsPath: import.meta.env.VITE_STREAMING_HLS_PATH || "/hls",
  webrtcPath: import.meta.env.VITE_STREAMING_WEBRTC_PATH || "/webrtc",
  embedPath: import.meta.env.VITE_STREAMING_EMBED_PATH || "/",
  marketingSiteUrl: import.meta.env.VITE_MARKETING_SITE_URL || "http://localhost:18031",
  docsSiteUrl: import.meta.env.VITE_DOCS_SITE_URL || "http://localhost:18090/docs",
};

// Parsed URLs for deriving hostnames and TLS mode
const ingest = parseStreamingUrl(rawConfig.ingestUrl);
const edge = parseStreamingUrl(rawConfig.edgeUrl);

// Derived config that components can use
interface Config {
  // Ingest endpoints
  ingestHostname: string;
  ingestUseTls: boolean;
  rtmpPort: string;
  srtPort: string;
  rtmpPath: string;
  // Edge/delivery endpoints
  edgeHostname: string;
  edgePort: string;
  edgeUseTls: boolean;
  hlsPath: string;
  webrtcPath: string;
  embedPath: string;
  // Other
  marketingSiteUrl: string;
  docsSiteUrl: string;
}

const config: Config = {
  // Ingest
  ingestHostname: ingest.hostname,
  ingestUseTls: ingest.useTls,
  rtmpPort: rawConfig.rtmpPort,
  srtPort: rawConfig.srtPort,
  rtmpPath: rawConfig.rtmpPath,
  // Edge
  edgeHostname: edge.hostname,
  edgePort: edge.port,
  edgeUseTls: edge.useTls,
  hlsPath: rawConfig.hlsPath,
  webrtcPath: rawConfig.webrtcPath,
  embedPath: rawConfig.embedPath,
  // Other
  marketingSiteUrl: rawConfig.marketingSiteUrl,
  docsSiteUrl: rawConfig.docsSiteUrl,
};

// Determine if we're in development
const isDev = import.meta.env.DEV;

// Build full RTMP URL: rtmp(s)://hostname:port/path
function buildRtmpUrl(): string {
  const proto = config.ingestUseTls ? "rtmps" : "rtmp";
  return `${proto}://${config.ingestHostname}:${config.rtmpPort}${config.rtmpPath}`;
}

// Build full SRT URL: srt://hostname:port
function buildSrtBaseUrl(): string {
  return `srt://${config.ingestHostname}:${config.srtPort}`;
}

// Build HTTP(S) base URL for edge delivery
function buildEdgeBaseUrl(): string {
  const proto = config.edgeUseTls ? "https" : "http";
  const portPart = config.edgePort ? `:${config.edgePort}` : "";
  return `${proto}://${config.edgeHostname}${portPart}`;
}

// Build WHIP/WHEP base URL (same host as ingest, uses HTTP(S))
function buildWhipBaseUrl(): string {
  const proto = config.ingestUseTls ? "https" : "http";
  const parsed = parseStreamingUrl(rawConfig.ingestUrl);
  const portPart = parsed.port ? `:${parsed.port}` : "";
  return `${proto}://${config.ingestHostname}${portPart}`;
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

  const edgeBase = buildEdgeBaseUrl();

  return {
    hls: `${edgeBase}${config.hlsPath}/${playbackId}/index.m3u8`,
    webrtc: `${edgeBase}${config.webrtcPath}/${playbackId}`,
    webm: `${edgeBase}/${playbackId}.webm`,
    mkv: `${edgeBase}/${playbackId}.mkv`,
    mp4: `${edgeBase}/${playbackId}.mp4`,
    embed: `${edgeBase}${config.embedPath}/${playbackId}`,
  };
}

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
  // Return NPM package usage snippet instead of iframe
  return `import { Player } from '@livepeer-frameworks/player';
import '@livepeer-frameworks/player/player.css';

<Player
  contentType="live"
  contentId="${playbackId}"
  options={{
    gatewayUrl: '${rawConfig.edgeUrl}',
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

export { config, isDev };

interface Config {
  rtmpDomain: string;
  rtmpPath: string;
  httpDomain: string;
  cdnDomain: string;
  hlsPath: string;
  embedPath: string;
  webrtcPath: string;
  marketingSiteUrl: string;
}

interface IngestUrls {
  rtmp: string;
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

// Configuration with environment variable overrides
const config: Config = {
  // RTMP-specific configuration (typically port 1935)
  rtmpDomain: import.meta.env.VITE_RTMP_DOMAIN || "rtmp://localhost:1935",
  rtmpPath: import.meta.env.VITE_RTMP_PATH || "/live",

  // HTTP/HTTPS domains for WebRTC and delivery (typically port 8080, 443, etc.)
  httpDomain: import.meta.env.VITE_HTTP_DOMAIN || "http://localhost:8080",
  cdnDomain: import.meta.env.VITE_CDN_DOMAIN || "http://localhost:8080",

  // Paths
  hlsPath: import.meta.env.VITE_HLS_PATH || "/hls",
  embedPath: import.meta.env.VITE_EMBED_PATH || "/embed",
  webrtcPath: import.meta.env.VITE_WEBRTC_PATH || "/webrtc",

  marketingSiteUrl:
    import.meta.env.VITE_MARKETING_SITE_URL || "http://localhost:18031",
};

// Determine if we're in development
const isDev = import.meta.env.DEV;

export function getIngestUrls(streamKey: string): Partial<IngestUrls> {
  if (!streamKey) return {};

  return {
    rtmp: `${config.rtmpDomain}${config.rtmpPath}/${streamKey}`,
    whip: `${config.httpDomain}${config.webrtcPath}/${streamKey}`, // WebRTC-HTTP Ingestion Protocol
  };
}

export function getDeliveryUrls(playbackId: string): Partial<DeliveryUrls> {
  if (!playbackId) return {};

  return {
    hls: `${config.cdnDomain}${config.hlsPath}/${playbackId}/index.m3u8`,
    webrtc: `${config.cdnDomain}${config.webrtcPath}/${playbackId}`, // WHEP - WebRTC-HTTP Egress Protocol
    webm: `${config.cdnDomain}/${playbackId}.webm`,
    mkv: `${config.cdnDomain}/${playbackId}.mkv`,
    mp4: `${config.cdnDomain}/${playbackId}.mp4`,
    embed: `${config.cdnDomain}${config.embedPath}/${playbackId}`,
  };
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
  const urls = getDeliveryUrls(playbackId);
  return `<iframe src="${urls.embed || ""}" frameborder="0" allowfullscreen></iframe>`;
}

export function getMarketingSiteUrl(): string {
  return config.marketingSiteUrl;
}

export { config, isDev };

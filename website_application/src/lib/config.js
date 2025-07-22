import { browser } from '$app/environment';

// Configuration with environment variable overrides
const config = {
  // RTMP-specific configuration (typically port 1935)
  rtmpDomain: import.meta.env.VITE_RTMP_DOMAIN || 'rtmp://localhost:1935',
  rtmpPath: import.meta.env.VITE_RTMP_PATH || '/live',

  // HTTP/HTTPS domains for WebRTC and delivery (typically port 8080, 443, etc.)
  httpDomain: import.meta.env.VITE_HTTP_DOMAIN || 'http://localhost:9090/view',
  cdnDomain: import.meta.env.VITE_CDN_DOMAIN || 'http://localhost:9090/view',

  // Paths
  hlsPath: import.meta.env.VITE_HLS_PATH || '/hls',
  embedPath: import.meta.env.VITE_EMBED_PATH || '/embed',
  webrtcPath: import.meta.env.VITE_WEBRTC_PATH || '/webrtc',

  marketingSiteUrl: import.meta.env.VITE_MARKETING_SITE_URL || 'http://localhost:9004'
};

// Determine if we're in development
const isDev = import.meta.env.DEV;

/**
 * Get all ingest URLs for a stream
 * @param {string} streamKey
 * @returns {{ [key: string]: string }}
 */
export function getIngestUrls(streamKey) {
  if (!streamKey) return {};

  return {
    rtmp: `${config.rtmpDomain}${config.rtmpPath}/${streamKey}`,
    whip: `${config.httpDomain}${config.webrtcPath}/${streamKey}` // WebRTC-HTTP Ingestion Protocol
  };
}

/**
 * Get all delivery/playback URLs for a stream
 * @param {string} playbackId
 * @returns {{ [key: string]: string }}
 */
export function getDeliveryUrls(playbackId) {
  if (!playbackId) return {};

  return {
    hls: `${config.cdnDomain}${config.hlsPath}/${playbackId}/index.m3u8`,
    webrtc: `${config.cdnDomain}${config.webrtcPath}/${playbackId}`, // WHEP - WebRTC-HTTP Egress Protocol
    webm: `${config.cdnDomain}/${playbackId}.webm`,
    mkv: `${config.cdnDomain}/${playbackId}.mkv`,
    mp4: `${config.cdnDomain}/${playbackId}.mp4`,
    embed: `${config.cdnDomain}${config.embedPath}/${playbackId}`
  };
}

/**
 * @param {string} streamKey
 */
export function getIngestUrl(streamKey) {
  const urls = getIngestUrls(streamKey);
  return urls.rtmp || '';
}

/**
 * @param {string} playbackId
 */
export function getPlaybackUrl(playbackId) {
  const urls = getDeliveryUrls(playbackId);
  return urls.hls || '';
}

/**
 * @param {string} playbackId
 */
export function getEmbedCode(playbackId) {
  if (!playbackId) return '';
  const urls = getDeliveryUrls(playbackId);
  return `<iframe src="${urls.embed || ''}" frameborder="0" allowfullscreen></iframe>`;
}

export function getMarketingSiteUrl() {
  return config.marketingSiteUrl;
}

export { config, isDev }; 
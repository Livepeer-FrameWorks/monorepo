import type { ContentEndpoints, EndpointInfo } from "../types";

export const viewerProtocols = {
  WEBRTC: "webrtc",
  WHEP: "whep",
  HLS: "hls",
  DASH: "dash",
  HLS_CMAF: "cmaf",
  MEWS: "wsmp4",
  MEWS_WEBM: "mews_webm",
  MP4: "mp4",
  WEBM: "webm",
  MKV: "mkv",
  TS: "ts",
  AAC: "aac",
  H264: "h264",
  H264_WS: "h264_ws",
  RAW_WS: "raw_ws",
  JSON_WS: "json_ws",
  FLV: "flv",
  HDS: "hds",
  SMOOTHSTREAMING: "smoothstreaming",
  SDP: "sdp",
  MIST_HTML: "mist_html",
  RTMP: "rtmp",
  RTSP: "rtsp",
  SRT: "srt",
  DTSC: "dtsc",
} as const;

export type ViewerProtocol = keyof typeof viewerProtocols;
const canonicalProtocols = new Set<string>(Object.values(viewerProtocols));

export function canonicalViewerProtocol(value: unknown): string | undefined {
  if (typeof value !== "string") return undefined;
  if (value.toUpperCase() === "MIST_WEBRTC") return "webrtc";
  return (
    viewerProtocols[value.toUpperCase() as ViewerProtocol] ??
    (canonicalProtocols.has(value) ? value : undefined)
  );
}

export function requireViewerProtocol(
  endpoints: ContentEndpoints,
  protocol: ViewerProtocol
): ContentEndpoints {
  const canonical = viewerProtocols[protocol];
  const primary = endpoints.primary;
  if (
    !canonical ||
    canonicalViewerProtocol(primary.protocol) !== canonical ||
    !primary.nodeId ||
    !validViewerURL(primary.url, canonical)
  ) {
    throw new Error("Gateway did not return the requested playback format");
  }
  const outputs: NonNullable<EndpointInfo["outputs"]> = {};
  for (const [key, output] of Object.entries(primary.outputs ?? {})) {
    const outputProtocol = canonicalViewerProtocol(key);
    const compatible =
      outputProtocol === canonical || (canonical === "mkv" && outputProtocol === "webm");
    if (compatible && output?.url === primary.url) outputs[key] = output;
  }
  if (!Object.keys(outputs).length) {
    throw new Error("Gateway did not return the requested playback output");
  }
  return { ...endpoints, primary: { ...primary, outputs }, fallbacks: [] };
}

function validViewerURL(value: string | undefined, protocol: string): boolean {
  if (!value) return false;
  try {
    const url = new URL(value);
    if (!url.hostname || url.username || url.password || url.hash) return false;
    if (["webrtc", "wsmp4", "mews_webm", "h264_ws", "raw_ws", "json_ws"].includes(protocol)) {
      return url.protocol === "ws:" || url.protocol === "wss:";
    }
    if (protocol === "rtmp") return url.protocol === "rtmp:" || url.protocol === "rtmps:";
    if (["rtsp", "srt", "dtsc"].includes(protocol)) return url.protocol === `${protocol}:`;
    return url.protocol === "http:" || url.protocol === "https:";
  } catch {
    return false;
  }
}

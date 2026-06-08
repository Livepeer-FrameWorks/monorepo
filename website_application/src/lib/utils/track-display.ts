export interface TrackLike {
  trackName?: string | null;
  track_name?: string | null;
  name?: string | null;
  trackType?: string | null;
  track_type?: string | null;
  type?: string | null;
  codec?: string | null;
  resolution?: string | null;
  width?: number | null;
  height?: number | null;
  fps?: number | null;
  fpks?: number | null;
  bitrateKbps?: number | null;
  bitrate_kbps?: number | null;
  bitrateBps?: number | null;
  bitrate_bps?: number | null;
  bps?: number | null;
  kbits?: number | null;
  channels?: number | null;
  sampleRate?: number | null;
  sample_rate?: number | null;
  rate?: number | null;
}

const VIDEO_CODECS = new Set(["H264", "H265", "HEVC", "AV1", "VP8", "VP9"]);
const AUDIO_CODECS = new Set(["AAC", "OPUS", "MP3", "VORBIS", "AC3", "EAC3"]);
const GENERATED_CODECS = new Set(["JPEG", "JPG", "MJPEG", "THUMBVTT", "VTT"]);

export type TrackDisplayKind = "video" | "audio" | "generated" | "metadata" | "unknown";

function firstString(...values: Array<string | null | undefined>): string | null {
  for (const value of values) {
    const trimmed = value?.trim();
    if (trimmed) return trimmed;
  }
  return null;
}

function firstNumber(...values: Array<number | null | undefined>): number | null {
  for (const value of values) {
    if (typeof value === "number" && Number.isFinite(value) && value > 0) return value;
  }
  return null;
}

export function trackName(track: TrackLike | null | undefined): string {
  return firstString(track?.trackName, track?.track_name, track?.name) ?? "";
}

export function trackType(track: TrackLike | null | undefined): string {
  return firstString(track?.trackType, track?.track_type, track?.type)?.toLowerCase() ?? "";
}

export function trackCodec(track: TrackLike | null | undefined): string {
  return firstString(track?.codec)?.toUpperCase() ?? "";
}

export function trackWidth(track: TrackLike | null | undefined): number | null {
  return firstNumber(track?.width);
}

export function trackHeight(track: TrackLike | null | undefined): number | null {
  return firstNumber(track?.height);
}

export function trackFps(track: TrackLike | null | undefined): number | null {
  const fps = firstNumber(track?.fps);
  if (fps !== null) return fps;
  const fpks = firstNumber(track?.fpks);
  return fpks === null ? null : fpks / 1000;
}

export function trackBitrateKbps(track: TrackLike | null | undefined): number | null {
  const kbps = firstNumber(track?.bitrateKbps, track?.bitrate_kbps, track?.kbits);
  if (kbps !== null) return Math.round(kbps);
  const bps = firstNumber(track?.bitrateBps, track?.bitrate_bps, track?.bps);
  return bps === null ? null : Math.round(bps / 1000);
}

export function trackResolution(track: TrackLike | null | undefined): string | null {
  const explicit = firstString(track?.resolution);
  if (explicit) return explicit;
  const width = trackWidth(track);
  const height = trackHeight(track);
  return width && height ? `${width}x${height}` : null;
}

export function classifyTrack(track: TrackLike | null | undefined): TrackDisplayKind {
  const name = trackName(track).toLowerCase();
  const type = trackType(track);
  const codec = trackCodec(track);

  if (
    GENERATED_CODECS.has(codec) ||
    name.includes("thumb") ||
    name.includes("sprite") ||
    name.includes("jpeg") ||
    name.includes("jpg")
  ) {
    return "generated";
  }
  if (type === "audio" || AUDIO_CODECS.has(codec) || name.includes("audio")) return "audio";
  if (
    type === "video" ||
    VIDEO_CODECS.has(codec) ||
    name.includes("video") ||
    (trackWidth(track) !== null && trackHeight(track) !== null)
  ) {
    return "video";
  }
  if (type === "meta" || type === "metadata" || codec === "JSON") return "metadata";
  return "unknown";
}

export function trackKindLabel(track: TrackLike | null | undefined): string {
  switch (classifyTrack(track)) {
    case "video":
      return "Video";
    case "audio":
      return "Audio";
    case "generated":
      return "Generated";
    case "metadata":
      return "Metadata";
    default:
      return "Track";
  }
}

export function trackDisplayName(track: TrackLike | null | undefined, index: number): string {
  const explicit = trackName(track);
  if (explicit) return explicit;

  switch (classifyTrack(track)) {
    case "video":
      return `video${index}`;
    case "audio":
      return `audio${index}`;
    case "generated": {
      const codec = trackCodec(track);
      return codec === "THUMBVTT" || codec === "VTT" ? "Thumbnail cues" : "Generated output";
    }
    case "metadata":
      return `metadata${index}`;
    default:
      return `track${index}`;
  }
}

export function shouldShowTrackBitrate(track: TrackLike | null | undefined): boolean {
  const bitrate = trackBitrateKbps(track);
  if (bitrate === null) return false;
  const kind = classifyTrack(track);
  if (kind === "generated" || kind === "metadata") return false;
  if (kind === "video") return bitrate >= 50;
  return true;
}

export function formatTrackBitrate(track: TrackLike | null | undefined): string | null {
  const kbps = trackBitrateKbps(track);
  if (kbps === null || !shouldShowTrackBitrate(track)) return null;
  return kbps >= 1000 ? `${(kbps / 1000).toFixed(1)} Mbps` : `${kbps} kbps`;
}

import type { StreamTrack } from "./PlayerInterface";

export interface MistQualityTrackInput {
  type?: string;
  codec?: string;
  lang?: string;
  width?: number;
  height?: number;
  bps?: number;
  bitrate?: number;
  fpks?: number;
  fps?: number;
  idx?: number;
}

export interface MistQualityLevel {
  id: string;
  label: string;
  width?: number;
  height?: number;
  bitrate?: number;
}

export function buildQualityLevelsFromMistTracks(
  tracks?: Record<string, MistQualityTrackInput>,
  opts: { hideAuxiliaryWhenRealVideo?: boolean } = {}
): MistQualityLevel[] {
  if (!tracks) return [];
  const hideAuxiliaryWhenRealVideo = opts.hideAuxiliaryWhenRealVideo ?? true;
  const videoTracks = Object.entries(tracks).filter(([, t]) => t.type === "video");
  const hasRealVideo = videoTracks.some(([, t]) => isRealSelectableVideoTrack(t));

  return videoTracks
    .filter(([, t]) => {
      if (!hideAuxiliaryWhenRealVideo || !hasRealVideo) return true;
      return isRealSelectableVideoTrack(t);
    })
    .map(([id, t]) => buildQualityLevel(id, t))
    .sort(sortQualityLevels);
}

export function buildQualityLevelsFromStreamTracks(
  tracks?: Array<StreamTrack | MistQualityTrackInput>,
  opts: { hideAuxiliaryWhenRealVideo?: boolean } = {}
): MistQualityLevel[] {
  if (!tracks) return [];
  const byId: Record<string, MistQualityTrackInput> = {};
  for (const track of tracks) {
    if (track.type !== "video") continue;
    const id = track.idx !== undefined ? String(track.idx) : undefined;
    if (!id) continue;
    byId[id] = track;
  }
  return buildQualityLevelsFromMistTracks(byId, opts);
}

function buildQualityLevel(id: string, track: MistQualityTrackInput): MistQualityLevel {
  return {
    id,
    label: formatMistQualityLabel(track),
    width: track.width,
    height: track.height,
    bitrate: track.bitrate ?? track.bps,
  };
}

function sortQualityLevels(a: MistQualityLevel, b: MistQualityLevel): number {
  const heightDelta = (b.height || 0) - (a.height || 0);
  if (heightDelta !== 0) return heightDelta;
  return (b.bitrate || 0) - (a.bitrate || 0);
}

function formatMistQualityLabel(track: MistQualityTrackInput): string {
  const resolution =
    track.width && track.height
      ? `${track.width}x${track.height}`
      : track.height
        ? `${track.height}p`
        : "";
  const fps = getTrackFps(track);
  const fpsLabel = fps ? `${formatFps(fps)}fps` : "";
  const bitrate = track.bitrate ?? track.bps;
  const bitrateLabel = formatBitrate(bitrate);
  const resolutionAndFps =
    resolution && fpsLabel ? `${resolution}@${fpsLabel}` : resolution || fpsLabel;
  const parts = [resolutionAndFps, bitrateLabel].filter(Boolean);
  if (parts.length) return parts.join(" ");
  return track.codec ?? String(track.idx ?? "Unknown");
}

function getTrackFps(track: MistQualityTrackInput): number | undefined {
  if (typeof track.fps === "number" && Number.isFinite(track.fps) && track.fps > 0) {
    return track.fps;
  }
  if (typeof track.fpks === "number" && Number.isFinite(track.fpks) && track.fpks > 0) {
    return track.fpks / 1000;
  }
  return undefined;
}

function formatFps(fps: number): string {
  return Number.isInteger(fps) ? String(fps) : fps.toFixed(2).replace(/\.?0+$/, "");
}

function formatBitrate(bitrate: number | undefined): string {
  if (!bitrate || !Number.isFinite(bitrate)) return "";
  return bitrate >= 1_000_000
    ? `${(bitrate / 1_000_000).toFixed(1)} Mbps`
    : `${Math.round(bitrate / 1000)} kbps`;
}

function isRealSelectableVideoTrack(track: MistQualityTrackInput): boolean {
  const codec = String(track.codec ?? "").toLowerCase();
  const lang = String(track.lang ?? "").toLowerCase();
  if (codec === "jpeg" || codec === "jpg" || codec === "mjpeg" || codec === "image/jpeg") {
    return false;
  }
  if (lang === "pre" || lang === "preview" || lang === "thumb" || lang === "thumbnails") {
    return false;
  }
  return true;
}

import React from "react";
import {
  cn,
  type ContentMetadata,
  type PlaybackQuality,
  type StreamState,
} from "@livepeer-frameworks/player-core";

interface StatsPanelProps {
  isOpen: boolean;
  onClose: () => void;
  metadata?: ContentMetadata | null;
  streamState?: StreamState | null;
  quality?: PlaybackQuality | null;
  videoElement?: HTMLVideoElement | null;
  protocol?: string;
  nodeId?: string;
  geoDistance?: number;
}

/**
 * "Stats for nerds" panel showing detailed playback information.
 * Toggleable overlay with technical details about the stream.
 */
const StatsPanel: React.FC<StatsPanelProps> = ({
  isOpen,
  onClose,
  metadata,
  streamState,
  quality,
  videoElement,
  protocol,
  nodeId,
  geoDistance,
}) => {
  if (!isOpen) return null;

  // Video element stats
  const video = videoElement;
  const currentRes = video ? `${video.videoWidth}x${video.videoHeight}` : "—";
  const buffered =
    video && video.buffered.length > 0
      ? (video.buffered.end(video.buffered.length - 1) - video.currentTime).toFixed(1)
      : "—";
  const playbackRate = video?.playbackRate?.toFixed(2) ?? "1.00";

  // Quality monitor stats
  const qualityScore = quality?.score?.toFixed(0) ?? "—";
  const bitrateKbps = quality?.bitrate ? `${(quality.bitrate / 1000).toFixed(0)} kbps` : "—";
  const frameDropRate = quality?.frameDropRate?.toFixed(1) ?? "—";
  const stallCount = quality?.stallCount ?? 0;
  const latency = quality?.latency ? `${Math.round(quality.latency)} ms` : "—";

  // Stream state stats
  const viewers = metadata?.viewers ?? "—";
  const streamStatus = streamState?.status ?? metadata?.status ?? "—";

  const mistInfo = metadata?.mist ?? streamState?.streamInfo;

  const deriveTracksFromMist = () => {
    const mistTracks = mistInfo?.meta?.tracks;
    if (!mistTracks) return undefined;
    return Object.values(mistTracks).map((t) => ({
      type: t.type,
      codec: t.codec,
      width: t.width,
      height: t.height,
      bitrate: typeof t.bps === "number" ? Math.round(t.bps) : undefined,
      fps: typeof t.fpks === "number" ? t.fpks / 1000 : undefined,
      channels: t.channels,
      sampleRate: t.rate,
    }));
  };

  // Format track info from metadata
  const formatTracks = () => {
    const tracks = metadata?.tracks ?? deriveTracksFromMist();
    if (!tracks?.length) return "—";
    return tracks
      .map((t) => {
        if (t.type === "video") {
          const resolution = t.width && t.height ? `${t.width}x${t.height}` : "?";
          const bitrate = t.bitrate ? `${Math.round(t.bitrate / 1000)}kbps` : "?";
          return `${t.codec ?? "?"} ${resolution}@${bitrate}`;
        }
        const channels = t.channels ? `${t.channels}ch` : "?";
        return `${t.codec ?? "?"} ${channels}`;
      })
      .join(", ");
  };

  const mistType = mistInfo?.type ?? "—";
  const mistBufferWindow = mistInfo?.meta?.buffer_window;
  const mistLastMs = mistInfo?.lastms;
  const mistUnixOffset = mistInfo?.unixoffset;

  const stats = [
    { label: "Resolution", value: currentRes },
    { label: "Buffer", value: `${buffered}s` },
    { label: "Latency", value: latency },
    { label: "Bitrate", value: bitrateKbps },
    { label: "Quality Score", value: `${qualityScore}/100` },
    { label: "Frame Drop Rate", value: `${frameDropRate}%` },
    { label: "Stalls", value: String(stallCount) },
    { label: "Playback Rate", value: `${playbackRate}x` },
    { label: "Protocol", value: protocol ?? "—" },
    { label: "Node", value: nodeId ?? "—" },
    { label: "Geo Distance", value: geoDistance ? `${geoDistance.toFixed(0)} km` : "—" },
    { label: "Viewers", value: String(viewers) },
    { label: "Status", value: streamStatus },
    { label: "Tracks", value: formatTracks() },
    { label: "Mist Type", value: mistType },
    {
      label: "Mist Buffer Window",
      value: mistBufferWindow != null ? String(mistBufferWindow) : "—",
    },
    { label: "Mist Lastms", value: mistLastMs != null ? String(mistLastMs) : "—" },
    { label: "Mist Unixoffset", value: mistUnixOffset != null ? String(mistUnixOffset) : "—" },
  ];

  // Add metadata fields if available
  if (metadata?.title) {
    stats.unshift({ label: "Title", value: metadata.title });
  }
  if (metadata?.durationSeconds) {
    const mins = Math.floor(metadata.durationSeconds / 60);
    const secs = metadata.durationSeconds % 60;
    stats.push({ label: "Duration", value: `${mins}:${String(secs).padStart(2, "0")}` });
  }
  if (metadata?.recordingSizeBytes) {
    const mb = (metadata.recordingSizeBytes / (1024 * 1024)).toFixed(1);
    stats.push({ label: "Size", value: `${mb} MB` });
  }

  return (
    <div
      className={cn(
        "fw-stats-panel absolute top-2 right-2 z-30",
        "bg-black border border-white/10 rounded",
        "text-white text-xs font-mono",
        "max-w-[320px] max-h-[80%] overflow-auto",
        "shadow-lg"
      )}
      style={{ backgroundColor: "#000000" }} // Inline fallback for opaque background
    >
      {/* Header */}
      <div className="flex items-center justify-between px-3 py-2 border-b border-white/10">
        <span className="text-white/70 text-[10px] uppercase tracking-wider">Stats Overlay</span>
        <button
          type="button"
          onClick={onClose}
          className="text-white/50 hover:text-white transition-colors p-1 -mr-1"
          aria-label="Close stats panel"
        >
          <svg
            width="12"
            height="12"
            viewBox="0 0 12 12"
            fill="none"
            stroke="currentColor"
            strokeWidth="1.5"
          >
            <path d="M2 2l8 8M10 2l-8 8" />
          </svg>
        </button>
      </div>

      {/* Stats grid */}
      <div className="px-3 py-2 space-y-1">
        {stats.map(({ label, value }) => (
          <div key={label} className="flex justify-between gap-4">
            <span className="text-white/50 shrink-0">{label}</span>
            <span className="text-white/90 truncate text-right">{value}</span>
          </div>
        ))}
      </div>
    </div>
  );
};

export default StatsPanel;

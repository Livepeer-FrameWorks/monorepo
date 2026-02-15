/**
 * <fw-stats-panel> — Stats for nerds overlay aligned with wrapper diagnostics.
 */
import { LitElement, html, css } from "lit";
import { customElement, property } from "lit/decorators.js";
import { sharedStyles } from "../styles/shared-styles.js";
import { utilityStyles } from "../styles/utility-styles.js";
import { closeIcon } from "../icons/index.js";
import type { PlayerControllerHost } from "../controllers/player-controller-host.js";

interface StatRow {
  label: string;
  value: string;
}

@customElement("fw-stats-panel")
export class FwStatsPanel extends LitElement {
  @property({ attribute: false }) pc!: PlayerControllerHost;

  static styles = [
    sharedStyles,
    utilityStyles,
    css`
      :host {
        display: contents;
      }
      .panel {
        position: absolute;
        top: 0.5rem;
        right: 0.5rem;
        z-index: 30;
        width: 18rem;
        max-height: 80%;
        overflow-y: auto;
        background: hsl(var(--tn-bg-dark) / 0.9);
        backdrop-filter: blur(4px);
        border: 1px solid hsl(var(--tn-fg-gutter) / 0.3);
        font-family: ui-monospace, monospace;
        font-size: 0.75rem;
        color: hsl(var(--tn-fg));
      }
      .header {
        display: flex;
        align-items: center;
        justify-content: space-between;
        padding: 0.5rem;
        border-bottom: 1px solid hsl(var(--tn-fg-gutter) / 0.3);
      }
      .title {
        font-size: 10px;
        font-weight: 600;
        text-transform: uppercase;
        letter-spacing: 0.05em;
        color: hsl(var(--tn-fg-dark));
      }
      .close {
        display: flex;
        align-items: center;
        justify-content: center;
        width: 1.5rem;
        height: 1.5rem;
        border: none;
        background: transparent;
        color: hsl(var(--tn-fg-dark));
        cursor: pointer;
      }
      .close:hover {
        color: hsl(var(--tn-fg));
      }
      .rows {
        padding: 0.5rem;
      }
      .row {
        display: flex;
        justify-content: space-between;
        gap: 0.5rem;
        padding: 0.125rem 0;
      }
      .label {
        color: hsl(var(--tn-fg-dark));
      }
      .value {
        color: hsl(var(--tn-fg));
        text-align: right;
        word-break: break-word;
      }
    `,
  ];

  private _deriveTracksFromMist(mistInfo: any) {
    const mistTracks = mistInfo?.meta?.tracks;
    if (!mistTracks) return undefined;
    return Object.values(mistTracks as Record<string, any>).map((track: any) => ({
      type: track.type,
      codec: track.codec,
      width: track.width,
      height: track.height,
      bitrate: typeof track.bps === "number" ? Math.round(track.bps) : undefined,
      fps: typeof track.fpks === "number" ? track.fpks / 1000 : undefined,
      channels: track.channels,
      sampleRate: track.rate,
    }));
  }

  private _formatTracks(metadata: any, mistInfo: any): string {
    const tracks = metadata?.tracks ?? this._deriveTracksFromMist(mistInfo);
    if (!tracks?.length) return "—";
    return tracks
      .map((track: any) => {
        if (track.type === "video") {
          const resolution = track.width && track.height ? `${track.width}x${track.height}` : "?";
          const bitrate = track.bitrate ? `${Math.round(track.bitrate / 1000)}kbps` : "?";
          return `${track.codec ?? "?"} ${resolution}@${bitrate}`;
        }
        const channels = track.channels ? `${track.channels}ch` : "?";
        return `${track.codec ?? "?"} ${channels}`;
      })
      .join(", ");
  }

  private _collectStats(): StatRow[] {
    const s = this.pc.s;
    const video = s.videoElement;
    const quality = s.playbackQuality;
    const metadata = s.metadata;
    const streamState = s.streamState;
    const primaryEndpoint = s.endpoints?.primary as
      | { protocol?: string; nodeId?: string; geoDistance?: number }
      | undefined;

    const currentRes = video ? `${video.videoWidth}x${video.videoHeight}` : "—";
    const buffered =
      video && video.buffered.length > 0
        ? (video.buffered.end(video.buffered.length - 1) - video.currentTime).toFixed(1)
        : "—";
    const playbackRate = video?.playbackRate?.toFixed(2) ?? "1.00";
    const qualityScore = quality?.score?.toFixed(0) ?? "—";
    const bitrateKbps = quality?.bitrate ? `${(quality.bitrate / 1000).toFixed(0)} kbps` : "—";
    const frameDropRate = quality?.frameDropRate?.toFixed(1) ?? "—";
    const stallCount = quality?.stallCount ?? 0;
    const latency = quality?.latency ? `${Math.round(quality.latency)} ms` : "—";
    const viewers = metadata?.viewers ?? "—";
    const streamStatus = streamState?.status ?? metadata?.status ?? "—";
    const mistInfo = metadata?.mist ?? streamState?.streamInfo;
    const mistType = mistInfo?.type ?? "—";
    const mistBufferWindow = mistInfo?.meta?.buffer_window;
    const mistLastMs = mistInfo?.lastms;
    const mistUnixOffset = mistInfo?.unixoffset;

    const stats: StatRow[] = [
      { label: "Resolution", value: currentRes },
      { label: "Buffer", value: `${buffered}s` },
      { label: "Latency", value: latency },
      { label: "Bitrate", value: bitrateKbps },
      { label: "Quality Score", value: `${qualityScore}/100` },
      { label: "Frame Drop Rate", value: `${frameDropRate}%` },
      { label: "Stalls", value: String(stallCount) },
      { label: "Playback Rate", value: `${playbackRate}x` },
      { label: "Protocol", value: primaryEndpoint?.protocol ?? "—" },
      { label: "Node", value: primaryEndpoint?.nodeId ?? "—" },
      {
        label: "Geo Distance",
        value: primaryEndpoint?.geoDistance ? `${primaryEndpoint.geoDistance.toFixed(0)} km` : "—",
      },
      { label: "Viewers", value: String(viewers) },
      { label: "Status", value: streamStatus },
      { label: "Tracks", value: this._formatTracks(metadata, mistInfo) },
      { label: "Mist Type", value: mistType },
      {
        label: "Mist Buffer Window",
        value: mistBufferWindow != null ? String(mistBufferWindow) : "—",
      },
      { label: "Mist Lastms", value: mistLastMs != null ? String(mistLastMs) : "—" },
      { label: "Mist Unixoffset", value: mistUnixOffset != null ? String(mistUnixOffset) : "—" },
    ];

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

    return stats;
  }

  protected render() {
    const stats = this._collectStats();

    return html`
      <div class="panel fw-stats-panel">
        <div class="header fw-stats-header">
          <span class="title">Stats Overlay</span>
          <button
            class="close"
            @click=${() =>
              this.dispatchEvent(new CustomEvent("fw-close", { bubbles: true, composed: true }))}
            aria-label="Close stats panel"
          >
            ${closeIcon()}
          </button>
        </div>
        <div class="rows">
          ${stats.map(
            (stat) =>
              html`<div class="row fw-stats-row">
                <span class="label">${stat.label}</span>
                <span class="value fw-stats-value">${stat.value}</span>
              </div>`
          )}
        </div>
      </div>
    `;
  }
}

declare global {
  interface HTMLElementTagNameMap {
    "fw-stats-panel": FwStatsPanel;
  }
}

/**
 * <fw-dev-mode-panel> — Developer mode side panel.
 * Feature parity with React/Svelte advanced panel.
 */
import { LitElement, html, css, nothing, type PropertyValues } from "lit";
import { customElement, property, state } from "lit/decorators.js";
import { classMap } from "lit/directives/class-map.js";
import { sharedStyles } from "../styles/shared-styles.js";
import { utilityStyles } from "../styles/utility-styles.js";
import { closeIcon } from "../icons/index.js";
import {
  QualityMonitor,
  globalPlayerManager,
  type MistStreamInfo,
  type PlaybackMode,
  type PlayerCombination,
  type StreamInfo,
} from "@livepeer-frameworks/player-core";
import type { PlayerControllerHost } from "../controllers/player-controller-host.js";

const SOURCE_TYPE_LABELS: Record<string, string> = {
  "html5/application/vnd.apple.mpegurl": "HLS",
  "dash/video/mp4": "DASH",
  "html5/video/mp4": "MP4",
  "html5/video/webm": "WebM",
  whep: "WHEP",
  "mist/html": "Mist",
  "mist/legacy": "Auto",
  "ws/video/mp4": "MEWS",
};

@customElement("fw-dev-mode-panel")
export class FwDevModePanel extends LitElement {
  @property({ attribute: false }) pc!: PlayerControllerHost;
  @property({ type: String }) playbackMode: PlaybackMode = "auto";

  @state() private _activeTab: "config" | "stats" = "config";
  @state() private _hoveredComboIndex: number | null = null;
  @state() private _tooltipPos: { top: number; left: number } | null = null;
  @state() private _showDisabledPlayers = false;

  @state() private _playbackScore = 1;
  @state() private _qualityScore = 100;
  @state() private _stallCount = 0;
  @state() private _frameDropRate = 0;

  @state()
  private _videoStats: {
    resolution: string;
    buffered: string;
    playbackRate: string;
    currentTime: string;
    duration: string;
    readyState: number;
    networkState: number;
  } | null = null;

  @state() private _playerStats: unknown = null;

  private _qualityMonitor: QualityMonitor | null = null;
  private _qualityMonitorVideo: HTMLVideoElement | null = null;
  private _videoStatsInterval: ReturnType<typeof setInterval> | null = null;
  private _playerStatsInterval: ReturnType<typeof setInterval> | null = null;

  static styles = [
    sharedStyles,
    utilityStyles,
    css`
      :host {
        display: block;
        height: 100%;
        min-height: 0;
      }
    `,
  ];

  disconnectedCallback(): void {
    super.disconnectedCallback();
    this._stopQualityMonitor();
    this._stopStatsPolling();
  }

  protected updated(_changed: PropertyValues<this>): void {
    this._syncQualityMonitor();
    this._syncStatsPolling();
  }

  private _getMistStreamInfo(): MistStreamInfo | undefined {
    return this.pc.s.streamState?.streamInfo as MistStreamInfo | undefined;
  }

  private _getAllCombinations(): PlayerCombination[] {
    const streamInfo = this.pc.s.streamInfo as StreamInfo | null;
    if (!streamInfo) {
      return [];
    }

    try {
      return globalPlayerManager.getAllCombinations(streamInfo, this.playbackMode);
    } catch {
      return [];
    }
  }

  private _getCompatibleCombinations(): PlayerCombination[] {
    return this._getAllCombinations().filter((combo) => combo.compatible);
  }

  private _getActiveComboIndex(combinations: PlayerCombination[]): number {
    const currentPlayer = this.pc.s.currentPlayerInfo;
    const currentSource = this.pc.s.currentSourceInfo;

    if (!currentPlayer || !currentSource || combinations.length === 0) {
      return -1;
    }

    return combinations.findIndex(
      (combo) => combo.player === currentPlayer.shortname && combo.sourceType === currentSource.type
    );
  }

  private _syncQualityMonitor(): void {
    const video = this.pc.s.videoElement;

    if (!video) {
      this._stopQualityMonitor();
      return;
    }

    if (!this._qualityMonitor) {
      this._qualityMonitor = new QualityMonitor({
        sampleInterval: 500,
        onSample: (quality) => {
          this._qualityScore = quality.score;
          this._stallCount = quality.stallCount;
          this._frameDropRate = quality.frameDropRate;
          this._playbackScore = this._qualityMonitor?.getPlaybackScore() ?? 1;
          this.requestUpdate();
        },
      });
    }

    if (this._qualityMonitorVideo !== video) {
      this._qualityMonitor.stop();
      this._qualityMonitor.start(video);
      this._qualityMonitorVideo = video;
    }
  }

  private _stopQualityMonitor(): void {
    this._qualityMonitor?.stop();
    this._qualityMonitorVideo = null;
  }

  private _syncStatsPolling(): void {
    if (this._activeTab !== "stats") {
      this._stopStatsPolling();
      return;
    }

    if (!this._videoStatsInterval) {
      this._updateVideoStats();
      this._videoStatsInterval = setInterval(() => {
        this._updateVideoStats();
      }, 500);
    }

    if (!this._playerStatsInterval) {
      void this._pollPlayerStats();
      this._playerStatsInterval = setInterval(() => {
        void this._pollPlayerStats();
      }, 500);
    }
  }

  private _stopStatsPolling(): void {
    if (this._videoStatsInterval) {
      clearInterval(this._videoStatsInterval);
      this._videoStatsInterval = null;
    }

    if (this._playerStatsInterval) {
      clearInterval(this._playerStatsInterval);
      this._playerStatsInterval = null;
    }
  }

  private _updateVideoStats(): void {
    const video = this.pc.s.videoElement;
    if (!video) {
      this._videoStats = null;
      return;
    }

    this._videoStats = {
      resolution: `${video.videoWidth}x${video.videoHeight}`,
      buffered:
        video.buffered.length > 0
          ? (video.buffered.end(video.buffered.length - 1) - video.currentTime).toFixed(1)
          : "0",
      playbackRate: video.playbackRate.toFixed(2),
      currentTime: video.currentTime.toFixed(1),
      duration: Number.isFinite(video.duration) ? video.duration.toFixed(1) : "live",
      readyState: video.readyState,
      networkState: video.networkState,
    };
  }

  private async _pollPlayerStats(): Promise<void> {
    try {
      const stats = await this.pc.getStats();
      if (stats) {
        this._playerStats = stats;
      }
    } catch {
      // No-op for optional stats backends.
    }
  }

  private _handleComboMouseEnter(index: number, event: MouseEvent): void {
    this._hoveredComboIndex = index;
    const row = event.currentTarget as HTMLElement;
    const rowRect = row.getBoundingClientRect();
    this._tooltipPos = {
      top: Math.max(8, Math.min(rowRect.top, window.innerHeight - 200)),
      left: Math.max(8, rowRect.left - 228),
    };
  }

  private _handleModeChange(mode: "auto" | "low-latency" | "quality"): void {
    this.playbackMode = mode;
    void this.pc.setDevModeOptions({ playbackMode: mode });
    this.dispatchEvent(
      new CustomEvent("fw-playback-mode-change", {
        detail: { mode },
        bubbles: true,
        composed: true,
      })
    );
  }

  private _handleReload(): void {
    this.pc.clearError();
    void this.pc.reload();
  }

  private _handleNextCombo(): void {
    const compatible = this._getCompatibleCombinations();
    if (compatible.length === 0) {
      return;
    }

    const activeCompatibleIndex = this._getActiveComboIndex(compatible);
    const startIndex = activeCompatibleIndex >= 0 ? activeCompatibleIndex : -1;
    const nextIndex = (startIndex + 1) % compatible.length;
    const next = compatible[nextIndex];

    void this.pc.setDevModeOptions({
      forcePlayer: next.player,
      forceType: next.sourceType,
      forceSource: next.sourceIndex,
    });
  }

  private _handleSelectCombo(index: number): void {
    const allCombinations = this._getAllCombinations();
    const combo = allCombinations[index];
    if (!combo) {
      return;
    }

    void this.pc.setDevModeOptions({
      forcePlayer: combo.player,
      forceType: combo.sourceType,
      forceSource: combo.sourceIndex,
    });
  }

  private _renderStatsTab(): unknown {
    const primaryEndpoint = (this.pc.s.endpoints?.primary ?? null) as {
      protocol?: string;
      nodeId?: string;
    } | null;

    const stats = this._videoStats;
    const playerStats = this._playerStats as any;
    const mistStreamInfo = this._getMistStreamInfo();
    const trackEntries = Object.entries(mistStreamInfo?.meta?.tracks ?? {});

    return html`
      <div class="fw-dev-body">
        <div class="fw-dev-section">
          <div class="fw-dev-label">Playback Rate</div>
          <div class="fw-dev-rate">
            <div
              class=${classMap({
                "fw-dev-rate-value": true,
                "fw-dev-stat-value--good":
                  this._playbackScore >= 0.95 && this._playbackScore <= 1.05,
                "fw-dev-stat-value--accent": this._playbackScore > 1.05,
                "fw-dev-stat-value--warn":
                  this._playbackScore >= 0.75 && this._playbackScore < 0.95,
                "fw-dev-stat-value--bad": this._playbackScore < 0.75,
              })}
            >
              ${this._playbackScore.toFixed(2)}x
            </div>
            <div class="fw-dev-rate-status">
              ${this._playbackScore >= 0.95 && this._playbackScore <= 1.05
                ? "realtime"
                : this._playbackScore > 1.05
                  ? "catching up"
                  : this._playbackScore >= 0.75
                    ? "slightly slow"
                    : "stalling"}
            </div>
          </div>
          <div class="fw-dev-rate-stats">
            <span
              class=${classMap({
                "fw-dev-stat-value--good": this._qualityScore >= 75,
                "fw-dev-stat-value--bad": this._qualityScore < 75,
              })}
            >
              Quality: ${this._qualityScore}/100
            </span>
            <span
              class=${classMap({
                "fw-dev-stat-value--good": this._stallCount === 0,
                "fw-dev-stat-value--warn": this._stallCount > 0,
              })}
            >
              Stalls: ${this._stallCount}
            </span>
            <span
              class=${classMap({
                "fw-dev-stat-value--good": this._frameDropRate < 1,
                "fw-dev-stat-value--bad": this._frameDropRate >= 1,
              })}
            >
              Drops: ${this._frameDropRate.toFixed(1)}%
            </span>
          </div>
        </div>

        ${stats
          ? html`
              <div class="fw-dev-stat">
                <span class="fw-dev-stat-label">Resolution</span>
                <span class="fw-dev-stat-value">${stats.resolution}</span>
              </div>
              <div class="fw-dev-stat">
                <span class="fw-dev-stat-label">Buffer</span>
                <span class="fw-dev-stat-value">${stats.buffered}s</span>
              </div>
              <div class="fw-dev-stat">
                <span class="fw-dev-stat-label">Playback Rate</span>
                <span class="fw-dev-stat-value">${stats.playbackRate}x</span>
              </div>
              <div class="fw-dev-stat">
                <span class="fw-dev-stat-label">Time</span>
                <span class="fw-dev-stat-value">${stats.currentTime} / ${stats.duration}</span>
              </div>
              <div class="fw-dev-stat">
                <span class="fw-dev-stat-label">Ready State</span>
                <span class="fw-dev-stat-value">${stats.readyState}</span>
              </div>
              <div class="fw-dev-stat">
                <span class="fw-dev-stat-label">Network State</span>
                <span class="fw-dev-stat-value">${stats.networkState}</span>
              </div>
              ${primaryEndpoint?.protocol
                ? html`
                    <div class="fw-dev-stat">
                      <span class="fw-dev-stat-label">Protocol</span>
                      <span class="fw-dev-stat-value">${primaryEndpoint.protocol}</span>
                    </div>
                  `
                : nothing}
              ${primaryEndpoint?.nodeId
                ? html`
                    <div class="fw-dev-stat">
                      <span class="fw-dev-stat-label">Node ID</span>
                      <span
                        class="fw-dev-stat-value"
                        style="max-width:150px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;"
                        >${primaryEndpoint.nodeId}</span
                      >
                    </div>
                  `
                : nothing}
            `
          : html`<div class="fw-dev-list-empty">No video element available</div>`}
        ${playerStats
          ? html`
              <div class="fw-dev-list-header fw-dev-section-header">
                <span class="fw-dev-list-title"
                  >${playerStats.type === "hls"
                    ? "HLS.js Stats"
                    : playerStats.type === "webrtc"
                      ? "WebRTC Stats"
                      : "Player Stats"}</span
                >
              </div>

              ${playerStats.type === "hls"
                ? html`
                    <div class="fw-dev-stat">
                      <span class="fw-dev-stat-label">Bitrate</span>
                      <span class="fw-dev-stat-value--accent"
                        >${typeof playerStats.currentBitrate === "number" &&
                        playerStats.currentBitrate > 0
                          ? `${Math.round(playerStats.currentBitrate / 1000)} kbps`
                          : "N/A"}</span
                      >
                    </div>
                    <div class="fw-dev-stat">
                      <span class="fw-dev-stat-label">Bandwidth Est.</span>
                      <span class="fw-dev-stat-value"
                        >${typeof playerStats.bandwidthEstimate === "number" &&
                        playerStats.bandwidthEstimate > 0
                          ? `${Math.round(playerStats.bandwidthEstimate / 1000)} kbps`
                          : "N/A"}</span
                      >
                    </div>
                    <div class="fw-dev-stat">
                      <span class="fw-dev-stat-label">Level</span>
                      <span class="fw-dev-stat-value"
                        >${typeof playerStats.currentLevel === "number" &&
                        playerStats.currentLevel >= 0
                          ? playerStats.currentLevel
                          : "Auto"}
                        / ${Array.isArray(playerStats.levels) ? playerStats.levels.length : 0}</span
                      >
                    </div>
                    ${typeof playerStats.latency === "number"
                      ? html`
                          <div class="fw-dev-stat">
                            <span class="fw-dev-stat-label">Latency</span>
                            <span
                              class=${classMap({
                                "fw-dev-stat-value": true,
                                "fw-dev-stat-value--warn": playerStats.latency > 5000,
                              })}
                              >${Math.round(playerStats.latency)} ms</span
                            >
                          </div>
                        `
                      : nothing}
                  `
                : nothing}
              ${playerStats.type === "webrtc"
                ? html`
                    ${playerStats.video
                      ? html`
                          <div class="fw-dev-stat">
                            <span class="fw-dev-stat-label">Video Bitrate</span>
                            <span class="fw-dev-stat-value--accent"
                              >${typeof playerStats.video.bitrate === "number" &&
                              playerStats.video.bitrate > 0
                                ? `${Math.round(playerStats.video.bitrate / 1000)} kbps`
                                : "N/A"}</span
                            >
                          </div>
                          <div class="fw-dev-stat">
                            <span class="fw-dev-stat-label">FPS</span>
                            <span class="fw-dev-stat-value"
                              >${Math.round(
                                (playerStats.video.framesPerSecond as number) || 0
                              )}</span
                            >
                          </div>
                          <div class="fw-dev-stat">
                            <span class="fw-dev-stat-label">Frames</span>
                            <span class="fw-dev-stat-value"
                              >${playerStats.video.framesDecoded as number} decoded,
                              <span
                                class=${classMap({
                                  "fw-dev-stat-value--bad":
                                    ((playerStats.video.frameDropRate as number) || 0) > 1,
                                  "fw-dev-stat-value--good":
                                    ((playerStats.video.frameDropRate as number) || 0) <= 1,
                                })}
                                >${playerStats.video.framesDropped as number} dropped</span
                              ></span
                            >
                          </div>
                          <div class="fw-dev-stat">
                            <span class="fw-dev-stat-label">Packet Loss</span>
                            <span
                              class=${classMap({
                                "fw-dev-stat-value--bad":
                                  ((playerStats.video.packetLossRate as number) || 0) > 1,
                                "fw-dev-stat-value--good":
                                  ((playerStats.video.packetLossRate as number) || 0) <= 1,
                              })}
                              >${(
                                ((playerStats.video.packetLossRate as number) || 0) as number
                              ).toFixed(2)}%</span
                            >
                          </div>
                          <div class="fw-dev-stat">
                            <span class="fw-dev-stat-label">Jitter</span>
                            <span
                              class=${classMap({
                                "fw-dev-stat-value": true,
                                "fw-dev-stat-value--warn":
                                  ((playerStats.video.jitter as number) || 0) > 30,
                              })}
                              >${(((playerStats.video.jitter as number) || 0) as number).toFixed(1)}
                              ms</span
                            >
                          </div>
                          <div class="fw-dev-stat">
                            <span class="fw-dev-stat-label">Jitter Buffer</span>
                            <span class="fw-dev-stat-value"
                              >${(
                                ((playerStats.video.jitterBufferDelay as number) || 0) as number
                              ).toFixed(1)}
                              ms</span
                            >
                          </div>
                        `
                      : nothing}
                    ${playerStats.network
                      ? html`
                          <div class="fw-dev-stat">
                            <span class="fw-dev-stat-label">RTT</span>
                            <span
                              class=${classMap({
                                "fw-dev-stat-value": true,
                                "fw-dev-stat-value--warn":
                                  ((playerStats.network.rtt as number) || 0) > 200,
                              })}
                              >${Math.round(((playerStats.network.rtt as number) || 0) as number)}
                              ms</span
                            >
                          </div>
                        `
                      : nothing}
                  `
                : nothing}
            `
          : nothing}
        ${trackEntries.length > 0
          ? html`
              <div class="fw-dev-list-header fw-dev-section-header">
                <span class="fw-dev-list-title">Tracks (${trackEntries.length})</span>
              </div>
              ${trackEntries.map(([id, track]) => {
                const typedTrack = track as {
                  type: string;
                  codec: string;
                  width?: number;
                  height?: number;
                  bps?: number;
                  fpks?: number;
                  channels?: number;
                  rate?: number;
                  lang?: string;
                };

                return html`
                  <div class="fw-dev-track">
                    <div class="fw-dev-track-header">
                      <span
                        class=${classMap({
                          "fw-dev-track-badge": true,
                          "fw-dev-track-badge--video": typedTrack.type === "video",
                          "fw-dev-track-badge--audio": typedTrack.type === "audio",
                          "fw-dev-track-badge--other":
                            typedTrack.type !== "video" && typedTrack.type !== "audio",
                        })}
                        >${typedTrack.type}</span
                      >
                      <span class="fw-dev-track-codec">${typedTrack.codec}</span>
                      <span class="fw-dev-track-id">#${id}</span>
                    </div>
                    <div class="fw-dev-track-meta">
                      ${typedTrack.type === "video" && typedTrack.width && typedTrack.height
                        ? html`<span>${typedTrack.width}x${typedTrack.height}</span>`
                        : nothing}
                      ${typedTrack.bps
                        ? html`<span>${Math.round(typedTrack.bps / 1000)} kbps</span>`
                        : nothing}
                      ${typedTrack.fpks
                        ? html`<span>${Math.round(typedTrack.fpks / 1000)} fps</span>`
                        : nothing}
                      ${typedTrack.type === "audio" && typedTrack.channels
                        ? html`<span>${typedTrack.channels}ch</span>`
                        : nothing}
                      ${typedTrack.type === "audio" && typedTrack.rate
                        ? html`<span>${typedTrack.rate} Hz</span>`
                        : nothing}
                      ${typedTrack.lang ? html`<span>${typedTrack.lang}</span>` : nothing}
                    </div>
                  </div>
                `;
              })}
            `
          : nothing}
        ${mistStreamInfo && trackEntries.length === 0
          ? html`
              <div class="fw-dev-no-tracks">
                <span class="fw-dev-no-tracks-text"
                  >No track data available
                  ${mistStreamInfo.type
                    ? html`<span class="fw-dev-no-tracks-type">(${mistStreamInfo.type})</span>`
                    : nothing}</span
                >
              </div>
            `
          : nothing}
      </div>
    `;
  }

  private _renderConfigTab(): unknown {
    const allCombinations = this._getAllCombinations();
    const compatibleCombinations = allCombinations.filter((combo) => combo.compatible);
    const activeComboIndex = this._getActiveComboIndex(allCombinations);

    const currentPlayer = this.pc.s.currentPlayerInfo;
    const currentSource = this.pc.s.currentSourceInfo;

    return html`
      <div class="fw-dev-body">
        <div class="fw-dev-section">
          <div class="fw-dev-label">Active</div>
          <div class="fw-dev-value">
            ${currentPlayer?.name || "None"}
            <span class="fw-dev-value-arrow">${"\u2192"}</span>
            ${SOURCE_TYPE_LABELS[currentSource?.type || ""] || currentSource?.type || "-"}
          </div>
          ${(this.pc.s.endpoints?.primary as { nodeId?: string } | undefined)?.nodeId
            ? html`
                <div class="fw-dev-value-muted">
                  Node: ${(this.pc.s.endpoints?.primary as { nodeId?: string }).nodeId}
                </div>
              `
            : nothing}
        </div>

        <div class="fw-dev-section">
          <div class="fw-dev-label">Playback Mode</div>
          <div class="fw-dev-mode-group">
            ${(["auto", "low-latency", "quality"] as const).map(
              (mode) => html`
                <button
                  type="button"
                  class=${classMap({
                    "fw-dev-mode-btn": true,
                    "fw-dev-mode-btn--active": this.playbackMode === mode,
                  })}
                  @click=${() => this._handleModeChange(mode)}
                >
                  ${mode === "low-latency"
                    ? "Low Lat"
                    : `${mode.charAt(0).toUpperCase()}${mode.slice(1)}`}
                </button>
              `
            )}
          </div>
          <div class="fw-dev-mode-desc">
            ${this.playbackMode === "auto"
              ? "Balanced: MP4/WS \u2192 WHEP \u2192 HLS"
              : this.playbackMode === "low-latency"
                ? "WHEP/WebRTC first (<1s delay)"
                : "MP4/WS first, HLS fallback"}
          </div>
        </div>

        <div class="fw-dev-actions">
          <button type="button" class="fw-dev-action-btn" @click=${this._handleReload}>
            Reload
          </button>
          <button type="button" class="fw-dev-action-btn" @click=${this._handleNextCombo}>
            Next Option
          </button>
        </div>

        <div class="fw-dev-section" style="padding:0;border-bottom:0;">
          <div class="fw-dev-list-header">
            <span class="fw-dev-list-title">Player Options (${compatibleCombinations.length})</span>
            ${allCombinations.length > compatibleCombinations.length
              ? html`
                  <button
                    type="button"
                    class="fw-dev-list-toggle"
                    @click=${() => {
                      this._showDisabledPlayers = !this._showDisabledPlayers;
                    }}
                  >
                    <svg
                      width="10"
                      height="10"
                      viewBox="0 0 24 24"
                      fill="none"
                      stroke="currentColor"
                      stroke-width="2"
                      class=${classMap({
                        "fw-dev-chevron": true,
                        "fw-dev-chevron--open": this._showDisabledPlayers,
                      })}
                    >
                      <path d="M6 9l6 6 6-6"></path>
                    </svg>
                    ${this._showDisabledPlayers ? "Hide" : "Show"} disabled
                    (${allCombinations.length - compatibleCombinations.length})
                  </button>
                `
              : nothing}
          </div>

          ${allCombinations.length === 0
            ? html`<div class="fw-dev-list-empty">No stream info available</div>`
            : html`
                ${allCombinations.map((combo, index) => {
                  const isCodecIncompatible = combo.codecIncompatible === true;
                  const isPartial = ((combo as any).missingTracks?.length ?? 0) > 0;
                  const isWarn = isCodecIncompatible || isPartial;
                  if (!combo.compatible && !isWarn && !this._showDisabledPlayers) {
                    return nothing;
                  }

                  const isActive = activeComboIndex === index;
                  const typeLabel =
                    SOURCE_TYPE_LABELS[combo.sourceType] || combo.sourceType.split("/").pop();

                  const scoreClass =
                    !combo.compatible && !isWarn
                      ? "fw-dev-combo-score--disabled"
                      : isWarn
                        ? "fw-dev-combo-score--low"
                        : combo.score >= 2
                          ? "fw-dev-combo-score--high"
                          : combo.score >= 1.5
                            ? "fw-dev-combo-score--mid"
                            : "fw-dev-combo-score--low";

                  const rankClass = isActive
                    ? "fw-dev-combo-rank--active"
                    : !combo.compatible && !isWarn
                      ? "fw-dev-combo-rank--disabled"
                      : isWarn
                        ? "fw-dev-combo-rank--warn"
                        : "";

                  const typeClass =
                    !combo.compatible && !isWarn
                      ? "fw-dev-combo-type--disabled"
                      : isWarn
                        ? "fw-dev-combo-type--warn"
                        : "";

                  return html`
                    <div
                      class="fw-dev-combo"
                      @mouseenter=${(event: MouseEvent) =>
                        this._handleComboMouseEnter(index, event)}
                      @mouseleave=${() => {
                        this._hoveredComboIndex = null;
                        this._tooltipPos = null;
                      }}
                    >
                      <button
                        type="button"
                        class=${classMap({
                          "fw-dev-combo-btn": true,
                          "fw-dev-combo-btn--active": isActive,
                          "fw-dev-combo-btn--disabled": !combo.compatible && !isWarn,
                          "fw-dev-combo-btn--codec-warn": isWarn,
                        })}
                        @click=${() => this._handleSelectCombo(index)}
                      >
                        <span
                          class=${classMap({
                            "fw-dev-combo-rank": true,
                            [rankClass]: rankClass.length > 0,
                          })}
                          >${combo.compatible && !isPartial
                            ? index + 1
                            : isWarn
                              ? "\u26A0"
                              : "\u2014"}</span
                        >

                        <span class="fw-dev-combo-name"
                          >${combo.playerName} <span class="fw-dev-combo-arrow">${"\u2192"}</span>
                          <span
                            class=${classMap({
                              "fw-dev-combo-type": true,
                              [typeClass]: typeClass.length > 0,
                            })}
                            >${typeLabel}</span
                          ></span
                        >

                        <span class=${classMap({ "fw-dev-combo-score": true, [scoreClass]: true })}
                          >${combo.score.toFixed(2)}</span
                        >
                      </button>

                      ${this._hoveredComboIndex === index && this._tooltipPos
                        ? html`
                            <div
                              class="fw-dev-tooltip"
                              style="top: ${this._tooltipPos.top}px; left: ${this._tooltipPos
                                .left}px;"
                            >
                              <div class="fw-dev-tooltip-header">
                                <div class="fw-dev-tooltip-title">${combo.playerName}</div>
                                <div class="fw-dev-tooltip-subtitle">${combo.sourceType}</div>
                                ${combo.scoreBreakdown?.trackTypes &&
                                combo.scoreBreakdown.trackTypes.length > 0
                                  ? html`
                                      <div class="fw-dev-tooltip-tracks">
                                        Tracks:
                                        <span class="fw-dev-tooltip-value"
                                          >${combo.scoreBreakdown.trackTypes.join(", ")}</span
                                        >
                                      </div>
                                    `
                                  : nothing}
                              </div>

                              ${combo.note
                                ? html`<div class="fw-dev-tooltip-note">${combo.note}</div>`
                                : nothing}
                              ${isPartial
                                ? html`<div class="fw-dev-tooltip-note">
                                    No compatible ${(combo as any).missingTracks.join(", ")} codec
                                  </div>`
                                : nothing}
                              ${combo.compatible && combo.scoreBreakdown
                                ? html`
                                    <div class="fw-dev-tooltip-score">
                                      Score: ${combo.score.toFixed(2)}
                                    </div>
                                    <div class="fw-dev-tooltip-row">
                                      Tracks [${combo.scoreBreakdown.trackTypes.join(", ")}]:
                                      <span class="fw-dev-tooltip-value"
                                        >${combo.scoreBreakdown.trackScore.toFixed(2)}</span
                                      >
                                      <span class="fw-dev-tooltip-weight"
                                        >x${combo.scoreBreakdown.weights.tracks}</span
                                      >
                                    </div>
                                    <div class="fw-dev-tooltip-row">
                                      Priority:
                                      <span class="fw-dev-tooltip-value"
                                        >${combo.scoreBreakdown.priorityScore.toFixed(2)}</span
                                      >
                                      <span class="fw-dev-tooltip-weight"
                                        >x${combo.scoreBreakdown.weights.priority}</span
                                      >
                                    </div>
                                    <div class="fw-dev-tooltip-row">
                                      Source:
                                      <span class="fw-dev-tooltip-value"
                                        >${combo.scoreBreakdown.sourceScore.toFixed(2)}</span
                                      >
                                      <span class="fw-dev-tooltip-weight"
                                        >x${combo.scoreBreakdown.weights.source}</span
                                      >
                                    </div>

                                    ${typeof combo.scoreBreakdown.reliabilityScore === "number"
                                      ? html`
                                          <div class="fw-dev-tooltip-row">
                                            Reliability:
                                            <span class="fw-dev-tooltip-value"
                                              >${combo.scoreBreakdown.reliabilityScore.toFixed(
                                                2
                                              )}</span
                                            >
                                            <span class="fw-dev-tooltip-weight"
                                              >x${combo.scoreBreakdown.weights.reliability ??
                                              0}</span
                                            >
                                          </div>
                                        `
                                      : nothing}
                                    ${typeof combo.scoreBreakdown.modeBonus === "number" &&
                                    combo.scoreBreakdown.modeBonus !== 0
                                      ? html`
                                          <div class="fw-dev-tooltip-row">
                                            Mode (${this.playbackMode}):
                                            <span class="fw-dev-tooltip-bonus"
                                              >+${combo.scoreBreakdown.modeBonus.toFixed(2)}</span
                                            >
                                            <span class="fw-dev-tooltip-weight"
                                              >x${combo.scoreBreakdown.weights.mode ?? 0}</span
                                            >
                                          </div>
                                        `
                                      : nothing}
                                    ${typeof combo.scoreBreakdown.routingBonus === "number" &&
                                    combo.scoreBreakdown.routingBonus !== 0
                                      ? html`
                                          <div class="fw-dev-tooltip-row">
                                            Routing:
                                            <span
                                              class=${classMap({
                                                "fw-dev-tooltip-bonus":
                                                  combo.scoreBreakdown.routingBonus > 0,
                                                "fw-dev-tooltip-penalty":
                                                  combo.scoreBreakdown.routingBonus < 0,
                                              })}
                                              >${combo.scoreBreakdown.routingBonus > 0
                                                ? "+"
                                                : ""}${combo.scoreBreakdown.routingBonus.toFixed(
                                                2
                                              )}</span
                                            >
                                            <span class="fw-dev-tooltip-weight"
                                              >x${combo.scoreBreakdown.weights.routing ?? 0}</span
                                            >
                                          </div>
                                        `
                                      : nothing}
                                  `
                                : html`
                                    <div class="fw-dev-tooltip-error">
                                      ${combo.incompatibleReason || "Incompatible"}
                                    </div>
                                  `}
                            </div>
                          `
                        : nothing}
                    </div>
                  `;
                })}
              `}
        </div>
      </div>
    `;
  }

  protected render() {
    return html`
      <div class="fw-dev-panel">
        <div class="fw-dev-header">
          <button
            type="button"
            class=${classMap({
              "fw-dev-tab": true,
              "fw-dev-tab--active": this._activeTab === "config",
            })}
            @click=${() => {
              this._activeTab = "config";
            }}
          >
            Config
          </button>
          <button
            type="button"
            class=${classMap({
              "fw-dev-tab": true,
              "fw-dev-tab--active": this._activeTab === "stats",
            })}
            @click=${() => {
              this._activeTab = "stats";
            }}
          >
            Stats
          </button>
          <div class="fw-dev-spacer"></div>
          <button
            type="button"
            class="fw-dev-close"
            aria-label="Close dev mode panel"
            @click=${() =>
              this.dispatchEvent(new CustomEvent("fw-close", { bubbles: true, composed: true }))}
          >
            ${closeIcon()}
          </button>
        </div>

        ${this._activeTab === "config" ? this._renderConfigTab() : this._renderStatsTab()}
      </div>
    `;
  }
}

declare global {
  interface HTMLElementTagNameMap {
    "fw-dev-mode-panel": FwDevModePanel;
  }
}

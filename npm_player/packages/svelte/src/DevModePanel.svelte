<!--
  DevModePanel.svelte - Advanced settings overlay for testing player configurations
  Port of src/components/DevModePanel.tsx
-->
<script lang="ts">
  import {
    cn,
    globalPlayerManager,
    QualityMonitor,
    type StreamInfo,
    type MistStreamInfo,
    type PlaybackMode,
  } from "@livepeer-frameworks/player-core";

  /** Short labels for source types */
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

  interface Props {
    /** Callback when user selects a combo (one-shot selection) */
    onSettingsChange: (settings: {
      forcePlayer?: string;
      forceType?: string;
      forceSource?: number;
    }) => void;
    playbackMode?: PlaybackMode;
    onModeChange?: (mode: PlaybackMode) => void;
    onReload?: () => void;
    streamInfo?: StreamInfo | null;
    mistStreamInfo?: MistStreamInfo | null;
    currentPlayer?: { name: string; shortname: string } | null;
    currentSource?: { url: string; type: string } | null;
    videoElement?: HTMLVideoElement | null;
    protocol?: string;
    nodeId?: string;
    isVisible?: boolean;
    isOpen?: boolean;
    onOpenChange?: (isOpen: boolean) => void;
  }

  let {
    onSettingsChange,
    playbackMode = "auto",
    onModeChange = undefined,
    onReload = undefined,
    streamInfo = null,
    mistStreamInfo = null,
    currentPlayer = null,
    currentSource = null,
    videoElement = null,
    protocol = undefined,
    nodeId = undefined,
    isVisible: _isVisible = true,
    isOpen: controlledIsOpen = undefined,
    onOpenChange = undefined,
  }: Props = $props();

  // Internal state
  let internalIsOpen = $state(false);
  let activeTab = $state<"config" | "stats">("config");
  let hoveredComboIndex = $state<number | null>(null);
  let tooltipAbove = $state(false);
  let showDisabledPlayers = $state(false);
  let comboListRef: HTMLDivElement | undefined = $state();

  // Quality monitoring state
  let playbackScore = $state(1.0);
  let qualityScore = $state(100);
  let stallCount = $state(0);
  let frameDropRate = $state(0);
  let qualityMonitor: QualityMonitor | null = null;

  // Video stats
  let stats = $state<{
    resolution: string;
    buffered: string;
    playbackRate: string;
    currentTime: string;
    duration: string;
    readyState: number;
    networkState: number;
  } | null>(null);

  // Player-specific stats (HLS.js / WebRTC)
  let playerStats = $state<any>(null);
  let statsIntervalRef: ReturnType<typeof setInterval> | null = null;

  // Controlled/uncontrolled state
  let isOpen = $derived(controlledIsOpen !== undefined ? controlledIsOpen : internalIsOpen);

  function setIsOpen(value: boolean) {
    if (onOpenChange) {
      onOpenChange(value);
    } else {
      internalIsOpen = value;
    }
  }

  // Get all player-source combinations with scores
  // getAllCombinations now includes all combos (compatible + incompatible)
  // and uses content-based caching - won't spam on every MistServer update
  let allCombinations = $derived.by(() => {
    if (!streamInfo) return [];
    try {
      return globalPlayerManager.getAllCombinations(streamInfo, playbackMode);
    } catch {
      return [];
    }
  });

  let combinations = $derived(allCombinations.filter((c) => c.compatible));

  // Find active combo index
  let activeComboIndex = $derived.by(() => {
    if (!currentPlayer || !currentSource || allCombinations.length === 0) return -1;
    return allCombinations.findIndex(
      (c) => c.player === currentPlayer.shortname && c.sourceType === currentSource.type
    );
  });

  let activeCompatibleIndex = $derived.by(() => {
    if (!currentPlayer || !currentSource || combinations.length === 0) return -1;
    return combinations.findIndex(
      (c) => c.player === currentPlayer.shortname && c.sourceType === currentSource.type
    );
  });

  // Handlers
  function handleReload() {
    // Just trigger reload - controller manages the state
    onReload?.();
  }

  function handleNextCombo() {
    if (combinations.length === 0) return;
    const startIdx = activeCompatibleIndex >= 0 ? activeCompatibleIndex : -1;
    const nextIdx = (startIdx + 1) % combinations.length;
    const combo = combinations[nextIdx];
    onSettingsChange({
      forcePlayer: combo.player,
      forceType: combo.sourceType,
      forceSource: combo.sourceIndex,
    });
  }

  function handleSelectCombo(index: number) {
    const combo = allCombinations[index];
    if (!combo) return;
    onSettingsChange({
      forcePlayer: combo.player,
      forceType: combo.sourceType,
      forceSource: combo.sourceIndex,
    });
  }

  function handleComboHover(index: number, e: MouseEvent) {
    hoveredComboIndex = index;
    if (comboListRef) {
      const container = comboListRef;
      const row = e.currentTarget as HTMLElement;
      const containerRect = container.getBoundingClientRect();
      const rowRect = row.getBoundingClientRect();
      const relativePosition = (rowRect.top - containerRect.top) / containerRect.height;
      tooltipAbove = relativePosition > 0.6;
    }
  }

  // Quality monitoring
  $effect(() => {
    if (videoElement && isOpen) {
      if (!qualityMonitor) {
        qualityMonitor = new QualityMonitor({
          sampleInterval: 500,
          onSample: (quality) => {
            qualityScore = quality.score;
            stallCount = quality.stallCount;
            frameDropRate = quality.frameDropRate;
            if (qualityMonitor) {
              playbackScore = qualityMonitor.getPlaybackScore();
            }
          },
        });
      }
      qualityMonitor.start(videoElement);
    }

    return () => {
      qualityMonitor?.stop();
    };
  });

  // Video stats polling
  $effect(() => {
    if (!isOpen || activeTab !== "stats") return;

    function updateStats() {
      const player = globalPlayerManager.getCurrentPlayer();
      const v = player?.getVideoElement() || videoElement;
      if (!v) {
        stats = null;
        return;
      }
      stats = {
        resolution: `${v.videoWidth}x${v.videoHeight}`,
        buffered:
          v.buffered.length > 0
            ? (v.buffered.end(v.buffered.length - 1) - v.currentTime).toFixed(1)
            : "0",
        playbackRate: v.playbackRate.toFixed(2),
        currentTime: v.currentTime.toFixed(1),
        duration: isFinite(v.duration) ? v.duration.toFixed(1) : "live",
        readyState: v.readyState,
        networkState: v.networkState,
      };
    }

    updateStats();
    const interval = setInterval(updateStats, 500);
    return () => clearInterval(interval);
  });

  // Poll player-specific stats when stats tab is open
  $effect(() => {
    if (!isOpen || activeTab !== "stats") {
      playerStats = null;
      return;
    }

    async function pollStats() {
      try {
        const player = globalPlayerManager.getCurrentPlayer();
        if (player && typeof player.getStats === "function") {
          const stats = await player.getStats();
          if (stats) {
            playerStats = stats;
          }
        }
      } catch {
        // Ignore errors
      }
    }

    // Poll immediately and then every 500ms
    pollStats();
    statsIntervalRef = setInterval(pollStats, 500);

    return () => {
      if (statsIntervalRef) {
        clearInterval(statsIntervalRef);
        statsIntervalRef = null;
      }
    };
  });
</script>

{#if isOpen}
  <div class="fw-dev-panel">
    <!-- Header with tabs -->
    <div class="fw-dev-header">
      <button
        type="button"
        onclick={() => (activeTab = "config")}
        class={cn("fw-dev-tab", activeTab === "config" && "fw-dev-tab--active")}
      >
        Config
      </button>
      <button
        type="button"
        onclick={() => (activeTab = "stats")}
        class={cn("fw-dev-tab", activeTab === "stats" && "fw-dev-tab--active")}
      >
        Stats
      </button>
      <div class="fw-dev-spacer"></div>
      <button
        type="button"
        onclick={() => setIsOpen(false)}
        class="fw-dev-close"
        aria-label="Close dev mode panel"
      >
        <svg
          width="12"
          height="12"
          viewBox="0 0 12 12"
          fill="none"
          stroke="currentColor"
          stroke-width="1.5"
        >
          <path d="M2 2l8 8M10 2l-8 8" />
        </svg>
      </button>
    </div>

    {#if activeTab === "config"}
      <div bind:this={comboListRef} class="fw-dev-body">
        <!-- Current State -->
        <div class="fw-dev-section">
          <div class="fw-dev-label">Active</div>
          <div class="fw-dev-value">
            {currentPlayer?.name || "None"}{" "}
            <span class="fw-dev-value-arrow">→</span>{" "}
            {SOURCE_TYPE_LABELS[currentSource?.type || ""] || currentSource?.type || "—"}
          </div>
          {#if nodeId}
            <div class="fw-dev-value-muted">Node: {nodeId}</div>
          {/if}
        </div>

        <!-- Playback Mode Selector -->
        <div class="fw-dev-section">
          <div class="fw-dev-label">Playback Mode</div>
          <div class="fw-dev-mode-group">
            {#each ["auto", "low-latency", "quality"] as mode}
              <button
                type="button"
                onclick={() => onModeChange?.(mode as PlaybackMode)}
                class={cn("fw-dev-mode-btn", playbackMode === mode && "fw-dev-mode-btn--active")}
              >
                {mode === "low-latency" ? "Low Lat" : mode.charAt(0).toUpperCase() + mode.slice(1)}
              </button>
            {/each}
          </div>
          <div class="fw-dev-mode-desc">
            {#if playbackMode === "auto"}Balanced: MP4/WS → WHEP → HLS{/if}
            {#if playbackMode === "low-latency"}WHEP/WebRTC first (sub-1s delay){/if}
            {#if playbackMode === "quality"}MP4/WS first, HLS fallback{/if}
          </div>
        </div>

        <!-- Action buttons -->
        <div class="fw-dev-actions">
          <button type="button" onclick={handleReload} class="fw-dev-action-btn"> Reload </button>
          <button type="button" onclick={handleNextCombo} class="fw-dev-action-btn">
            Next Option
          </button>
        </div>

        <!-- Combo list -->
        <div class="fw-dev-section fw-dev-section-header">
          <div class="fw-dev-list-header">
            <span class="fw-dev-list-title">
              Player Options ({combinations.length})
            </span>
            {#if allCombinations.length > combinations.length}
              <button
                type="button"
                onclick={() => (showDisabledPlayers = !showDisabledPlayers)}
                class="fw-dev-list-toggle"
              >
                <svg
                  width="10"
                  height="10"
                  viewBox="0 0 24 24"
                  fill="none"
                  stroke="currentColor"
                  stroke-width="2"
                  class={cn("fw-dev-chevron", showDisabledPlayers && "fw-dev-chevron--open")}
                >
                  <path d="M6 9l6 6 6-6" />
                </svg>
                {showDisabledPlayers ? "Hide" : "Show"} disabled ({allCombinations.length -
                  combinations.length})
              </button>
            {/if}
          </div>

          {#if allCombinations.length === 0}
            <div class="fw-dev-list-empty">No stream info available</div>
          {:else}
            {#each allCombinations as combo, index}
              {@const isCodecIncompat = (combo as any).codecIncompatible === true}
              {@const shouldShow = combo.compatible || isCodecIncompat || showDisabledPlayers}
              {@const isActive = activeComboIndex === index}
              {@const typeLabel =
                SOURCE_TYPE_LABELS[combo.sourceType] || combo.sourceType.split("/").pop()}

              {#if shouldShow}
                <div
                  class="fw-dev-combo"
                  role="listitem"
                  onmouseenter={(e) => handleComboHover(index, e)}
                  onmouseleave={() => (hoveredComboIndex = null)}
                >
                  <button
                    type="button"
                    onclick={() => handleSelectCombo(index)}
                    class={cn(
                      "fw-dev-combo-btn",
                      isActive && "fw-dev-combo-btn--active",
                      !combo.compatible && !isCodecIncompat && "fw-dev-combo-btn--disabled",
                      isCodecIncompat && "fw-dev-combo-btn--codec-warn"
                    )}
                  >
                    <!-- Rank -->
                    <span
                      class={cn(
                        "fw-dev-combo-rank",
                        isActive
                          ? "fw-dev-combo-rank--active"
                          : !combo.compatible && !isCodecIncompat
                            ? "fw-dev-combo-rank--disabled"
                            : isCodecIncompat
                              ? "fw-dev-combo-rank--warn"
                              : ""
                      )}
                    >
                      {combo.compatible ? index + 1 : isCodecIncompat ? "⚠" : "—"}
                    </span>
                    <!-- Player + Protocol -->
                    <span class="fw-dev-combo-name">
                      {combo.playerName}{" "}
                      <span class="fw-dev-combo-arrow">→</span>{" "}
                      <span
                        class={cn(
                          "fw-dev-combo-type",
                          isCodecIncompat && "fw-dev-combo-type--warn",
                          !combo.compatible && !isCodecIncompat && "fw-dev-combo-type--disabled"
                        )}>{typeLabel}</span
                      >
                    </span>
                    <!-- Score -->
                    <span
                      class={cn(
                        "fw-dev-combo-score",
                        !combo.compatible && !isCodecIncompat
                          ? "fw-dev-combo-score--disabled"
                          : isCodecIncompat
                            ? "fw-dev-combo-score--low"
                            : combo.score >= 2
                              ? "fw-dev-combo-score--high"
                              : combo.score >= 1.5
                                ? "fw-dev-combo-score--mid"
                                : "fw-dev-combo-score--low"
                      )}
                    >
                      {combo.score.toFixed(2)}
                    </span>
                  </button>

                  <!-- Tooltip -->
                  {#if hoveredComboIndex === index}
                    <div
                      class={cn(
                        "fw-dev-tooltip",
                        tooltipAbove ? "fw-dev-tooltip--above" : "fw-dev-tooltip--below"
                      )}
                    >
                      <div class="fw-dev-tooltip-header">
                        <div class="fw-dev-tooltip-title">{combo.playerName}</div>
                        <div class="fw-dev-tooltip-subtitle">{combo.sourceType}</div>
                        {#if combo.scoreBreakdown?.trackTypes?.length}
                          <div class="fw-dev-tooltip-tracks">
                            Tracks: <span class="fw-dev-tooltip-value"
                              >{combo.scoreBreakdown.trackTypes.join(", ")}</span
                            >
                          </div>
                        {/if}
                      </div>
                      {#if combo.compatible && combo.scoreBreakdown}
                        <div class="fw-dev-tooltip-score">Score: {combo.score.toFixed(2)}</div>
                        <div class="fw-dev-tooltip-row">
                          Tracks: <span class="fw-dev-tooltip-value"
                            >{combo.scoreBreakdown.trackScore.toFixed(2)}</span
                          >
                          <span class="fw-dev-tooltip-weight"
                            >x{combo.scoreBreakdown.weights.tracks}</span
                          >
                        </div>
                        <div class="fw-dev-tooltip-row">
                          Priority: <span class="fw-dev-tooltip-value"
                            >{combo.scoreBreakdown.priorityScore.toFixed(2)}</span
                          >
                          <span class="fw-dev-tooltip-weight"
                            >x{combo.scoreBreakdown.weights.priority}</span
                          >
                        </div>
                        <div class="fw-dev-tooltip-row">
                          Source: <span class="fw-dev-tooltip-value"
                            >{combo.scoreBreakdown.sourceScore.toFixed(2)}</span
                          >
                          <span class="fw-dev-tooltip-weight"
                            >x{combo.scoreBreakdown.weights.source}</span
                          >
                        </div>
                      {:else}
                        <div class="fw-dev-tooltip-error">
                          {combo.incompatibleReason || "Incompatible"}
                        </div>
                      {/if}
                    </div>
                  {/if}
                </div>
              {/if}
            {/each}
          {/if}
        </div>
      </div>
    {:else if activeTab === "stats"}
      <div class="fw-dev-body">
        <!-- Playback Rate -->
        <div class="fw-dev-section">
          <div class="fw-dev-label">Playback Rate</div>
          <div class="fw-dev-rate">
            <div
              class={cn(
                "fw-dev-rate-value",
                playbackScore >= 0.95 && playbackScore <= 1.05
                  ? "fw-dev-stat-value--good"
                  : playbackScore > 1.05
                    ? "fw-dev-stat-value--accent"
                    : playbackScore >= 0.75
                      ? "fw-dev-stat-value--warn"
                      : "fw-dev-stat-value--bad"
              )}
            >
              {playbackScore.toFixed(2)}×
            </div>
            <div class="fw-dev-rate-status">
              {playbackScore >= 0.95 && playbackScore <= 1.05
                ? "realtime"
                : playbackScore > 1.05
                  ? "catching up"
                  : playbackScore >= 0.75
                    ? "slightly slow"
                    : "stalling"}
            </div>
          </div>
          <div class="fw-dev-rate-stats">
            <span class={qualityScore >= 75 ? "fw-dev-stat-value--good" : "fw-dev-stat-value--bad"}
              >Quality: {qualityScore}/100</span
            >
            <span class={stallCount === 0 ? "fw-dev-stat-value--good" : "fw-dev-stat-value--warn"}
              >Stalls: {stallCount}</span
            >
            <span class={frameDropRate < 1 ? "fw-dev-stat-value--good" : "fw-dev-stat-value--bad"}
              >Drops: {frameDropRate.toFixed(1)}%</span
            >
          </div>
        </div>

        <!-- Video Stats -->
        {#if stats}
          <div class="fw-dev-stat">
            <span class="fw-dev-stat-label">Resolution</span><span class="fw-dev-stat-value"
              >{stats.resolution}</span
            >
          </div>
          <div class="fw-dev-stat">
            <span class="fw-dev-stat-label">Buffer</span><span class="fw-dev-stat-value"
              >{stats.buffered}s</span
            >
          </div>
          <div class="fw-dev-stat">
            <span class="fw-dev-stat-label">Playback Rate</span><span class="fw-dev-stat-value"
              >{stats.playbackRate}x</span
            >
          </div>
          <div class="fw-dev-stat">
            <span class="fw-dev-stat-label">Time</span><span class="fw-dev-stat-value"
              >{stats.currentTime} / {stats.duration}</span
            >
          </div>
          <div class="fw-dev-stat">
            <span class="fw-dev-stat-label">Ready State</span><span class="fw-dev-stat-value"
              >{stats.readyState}</span
            >
          </div>
          <div class="fw-dev-stat">
            <span class="fw-dev-stat-label">Network State</span><span class="fw-dev-stat-value"
              >{stats.networkState}</span
            >
          </div>
          {#if protocol}
            <div class="fw-dev-stat">
              <span class="fw-dev-stat-label">Protocol</span><span class="fw-dev-stat-value"
                >{protocol}</span
              >
            </div>
          {/if}
          {#if nodeId}
            <div class="fw-dev-stat">
              <span class="fw-dev-stat-label">Node ID</span><span class="fw-dev-stat-value"
                >{nodeId}</span
              >
            </div>
          {/if}
        {:else}
          <div class="fw-dev-list-empty">No video element available</div>
        {/if}

        <!-- Player-specific Stats (HLS.js / WebRTC) -->
        {#if playerStats}
          <div class="fw-dev-section fw-dev-section-header">
            <div class="fw-dev-label">
              {playerStats.type === "hls"
                ? "HLS.js Stats"
                : playerStats.type === "webrtc"
                  ? "WebRTC Stats"
                  : "Player Stats"}
            </div>
          </div>
          <!-- HLS-specific stats -->
          {#if playerStats.type === "hls"}
            <div class="fw-dev-stat">
              <span class="fw-dev-stat-label">Bitrate</span>
              <span class="fw-dev-stat-value fw-dev-stat-value--accent">
                {playerStats.currentBitrate > 0
                  ? `${Math.round(playerStats.currentBitrate / 1000)} kbps`
                  : "N/A"}
              </span>
            </div>
            <div class="fw-dev-stat">
              <span class="fw-dev-stat-label">Bandwidth Est.</span>
              <span class="fw-dev-stat-value">
                {playerStats.bandwidthEstimate > 0
                  ? `${Math.round(playerStats.bandwidthEstimate / 1000)} kbps`
                  : "N/A"}
              </span>
            </div>
            <div class="fw-dev-stat">
              <span class="fw-dev-stat-label">Level</span>
              <span class="fw-dev-stat-value">
                {playerStats.currentLevel >= 0 ? playerStats.currentLevel : "Auto"} / {playerStats
                  .levels?.length || 0}
              </span>
            </div>
            {#if playerStats.latency !== undefined}
              <div class="fw-dev-stat">
                <span class="fw-dev-stat-label">Latency</span>
                <span
                  class={playerStats.latency > 5000
                    ? "fw-dev-stat-value fw-dev-stat-value--warn"
                    : "fw-dev-stat-value"}
                >
                  {Math.round(playerStats.latency)} ms
                </span>
              </div>
            {/if}
          {/if}

          <!-- WebRTC-specific stats -->
          {#if playerStats.type === "webrtc"}
            {#if playerStats.video}
              <div class="fw-dev-stat">
                <span class="fw-dev-stat-label">Video Bitrate</span>
                <span class="fw-dev-stat-value fw-dev-stat-value--accent">
                  {playerStats.video.bitrate > 0
                    ? `${Math.round(playerStats.video.bitrate / 1000)} kbps`
                    : "N/A"}
                </span>
              </div>
              <div class="fw-dev-stat">
                <span class="fw-dev-stat-label">FPS</span>
                <span class="fw-dev-stat-value"
                  >{Math.round(playerStats.video.framesPerSecond || 0)}</span
                >
              </div>
              <div class="fw-dev-stat">
                <span class="fw-dev-stat-label">Frames</span>
                <span class="fw-dev-stat-value">
                  {playerStats.video.framesDecoded} decoded,{" "}
                  <span
                    class={playerStats.video.frameDropRate > 1
                      ? "fw-dev-stat-value--bad"
                      : "fw-dev-stat-value--good"}
                  >
                    {playerStats.video.framesDropped} dropped
                  </span>
                </span>
              </div>
              <div class="fw-dev-stat">
                <span class="fw-dev-stat-label">Packet Loss</span>
                <span
                  class={playerStats.video.packetLossRate > 1
                    ? "fw-dev-stat-value fw-dev-stat-value--bad"
                    : "fw-dev-stat-value fw-dev-stat-value--good"}
                >
                  {playerStats.video.packetLossRate?.toFixed(2) || 0}%
                </span>
              </div>
              <div class="fw-dev-stat">
                <span class="fw-dev-stat-label">Jitter</span>
                <span
                  class={playerStats.video.jitter > 30
                    ? "fw-dev-stat-value fw-dev-stat-value--warn"
                    : "fw-dev-stat-value"}
                >
                  {playerStats.video.jitter?.toFixed(1) || 0} ms
                </span>
              </div>
              <div class="fw-dev-stat">
                <span class="fw-dev-stat-label">Jitter Buffer</span>
                <span class="fw-dev-stat-value"
                  >{playerStats.video.jitterBufferDelay?.toFixed(1) || 0} ms</span
                >
              </div>
            {/if}
            {#if playerStats.network}
              <div class="fw-dev-stat">
                <span class="fw-dev-stat-label">RTT</span>
                <span
                  class={playerStats.network.rtt > 200
                    ? "fw-dev-stat-value fw-dev-stat-value--warn"
                    : "fw-dev-stat-value"}
                >
                  {Math.round(playerStats.network.rtt || 0)} ms
                </span>
              </div>
            {/if}
          {/if}
        {/if}

        <!-- MistServer Track Info -->
        {#if mistStreamInfo?.meta?.tracks && Object.keys(mistStreamInfo.meta.tracks).length > 0}
          <div class="fw-dev-section fw-dev-section-header">
            <div class="fw-dev-label">
              Tracks ({Object.keys(mistStreamInfo.meta.tracks).length})
            </div>
          </div>
          {#each Object.entries(mistStreamInfo.meta.tracks) as [id, track]}
            <div class="fw-dev-track">
              <div class="fw-dev-track-header">
                <span
                  class={cn(
                    "fw-dev-track-badge",
                    track.type === "video"
                      ? "fw-dev-track-badge--video"
                      : track.type === "audio"
                        ? "fw-dev-track-badge--audio"
                        : "fw-dev-track-badge--other"
                  )}
                >
                  {track.type}
                </span>
                <span class="fw-dev-track-codec">{track.codec}</span>
                <span class="fw-dev-track-id">#{id}</span>
              </div>
              <div class="fw-dev-track-meta">
                {#if track.type === "video" && track.width && track.height}
                  <span>{track.width}×{track.height}</span>
                {/if}
                {#if track.bps}
                  <span>{Math.round(track.bps / 1000)} kbps</span>
                {/if}
                {#if track.fpks}
                  <span>{Math.round(track.fpks / 1000)} fps</span>
                {/if}
                {#if track.type === "audio" && track.channels}
                  <span>{track.channels}ch</span>
                {/if}
                {#if track.type === "audio" && track.rate}
                  <span>{track.rate} Hz</span>
                {/if}
                {#if track.lang}
                  <span>{track.lang}</span>
                {/if}
              </div>
            </div>
          {/each}
        {/if}
      </div>
    {/if}
  </div>
{/if}

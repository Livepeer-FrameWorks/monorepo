<!--
  AdvancedPanel - Sidebar panel for advanced StreamCrafter settings
  Matches Player's DevModePanel styling exactly

  Tabs:
  - Audio: Master gain, per-source volume, audio processing info
  - Stats: Connection info, WebRTC stats
  - Info: WHIP URL, profile, sources
  - Compositor: Renderer info, performance stats
-->
<script lang="ts">
  import { getContext } from "svelte";
  import type { Readable } from "svelte/store";
  import type {
    IngestState,
    IngestStats,
    QualityProfile,
    MediaSource,
    RendererType,
    RendererStats,
    EncoderOverrides,
    StudioTranslateFn,
  } from "@livepeer-frameworks/streamcrafter-core";
  import {
    createEncoderConfig,
    getAudioConstraints,
    getEncoderSettings,
    createStudioTranslator,
  } from "@livepeer-frameworks/streamcrafter-core";
  import VolumeSlider from "./VolumeSlider.svelte";

  export interface AudioProcessingSettings {
    echoCancellation: boolean;
    noiseSuppression: boolean;
    autoGainControl: boolean;
  }

  // Encoder stats interface
  export interface EncoderStats {
    video: {
      framesEncoded: number;
      framesPending: number;
      bytesEncoded: number;
      lastFrameTime: number;
    };
    audio: {
      samplesEncoded: number;
      samplesPending: number;
      bytesEncoded: number;
      lastSampleTime: number;
    };
    timestamp: number;
  }

  interface Props {
    /** Whether the panel is open */
    isOpen: boolean;
    /** Callback when panel should close */
    onClose: () => void;
    /** Current ingest state */
    ingestState: IngestState;
    /** Quality profile */
    qualityProfile: QualityProfile;
    /** WHIP URL */
    whipUrl?: string;
    /** Sources */
    sources: MediaSource[];
    /** Stats */
    stats: IngestStats | null;
    /** Media stream for actual track settings */
    mediaStream?: MediaStream | null;
    /** Master volume (0-2) */
    masterVolume: number;
    /** Callback to set master volume */
    onMasterVolumeChange: (volume: number) => void;
    /** Audio level (0-1) */
    audioLevel: number;
    /** Is audio mixing enabled */
    audioMixingEnabled: boolean;
    /** Error */
    error: string | null;
    /** Audio processing settings */
    audioProcessing: AudioProcessingSettings;
    /** Callback to change audio processing settings */
    onAudioProcessingChange: (settings: Partial<AudioProcessingSettings>) => void;
    /** Compositor enabled */
    compositorEnabled?: boolean;
    /** Compositor renderer type */
    compositorRendererType?: RendererType | null;
    /** Compositor stats */
    compositorStats?: RendererStats | null;
    /** Scene count */
    sceneCount?: number;
    /** Layer count */
    layerCount?: number;
    /** Encoder: useWebCodecs setting */
    useWebCodecs?: boolean;
    /** Encoder: is WebCodecs actually active */
    isWebCodecsActive?: boolean;
    /** Encoder: stats from WebCodecs encoder */
    encoderStats?: EncoderStats | null;
    /** Encoder: callback to toggle useWebCodecs */
    onUseWebCodecsChange?: (enabled: boolean) => void;
    /** Whether WebCodecs encoding path is available */
    isWebCodecsAvailable?: boolean;
    /** Encoder settings overrides */
    encoderOverrides?: EncoderOverrides;
    /** Callback to change encoder overrides */
    onEncoderOverridesChange?: (overrides: EncoderOverrides) => void;
  }

  // Preset options for encoder settings
  interface SettingOption<T> {
    value: T;
    label: string;
  }

  const RESOLUTION_OPTIONS: SettingOption<string>[] = [
    { value: "3840x2160", label: "3840×2160 (4K)" },
    { value: "2560x1440", label: "2560×1440 (1440p)" },
    { value: "1920x1080", label: "1920×1080 (1080p)" },
    { value: "1280x720", label: "1280×720 (720p)" },
    { value: "854x480", label: "854×480 (480p)" },
    { value: "640x360", label: "640×360 (360p)" },
  ];

  const VIDEO_BITRATE_OPTIONS: SettingOption<number>[] = [
    { value: 50_000_000, label: "50 Mbps" },
    { value: 35_000_000, label: "35 Mbps" },
    { value: 25_000_000, label: "25 Mbps" },
    { value: 15_000_000, label: "15 Mbps" },
    { value: 10_000_000, label: "10 Mbps" },
    { value: 8_000_000, label: "8 Mbps" },
    { value: 6_000_000, label: "6 Mbps" },
    { value: 4_000_000, label: "4 Mbps" },
    { value: 2_500_000, label: "2.5 Mbps" },
    { value: 2_000_000, label: "2 Mbps" },
    { value: 1_500_000, label: "1.5 Mbps" },
    { value: 1_000_000, label: "1 Mbps" },
    { value: 500_000, label: "500 kbps" },
  ];

  const FRAMERATE_OPTIONS: SettingOption<number>[] = [
    { value: 120, label: "120 fps" },
    { value: 60, label: "60 fps" },
    { value: 30, label: "30 fps" },
    { value: 24, label: "24 fps" },
    { value: 15, label: "15 fps" },
  ];

  const AUDIO_BITRATE_OPTIONS: SettingOption<number>[] = [
    { value: 320_000, label: "320 kbps" },
    { value: 256_000, label: "256 kbps" },
    { value: 192_000, label: "192 kbps" },
    { value: 128_000, label: "128 kbps" },
    { value: 96_000, label: "96 kbps" },
    { value: 64_000, label: "64 kbps" },
  ];

  let {
    isOpen,
    onClose,
    ingestState,
    qualityProfile,
    whipUrl,
    sources,
    stats,
    mediaStream = null,
    masterVolume,
    onMasterVolumeChange,
    audioLevel,
    audioMixingEnabled,
    error,
    audioProcessing,
    onAudioProcessingChange,
    compositorEnabled = false,
    compositorRendererType = null,
    compositorStats = null,
    sceneCount = 0,
    layerCount = 0,
    useWebCodecs = true,
    isWebCodecsActive = false,
    encoderStats = null,
    onUseWebCodecsChange,
    isWebCodecsAvailable = true,
    encoderOverrides = {},
    onEncoderOverridesChange,
  }: Props = $props();

  const translatorCtx = getContext<Readable<StudioTranslateFn> | undefined>("fw-sc-translator");
  const fallbackT = createStudioTranslator({ locale: "en" });
  let t: StudioTranslateFn = $derived(translatorCtx ? $translatorCtx : fallbackT);

  let profileEncoderSettings = $derived(getEncoderSettings(qualityProfile));
  let effectiveEncoderConfig = $derived(
    createEncoderConfig(qualityProfile === "auto" ? "broadcast" : qualityProfile, encoderOverrides)
  );
  // Computed values for encoder settings display
  let currentResolution = $derived(
    `${encoderOverrides?.video?.width ?? profileEncoderSettings.video.width}x${encoderOverrides?.video?.height ?? profileEncoderSettings.video.height}`
  );
  let hasEncoderOverrides = $derived(!!(encoderOverrides?.video || encoderOverrides?.audio));

  let activeTab = $state<"audio" | "stats" | "info" | "compositor">("audio");
  let profileDefaults = $derived(getAudioConstraints(qualityProfile));
  let videoTrackSettings = $derived.by(() => {
    const track = mediaStream?.getVideoTracks?.()[0];
    return track?.getSettings ? track.getSettings() : undefined;
  });

  function formatBitrate(bps: number): string {
    if (bps >= 1_000_000) {
      return `${(bps / 1_000_000).toFixed(1)} Mbps`;
    }
    return `${(bps / 1000).toFixed(0)} kbps`;
  }

  function copyWhipUrl() {
    if (whipUrl) {
      navigator.clipboard.writeText(whipUrl).catch(console.error);
    }
  }

  // Audio processing toggles
  let audioToggles = $derived([
    {
      key: "echoCancellation" as const,
      label: t("echoCancellation"),
      description: t("echoCancellationDesc"),
    },
    {
      key: "noiseSuppression" as const,
      label: t("noiseSuppression"),
      description: t("noiseSuppressionDesc"),
    },
    {
      key: "autoGainControl" as const,
      label: t("autoGainControl"),
      description: t("autoGainControlDesc"),
    },
  ]);
</script>

{#if isOpen}
  <div class="fw-dev-mode-panel">
    <!-- Header with tabs -->
    <div class="fw-dev-mode-tabs">
      <button
        type="button"
        class="fw-dev-mode-tab {activeTab === 'audio' ? 'fw-dev-mode-tab--active' : ''}"
        onclick={() => (activeTab = "audio")}
      >
        {t("audio")}
      </button>
      <button
        type="button"
        class="fw-dev-mode-tab {activeTab === 'stats' ? 'fw-dev-mode-tab--active' : ''}"
        onclick={() => (activeTab = "stats")}
      >
        {t("stats")}
      </button>
      <button
        type="button"
        class="fw-dev-mode-tab {activeTab === 'info' ? 'fw-dev-mode-tab--active' : ''}"
        onclick={() => (activeTab = "info")}
      >
        {t("info")}
      </button>
      {#if compositorEnabled}
        <button
          type="button"
          class="fw-dev-mode-tab {activeTab === 'compositor' ? 'fw-dev-mode-tab--active' : ''}"
          onclick={() => (activeTab = "compositor")}
        >
          {t("comp")}
        </button>
      {/if}
      <div style="flex: 1;"></div>
      <button
        type="button"
        class="fw-dev-mode-close"
        onclick={onClose}
        aria-label={t("closeAdvancedPanel")}
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

    <!-- Audio Tab -->
    {#if activeTab === "audio"}
      <div class="fw-dev-mode-content">
        <!-- Master Volume -->
        <div class="fw-dev-mode-section">
          <div class="fw-dev-mode-section-header">{t("masterVolume")}</div>
          <div class="fw-dev-mode-volume-control">
            <VolumeSlider value={masterVolume} onChange={onMasterVolumeChange} max={2} />
            <span
              class="fw-dev-mode-volume-value {masterVolume > 1
                ? 'fw-dev-mode-volume--boosted'
                : ''}"
            >
              {Math.round(masterVolume * 100)}%
            </span>
          </div>
          {#if masterVolume > 1}
            <div class="fw-dev-mode-boost-label">
              +{((masterVolume - 1) * 100).toFixed(0)}% boost
            </div>
          {/if}
        </div>

        <!-- Audio Level Meter -->
        <div class="fw-dev-mode-section">
          <div class="fw-dev-mode-section-header">{t("outputLevel")}</div>
          <div class="fw-dev-mode-meter">
            <div
              class="fw-dev-mode-meter-fill"
              style="width: {audioLevel * 100}%; background: {audioLevel > 0.9
                ? 'hsl(var(--fw-sc-danger))'
                : audioLevel > 0.7
                  ? 'hsl(var(--fw-sc-warning))'
                  : 'hsl(var(--fw-sc-success))'};"
            ></div>
          </div>
          <div class="fw-dev-mode-meter-labels">
            <span>-60dB</span>
            <span>0dB</span>
          </div>
        </div>

        <!-- Audio Mixing Status -->
        <div class="fw-dev-mode-section">
          <div class="fw-dev-mode-row">
            <span class="fw-dev-mode-section-header">{t("audioMixing")}</span>
            <span
              class="fw-dev-mode-badge {audioMixingEnabled ? 'fw-dev-mode-badge--success' : ''}"
            >
              {audioMixingEnabled ? t("on") : t("off")}
            </span>
          </div>
          {#if audioMixingEnabled}
            <div class="fw-dev-mode-hint">{t("compressorLimiterActive")}</div>
          {/if}
        </div>

        <!-- Audio Processing Controls -->
        <div class="fw-dev-mode-section fw-dev-mode-section--flush">
          <div class="fw-dev-mode-section-header-bg">
            <span class="fw-dev-mode-section-header">{t("processing")}</span>
            <span class="fw-dev-mode-profile-tag">profile: {qualityProfile}</span>
          </div>
          {#each audioToggles as toggle, idx (toggle.key)}
            {@const isModified = audioProcessing[toggle.key] !== profileDefaults[toggle.key]}
            <div class="fw-dev-mode-toggle-row {idx > 0 ? 'fw-dev-mode-toggle-row--bordered' : ''}">
              <div class="fw-dev-mode-toggle-info">
                <div class="fw-dev-mode-toggle-label">
                  {toggle.label}
                  {#if isModified}
                    <span class="fw-dev-mode-modified-badge">{t("modified")}</span>
                  {/if}
                </div>
                <div class="fw-dev-mode-toggle-desc">{toggle.description}</div>
              </div>
              <button
                type="button"
                class="fw-dev-mode-switch {audioProcessing[toggle.key]
                  ? 'fw-dev-mode-switch--on'
                  : ''}"
                onclick={() =>
                  onAudioProcessingChange({ [toggle.key]: !audioProcessing[toggle.key] })}
                role="switch"
                aria-checked={audioProcessing[toggle.key]}
                aria-label={toggle.label}
              >
                <span class="fw-dev-mode-switch-thumb"></span>
              </button>
            </div>
          {/each}
          <div class="fw-dev-mode-info-row">
            <span>{t("sampleRate")}</span>
            <span class="fw-dev-mode-mono">{profileDefaults.sampleRate} Hz</span>
          </div>
          <div class="fw-dev-mode-info-row">
            <span>{t("channels")}</span>
            <span class="fw-dev-mode-mono">{profileDefaults.channelCount}</span>
          </div>
        </div>
      </div>
    {/if}

    <!-- Stats Tab -->
    {#if activeTab === "stats"}
      <div class="fw-dev-mode-content">
        <!-- Connection State -->
        <div class="fw-dev-mode-section">
          <div class="fw-dev-mode-section-header">{t("connection")}</div>
          <div class="fw-dev-mode-state fw-dev-mode-state--{ingestState}">
            {ingestState.charAt(0).toUpperCase() + ingestState.slice(1)}
          </div>
        </div>

        <!-- Stats -->
        {#if stats}
          <div class="fw-dev-mode-section fw-dev-mode-section--flush">
            <div class="fw-dev-mode-info-row">
              <span>{t("bitrate")}</span>
              <span>{formatBitrate(stats.video.bitrate + stats.audio.bitrate)}</span>
            </div>
            <div class="fw-dev-mode-info-row">
              <span>{t("video")}</span>
              <span class="fw-dev-mode-value--primary">{formatBitrate(stats.video.bitrate)}</span>
            </div>
            <div class="fw-dev-mode-info-row">
              <span>{t("audio")}</span>
              <span class="fw-dev-mode-value--primary">{formatBitrate(stats.audio.bitrate)}</span>
            </div>
            <div class="fw-dev-mode-info-row">
              <span>{t("frameRate")}</span>
              <span>{stats.video.framesPerSecond.toFixed(0)} fps</span>
            </div>
            <div class="fw-dev-mode-info-row">
              <span>{t("framesEncoded")}</span>
              <span>{stats.video.framesEncoded}</span>
            </div>
            {#if stats.video.packetsLost > 0 || stats.audio.packetsLost > 0}
              <div class="fw-dev-mode-info-row">
                <span>{t("packetsLost")}</span>
                <span class="fw-dev-mode-value--error"
                  >{stats.video.packetsLost + stats.audio.packetsLost}</span
                >
              </div>
            {/if}
            <div class="fw-dev-mode-info-row">
              <span>{t("rtt")}</span>
              <span class={stats.connection.rtt > 200 ? "fw-dev-mode-value--warning" : ""}
                >{stats.connection.rtt.toFixed(0)} ms</span
              >
            </div>
            <div class="fw-dev-mode-info-row">
              <span>{t("iceState")}</span>
              <span style="text-transform: capitalize;">{stats.connection.iceState}</span>
            </div>
          </div>
        {:else}
          <div class="fw-dev-mode-empty">
            {ingestState === "streaming" ? t("waitingForStats") : t("startStreamingForStats")}
          </div>
        {/if}

        <!-- Error -->
        {#if error}
          <div class="fw-dev-mode-error">
            <div class="fw-dev-mode-section-header fw-dev-mode-section-header--error">
              {t("error")}
            </div>
            <div class="fw-dev-mode-error-text">{error}</div>
          </div>
        {/if}
      </div>
    {/if}

    <!-- Info Tab -->
    {#if activeTab === "info"}
      <div class="fw-dev-mode-content">
        <!-- Quality Profile -->
        <div class="fw-dev-mode-section">
          <div class="fw-dev-mode-section-header">{t("qualityProfile")}</div>
          <div class="fw-dev-mode-profile-name">{qualityProfile}</div>
          <div class="fw-dev-mode-hint">
            {profileEncoderSettings.video.width}x{profileEncoderSettings.video.height} @ {formatBitrate(
              profileEncoderSettings.video.bitrate
            )}
          </div>
        </div>

        <!-- WHIP URL -->
        <div class="fw-dev-mode-section">
          <div class="fw-dev-mode-section-header">{t("whipEndpoint")}</div>
          <div class="fw-dev-mode-url">{whipUrl || t("notConfigured")}</div>
          {#if whipUrl}
            <button type="button" class="fw-dev-mode-copy-btn" onclick={copyWhipUrl}
              >{t("copyUrl")}</button
            >
          {/if}
        </div>

        <!-- Encoder Settings -->
        <div class="fw-dev-mode-section fw-dev-mode-section--flush">
          <div class="fw-dev-mode-section-header-bg">
            <span class="fw-dev-mode-section-header">{t("encoder")}</span>
            {#if hasEncoderOverrides}
              <button
                type="button"
                class="fw-dev-mode-reset-btn"
                onclick={() => onEncoderOverridesChange?.({})}
              >
                {t("resetToProfile")}
              </button>
            {/if}
          </div>
          <div class="fw-dev-mode-info-row">
            <span>{t("videoCodec")}</span>
            <span class="fw-dev-mode-mono">{effectiveEncoderConfig.video.codec}</span>
          </div>
          <div class="fw-dev-mode-info-row">
            <span>{t("resolution")}</span>
            <select
              class="fw-dev-mode-select {encoderOverrides?.video?.width ||
              encoderOverrides?.video?.height
                ? 'fw-dev-mode-select--overridden'
                : ''}"
              value={currentResolution}
              disabled={ingestState === "streaming"}
              onchange={(e) => {
                const [w, h] = (e.target as HTMLSelectElement).value.split("x").map(Number);
                const isProfileDefault =
                  w === profileEncoderSettings.video.width &&
                  h === profileEncoderSettings.video.height;
                onEncoderOverridesChange?.({
                  ...encoderOverrides,
                  video: {
                    ...encoderOverrides?.video,
                    width: isProfileDefault ? undefined : w,
                    height: isProfileDefault ? undefined : h,
                  },
                });
              }}
            >
              {#each RESOLUTION_OPTIONS as opt}
                <option value={opt.value}>{opt.label}</option>
              {/each}
            </select>
          </div>
          {#if videoTrackSettings?.width && videoTrackSettings?.height}
            <div class="fw-dev-mode-info-row">
              <span>{t("actualResolution")}</span>
              <span class="fw-dev-mode-mono"
                >{Math.round(videoTrackSettings.width)}x{Math.round(
                  videoTrackSettings.height
                )}</span
              >
            </div>
          {/if}
          <div class="fw-dev-mode-info-row">
            <span>{t("framerate")}</span>
            <select
              class="fw-dev-mode-select {encoderOverrides?.video?.framerate
                ? 'fw-dev-mode-select--overridden'
                : ''}"
              value={encoderOverrides?.video?.framerate ?? profileEncoderSettings.video.framerate}
              disabled={ingestState === "streaming"}
              onchange={(e) => {
                const value = Number((e.target as HTMLSelectElement).value);
                const isProfileDefault = value === profileEncoderSettings.video.framerate;
                onEncoderOverridesChange?.({
                  ...encoderOverrides,
                  video: {
                    ...encoderOverrides?.video,
                    framerate: isProfileDefault ? undefined : value,
                  },
                });
              }}
            >
              {#each FRAMERATE_OPTIONS as opt}
                <option value={opt.value}>{opt.label}</option>
              {/each}
            </select>
          </div>
          {#if videoTrackSettings?.frameRate}
            <div class="fw-dev-mode-info-row">
              <span>{t("actualFramerate")}</span>
              <span class="fw-dev-mode-mono">{Math.round(videoTrackSettings.frameRate)} fps</span>
            </div>
          {/if}
          <div class="fw-dev-mode-info-row">
            <span>{t("videoBitrate")}</span>
            <select
              class="fw-dev-mode-select {encoderOverrides?.video?.bitrate
                ? 'fw-dev-mode-select--overridden'
                : ''}"
              value={encoderOverrides?.video?.bitrate ?? profileEncoderSettings.video.bitrate}
              disabled={ingestState === "streaming"}
              onchange={(e) => {
                const value = Number((e.target as HTMLSelectElement).value);
                const isProfileDefault = value === profileEncoderSettings.video.bitrate;
                onEncoderOverridesChange?.({
                  ...encoderOverrides,
                  video: {
                    ...encoderOverrides?.video,
                    bitrate: isProfileDefault ? undefined : value,
                  },
                });
              }}
            >
              {#each VIDEO_BITRATE_OPTIONS as opt}
                <option value={opt.value}>{opt.label}</option>
              {/each}
            </select>
          </div>
          <div class="fw-dev-mode-info-row">
            <span>{t("audioCodec")}</span>
            <span class="fw-dev-mode-mono">{effectiveEncoderConfig.audio.codec}</span>
          </div>
          <div class="fw-dev-mode-info-row">
            <span>{t("audioBitrate")}</span>
            <select
              class="fw-dev-mode-select {encoderOverrides?.audio?.bitrate
                ? 'fw-dev-mode-select--overridden'
                : ''}"
              value={encoderOverrides?.audio?.bitrate ?? profileEncoderSettings.audio.bitrate}
              disabled={ingestState === "streaming"}
              onchange={(e) => {
                const value = Number((e.target as HTMLSelectElement).value);
                const isProfileDefault = value === profileEncoderSettings.audio.bitrate;
                onEncoderOverridesChange?.({
                  ...encoderOverrides,
                  audio: {
                    ...encoderOverrides?.audio,
                    bitrate: isProfileDefault ? undefined : value,
                  },
                });
              }}
            >
              {#each AUDIO_BITRATE_OPTIONS as opt}
                <option value={opt.value}>{opt.label}</option>
              {/each}
            </select>
          </div>
          {#if ingestState === "streaming"}
            <div class="fw-dev-mode-locked-notice">{t("settingsLockedWhileStreaming")}</div>
          {/if}
        </div>

        <!-- Sources -->
        <div class="fw-dev-mode-section fw-dev-mode-section--flush">
          <div class="fw-dev-mode-section-header-bg">
            <span class="fw-dev-mode-section-header">{t("sources")} ({sources.length})</span>
          </div>
          {#if sources.length > 0}
            {#each sources as source, idx (source.id)}
              <div
                class="fw-dev-mode-source-row {idx > 0 ? 'fw-dev-mode-source-row--bordered' : ''}"
              >
                <div class="fw-dev-mode-source-header">
                  <span class="fw-dev-mode-source-type fw-dev-mode-source-type--{source.type}">
                    {source.type.toUpperCase()}
                  </span>
                  <span class="fw-dev-mode-source-label">{source.label}</span>
                </div>
                <div class="fw-dev-mode-source-meta">
                  <span>Vol: {Math.round(source.volume * 100)}%</span>
                  {#if source.muted}
                    <span class="fw-dev-mode-value--error">{t("mute")}</span>
                  {/if}
                  {#if !source.active}
                    <span class="fw-dev-mode-value--warning">{t("inactive")}</span>
                  {/if}
                </div>
              </div>
            {/each}
          {:else}
            <div class="fw-dev-mode-empty">{t("noSourcesAdded")}</div>
          {/if}
        </div>
      </div>
    {/if}

    <!-- Compositor Tab -->
    {#if activeTab === "compositor" && compositorEnabled}
      <div class="fw-dev-mode-content">
        <!-- Renderer Info -->
        <div class="fw-dev-mode-section">
          <div class="fw-dev-mode-section-header">{t("renderer")}</div>
          <div class="fw-dev-mode-renderer fw-dev-mode-renderer--{compositorRendererType}">
            {#if compositorRendererType === "webgpu"}
              WebGPU
            {:else if compositorRendererType === "webgl"}
              WebGL
            {:else if compositorRendererType === "canvas2d"}
              Canvas2D
            {:else}
              {t("notInitialized")}
            {/if}
          </div>
          <div class="fw-dev-mode-hint">{t("setRendererHint")}</div>
        </div>

        <!-- Stats -->
        {#if compositorStats}
          <div class="fw-dev-mode-section fw-dev-mode-section--flush">
            <div class="fw-dev-mode-section-header-bg">
              <span class="fw-dev-mode-section-header">{t("performance")}</span>
            </div>
            <div class="fw-dev-mode-info-row">
              <span>{t("frameRate")}</span>
              <span class="fw-dev-mode-mono">{compositorStats.fps} fps</span>
            </div>
            <div class="fw-dev-mode-info-row">
              <span>{t("frameTime")}</span>
              <span
                class="fw-dev-mode-mono {compositorStats.frameTimeMs > 16
                  ? 'fw-dev-mode-value--warning'
                  : ''}"
              >
                {compositorStats.frameTimeMs.toFixed(2)} ms
              </span>
            </div>
            {#if compositorStats.gpuMemoryMB !== undefined}
              <div class="fw-dev-mode-info-row">
                <span>{t("gpuMemory")}</span>
                <span class="fw-dev-mode-mono">{compositorStats.gpuMemoryMB.toFixed(1)} MB</span>
              </div>
            {/if}
          </div>
        {/if}

        <!-- Scenes & Layers -->
        <div class="fw-dev-mode-section fw-dev-mode-section--flush">
          <div class="fw-dev-mode-section-header-bg">
            <span class="fw-dev-mode-section-header">{t("composition")}</span>
          </div>
          <div class="fw-dev-mode-info-row">
            <span>{t("scenes")}</span>
            <span class="fw-dev-mode-mono">{sceneCount}</span>
          </div>
          <div class="fw-dev-mode-info-row">
            <span>{t("layers")}</span>
            <span class="fw-dev-mode-mono">{layerCount}</span>
          </div>
        </div>

        <!-- Encoder Section -->
        <div class="fw-dev-mode-section fw-dev-mode-section--flush">
          <div class="fw-dev-mode-section-header-bg">
            <span class="fw-dev-mode-section-header">{t("encoder")}</span>
          </div>
          <div class="fw-dev-mode-info-row">
            <span>{t("type")}</span>
            <span
              class="fw-dev-mode-encoder-badge {useWebCodecs && isWebCodecsAvailable
                ? 'fw-dev-mode-encoder-badge--webcodecs'
                : 'fw-dev-mode-encoder-badge--browser'}"
            >
              {useWebCodecs && isWebCodecsAvailable ? t("webCodecs") : t("browser")}
              {#if ingestState === "streaming"}
                <span style="opacity: 0.7; margin-left: 4px;">
                  {isWebCodecsActive ? "(active)" : "(pending)"}
                </span>
              {/if}
            </span>
          </div>
          <div class="fw-dev-mode-toggle-row">
            <div class="fw-dev-mode-toggle-info">
              <div class="fw-dev-mode-toggle-label">{t("useWebCodecs")}</div>
              <div class="fw-dev-mode-toggle-desc">
                {ingestState === "streaming" ? t("changeTakesEffect") : t("enableWebCodecsDesc")}
              </div>
            </div>
            <button
              type="button"
              class="fw-dev-mode-switch {useWebCodecs ? 'fw-dev-mode-switch--on' : ''}"
              onclick={() => onUseWebCodecsChange?.(!useWebCodecs)}
              disabled={ingestState === "streaming" || !isWebCodecsAvailable}
              role="switch"
              aria-checked={useWebCodecs}
              aria-label={t("useWebCodecs")}
            >
              <span class="fw-dev-mode-switch-thumb"></span>
            </button>
          </div>
          {#if !isWebCodecsAvailable}
            <div class="fw-dev-mode-locked-notice fw-dev-mode-value--error">
              {t("webCodecsUnsupported")}
            </div>
          {/if}
          {#if isWebCodecsAvailable && ingestState === "streaming" && useWebCodecs !== isWebCodecsActive}
            <div class="fw-dev-mode-locked-notice fw-dev-mode-value--warning">
              {t("changeTakesEffect")}
            </div>
          {/if}
        </div>

        <!-- WebCodecs Encoder Stats -->
        {#if isWebCodecsActive && encoderStats}
          <div class="fw-dev-mode-section fw-dev-mode-section--flush">
            <div class="fw-dev-mode-section-header-bg">
              <span class="fw-dev-mode-section-header">{t("encoderStats")}</span>
            </div>
            <div class="fw-dev-mode-info-row">
              <span>{t("videoFrames")}</span>
              <span class="fw-dev-mode-mono">{encoderStats.video.framesEncoded}</span>
            </div>
            <div class="fw-dev-mode-info-row">
              <span>{t("videoPending")}</span>
              <span
                class="fw-dev-mode-mono {encoderStats.video.framesPending > 5
                  ? 'fw-dev-mode-value--warning'
                  : ''}"
              >
                {encoderStats.video.framesPending}
              </span>
            </div>
            <div class="fw-dev-mode-info-row">
              <span>{t("videoBytes")}</span>
              <span class="fw-dev-mode-mono"
                >{(encoderStats.video.bytesEncoded / 1024 / 1024).toFixed(2)} MB</span
              >
            </div>
            <div class="fw-dev-mode-info-row">
              <span>{t("audioSamples")}</span>
              <span class="fw-dev-mode-mono">{encoderStats.audio.samplesEncoded}</span>
            </div>
            <div class="fw-dev-mode-info-row">
              <span>{t("audioBytes")}</span>
              <span class="fw-dev-mode-mono"
                >{(encoderStats.audio.bytesEncoded / 1024).toFixed(1)} KB</span
              >
            </div>
          </div>
        {/if}

        <!-- Info -->
        <div class="fw-dev-mode-section">
          <div class="fw-dev-mode-compositor-info">
            {#if useWebCodecs && isWebCodecsAvailable}
              Using WebCodecs encoder via RTCRtpScriptTransform for lower latency and better
              control.
            {:else}
              Using browser's built-in MediaStream encoder. Toggle WebCodecs for advanced encoding.
            {/if}
          </div>
        </div>
      </div>
    {/if}
  </div>
{/if}

<style>
  .fw-dev-mode-panel {
    background: hsl(var(--fw-sc-surface-deep));
    border-left: 1px solid hsl(var(--fw-sc-border) / 0.5);
    color: hsl(var(--fw-sc-text-muted));
    font-size: 12px;
    font-family:
      ui-monospace,
      SFMono-Regular,
      SF Mono,
      Menlo,
      Consolas,
      monospace;
    width: 280px;
    display: flex;
    flex-direction: column;
    height: 100%;
    flex-shrink: 0;
    z-index: 40;
  }

  .fw-dev-mode-tabs {
    display: flex;
    align-items: center;
    border-bottom: 1px solid hsl(var(--fw-sc-border) / 0.3);
    background: hsl(var(--fw-sc-surface));
  }

  .fw-dev-mode-tab {
    padding: 8px 12px;
    font-size: 10px;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    font-weight: 600;
    transition: all 0.15s;
    border-right: 1px solid hsl(var(--fw-sc-border) / 0.3);
    background: transparent;
    color: hsl(var(--fw-sc-text-faint));
    cursor: pointer;
    border: none;
    border-right: 1px solid hsl(var(--fw-sc-border) / 0.3);
  }

  .fw-dev-mode-tab:hover {
    color: hsl(var(--fw-sc-text-muted));
  }

  .fw-dev-mode-tab--active {
    background: hsl(var(--fw-sc-surface-deep));
    color: hsl(var(--fw-sc-text));
  }

  .fw-dev-mode-close {
    color: hsl(var(--fw-sc-text-faint));
    background: transparent;
    border: none;
    padding: 8px;
    cursor: pointer;
    transition: color 0.15s;
  }

  .fw-dev-mode-close:hover {
    color: hsl(var(--fw-sc-text-muted));
  }

  .fw-dev-mode-content {
    flex: 1;
    overflow-y: auto;
  }

  .fw-dev-mode-section {
    padding: 12px;
    border-bottom: 1px solid hsl(var(--fw-sc-border) / 0.3);
  }

  .fw-dev-mode-section--flush {
    padding: 0;
  }

  .fw-dev-mode-section-header {
    font-size: 10px;
    color: hsl(var(--fw-sc-text-faint));
    text-transform: uppercase;
    letter-spacing: 0.05em;
    font-weight: 600;
    margin-bottom: 8px;
  }

  .fw-dev-mode-section-header-bg {
    padding: 8px 12px;
    background: hsl(var(--fw-sc-surface));
    display: flex;
    align-items: center;
    justify-content: space-between;
  }

  .fw-dev-mode-section-header-bg .fw-dev-mode-section-header {
    margin-bottom: 0;
  }

  .fw-dev-mode-profile-tag {
    font-size: 9px;
    color: hsl(var(--fw-sc-text-faint));
    font-family: monospace;
  }

  .fw-dev-mode-row {
    display: flex;
    justify-content: space-between;
    align-items: center;
  }

  .fw-dev-mode-volume-control {
    display: flex;
    align-items: center;
    gap: 12px;
  }

  .fw-dev-mode-slider {
    flex: 1;
    height: 6px;
    border-radius: 3px;
    cursor: pointer;
    accent-color: hsl(var(--fw-sc-accent));
  }

  .fw-dev-mode-slider--boosted {
    accent-color: hsl(var(--fw-sc-warning));
  }

  .fw-dev-mode-volume-value {
    font-size: 14px;
    font-family: monospace;
    min-width: 48px;
    text-align: right;
    color: hsl(var(--fw-sc-text));
  }

  .fw-dev-mode-volume--boosted {
    color: hsl(var(--fw-sc-warning));
  }

  .fw-dev-mode-boost-label {
    font-size: 10px;
    color: hsl(var(--fw-sc-warning));
    margin-top: 4px;
  }

  .fw-dev-mode-meter {
    height: 8px;
    background: hsl(var(--fw-sc-border) / 0.3);
    border-radius: 4px;
    overflow: hidden;
  }

  .fw-dev-mode-meter-fill {
    height: 100%;
    transition: all 75ms;
  }

  .fw-dev-mode-meter-labels {
    display: flex;
    justify-content: space-between;
    font-size: 10px;
    color: hsl(var(--fw-sc-text-faint));
    margin-top: 4px;
  }

  .fw-dev-mode-badge {
    font-size: 12px;
    font-family: monospace;
    padding: 2px 6px;
    background: hsl(var(--fw-sc-border) / 0.3);
    color: hsl(var(--fw-sc-text-faint));
  }

  .fw-dev-mode-badge--success {
    background: hsl(var(--fw-sc-success) / 0.2);
    color: hsl(var(--fw-sc-success));
  }

  .fw-dev-mode-hint {
    font-size: 10px;
    color: hsl(var(--fw-sc-text-faint));
    margin-top: 4px;
  }

  .fw-dev-mode-toggle-row {
    padding: 10px 12px;
    display: flex;
    justify-content: space-between;
    align-items: center;
  }

  .fw-dev-mode-toggle-row--bordered {
    border-top: 1px solid hsl(var(--fw-sc-border) / 0.2);
  }

  .fw-dev-mode-toggle-info {
    flex: 1;
    min-width: 0;
  }

  .fw-dev-mode-toggle-label {
    color: hsl(var(--fw-sc-text));
    font-size: 12px;
    display: flex;
    align-items: center;
    gap: 8px;
  }

  .fw-dev-mode-toggle-desc {
    font-size: 10px;
    color: hsl(var(--fw-sc-text-faint));
    margin-top: 2px;
  }

  .fw-dev-mode-modified-badge {
    font-size: 8px;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: hsl(var(--fw-sc-warning));
    background: hsl(var(--fw-sc-warning) / 0.2);
    padding: 2px 4px;
  }

  .fw-dev-mode-switch {
    position: relative;
    display: inline-flex;
    height: 20px;
    width: 36px;
    flex-shrink: 0;
    cursor: pointer;
    border-radius: 10px;
    border: 2px solid transparent;
    background: hsl(var(--fw-sc-border));
    transition: background-color 0.2s;
  }

  .fw-dev-mode-switch--on {
    background: hsl(var(--fw-sc-accent));
  }

  .fw-dev-mode-switch-thumb {
    display: inline-block;
    height: 16px;
    width: 16px;
    border-radius: 50%;
    background: white;
    box-shadow: 0 2px 4px rgba(0, 0, 0, 0.3);
    transition: transform 0.2s;
    transform: translateX(0);
  }

  .fw-dev-mode-switch--on .fw-dev-mode-switch-thumb {
    transform: translateX(16px);
  }

  .fw-dev-mode-info-row {
    display: flex;
    justify-content: space-between;
    padding: 8px 12px;
    border-top: 1px solid hsl(var(--fw-sc-border) / 0.2);
    color: hsl(var(--fw-sc-text-faint));
  }

  .fw-dev-mode-info-row span:last-child {
    color: hsl(var(--fw-sc-text));
  }

  .fw-dev-mode-mono {
    font-family: monospace;
  }

  .fw-dev-mode-value--primary {
    color: hsl(var(--fw-sc-accent)) !important;
  }

  .fw-dev-mode-value--error {
    color: hsl(var(--fw-sc-danger)) !important;
  }

  .fw-dev-mode-value--warning {
    color: hsl(var(--fw-sc-warning)) !important;
  }

  .fw-dev-mode-empty {
    color: hsl(var(--fw-sc-text-faint));
    text-align: center;
    padding: 24px;
  }

  .fw-dev-mode-error {
    padding: 12px;
    border-top: 1px solid hsl(var(--fw-sc-danger) / 0.3);
    background: hsl(var(--fw-sc-danger) / 0.1);
  }

  .fw-dev-mode-section-header--error {
    color: hsl(var(--fw-sc-danger));
    margin-bottom: 4px;
  }

  .fw-dev-mode-error-text {
    font-size: 12px;
    color: hsl(var(--fw-sc-danger));
  }

  .fw-dev-mode-state {
    font-size: 14px;
    font-weight: 600;
    color: hsl(var(--fw-sc-text));
  }

  .fw-dev-mode-state--streaming {
    color: hsl(var(--fw-sc-success));
  }

  .fw-dev-mode-state--connecting {
    color: hsl(var(--fw-sc-accent));
  }

  .fw-dev-mode-state--error {
    color: hsl(var(--fw-sc-danger));
  }

  .fw-dev-mode-profile-name {
    font-size: 14px;
    color: hsl(var(--fw-sc-text));
    text-transform: capitalize;
  }

  .fw-dev-mode-url {
    font-size: 12px;
    color: hsl(var(--fw-sc-accent));
    word-break: break-all;
  }

  .fw-dev-mode-copy-btn {
    margin-top: 8px;
    font-size: 10px;
    color: hsl(var(--fw-sc-text-faint));
    background: transparent;
    border: none;
    cursor: pointer;
    padding: 0;
    transition: color 0.15s;
  }

  .fw-dev-mode-copy-btn:hover {
    color: hsl(var(--fw-sc-text-muted));
  }

  .fw-dev-mode-reset-btn {
    font-size: 10px;
    color: hsl(var(--fw-sc-accent-secondary));
    background: transparent;
    border: none;
    cursor: pointer;
    padding: 2px 6px;
  }

  .fw-dev-mode-reset-btn:hover {
    color: hsl(var(--fw-sc-text));
  }

  .fw-dev-mode-select {
    background: hsl(var(--fw-sc-border) / 0.3);
    border: 1px solid hsl(var(--fw-sc-border) / 0.5);
    border-radius: 4px;
    color: hsl(var(--fw-sc-text));
    padding: 4px 8px;
    font-size: 12px;
    font-family: inherit;
    cursor: pointer;
    min-width: 100px;
    text-align: right;
  }

  .fw-dev-mode-select:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  .fw-dev-mode-select--overridden {
    background: hsl(var(--fw-sc-accent-secondary) / 0.15);
    border-color: hsl(var(--fw-sc-accent-secondary) / 0.4);
    color: hsl(var(--fw-sc-accent-secondary));
  }

  .fw-dev-mode-locked-notice {
    padding: 8px 12px;
    font-size: 10px;
    color: hsl(var(--fw-sc-warning));
  }

  .fw-dev-mode-source-row {
    padding: 8px 12px;
  }

  .fw-dev-mode-source-row--bordered {
    border-top: 1px solid hsl(var(--fw-sc-border) / 0.2);
  }

  .fw-dev-mode-source-header {
    display: flex;
    align-items: center;
    gap: 8px;
  }

  .fw-dev-mode-source-type {
    font-size: 10px;
    font-family: monospace;
    padding: 2px 6px;
    text-transform: uppercase;
  }

  .fw-dev-mode-source-type--camera {
    background: hsl(var(--fw-sc-accent) / 0.2);
    color: hsl(var(--fw-sc-accent));
  }

  .fw-dev-mode-source-type--screen {
    background: hsl(var(--fw-sc-success) / 0.2);
    color: hsl(var(--fw-sc-success));
  }

  .fw-dev-mode-source-type--custom {
    background: hsl(var(--fw-sc-warning) / 0.2);
    color: hsl(var(--fw-sc-warning));
  }

  .fw-dev-mode-source-label {
    color: hsl(var(--fw-sc-text));
    font-size: 12px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .fw-dev-mode-source-meta {
    display: flex;
    gap: 12px;
    margin-top: 4px;
    font-size: 10px;
    color: hsl(var(--fw-sc-text-faint));
  }

  .fw-dev-mode-renderer {
    font-size: 14px;
    font-weight: 600;
    color: hsl(var(--fw-sc-text));
  }

  .fw-dev-mode-renderer--webgpu {
    color: hsl(var(--fw-sc-accent-secondary));
  }

  .fw-dev-mode-renderer--webgl {
    color: hsl(var(--fw-sc-accent));
  }

  .fw-dev-mode-renderer--canvas2d {
    color: hsl(var(--fw-sc-success));
  }

  .fw-dev-mode-compositor-info {
    font-size: 10px;
    color: hsl(var(--fw-sc-text-faint));
    line-height: 1.5;
  }

  .fw-dev-mode-encoder-badge {
    font-size: 12px;
    font-family: monospace;
    padding: 2px 6px;
  }

  .fw-dev-mode-encoder-badge--webcodecs {
    background: hsl(var(--fw-sc-accent-secondary) / 0.2);
    color: hsl(var(--fw-sc-accent-secondary));
  }

  .fw-dev-mode-encoder-badge--browser {
    background: hsl(var(--fw-sc-accent) / 0.2);
    color: hsl(var(--fw-sc-accent));
  }

  .fw-dev-mode-switch:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }
</style>

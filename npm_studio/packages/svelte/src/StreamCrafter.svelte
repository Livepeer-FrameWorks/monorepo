<!--
  StreamCrafter Svelte Component
  Self-contained browser-based WHIP streaming component
  Uses slab design system with Tokyo Night colors

  @example
  import { StreamCrafter } from '@livepeer-frameworks/streamcrafter-svelte';
  import '@livepeer-frameworks/streamcrafter-svelte/streamcrafter.css';
-->
<script lang="ts">
  import { onMount, onDestroy, untrack, setContext } from "svelte";
  import type { Snippet } from "svelte";

  import {
    createStreamCrafterContextV2,
    setStreamCrafterContextV2,
    type StreamCrafterV2State,
  } from "./stores/streamCrafterContextV2";
  import { createAudioLevelsStore, type AudioLevelsState } from "./stores/audioLevels.svelte";
  import { createCompositorStore, type CompositorState } from "./stores/compositor";
  import { createIngestEndpointsStore, type IngestEndpointsState } from "./stores/ingestEndpoints";
  import CompositorControls from "./components/CompositorControls.svelte";
  import AdvancedPanel, { type AudioProcessingSettings } from "./components/AdvancedPanel.svelte";
  import VolumeSlider from "./components/VolumeSlider.svelte";

  // Icons
  import CameraIcon from "./icons/CameraIcon.svelte";
  import MonitorIcon from "./icons/MonitorIcon.svelte";
  import MicIcon from "./icons/MicIcon.svelte";
  import XIcon from "./icons/XIcon.svelte";
  import SettingsIcon from "./icons/SettingsIcon.svelte";
  import VideoIcon from "./icons/VideoIcon.svelte";
  import EyeIcon from "./icons/EyeIcon.svelte";
  import ChevronsRightIcon from "./icons/ChevronsRightIcon.svelte";
  import ChevronsLeftIcon from "./icons/ChevronsLeftIcon.svelte";

  import type {
    QualityProfile,
    IngestState,
    IngestStateContextV2,
    MediaSource,
    ReconnectionState,
    CompositorConfig,
    EncoderOverrides,
    FwThemePreset,
    StudioThemeOverrides,
    StudioLocale,
  } from "@livepeer-frameworks/streamcrafter-core";
  import {
    isWebCodecsEncodingPathSupported,
    detectCapabilities,
    resolveStudioTheme,
    studioThemeOverridesToStyle,
  } from "@livepeer-frameworks/streamcrafter-core";
  import { studioLocaleStore, studioTranslatorStore } from "./stores/i18n";

  // Props
  interface Props {
    /** Direct WHIP endpoint URL */
    whipUrl?: string;
    /** Gateway URL for endpoint resolution (alternative to whipUrl) */
    gatewayUrl?: string;
    /** Stream key for gateway mode */
    streamKey?: string;
    /** Initial quality profile */
    initialProfile?: QualityProfile;
    /** Auto-start camera on mount */
    autoStartCamera?: boolean;
    /** Show settings panel by default */
    showSettings?: boolean;
    /** Enable dev mode UI */
    devMode?: boolean;
    /** Enable debug logging */
    debug?: boolean;
    /** Enable compositor for multi-source composition */
    enableCompositor?: boolean;
    /** Configuration for the compositor */
    compositorConfig?: Partial<CompositorConfig>;
    /** Theme preset or custom overrides */
    theme?: FwThemePreset | StudioThemeOverrides;
    /** Locale for i18n */
    locale?: StudioLocale;
    /** Custom class name */
    class?: string;
    /** State change callback */
    onStateChange?: (state: IngestState, context?: IngestStateContextV2) => void;
    /** Error callback */
    onError?: (error: string) => void;
    /** Composable children — replaces the default UI when provided. */
    children?: Snippet;
  }

  let {
    whipUrl,
    gatewayUrl,
    streamKey,
    initialProfile = "broadcast",
    autoStartCamera = false,
    showSettings: initialShowSettings = false,
    devMode = false,
    debug = false,
    enableCompositor = true,
    compositorConfig = {},
    theme,
    locale,
    class: className = "",
    onStateChange,
    onError,
    children,
  }: Props = $props();

  // Theme
  let themeStyle = $derived.by(() => {
    if (!theme) return "";
    if (typeof theme === "object") {
      return Object.entries(studioThemeOverridesToStyle(theme))
        .map(([k, v]) => `${k}: ${v}`)
        .join("; ");
    }
    const resolved = resolveStudioTheme(theme);
    if (!resolved) return "";
    return Object.entries(studioThemeOverridesToStyle(resolved))
      .map(([k, v]) => `${k}: ${v}`)
      .join("; ");
  });
  let dataTheme = $derived(typeof theme === "string" && theme !== "default" ? theme : undefined);

  // i18n: sync locale prop to store and provide translator context
  $effect(() => {
    studioLocaleStore.set(locale ?? "en");
  });
  setContext("fw-sc-translator", studioTranslatorStore);
  let t = $derived($studioTranslatorStore);

  // Quality profiles
  let QUALITY_PROFILES = $derived([
    {
      id: "professional" as QualityProfile,
      label: t("professional"),
      description: t("professionalDesc"),
    },
    { id: "broadcast" as QualityProfile, label: t("broadcast"), description: t("broadcastDesc") },
    {
      id: "conference" as QualityProfile,
      label: t("conference"),
      description: t("conferenceDesc"),
    },
  ]);

  // State
  let videoEl: HTMLVideoElement;
  let settingsDropdownEl: HTMLDivElement;
  let settingsButtonEl: HTMLButtonElement;
  let contextMenuEl: HTMLDivElement;
  let showSettings = $state(initialShowSettings);
  let showSources = $state(true);
  let contextMenu = $state<{ x: number; y: number } | null>(null);
  let isAdvancedPanelOpen = $state(false);
  let crafterState = $state<StreamCrafterV2State>({
    state: "idle",
    stateContext: {},
    mediaStream: null,
    sources: [],
    isStreaming: false,
    isCapturing: false,
    isReconnecting: false,
    error: null,
    stats: null,
    qualityProfile: initialProfile,
    reconnectionState: null,
    // Encoder
    useWebCodecs: detectCapabilities().recommended === "webcodecs",
    isWebCodecsActive: false,
    encoderStats: null,
  });
  let audioLevels = $state<AudioLevelsState>({
    level: 0,
    peakLevel: 0,
    isMonitoring: false,
  });
  let compositorState = $state<CompositorState>({
    isEnabled: false,
    isInitialized: false,
    rendererType: null,
    stats: null,
    scenes: [],
    activeSceneId: null,
    currentLayout: null,
  });
  let ingestEndpointsState = $state<IngestEndpointsState>({
    endpoints: null,
    status: "idle",
    error: null,
  });
  let masterVolume = $state(1);
  let audioProcessing = $state<AudioProcessingSettings>({
    echoCancellation: true,
    noiseSuppression: true,
    autoGainControl: true,
  });
  let encoderOverrides = $state<EncoderOverrides>({});
  const isWebCodecsAvailable = isWebCodecsEncodingPathSupported();

  // Create store
  const crafter = createStreamCrafterContextV2();
  setStreamCrafterContextV2(crafter);

  // Set rich context with reactive getters for composable sub-components.
  // Svelte 5 tracks getter access automatically via $state.
  setContext("fw-sc-controller", {
    get state() {
      return crafterState.state;
    },
    get stateContext() {
      return crafterState.stateContext;
    },
    get isStreaming() {
      return crafterState.isStreaming;
    },
    get isCapturing() {
      return crafterState.isCapturing;
    },
    get isReconnecting() {
      return crafterState.isReconnecting;
    },
    get error() {
      return crafterState.error;
    },
    get mediaStream() {
      return crafterState.mediaStream;
    },
    get sources() {
      return crafterState.sources;
    },
    get qualityProfile() {
      return crafterState.qualityProfile;
    },
    get reconnectionState() {
      return crafterState.reconnectionState;
    },
    get stats() {
      return crafterState.stats;
    },
    get useWebCodecs() {
      return crafterState.useWebCodecs;
    },
    get isWebCodecsActive() {
      return crafterState.isWebCodecsActive;
    },
    // Actions
    startCamera: (opts?: any) => crafter.startCamera(opts),
    startScreenShare: (opts?: any) => crafter.startScreenShare(opts),
    removeSource: (id: string) => crafter.removeSource(id),
    setSourceVolume: (id: string, vol: number) => crafter.setSourceVolume(id, vol),
    setSourceMuted: (id: string, muted: boolean) => crafter.setSourceMuted(id, muted),
    setPrimaryVideoSource: (id: string) => crafter.setPrimaryVideoSource(id),
    setMasterVolume: (vol: number) => crafter.setMasterVolume(vol),
    setQualityProfile: (p: QualityProfile) => crafter.setQualityProfile(p),
    startStreaming: () => crafter.startStreaming(),
    stopStreaming: () => crafter.stopStreaming(),
    getController: () => crafter.getController(),
    setUseWebCodecs: (enabled: boolean) => crafter.setUseWebCodecs(enabled),
    setEncoderOverrides: (o: any) => crafter.setEncoderOverrides(o),
  });

  // Audio levels store
  let audioLevelsStore: ReturnType<typeof createAudioLevelsStore> | null = null;

  // Compositor store (must be $state for reactivity when assigned in $effect)
  let compositorStore = $state<ReturnType<typeof createCompositorStore> | null>(null);
  let unsubscribeCompositor: (() => void) | undefined;

  // Ingest endpoints store (for gateway resolution)
  let ingestEndpointsStore: ReturnType<typeof createIngestEndpointsStore> | null = null;
  let unsubscribeIngestEndpoints: (() => void) | undefined;

  // Resolve WHIP URL: priority is direct prop > gateway-resolved > undefined
  let resolvedWhipUrl = $derived.by(() => {
    if (whipUrl) return whipUrl;
    if (ingestEndpointsState.endpoints?.primary?.whipUrl) {
      return ingestEndpointsState.endpoints.primary.whipUrl;
    }
    return undefined;
  });

  // Track if we're waiting for gateway resolution
  let isResolvingEndpoint = $derived(
    !whipUrl && gatewayUrl && streamKey && ingestEndpointsState.status === "loading"
  );

  // Subscriptions
  let unsubscribe: (() => void) | undefined;
  let unsubscribeAudioLevels: (() => void) | undefined;

  // Track controller init inputs so we reinitialize when endpoint/profile/debug change.
  let controllerInitKey = "";

  $effect(() => {
    if (resolvedWhipUrl) {
      // Use untrack to read compositorStore without creating a dependency
      // This prevents the effect from re-running when compositorStore changes
      const existingCompositorStore = untrack(() => compositorStore);
      const currentInitKey = `${resolvedWhipUrl}|${initialProfile}|${debug}`;
      const lastInitKey = untrack(() => controllerInitKey);

      if (lastInitKey !== currentInitKey) {
        crafter.initialize({
          whipUrl: resolvedWhipUrl,
          profile: initialProfile,
          debug,
          reconnection: { enabled: true, maxAttempts: 5 },
          audioMixing: true,
        });
        controllerInitKey = currentInitKey;
      }

      // Setup compositor store immediately after controller is created
      if (enableCompositor && !existingCompositorStore) {
        const controller = crafter.getController();
        if (controller) {
          compositorStore = createCompositorStore({
            controller,
            config: compositorConfig,
            autoEnable: true,
          });
          unsubscribeCompositor = compositorStore.subscribe((state) => {
            compositorState = state;
          });
        }
      }
      return;
    }

    controllerInitKey = "";
  });

  // Update video preview when stream changes
  $effect(() => {
    if (videoEl && crafterState.mediaStream) {
      videoEl.srcObject = crafterState.mediaStream;
      videoEl.play().catch(() => {});
    } else if (videoEl) {
      videoEl.srcObject = null;
    }
  });

  // Notify parent of state changes
  $effect(() => {
    onStateChange?.(crafterState.state, crafterState.stateContext);
  });

  // Notify parent of errors
  $effect(() => {
    if (crafterState.error) {
      onError?.(crafterState.error);
    }
  });

  // Auto-start camera
  $effect(() => {
    if (autoStartCamera && resolvedWhipUrl && crafterState.state === "idle") {
      handleStartCamera();
    }
  });

  // Click-outside handler for settings dropdown
  $effect(() => {
    if (!showSettings) return;

    function handleClickOutside(e: MouseEvent) {
      const target = e.target as Node;
      if (
        settingsDropdownEl &&
        !settingsDropdownEl.contains(target) &&
        settingsButtonEl &&
        !settingsButtonEl.contains(target)
      ) {
        showSettings = false;
      }
    }

    function handleEscape(e: KeyboardEvent) {
      if (e.key === "Escape") showSettings = false;
    }

    document.addEventListener("mousedown", handleClickOutside);
    document.addEventListener("keydown", handleEscape);

    return () => {
      document.removeEventListener("mousedown", handleClickOutside);
      document.removeEventListener("keydown", handleEscape);
    };
  });

  // Click-outside handler for context menu
  $effect(() => {
    if (!contextMenu) return;

    function handleClickOutside(e: MouseEvent) {
      const target = e.target as Node;
      if (contextMenuEl && !contextMenuEl.contains(target)) {
        contextMenu = null;
      }
    }

    function handleEscape(e: KeyboardEvent) {
      if (e.key === "Escape") contextMenu = null;
    }

    // Small delay to prevent immediate close on right-click
    const timer = setTimeout(() => {
      document.addEventListener("mousedown", handleClickOutside);
      document.addEventListener("keydown", handleEscape);
    }, 0);

    return () => {
      clearTimeout(timer);
      document.removeEventListener("mousedown", handleClickOutside);
      document.removeEventListener("keydown", handleEscape);
    };
  });

  onMount(() => {
    unsubscribe = crafter.subscribe((state) => {
      crafterState = state;
    });

    // Setup audio levels store
    audioLevelsStore = createAudioLevelsStore(() => crafter.getController(), { autoStart: true });
    unsubscribeAudioLevels = audioLevelsStore.subscribe((state) => {
      audioLevels = state;
    });

    // Note: compositor store is created in $effect when controller becomes available

    ingestEndpointsStore = createIngestEndpointsStore();
    unsubscribeIngestEndpoints = ingestEndpointsStore.subscribe((state) => {
      ingestEndpointsState = state;
    });
  });

  $effect(() => {
    if (!ingestEndpointsStore) return;

    if (whipUrl) {
      ingestEndpointsStore.reset();
      return;
    }

    if (gatewayUrl && streamKey) {
      ingestEndpointsStore.resolve({ gatewayUrl, streamKey });
      return;
    }

    ingestEndpointsStore.reset();
  });

  onDestroy(() => {
    unsubscribe?.();
    unsubscribeAudioLevels?.();
    unsubscribeCompositor?.();
    unsubscribeIngestEndpoints?.();
    audioLevelsStore?.destroy();
    compositorStore?.destroy();
    ingestEndpointsStore?.destroy();
    crafter.destroy();
  });

  // Handlers
  async function handleStartCamera() {
    try {
      await crafter.startCamera();
    } catch (err) {
      console.error("Failed to start camera:", err);
    }
  }

  async function handleStartScreenShare() {
    try {
      await crafter.startScreenShare({ audio: true });
    } catch (err) {
      console.error("Failed to start screen share:", err);
    }
  }

  async function handleGoLive() {
    if (!resolvedWhipUrl) {
      console.error("No WHIP endpoint configured");
      return;
    }
    try {
      await crafter.startStreaming();
    } catch (err) {
      console.error("Failed to start streaming:", err);
    }
  }

  async function handleStopStreaming() {
    await crafter.stopStreaming();
  }

  async function selectQualityProfile(profileId: QualityProfile) {
    await crafter.setQualityProfile(profileId);
    if (!devMode) {
      showSettings = false;
    }
  }

  function toggleSourceMute(sourceId: string, currentMuted: boolean) {
    crafter.setSourceMuted(sourceId, !currentMuted);
  }

  function handleRemoveSource(sourceId: string) {
    crafter.removeSource(sourceId);
  }

  function handleSetPrimaryVideo(sourceId: string) {
    crafter.setPrimaryVideoSource(sourceId);
  }

  function handleMasterVolumeChange(volume: number) {
    masterVolume = volume;
    crafter.setMasterVolume(volume);
  }

  function handleAudioProcessingChange(settings: Partial<AudioProcessingSettings>) {
    const next = { ...audioProcessing, ...settings };
    audioProcessing = next;

    crafterState.sources.forEach((source) => {
      source.stream.getAudioTracks().forEach((track) => {
        track
          .applyConstraints({
            echoCancellation: next.echoCancellation,
            noiseSuppression: next.noiseSuppression,
            autoGainControl: next.autoGainControl,
          })
          .catch((err) => {
            console.warn("Failed to apply audio constraints:", err);
          });
      });
    });
  }

  function handleEncoderOverridesChange(overrides: EncoderOverrides) {
    encoderOverrides = overrides;
    crafter.setEncoderOverrides(overrides);
  }

  function handleToggleSourceVisibility(sourceId: string) {
    if (!compositorStore || !compositorState.activeSceneId) return;
    const layers =
      compositorState.scenes.find((s) => s.id === compositorState.activeSceneId)?.layers ?? [];
    const layer = layers.find((l) => l.sourceId === sourceId);
    if (layer) {
      compositorStore.setLayerVisibility(compositorState.activeSceneId, layer.id, !layer.visible);
    }
  }

  function getSourceLayerVisibility(sourceId: string): boolean {
    if (!compositorState.isEnabled) return true;
    const activeScene = compositorState.scenes.find((s) => s.id === compositorState.activeSceneId);
    if (!activeScene) return true;
    const layer = activeScene.layers.find((l) => l.sourceId === sourceId);
    return layer?.visible ?? true;
  }

  function sourceHasVideo(source: MediaSource): boolean {
    return source.stream.getVideoTracks().length > 0;
  }

  // Context menu handler
  function handleContextMenu(e: MouseEvent) {
    e.preventDefault();
    contextMenu = { x: e.clientX, y: e.clientY };
  }

  // Context menu actions
  function copyWhipUrl() {
    if (resolvedWhipUrl) {
      navigator.clipboard.writeText(resolvedWhipUrl).catch(console.error);
    }
    contextMenu = null;
  }

  function copyStreamInfo() {
    const profile = QUALITY_PROFILES.find((p) => p.id === crafterState.qualityProfile);
    const info = [
      `Status: ${crafterState.state}`,
      `Quality: ${profile?.label ?? crafterState.qualityProfile} (${profile?.description ?? ""})`,
      `Sources: ${crafterState.sources.length}`,
      resolvedWhipUrl ? `WHIP: ${resolvedWhipUrl}` : null,
    ]
      .filter(Boolean)
      .join("\n");
    navigator.clipboard.writeText(info).catch(console.error);
    contextMenu = null;
  }

  // Computed
  let canAddSource = $derived(crafterState.state !== "destroyed" && crafterState.state !== "error");
  let canStream = $derived(
    crafterState.isCapturing && !crafterState.isStreaming && resolvedWhipUrl
  );
  let hasCamera = $derived(crafterState.sources.some((s) => s.type === "camera"));
  let _hasScreen = $derived(crafterState.sources.some((s) => s.type === "screen"));

  function getStatusText(state: IngestState, reconnState?: ReconnectionState | null): string {
    if (reconnState?.isReconnecting) {
      return t("reconnectingAttempt", { attempt: reconnState.attemptNumber, max: 5 });
    }
    switch (state) {
      case "idle":
        return t("idle");
      case "requesting_permissions":
        return t("requestingPermissions");
      case "capturing":
        return t("ready");
      case "connecting":
        return t("connecting");
      case "streaming":
        return t("live");
      case "reconnecting":
        return t("reconnecting");
      case "error":
        return t("error");
      case "destroyed":
        return t("destroyed");
      default:
        return state;
    }
  }

  function getStatusBadgeClass(state: IngestState, isReconnecting: boolean): string {
    if (state === "streaming") return "fw-sc-badge fw-sc-badge--live";
    if (isReconnecting) return "fw-sc-badge fw-sc-badge--connecting";
    if (state === "error") return "fw-sc-badge fw-sc-badge--error";
    if (state === "capturing") return "fw-sc-badge fw-sc-badge--ready";
    return "fw-sc-badge fw-sc-badge--idle";
  }

  let statusText = $derived(getStatusText(crafterState.state, crafterState.reconnectionState));
  let statusBadgeClass = $derived(
    getStatusBadgeClass(crafterState.state, crafterState.isReconnecting)
  );
</script>

{#if children}
  <div
    class="fw-sc-root {className}"
    data-theme={dataTheme}
    style={themeStyle || undefined}
    role="application"
  >
    {@render children()}
  </div>
{:else}
  <div
    class="fw-sc-root {devMode ? 'fw-sc-root--devmode' : ''} {className}"
    data-theme={dataTheme}
    style={themeStyle || undefined}
    oncontextmenu={handleContextMenu}
    role="application"
  >
    <!-- Main content wrapper -->
    <div class="fw-sc-main {devMode ? 'flex-1 min-w-0' : 'w-full'}">
      <!-- Header -->
      <div class="fw-sc-header">
        <span class="fw-sc-header-title">{t("streamCrafter")}</span>
        <div class="fw-sc-header-status">
          <span class={statusBadgeClass}>{statusText}</span>
        </div>
      </div>

      <!-- Content area (preview + mixer) - responsive layout -->
      <div class="fw-sc-content">
        <!-- Preview wrapper for flex sizing -->
        <div class="fw-sc-preview-wrapper">
          <!-- Video Preview (flush - no padding) -->
          <div class="fw-sc-preview">
            <video bind:this={videoEl} playsinline muted autoplay aria-label={t("streamPreview")}
            ></video>

            <!-- Empty State -->
            {#if !crafterState.mediaStream}
              <div class="fw-sc-preview-placeholder">
                <CameraIcon size={48} />
                <span>{t("addSourcePrompt")}</span>
              </div>
            {/if}

            <!-- Status Overlay - Connecting/Reconnecting -->
            {#if crafterState.state === "connecting" || crafterState.state === "reconnecting"}
              <div class="fw-sc-status-overlay">
                <div class="fw-sc-status-spinner"></div>
                <span class="fw-sc-status-text">{statusText}</span>
              </div>
            {/if}

            <!-- Live Badge -->
            {#if crafterState.isStreaming}
              <div class="fw-sc-live-badge">{t("live")}</div>
            {/if}

            <!-- Compositor Controls Overlay (inside preview) -->
            {#if enableCompositor && compositorStore && compositorState.isEnabled && compositorState.isInitialized}
              {@const activeScene =
                compositorState.scenes.find((s) => s.id === compositorState.activeSceneId) || null}
              <CompositorControls
                isEnabled={compositorState.isEnabled}
                isInitialized={compositorState.isInitialized}
                rendererType={compositorState.rendererType}
                stats={compositorState.stats}
                sources={crafterState.sources}
                layers={activeScene?.layers ?? []}
                currentLayout={compositorState.currentLayout}
                onLayoutApply={(layout) => compositorStore?.applyLayout(layout)}
                onCycleSourceOrder={(direction) => compositorStore?.cycleSourceOrder(direction)}
              />
            {/if}
          </div>
        </div>

        <!-- Mixer Section - moves to right on wide screens -->
        {#if crafterState.sources.length > 0}
          <div class="fw-sc-section fw-sc-mixer {!showSources ? 'fw-sc-section--collapsed' : ''}">
            <div
              class="fw-sc-section-header"
              onclick={() => (showSources = !showSources)}
              role="button"
              tabindex="0"
              onkeydown={(e) => e.key === "Enter" && (showSources = !showSources)}
              title={showSources ? t("collapseMixer") : t("expandMixer")}
            >
              <span>{t("mixer")} ({crafterState.sources.length})</span>
              {#if showSources}
                <ChevronsRightIcon size={14} />
              {:else}
                <ChevronsLeftIcon size={14} />
              {/if}
            </div>
            {#if showSources}
              <div class="fw-sc-sources">
                {#each crafterState.sources as source (source.id)}
                  {@const isVisible = getSourceLayerVisibility(source.id)}
                  <div class="fw-sc-source {!isVisible ? 'fw-sc-source--hidden' : ''}">
                    {#if enableCompositor}
                      <button
                        type="button"
                        class="fw-sc-icon-btn {!isVisible ? 'fw-sc-icon-btn--muted' : ''}"
                        onclick={() => handleToggleSourceVisibility(source.id)}
                        title={isVisible ? t("hideFromComposition") : t("showInComposition")}
                      >
                        <EyeIcon size={14} visible={isVisible} />
                      </button>
                    {/if}
                    <div class="fw-sc-source-icon">
                      {#if source.type === "camera"}
                        <CameraIcon size={16} />
                      {:else if source.type === "screen"}
                        <MonitorIcon size={16} />
                      {/if}
                    </div>
                    <div class="fw-sc-source-info">
                      <div class="fw-sc-source-label">
                        {source.label}
                        {#if source.primaryVideo && !enableCompositor}
                          <span class="fw-sc-primary-badge">{t("primary")}</span>
                        {/if}
                      </div>
                      <div class="fw-sc-source-type">{source.type}</div>
                    </div>
                    <div class="fw-sc-source-controls">
                      {#if sourceHasVideo(source) && !enableCompositor}
                        <button
                          type="button"
                          class="fw-sc-icon-btn {source.primaryVideo
                            ? 'fw-sc-icon-btn--primary'
                            : ''}"
                          onclick={() => handleSetPrimaryVideo(source.id)}
                          disabled={source.primaryVideo}
                          title={source.primaryVideo ? t("primaryVideoSource") : t("setAsPrimary")}
                        >
                          <VideoIcon size={14} active={source.primaryVideo} />
                        </button>
                      {/if}
                      <span class="fw-sc-volume-label">{Math.round(source.volume * 100)}%</span>
                      <VolumeSlider
                        value={source.volume}
                        onChange={(volume) => crafter.setSourceVolume(source.id, volume)}
                        compact={true}
                      />
                      <button
                        type="button"
                        class="fw-sc-icon-btn {source.muted ? 'fw-sc-icon-btn--active' : ''}"
                        onclick={() => toggleSourceMute(source.id, source.muted)}
                        title={source.muted ? t("unmute") : t("mute")}
                      >
                        <MicIcon size={14} muted={source.muted} />
                      </button>
                      <button
                        type="button"
                        class="fw-sc-icon-btn fw-sc-icon-btn--destructive"
                        onclick={() => handleRemoveSource(source.id)}
                        disabled={crafterState.isStreaming}
                        title={crafterState.isStreaming
                          ? t("cannotRemoveWhileStreaming")
                          : t("removeSource")}
                      >
                        <XIcon size={14} />
                      </button>
                    </div>
                  </div>
                {/each}
              </div>
            {/if}
          </div>
        {/if}
      </div>

      <!-- VU Meter (horizontal bar under content area) -->
      {#if crafterState.isCapturing}
        <div class="fw-sc-vu-meter">
          <div
            class="fw-sc-vu-meter-fill"
            style="width: {Math.min(audioLevels.level * 100, 100)}%"
          ></div>
          <div
            class="fw-sc-vu-meter-peak"
            style="left: {Math.min(audioLevels.peakLevel * 100, 100)}%"
          ></div>
        </div>
      {/if}

      <!-- Error Display -->
      {#if crafterState.error || ingestEndpointsState.error}
        <div class="fw-sc-error">
          <div class="fw-sc-error-title">{t("error")}</div>
          <div class="fw-sc-error-message">{crafterState.error || ingestEndpointsState.error}</div>
        </div>
      {/if}

      <!-- No Endpoint Warning -->
      {#if !resolvedWhipUrl && !crafterState.error && !ingestEndpointsState.error && !isResolvingEndpoint}
        <div class="fw-sc-error" style="border-left-color: hsl(40 80% 65%)">
          <div class="fw-sc-error-title" style="color: hsl(40 80% 65%)">{t("warning")}</div>
          <div class="fw-sc-error-message">{t("configureWhipEndpoint")}</div>
        </div>
      {/if}

      <!-- Resolving Endpoint State -->
      {#if isResolvingEndpoint}
        <div class="fw-sc-error" style="border-left-color: hsl(210 80% 65%)">
          <div class="fw-sc-error-title" style="color: hsl(210 80% 65%)">{t("connecting")}</div>
          <div class="fw-sc-error-message">{t("resolvingEndpoint")}</div>
        </div>
      {/if}

      <!-- Action Bar -->
      <div class="fw-sc-actions">
        <!-- Secondary actions: Camera, Screen, Settings -->
        <button
          type="button"
          class="fw-sc-action-secondary"
          onclick={handleStartCamera}
          disabled={!canAddSource || hasCamera}
          title={hasCamera ? t("cameraActive") : t("addCamera")}
        >
          <CameraIcon size={18} />
        </button>
        <button
          type="button"
          class="fw-sc-action-secondary"
          onclick={handleStartScreenShare}
          disabled={!canAddSource}
          title={t("shareScreen")}
        >
          <MonitorIcon size={18} />
        </button>
        <!-- Settings button in action bar -->
        <div style="position: relative">
          <button
            bind:this={settingsButtonEl}
            type="button"
            class="fw-sc-action-secondary {showSettings ? 'fw-sc-action-secondary--active' : ''}"
            onclick={() => (showSettings = !showSettings)}
            title={t("settings")}
            style="display: flex; align-items: center; justify-content: center;"
          >
            <span class="settings-icon-wrapper">
              <SettingsIcon size={16} />
            </span>
          </button>
          <!-- Settings Popup - positioned above button -->
          {#if showSettings}
            <div
              bind:this={settingsDropdownEl}
              style="
            position: absolute;
            bottom: 100%;
            left: 0;
            margin-bottom: 8px;
            width: 192px;
            background: #1a1b26;
            border: 1px solid rgba(90, 96, 127, 0.3);
            box-shadow: 0 4px 12px rgba(0, 0, 0, 0.4);
            border-radius: 4px;
            overflow: hidden;
            z-index: 50;
          "
            >
              <!-- Quality Section -->
              <div style="padding: 8px; border-bottom: 1px solid rgba(90, 96, 127, 0.3);">
                <div
                  style="font-size: 10px; color: #565f89; text-transform: uppercase; font-weight: 600; margin-bottom: 4px; padding-left: 4px;"
                >
                  {t("quality")}
                </div>
                <div style="display: flex; flex-direction: column; gap: 2px;">
                  {#each QUALITY_PROFILES as profile}
                    <button
                      type="button"
                      onclick={() => {
                        if (!crafterState.isStreaming) {
                          selectQualityProfile(profile.id);
                        }
                      }}
                      disabled={crafterState.isStreaming}
                      style="
                    width: 100%;
                    padding: 6px 8px;
                    text-align: left;
                    font-size: 12px;
                    border-radius: 4px;
                    transition: all 0.15s;
                    border: none;
                    cursor: {crafterState.isStreaming ? 'not-allowed' : 'pointer'};
                    opacity: {crafterState.isStreaming ? 0.5 : 1};
                    background: {crafterState.qualityProfile === profile.id
                        ? 'rgba(122, 162, 247, 0.2)'
                        : 'transparent'};
                    color: {crafterState.qualityProfile === profile.id ? '#7aa2f7' : '#a9b1d6'};
                  "
                    >
                      <div style="font-weight: 500;">{profile.label}</div>
                      <div style="font-size: 10px; color: #565f89;">{profile.description}</div>
                    </button>
                  {/each}
                </div>
              </div>

              <!-- Dev Info Section -->
              {#if devMode}
                <div style="padding: 8px;">
                  <div
                    style="font-size: 10px; color: #565f89; text-transform: uppercase; font-weight: 600; margin-bottom: 4px; padding-left: 4px;"
                  >
                    {t("debug")}
                  </div>
                  <div
                    style="display: flex; flex-direction: column; gap: 4px; padding-left: 4px; font-size: 12px; font-family: ui-monospace, SFMono-Regular, SF Mono, Menlo, Consolas, monospace;"
                  >
                    <div style="display: flex; justify-content: space-between;">
                      <span style="color: #565f89;">{t("state")}</span>
                      <span style="color: #c0caf5;">{crafterState.state}</span>
                    </div>
                    <div style="display: flex; justify-content: space-between;">
                      <span style="color: #565f89;">{t("audio")}</span>
                      <span style="color: {audioLevels.isMonitoring ? '#9ece6a' : '#565f89'};">
                        {audioLevels.isMonitoring ? t("active") : t("inactive")}
                      </span>
                    </div>
                    <div style="display: flex; justify-content: space-between;">
                      <span style="color: #565f89;">{t("whip")}</span>
                      <span style="color: {resolvedWhipUrl ? '#9ece6a' : '#f7768e'};">
                        {resolvedWhipUrl ? t("ok") : t("notSet")}
                      </span>
                    </div>
                  </div>
                </div>
              {/if}
            </div>
          {/if}
        </div>
        <!-- Primary action: Go Live / Stop -->
        {#if !crafterState.isStreaming}
          <button
            type="button"
            class="fw-sc-action-primary"
            onclick={handleGoLive}
            disabled={!canStream}
          >
            {crafterState.state === "connecting" ? t("connecting") : t("goLive")}
          </button>
        {:else}
          <button
            type="button"
            class="fw-sc-action-primary fw-sc-action-stop"
            onclick={handleStopStreaming}
          >
            {t("stopStreaming")}
          </button>
        {/if}
      </div>

      <!-- Context Menu -->
      {#if contextMenu}
        <div
          bind:this={contextMenuEl}
          class="fw-sc-context-menu"
          style="
          position: fixed;
          top: {contextMenu.y}px;
          left: {contextMenu.x}px;
          z-index: 1000;
          background: #1a1b26;
          border: 1px solid rgba(90, 96, 127, 0.3);
          border-radius: 6px;
          padding: 4px;
          box-shadow: 0 4px 12px rgba(0,0,0,0.5);
          min-width: 160px;
        "
        >
          {#if resolvedWhipUrl}
            <button
              type="button"
              class="fw-sc-context-menu-item"
              onclick={copyWhipUrl}
              style="
              width: 100%;
              text-align: left;
              padding: 6px 8px;
              color: #c0caf5;
              font-size: 13px;
              border-radius: 4px;
              border: none;
              background: transparent;
              cursor: pointer;
            "
              onmouseenter={(e) => (e.currentTarget.style.background = "rgba(122, 162, 247, 0.1)")}
              onmouseleave={(e) => (e.currentTarget.style.background = "transparent")}
            >
              {t("copyWhipUrl")}
            </button>
          {/if}
          <button
            type="button"
            class="fw-sc-context-menu-item"
            onclick={copyStreamInfo}
            style="
            width: 100%;
            text-align: left;
            padding: 6px 8px;
            color: #c0caf5;
            font-size: 13px;
            border-radius: 4px;
            border: none;
            background: transparent;
            cursor: pointer;
          "
            onmouseenter={(e) => (e.currentTarget.style.background = "rgba(122, 162, 247, 0.1)")}
            onmouseleave={(e) => (e.currentTarget.style.background = "transparent")}
          >
            {t("copyStreamInfo")}
          </button>
          {#if devMode}
            <div style="height: 1px; background: rgba(90, 96, 127, 0.3); margin: 4px 0;"></div>
            <button
              type="button"
              class="fw-sc-context-menu-item"
              onclick={() => {
                isAdvancedPanelOpen = !isAdvancedPanelOpen;
                contextMenu = null;
              }}
              style="
              width: 100%;
              text-align: left;
              padding: 6px 8px;
              color: #c0caf5;
              font-size: 13px;
              border-radius: 4px;
              border: none;
              background: transparent;
              cursor: pointer;
              display: flex;
              gap: 8px;
              align-items: center;
            "
              onmouseenter={(e) => (e.currentTarget.style.background = "rgba(122, 162, 247, 0.1)")}
              onmouseleave={(e) => (e.currentTarget.style.background = "transparent")}
            >
              <SettingsIcon size={14} />
              <span>{isAdvancedPanelOpen ? t("hideAdvanced") : t("advanced")}</span>
            </button>
          {/if}
        </div>
      {/if}
    </div>

    <!-- Advanced Panel for devMode -->
    {#if devMode}
      {@const activeScene = compositorState.scenes.find(
        (s) => s.id === compositorState.activeSceneId
      )}
      <AdvancedPanel
        isOpen={isAdvancedPanelOpen}
        onClose={() => (isAdvancedPanelOpen = false)}
        ingestState={crafterState.state}
        qualityProfile={crafterState.qualityProfile}
        whipUrl={resolvedWhipUrl}
        sources={crafterState.sources}
        stats={crafterState.stats}
        mediaStream={crafterState.mediaStream}
        {masterVolume}
        onMasterVolumeChange={handleMasterVolumeChange}
        audioLevel={audioLevels.level}
        audioMixingEnabled={true}
        error={crafterState.error}
        {audioProcessing}
        onAudioProcessingChange={handleAudioProcessingChange}
        compositorEnabled={compositorState.isEnabled}
        compositorRendererType={compositorState.rendererType}
        compositorStats={compositorState.stats}
        sceneCount={compositorState.scenes.length}
        layerCount={activeScene?.layers.length ?? 0}
        useWebCodecs={crafterState.useWebCodecs}
        isWebCodecsActive={crafterState.isWebCodecsActive}
        encoderStats={crafterState.encoderStats}
        onUseWebCodecsChange={(enabled) => crafter.setUseWebCodecs(enabled)}
        {isWebCodecsAvailable}
        {encoderOverrides}
        onEncoderOverridesChange={handleEncoderOverridesChange}
      />
    {/if}
  </div>
{/if}

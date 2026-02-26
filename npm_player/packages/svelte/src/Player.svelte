<!--
  Player.svelte - Full-featured video player using PlayerController
  Thin wrapper over PlayerController from @livepeer-frameworks/player-core
-->
<script lang="ts">
  import { onMount, setContext, type Snippet } from "svelte";
  import IdleScreen from "./IdleScreen.svelte";
  import SubtitleRenderer from "./SubtitleRenderer.svelte";
  import PlayerControls from "./PlayerControls.svelte";
  import SpeedIndicator from "./SpeedIndicator.svelte";
  import SkipIndicator from "./SkipIndicator.svelte";
  import TitleOverlay from "./TitleOverlay.svelte";
  import StatsPanel from "./StatsPanel.svelte";
  import DevModePanel from "./DevModePanel.svelte";
  import {
    ContextMenu,
    ContextMenuTrigger,
    ContextMenuPortal,
    ContextMenuContent,
    ContextMenuItem,
    ContextMenuSeparator,
  } from "./ui/context-menu";
  import { StatsIcon, SettingsIcon, PictureInPictureIcon } from "./icons";
  import {
    cn,
    themeOverridesToStyle,
    resolveTheme,
    type PlaybackMode,
    type ContentEndpoints,
    type PlayerState,
    type PlayerStateContext,
    type ContentType,
    type EndpointInfo,
    type PlayerMetadata,
    type FwThemePreset,
    type FwThemeOverrides,
    type FwLocale,
  } from "@livepeer-frameworks/player-core";
  import {
    createPlayerControllerStore,
    type PlayerControllerStore,
  } from "./stores/playerController";
  import { localeStore, translatorStore } from "./stores/i18n";
  import type { SkipDirection } from "./SkipIndicator.svelte";

  // Props - aligned with React Player
  interface Props {
    contentId: string;
    contentType?: ContentType;
    thumbnailUrl?: string | null;
    endpoints?: ContentEndpoints;
    options?: {
      gatewayUrl?: string;
      mistUrl?: string;
      authToken?: string;
      autoplay?: boolean;
      muted?: boolean;
      controls?: boolean;
      stockControls?: boolean;
      devMode?: boolean;
      debug?: boolean;
      forcePlayer?: string;
      forceType?: string;
      forceSource?: number;
      playbackMode?: PlaybackMode;
      theme?: FwThemePreset;
      themeOverrides?: FwThemeOverrides;
      locale?: FwLocale;
    };
    onStateChange?: (state: PlayerState, context?: PlayerStateContext) => void;
    onMetadata?: (metadata: PlayerMetadata) => void;
    children?: Snippet;
  }

  let {
    contentId,
    contentType,
    thumbnailUrl = null,
    endpoints = undefined,
    options = {},
    onStateChange = undefined,
    onMetadata = undefined,
    children = undefined,
  }: Props = $props();

  // ============================================================================
  // UI-only State (stays in wrapper)
  // ============================================================================
  let isStatsOpen = $state(false);
  let isDevPanelOpen = $state(false);
  let skipDirection: SkipDirection = $state(null);

  let activeTheme = $state<FwThemePreset>(options?.theme ?? "default");
  let activeLocale = $state<FwLocale>(options?.locale ?? "en");

  // Sync locale state to i18n store and provide translator context
  $effect(() => {
    localeStore.set(activeLocale);
  });
  setContext("fw-translator", translatorStore);

  // Provide context for composable controls (reactive getters)
  setContext("fw-player-controller", {
    get isPlaying() {
      return storeState.isPlaying;
    },
    get isPaused() {
      return storeState.isPaused;
    },
    get isMuted() {
      return storeState.isMuted;
    },
    get volume() {
      return storeState.volume;
    },
    get currentTime() {
      return storeState.currentTime;
    },
    get duration() {
      return storeState.duration;
    },
    get isFullscreen() {
      return storeState.isFullscreen;
    },
    get isEffectivelyLive() {
      return storeState.isEffectivelyLive;
    },
    get isBuffering() {
      return storeState.isBuffering;
    },
    get isLoopEnabled() {
      return storeState.isLoopEnabled;
    },
    get error() {
      return storeState.error;
    },
    get qualities() {
      return playerStore?.getQualities() ?? [];
    },
    togglePlay: () => playerStore?.togglePlay(),
    toggleMute: () => playerStore?.toggleMute(),
    toggleFullscreen: () => playerStore?.toggleFullscreen(),
    toggleLoop: () => playerStore?.toggleLoop(),
    setVolume: (v: number) => playerStore?.setVolume(v),
    jumpToLive: () => playerStore?.jumpToLive(),
    seek: (t: number) => playerStore?.seek(t),
    selectQuality: (id: string) => playerStore?.selectQuality(id),
    getQualities: () => playerStore?.getQualities() ?? [],
  });

  // Playback mode preference (persistent)
  let devPlaybackMode: PlaybackMode = $state("auto");
  $effect(() => {
    if (options?.playbackMode) {
      devPlaybackMode = options.playbackMode;
    }
  });

  // Error fade-out: keep overlay visible while it animates out
  let displayedError: string | null = $state(null);
  let displayedIsPassive = $state(false);
  let isErrorDismissing = $state(false);
  let errorDismissTimer: ReturnType<typeof setTimeout> | null = null;
  $effect(() => {
    const error = storeState.error;
    const passive = storeState.isPassiveError;
    if (error) {
      if (errorDismissTimer) {
        clearTimeout(errorDismissTimer);
        errorDismissTimer = null;
      }
      displayedError = error;
      displayedIsPassive = passive;
      isErrorDismissing = false;
    } else if (displayedError) {
      isErrorDismissing = true;
      errorDismissTimer = setTimeout(() => {
        displayedError = null;
        displayedIsPassive = false;
        isErrorDismissing = false;
        errorDismissTimer = null;
      }, 300);
    }
  });

  // Container ref
  let containerRef: HTMLElement | undefined = $state();
  let playerRootRef: HTMLDivElement | undefined = $state();

  // ============================================================================
  // PlayerController Store - ALL business logic
  // ============================================================================
  let playerStore: PlayerControllerStore | null = $state(null);
  let storeState = $state({
    state: "booting" as PlayerState,
    streamState: null as any,
    endpoints: null as any,
    metadata: null as any,
    videoElement: null as HTMLVideoElement | null,
    currentTime: 0,
    duration: NaN,
    isPlaying: false,
    isPaused: true,
    isBuffering: false,
    isMuted: false,
    volume: 1,
    error: null as string | null,
    isPassiveError: false,
    hasPlaybackStarted: false,
    isHoldingSpeed: false,
    holdSpeed: 2,
    isHovering: false,
    shouldShowControls: false,
    isLoopEnabled: false,
    isFullscreen: false,
    isPiPActive: false,
    isEffectivelyLive: false,
    shouldShowIdleScreen: true,
    currentPlayerInfo: null as { name: string; shortname: string } | null,
    currentSourceInfo: null as { url: string; type: string } | null,
    playbackQuality: null as any,
    subtitlesEnabled: false,
    toast: null as { message: string; timestamp: number } | null,
    controllerSeekableStart: 0,
    controllerLiveEdge: 0,
    controllerCanSeek: false,
    controllerHasAudio: true,
  });

  // Track if we've already attached to prevent double-attach race
  let hasAttached = false;

  // Debug helper
  const debug = (msg: string) => {
    if (options?.debug) {
      console.log(`[Player.svelte] ${msg}`);
    }
  };

  // Create store on mount
  onMount(() => {
    debug(`onMount - contentId: ${contentId}, contentType: ${contentType}`);
    debug(`onMount - gatewayUrl: ${options?.gatewayUrl}, mistUrl: ${options?.mistUrl}`);
    debug(`onMount - endpoints: ${endpoints ? "provided" : "not provided"}`);

    playerStore = createPlayerControllerStore({
      contentId,
      contentType,
      endpoints,
      gatewayUrl: options?.gatewayUrl,
      mistUrl: options?.mistUrl,
      authToken: options?.authToken,
      autoplay: options?.autoplay !== false,
      muted: options?.muted === true,
      controls: options?.stockControls === true,
      poster: thumbnailUrl || undefined,
      debug: options?.debug,
    });

    debug("playerStore created");

    // Subscribe to store state
    let prevMetadata: PlayerMetadata | null = null;
    const unsubscribe = playerStore.subscribe((state) => {
      storeState = state;
      // Forward state changes to prop callback
      if (onStateChange && state.state) {
        onStateChange(state.state);
      }
      // Forward metadata changes to prop callback
      if (onMetadata && state.metadata && state.metadata !== prevMetadata) {
        prevMetadata = state.metadata;
        onMetadata(state.metadata);
      }
    });

    return () => {
      debug("cleanup - destroying playerStore");
      unsubscribe();
      playerStore?.destroy();
      playerStore = null;
      hasAttached = false;
    };
  });

  // Attach when container becomes available (only once)
  $effect(() => {
    debug(
      `$effect - containerRef: ${!!containerRef}, playerStore: ${!!playerStore}, hasAttached: ${hasAttached}`
    );
    if (containerRef && playerStore && !hasAttached) {
      hasAttached = true;
      debug("attaching to container");
      playerStore
        .attach(containerRef)
        .then(() => {
          debug("attach completed");
        })
        .catch((err) => {
          debug(`attach failed: ${err}`);
          console.error("[Player.svelte] attach failed:", err);
        });
    }
  });

  // Auto-dismiss toast after 3 seconds
  $effect(() => {
    if (!storeState.toast) return;
    const timer = setTimeout(() => {
      playerStore?.dismissToast();
    }, 3000);
    return () => clearTimeout(timer);
  });

  // ============================================================================
  // Dev Mode Callbacks
  // ============================================================================
  function handleDevSettingsChange(settings: {
    forcePlayer?: string;
    forceType?: string;
    forceSource?: number;
  }) {
    // One-shot selection - controller handles the state
    playerStore?.setDevModeOptions({
      forcePlayer: settings.forcePlayer,
      forceType: settings.forceType,
      forceSource: settings.forceSource,
    });
  }

  function handleModeChange(mode: PlaybackMode) {
    devPlaybackMode = mode;
    // Mode is a persistent preference
    playerStore?.setDevModeOptions({ playbackMode: mode });
  }

  function handleReload() {
    playerStore?.clearError();
    playerStore?.reload();
  }

  function handleSkipIndicatorHide() {
    skipDirection = null;
  }

  // ============================================================================
  // Derived Values
  // ============================================================================
  let primaryEndpoint = $derived(storeState.endpoints?.primary as EndpointInfo | undefined);
  let isLegacyPlayer = $derived(storeState.currentPlayerInfo?.shortname === "mist-legacy");
  let useStockControls = $derived(options?.stockControls === true || isLegacyPlayer);
  let metadata = $derived(storeState.metadata);

  // Title overlay visibility: show on hover or when paused
  let showTitleOverlay = $derived(
    (storeState.isHovering || storeState.isPaused) &&
      !storeState.shouldShowIdleScreen &&
      !storeState.isBuffering &&
      !storeState.error
  );

  // Buffering spinner: only during active playback
  let showBufferingSpinner = $derived(
    !storeState.shouldShowIdleScreen &&
      storeState.isBuffering &&
      !storeState.error &&
      storeState.hasPlaybackStarted
  );

  // Waiting for endpoint (shown as overlay, not early return)
  let showWaitingForEndpoint = $derived(
    !storeState.endpoints?.primary && storeState.state !== "booting"
  );

  let waitingMessage = $derived(
    options?.gatewayUrl
      ? storeState.state === "gateway_loading"
        ? $translatorStore("resolvingEndpoint")
        : $translatorStore("waitingForStream")
      : $translatorStore("waitingForStream")
  );
</script>

<ContextMenu>
  <ContextMenuTrigger>
    {#snippet child({ props })}
      <div
        bind:this={playerRootRef}
        {...props}
        class={cn(
          "fw-player-surface fw-player-root relative w-full h-full bg-black",
          options?.devMode && "flex"
        )}
        data-player-container="true"
        data-theme={activeTheme && activeTheme !== "default" ? activeTheme : undefined}
        style={(() => {
          const presetOverrides = activeTheme ? resolveTheme(activeTheme) : null;
          const merged = { ...presetOverrides, ...options?.themeOverrides };
          return Object.keys(merged).length > 0
            ? Object.entries(themeOverridesToStyle(merged))
                .map(([k, v]) => `${k}: ${v}`)
                .join("; ")
            : undefined;
        })()}
        role="region"
        aria-label="Video player"
        onmouseenter={() => playerStore?.handleMouseEnter()}
        onmouseleave={() => playerStore?.handleMouseLeave()}
        onmousemove={() => playerStore?.handleMouseMove()}
      >
        <!-- Player area -->
        <div class={cn("relative", options?.devMode ? "flex-1 min-w-0 h-full" : "w-full h-full")}>
          <!-- Video container - PlayerController attaches here -->
          <div bind:this={containerRef} class="fw-player-container w-full h-full"></div>

          <!-- Subtitle renderer -->
          {#if storeState.subtitlesEnabled}
            <SubtitleRenderer
              currentTime={storeState.currentTime}
              enabled={storeState.subtitlesEnabled}
            />
          {/if}

          <!-- Title overlay -->
          <TitleOverlay
            title={metadata?.title}
            description={metadata?.description}
            isVisible={showTitleOverlay}
          />

          <!-- Stats panel -->
          <StatsPanel
            isOpen={isStatsOpen}
            onClose={() => (isStatsOpen = false)}
            {metadata}
            streamState={storeState.streamState}
            quality={storeState.playbackQuality}
            videoElement={storeState.videoElement}
            protocol={primaryEndpoint?.protocol}
            nodeId={primaryEndpoint?.nodeId}
            geoDistance={primaryEndpoint?.geoDistance}
          />

          <!-- Dev mode toggle (when panel closed) -->
          {#if options?.devMode && !isDevPanelOpen}
            <DevModePanel
              onSettingsChange={handleDevSettingsChange}
              playbackMode={devPlaybackMode}
              onModeChange={handleModeChange}
              onReload={handleReload}
              streamInfo={storeState.currentSourceInfo
                ? {
                    source: [
                      {
                        url: storeState.currentSourceInfo.url,
                        type: storeState.currentSourceInfo.type,
                      },
                    ],
                    meta: { tracks: [] },
                  }
                : null}
              mistStreamInfo={storeState.streamState?.streamInfo}
              currentPlayer={storeState.currentPlayerInfo}
              currentSource={storeState.currentSourceInfo}
              videoElement={storeState.videoElement}
              protocol={primaryEndpoint?.protocol}
              nodeId={primaryEndpoint?.nodeId}
              isVisible={storeState.isHovering || storeState.isPaused}
              isOpen={false}
              onOpenChange={(open) => (isDevPanelOpen = open)}
            />
          {/if}

          <!-- Speed indicator overlay -->
          {#if storeState.isHoldingSpeed}
            <SpeedIndicator isVisible={true} speed={storeState.holdSpeed} />
          {/if}

          <!-- Skip indicator overlay -->
          <SkipIndicator direction={skipDirection} seconds={10} onhide={handleSkipIndicatorHide} />

          <!-- Waiting for endpoint overlay -->
          {#if showWaitingForEndpoint}
            <IdleScreen status="OFFLINE" message={waitingMessage} />
          {/if}

          <!-- Idle screen -->
          {#if !showWaitingForEndpoint && storeState.shouldShowIdleScreen}
            <IdleScreen
              status={storeState.isEffectivelyLive ? storeState.streamState?.status : undefined}
              message={storeState.isEffectivelyLive
                ? storeState.streamState?.message
                : $translatorStore("loading")}
              percentage={storeState.isEffectivelyLive
                ? storeState.streamState?.percentage
                : undefined}
            />
          {/if}

          <!-- Buffering spinner -->
          {#if showBufferingSpinner}
            <div role="status" aria-live="polite" class="fw-buffering-overlay">
              <div class="fw-buffering-pill">
                <div class="fw-buffering-spinner"></div>
                <span>{$translatorStore("buffering")}</span>
              </div>
            </div>
          {/if}

          <!-- Passive error toast (non-blocking) -->
          {#if displayedError && !storeState.shouldShowIdleScreen && displayedIsPassive}
            <div
              class={cn(
                "absolute bottom-20 left-1/2 -translate-x-1/2 z-30 transition-opacity duration-300",
                isErrorDismissing
                  ? "opacity-0"
                  : "animate-in fade-in slide-in-from-bottom-2 duration-200"
              )}
              role="status"
              aria-live="polite"
            >
              <div
                class="flex items-center gap-2 rounded-lg border border-yellow-500/30 bg-black/80 px-4 py-2 text-sm text-white shadow-lg backdrop-blur-sm"
              >
                <span class="text-yellow-400 text-xs font-semibold uppercase"
                  >{$translatorStore("warning")}</span
                >
                <span>{displayedError}</span>
                <button
                  type="button"
                  onclick={() => playerStore?.clearError()}
                  class="ml-2 text-white/60 hover:text-white"
                  aria-label={$translatorStore("dismiss")}
                >
                  <svg width="12" height="12" viewBox="0 0 12 12" fill="none">
                    <path
                      d="M9 3L3 9M3 3L9 9"
                      stroke="currentColor"
                      stroke-width="1.5"
                      stroke-linecap="round"
                    />
                  </svg>
                </button>
              </div>
            </div>
          {/if}

          <!-- Fatal error overlay (blocking) — auto-dismisses on playback resume -->
          {#if displayedError && !storeState.shouldShowIdleScreen && !displayedIsPassive}
            <div
              role="alert"
              aria-live="assertive"
              class={cn(
                "fw-error-overlay fw-error-overlay--fullscreen transition-opacity duration-300",
                isErrorDismissing && "opacity-0"
              )}
            >
              <div class="fw-error-popup fw-error-popup--fullscreen">
                <div class="fw-error-header fw-error-header--error">
                  <span class="fw-error-title fw-error-title--error"
                    >{$translatorStore("error")}</span
                  >
                  <button
                    type="button"
                    class="fw-error-close"
                    onclick={() => playerStore?.clearError()}
                    aria-label={$translatorStore("dismiss")}
                  >
                    <svg width="12" height="12" viewBox="0 0 12 12" fill="none">
                      <path
                        d="M9 3L3 9M3 3L9 9"
                        stroke="currentColor"
                        stroke-width="1.5"
                        stroke-linecap="round"
                      />
                    </svg>
                  </button>
                </div>
                <div class="fw-error-body">
                  <p class="fw-error-message">{displayedError}</p>
                </div>
                <div class="fw-error-actions">
                  <button
                    type="button"
                    class="fw-error-btn"
                    onclick={() => {
                      playerStore?.clearError();
                      playerStore?.retry();
                    }}
                    aria-label={$translatorStore("retry")}
                  >
                    {$translatorStore("retry")}
                  </button>
                  {#if options?.devMode && playerStore?.getController()?.canAttemptFallback()}
                    <button
                      type="button"
                      class="fw-error-btn fw-error-btn--secondary"
                      onclick={() => {
                        playerStore?.clearError();
                        playerStore?.getController()?.retryWithFallback();
                      }}
                      aria-label={$translatorStore("tryNext")}
                    >
                      {$translatorStore("tryNext")}
                    </button>
                  {/if}
                  {#if options?.devMode}
                    <button
                      type="button"
                      class="fw-error-btn fw-error-btn--secondary"
                      onclick={() => {
                        playerStore?.clearError();
                        playerStore?.reload();
                      }}
                      aria-label={$translatorStore("reloadPlayer")}
                    >
                      {$translatorStore("reloadPlayer")}
                    </button>
                  {/if}
                </div>
              </div>
            </div>
          {/if}

          <!-- Toast notification -->
          {#if storeState.toast}
            <div
              class="absolute bottom-20 left-1/2 -translate-x-1/2 z-30 animate-in fade-in slide-in-from-bottom-2 duration-200"
              role="status"
              aria-live="polite"
            >
              <div
                class="flex items-center gap-2 rounded-lg border border-white/10 bg-black/80 px-4 py-2 text-sm text-white shadow-lg backdrop-blur-sm"
              >
                <span>{storeState.toast.message}</span>
                <button
                  type="button"
                  onclick={() => playerStore?.dismissToast()}
                  class="ml-2 text-white/60 hover:text-white"
                  aria-label={$translatorStore("dismiss")}
                >
                  <svg width="12" height="12" viewBox="0 0 12 12" fill="none">
                    <path
                      d="M9 3L3 9M3 3L9 9"
                      stroke="currentColor"
                      stroke-width="1.5"
                      stroke-linecap="round"
                    />
                  </svg>
                </button>
              </div>
            </div>
          {/if}

          <!-- Player controls — custom children or default -->
          {#if children}
            {@render children()}
          {:else if !useStockControls}
            <PlayerControls
              currentTime={storeState.currentTime}
              duration={storeState.duration}
              isVisible={storeState.shouldShowControls}
              onseek={(t) => playerStore?.seek(t)}
              disabled={!storeState.videoElement}
              sourceType={storeState.currentSourceInfo?.type}
              playbackMode={devPlaybackMode}
              onModeChange={handleModeChange}
              mistStreamInfo={storeState.streamState?.streamInfo}
              showStatsButton={false}
              {isStatsOpen}
              onStatsToggle={() => (isStatsOpen = !isStatsOpen)}
              isContentLive={storeState.isEffectivelyLive}
              onJumpToLive={() => playerStore?.getController()?.jumpToLive()}
              controllerSeekableStart={storeState?.controllerSeekableStart ?? 0}
              controllerLiveEdge={storeState?.controllerLiveEdge ?? 0}
              {activeLocale}
              onLocaleChange={(l) => {
                activeLocale = l;
              }}
            />
          {/if}
        </div>

        <!-- Dev mode panel (when open) -->
        {#if options?.devMode && isDevPanelOpen}
          <DevModePanel
            onSettingsChange={handleDevSettingsChange}
            playbackMode={devPlaybackMode}
            onModeChange={handleModeChange}
            onReload={handleReload}
            streamInfo={storeState.currentSourceInfo
              ? {
                  source: [
                    {
                      url: storeState.currentSourceInfo.url,
                      type: storeState.currentSourceInfo.type,
                    },
                  ],
                  meta: { tracks: [] },
                }
              : null}
            mistStreamInfo={storeState.streamState?.streamInfo}
            currentPlayer={storeState.currentPlayerInfo}
            currentSource={storeState.currentSourceInfo}
            videoElement={storeState.videoElement}
            protocol={primaryEndpoint?.protocol}
            nodeId={primaryEndpoint?.nodeId}
            isVisible={true}
            isOpen={true}
            onOpenChange={(open) => (isDevPanelOpen = open)}
          />
        {/if}
      </div>
    {/snippet}
  </ContextMenuTrigger>

  <ContextMenuPortal>
    <ContextMenuContent>
      <ContextMenuItem
        onSelect={() => {
          isStatsOpen = !isStatsOpen;
        }}
      >
        <StatsIcon size={14} class="opacity-70 flex-shrink-0 mr-2" />
        {isStatsOpen ? $translatorStore("hideStats") : $translatorStore("showStats")}
      </ContextMenuItem>
      {#if options?.devMode}
        <ContextMenuSeparator />
        <ContextMenuItem
          onSelect={() => {
            isDevPanelOpen = !isDevPanelOpen;
          }}
        >
          <SettingsIcon size={14} class="opacity-70 flex-shrink-0 mr-2" />
          {isDevPanelOpen ? $translatorStore("hideSettings") : $translatorStore("settings")}
        </ContextMenuItem>
      {/if}
      <ContextMenuSeparator />
      <ContextMenuItem onSelect={() => playerStore?.togglePiP()}>
        <PictureInPictureIcon size={14} class="opacity-70 flex-shrink-0 mr-2" />
        {$translatorStore("pictureInPicture")}
      </ContextMenuItem>
      <ContextMenuItem onSelect={() => playerStore?.toggleLoop()}>
        <svg
          width="14"
          height="14"
          viewBox="0 0 24 24"
          fill="none"
          stroke="currentColor"
          stroke-width="2"
          stroke-linecap="round"
          stroke-linejoin="round"
          class="opacity-70 flex-shrink-0 mr-2"
        >
          <polyline points="17 1 21 5 17 9"></polyline>
          <path d="M3 11V9a4 4 0 0 1 4-4h14"></path>
          <polyline points="7 23 3 19 7 15"></polyline>
          <path d="M21 13v2a4 4 0 0 1-4 4H3"></path>
        </svg>
        {storeState.isLoopEnabled
          ? $translatorStore("disableLoop")
          : $translatorStore("enableLoop")}
      </ContextMenuItem>
    </ContextMenuContent>
  </ContextMenuPortal>
</ContextMenu>

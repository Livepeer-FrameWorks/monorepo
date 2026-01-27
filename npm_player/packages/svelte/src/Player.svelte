<!--
  Player.svelte - Full-featured video player using PlayerController
  Thin wrapper over PlayerController from @livepeer-frameworks/player-core
-->
<script lang="ts">
  import { onMount } from "svelte";
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
    type PlaybackMode,
    type ContentEndpoints,
    type PlayerState,
    type PlayerStateContext,
    type ContentType,
    type EndpointInfo,
    type PlayerMetadata,
  } from "@livepeer-frameworks/player-core";
  import {
    createPlayerControllerStore,
    type PlayerControllerStore,
  } from "./stores/playerController";
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
    };
    onStateChange?: (state: PlayerState, context?: PlayerStateContext) => void;
    onMetadata?: (metadata: PlayerMetadata) => void;
  }

  let {
    contentId,
    contentType,
    thumbnailUrl = null,
    endpoints = undefined,
    options = {},
    onStateChange = undefined,
    onMetadata = undefined,
  }: Props = $props();

  // ============================================================================
  // UI-only State (stays in wrapper)
  // ============================================================================
  let isStatsOpen = $state(false);
  let isDevPanelOpen = $state(false);
  let skipDirection: SkipDirection = $state(null);

  // Playback mode preference (persistent)
  let devPlaybackMode: PlaybackMode = $state(options?.playbackMode || "auto");

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
    isMuted: true,
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
      muted: options?.muted !== false,
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
        ? "Resolving viewing endpoint..."
        : "Waiting for endpoint..."
      : "Waiting for endpoint..."
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
        tabindex="0"
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
                : "Loading video..."}
              percentage={storeState.isEffectivelyLive
                ? storeState.streamState?.percentage
                : undefined}
            />
          {/if}

          <!-- Buffering spinner -->
          {#if showBufferingSpinner}
            <div
              class="absolute inset-0 flex items-center justify-center bg-black/40 backdrop-blur-sm z-20"
            >
              <div
                class="flex items-center gap-3 rounded-lg border border-white/10 bg-black/70 px-4 py-3 text-sm text-white shadow-lg"
              >
                <div
                  class="w-4 h-4 border-2 border-white/30 border-t-white rounded-full animate-spin"
                ></div>
                <span>Buffering...</span>
              </div>
            </div>
          {/if}

          <!-- Error overlay -->
          {#if storeState.error && !storeState.shouldShowIdleScreen}
            <div
              role="alert"
              aria-live="assertive"
              class={cn(
                "fw-error-overlay",
                storeState.isPassiveError
                  ? "fw-error-overlay--passive"
                  : "fw-error-overlay--fullscreen"
              )}
            >
              <div
                class={cn(
                  "fw-error-popup",
                  storeState.isPassiveError
                    ? "fw-error-popup--passive"
                    : "fw-error-popup--fullscreen"
                )}
              >
                <div
                  class={cn(
                    "fw-error-header",
                    storeState.isPassiveError
                      ? "fw-error-header--warning"
                      : "fw-error-header--error"
                  )}
                >
                  <span
                    class={cn(
                      "fw-error-title",
                      storeState.isPassiveError
                        ? "fw-error-title--warning"
                        : "fw-error-title--error"
                    )}
                  >
                    {storeState.isPassiveError ? "Warning" : "Error"}
                  </span>
                  <button
                    type="button"
                    class="fw-error-close"
                    onclick={() => playerStore?.clearError()}
                    aria-label="Dismiss"
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
                  <p class="fw-error-message">Playback issue</p>
                </div>
                <div class="fw-error-actions">
                  <button
                    type="button"
                    class="fw-error-btn"
                    onclick={() => {
                      playerStore?.clearError();
                      playerStore?.retry();
                    }}
                    aria-label="Retry playback"
                  >
                    Retry
                  </button>
                </div>
              </div>
            </div>
          {/if}

          <!-- Player controls -->
          {#if !useStockControls}
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
        {isStatsOpen ? "Hide Stats" : "Stats"}
      </ContextMenuItem>
      {#if options?.devMode}
        <ContextMenuSeparator />
        <ContextMenuItem
          onSelect={() => {
            isDevPanelOpen = !isDevPanelOpen;
          }}
        >
          <SettingsIcon size={14} class="opacity-70 flex-shrink-0 mr-2" />
          {isDevPanelOpen ? "Hide Settings" : "Settings"}
        </ContextMenuItem>
      {/if}
      <ContextMenuSeparator />
      <ContextMenuItem onSelect={() => playerStore?.togglePiP()}>
        <PictureInPictureIcon size={14} class="opacity-70 flex-shrink-0 mr-2" />
        Picture-in-Picture
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
        {storeState.isLoopEnabled ? "Disable Loop" : "Enable Loop"}
      </ContextMenuItem>
    </ContextMenuContent>
  </ContextMenuPortal>
</ContextMenu>

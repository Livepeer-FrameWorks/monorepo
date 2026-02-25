<script lang="ts">
  import { getContext } from "svelte";
  import { readable } from "svelte/store";
  import type { Readable } from "svelte/store";
  import {
    cn,
    globalPlayerManager,
    createTranslator,
    type MistStreamInfo,
    type PlaybackMode,
    type TranslateFn,
    // Seeking utilities from core
    SPEED_PRESETS,
    isMediaStreamSource,
    supportsPlaybackRate as coreSupportsPlaybackRate,
    calculateSeekableRange,
    canSeekStream,
    calculateLiveThresholds,
    calculateIsNearLive,
    isLiveContent,
    // Time formatting from core
    formatTimeDisplay,
    getAvailableLocales,
    getLocaleDisplayName,
  } from "@livepeer-frameworks/player-core";
  import type { FwLocale } from "@livepeer-frameworks/player-core";
  import SeekBar from "./SeekBar.svelte";
  import Slider from "./ui/Slider.svelte";
  import VolumeIcons from "./components/VolumeIcons.svelte";
  import {
    StatsIcon,
    SettingsIcon,
    PlayIcon,
    PauseIcon,
    SkipBackIcon,
    SkipForwardIcon,
    FullscreenIcon,
    FullscreenExitIcon,
    SeekToLiveIcon,
  } from "./icons";

  // i18n: get translator from context, fall back to default English
  const translatorStore: Readable<TranslateFn> =
    getContext<Readable<TranslateFn> | undefined>("fw-translator") ??
    readable(createTranslator({ locale: "en" }));
  let t: TranslateFn = $derived($translatorStore);

  // Props - aligned with React PlayerControls
  interface Props {
    currentTime: number;
    duration: number;
    isVisible?: boolean;
    onseek?: (time: number) => void;
    mistStreamInfo?: MistStreamInfo;
    disabled?: boolean;
    playbackMode?: PlaybackMode;
    onModeChange?: (mode: PlaybackMode) => void;
    sourceType?: string;
    showStatsButton?: boolean;
    isStatsOpen?: boolean;
    onStatsToggle?: () => void;
    /** Content-type based live flag (for mode selector visibility, separate from seek bar isLive) */
    isContentLive?: boolean;
    /** Jump to live edge callback */
    onJumpToLive?: () => void;
    activeLocale?: FwLocale;
    onLocaleChange?: (locale: FwLocale) => void;
    /** Controller-derived seekable start (ms) — preferred over player direct */
    controllerSeekableStart?: number;
    /** Controller-derived live edge (ms) — preferred over player direct */
    controllerLiveEdge?: number;
  }

  let {
    currentTime,
    duration,
    isVisible = true,
    onseek = undefined,
    mistStreamInfo = undefined,
    disabled = false,
    playbackMode = "auto",
    onModeChange = undefined,
    sourceType = undefined,
    showStatsButton = false,
    isStatsOpen = false,
    onStatsToggle = undefined,
    isContentLive = undefined,
    onJumpToLive = undefined,
    activeLocale = undefined,
    onLocaleChange = undefined,
    controllerSeekableStart = undefined,
    controllerLiveEdge = undefined,
  }: Props = $props();

  // Video element discovery
  let video: HTMLVideoElement | null = $state(null);
  let videoCheckInterval: ReturnType<typeof setInterval> | null = null;

  function findVideoElement(): HTMLVideoElement | null {
    const player = globalPlayerManager.getCurrentPlayer();
    if (player?.getVideoElement?.()) return player.getVideoElement();
    return (
      (document.querySelector('[data-player-container="true"] video') as HTMLVideoElement | null) ??
      (document.querySelector(".fw-player-container video") as HTMLVideoElement | null)
    );
  }

  $effect(() => {
    video = findVideoElement();
    if (!video) {
      videoCheckInterval = setInterval(() => {
        const v = findVideoElement();
        if (v) {
          video = v;
          if (videoCheckInterval) {
            clearInterval(videoCheckInterval);
            videoCheckInterval = null;
          }
        }
      }, 100);
      setTimeout(() => {
        if (videoCheckInterval) {
          clearInterval(videoCheckInterval);
          videoCheckInterval = null;
        }
      }, 5000);
    }
    return () => {
      if (videoCheckInterval) {
        clearInterval(videoCheckInterval);
        videoCheckInterval = null;
      }
    };
  });

  // Local state
  let isPlaying = $state(false);
  let isMuted = $state(false);
  let isFullscreen = $state(false);
  let hasAudio = $state(true);
  let volumeValue = $state(100);
  let playbackRate = $state(1);
  let showSettingsMenu = $state(false);
  let isNearLiveState = $state(true);
  let buffered: TimeRanges | undefined = $state(undefined);
  let _hasSeekToLive = false; // Track if we've auto-seeked to live
  let qualityValue = $state("auto");
  let captionValue = $state("none");

  // Audio detection: trust MistServer metadata first, then DOM fallback
  $effect(() => {
    // Primary: trust MistServer stream metadata (matches ddvtech embed approach)
    if (mistStreamInfo?.hasAudio !== undefined) {
      hasAudio = mistStreamInfo.hasAudio;
      return;
    }

    if (!video) {
      hasAudio = true;
      return;
    }

    const checkAudio = () => {
      if (video!.srcObject instanceof MediaStream) {
        hasAudio = video!.srcObject.getAudioTracks().length > 0;
        return;
      }
      const videoAny = video as any;
      if (videoAny.audioTracks && videoAny.audioTracks.length !== undefined) {
        hasAudio = videoAny.audioTracks.length > 0;
        return;
      }
      hasAudio = true;
    };
    checkAudio();
    video.addEventListener("loadedmetadata", checkAudio);
    // Safari: audioTracks may be populated after loadedmetadata for HLS streams
    const audioTracks = (video as any).audioTracks;
    if (audioTracks?.addEventListener) {
      audioTracks.addEventListener("addtrack", checkAudio);
    }
    return () => {
      video!.removeEventListener("loadedmetadata", checkAudio);
      if (audioTracks?.removeEventListener) {
        audioTracks.removeEventListener("addtrack", checkAudio);
      }
    };
  });

  // Text tracks from player
  let textTracks = $derived.by(() => {
    return globalPlayerManager.getCurrentPlayer()?.getTextTracks?.() ?? [];
  });

  // Quality selection priority:
  // 1. Player-provided qualities (HLS.js/DASH.js levels with correct numeric indices)
  // 2. Mist track metadata (for players that don't provide quality API)
  // This fixes a critical bug where Mist track IDs (e.g., "a1", "v0") were passed to
  // HLS/DASH players which expect numeric indices (e.g., "0", "1", "2")
  let qualities = $derived.by(() => {
    // Try player's quality API first - this returns properly indexed levels
    const playerQualities = globalPlayerManager.getCurrentPlayer()?.getQualities?.();
    if (playerQualities && playerQualities.length > 0) {
      return playerQualities;
    }

    // Fallback to Mist track metadata for players without quality API
    const mistTracks = mistStreamInfo?.meta?.tracks;
    if (mistTracks) {
      return Object.entries(mistTracks)
        .filter(([, t]) => t.type === "video")
        .map(([id, t]) => ({
          id,
          label: t.height ? `${t.height}p` : t.codec,
          width: t.width,
          height: t.height,
          bitrate: t.bps,
        }))
        .sort((a, b) => (b.height || 0) - (a.height || 0));
    }
    return [];
  });

  // Hover state for volume
  let isVolumeHovered = $state(false);
  let isVolumeFocused = $state(false);
  let isVolumeExpanded = $derived(isVolumeHovered || isVolumeFocused);
  let volumeGroupRef: HTMLDivElement | null = $state(null);

  // Derived values - using centralized core utilities
  let isLive = $derived(isLiveContent(isContentLive, mistStreamInfo, duration));
  let isWebRTC = $derived(isMediaStreamSource(video));
  let supportsPlaybackRate = $derived(coreSupportsPlaybackRate(video));
  function deriveBufferWindowMs(
    tracks?: Record<string, { type?: string; firstms?: number; lastms?: number }>
  ): number | undefined {
    if (!tracks) return undefined;
    const trackValues = Object.values(tracks).filter(
      (t) => t.type !== "meta" && (t.lastms === undefined || t.lastms > 0)
    );
    if (trackValues.length === 0) return undefined;
    const firstmsValues = trackValues
      .map((t) => t.firstms)
      .filter((v): v is number => v !== undefined);
    const lastmsValues = trackValues
      .map((t) => t.lastms)
      .filter((v): v is number => v !== undefined);
    if (firstmsValues.length === 0 || lastmsValues.length === 0) return undefined;
    const firstms = Math.min(...firstmsValues);
    const lastms = Math.max(...lastmsValues);
    const window = lastms - firstms;
    if (!Number.isFinite(window) || window <= 0) return undefined;
    return window;
  }

  let bufferWindowMs = $derived(
    mistStreamInfo?.meta?.buffer_window ??
      deriveBufferWindowMs(
        mistStreamInfo?.meta?.tracks as
          | Record<string, { type?: string; firstms?: number; lastms?: number }>
          | undefined
      )
  );

  let allowMediaStreamDvr = $derived(
    isMediaStreamSource(video) &&
      bufferWindowMs !== undefined &&
      bufferWindowMs > 0 &&
      sourceType !== "whep" &&
      sourceType !== "webrtc"
  );

  // Seekable range: prefer controller-derived values (same pattern as React/WC)
  let calcRange = $derived(
    calculateSeekableRange({
      isLive,
      video,
      mistStreamInfo,
      currentTime,
      duration,
      allowMediaStreamDvr,
    })
  );
  let useControllerRange = $derived(
    Number.isFinite(controllerSeekableStart) &&
      Number.isFinite(controllerLiveEdge) &&
      (controllerLiveEdge as number) >= (controllerSeekableStart as number) &&
      ((controllerLiveEdge as number) > 0 || (controllerSeekableStart as number) > 0)
  );
  let seekableRange = $derived({
    seekableStart: useControllerRange
      ? (controllerSeekableStart as number)
      : calcRange.seekableStart,
    liveEdge: useControllerRange ? (controllerLiveEdge as number) : calcRange.liveEdge,
  });
  let seekableStart = $derived(seekableRange.seekableStart);
  let liveEdge = $derived(seekableRange.liveEdge);
  let hasDvrWindow = $derived(
    isLive &&
      Number.isFinite(liveEdge) &&
      Number.isFinite(seekableStart) &&
      liveEdge > seekableStart
  );
  let commitOnRelease = $derived(isLive);

  // Live thresholds with buffer window scaling
  let liveThresholds = $derived(calculateLiveThresholds(sourceType, isWebRTC, bufferWindowMs));

  // Can seek - check player's canSeek method first (for WebCodecs, MEWS server-side seeking)
  let baseCanSeek = $derived.by(() => {
    // Check if current player has canSeek method
    const player = globalPlayerManager.getCurrentPlayer();
    if (player && typeof (player as any).canSeek === "function") {
      return (player as any).canSeek();
    }
    // Fallback to core utility logic
    return canSeekStream({
      video,
      isLive,
      duration,
      bufferWindowMs,
    });
  });
  let canSeek = $derived(baseCanSeek && (!isLive || hasDvrWindow));

  // Update state from video events
  $effect(() => {
    if (!video) return;

    function updatePlayingState() {
      const player = globalPlayerManager.getCurrentPlayer();
      const paused = player?.isPaused?.() ?? video!.paused;
      isPlaying = !paused;
    }
    function updateMutedState() {
      isMuted = video!.muted || video!.volume === 0;
      const vol = video!.volume;
      volumeValue = Number.isFinite(vol) ? Math.round(vol * 100) : 100;
    }
    function updateFullscreenState() {
      isFullscreen = !!document.fullscreenElement;
    }
    function updatePlaybackRate() {
      playbackRate = video!.playbackRate;
    }
    function updateBuffered() {
      const player = globalPlayerManager.getCurrentPlayer();
      buffered = player?.getBufferedRanges?.() ?? video!.buffered;
    }

    updatePlayingState();
    updateMutedState();
    updateFullscreenState();
    updatePlaybackRate();
    updateBuffered();

    video.addEventListener("play", updatePlayingState);
    video.addEventListener("pause", updatePlayingState);
    video.addEventListener("playing", updatePlayingState);
    video.addEventListener("volumechange", updateMutedState);
    video.addEventListener("ratechange", updatePlaybackRate);
    video.addEventListener("progress", updateBuffered);
    video.addEventListener("loadeddata", updateBuffered);
    document.addEventListener("fullscreenchange", updateFullscreenState);

    return () => {
      video!.removeEventListener("play", updatePlayingState);
      video!.removeEventListener("pause", updatePlayingState);
      video!.removeEventListener("playing", updatePlayingState);
      video!.removeEventListener("volumechange", updateMutedState);
      video!.removeEventListener("ratechange", updatePlaybackRate);
      video!.removeEventListener("progress", updateBuffered);
      video!.removeEventListener("loadeddata", updateBuffered);
      document.removeEventListener("fullscreenchange", updateFullscreenState);
    };
  });

  // Reset seek-to-live flag when video element changes
  $effect(() => {
    if (video) {
      _hasSeekToLive = false;
    }
  });

  // Hysteresis for live badge - using core calculation
  $effect(() => {
    if (!isLive) {
      isNearLiveState = true; // Always "at live" for VOD
      return;
    }
    isNearLiveState = calculateIsNearLive(currentTime, liveEdge, liveThresholds, isNearLiveState);
  });

  // Time display - using core formatTimeDisplay
  let timeDisplay = $derived(
    formatTimeDisplay({
      isLive,
      currentTime,
      duration,
      liveEdge,
      seekableStart,
      unixoffset: mistStreamInfo?.unixoffset,
    })
  );

  // Seek value for slider
  let _seekValue = $derived.by(() => {
    if (isLive) {
      const window = liveEdge - seekableStart;
      if (window <= 0) return 1000;
      return ((currentTime - seekableStart) / window) * 1000;
    }
    return Number.isFinite(duration) && duration > 0 ? (currentTime / duration) * 1000 : 0;
  });

  // Handlers
  function handlePlayPause() {
    if (disabled) return;
    const player = globalPlayerManager.getCurrentPlayer();
    const v = player?.getVideoElement?.() ?? video;
    if (!v && !player) return;
    const paused = player?.isPaused?.() ?? v?.paused ?? true;
    if (paused) {
      player?.play?.().catch(() => {});
      v?.play?.().catch(() => {});
    } else {
      player?.pause?.();
      v?.pause?.();
    }
  }

  function handleSkipBack() {
    const newTime = Math.max(0, currentTime - 10000);
    if (onseek) {
      onseek(newTime);
      return;
    }
    const v = findVideoElement();
    if (v) v.currentTime = newTime / 1000;
  }

  function handleSkipForward() {
    const maxTime = Number.isFinite(duration) ? duration : currentTime + 10000;
    const newTime = Math.min(maxTime, currentTime + 10000);
    if (onseek) {
      onseek(newTime);
      return;
    }
    const v = findVideoElement();
    if (v) v.currentTime = newTime / 1000;
  }

  function handleMute() {
    if (disabled) return;
    const player = globalPlayerManager.getCurrentPlayer();
    if (player?.setMuted) {
      const currentlyMuted = player.isMuted?.() ?? video?.muted ?? false;
      player.setMuted(!currentlyMuted);
    } else {
      // Fallback: direct video manipulation
      const v = video;
      if (!v) return;
      v.muted = !v.muted;
    }
  }

  function handleVolumeChange(val: number) {
    if (disabled) return;
    const next = Math.max(0, Math.min(100, val ?? 100));
    if (!Number.isFinite(next)) return;

    const player = globalPlayerManager.getCurrentPlayer();
    if (player?.setVolume) {
      // Use core controller which handles mute/unmute logic
      player.setVolume(next / 100);
    } else {
      // Fallback: direct video manipulation
      const v = video;
      if (!v) return;
      v.volume = next / 100;
      v.muted = next === 0;
    }
    volumeValue = next;
    isMuted = next === 0;
  }

  function handleFullscreen() {
    if (disabled) return;
    const container = document.querySelector(
      '[data-player-container="true"]'
    ) as HTMLElement | null;
    if (!container) return;
    if (document.fullscreenElement) {
      document.exitFullscreen().catch(() => {});
    } else {
      container.requestFullscreen().catch(() => {});
    }
  }

  function handleGoLive() {
    if (disabled || !video) return;
    if (onJumpToLive) {
      onJumpToLive();
      return;
    }
    globalPlayerManager.getCurrentPlayer()?.jumpToLive?.();
  }

  function handleSpeedSelect(rate: number) {
    if (disabled) return;
    // Use findVideoElement for robust detection
    const v = findVideoElement();
    if (!v) return;
    v.playbackRate = rate;
    showSettingsMenu = false;
  }

  function handleQualityChange(value: string) {
    if (disabled) return;
    qualityValue = value;
    globalPlayerManager.getCurrentPlayer()?.selectQuality?.(value);
    showSettingsMenu = false;
  }

  function handleCaptionChange(value: string) {
    if (disabled) return;
    captionValue = value;
    if (value === "none") {
      globalPlayerManager.getCurrentPlayer()?.selectTextTrack?.(null);
    } else {
      globalPlayerManager.getCurrentPlayer()?.selectTextTrack?.(value);
    }
    showSettingsMenu = false;
  }

  // Non-passive wheel listener for volume control
  $effect(() => {
    if (!volumeGroupRef) return;
    const handler = (e: WheelEvent) => {
      if (disabled || !hasAudio) return;
      e.preventDefault();
      const delta = e.deltaY < 0 ? 5 : -5;
      handleVolumeChange(volumeValue + delta);
    };
    volumeGroupRef.addEventListener("wheel", handler, { passive: false });
    return () => volumeGroupRef?.removeEventListener("wheel", handler);
  });

  // Close menu when clicking outside - with debounce to prevent immediate close from same click
  $effect(() => {
    if (!showSettingsMenu) return;

    const handleClick = (e: MouseEvent) => {
      const target = e.target as HTMLElement;
      if (!target.closest(".fw-settings-menu")) {
        showSettingsMenu = false;
      }
    };

    // Debounce to prevent immediate close from the same click that opened the menu
    const timeout = setTimeout(() => {
      window.addEventListener("click", handleClick);
    }, 0);

    return () => {
      clearTimeout(timeout);
      window.removeEventListener("click", handleClick);
    };
  });
</script>

<div
  class={cn(
    "fw-controls-wrapper",
    isVisible ? "fw-controls-wrapper--visible" : "fw-controls-wrapper--hidden"
  )}
>
  <!-- Control bar -->
  <div
    class="fw-control-bar pointer-events-auto"
    role="toolbar"
    aria-label="Media controls"
    tabindex="-1"
    onclick={(e) => e.stopPropagation()}
    onkeydown={(e) => e.stopPropagation()}
  >
    <!-- Seek bar -->
    {#if canSeek}
      <div class="fw-seek-wrapper">
        <SeekBar
          {currentTime}
          {duration}
          {buffered}
          {disabled}
          {isLive}
          {seekableStart}
          {liveEdge}
          {commitOnRelease}
          onseek={(time) => {
            if (onseek) {
              onseek(time);
            } else if (video) {
              video.currentTime = time / 1000;
            }
          }}
        />
      </div>
    {/if}

    <!-- Control buttons -->
    <div class="fw-controls-row">
      <!-- Left: Play, Skip, Volume, Time, Live -->
      <div class="fw-controls-left">
        <div class="fw-control-group">
          <button
            type="button"
            class="fw-btn-flush"
            aria-label={isPlaying ? t("pause") : t("play")}
            onclick={handlePlayPause}
            {disabled}
          >
            {#if isPlaying}
              <PauseIcon size={18} />
            {:else}
              <PlayIcon size={18} />
            {/if}
          </button>
          {#if canSeek}
            <button
              type="button"
              class="fw-btn-flush hidden sm:flex"
              aria-label={t("skipBackward")}
              onclick={handleSkipBack}
              {disabled}
            >
              <SkipBackIcon size={16} />
            </button>
            <button
              type="button"
              class="fw-btn-flush hidden sm:flex"
              aria-label={t("skipForward")}
              onclick={handleSkipForward}
              {disabled}
            >
              <SkipForwardIcon size={16} />
            </button>
          {/if}
        </div>

        <!-- Volume -->
        <div
          bind:this={volumeGroupRef}
          class={cn(
            "fw-volume-group",
            isVolumeExpanded && "fw-volume-group--expanded",
            !hasAudio && "fw-volume-group--disabled"
          )}
          role="group"
          aria-label={t("volume")}
          onmouseenter={() => hasAudio && (isVolumeHovered = true)}
          onmouseleave={() => {
            isVolumeHovered = false;
            isVolumeFocused = false;
          }}
          onfocusin={() => hasAudio && (isVolumeFocused = true)}
          onfocusout={(e) => {
            if (!e.currentTarget.contains(e.relatedTarget as Node)) isVolumeFocused = false;
          }}
          onpointerup={(e) => {
            if (hasAudio && e.target === e.currentTarget) {
              handleMute();
            }
          }}
        >
          <button
            type="button"
            class="fw-volume-btn"
            aria-label={!hasAudio ? t("muted") : isMuted ? t("unmute") : t("mute")}
            title={!hasAudio ? t("muted") : isMuted ? t("unmute") : t("mute")}
            onclick={handleMute}
            disabled={!hasAudio}
          >
            <VolumeIcons {isMuted} volume={volumeValue / 100} size={16} />
          </button>
          <div
            class={cn(
              "fw-volume-slider-wrapper",
              isVolumeExpanded
                ? "fw-volume-slider-wrapper--expanded"
                : "fw-volume-slider-wrapper--collapsed"
            )}
          >
            <Slider
              min={0}
              max={100}
              step={1}
              value={isMuted ? 0 : volumeValue}
              oninput={handleVolumeChange}
              orientation="horizontal"
              className="w-full"
              aria-label={t("volume")}
              disabled={!hasAudio}
            />
          </div>
        </div>

        <div class="fw-control-group">
          <span class="fw-time-display">
            {timeDisplay}
          </span>
        </div>

        {#if isLive}
          <div class="fw-control-group">
            <button
              type="button"
              onclick={handleGoLive}
              disabled={!hasDvrWindow || isNearLiveState}
              class={cn(
                "fw-live-badge",
                !hasDvrWindow || isNearLiveState ? "fw-live-badge--active" : "fw-live-badge--behind"
              )}
              title={t("live")}
            >
              {t("live").toUpperCase()}
              {#if !isNearLiveState && hasDvrWindow}
                <SeekToLiveIcon size={10} />
              {/if}
            </button>
          </div>
        {/if}
      </div>

      <!-- Right: Stats, Settings, Fullscreen -->
      <div class="fw-controls-right">
        {#if showStatsButton}
          <div class="fw-control-group">
            <button
              type="button"
              class={cn("fw-btn-flush", isStatsOpen && "fw-btn-flush--active")}
              aria-label={t("showStats")}
              title={t("showStats")}
              onclick={onStatsToggle}
              {disabled}
            >
              <StatsIcon size={16} />
            </button>
          </div>
        {/if}
        <div class="fw-control-group relative">
          <button
            type="button"
            class={cn("fw-btn-flush group", showSettingsMenu && "fw-btn-flush--active")}
            aria-label={t("settings")}
            title={t("settings")}
            onclick={() => (showSettingsMenu = !showSettingsMenu)}
            {disabled}
          >
            <SettingsIcon size={16} class="transition-transform group-hover:rotate-90" />
          </button>

          {#if showSettingsMenu}
            <div class="fw-settings-menu">
              <!-- Playback Mode - only show for live content (not VOD/clips) -->
              {#if onModeChange && isContentLive !== false}
                <div class="fw-settings-section">
                  <div class="fw-settings-label">{t("mode")}</div>
                  <div class="fw-settings-options">
                    {#each ["auto", "low-latency", "quality"] as mode}
                      <button
                        type="button"
                        class={cn(
                          "fw-settings-btn",
                          playbackMode === mode && "fw-settings-btn--active"
                        )}
                        onclick={() => {
                          onModeChange(mode as PlaybackMode);
                          showSettingsMenu = false;
                        }}
                      >
                        {mode === "low-latency"
                          ? t("fast")
                          : mode === "quality"
                            ? t("stable")
                            : t("auto")}
                      </button>
                    {/each}
                  </div>
                </div>
              {/if}

              <!-- Speed (hidden for WebRTC MediaStream) -->
              {#if supportsPlaybackRate}
                <div class="fw-settings-section">
                  <div class="fw-settings-label">{t("speed")}</div>
                  <div class="fw-settings-options fw-settings-options--wrap">
                    {#each SPEED_PRESETS as rate}
                      <button
                        type="button"
                        class={cn(
                          "fw-settings-btn",
                          playbackRate === rate && "fw-settings-btn--active"
                        )}
                        onclick={() => handleSpeedSelect(rate)}
                        {disabled}
                      >
                        {rate}x
                      </button>
                    {/each}
                  </div>
                </div>
              {/if}

              <!-- Quality -->
              {#if qualities.length > 0}
                <div class="fw-settings-section">
                  <div class="fw-settings-label">{t("quality")}</div>
                  <div class="fw-settings-list">
                    <button
                      class={cn(
                        "fw-settings-list-item",
                        qualityValue === "auto" && "fw-settings-list-item--active"
                      )}
                      onclick={() => handleQualityChange("auto")}
                    >
                      {t("auto")}
                    </button>
                    {#each qualities as q}
                      <button
                        class={cn(
                          "fw-settings-list-item",
                          qualityValue === q.id && "fw-settings-list-item--active"
                        )}
                        onclick={() => handleQualityChange(q.id)}
                      >
                        {q.label}
                      </button>
                    {/each}
                  </div>
                </div>
              {/if}

              <!-- Captions -->
              {#if textTracks.length > 0}
                <div class="fw-settings-section">
                  <div class="fw-settings-label">{t("captions")}</div>
                  <div class="fw-settings-list">
                    <button
                      class={cn(
                        "fw-settings-list-item",
                        captionValue === "none" && "fw-settings-list-item--active"
                      )}
                      onclick={() => handleCaptionChange("none")}
                    >
                      {t("captionsOff")}
                    </button>
                    {#each textTracks as tt}
                      <button
                        class={cn(
                          "fw-settings-list-item",
                          captionValue === tt.id && "fw-settings-list-item--active"
                        )}
                        onclick={() => handleCaptionChange(tt.id)}
                      >
                        {tt.label || tt.id}
                      </button>
                    {/each}
                  </div>
                </div>
              {/if}

              <!-- Locale -->
              {#if onLocaleChange}
                <div class="fw-settings-section">
                  <div class="fw-settings-label">{t("language")}</div>
                  <div class="fw-settings-list">
                    {#each getAvailableLocales() as loc}
                      <button
                        type="button"
                        class={cn(
                          "fw-settings-list-item",
                          activeLocale === loc && "fw-settings-list-item--active"
                        )}
                        onclick={() => onLocaleChange(loc)}
                      >
                        {getLocaleDisplayName(loc)}
                      </button>
                    {/each}
                  </div>
                </div>
              {/if}
            </div>
          {/if}
        </div>

        <div class="fw-control-group">
          <button
            type="button"
            class="fw-btn-flush"
            aria-label={isFullscreen ? t("exitFullscreen") : t("fullscreen")}
            title={t("fullscreen")}
            onclick={handleFullscreen}
            {disabled}
          >
            {#if isFullscreen}
              <FullscreenExitIcon size={16} />
            {:else}
              <FullscreenIcon size={16} />
            {/if}
          </button>
        </div>
      </div>
    </div>
  </div>
</div>

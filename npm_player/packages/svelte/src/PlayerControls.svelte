<script lang="ts">
  import {
    cn,
    globalPlayerManager,
    type MistStreamInfo,
    type PlaybackMode,
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
  } from "@livepeer-frameworks/player-core";
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

  // Derived values - using centralized core utilities
  let isLive = $derived(isLiveContent(isContentLive, mistStreamInfo, duration));
  let isWebRTC = $derived(isMediaStreamSource(video));
  let supportsPlaybackRate = $derived(coreSupportsPlaybackRate(video));
  function deriveBufferWindowMs(
    tracks?: Record<string, { firstms?: number; lastms?: number }>
  ): number | undefined {
    if (!tracks) return undefined;
    const list = Object.values(tracks);
    if (list.length === 0) return undefined;
    const firstmsValues = list.map((t) => t.firstms).filter((v): v is number => v !== undefined);
    const lastmsValues = list.map((t) => t.lastms).filter((v): v is number => v !== undefined);
    if (firstmsValues.length === 0 || lastmsValues.length === 0) return undefined;
    const firstms = Math.max(...firstmsValues);
    const lastms = Math.min(...lastmsValues);
    const window = lastms - firstms;
    if (!Number.isFinite(window) || window <= 0) return undefined;
    return window;
  }

  let bufferWindowMs = $derived(
    mistStreamInfo?.meta?.buffer_window ??
      deriveBufferWindowMs(
        mistStreamInfo?.meta?.tracks as
          | Record<string, { firstms?: number; lastms?: number }>
          | undefined
      )
  );

  function getPlayerSeekableRange(): { seekableStart: number; liveEdge: number } | null {
    const player = globalPlayerManager.getCurrentPlayer();
    if (player && typeof (player as any).getSeekableRange === "function") {
      const range = (player as any).getSeekableRange();
      if (
        range &&
        Number.isFinite(range.start) &&
        Number.isFinite(range.end) &&
        range.end >= range.start
      ) {
        return { seekableStart: range.start, liveEdge: range.end };
      }
    }
    return null;
  }

  const allowMediaStreamDvr =
    isMediaStreamSource(video) &&
    bufferWindowMs !== undefined &&
    bufferWindowMs > 0 &&
    sourceType !== "whep" &&
    sourceType !== "webrtc";

  // Seekable range using core calculation (allow player override)
  let seekableRange = $derived.by(
    () =>
      getPlayerSeekableRange() ??
      calculateSeekableRange({
        isLive,
        video,
        mistStreamInfo,
        currentTime,
        duration,
        allowMediaStreamDvr,
      })
  );
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
    const newTime = Math.max(0, currentTime - 10);
    if (onseek) {
      onseek(newTime);
      return;
    }
    const v = findVideoElement();
    if (v) v.currentTime = newTime;
  }

  function handleSkipForward() {
    const maxTime = Number.isFinite(duration) ? duration : currentTime + 10;
    const newTime = Math.min(maxTime, currentTime + 10);
    if (onseek) {
      onseek(newTime);
      return;
    }
    const v = findVideoElement();
    if (v) v.currentTime = newTime;
  }

  function handleMute() {
    if (disabled) return;
    const player = globalPlayerManager.getCurrentPlayer();
    if (player?.toggleMute) {
      // Use core controller which handles volume restore
      player.toggleMute();
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
    "fw-player-surface fw-controls-wrapper",
    isVisible ? "fw-controls-wrapper--visible" : "fw-controls-wrapper--hidden"
  )}
>
  <!-- Control bar -->
  <div class="fw-control-bar pointer-events-auto" onclick={(e) => e.stopPropagation()}>
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
              video.currentTime = time;
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
            aria-label={isPlaying ? "Pause" : "Play"}
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
              aria-label="Skip back 10s"
              onclick={handleSkipBack}
              {disabled}
            >
              <SkipBackIcon size={16} />
            </button>
            <button
              type="button"
              class="fw-btn-flush hidden sm:flex"
              aria-label="Skip forward 10s"
              onclick={handleSkipForward}
              {disabled}
            >
              <SkipForwardIcon size={16} />
            </button>
          {/if}
        </div>

        <!-- Volume -->
        <div
          class={cn(
            "fw-volume-group",
            isVolumeExpanded && "fw-volume-group--expanded",
            !hasAudio && "fw-volume-group--disabled"
          )}
          onmouseenter={() => hasAudio && (isVolumeHovered = true)}
          onmouseleave={() => {
            isVolumeHovered = false;
            isVolumeFocused = false;
          }}
          onfocuscapture={() => hasAudio && (isVolumeFocused = true)}
          onblurcapture={(e) => {
            if (!e.currentTarget.contains(e.relatedTarget as Node)) isVolumeFocused = false;
          }}
          onclick={(e) => {
            if (disabled) return;
            if (hasAudio && e.target === e.currentTarget) {
              handleMute();
            }
          }}
        >
          <button
            type="button"
            class="fw-volume-btn"
            aria-label={!hasAudio ? "No audio" : isMuted ? "Unmute" : "Mute"}
            title={!hasAudio ? "No audio" : isMuted ? "Unmute" : "Mute"}
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
              aria-label="Volume"
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
              title={!hasDvrWindow
                ? "Live only"
                : isNearLiveState
                  ? "At live edge"
                  : "Jump to live"}
            >
              LIVE
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
              aria-label="Toggle stats"
              title="Stats"
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
            aria-label="Settings"
            title="Settings"
            onclick={() => (showSettingsMenu = !showSettingsMenu)}
            {disabled}
          >
            <SettingsIcon size={16} class="transition-transform group-hover:rotate-90" />
          </button>

          {#if showSettingsMenu}
            <div class="fw-player-surface fw-settings-menu">
              <!-- Playback Mode - only show for live content (not VOD/clips) -->
              {#if onModeChange && isContentLive !== false}
                <div class="fw-settings-section">
                  <div class="fw-settings-label">Mode</div>
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
                        {mode === "low-latency" ? "Fast" : mode === "quality" ? "Stable" : "Auto"}
                      </button>
                    {/each}
                  </div>
                </div>
              {/if}

              <!-- Speed (hidden for WebRTC MediaStream) -->
              {#if supportsPlaybackRate}
                <div class="fw-settings-section">
                  <div class="fw-settings-label">Speed</div>
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
                  <div class="fw-settings-label">Quality</div>
                  <div class="fw-settings-list">
                    <button
                      class={cn(
                        "fw-settings-list-item",
                        qualityValue === "auto" && "fw-settings-list-item--active"
                      )}
                      onclick={() => handleQualityChange("auto")}
                    >
                      Auto
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
                  <div class="fw-settings-label">Captions</div>
                  <div class="fw-settings-list">
                    <button
                      class={cn(
                        "fw-settings-list-item",
                        captionValue === "none" && "fw-settings-list-item--active"
                      )}
                      onclick={() => handleCaptionChange("none")}
                    >
                      Off
                    </button>
                    {#each textTracks as t}
                      <button
                        class={cn(
                          "fw-settings-list-item",
                          captionValue === t.id && "fw-settings-list-item--active"
                        )}
                        onclick={() => handleCaptionChange(t.id)}
                      >
                        {t.label || t.id}
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
            aria-label="Toggle fullscreen"
            title="Fullscreen"
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

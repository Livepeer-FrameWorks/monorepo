import React, { useState, useCallback, useEffect, useMemo } from "react";
import IdleScreen from "./IdleScreen";
import TitleOverlay from "./TitleOverlay";
import StatsPanel from "./StatsPanel";
import PlayerControls from "./PlayerControls";
import DevModePanel from "./DevModePanel";
import SpeedIndicator from "./SpeedIndicator";
import type { SkipDirection } from "./SkipIndicator";
import SkipIndicator from "./SkipIndicator";
import { StatsIcon, SettingsIcon, PictureInPictureIcon } from "./Icons";
import type { PlayerProps } from "../types";
import { usePlayerController } from "../hooks/usePlayerController";
import { PlayerProvider } from "../context/PlayerContext";
import { I18nProvider } from "../context/I18nContext";
import {
  cn,
  themeOverridesToStyle,
  resolveTheme,
  createTranslator,
} from "@livepeer-frameworks/player-core";
import type {
  PlaybackMode,
  EndpointInfo,
  FwThemePreset,
  FwLocale,
} from "@livepeer-frameworks/player-core";
import {
  ContextMenu,
  ContextMenuContent,
  ContextMenuItem,
  ContextMenuSeparator,
  ContextMenuTrigger,
} from "../ui/context-menu";

/**
 * Inner player component that uses PlayerController via hook
 */
const PlayerInner: React.FC<PlayerProps> = ({
  contentId,
  contentType,
  thumbnailUrl = null,
  options,
  endpoints: propsEndpoints,
  onStateChange,
  children,
}) => {
  // ============================================================================
  // UI-only State (stays in wrapper)
  // ============================================================================
  const [isStatsOpen, setIsStatsOpen] = useState(false);
  const [isDevPanelOpen, setIsDevPanelOpen] = useState(false);
  const [skipDirection, setSkipDirection] = useState<SkipDirection>(null);
  const [activeTheme, setActiveTheme] = useState<FwThemePreset>(options?.theme ?? "default");
  const [activeLocale, setActiveLocale] = useState<FwLocale>(options?.locale ?? "en");
  const t = useMemo(() => createTranslator({ locale: activeLocale }), [activeLocale]);

  // Error fade-out: keep showing the overlay briefly while it animates out
  const [displayedError, setDisplayedError] = useState<string | null>(null);
  const [displayedIsPassive, setDisplayedIsPassive] = useState(false);
  const [isErrorDismissing, setIsErrorDismissing] = useState(false);

  // Sync theme/locale from props when parent changes them
  useEffect(() => {
    if (options?.theme) setActiveTheme(options.theme);
  }, [options?.theme]);
  useEffect(() => {
    if (options?.locale) setActiveLocale(options.locale);
  }, [options?.locale]);

  // Playback mode preference (persistent)
  const [devPlaybackMode, setDevPlaybackMode] = useState<PlaybackMode>(
    options?.playbackMode || "auto"
  );

  // ============================================================================
  // PlayerController Hook - ALL business logic
  // ============================================================================
  const playerHook = usePlayerController({
    contentId,
    contentType,
    endpoints: propsEndpoints,
    gatewayUrl: options?.gatewayUrl,
    mistUrl: options?.mistUrl,
    authToken: options?.authToken,
    autoplay: options?.autoplay !== false,
    muted: options?.muted !== false,
    controls: options?.stockControls === true,
    poster: thumbnailUrl || undefined,
    debug: options?.debug,
    onStateChange: (playerState) => {
      onStateChange?.(playerState);
    },
    onError: (error) => {
      console.warn("[Player] Error:", error);
    },
  });

  const {
    containerRef,
    state,
    controller,
    togglePlay,
    toggleMute,
    toggleLoop,
    toggleFullscreen,
    togglePiP,
    setVolume,
    selectQuality,
    clearError,
    dismissToast,
    retry,
    reload,
    jumpToLive,
    handleMouseEnter,
    handleMouseLeave,
    handleMouseMove,
    setDevModeOptions,
  } = playerHook;

  // Error fade-out transition: when error clears, animate out before removing
  useEffect(() => {
    if (state.error) {
      setDisplayedError(state.error);
      setDisplayedIsPassive(state.isPassiveError);
      setIsErrorDismissing(false);
    } else if (displayedError) {
      setIsErrorDismissing(true);
      const timer = setTimeout(() => {
        setDisplayedError(null);
        setDisplayedIsPassive(false);
        setIsErrorDismissing(false);
      }, 300);
      return () => clearTimeout(timer);
    }
  }, [state.error, state.isPassiveError]);

  // ============================================================================
  // Dev Mode Callbacks
  // ============================================================================
  const handleDevSettingsChange = useCallback(
    (settings: { forcePlayer?: string; forceType?: string; forceSource?: number }) => {
      // One-shot selection - controller handles the state
      setDevModeOptions({
        forcePlayer: settings.forcePlayer,
        forceType: settings.forceType,
        forceSource: settings.forceSource,
      });
    },
    [setDevModeOptions]
  );

  const handleModeChange = useCallback(
    (mode: PlaybackMode) => {
      setDevPlaybackMode(mode);
      // Mode is a persistent preference
      setDevModeOptions({ playbackMode: mode });
    },
    [setDevModeOptions]
  );

  const handleReload = useCallback(() => {
    clearError();
    reload();
  }, [clearError, reload]);

  const handleStatsToggle = useCallback(() => {
    setIsStatsOpen((prev) => !prev);
  }, []);

  // Clear skip indicator after animation
  const handleSkipIndicatorHide = useCallback(() => {
    setSkipDirection(null);
  }, []);

  // Auto-dismiss toast after 3 seconds
  useEffect(() => {
    if (!state.toast) return;
    const timer = setTimeout(() => {
      dismissToast();
    }, 3000);
    return () => clearTimeout(timer);
  }, [state.toast, dismissToast]);

  // ============================================================================
  // Derived Values
  // ============================================================================
  const primaryEndpoint = state.endpoints?.primary as EndpointInfo | undefined;
  const isLegacyPlayer = state.currentPlayerInfo?.shortname === "mist-legacy";
  const useStockControls = options?.stockControls === true || isLegacyPlayer;

  // Title overlay visibility: show on hover or when paused
  const showTitleOverlay =
    (state.isHovering || state.isPaused) &&
    !state.shouldShowIdleScreen &&
    !state.isBuffering &&
    !state.error;

  // Buffering spinner: only during active playback
  const showBufferingSpinner =
    !state.shouldShowIdleScreen && state.isBuffering && !state.error && state.hasPlaybackStarted;

  // ============================================================================
  // Waiting for Endpoint (shown as overlay, not early return)
  // ============================================================================
  const showWaitingForEndpoint = !state.endpoints?.primary && state.state !== "booting";
  const waitingMessage = options?.gatewayUrl
    ? state.state === "gateway_loading"
      ? t("resolvingEndpoint")
      : t("waitingForStream")
    : t("waitingForStream");

  // ============================================================================
  // Render
  // ============================================================================
  return (
    <I18nProvider locale={activeLocale}>
      <PlayerProvider value={playerHook}>
        <ContextMenu>
          <ContextMenuTrigger asChild>
            <div
              className={cn(
                "fw-player-surface fw-player-root w-full h-full overflow-hidden",
                options?.devMode && "flex"
              )}
              role="region"
              aria-label="Video player"
              data-player-container="true"
              data-theme={activeTheme && activeTheme !== "default" ? activeTheme : undefined}
              style={(() => {
                const presetOverrides = activeTheme ? resolveTheme(activeTheme) : null;
                const merged = { ...presetOverrides, ...options?.themeOverrides };
                return Object.keys(merged).length > 0 ? themeOverridesToStyle(merged) : undefined;
              })()}
              tabIndex={0}
              onMouseEnter={handleMouseEnter}
              onMouseLeave={handleMouseLeave}
              onMouseMove={handleMouseMove}
            >
              {/* Player area */}
              <div
                className={cn("relative", options?.devMode ? "flex-1 min-w-0" : "w-full h-full")}
              >
                {/* Video container - PlayerController attaches here */}
                <div ref={containerRef} className="fw-player-container" />

                {/* Title/Description overlay */}
                <TitleOverlay
                  title={state.metadata?.title}
                  description={state.metadata?.description}
                  isVisible={showTitleOverlay}
                />

                {/* Stats panel */}
                <StatsPanel
                  isOpen={isStatsOpen}
                  onClose={handleStatsToggle}
                  metadata={state.metadata}
                  streamState={state.streamState}
                  quality={state.playbackQuality}
                  videoElement={state.videoElement}
                  protocol={primaryEndpoint?.protocol}
                  nodeId={primaryEndpoint?.nodeId}
                  geoDistance={primaryEndpoint?.geoDistance}
                />

                {/* Dev Mode Panel toggle */}
                {options?.devMode && !isDevPanelOpen && (
                  <DevModePanel
                    onSettingsChange={handleDevSettingsChange}
                    playbackMode={devPlaybackMode}
                    onModeChange={handleModeChange}
                    onReload={handleReload}
                    streamInfo={state.streamInfo}
                    mistStreamInfo={state.streamState?.streamInfo}
                    currentPlayer={state.currentPlayerInfo}
                    currentSource={state.currentSourceInfo}
                    videoElement={state.videoElement}
                    protocol={primaryEndpoint?.protocol}
                    nodeId={primaryEndpoint?.nodeId}
                    isVisible={false}
                    isOpen={false}
                    onOpenChange={setIsDevPanelOpen}
                  />
                )}

                {/* Speed indicator */}
                {state.isHoldingSpeed && (
                  <SpeedIndicator isVisible={true} speed={state.holdSpeed} />
                )}

                {/* Skip indicator */}
                <SkipIndicator
                  direction={skipDirection}
                  seconds={10}
                  onHide={handleSkipIndicatorHide}
                />

                {/* Waiting for endpoint overlay */}
                {showWaitingForEndpoint && <IdleScreen status="OFFLINE" message={waitingMessage} />}

                {/* Idle screen */}
                {!showWaitingForEndpoint && state.shouldShowIdleScreen && (
                  <IdleScreen
                    status={state.isEffectivelyLive ? state.streamState?.status : undefined}
                    message={state.isEffectivelyLive ? state.streamState?.message : t("loading")}
                    percentage={state.isEffectivelyLive ? state.streamState?.percentage : undefined}
                  />
                )}

                {/* Buffering spinner */}
                {showBufferingSpinner && (
                  <div role="status" aria-live="polite" className="fw-buffering-overlay">
                    <div className="fw-buffering-pill">
                      <div className="fw-buffering-spinner" />
                      <span>{t("buffering")}</span>
                    </div>
                  </div>
                )}

                {/* Passive error toast (non-blocking) */}
                {!state.shouldShowIdleScreen && displayedError && displayedIsPassive && (
                  <div
                    className={cn(
                      "absolute bottom-20 left-1/2 -translate-x-1/2 z-30 transition-opacity duration-300",
                      isErrorDismissing
                        ? "opacity-0"
                        : "animate-in fade-in slide-in-from-bottom-2 duration-200"
                    )}
                    role="status"
                    aria-live="polite"
                  >
                    <div className="flex items-center gap-2 rounded-lg border border-yellow-500/30 bg-black/80 px-4 py-2 text-sm text-white shadow-lg backdrop-blur-sm">
                      <span className="text-yellow-400 text-xs font-semibold uppercase">
                        {t("warning")}
                      </span>
                      <span>{displayedError}</span>
                      <button
                        type="button"
                        onClick={clearError}
                        className="ml-2 text-white/60 hover:text-white"
                        aria-label={t("dismiss")}
                      >
                        <svg width="12" height="12" viewBox="0 0 12 12" fill="none">
                          <path
                            d="M9 3L3 9M3 3L9 9"
                            stroke="currentColor"
                            strokeWidth="1.5"
                            strokeLinecap="round"
                          />
                        </svg>
                      </button>
                    </div>
                  </div>
                )}

                {/* Fatal error overlay (blocking) — auto-dismisses on playback resume */}
                {!state.shouldShowIdleScreen && displayedError && !displayedIsPassive && (
                  <div
                    role="alert"
                    aria-live="assertive"
                    className={cn(
                      "fw-error-overlay fw-error-overlay--fullscreen transition-opacity duration-300",
                      isErrorDismissing && "opacity-0"
                    )}
                  >
                    <div className="fw-error-popup fw-error-popup--fullscreen">
                      <div className="fw-error-header fw-error-header--error">
                        <span className="fw-error-title fw-error-title--error">{t("error")}</span>
                        <button
                          type="button"
                          className="fw-error-close"
                          onClick={clearError}
                          aria-label={t("dismiss")}
                        >
                          <svg width="12" height="12" viewBox="0 0 12 12" fill="none">
                            <path
                              d="M9 3L3 9M3 3L9 9"
                              stroke="currentColor"
                              strokeWidth="1.5"
                              strokeLinecap="round"
                            />
                          </svg>
                        </button>
                      </div>
                      <div className="fw-error-body">
                        <p className="fw-error-message">{displayedError}</p>
                      </div>
                      <div className="fw-error-actions">
                        <button
                          type="button"
                          className="fw-error-btn"
                          aria-label={t("retry")}
                          onClick={() => {
                            clearError();
                            retry();
                          }}
                        >
                          {t("retry")}
                        </button>
                        {options?.devMode && controller?.canAttemptFallback?.() && (
                          <button
                            type="button"
                            className="fw-error-btn fw-error-btn--secondary"
                            aria-label={t("tryNext")}
                            onClick={() => {
                              clearError();
                              controller?.retryWithFallback();
                            }}
                          >
                            {t("tryNext")}
                          </button>
                        )}
                        {options?.devMode && (
                          <button
                            type="button"
                            className="fw-error-btn fw-error-btn--secondary"
                            aria-label={t("reloadPlayer")}
                            onClick={() => {
                              clearError();
                              reload();
                            }}
                          >
                            {t("reloadPlayer")}
                          </button>
                        )}
                      </div>
                    </div>
                  </div>
                )}

                {/* Toast notification */}
                {state.toast && (
                  <div
                    className="absolute bottom-20 left-1/2 -translate-x-1/2 z-30 animate-in fade-in slide-in-from-bottom-2 duration-200"
                    role="status"
                    aria-live="polite"
                  >
                    <div className="flex items-center gap-2 rounded-lg border border-white/10 bg-black/80 px-4 py-2 text-sm text-white shadow-lg backdrop-blur-sm">
                      <span>{state.toast.message}</span>
                      <button
                        type="button"
                        onClick={dismissToast}
                        className="ml-2 text-white/60 hover:text-white"
                        aria-label={t("dismiss")}
                      >
                        <svg width="12" height="12" viewBox="0 0 12 12" fill="none">
                          <path
                            d="M9 3L3 9M3 3L9 9"
                            stroke="currentColor"
                            strokeWidth="1.5"
                            strokeLinecap="round"
                          />
                        </svg>
                      </button>
                    </div>
                  </div>
                )}

                {/* Player controls — custom children or default */}
                {children
                  ? children
                  : !useStockControls && (
                      <PlayerControls
                        currentTime={state.currentTime}
                        duration={state.duration}
                        isVisible={state.shouldShowControls}
                        onSeek={(t) => controller?.seek(t)}
                        showStatsButton={false}
                        isStatsOpen={isStatsOpen}
                        onStatsToggle={handleStatsToggle}
                        mistStreamInfo={state.streamState?.streamInfo}
                        disabled={!state.videoElement}
                        playbackMode={devPlaybackMode}
                        onModeChange={handleModeChange}
                        sourceType={state.currentSourceInfo?.type}
                        isContentLive={state.isEffectivelyLive}
                        // Props from usePlayerController hook
                        videoElement={state.videoElement}
                        qualities={state.qualities}
                        onSelectQuality={selectQuality}
                        isMuted={state.isMuted}
                        volume={state.volume}
                        onVolumeChange={setVolume}
                        onToggleMute={toggleMute}
                        isPlaying={state.isPlaying}
                        onTogglePlay={togglePlay}
                        onToggleFullscreen={toggleFullscreen}
                        isFullscreen={state.isFullscreen}
                        isLoopEnabled={state.isLoopEnabled}
                        onToggleLoop={toggleLoop}
                        onJumpToLive={jumpToLive}
                        activeLocale={activeLocale}
                        onLocaleChange={setActiveLocale}
                      />
                    )}
              </div>

              {/* Dev Mode Panel - side panel */}
              {options?.devMode && isDevPanelOpen && (
                <DevModePanel
                  onSettingsChange={handleDevSettingsChange}
                  playbackMode={devPlaybackMode}
                  onModeChange={handleModeChange}
                  onReload={handleReload}
                  streamInfo={state.streamInfo}
                  mistStreamInfo={state.streamState?.streamInfo}
                  currentPlayer={state.currentPlayerInfo}
                  currentSource={state.currentSourceInfo}
                  videoElement={state.videoElement}
                  protocol={primaryEndpoint?.protocol}
                  nodeId={primaryEndpoint?.nodeId}
                  isVisible={true}
                  isOpen={true}
                  onOpenChange={setIsDevPanelOpen}
                />
              )}
            </div>
          </ContextMenuTrigger>

          {/* Context menu */}
          <ContextMenuContent>
            <ContextMenuItem onSelect={handleStatsToggle} className="gap-2">
              <StatsIcon size={14} className="opacity-70 flex-shrink-0" />
              <span>{isStatsOpen ? t("hideStats") : t("showStats")}</span>
            </ContextMenuItem>
            {options?.devMode && (
              <>
                <ContextMenuSeparator />
                <ContextMenuItem
                  onSelect={() => setIsDevPanelOpen(!isDevPanelOpen)}
                  className="gap-2"
                >
                  <SettingsIcon size={14} className="opacity-70 flex-shrink-0" />
                  <span>{isDevPanelOpen ? t("hideSettings") : t("settings")}</span>
                </ContextMenuItem>
              </>
            )}
            <ContextMenuSeparator />
            <ContextMenuItem onSelect={togglePiP} className="gap-2">
              <PictureInPictureIcon size={14} className="opacity-70 flex-shrink-0" />
              <span>{t("pictureInPicture")}</span>
            </ContextMenuItem>
            <ContextMenuItem onSelect={toggleLoop} className="gap-2">
              <svg
                width="14"
                height="14"
                viewBox="0 0 24 24"
                fill="none"
                stroke="currentColor"
                strokeWidth="2"
                strokeLinecap="round"
                strokeLinejoin="round"
                className="opacity-70 flex-shrink-0"
              >
                <polyline points="17 1 21 5 17 9"></polyline>
                <path d="M3 11V9a4 4 0 0 1 4-4h14"></path>
                <polyline points="7 23 3 19 7 15"></polyline>
                <path d="M21 13v2a4 4 0 0 1-4 4H3"></path>
              </svg>
              <span>{state.isLoopEnabled ? t("disableLoop") : t("enableLoop")}</span>
            </ContextMenuItem>
          </ContextMenuContent>
        </ContextMenu>
      </PlayerProvider>
    </I18nProvider>
  );
};

/**
 * Main Player component.
 *
 * Note: PlayerProvider is available if you need to use context-based access
 * across multiple components. PlayerInner manages its own PlayerController
 * via usePlayerController hook.
 */
const Player: React.FC<PlayerProps> = (props) => {
  return <PlayerInner {...props} />;
};

export default Player;

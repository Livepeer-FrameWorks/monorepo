import React, { useState, useCallback, useEffect } from "react";
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
import { cn } from "@livepeer-frameworks/player-core";
import type { PlaybackMode, EndpointInfo } from "@livepeer-frameworks/player-core";
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
}) => {
  // ============================================================================
  // UI-only State (stays in wrapper)
  // ============================================================================
  const [isStatsOpen, setIsStatsOpen] = useState(false);
  const [isDevPanelOpen, setIsDevPanelOpen] = useState(false);
  const [skipDirection, setSkipDirection] = useState<SkipDirection>(null);

  // Playback mode preference (persistent)
  const [devPlaybackMode, setDevPlaybackMode] = useState<PlaybackMode>(
    options?.playbackMode || "auto"
  );

  // ============================================================================
  // PlayerController Hook - ALL business logic
  // ============================================================================
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
  } = usePlayerController({
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
      ? "Resolving viewing endpoint..."
      : "Waiting for endpoint..."
    : "Waiting for endpoint...";

  // ============================================================================
  // Render
  // ============================================================================
  return (
    <ContextMenu>
      <ContextMenuTrigger asChild>
        <div
          className={cn(
            "fw-player-surface fw-player-root w-full h-full overflow-hidden",
            options?.devMode && "flex"
          )}
          data-player-container="true"
          tabIndex={0}
          onMouseEnter={handleMouseEnter}
          onMouseLeave={handleMouseLeave}
          onMouseMove={handleMouseMove}
        >
          {/* Player area */}
          <div className={cn("relative", options?.devMode ? "flex-1 min-w-0" : "w-full h-full")}>
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
            {state.isHoldingSpeed && <SpeedIndicator isVisible={true} speed={state.holdSpeed} />}

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
                message={state.isEffectivelyLive ? state.streamState?.message : "Loading video..."}
                percentage={state.isEffectivelyLive ? state.streamState?.percentage : undefined}
              />
            )}

            {/* Buffering spinner */}
            {showBufferingSpinner && (
              <div
                role="status"
                aria-live="polite"
                className="fw-player-surface absolute inset-0 flex items-center justify-center bg-black/40 backdrop-blur-sm z-20"
              >
                <div className="flex items-center gap-3 rounded-lg border border-white/10 bg-black/70 px-4 py-3 text-sm text-white shadow-lg">
                  <div className="w-4 h-4 border-2 border-white/30 border-t-white rounded-full animate-spin"></div>
                  <span>Buffering...</span>
                </div>
              </div>
            )}

            {/* Error overlay */}
            {!state.shouldShowIdleScreen && state.error && (
              <div
                role="alert"
                aria-live="assertive"
                className={cn(
                  "fw-error-overlay",
                  state.isPassiveError
                    ? "fw-error-overlay--passive"
                    : "fw-error-overlay--fullscreen"
                )}
              >
                <div
                  className={cn(
                    "fw-error-popup",
                    state.isPassiveError ? "fw-error-popup--passive" : "fw-error-popup--fullscreen"
                  )}
                >
                  <div
                    className={cn(
                      "fw-error-header",
                      state.isPassiveError ? "fw-error-header--warning" : "fw-error-header--error"
                    )}
                  >
                    <span
                      className={cn(
                        "fw-error-title",
                        state.isPassiveError ? "fw-error-title--warning" : "fw-error-title--error"
                      )}
                    >
                      {state.isPassiveError ? "Warning" : "Error"}
                    </span>
                    <button
                      type="button"
                      className="fw-error-close"
                      onClick={clearError}
                      aria-label="Dismiss"
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
                    <p className="fw-error-message">Playback issue</p>
                  </div>
                  <div className="fw-error-actions">
                    <button
                      type="button"
                      className="fw-error-btn"
                      aria-label="Retry playback"
                      onClick={() => {
                        clearError();
                        retry();
                      }}
                    >
                      Retry
                    </button>
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
                    aria-label="Dismiss"
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

            {/* Player controls */}
            {!useStockControls && (
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
          <span>{isStatsOpen ? "Hide Stats" : "Stats"}</span>
        </ContextMenuItem>
        {options?.devMode && (
          <>
            <ContextMenuSeparator />
            <ContextMenuItem onSelect={() => setIsDevPanelOpen(!isDevPanelOpen)} className="gap-2">
              <SettingsIcon size={14} className="opacity-70 flex-shrink-0" />
              <span>{isDevPanelOpen ? "Hide Settings" : "Settings"}</span>
            </ContextMenuItem>
          </>
        )}
        <ContextMenuSeparator />
        <ContextMenuItem onSelect={togglePiP} className="gap-2">
          <PictureInPictureIcon size={14} className="opacity-70 flex-shrink-0" />
          <span>Picture-in-Picture</span>
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
          <span>{state.isLoopEnabled ? "Disable Loop" : "Enable Loop"}</span>
        </ContextMenuItem>
      </ContextMenuContent>
    </ContextMenu>
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

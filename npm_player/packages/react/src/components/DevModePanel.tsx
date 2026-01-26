import React, { useState, useCallback, useMemo, useEffect, useRef } from "react";
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
  "whep": "WHEP",
  "mist/html": "Mist",
  "mist/legacy": "Auto",
  "ws/video/mp4": "MEWS",
};

export interface DevModePanelProps {
  /** Callback when user selects a combo (one-shot selection) */
  onSettingsChange: (settings: {
    forcePlayer?: string;
    forceType?: string;
    forceSource?: number;
  }) => void;
  /** Current playback mode */
  playbackMode?: PlaybackMode;
  /** Callback when playback mode changes */
  onModeChange?: (mode: PlaybackMode) => void;
  /** Callback to force player reload */
  onReload?: () => void;
  /** Stream info for getting all combinations (sources + tracks from MistServer) */
  streamInfo?: StreamInfo | null;
  /** MistServer stream metadata including tracks */
  mistStreamInfo?: MistStreamInfo | null;
  /** Current player info */
  currentPlayer?: {
    name: string;
    shortname: string;
  } | null;
  /** Current source info */
  currentSource?: {
    url: string;
    type: string;
  } | null;
  /** Video element for stats */
  videoElement?: HTMLVideoElement | null;
  /** Protocol/node info */
  protocol?: string;
  nodeId?: string;
  /** Whether the panel toggle is visible (hover state) */
  isVisible?: boolean;
  /** Controlled open state (if provided, component is controlled) */
  isOpen?: boolean;
  /** Callback when open state changes */
  onOpenChange?: (isOpen: boolean) => void;
}

/**
 * DevModePanel - Advanced Settings overlay for testing player configurations
 * Similar to MistPlayer's skin: "dev" mode
 */
const DevModePanel: React.FC<DevModePanelProps> = ({
  onSettingsChange,
  playbackMode = 'auto',
  onModeChange,
  onReload,
  streamInfo,
  mistStreamInfo,
  currentPlayer,
  currentSource,
  videoElement,
  protocol,
  nodeId,
  isVisible: _isVisible = true,
  isOpen: controlledIsOpen,
  onOpenChange,
}) => {
  // Support both controlled and uncontrolled modes
  const [internalIsOpen, setInternalIsOpen] = useState(false);
  const isOpen = controlledIsOpen !== undefined ? controlledIsOpen : internalIsOpen;
  const setIsOpen = useCallback((value: boolean) => {
    if (onOpenChange) {
      onOpenChange(value);
    } else {
      setInternalIsOpen(value);
    }
  }, [onOpenChange]);
  const [activeTab, setActiveTab] = useState<"config" | "stats">("config");
  const [, setCurrentComboIndex] = useState(0);
  const [hoveredComboIndex, setHoveredComboIndex] = useState<number | null>(null);
  const [tooltipAbove, setTooltipAbove] = useState(false);
  const [showDisabledPlayers, setShowDisabledPlayers] = useState(false);
  const comboListRef = useRef<HTMLDivElement>(null);

  // Quality monitoring for playback score
  const qualityMonitorRef = useRef<QualityMonitor | null>(null);
  const [playbackScore, setPlaybackScore] = useState<number>(1.0);
  const [qualityScore, setQualityScore] = useState<number>(100);
  const [stallCount, setStallCount] = useState<number>(0);
  const [frameDropRate, setFrameDropRate] = useState<number>(0);

  // Player-specific stats (from getStats())
  const [playerStats, setPlayerStats] = useState<any>(null);
  const statsIntervalRef = useRef<ReturnType<typeof setInterval> | null>(null);

  // Start/stop quality monitoring based on video element
  useEffect(() => {
    if (videoElement && isOpen) {
      if (!qualityMonitorRef.current) {
        qualityMonitorRef.current = new QualityMonitor({
          sampleInterval: 500,
          onSample: (quality) => {
            setQualityScore(quality.score);
            setStallCount(quality.stallCount);
            setFrameDropRate(quality.frameDropRate);
            // Get playback score from monitor
            if (qualityMonitorRef.current) {
              setPlaybackScore(qualityMonitorRef.current.getPlaybackScore());
            }
          },
        });
      }
      qualityMonitorRef.current.start(videoElement);
    }

    return () => {
      if (qualityMonitorRef.current) {
        qualityMonitorRef.current.stop();
      }
    };
  }, [videoElement, isOpen]);

  // Poll player-specific stats when stats tab is open
  useEffect(() => {
    if (isOpen && activeTab === "stats") {
      const pollStats = async () => {
        try {
          const player = globalPlayerManager.getCurrentPlayer();
          if (player && player.getStats) {
            const stats = await player.getStats();
            if (stats) {
              setPlayerStats(stats);
            }
          }
        } catch {
          // Ignore errors
        }
      };

      // Poll immediately and then every 500ms
      pollStats();
      statsIntervalRef.current = setInterval(pollStats, 500);

      return () => {
        if (statsIntervalRef.current) {
          clearInterval(statsIntervalRef.current);
          statsIntervalRef.current = null;
        }
      };
    }
  }, [isOpen, activeTab]);

  // Get all player-source combinations with scores (including incompatible)
  // Uses cached results from PlayerManager - won't recompute if data unchanged
  const allCombinations = useMemo(() => {
    if (!streamInfo) return [];
    try {
      // getAllCombinations now includes all combos (compatible + incompatible)
      // and uses content-based caching - won't spam on every render
      return globalPlayerManager.getAllCombinations(streamInfo, playbackMode);
    } catch {
      return [];
    }
  }, [streamInfo, playbackMode]);

  // For backward compatibility (Next Option only cycles compatible)
  const combinations = useMemo(() => {
    return allCombinations.filter(c => c.compatible);
  }, [allCombinations]);

  // Find current active combo index based on current player/source (in allCombinations)
  const activeComboIndex = useMemo(() => {
    if (!currentPlayer || !currentSource || allCombinations.length === 0) return -1;
    return allCombinations.findIndex(
      (c) => c.player === currentPlayer.shortname && c.sourceType === currentSource.type
    );
  }, [currentPlayer, currentSource, allCombinations]);

  // Find index in compatible-only list for Next Option cycling
  const activeCompatibleIndex = useMemo(() => {
    if (!currentPlayer || !currentSource || combinations.length === 0) return -1;
    return combinations.findIndex(
      (c) => c.player === currentPlayer.shortname && c.sourceType === currentSource.type
    );
  }, [currentPlayer, currentSource, combinations]);

  const handleReload = useCallback(() => {
    // Just trigger reload - controller manages the state
    onReload?.();
  }, [onReload]);

  const handleNextCombo = useCallback(() => {
    if (combinations.length === 0) return;

    // Start from active combo or 0, then move to next (only cycles compatible)
    const startIdx = activeCompatibleIndex >= 0 ? activeCompatibleIndex : -1;
    const nextIdx = (startIdx + 1) % combinations.length;
    const combo = combinations[nextIdx];

    setCurrentComboIndex(nextIdx);
    onSettingsChange({
      forcePlayer: combo.player,
      forceType: combo.sourceType,
      forceSource: combo.sourceIndex,
    });
  }, [combinations, activeCompatibleIndex, onSettingsChange]);

  const handleSelectCombo = useCallback((index: number) => {
    const combo = allCombinations[index];
    if (!combo) return;

    // Allow selecting even incompatible combos in dev mode (for testing)
    setCurrentComboIndex(index);
    onSettingsChange({
      forcePlayer: combo.player,
      forceType: combo.sourceType,
      forceSource: combo.sourceIndex,
    });
  }, [allCombinations, onSettingsChange]);

  // Video stats - poll periodically when stats tab is open
  const [stats, setStats] = useState<{
    resolution: string;
    buffered: string;
    playbackRate: string;
    currentTime: string;
    duration: string;
    readyState: number;
    networkState: number;
  } | null>(null);

  useEffect(() => {
    if (!isOpen || activeTab !== "stats") return;

    const updateStats = () => {
      // Get fresh video element from player manager
      const player = globalPlayerManager.getCurrentPlayer();
      const v = player?.getVideoElement() || videoElement;
      if (!v) {
        setStats(null);
        return;
      }
      setStats({
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
      });
    };

    updateStats();
    const interval = setInterval(updateStats, 500);
    return () => clearInterval(interval);
  }, [isOpen, activeTab, videoElement]);

  // Panel is only rendered when open (no floating toggle button)
  if (!isOpen) {
    return null;
  }

  return (
    <div className="fw-dev-panel">
      {/* Header with tabs */}
      <div className="fw-dev-header">
        <button
          type="button"
          onClick={() => setActiveTab("config")}
          className={cn("fw-dev-tab", activeTab === "config" && "fw-dev-tab--active")}
        >
          Config
        </button>
        <button
          type="button"
          onClick={() => setActiveTab("stats")}
          className={cn("fw-dev-tab", activeTab === "stats" && "fw-dev-tab--active")}
        >
          Stats
        </button>
        <div className="fw-dev-spacer" />
        <button
          type="button"
          onClick={() => setIsOpen(false)}
          className="fw-dev-close"
          aria-label="Close dev mode panel"
        >
          <svg
            width="12"
            height="12"
            viewBox="0 0 12 12"
            fill="none"
            stroke="currentColor"
            strokeWidth="1.5"
          >
            <path d="M2 2l8 8M10 2l-8 8" />
          </svg>
        </button>
      </div>

      {/* Config Tab */}
      {activeTab === "config" && (
        <div ref={comboListRef} className="fw-dev-body">
          {/* Current State */}
          <div className="fw-dev-section">
            <div className="fw-dev-label">Active</div>
            <div className="fw-dev-value">
              {currentPlayer?.name || "None"}{" "}
              <span className="fw-dev-value-arrow">→</span>{" "}
              {SOURCE_TYPE_LABELS[currentSource?.type || ""] || currentSource?.type || "—"}
            </div>
            {nodeId && (
              <div className="fw-dev-value-muted">Node: {nodeId}</div>
            )}
          </div>

          {/* Playback Mode Selector */}
          <div className="fw-dev-section">
            <div className="fw-dev-label">Playback Mode</div>
            <div className="fw-dev-mode-group">
              {(['auto', 'low-latency', 'quality'] as const).map((mode) => (
                <button
                  key={mode}
                  type="button"
                  onClick={() => onModeChange?.(mode)}
                  className={cn(
                    "fw-dev-mode-btn",
                    playbackMode === mode && "fw-dev-mode-btn--active"
                  )}
                >
                  {mode === 'low-latency' ? 'Low Lat' : mode.charAt(0).toUpperCase() + mode.slice(1)}
                </button>
              ))}
            </div>
            <div className="fw-dev-mode-desc">
              {playbackMode === 'auto' && 'Balanced: MP4/WS → WHEP → HLS'}
              {playbackMode === 'low-latency' && 'WHEP/WebRTC first (<1s delay)'}
              {playbackMode === 'quality' && 'MP4/WS first, HLS fallback'}
            </div>
          </div>

          {/* Action buttons */}
          <div className="fw-dev-actions">
            <button
              type="button"
              onClick={handleReload}
              className="fw-dev-action-btn"
            >
              Reload
            </button>
            <button
              type="button"
              onClick={handleNextCombo}
              className="fw-dev-action-btn"
            >
              Next Option
            </button>
          </div>

          {/* Combo list */}
          <div className="fw-dev-section" style={{ padding: 0, borderBottom: 0 }}>
            <div className="fw-dev-list-header">
              <span className="fw-dev-list-title">
                Player Options ({combinations.length})
              </span>
              {allCombinations.length > combinations.length && (
                <button
                  type="button"
                  onClick={() => setShowDisabledPlayers(!showDisabledPlayers)}
                  className="fw-dev-list-toggle"
                >
                  <svg
                    width="10"
                    height="10"
                    viewBox="0 0 24 24"
                    fill="none"
                    stroke="currentColor"
                    strokeWidth="2"
                    className={cn("fw-dev-chevron", showDisabledPlayers && "fw-dev-chevron--open")}
                  >
                    <path d="M6 9l6 6 6-6" />
                  </svg>
                  {showDisabledPlayers ? "Hide" : "Show"} disabled ({allCombinations.length - combinations.length})
                </button>
              )}
            </div>
            {allCombinations.length === 0 ? (
              <div className="fw-dev-list-empty">
                No stream info available
              </div>
            ) : (
              <div>
                {allCombinations.map((combo, index) => {
                  // Codec-incompatible items always show (with warning), MIME-incompatible hide in "disabled"
                  const isCodecIncompat = (combo as any).codecIncompatible === true;
                  if (!combo.compatible && !isCodecIncompat && !showDisabledPlayers) return null;

                  const isActive = activeComboIndex === index;
                  const typeLabel = SOURCE_TYPE_LABELS[combo.sourceType] || combo.sourceType.split("/").pop();

                  // Determine score class
                  const getScoreClass = () => {
                    if (!combo.compatible && !isCodecIncompat) return "fw-dev-combo-score--disabled";
                    if (isCodecIncompat) return "fw-dev-combo-score--low";
                    if (combo.score >= 2) return "fw-dev-combo-score--high";
                    if (combo.score >= 1.5) return "fw-dev-combo-score--mid";
                    return "fw-dev-combo-score--low";
                  };

                  // Determine rank class
                  const getRankClass = () => {
                    if (isActive) return "fw-dev-combo-rank--active";
                    if (!combo.compatible && !isCodecIncompat) return "fw-dev-combo-rank--disabled";
                    if (isCodecIncompat) return "fw-dev-combo-rank--warn";
                    return "";
                  };

                  // Determine type class
                  const getTypeClass = () => {
                    if (!combo.compatible && !isCodecIncompat) return "fw-dev-combo-type--disabled";
                    if (isCodecIncompat) return "fw-dev-combo-type--warn";
                    return "";
                  };

                  return (
                    <div
                      key={`${combo.player}-${combo.sourceType}`}
                      onMouseEnter={(e) => {
                        setHoveredComboIndex(index);
                        if (comboListRef.current) {
                          const container = comboListRef.current;
                          const row = e.currentTarget;
                          const containerRect = container.getBoundingClientRect();
                          const rowRect = row.getBoundingClientRect();
                          const relativePosition = (rowRect.top - containerRect.top) / containerRect.height;
                          setTooltipAbove(relativePosition > 0.6);
                        }
                      }}
                      onMouseLeave={() => setHoveredComboIndex(null)}
                      className="fw-dev-combo"
                    >
                      <button
                        type="button"
                        onClick={() => handleSelectCombo(index)}
                        className={cn(
                          "fw-dev-combo-btn",
                          isActive && "fw-dev-combo-btn--active",
                          !combo.compatible && !isCodecIncompat && "fw-dev-combo-btn--disabled",
                          isCodecIncompat && "fw-dev-combo-btn--codec-warn"
                        )}
                      >
                        {/* Rank */}
                        <span className={cn("fw-dev-combo-rank", getRankClass())}>
                          {combo.compatible ? index + 1 : isCodecIncompat ? "⚠" : "—"}
                        </span>
                        {/* Player + Protocol */}
                        <span className="fw-dev-combo-name">
                          {combo.playerName}{" "}
                          <span className="fw-dev-combo-arrow">→</span>{" "}
                          <span className={cn("fw-dev-combo-type", getTypeClass())}>{typeLabel}</span>
                        </span>
                        {/* Score */}
                        <span className={cn("fw-dev-combo-score", getScoreClass())}>
                          {combo.score.toFixed(2)}
                        </span>
                      </button>

                      {/* Score breakdown tooltip */}
                      {hoveredComboIndex === index && (
                        <div className={cn(
                          "fw-dev-tooltip",
                          tooltipAbove ? "fw-dev-tooltip--above" : "fw-dev-tooltip--below"
                        )}>
                          {/* Full player/source info */}
                          <div className="fw-dev-tooltip-header">
                            <div className="fw-dev-tooltip-title">{combo.playerName}</div>
                            <div className="fw-dev-tooltip-subtitle">{combo.sourceType}</div>
                            {combo.scoreBreakdown?.trackTypes && combo.scoreBreakdown.trackTypes.length > 0 && (
                              <div className="fw-dev-tooltip-tracks">
                                Tracks: <span className="fw-dev-tooltip-value">{combo.scoreBreakdown.trackTypes.join(', ')}</span>
                              </div>
                            )}
                          </div>
                          {combo.compatible && combo.scoreBreakdown ? (
                            <>
                              <div className="fw-dev-tooltip-score">Score: {combo.score.toFixed(2)}</div>
                              <div className="fw-dev-tooltip-row">
                                Tracks [{combo.scoreBreakdown.trackTypes.join(', ')}]: <span className="fw-dev-tooltip-value">{combo.scoreBreakdown.trackScore.toFixed(2)}</span> <span className="fw-dev-tooltip-weight">x{combo.scoreBreakdown.weights.tracks}</span>
                              </div>
                              <div className="fw-dev-tooltip-row">
                                Priority: <span className="fw-dev-tooltip-value">{combo.scoreBreakdown.priorityScore.toFixed(2)}</span> <span className="fw-dev-tooltip-weight">x{combo.scoreBreakdown.weights.priority}</span>
                              </div>
                              <div className="fw-dev-tooltip-row">
                                Source: <span className="fw-dev-tooltip-value">{combo.scoreBreakdown.sourceScore.toFixed(2)}</span> <span className="fw-dev-tooltip-weight">x{combo.scoreBreakdown.weights.source}</span>
                              </div>
                              {combo.scoreBreakdown.reliabilityScore !== undefined && (
                                <div className="fw-dev-tooltip-row">
                                  Reliability: <span className="fw-dev-tooltip-value">{combo.scoreBreakdown.reliabilityScore.toFixed(2)}</span> <span className="fw-dev-tooltip-weight">x{combo.scoreBreakdown.weights.reliability ?? 0}</span>
                                </div>
                              )}
                              {combo.scoreBreakdown.modeBonus !== undefined && combo.scoreBreakdown.modeBonus !== 0 && (
                                <div className="fw-dev-tooltip-row">
                                  Mode ({playbackMode}): <span className="fw-dev-tooltip-bonus">+{combo.scoreBreakdown.modeBonus.toFixed(2)}</span> <span className="fw-dev-tooltip-weight">x{combo.scoreBreakdown.weights.mode ?? 0}</span>
                                </div>
                              )}
                              {combo.scoreBreakdown.routingBonus !== undefined && combo.scoreBreakdown.routingBonus !== 0 && (
                                <div className="fw-dev-tooltip-row">
                                  Routing: <span className={combo.scoreBreakdown.routingBonus > 0 ? "fw-dev-tooltip-bonus" : "fw-dev-tooltip-penalty"}>{combo.scoreBreakdown.routingBonus > 0 ? '+' : ''}{combo.scoreBreakdown.routingBonus.toFixed(2)}</span> <span className="fw-dev-tooltip-weight">x{combo.scoreBreakdown.weights.routing ?? 0}</span>
                                </div>
                              )}
                            </>
                          ) : (
                            <div className="fw-dev-tooltip-error">{combo.incompatibleReason || 'Incompatible'}</div>
                          )}
                        </div>
                      )}
                    </div>
                  );
                })}
              </div>
            )}
          </div>
        </div>
      )}

      {/* Stats Tab */}
      {activeTab === "stats" && (
        <div className="fw-dev-body">
          {/* Playback Rate */}
          <div className="fw-dev-section">
            <div className="fw-dev-label">Playback Rate</div>
            <div className="fw-dev-rate">
              <div className={cn(
                "fw-dev-rate-value",
                playbackScore >= 0.95 && playbackScore <= 1.05 ? "fw-dev-stat-value--good" :
                playbackScore > 1.05 ? "fw-dev-stat-value--accent" :
                playbackScore >= 0.75 ? "fw-dev-stat-value--warn" :
                "fw-dev-stat-value--bad"
              )}>
                {playbackScore.toFixed(2)}×
              </div>
              <div className="fw-dev-rate-status">
                {playbackScore >= 0.95 && playbackScore <= 1.05 ? "realtime" :
                 playbackScore > 1.05 ? "catching up" :
                 playbackScore >= 0.75 ? "slightly slow" :
                 "stalling"}
              </div>
            </div>
            <div className="fw-dev-rate-stats">
              <span className={qualityScore >= 75 ? "fw-dev-stat-value--good" : "fw-dev-stat-value--bad"}>
                Quality: {qualityScore}/100
              </span>
              <span className={stallCount === 0 ? "fw-dev-stat-value--good" : "fw-dev-stat-value--warn"}>
                Stalls: {stallCount}
              </span>
              <span className={frameDropRate < 1 ? "fw-dev-stat-value--good" : "fw-dev-stat-value--bad"}>
                Drops: {frameDropRate.toFixed(1)}%
              </span>
            </div>
          </div>

          {/* Video Stats */}
          {stats ? (
            <div>
              <div className="fw-dev-stat">
                <span className="fw-dev-stat-label">Resolution</span>
                <span className="fw-dev-stat-value">{stats.resolution}</span>
              </div>
              <div className="fw-dev-stat">
                <span className="fw-dev-stat-label">Buffer</span>
                <span className="fw-dev-stat-value">{stats.buffered}s</span>
              </div>
              <div className="fw-dev-stat">
                <span className="fw-dev-stat-label">Playback Rate</span>
                <span className="fw-dev-stat-value">{stats.playbackRate}x</span>
              </div>
              <div className="fw-dev-stat">
                <span className="fw-dev-stat-label">Time</span>
                <span className="fw-dev-stat-value">
                  {stats.currentTime} / {stats.duration}
                </span>
              </div>
              <div className="fw-dev-stat">
                <span className="fw-dev-stat-label">Ready State</span>
                <span className="fw-dev-stat-value">{stats.readyState}</span>
              </div>
              <div className="fw-dev-stat">
                <span className="fw-dev-stat-label">Network State</span>
                <span className="fw-dev-stat-value">{stats.networkState}</span>
              </div>
              {protocol && (
                <div className="fw-dev-stat">
                  <span className="fw-dev-stat-label">Protocol</span>
                  <span className="fw-dev-stat-value">{protocol}</span>
                </div>
              )}
              {nodeId && (
                <div className="fw-dev-stat">
                  <span className="fw-dev-stat-label">Node ID</span>
                  <span className="fw-dev-stat-value truncate" style={{ maxWidth: '150px' }}>
                    {nodeId}
                  </span>
                </div>
              )}
            </div>
          ) : (
            <div className="fw-dev-list-empty">
              No video element available
            </div>
          )}

          {/* Player-specific Stats (HLS.js / WebRTC) */}
          {playerStats && (
            <div>
              <div className="fw-dev-list-header fw-dev-section-header">
                <span className="fw-dev-list-title">
                  {playerStats.type === 'hls' ? 'HLS.js Stats' :
                   playerStats.type === 'webrtc' ? 'WebRTC Stats' : 'Player Stats'}
                </span>
              </div>

              {/* HLS-specific stats */}
              {playerStats.type === 'hls' && (
                <>
                  <div className="fw-dev-stat">
                    <span className="fw-dev-stat-label">Bitrate</span>
                    <span className="fw-dev-stat-value--accent">
                      {playerStats.currentBitrate > 0
                        ? `${Math.round(playerStats.currentBitrate / 1000)} kbps`
                        : 'N/A'}
                    </span>
                  </div>
                  <div className="fw-dev-stat">
                    <span className="fw-dev-stat-label">Bandwidth Est.</span>
                    <span className="fw-dev-stat-value">
                      {playerStats.bandwidthEstimate > 0
                        ? `${Math.round(playerStats.bandwidthEstimate / 1000)} kbps`
                        : 'N/A'}
                    </span>
                  </div>
                  <div className="fw-dev-stat">
                    <span className="fw-dev-stat-label">Level</span>
                    <span className="fw-dev-stat-value">
                      {playerStats.currentLevel >= 0 ? playerStats.currentLevel : 'Auto'} / {playerStats.levels?.length || 0}
                    </span>
                  </div>
                  {playerStats.latency !== undefined && (
                    <div className="fw-dev-stat">
                      <span className="fw-dev-stat-label">Latency</span>
                      <span className={playerStats.latency > 5000 ? "fw-dev-stat-value--warn" : "fw-dev-stat-value"}>
                        {Math.round(playerStats.latency)} ms
                      </span>
                    </div>
                  )}
                </>
              )}

              {/* WebRTC-specific stats */}
              {playerStats.type === 'webrtc' && (
                <>
                  {playerStats.video && (
                    <>
                      <div className="fw-dev-stat">
                        <span className="fw-dev-stat-label">Video Bitrate</span>
                        <span className="fw-dev-stat-value--accent">
                          {playerStats.video.bitrate > 0
                            ? `${Math.round(playerStats.video.bitrate / 1000)} kbps`
                            : 'N/A'}
                        </span>
                      </div>
                      <div className="fw-dev-stat">
                        <span className="fw-dev-stat-label">FPS</span>
                        <span className="fw-dev-stat-value">
                          {Math.round(playerStats.video.framesPerSecond || 0)}
                        </span>
                      </div>
                      <div className="fw-dev-stat">
                        <span className="fw-dev-stat-label">Frames</span>
                        <span className="fw-dev-stat-value">
                          {playerStats.video.framesDecoded} decoded,{' '}
                          <span className={playerStats.video.frameDropRate > 1 ? "fw-dev-stat-value--bad" : "fw-dev-stat-value--good"}>
                            {playerStats.video.framesDropped} dropped
                          </span>
                        </span>
                      </div>
                      <div className="fw-dev-stat">
                        <span className="fw-dev-stat-label">Packet Loss</span>
                        <span className={playerStats.video.packetLossRate > 1 ? "fw-dev-stat-value--bad" : "fw-dev-stat-value--good"}>
                          {playerStats.video.packetLossRate.toFixed(2)}%
                        </span>
                      </div>
                      <div className="fw-dev-stat">
                        <span className="fw-dev-stat-label">Jitter</span>
                        <span className={playerStats.video.jitter > 30 ? "fw-dev-stat-value--warn" : "fw-dev-stat-value"}>
                          {playerStats.video.jitter.toFixed(1)} ms
                        </span>
                      </div>
                      <div className="fw-dev-stat">
                        <span className="fw-dev-stat-label">Jitter Buffer</span>
                        <span className="fw-dev-stat-value">
                          {playerStats.video.jitterBufferDelay.toFixed(1)} ms
                        </span>
                      </div>
                    </>
                  )}
                  {playerStats.network && (
                    <div className="fw-dev-stat">
                      <span className="fw-dev-stat-label">RTT</span>
                      <span className={playerStats.network.rtt > 200 ? "fw-dev-stat-value--warn" : "fw-dev-stat-value"}>
                        {Math.round(playerStats.network.rtt)} ms
                      </span>
                    </div>
                  )}
                </>
              )}
            </div>
          )}

          {/* MistServer Track Info */}
          {mistStreamInfo?.meta?.tracks && Object.keys(mistStreamInfo.meta.tracks).length > 0 && (
            <div>
              <div className="fw-dev-list-header fw-dev-section-header">
                <span className="fw-dev-list-title">
                  Tracks ({Object.keys(mistStreamInfo.meta.tracks).length})
                </span>
              </div>
              {Object.entries(mistStreamInfo.meta.tracks).map(([id, track]) => (
                <div key={id} className="fw-dev-track">
                  <div className="fw-dev-track-header">
                    <span className={cn(
                      "fw-dev-track-badge",
                      track.type === 'video' ? "fw-dev-track-badge--video" :
                      track.type === 'audio' ? "fw-dev-track-badge--audio" :
                      "fw-dev-track-badge--other"
                    )}>
                      {track.type}
                    </span>
                    <span className="fw-dev-track-codec">{track.codec}</span>
                    <span className="fw-dev-track-id">#{id}</span>
                  </div>
                  <div className="fw-dev-track-meta">
                    {track.type === 'video' && track.width && track.height && (
                      <span>{track.width}×{track.height}</span>
                    )}
                    {track.bps && (
                      <span>{Math.round(track.bps / 1000)} kbps</span>
                    )}
                    {track.fpks && (
                      <span>{Math.round(track.fpks / 1000)} fps</span>
                    )}
                    {track.type === 'audio' && track.channels && (
                      <span>{track.channels}ch</span>
                    )}
                    {track.type === 'audio' && track.rate && (
                      <span>{track.rate} Hz</span>
                    )}
                    {track.lang && (
                      <span>{track.lang}</span>
                    )}
                  </div>
                </div>
              ))}
            </div>
          )}

          {/* Debug: Show if mistStreamInfo is missing tracks */}
          {mistStreamInfo && (!mistStreamInfo.meta?.tracks || Object.keys(mistStreamInfo.meta.tracks).length === 0) && (
            <div className="fw-dev-no-tracks">
              <span className="fw-dev-no-tracks-text">
                No track data available
                {mistStreamInfo.type && <span className="fw-dev-no-tracks-type">({mistStreamInfo.type})</span>}
              </span>
            </div>
          )}
        </div>
      )}
    </div>
  );
};

export default DevModePanel;

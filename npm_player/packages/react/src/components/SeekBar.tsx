import React, { useRef, useState, useCallback, useMemo, useEffect } from "react";
import { cn } from "@livepeer-frameworks/player-core";
import { useTranslate } from "../context/i18n";

interface SeekBarProps {
  /** Current playback time in milliseconds */
  currentTime: number;
  /** Total duration in milliseconds */
  duration: number;
  /** Buffered time ranges from video element */
  buffered?: TimeRanges;
  /** Whether seeking is allowed */
  disabled?: boolean;
  /** Called when user seeks to a new time */
  onSeek?: (time: number) => void;
  /** Additional class names */
  className?: string;
  /** Whether this is a live stream */
  isLive?: boolean;
  /** For live: start of seekable DVR window (ms) */
  seekableStart?: number;
  /** For live: current live edge position (ms) */
  liveEdge?: number;
  /** Defer seeking until drag release */
  commitOnRelease?: boolean;
  /** Whether video is currently playing (enables RAF interpolation for smooth progress) */
  isPlaying?: boolean;
}

/**
 * Industry-standard video seek bar with:
 * - Track background
 * - Buffer progress indicator
 * - Playback progress indicator
 * - Thumb on hover
 * - Time tooltip on hover (relative for live: "-5:30")
 */
const SeekBar: React.FC<SeekBarProps> = ({
  currentTime,
  duration,
  buffered,
  disabled = false,
  onSeek,
  className,
  isLive = false,
  seekableStart = 0,
  liveEdge,
  commitOnRelease = false,
  isPlaying = false,
}) => {
  const t = useTranslate();
  const trackRef = useRef<HTMLDivElement>(null);
  const [isHovering, setIsHovering] = useState(false);
  const [isDragging, setIsDragging] = useState(false);
  const [dragTime, setDragTime] = useState<number | null>(null);
  const dragTimeRef = useRef<number | null>(null);
  const [hoverPosition, setHoverPosition] = useState(0);
  const [hoverTime, setHoverTime] = useState(0);

  // RAF smooth progress: interpolate between timeupdate events at ~60fps
  const progressRef = useRef<HTMLDivElement>(null);
  const baseRef = useRef({ time: 0, stamp: 0 });
  const rafIdRef = useRef(0);

  // Effective live edge (use provided or fall back to duration)
  const effectiveLiveEdge = liveEdge ?? duration;

  // Seekable window size
  const seekableWindow = effectiveLiveEdge - seekableStart;

  // Calculate progress percentage
  // For live streams: position within the DVR window
  // For VOD: position within total duration
  const displayTime = dragTime ?? currentTime;
  const progressPercent = useMemo(() => {
    if (isLive && seekableWindow > 0) {
      const positionInWindow = displayTime - seekableStart;
      return Math.min(100, Math.max(0, (positionInWindow / seekableWindow) * 100));
    }
    if (!Number.isFinite(duration) || duration <= 0) return 0;
    return Math.min(100, Math.max(0, (displayTime / duration) * 100));
  }, [displayTime, duration, isLive, seekableStart, seekableWindow]);

  // Reset interpolation baseline when currentTime prop updates (from timeupdate events)
  useEffect(() => {
    baseRef.current = { time: currentTime, stamp: performance.now() };
  }, [currentTime]);

  // RAF loop: during playback, interpolate progress at ~60fps for smooth bar motion.
  // Bypasses React rendering — updates the DOM element directly via ref.
  const rafActiveRef = useRef(false);
  useEffect(() => {
    const shouldAnimate = isPlaying && !isDragging && !disabled;
    rafActiveRef.current = shouldAnimate;

    if (!shouldAnimate) {
      cancelAnimationFrame(rafIdRef.current);
      // Sync DOM to React-computed value when RAF stops
      if (progressRef.current) {
        progressRef.current.style.width = `${progressPercent}%`;
      }
      return;
    }

    const rangeStart = isLive ? seekableStart : 0;
    const rangeSize = isLive ? seekableWindow : duration;

    const animate = () => {
      if (!rafActiveRef.current) return;
      const base = baseRef.current;
      const interpolated = base.time + (performance.now() - base.stamp);
      const relative = interpolated - rangeStart;
      const pct =
        Number.isFinite(rangeSize) && rangeSize > 0
          ? Math.min(100, Math.max(0, (relative / rangeSize) * 100))
          : 0;

      if (progressRef.current) {
        progressRef.current.style.width = `${pct}%`;
      }
      rafIdRef.current = requestAnimationFrame(animate);
    };

    rafIdRef.current = requestAnimationFrame(animate);
    return () => {
      cancelAnimationFrame(rafIdRef.current);
    };
  }, [
    isPlaying,
    isDragging,
    disabled,
    isLive,
    seekableStart,
    seekableWindow,
    duration,
    progressPercent,
  ]);

  // Calculate buffered segments as array of {start%, end%} for accurate display
  const bufferedSegments = useMemo(() => {
    if (!buffered || buffered.length === 0) return [];

    const rangeEnd = isLive ? effectiveLiveEdge : duration;
    const rangeStart = isLive ? seekableStart : 0;
    const rangeSize = rangeEnd - rangeStart;

    if (!Number.isFinite(rangeSize) || rangeSize <= 0) return [];

    const segments: Array<{ startPercent: number; endPercent: number }> = [];
    for (let i = 0; i < buffered.length; i++) {
      // buffered TimeRanges are in seconds (browser API), convert to ms
      const start = buffered.start(i) * 1000;
      const end = buffered.end(i) * 1000;

      // Calculate position relative to the seekable range
      const relativeStart = start - rangeStart;
      const relativeEnd = end - rangeStart;

      segments.push({
        startPercent: Math.min(100, Math.max(0, (relativeStart / rangeSize) * 100)),
        endPercent: Math.min(100, Math.max(0, (relativeEnd / rangeSize) * 100)),
      });
    }
    return segments;
  }, [buffered, duration, isLive, seekableStart, effectiveLiveEdge]);

  // Format time as MM:SS or HH:MM:SS (for VOD)
  const formatTime = useCallback((ms: number): string => {
    if (!Number.isFinite(ms) || ms < 0) return "0:00";
    const total = Math.floor(ms / 1000);
    const hours = Math.floor(total / 3600);
    const minutes = Math.floor((total % 3600) / 60);
    const secs = total % 60;
    if (hours > 0) {
      return `${hours}:${String(minutes).padStart(2, "0")}:${String(secs).padStart(2, "0")}`;
    }
    return `${minutes}:${String(secs).padStart(2, "0")}`;
  }, []);

  // Format relative time for live streams (e.g., "-5:30" = 5:30 behind live edge)
  const formatLiveTime = useCallback((ms: number, edgeMs: number): string => {
    const behindMs = edgeMs - ms;
    if (behindMs < 1000) return t("live").toUpperCase();
    const total = Math.floor(behindMs / 1000);
    const hours = Math.floor(total / 3600);
    const minutes = Math.floor((total % 3600) / 60);
    const secs = total % 60;
    if (hours > 0) {
      return `-${hours}:${String(minutes).padStart(2, "0")}:${String(secs).padStart(2, "0")}`;
    }
    return `-${minutes}:${String(secs).padStart(2, "0")}`;
  }, []);

  // Calculate time from mouse position
  // For live: maps position to time within DVR window
  const getTimeFromPosition = useCallback(
    (clientX: number): number => {
      if (!trackRef.current) return 0;
      const rect = trackRef.current.getBoundingClientRect();
      const x = clientX - rect.left;
      const percent = Math.min(1, Math.max(0, x / rect.width));

      // Live with valid seekable window
      if (isLive && Number.isFinite(seekableWindow) && seekableWindow > 0) {
        return seekableStart + percent * seekableWindow;
      }

      // VOD with finite duration
      if (Number.isFinite(duration) && duration > 0) {
        return percent * duration;
      }

      // Fallback: If we have liveEdge, use it even if not marked as live
      // This handles cases where duration is Infinity but we have valid seekable data
      if (liveEdge !== undefined && Number.isFinite(liveEdge) && liveEdge > 0) {
        const start = Number.isFinite(seekableStart) ? seekableStart : 0;
        const window = liveEdge - start;
        if (window > 0) {
          return start + percent * window;
        }
      }

      // Last resort: use currentTime as a baseline
      return percent * (currentTime || 1);
    },
    [duration, isLive, seekableStart, seekableWindow, liveEdge, currentTime]
  );

  // Handle mouse move for hover preview
  const handleMouseMove = useCallback(
    (e: React.MouseEvent) => {
      if (!trackRef.current || disabled) return;
      const rect = trackRef.current.getBoundingClientRect();
      const x = e.clientX - rect.left;
      const percent = Math.min(1, Math.max(0, x / rect.width));
      setHoverPosition(percent * 100);
      setHoverTime(getTimeFromPosition(e.clientX));
    },
    [disabled, getTimeFromPosition]
  );

  // Handle drag start
  const handleMouseDown = useCallback(
    (e: React.MouseEvent) => {
      if (disabled) return;
      if (!isLive && !Number.isFinite(duration)) return;
      e.preventDefault();
      setIsDragging(true);

      const handleDragMove = (moveEvent: MouseEvent) => {
        const time = getTimeFromPosition(moveEvent.clientX);
        if (commitOnRelease) {
          setDragTime(time);
          dragTimeRef.current = time;
        } else {
          onSeek?.(time);
        }
      };

      const handleDragEnd = () => {
        setIsDragging(false);
        document.removeEventListener("mousemove", handleDragMove);
        document.removeEventListener("mouseup", handleDragEnd);
        const pending = dragTimeRef.current;
        if (commitOnRelease && pending !== null) {
          onSeek?.(pending);
          setDragTime(null);
          dragTimeRef.current = null;
        }
      };

      document.addEventListener("mousemove", handleDragMove);
      document.addEventListener("mouseup", handleDragEnd);

      // Initial seek
      const time = getTimeFromPosition(e.clientX);
      if (commitOnRelease) {
        setDragTime(time);
        dragTimeRef.current = time;
      } else {
        onSeek?.(time);
      }
    },
    [disabled, duration, isLive, getTimeFromPosition, onSeek, commitOnRelease]
  );

  const handleKeyDown = useCallback(
    (e: React.KeyboardEvent) => {
      if (disabled || !onSeek) return;
      const step = e.shiftKey ? 10000 : 5000;
      const rangeEnd = isLive ? effectiveLiveEdge : duration;
      const rangeStart = isLive ? seekableStart : 0;

      switch (e.key) {
        case "ArrowLeft":
        case "ArrowDown":
          e.preventDefault();
          onSeek(Math.max(rangeStart, currentTime - step));
          break;
        case "ArrowRight":
        case "ArrowUp":
          e.preventDefault();
          onSeek(Math.min(rangeEnd, currentTime + step));
          break;
        case "Home":
          e.preventDefault();
          onSeek(rangeStart);
          break;
        case "End":
          e.preventDefault();
          onSeek(rangeEnd);
          break;
      }
    },
    [disabled, onSeek, currentTime, duration, isLive, seekableStart, effectiveLiveEdge]
  );

  const showThumb = isHovering || isDragging;
  const canShowTooltip = isLive ? seekableWindow > 0 : Number.isFinite(duration);

  return (
    <div
      ref={trackRef}
      className={cn(
        "group relative w-full h-6 flex items-center cursor-pointer",
        disabled && "opacity-50 cursor-not-allowed",
        className
      )}
      onMouseEnter={() => !disabled && setIsHovering(true)}
      onMouseLeave={() => {
        setIsHovering(false);
        setIsDragging(false);
      }}
      onMouseMove={handleMouseMove}
      onMouseDown={handleMouseDown}
      onKeyDown={handleKeyDown}
      role="slider"
      aria-label={t("seekBar")}
      aria-valuemin={isLive ? seekableStart : 0}
      aria-valuemax={isLive ? effectiveLiveEdge : duration || 100}
      aria-valuenow={displayTime}
      aria-valuetext={
        isLive ? formatLiveTime(displayTime, effectiveLiveEdge) : formatTime(displayTime)
      }
      tabIndex={disabled ? -1 : 0}
    >
      {/* Track background */}
      <div className={cn("fw-seek-track", isDragging && "fw-seek-track--active")}>
        {/* Buffered segments - show actual buffered ranges */}
        {bufferedSegments.map((segment, index) => (
          <div
            key={index}
            className="fw-seek-buffered"
            style={{
              left: `${segment.startPercent}%`,
              width: `${segment.endPercent - segment.startPercent}%`,
            }}
          />
        ))}
        {/* Playback progress — RAF loop handles width during playback for smooth ~60fps updates */}
        <div
          ref={progressRef}
          className="fw-seek-progress"
          style={{ width: `${progressPercent}%` }}
        />
        {/* Hover scrub line */}
        {isHovering && !isDragging && (
          <div className="fw-seek-hover-line" style={{ left: `${hoverPosition}%` }} />
        )}
        {/* Live edge indicator */}
        {isLive && <div className="fw-seek-live-edge" />}
      </div>

      {/* Thumb */}
      <div
        className={cn(
          "fw-seek-thumb",
          showThumb ? "fw-seek-thumb--active" : "fw-seek-thumb--hidden"
        )}
        style={{ left: `${progressPercent}%` }}
      />

      {/* Hover time tooltip */}
      {isHovering && !isDragging && canShowTooltip && (
        <div className="fw-seek-tooltip" style={{ left: `${hoverPosition}%` }}>
          {isLive ? formatLiveTime(hoverTime, effectiveLiveEdge) : formatTime(hoverTime)}
        </div>
      )}
    </div>
  );
};

export default SeekBar;

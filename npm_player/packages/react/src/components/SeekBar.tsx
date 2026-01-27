import React, { useRef, useState, useCallback, useMemo } from "react";
import { cn } from "@livepeer-frameworks/player-core";

interface SeekBarProps {
  /** Current playback time in seconds */
  currentTime: number;
  /** Total duration in seconds */
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
  /** For live: start of seekable DVR window (seconds) */
  seekableStart?: number;
  /** For live: current live edge position (seconds) */
  liveEdge?: number;
  /** Defer seeking until drag release */
  commitOnRelease?: boolean;
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
}) => {
  const trackRef = useRef<HTMLDivElement>(null);
  const [isHovering, setIsHovering] = useState(false);
  const [isDragging, setIsDragging] = useState(false);
  const [dragTime, setDragTime] = useState<number | null>(null);
  const dragTimeRef = useRef<number | null>(null);
  const [hoverPosition, setHoverPosition] = useState(0);
  const [hoverTime, setHoverTime] = useState(0);

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

  // Calculate buffered segments as array of {start%, end%} for accurate display
  const bufferedSegments = useMemo(() => {
    if (!buffered || buffered.length === 0) return [];

    const rangeEnd = isLive ? effectiveLiveEdge : duration;
    const rangeStart = isLive ? seekableStart : 0;
    const rangeSize = rangeEnd - rangeStart;

    if (!Number.isFinite(rangeSize) || rangeSize <= 0) return [];

    const segments: Array<{ startPercent: number; endPercent: number }> = [];
    for (let i = 0; i < buffered.length; i++) {
      const start = buffered.start(i);
      const end = buffered.end(i);

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
  const formatTime = useCallback((seconds: number): string => {
    if (!Number.isFinite(seconds) || seconds < 0) return "0:00";
    const total = Math.floor(seconds);
    const hours = Math.floor(total / 3600);
    const minutes = Math.floor((total % 3600) / 60);
    const secs = total % 60;
    if (hours > 0) {
      return `${hours}:${String(minutes).padStart(2, "0")}:${String(secs).padStart(2, "0")}`;
    }
    return `${minutes}:${String(secs).padStart(2, "0")}`;
  }, []);

  // Format relative time for live streams (e.g., "-5:30" = 5:30 behind live edge)
  const formatLiveTime = useCallback((seconds: number, edge: number): string => {
    const behindSeconds = edge - seconds;
    if (behindSeconds < 1) return "LIVE";
    const total = Math.floor(behindSeconds);
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

  // Handle click to seek
  const handleClick = useCallback(
    (e: React.MouseEvent) => {
      if (disabled) return;
      if (!isLive && !Number.isFinite(duration)) return;
      const time = getTimeFromPosition(e.clientX);
      onSeek?.(time);
      setDragTime(null);
      dragTimeRef.current = null;
    },
    [disabled, duration, isLive, getTimeFromPosition, onSeek]
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
      onClick={handleClick}
      onMouseDown={handleMouseDown}
      role="slider"
      aria-label="Seek"
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
        {/* Playback progress */}
        <div className="fw-seek-progress" style={{ width: `${progressPercent}%` }} />
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

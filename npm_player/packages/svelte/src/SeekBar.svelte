<!--
  SeekBar.svelte - Industry-standard video seek bar
  Port of src/components/SeekBar.tsx
-->
<script lang="ts">
  import { cn } from '@livepeer-frameworks/player-core';

  interface Props {
    /** Current playback time in seconds */
    currentTime: number;
    /** Total duration in seconds */
    duration: number;
    /** Buffered time ranges from video element */
    buffered?: TimeRanges;
    /** Whether seeking is allowed */
    disabled?: boolean;
    /** Called when user seeks to a new time */
    onseek?: (time: number) => void;
    /** Additional class names */
    class?: string;
    /** Whether this is a live stream */
    isLive?: boolean;
    /** For live: start of seekable DVR window (seconds) */
    seekableStart?: number;
    /** For live: current live edge position (seconds) */
    liveEdge?: number;
    /** Defer seeking until drag release */
    commitOnRelease?: boolean;
  }

  let {
    currentTime,
    duration,
    buffered = undefined,
    disabled = false,
    onseek = undefined,
    class: className = '',
    isLive = false,
    seekableStart = 0,
    liveEdge = undefined,
    commitOnRelease = false,
  }: Props = $props();

  // Refs
  let trackRef: HTMLDivElement | undefined = $state();

  // Local state
  let isHovering = $state(false);
  let isDragging = $state(false);
  let dragTime = $state<number | null>(null);
  let hoverPosition = $state(0);
  let hoverTime = $state(0);

  // Effective live edge (use provided or fall back to duration)
  let effectiveLiveEdge = $derived(liveEdge ?? duration);

  // Seekable window size
  let seekableWindow = $derived(effectiveLiveEdge - seekableStart);

  // Calculate progress percentage
  let displayTime = $derived(dragTime ?? currentTime);
  let progressPercent = $derived.by(() => {
    if (isLive && seekableWindow > 0) {
      const positionInWindow = displayTime - seekableStart;
      return Math.min(100, Math.max(0, (positionInWindow / seekableWindow) * 100));
    }
    if (!Number.isFinite(duration) || duration <= 0) return 0;
    return Math.min(100, Math.max(0, (displayTime / duration) * 100));
  });

  // Calculate buffered segments as array of {start%, end%}
  let bufferedSegments = $derived.by(() => {
    if (!buffered || buffered.length === 0) return [];

    const rangeEnd = isLive ? effectiveLiveEdge : duration;
    const rangeStart = isLive ? seekableStart : 0;
    const rangeSize = rangeEnd - rangeStart;

    if (!Number.isFinite(rangeSize) || rangeSize <= 0) return [];

    const segments: Array<{ startPercent: number; endPercent: number }> = [];
    for (let i = 0; i < buffered.length; i++) {
      const start = buffered.start(i);
      const end = buffered.end(i);

      const relativeStart = start - rangeStart;
      const relativeEnd = end - rangeStart;

      segments.push({
        startPercent: Math.min(100, Math.max(0, (relativeStart / rangeSize) * 100)),
        endPercent: Math.min(100, Math.max(0, (relativeEnd / rangeSize) * 100)),
      });
    }
    return segments;
  });

  // Format time as MM:SS or HH:MM:SS
  function formatTime(seconds: number): string {
    if (!Number.isFinite(seconds) || seconds < 0) return '0:00';
    const total = Math.floor(seconds);
    const hours = Math.floor(total / 3600);
    const minutes = Math.floor((total % 3600) / 60);
    const secs = total % 60;
    if (hours > 0) {
      return `${hours}:${String(minutes).padStart(2, '0')}:${String(secs).padStart(2, '0')}`;
    }
    return `${minutes}:${String(secs).padStart(2, '0')}`;
  }

  // Format relative time for live streams
  function formatLiveTime(seconds: number, edge: number): string {
    const behindSeconds = edge - seconds;
    if (behindSeconds < 1) return 'LIVE';
    const total = Math.floor(behindSeconds);
    const hours = Math.floor(total / 3600);
    const minutes = Math.floor((total % 3600) / 60);
    const secs = total % 60;
    if (hours > 0) {
      return `-${hours}:${String(minutes).padStart(2, '0')}:${String(secs).padStart(2, '0')}`;
    }
    return `-${minutes}:${String(secs).padStart(2, '0')}`;
  }

  // Calculate time from mouse position
  function getTimeFromPosition(clientX: number): number {
    if (!trackRef) return 0;
    const rect = trackRef.getBoundingClientRect();
    const x = clientX - rect.left;
    const percent = Math.min(1, Math.max(0, x / rect.width));

    // Live with valid seekable window
    if (isLive && Number.isFinite(seekableWindow) && seekableWindow > 0) {
      return seekableStart + (percent * seekableWindow);
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
        return start + (percent * window);
      }
    }

    // Last resort: use currentTime as a baseline
    return percent * (currentTime || 1);
  }

  // Handle mouse move for hover preview
  function handleMouseMove(e: MouseEvent) {
    if (!trackRef || disabled) return;
    const rect = trackRef.getBoundingClientRect();
    const x = e.clientX - rect.left;
    const percent = Math.min(1, Math.max(0, x / rect.width));
    hoverPosition = percent * 100;
    hoverTime = getTimeFromPosition(e.clientX);
  }

  // Handle click to seek
  function handleClick(e: MouseEvent) {
    if (disabled) return;
    if (!isLive && !Number.isFinite(duration)) return;
    const time = getTimeFromPosition(e.clientX);
    onseek?.(time);
    dragTime = null;
  }

  // Handle drag start
  function handleMouseDown(e: MouseEvent) {
    if (disabled) return;
    if (!isLive && !Number.isFinite(duration)) return;
    e.preventDefault();
    isDragging = true;

    const handleDragMove = (moveEvent: MouseEvent) => {
      const time = getTimeFromPosition(moveEvent.clientX);
      if (commitOnRelease) {
        dragTime = time;
      } else {
        onseek?.(time);
      }
    };

    const handleDragEnd = () => {
      isDragging = false;
      document.removeEventListener('mousemove', handleDragMove);
      document.removeEventListener('mouseup', handleDragEnd);
      if (commitOnRelease && dragTime !== null) {
        onseek?.(dragTime);
        dragTime = null;
      }
    };

    document.addEventListener('mousemove', handleDragMove);
    document.addEventListener('mouseup', handleDragEnd);

    // Initial seek
    const time = getTimeFromPosition(e.clientX);
    if (commitOnRelease) {
      dragTime = time;
    } else {
      onseek?.(time);
    }
  }

  let showThumb = $derived(isHovering || isDragging);
  let canShowTooltip = $derived(isLive ? seekableWindow > 0 : Number.isFinite(duration));
  let ariaValueText = $derived(isLive ? formatLiveTime(displayTime, effectiveLiveEdge) : formatTime(displayTime));
</script>

<div
  bind:this={trackRef}
  class={cn(
    'group relative w-full h-6 flex items-center cursor-pointer',
    disabled && 'opacity-50 cursor-not-allowed',
    className
  )}
  onmouseenter={() => !disabled && (isHovering = true)}
  onmouseleave={() => { isHovering = false; isDragging = false; }}
  onmousemove={handleMouseMove}
  onclick={handleClick}
  onmousedown={handleMouseDown}
  role="slider"
  aria-label="Seek"
  aria-valuemin={isLive ? seekableStart : 0}
  aria-valuemax={isLive ? effectiveLiveEdge : (duration || 100)}
  aria-valuenow={displayTime}
  aria-valuetext={ariaValueText}
  tabindex={disabled ? -1 : 0}
>
  <!-- Track background -->
  <div class={cn(
    'fw-seek-track',
    isDragging && 'fw-seek-track--active'
  )}>
    <!-- Buffered segments -->
    {#each bufferedSegments as segment, _index}
      <div
        class="fw-seek-buffered"
        style="left: {segment.startPercent}%; width: {segment.endPercent - segment.startPercent}%;"
      ></div>
    {/each}
    <!-- Playback progress -->
    <div
      class="fw-seek-progress"
      style="width: {progressPercent}%;"
    ></div>
  </div>

  <!-- Thumb -->
  <div
    class={cn(
      'fw-seek-thumb',
      showThumb ? 'fw-seek-thumb--active' : 'fw-seek-thumb--hidden'
    )}
    style="left: {progressPercent}%;"
  ></div>

  <!-- Hover time tooltip -->
  {#if isHovering && !isDragging && canShowTooltip}
    <div
      class="fw-seek-tooltip"
      style="left: {hoverPosition}%;"
    >
      {isLive ? formatLiveTime(hoverTime, effectiveLiveEdge) : formatTime(hoverTime)}
    </div>
  {/if}
</div>

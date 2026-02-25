<!--
  SubtitleRenderer.svelte - Live or static subtitles renderer
  Port of src/components/SubtitleRenderer.tsx

  Supports:
  - Static cue list (pre-loaded)
  - Live cues from meta track subscription
  - Customizable styling (font, colors, position)
  - Automatic timing synchronization with video
-->
<script lang="ts">
  import { onDestroy } from "svelte";

  interface SubtitleCue {
    id: string;
    text: string;
    startTime: number;
    endTime: number;
    lang?: string;
  }

  interface MetaTrackEvent {
    type: string;
    data: unknown;
  }

  interface SubtitleStyle {
    fontSize?: string;
    fontFamily?: string;
    color?: string;
    backgroundColor?: string;
    textShadow?: string;
    bottom?: string;
    maxWidth?: string;
    padding?: string;
    borderRadius?: string;
  }

  interface Props {
    /** Current video playback time in milliseconds */
    currentTime: number;
    /** Whether subtitles are enabled */
    enabled?: boolean;
    /** Subtitle cues to render (static or from meta track) */
    cues?: SubtitleCue[];
    /** Subscribe to meta track function (for live subtitles) */
    subscribeToMetaTrack?: (
      trackId: string,
      callback: (event: MetaTrackEvent) => void
    ) => () => void;
    /** Meta track ID for live subtitles */
    metaTrackId?: string;
    /** Custom styles */
    style?: SubtitleStyle;
    /** Container class name */
    class?: string;
  }

  let {
    currentTime,
    enabled = true,
    cues: staticCues,
    subscribeToMetaTrack,
    metaTrackId,
    style: customStyle,
    class: className = "",
  }: Props = $props();

  const DEFAULT_STYLE: SubtitleStyle = {
    fontSize: "1.5rem",
    fontFamily: "system-ui, -apple-system, sans-serif",
    color: "white",
    backgroundColor: "rgba(0, 0, 0, 0.75)",
    textShadow: "2px 2px 4px rgba(0, 0, 0, 0.5)",
    bottom: "5%",
    maxWidth: "90%",
    padding: "0.5em 1em",
    borderRadius: "4px",
  };

  // State
  let liveCues = $state<SubtitleCue[]>([]);
  let displayedText = $state<string>("");
  let _lastCueId: string | null = null;
  let unsubscribe: (() => void) | null = null;

  // Merged style
  let mergedStyle = $derived({ ...DEFAULT_STYLE, ...customStyle });

  // All available cues (static + live)
  let allCues = $derived([...(staticCues ?? []), ...liveCues]);

  // Parse subtitle cue from meta track event data
  function parseSubtitleCue(data: unknown): SubtitleCue | null {
    if (typeof data !== "object" || data === null) return null;

    const obj = data as Record<string, unknown>;

    const text = typeof obj.text === "string" ? obj.text : String(obj.text ?? "");
    if (!text) return null;

    let startTime = 0;
    let endTime = Infinity;

    if ("startTime" in obj) startTime = Number(obj.startTime);
    else if ("start" in obj) startTime = Number(obj.start);

    if ("endTime" in obj) endTime = Number(obj.endTime);
    else if ("end" in obj) endTime = Number(obj.end);

    const id = typeof obj.id === "string" ? obj.id : String(Date.now());

    return {
      id,
      text,
      startTime,
      endTime,
      lang: typeof obj.lang === "string" ? obj.lang : undefined,
    };
  }

  // Subscribe to live subtitles if meta track is configured
  $effect(() => {
    if (!enabled || !subscribeToMetaTrack || !metaTrackId) {
      if (unsubscribe) {
        unsubscribe();
        unsubscribe = null;
      }
      return;
    }

    const handleMetaEvent = (event: MetaTrackEvent) => {
      if (event.type === "subtitle") {
        const cue = parseSubtitleCue(event.data);
        if (cue) {
          // Deduplicate by ID
          const existing = liveCues.find((c) => c.id === cue.id);
          if (!existing) {
            // Keep last 50 cues max
            liveCues = [...liveCues, cue].slice(-50);
          }
        }
      }
    };

    unsubscribe = subscribeToMetaTrack(metaTrackId, handleMetaEvent);

    return () => {
      if (unsubscribe) {
        unsubscribe();
        unsubscribe = null;
      }
    };
  });

  // Find active cue based on current time
  $effect(() => {
    if (!enabled) {
      displayedText = "";
      return;
    }

    const currentTimeMs = currentTime;
    const activeCue = allCues.find((cue) => {
      const start = cue.startTime;
      const end = cue.endTime;
      return currentTimeMs >= start && currentTimeMs < end;
    });

    if (activeCue) {
      displayedText = activeCue.text;
      _lastCueId = activeCue.id;
    } else {
      displayedText = "";
      _lastCueId = null;
    }
  });

  // Clean up expired cues
  $effect(() => {
    const currentTimeMs = currentTime;

    liveCues = liveCues.filter((cue) => {
      const endTime = cue.endTime === Infinity ? cue.startTime + 10000 : cue.endTime;
      return endTime >= currentTimeMs - 30000;
    });
  });

  // Cleanup on destroy
  onDestroy(() => {
    if (unsubscribe) {
      unsubscribe();
      unsubscribe = null;
    }
  });
</script>

{#if enabled && displayedText}
  <div
    class="subtitle-container {className}"
    style="bottom: {mergedStyle.bottom}; max-width: {mergedStyle.maxWidth};"
    role="region"
    aria-live="polite"
    aria-label="Subtitles"
  >
    <span
      class="subtitle-text"
      style="
        font-size: {mergedStyle.fontSize};
        font-family: {mergedStyle.fontFamily};
        color: {mergedStyle.color};
        background-color: {mergedStyle.backgroundColor};
        text-shadow: {mergedStyle.textShadow};
        padding: {mergedStyle.padding};
        border-radius: {mergedStyle.borderRadius};
      "
    >
      {displayedText}
    </span>
  </div>
{/if}

<style>
  .subtitle-container {
    position: absolute;
    left: 50%;
    transform: translateX(-50%);
    z-index: 30;
    text-align: center;
    pointer-events: none;
  }

  .subtitle-text {
    display: inline-block;
    white-space: pre-wrap;
  }
</style>

import React, { useEffect, useState, useRef } from "react";
import type { SubtitleCue, MetaTrackEvent } from "../types";

export interface SubtitleRendererProps {
  /** Current video playback time in milliseconds */
  currentTime: number;
  /** Whether subtitles are enabled */
  enabled?: boolean;
  /** Subtitle cues to render (static or from meta track) */
  cues?: SubtitleCue[];
  /** Subscribe to meta track function (for live subtitles) */
  subscribeToMetaTrack?: (trackId: string, callback: (event: MetaTrackEvent) => void) => () => void;
  /** Meta track ID for live subtitles */
  metaTrackId?: string;
  /** Custom styles */
  style?: SubtitleStyle;
  /** Container class name */
  className?: string;
}

export interface SubtitleStyle {
  /** Font size (default: '1.5rem') */
  fontSize?: string;
  /** Font family (default: system) */
  fontFamily?: string;
  /** Text color (default: 'white') */
  color?: string;
  /** Background color (default: 'rgba(0,0,0,0.75)') */
  backgroundColor?: string;
  /** Text shadow for readability */
  textShadow?: string;
  /** Bottom offset from video (default: '5%') */
  bottom?: string;
  /** Max width (default: '90%') */
  maxWidth?: string;
  /** Padding (default: '0.5em 1em') */
  padding?: string;
  /** Border radius (default: '4px') */
  borderRadius?: string;
}

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

/**
 * Parse subtitle cue from meta track event data
 */
function parseSubtitleCue(data: unknown): SubtitleCue | null {
  if (typeof data !== "object" || data === null) return null;

  const obj = data as Record<string, unknown>;

  // Extract text
  const text = typeof obj.text === "string" ? obj.text : String(obj.text ?? "");
  if (!text) return null;

  // Extract timing
  let startTime = 0;
  let endTime = Infinity;

  if ("startTime" in obj) startTime = Number(obj.startTime);
  else if ("start" in obj) startTime = Number(obj.start);

  if ("endTime" in obj) endTime = Number(obj.endTime);
  else if ("end" in obj) endTime = Number(obj.end);

  // Extract ID
  const id = typeof obj.id === "string" ? obj.id : String(Date.now());

  return {
    id,
    text,
    startTime,
    endTime,
    lang: typeof obj.lang === "string" ? obj.lang : undefined,
  };
}

/**
 * SubtitleRenderer - Renders live or static subtitles over video
 *
 * Supports:
 * - Static cue list (pre-loaded)
 * - Live cues from meta track subscription
 * - Customizable styling (font, colors, position)
 * - Automatic timing synchronization with video
 *
 * @example
 * ```tsx
 * // Static subtitles
 * <SubtitleRenderer
 *   currentTime={videoElement.currentTime * 1000}
 *   enabled={showSubtitles}
 *   cues={subtitleCues}
 * />
 *
 * // Live subtitles from meta track
 * const { subscribe } = useMetaTrack({ mistBaseUrl, streamName });
 *
 * <SubtitleRenderer
 *   currentTime={videoElement.currentTime * 1000}
 *   enabled={showSubtitles}
 *   subscribeToMetaTrack={subscribe}
 *   metaTrackId="subtitle-track"
 * />
 * ```
 */
export const SubtitleRenderer: React.FC<SubtitleRendererProps> = ({
  currentTime,
  enabled = true,
  cues: staticCues,
  subscribeToMetaTrack,
  metaTrackId,
  style: customStyle,
  className = "",
}) => {
  const [liveCues, setLiveCues] = useState<SubtitleCue[]>([]);
  const [displayedText, setDisplayedText] = useState<string>("");
  const lastCueIdRef = useRef<string | null>(null);

  const style = { ...DEFAULT_STYLE, ...customStyle };

  // All available cues (static + live)
  const allCues = [...(staticCues ?? []), ...liveCues];

  // Subscribe to live subtitles if meta track is configured
  useEffect(() => {
    if (!enabled || !subscribeToMetaTrack || !metaTrackId) {
      return;
    }

    const handleMetaEvent = (event: MetaTrackEvent) => {
      if (event.type === "subtitle") {
        const cue = parseSubtitleCue(event.data);
        if (cue) {
          setLiveCues((prev) => {
            // Deduplicate by ID
            const existing = prev.find((c) => c.id === cue.id);
            if (existing) return prev;

            // Keep last 50 cues max
            const updated = [...prev, cue];
            return updated.slice(-50);
          });
        }
      }
    };

    const unsubscribe = subscribeToMetaTrack(metaTrackId, handleMetaEvent);

    return () => {
      unsubscribe();
    };
  }, [enabled, subscribeToMetaTrack, metaTrackId]);

  // Find active cue based on current time
  useEffect(() => {
    if (!enabled) {
      setDisplayedText("");
      return;
    }

    // Find cue that matches current time
    const currentTimeMs = currentTime;
    const activeCue = allCues.find((cue) => {
      const start = cue.startTime;
      const end = cue.endTime;
      return currentTimeMs >= start && currentTimeMs < end;
    });

    if (activeCue) {
      setDisplayedText(activeCue.text);
      lastCueIdRef.current = activeCue.id;
    } else {
      setDisplayedText("");
      lastCueIdRef.current = null;
    }
  }, [enabled, currentTime, allCues]);

  // Clean up expired cues
  useEffect(() => {
    const currentTimeMs = currentTime;

    setLiveCues((prev) => {
      // Remove cues that are more than 30 seconds old
      return prev.filter((cue) => {
        const endTime = cue.endTime === Infinity ? cue.startTime + 10000 : cue.endTime;
        return endTime >= currentTimeMs - 30000;
      });
    });
  }, [currentTime]);

  if (!enabled || !displayedText) {
    return null;
  }

  return (
    <div
      className={`fw-absolute fw-left-1/2 fw-transform fw--translate-x-1/2 fw-z-30 fw-text-center fw-pointer-events-none ${className}`}
      style={{
        bottom: style.bottom,
        maxWidth: style.maxWidth,
      }}
      aria-live="polite"
      role="region"
      aria-label="Subtitles"
    >
      <span
        className="fw-inline-block fw-whitespace-pre-wrap"
        style={{
          fontSize: style.fontSize,
          fontFamily: style.fontFamily,
          color: style.color,
          backgroundColor: style.backgroundColor,
          textShadow: style.textShadow,
          padding: style.padding,
          borderRadius: style.borderRadius,
        }}
      >
        {displayedText}
      </span>
    </div>
  );
};

export default SubtitleRenderer;

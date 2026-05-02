export interface ThumbnailCue {
  startTime: number;
  endTime: number;
  url: string;
  x?: number;
  y?: number;
  width?: number;
  height?: number;
}

export interface ThumbnailCueTimelineOptions {
  isLive: boolean;
  seekableStartMs: number;
  liveEdgeMs: number;
  mistRangeMs?: { start: number; end: number } | null;
  maxLiveEdgeGapSec?: number;
}

function parseTimestamp(ts: string): number {
  const parts = ts.trim().split(":");
  if (parts.length === 3) {
    const [h, m, s] = parts;
    return parseInt(h, 10) * 3600 + parseInt(m, 10) * 60 + parseFloat(s);
  }
  if (parts.length === 2) {
    const [m, s] = parts;
    return parseInt(m, 10) * 60 + parseFloat(s);
  }
  return 0;
}

function parseXywh(url: string): {
  baseUrl: string;
  x?: number;
  y?: number;
  width?: number;
  height?: number;
} {
  const match = url.match(/#xywh=(\d+),(\d+),(\d+),(\d+)$/);
  if (!match) return { baseUrl: url };
  return {
    baseUrl: url.slice(0, url.indexOf("#")),
    x: parseInt(match[1], 10),
    y: parseInt(match[2], 10),
    width: parseInt(match[3], 10),
    height: parseInt(match[4], 10),
  };
}

export function parseThumbnailVtt(vttText: string): ThumbnailCue[] {
  const cues: ThumbnailCue[] = [];
  const blocks = vttText.trim().split(/\n\s*\n/);

  for (const block of blocks) {
    const lines = block.trim().split("\n");

    let timeLine = -1;
    for (let i = 0; i < lines.length; i++) {
      if (lines[i].includes("-->")) {
        timeLine = i;
        break;
      }
    }
    if (timeLine === -1) continue;

    const timeMatch = lines[timeLine].match(
      /(\d+:\d{2}:\d{2}\.\d{3}|\d{1,2}:\d{2}\.\d{3})\s*-->\s*(\d+:\d{2}:\d{2}\.\d{3}|\d{1,2}:\d{2}\.\d{3})/
    );
    if (!timeMatch) continue;

    const startTime = parseTimestamp(timeMatch[1]);
    const endTime = parseTimestamp(timeMatch[2]);

    const payload = lines
      .slice(timeLine + 1)
      .join("\n")
      .trim();
    if (!payload) continue;

    const { baseUrl, x, y, width, height } = parseXywh(payload);

    cues.push({
      startTime,
      endTime,
      url: baseUrl,
      ...(x !== undefined && { x }),
      ...(y !== undefined && { y }),
      ...(width !== undefined && { width }),
      ...(height !== undefined && { height }),
    });
  }

  return cues;
}

function getCueRange(cues: ThumbnailCue[]): { start: number; end: number } | null {
  if (cues.length === 0) return null;
  let start = Infinity;
  let end = -Infinity;
  for (const cue of cues) {
    if (Number.isFinite(cue.startTime)) start = Math.min(start, cue.startTime);
    if (Number.isFinite(cue.endTime)) end = Math.max(end, cue.endTime);
  }
  if (!Number.isFinite(start) || !Number.isFinite(end) || end <= start) return null;
  return { start, end };
}

/**
 * Rebase live thumbnail cue times into the active player's seek coordinate space.
 *
 * Mist/Chandler sprite VTT can be expressed as absolute Mist track time, browser
 * media time, or live-window-relative time depending on source/protocol. The seek
 * bar and seek() calls must use the same coordinates, so normalize cues before UI
 * lookup.
 */
export function normalizeThumbnailCueTimeline(
  cues: ThumbnailCue[],
  options: ThumbnailCueTimelineOptions
): ThumbnailCue[] {
  if (!options.isLive || cues.length === 0) return cues;

  const cueRange = getCueRange(cues);
  const playerStart = options.seekableStartMs / 1000;
  const playerEnd = options.liveEdgeMs / 1000;
  if (
    !cueRange ||
    !Number.isFinite(playerStart) ||
    !Number.isFinite(playerEnd) ||
    playerEnd <= playerStart
  ) {
    return cues;
  }

  const playerWindow = playerEnd - playerStart;
  const cueWindow = cueRange.end - cueRange.start;
  const tolerance = Math.max(1, Math.min(10, playerWindow * 0.1));
  const maxGap = options.maxLiveEdgeGapSec ?? Math.max(30, Math.min(120, playerWindow * 0.25));
  const overlapsPlayerWindow =
    cueRange.end >= playerStart - tolerance && cueRange.start <= playerEnd + tolerance;
  const trailsLiveEdge = cueRange.end <= playerEnd && playerEnd - cueRange.end <= maxGap;
  let offset = 0;

  if (Math.abs(cueRange.end - playerEnd) <= tolerance || (overlapsPlayerWindow && trailsLiveEdge)) {
    offset = 0;
  } else {
    const mistRange = options.mistRangeMs;
    const mistEnd = mistRange ? mistRange.end / 1000 : NaN;
    if (Number.isFinite(mistEnd) && Math.abs(cueRange.end - mistEnd) <= tolerance) {
      offset = playerEnd - mistEnd;
    } else {
      offset = playerEnd - cueRange.end;
    }
  }

  const normalized =
    offset === 0
      ? cues.map((cue) => ({ ...cue }))
      : cues.map((cue) => ({
          ...cue,
          startTime: cue.startTime + offset,
          endTime: cue.endTime + offset,
        }));

  const normalizedRange = getCueRange(normalized);
  if (!normalizedRange) return normalized;

  const liveEdgeGap = playerEnd - normalizedRange.end;
  if (liveEdgeGap > 0 && liveEdgeGap <= maxGap) {
    let lastIndex = 0;
    for (let i = 1; i < normalized.length; i += 1) {
      if (normalized[i].endTime > normalized[lastIndex].endTime) lastIndex = i;
    }
    normalized[lastIndex] = { ...normalized[lastIndex], endTime: playerEnd };
  }

  return normalized;
}

export function findCueAtTime(cues: ThumbnailCue[], time: number): ThumbnailCue | null {
  if (cues.length === 0) return null;

  let lo = 0;
  let hi = cues.length - 1;

  while (lo <= hi) {
    const mid = (lo + hi) >>> 1;
    const cue = cues[mid];

    if (time < cue.startTime) {
      hi = mid - 1;
    } else if (time >= cue.endTime) {
      lo = mid + 1;
    } else {
      return cue;
    }
  }

  return null;
}

export async function fetchThumbnailVtt(url: string): Promise<ThumbnailCue[]> {
  const response = await fetch(url);
  if (!response.ok) {
    throw new Error(`Failed to fetch thumbnail VTT: ${response.status}`);
  }
  const text = await response.text();
  return parseThumbnailVtt(text);
}

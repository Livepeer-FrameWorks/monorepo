export interface ThumbnailCue {
  startTime: number;
  endTime: number;
  url: string;
  x?: number;
  y?: number;
  width?: number;
  height?: number;
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
      /(\d{1,2}:?\d{2}:\d{2}\.\d{3})\s*-->\s*(\d{1,2}:?\d{2}:\d{2}\.\d{3})/
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

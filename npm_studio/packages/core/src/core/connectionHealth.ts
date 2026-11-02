import type { IngestStats } from "../types";

export type ConnectionHealthLevel = "good" | "fair" | "poor";

export interface ConnectionHealth {
  level: ConnectionHealthLevel;
  /** Video plus audio send rate */
  bitrateKbps: number;
  /** Lost packets as a share of the packets sent since the previous sample, in percent */
  packetLossPct: number;
  rttMs: number;
}

// Above these a viewer sees stalls or quality drops: WebRTC recovers from
// about 2% loss and 150 ms without visible effect, and rarely from 5% or 300 ms.
const FAIR_LOSS_PCT = 2;
const POOR_LOSS_PCT = 5;
const FAIR_RTT_MS = 150;
const POOR_RTT_MS = 300;

/**
 * Loss between two samples, so the grade follows the link as it is now rather
 * than its whole history. Without a usable previous sample (the first one, or
 * counters that restarted with a new connection) it falls back to the totals.
 */
export function intervalLossPct(current: IngestStats, previous: IngestStats | null): number {
  const sentNow = current.video.packetsSent + current.audio.packetsSent;
  const lostNow = current.video.packetsLost + current.audio.packetsLost;
  let sent = sentNow;
  let lost = lostNow;
  if (previous) {
    const sentDelta = sentNow - (previous.video.packetsSent + previous.audio.packetsSent);
    const lostDelta = lostNow - (previous.video.packetsLost + previous.audio.packetsLost);
    if (sentDelta > 0 && lostDelta >= 0) {
      sent = sentDelta;
      lost = lostDelta;
    }
  }
  if (sent <= 0 || lost <= 0) return 0;
  return Math.min(100, (lost / sent) * 100);
}

/** How the publish connection is doing, for the health pill; null before stats arrive. */
export function assessConnectionHealth(stats: IngestStats | null): ConnectionHealth | null {
  if (!stats) return null;
  const packetLossPct = stats.connection.packetLossPct;
  const rttMs = stats.connection.rtt;
  const bitrateKbps = Math.round((stats.video.bitrate + stats.audio.bitrate) / 1000);

  let level: ConnectionHealthLevel = "good";
  if (packetLossPct >= POOR_LOSS_PCT || rttMs >= POOR_RTT_MS) level = "poor";
  else if (packetLossPct >= FAIR_LOSS_PCT || rttMs >= FAIR_RTT_MS) level = "fair";

  return { level, bitrateKbps, packetLossPct, rttMs };
}

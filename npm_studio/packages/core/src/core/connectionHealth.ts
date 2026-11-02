import type { IngestStats } from "../types";

export type ConnectionHealthLevel = "good" | "fair" | "poor";

export interface ConnectionHealth {
  level: ConnectionHealthLevel;
  /** Video plus audio send rate */
  bitrateKbps: number;
  /** Lost packets as a share of all packets sent so far, in percent */
  packetLossPct: number;
  rttMs: number;
}

// Above these a viewer sees stalls or quality drops: WebRTC recovers from
// about 2% loss and 150 ms without visible effect, and rarely from 5% or 300 ms.
const FAIR_LOSS_PCT = 2;
const POOR_LOSS_PCT = 5;
const FAIR_RTT_MS = 150;
const POOR_RTT_MS = 300;

/** How the publish connection is doing, for the health pill; null before stats arrive. */
export function assessConnectionHealth(stats: IngestStats | null): ConnectionHealth | null {
  if (!stats) return null;
  const sent = stats.video.packetsSent + stats.audio.packetsSent;
  const lost = stats.video.packetsLost + stats.audio.packetsLost;
  const packetLossPct = sent + lost > 0 ? (lost / (sent + lost)) * 100 : 0;
  const rttMs = stats.connection.rtt;
  const bitrateKbps = Math.round((stats.video.bitrate + stats.audio.bitrate) / 1000);

  let level: ConnectionHealthLevel = "good";
  if (packetLossPct >= POOR_LOSS_PCT || rttMs >= POOR_RTT_MS) level = "poor";
  else if (packetLossPct >= FAIR_LOSS_PCT || rttMs >= FAIR_RTT_MS) level = "fair";

  return { level, bitrateKbps, packetLossPct, rttMs };
}

/**
 * Bitrate Adaptation
 * Network-aware encoding that dynamically adjusts quality based on connection conditions.
 * Polls RTCPeerConnection stats and adjusts encoder bitrate/resolution.
 *
 * Algorithm:
 *   1. Poll getStats() every pollInterval (default 2s)
 *   2. Track: availableOutgoingBitrate, roundTripTime, packetLoss
 *   3. Detect congestion: packet loss spikes, RTT increases
 *   4. Adjust bitrate with smooth transitions (no jumps)
 *   5. Resolution downscale at very low bandwidth
 */

import { TypedEventEmitter } from "./EventEmitter";
import type { EncoderManager } from "./EncoderManager";

// ============================================================================
// Types
// ============================================================================

export type CongestionLevel = "none" | "mild" | "severe";

interface BitrateEvents {
  bitrateChanged: { bitrate: number; previousBitrate: number; congestion: CongestionLevel };
  congestionChanged: { level: CongestionLevel; packetLoss: number; rtt: number };
  resolutionChanged: { width: number; height: number };
}

export interface BitrateAdaptationOptions {
  pc: RTCPeerConnection;
  encoder: EncoderManager;
  minBitrate?: number; // Floor (default: 500 kbps)
  maxBitrate?: number; // Ceiling from quality profile
  pollInterval?: number; // Stats polling interval in ms (default: 2000)
}

interface StatsSnapshot {
  timestamp: number;
  bytesSent: number;
  packetsSent: number;
  packetsLost: number;
  rtt: number;
  availableBitrate: number | null;
}

// Resolution tiers for downscale (from quality profile's base down)
const RESOLUTION_TIERS = [
  { width: 1920, height: 1080 },
  { width: 1280, height: 720 },
  { width: 854, height: 480 },
  { width: 640, height: 360 },
];

// ============================================================================
// BitrateAdaptation
// ============================================================================

export class BitrateAdaptation extends TypedEventEmitter<BitrateEvents> {
  private pc: RTCPeerConnection;
  private encoder: EncoderManager;
  private minBitrate: number;
  private maxBitrate: number;
  private pollInterval: number;

  private timer: ReturnType<typeof setInterval> | null = null;
  private running = false;

  // Current state
  private currentBitrate: number;
  private currentCongestion: CongestionLevel = "none";
  private currentResolutionTier = 0; // Index into RESOLUTION_TIERS

  // Stats history (last 5 samples for trend detection)
  private history: StatsSnapshot[] = [];
  private readonly MAX_HISTORY = 5;

  // Smoothing
  private readonly INCREASE_STEP = 0.1; // +10% per interval when clear
  private readonly MILD_DECREASE = 0.2; // -20% on mild congestion
  private readonly SEVERE_DECREASE = 0.5; // -50% on severe congestion
  private readonly MILD_LOSS_THRESHOLD = 0.05; // 5% packet loss = mild
  private readonly SEVERE_LOSS_THRESHOLD = 0.15; // 15% = severe
  private readonly RTT_INCREASE_MILD = 50; // +50ms RTT increase = mild
  private readonly RTT_INCREASE_SEVERE = 150; // +150ms = severe

  constructor(opts: BitrateAdaptationOptions) {
    super();
    this.pc = opts.pc;
    this.encoder = opts.encoder;
    this.minBitrate = opts.minBitrate ?? 500_000;
    this.maxBitrate = opts.maxBitrate ?? 8_000_000;
    this.pollInterval = opts.pollInterval ?? 2000;
    this.currentBitrate = this.maxBitrate;
  }

  // ==========================================================================
  // Public API
  // ==========================================================================

  start(): void {
    if (this.running) return;
    this.running = true;
    this.history = [];

    this.timer = setInterval(() => this.poll(), this.pollInterval);
  }

  stop(): void {
    this.running = false;
    if (this.timer) {
      clearInterval(this.timer);
      this.timer = null;
    }
  }

  get bitrate(): number {
    return this.currentBitrate;
  }

  get congestionLevel(): CongestionLevel {
    return this.currentCongestion;
  }

  destroy(): void {
    this.stop();
    this.removeAllListeners();
  }

  // ==========================================================================
  // Stats polling
  // ==========================================================================

  private async poll(): Promise<void> {
    if (!this.running) return;

    try {
      const report = await this.pc.getStats();
      const snapshot = this.extractStats(report);
      if (!snapshot) return;

      this.history.push(snapshot);
      if (this.history.length > this.MAX_HISTORY) {
        this.history.shift();
      }

      if (this.history.length < 2) return; // Need at least 2 samples

      const congestion = this.detectCongestion();
      this.adapt(congestion);
    } catch {
      // Stats fetch can fail during disconnect — ignore
    }
  }

  private extractStats(report: RTCStatsReport): StatsSnapshot | null {
    let bytesSent = 0;
    let packetsSent = 0;
    let packetsLost = 0;
    let rtt = 0;
    let availableBitrate: number | null = null;
    let hasVideo = false;

    report.forEach((stat) => {
      if (stat.type === "outbound-rtp" && (stat as RTCOutboundRtpStreamStats).kind === "video") {
        const rtp = stat as RTCOutboundRtpStreamStats;
        bytesSent = rtp.bytesSent ?? 0;
        packetsSent = rtp.packetsSent ?? 0;
        hasVideo = true;
      }

      if (stat.type === "remote-inbound-rtp" && (stat as any).kind === "video") {
        packetsLost = (stat as any).packetsLost ?? 0;
        rtt = (stat as any).roundTripTime ?? 0;
      }

      if (
        stat.type === "candidate-pair" &&
        (stat as RTCIceCandidatePairStats).state === "succeeded"
      ) {
        const pair = stat as RTCIceCandidatePairStats;
        if ((pair as any).availableOutgoingBitrate !== undefined) {
          availableBitrate = (pair as any).availableOutgoingBitrate;
        }
        if (pair.currentRoundTripTime !== undefined) {
          rtt = pair.currentRoundTripTime;
        }
      }
    });

    if (!hasVideo) return null;

    return {
      timestamp: performance.now(),
      bytesSent,
      packetsSent,
      packetsLost,
      rtt,
      availableBitrate,
    };
  }

  // ==========================================================================
  // Congestion detection
  // ==========================================================================

  private detectCongestion(): CongestionLevel {
    const latest = this.history[this.history.length - 1];
    const prev = this.history[this.history.length - 2];

    // Packet loss rate over last interval
    const packetsDelta = latest.packetsSent - prev.packetsSent;
    const lostDelta = latest.packetsLost - prev.packetsLost;
    const lossRate = packetsDelta > 0 ? Math.max(0, lostDelta) / packetsDelta : 0;

    // RTT increase over baseline (average of first 3 samples)
    const baselineRtt =
      this.history.length >= 3
        ? this.history.slice(0, 3).reduce((sum, s) => sum + s.rtt, 0) / 3
        : prev.rtt;
    const rttIncrease = (latest.rtt - baselineRtt) * 1000; // Convert to ms

    let level: CongestionLevel = "none";

    if (lossRate >= this.SEVERE_LOSS_THRESHOLD || rttIncrease >= this.RTT_INCREASE_SEVERE) {
      level = "severe";
    } else if (lossRate >= this.MILD_LOSS_THRESHOLD || rttIncrease >= this.RTT_INCREASE_MILD) {
      level = "mild";
    }

    if (level !== this.currentCongestion) {
      this.currentCongestion = level;
      this.emit("congestionChanged", { level, packetLoss: lossRate, rtt: latest.rtt });
    }

    return level;
  }

  // ==========================================================================
  // Bitrate adaptation
  // ==========================================================================

  private adapt(congestion: CongestionLevel): void {
    const previousBitrate = this.currentBitrate;
    let newBitrate = this.currentBitrate;

    switch (congestion) {
      case "none":
        // Gradually increase toward max
        if (this.currentBitrate < this.maxBitrate) {
          newBitrate = Math.min(this.maxBitrate, this.currentBitrate * (1 + this.INCREASE_STEP));
        }
        break;

      case "mild":
        newBitrate = Math.max(this.minBitrate, this.currentBitrate * (1 - this.MILD_DECREASE));
        break;

      case "severe":
        newBitrate = Math.max(this.minBitrate, this.currentBitrate * (1 - this.SEVERE_DECREASE));
        break;
    }

    // Quantize to nearest 100kbps for stable steps
    newBitrate = Math.round(newBitrate / 100_000) * 100_000;
    newBitrate = Math.max(this.minBitrate, Math.min(this.maxBitrate, newBitrate));

    if (newBitrate !== previousBitrate) {
      this.currentBitrate = newBitrate;
      this.applyBitrate(newBitrate);
      this.emit("bitrateChanged", { bitrate: newBitrate, previousBitrate, congestion });
    }

    // Resolution downscale if bitrate is very low
    this.checkResolutionDownscale(newBitrate);
  }

  private applyBitrate(bitrate: number): void {
    this.encoder
      .updateConfig({
        video: { bitrate } as any,
      })
      .catch(() => {
        // Encoder may reject if not initialized
      });
  }

  private checkResolutionDownscale(bitrate: number): void {
    // Resolution thresholds (below these bitrates, downscale)
    const thresholds = [
      { tier: 0, minBitrate: 2_000_000 }, // 1080p needs at least 2Mbps
      { tier: 1, minBitrate: 800_000 }, // 720p needs at least 800kbps
      { tier: 2, minBitrate: 400_000 }, // 480p needs at least 400kbps
    ];

    let targetTier = 0;
    for (const t of thresholds) {
      if (bitrate < t.minBitrate && t.tier > targetTier) {
        targetTier = t.tier + 1;
      }
    }

    targetTier = Math.min(targetTier, RESOLUTION_TIERS.length - 1);

    if (targetTier !== this.currentResolutionTier) {
      this.currentResolutionTier = targetTier;
      const res = RESOLUTION_TIERS[targetTier];
      this.encoder
        .updateConfig({
          video: { width: res.width, height: res.height } as any,
        })
        .catch(() => {
          // Ignore
        });
      this.emit("resolutionChanged", res);
    }
  }
}

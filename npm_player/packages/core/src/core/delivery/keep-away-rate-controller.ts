/**
 * Keep-away micro-rate controller (pure).
 *
 * Holds a live stream at a chosen latency behind the edge by nudging playbackRate
 * by ±1% — imperceptible (no audible pitch shift), unlike coarse 1.08x catch-up.
 * It is the generalized "latency compensator" pattern: measure distance from the
 * live edge, steer playback speed back toward a setpoint.
 *
 * MistServer leaves this as a TODO for native progressive MP4 (`liveCatchup` in the
 * embed is unimplemented), so this is our addition. The decision is pure — the
 * caller (a per-player adapter) supplies the measured latency and current rate and
 * applies the returned rate to its element.
 *
 * Hysteresis: once steering, hold the corrective rate until latency crosses back
 * through the setpoint (not merely back inside the deadband). This is the mews.js
 * reference behavior and avoids chatter at the deadband edges.
 */
export interface KeepAwayConfig {
  /** Desired latency behind the live edge, in ms. */
  targetLatencyMs: number;
  /** Half-width (ms) of the deadband around target where rate stays 1.0. */
  deadbandMs: number;
  /** Rate applied when too far behind target (catch up). Slightly > 1. */
  catchUpRate: number;
  /** Rate applied when too close to the edge (rebuild margin). Slightly < 1. */
  rebuildRate: number;
}

export const KEEP_AWAY_DEFAULTS: KeepAwayConfig = {
  targetLatencyMs: 2000,
  deadbandMs: 500,
  catchUpRate: 1.01,
  rebuildRate: 0.99,
};

/** Minimum live target (ms) — floor so low-keepaway streams still keep a safe margin. */
export const KEEP_AWAY_FLOOR_MS = 1000;
/** Multiplier + cushion applied to the reported keepaway to absorb network variance. */
export const KEEP_AWAY_JITTER_K = 1.5;
export const KEEP_AWAY_CUSHION_MS = 500;

/**
 * Derive the live target (ms behind the live edge / read-ahead to hold) from MistServer's
 * reported keepaway (`meta.jitter` = max minKeepAway). Scales with the stream's actual
 * jitter instead of a fixed guess: tiny-jitter streams sit close to live, high-jitter
 * (long-GOP / lossy) streams sit further back. Floored so we never hug the edge.
 */
export function keepAwareTargetMs(
  keepAwayMs: number | undefined,
  floorMs: number = KEEP_AWAY_FLOOR_MS
): number {
  if (!Number.isFinite(keepAwayMs) || (keepAwayMs ?? 0) <= 0) return floorMs;
  return Math.max(floorMs, Math.round(keepAwayMs! * KEEP_AWAY_JITTER_K) + KEEP_AWAY_CUSHION_MS);
}

/**
 * Max keepaway (ms) across the playable A/V tracks, from MistServer track metadata.
 * Excludes meta tracks (their `jitter` is inflated by slow update cadence). Falls back
 * to the stream-level keepaway when no per-track value is present.
 */
export function maxPlayableKeepAwayMs(
  tracks: Record<string, { type?: string; jitter?: number }> | undefined,
  fallbackMs?: number
): number {
  let max = 0;
  if (tracks) {
    for (const t of Object.values(tracks)) {
      if (t.type === "meta") continue;
      if (typeof t.jitter === "number" && t.jitter > max) max = t.jitter;
    }
  }
  if (max <= 0 && typeof fallbackMs === "number" && fallbackMs > 0) max = fallbackMs;
  return max;
}

export interface KeepAwayInput {
  /** Current latency behind the live edge, in ms. */
  currentLatencyMs: number;
  /** Current playbackRate, so we apply hysteresis and avoid redundant sets. */
  currentRate: number;
}

export type KeepAwayDecision = { kind: "hold" } | { kind: "set_rate"; rate: number };

const RATE_EPSILON = 0.001;

/**
 * Decide the playbackRate that steers currentLatency toward targetLatency.
 * Returns `hold` when no change from the current rate is warranted.
 */
export function decideKeepAwayRate(
  input: KeepAwayInput,
  config: KeepAwayConfig = KEEP_AWAY_DEFAULTS
): KeepAwayDecision {
  const { currentLatencyMs, currentRate } = input;
  if (!Number.isFinite(currentLatencyMs)) return { kind: "hold" };

  const { targetLatencyMs, deadbandMs, catchUpRate, rebuildRate } = config;
  const lower = targetLatencyMs - deadbandMs;
  const upper = targetLatencyMs + deadbandMs;

  let desired: number;
  if (currentRate > 1 + RATE_EPSILON) {
    // Catching up: keep speeding until we reach the setpoint, then normalize.
    desired = currentLatencyMs > targetLatencyMs ? catchUpRate : 1.0;
  } else if (currentRate < 1 - RATE_EPSILON) {
    // Rebuilding margin: keep slowing until we reach the setpoint, then normalize.
    desired = currentLatencyMs < targetLatencyMs ? rebuildRate : 1.0;
  } else {
    // At normal speed: only act once latency leaves the deadband.
    desired = currentLatencyMs > upper ? catchUpRate : currentLatencyMs < lower ? rebuildRate : 1.0;
  }

  if (Math.abs(desired - currentRate) < RATE_EPSILON) return { kind: "hold" };
  return { kind: "set_rate", rate: desired };
}

/**
 * Latency Profiles for WebCodecs Player
 *
 * Presets for trading off latency vs stability.
 *
 * Buffer calculation: desiredBuffer = keepAway + serverDelay + (jitter * jitterMultiplier)
 *
 * Speed tweaking:
 *   - If buffer > desired * speedUpThreshold → speed up to maxSpeedUp
 *   - If buffer < desired * speedDownThreshold → slow down to minSpeedDown
 */

import type { LatencyProfile, LatencyProfileName } from "./types";

/**
 * Ultra-low latency profile
 * Target: <200ms end-to-end latency
 * Use case: Real-time conferencing, remote control
 * Trade-offs: May stutter on poor networks
 */
export const ULTRA_LOW_PROFILE: LatencyProfile = {
  name: "Ultra Low Latency",
  keepAway: 50, // 50ms base buffer
  jitterMultiplier: 1.0, // Full jitter protection (no extra margin)
  speedUpThreshold: 1.5, // Speed up when buffer > 150% of desired
  speedDownThreshold: 0.5, // Slow down when buffer < 50% of desired
  maxSpeedUp: 1.08, // 8% speed up max
  minSpeedDown: 0.95, // 5% slow down max
  audioBufferMs: 100, // 100ms audio ring buffer
  optimizeForLatency: true, // Tell decoders to optimize for latency
};

/**
 * Low latency profile
 * Target: ~300-500ms end-to-end latency
 * Use case: Live sports, gaming streams
 * Trade-offs: Balanced latency/stability
 */
export const LOW_PROFILE: LatencyProfile = {
  name: "Low Latency",
  keepAway: 100, // 100ms base buffer
  jitterMultiplier: 1.2, // 20% extra jitter protection
  speedUpThreshold: 2.0, // Speed up when buffer > 200% of desired
  speedDownThreshold: 0.6, // Slow down when buffer < 60% of desired
  maxSpeedUp: 1.05, // 5% speed up max (legacy default)
  minSpeedDown: 0.98, // 2% slow down max (legacy default)
  audioBufferMs: 150, // 150ms audio ring buffer
  optimizeForLatency: true,
};

/**
 * Balanced profile
 * Target: ~500-1000ms end-to-end latency
 * Use case: General live streaming
 * Trade-offs: Prioritizes stability over latency
 */
export const BALANCED_PROFILE: LatencyProfile = {
  name: "Balanced",
  keepAway: 200, // 200ms base buffer
  jitterMultiplier: 1.5, // 50% extra jitter protection
  speedUpThreshold: 2.5, // Speed up when buffer > 250% of desired
  speedDownThreshold: 0.5, // Slow down when buffer < 50% of desired
  maxSpeedUp: 1.03, // 3% speed up max
  minSpeedDown: 0.97, // 3% slow down max
  audioBufferMs: 200, // 200ms audio ring buffer
  optimizeForLatency: false, // Let decoders optimize for quality
};

/**
 * Quality priority profile
 * Target: ~1-2s end-to-end latency
 * Use case: VOD, recorded content, poor networks
 * Trade-offs: Maximum stability, higher latency
 */
export const QUALITY_PROFILE: LatencyProfile = {
  name: "Quality Priority",
  keepAway: 500, // 500ms base buffer (legacy VOD default)
  jitterMultiplier: 2.0, // Double jitter protection
  speedUpThreshold: 3.0, // Speed up when buffer > 300% of desired
  speedDownThreshold: 0.4, // Slow down when buffer < 40% of desired
  maxSpeedUp: 1.02, // 2% speed up max
  minSpeedDown: 0.98, // 2% slow down max
  audioBufferMs: 300, // 300ms audio ring buffer
  optimizeForLatency: false,
};

/**
 * All available latency profiles
 */
export const LATENCY_PROFILES: Record<LatencyProfileName, LatencyProfile> = {
  "ultra-low": ULTRA_LOW_PROFILE,
  low: LOW_PROFILE,
  balanced: BALANCED_PROFILE,
  quality: QUALITY_PROFILE,
};

/**
 * Get a latency profile by name
 * @param name - Profile name
 * @returns The profile, or 'low' as default
 */
export function getLatencyProfile(name?: LatencyProfileName): LatencyProfile {
  if (name && name in LATENCY_PROFILES) {
    return LATENCY_PROFILES[name];
  }
  return LATENCY_PROFILES["low"];
}

/**
 * Merge a custom partial profile with a base profile
 * @param base - Base profile name or profile object
 * @param custom - Partial overrides
 * @returns Merged profile
 */
export function mergeLatencyProfile(
  base: LatencyProfileName | LatencyProfile,
  custom?: Partial<LatencyProfile>
): LatencyProfile {
  const baseProfile = typeof base === "string" ? getLatencyProfile(base) : base;

  if (!custom) {
    return baseProfile;
  }

  return {
    ...baseProfile,
    ...custom,
    name: custom.name ?? `${baseProfile.name} (Custom)`,
  };
}

/**
 * Select appropriate profile based on stream type
 * @param isLive - Whether the stream is live
 * @param preferLowLatency - Whether to prefer low latency (e.g., WebRTC source)
 * @returns Recommended profile name
 */
export function selectDefaultProfile(
  isLive: boolean,
  preferLowLatency = false
): LatencyProfileName {
  if (!isLive) {
    return "quality";
  }

  if (preferLowLatency) {
    return "low";
  }

  return "balanced";
}

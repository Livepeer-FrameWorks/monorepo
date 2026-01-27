/**
 * Transition Engine
 *
 * Manages smooth transitions between scenes with configurable easing.
 *
 * Supported transition types:
 * - cut: Instant switch (no animation)
 * - fade: Cross-dissolve between scenes
 * - slide-left/right/up/down: Sliding panel transition
 *
 * Easing functions:
 * - linear: Constant speed
 * - ease-in: Start slow, end fast
 * - ease-out: Start fast, end slow
 * - ease-in-out: Start slow, accelerate, end slow
 */

import type { TransitionConfig, TransitionState, TransitionType, EasingType } from "../types";

// ============================================================================
// Easing Functions
// ============================================================================

type EasingFunction = (t: number) => number;

/**
 * Standard easing functions based on Robert Penner's easing equations
 */
const EASING_FUNCTIONS: Record<EasingType, EasingFunction> = {
  linear: (t) => t,

  "ease-in": (t) => t * t,

  "ease-out": (t) => t * (2 - t),

  "ease-in-out": (t) => (t < 0.5 ? 2 * t * t : -1 + (4 - 2 * t) * t),
};

// ============================================================================
// TransitionEngine Class
// ============================================================================

/**
 * Engine for managing scene transitions with timing and easing
 */
export class TransitionEngine {
  private state: TransitionState | null = null;
  private easing: EasingFunction = EASING_FUNCTIONS.linear;

  /**
   * Start a new transition between scenes
   *
   * @param fromSceneId - ID of the scene being transitioned from
   * @param toSceneId - ID of the scene being transitioned to
   * @param config - Transition configuration (type, duration, easing)
   */
  start(fromSceneId: string, toSceneId: string, config: TransitionConfig): void {
    this.state = {
      active: true,
      type: config.type,
      progress: 0,
      fromSceneId,
      toSceneId,
      startTime: performance.now(),
      durationMs: config.durationMs,
    };

    this.easing = EASING_FUNCTIONS[config.easing] || EASING_FUNCTIONS.linear;

    // For 'cut' transitions, immediately complete
    if (config.type === "cut") {
      this.state.progress = 1;
      this.state.active = false;
    }
  }

  /**
   * Update transition progress based on elapsed time
   * Call this every frame during a transition
   *
   * @returns Current transition state, or null if no transition is active
   */
  update(): TransitionState | null {
    if (!this.state?.active) {
      return null;
    }

    const elapsed = performance.now() - this.state.startTime;
    const rawProgress = Math.min(1, elapsed / this.state.durationMs);

    // Apply easing to get smooth progress
    this.state.progress = this.easing(rawProgress);

    // Check if transition is complete
    if (rawProgress >= 1) {
      this.state.active = false;
      this.state.progress = 1; // Ensure we end at exactly 1
    }

    return this.state;
  }

  /**
   * Check if a transition is currently active
   */
  isActive(): boolean {
    return this.state?.active ?? false;
  }

  /**
   * Get the current transition progress (0-1, with easing applied)
   */
  getProgress(): number {
    return this.state?.progress ?? 0;
  }

  /**
   * Get the raw linear progress (0-1, without easing)
   */
  getRawProgress(): number {
    if (!this.state?.active) {
      return this.state?.progress ?? 0;
    }

    const elapsed = performance.now() - this.state.startTime;
    return Math.min(1, elapsed / this.state.durationMs);
  }

  /**
   * Get the current transition type
   */
  getType(): TransitionType {
    return this.state?.type ?? "cut";
  }

  /**
   * Get the source scene ID (scene being transitioned from)
   */
  getFromSceneId(): string | null {
    return this.state?.fromSceneId ?? null;
  }

  /**
   * Get the target scene ID (scene being transitioned to)
   */
  getToSceneId(): string | null {
    return this.state?.toSceneId ?? null;
  }

  /**
   * Get remaining time in milliseconds
   */
  getRemainingTime(): number {
    if (!this.state?.active) {
      return 0;
    }

    const elapsed = performance.now() - this.state.startTime;
    return Math.max(0, this.state.durationMs - elapsed);
  }

  /**
   * Get the full transition state (for serialization/debugging)
   */
  getState(): TransitionState | null {
    return this.state ? { ...this.state } : null;
  }

  /**
   * Cancel the current transition
   * The transition will stop at its current progress
   */
  cancel(): void {
    if (this.state) {
      this.state.active = false;
    }
  }

  /**
   * Force-complete the current transition
   * Jumps to the end state immediately
   */
  complete(): void {
    if (this.state) {
      this.state.active = false;
      this.state.progress = 1;
    }
  }

  /**
   * Reset the engine, clearing any transition state
   */
  reset(): void {
    this.state = null;
    this.easing = EASING_FUNCTIONS.linear;
  }
}

// ============================================================================
// Factory Functions
// ============================================================================

/**
 * Create a default transition configuration
 */
export function createDefaultTransitionConfig(): TransitionConfig {
  return {
    type: "fade",
    durationMs: 500,
    easing: "ease-in-out",
  };
}

/**
 * Create a cut (instant) transition configuration
 */
export function createCutTransition(): TransitionConfig {
  return {
    type: "cut",
    durationMs: 0,
    easing: "linear",
  };
}

/**
 * Create a fade transition configuration
 */
export function createFadeTransition(
  durationMs: number = 500,
  easing: EasingType = "ease-in-out"
): TransitionConfig {
  return {
    type: "fade",
    durationMs,
    easing,
  };
}

/**
 * Create a slide transition configuration
 */
export function createSlideTransition(
  direction: "left" | "right" | "up" | "down" = "left",
  durationMs: number = 500,
  easing: EasingType = "ease-in-out"
): TransitionConfig {
  return {
    type: `slide-${direction}` as TransitionType,
    durationMs,
    easing,
  };
}

// ============================================================================
// Utility Functions
// ============================================================================

/**
 * Get all available transition types
 */
export function getAvailableTransitionTypes(): TransitionType[] {
  return ["cut", "fade", "slide-left", "slide-right", "slide-up", "slide-down"];
}

/**
 * Get all available easing types
 */
export function getAvailableEasingTypes(): EasingType[] {
  return ["linear", "ease-in", "ease-out", "ease-in-out"];
}

/**
 * Validate a transition configuration
 */
export function validateTransitionConfig(config: Partial<TransitionConfig>): TransitionConfig {
  return {
    type: config.type || "fade",
    durationMs: Math.max(0, config.durationMs ?? 500),
    easing: config.easing || "ease-in-out",
  };
}

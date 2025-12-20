/**
 * InteractionController - Unified keyboard and gesture handling for video players
 *
 * Features:
 * - Hold space for 2x speed (VOD/clips only, tap = play/pause)
 * - Click/touch and hold for 2x speed
 * - Comprehensive keyboard shortcuts
 * - Double-tap to skip on mobile
 * - All interactions disabled for live streams (where applicable)
 */

export interface InteractionControllerConfig {
  container: HTMLElement;
  videoElement: HTMLVideoElement;
  isLive: boolean;
  isPaused?: () => boolean;
  onPlayPause: () => void;
  onSeek: (delta: number) => void;
  onVolumeChange: (delta: number) => void;
  onMuteToggle: () => void;
  onFullscreenToggle: () => void;
  onCaptionsToggle?: () => void;
  onLoopToggle?: () => void;
  onSpeedChange: (speed: number, isHolding: boolean) => void;
  onSeekPercent?: (percent: number) => void;
  /** Optional: player-specific frame stepping (return true if handled) */
  onFrameStep?: (direction: -1 | 1, seconds: number) => boolean | void;
  speedHoldValue?: number;
  /** Frame step duration in seconds (for prev/next frame shortcuts) */
  frameStepSeconds?: number;
  /** Idle timeout in ms (default 5000). Set to 0 to disable. */
  idleTimeout?: number;
  /** Callback fired when user becomes idle */
  onIdle?: () => void;
  /** Callback fired when user becomes active after being idle */
  onActive?: () => void;
}

export interface InteractionState {
  isHoldingSpeed: boolean;
  previousSpeed: number;
  holdSpeed: number;
  /** Whether the user is currently idle (no interaction for idleTimeout) */
  isIdle: boolean;
}

// Timing constants
const HOLD_THRESHOLD_MS = 200;       // Time before keydown becomes "hold" vs "tap"
const LONG_PRESS_THRESHOLD_MS = 300; // Time for touch/click to become "hold"
const DOUBLE_TAP_WINDOW_MS = 300;    // Window for detecting double-tap
const SKIP_AMOUNT_SECONDS = 10;      // Skip forward/backward amount
const VOLUME_STEP = 0.1;             // Volume change per arrow press (10%)
const DEFAULT_IDLE_TIMEOUT_MS = 5000; // Default idle timeout (5 seconds)

export class InteractionController {
  private config: InteractionControllerConfig;
  private state: InteractionState;
  private isAttached = false;

  // Keyboard tracking
  private spaceKeyDownTime = 0;
  private spaceIsHeld = false;
  private holdCheckTimeout: ReturnType<typeof setTimeout> | null = null;

  // Touch/click tracking
  private pointerDownTime = 0;
  private pointerIsHeld = false;
  private pointerHoldTimeout: ReturnType<typeof setTimeout> | null = null;
  private lastTapTime = 0;
  private lastTapX = 0;
  private pendingTapTimeout: ReturnType<typeof setTimeout> | null = null;

  // Idle tracking
  private idleTimeout: ReturnType<typeof setTimeout> | null = null;
  private lastInteractionTime = 0;

  // Bound event handlers
  private boundKeyDown: (e: KeyboardEvent) => void;
  private boundKeyUp: (e: KeyboardEvent) => void;
  private boundPointerDown: (e: PointerEvent) => void;
  private boundPointerUp: (e: PointerEvent) => void;
  private boundPointerCancel: (e: PointerEvent) => void;
  private boundContextMenu: (e: Event) => void;
  private boundMouseMove: (e: MouseEvent) => void;
  private boundDoubleClick: (e: MouseEvent) => void;
  private boundDocumentKeyDown: (e: KeyboardEvent) => void;
  private boundDocumentKeyUp: (e: KeyboardEvent) => void;

  constructor(config: InteractionControllerConfig) {
    this.config = config;
    this.state = {
      isHoldingSpeed: false,
      previousSpeed: 1,
      holdSpeed: config.speedHoldValue ?? 2,
      isIdle: false,
    };

    // Bind handlers
    this.boundKeyDown = this.handleKeyDown.bind(this);
    this.boundKeyUp = this.handleKeyUp.bind(this);
    this.boundPointerDown = this.handlePointerDown.bind(this);
    this.boundPointerUp = this.handlePointerUp.bind(this);
    this.boundPointerCancel = this.handlePointerCancel.bind(this);
    this.boundContextMenu = this.handleContextMenu.bind(this);
    this.boundMouseMove = this.handleMouseMove.bind(this);
    this.boundDoubleClick = this.handleDoubleClick.bind(this);
    this.boundDocumentKeyDown = this.handleKeyDown.bind(this);
    this.boundDocumentKeyUp = this.handleKeyUp.bind(this);
  }

  /**
   * Attach event listeners to container
   */
  attach(): void {
    if (this.isAttached) return;

    const { container } = this.config;

    // Make container focusable for keyboard events
    if (!container.hasAttribute('tabindex')) {
      container.setAttribute('tabindex', '0');
    }

    // Keyboard events
    container.addEventListener('keydown', this.boundKeyDown);
    container.addEventListener('keyup', this.boundKeyUp);
    document.addEventListener('keydown', this.boundDocumentKeyDown);
    document.addEventListener('keyup', this.boundDocumentKeyUp);

    // Pointer events (unified mouse + touch)
    container.addEventListener('pointerdown', this.boundPointerDown);
    container.addEventListener('pointerup', this.boundPointerUp);
    container.addEventListener('pointercancel', this.boundPointerCancel);
    container.addEventListener('pointerleave', this.boundPointerCancel);

    // Mouse move for idle detection
    container.addEventListener('mousemove', this.boundMouseMove);

    // Double click for fullscreen (desktop)
    container.addEventListener('dblclick', this.boundDoubleClick);

    // Prevent context menu on long press
    container.addEventListener('contextmenu', this.boundContextMenu);

    // Start idle tracking
    this.resetIdleTimer();

    this.isAttached = true;
  }

  /**
   * Detach event listeners and cleanup
   */
  detach(): void {
    if (!this.isAttached) return;

    const { container } = this.config;

    container.removeEventListener('keydown', this.boundKeyDown);
    container.removeEventListener('keyup', this.boundKeyUp);
    document.removeEventListener('keydown', this.boundDocumentKeyDown);
    document.removeEventListener('keyup', this.boundDocumentKeyUp);
    container.removeEventListener('pointerdown', this.boundPointerDown);
    container.removeEventListener('pointerup', this.boundPointerUp);
    container.removeEventListener('pointercancel', this.boundPointerCancel);
    container.removeEventListener('pointerleave', this.boundPointerCancel);
    container.removeEventListener('mousemove', this.boundMouseMove);
    container.removeEventListener('dblclick', this.boundDoubleClick);
    container.removeEventListener('contextmenu', this.boundContextMenu);

    // Clear any pending timeouts
    if (this.holdCheckTimeout) {
      clearTimeout(this.holdCheckTimeout);
      this.holdCheckTimeout = null;
    }
    if (this.pointerHoldTimeout) {
      clearTimeout(this.pointerHoldTimeout);
      this.pointerHoldTimeout = null;
    }
    if (this.pendingTapTimeout) {
      clearTimeout(this.pendingTapTimeout);
      this.pendingTapTimeout = null;
    }
    if (this.idleTimeout) {
      clearTimeout(this.idleTimeout);
      this.idleTimeout = null;
    }

    // Restore speed if holding
    if (this.state.isHoldingSpeed) {
      this.releaseSpeedHold();
    }

    this.isAttached = false;
  }

  /**
   * Check if currently holding for speed boost
   */
  isHoldingSpeed(): boolean {
    return this.state.isHoldingSpeed;
  }

  /**
   * Check if user is currently idle (no interaction for idleTimeout)
   */
  isIdle(): boolean {
    return this.state.isIdle;
  }

  /**
   * Get current interaction state
   */
  getState(): InteractionState {
    return { ...this.state };
  }

  /**
   * Update config (e.g., when isLive changes)
   */
  updateConfig(updates: Partial<InteractionControllerConfig>): void {
    this.config = { ...this.config, ...updates };

    // If we switched to live mode while holding, release
    if (updates.isLive && this.state.isHoldingSpeed) {
      this.releaseSpeedHold();
    }
  }

  // ─────────────────────────────────────────────────────────────────
  // Keyboard Handling
  // ─────────────────────────────────────────────────────────────────

  private handleKeyDown(e: KeyboardEvent): void {
    // Ignore if focus is on an input element
    if (this.isInputElement(e.target)) return;
    if (e.defaultPrevented) return;
    if (!this.shouldHandleKeyboard(e)) return;

    // Record interaction for idle detection
    this.recordInteraction();

    const { isLive } = this.config;
    const isPaused = this.config.isPaused?.() ?? this.config.videoElement?.paused ?? false;

    switch (e.key) {
      case ' ':
      case 'Spacebar':
        e.preventDefault();
        this.handleSpaceDown();
        break;

      case 'ArrowLeft':
      case 'j':
      case 'J':
        e.preventDefault();
        if (!isLive) {
          this.config.onSeek(-SKIP_AMOUNT_SECONDS);
        }
        break;

      case 'ArrowRight':
      case 'l':
      case 'L':
        e.preventDefault();
        if (!isLive) {
          this.config.onSeek(SKIP_AMOUNT_SECONDS);
        }
        break;

      case 'ArrowUp':
        e.preventDefault();
        this.config.onVolumeChange(VOLUME_STEP);
        break;

      case 'ArrowDown':
        e.preventDefault();
        this.config.onVolumeChange(-VOLUME_STEP);
        break;

      case 'm':
      case 'M':
        e.preventDefault();
        this.config.onMuteToggle();
        break;

      case 'f':
      case 'F':
        e.preventDefault();
        this.config.onFullscreenToggle();
        break;

      case 'c':
      case 'C':
        e.preventDefault();
        this.config.onCaptionsToggle?.();
        break;

      case 'k':
      case 'K':
        // YouTube-style: K = play/pause (no hold behavior)
        e.preventDefault();
        this.config.onPlayPause();
        break;

      case '<':
        // Decrease speed (shift+, = <)
        e.preventDefault();
        if (!isLive) {
          this.adjustPlaybackSpeed(-0.25);
        }
        break;

      case '>':
        // Increase speed (shift+. = >)
        e.preventDefault();
        if (!isLive) {
          this.adjustPlaybackSpeed(0.25);
        }
        break;

      case ',':
        // Previous frame when paused
        if (this.config.onFrameStep || (!isLive && isPaused)) {
          e.preventDefault();
          this.stepFrame(-1);
        }
        break;

      case '.':
        // Next frame when paused
        if (this.config.onFrameStep || (!isLive && isPaused)) {
          e.preventDefault();
          this.stepFrame(1);
        }
        break;

      // Number keys for seeking to percentage
      case '0':
      case '1':
      case '2':
      case '3':
      case '4':
      case '5':
      case '6':
      case '7':
      case '8':
      case '9':
        e.preventDefault();
        if (!isLive && this.config.onSeekPercent) {
          const percent = parseInt(e.key, 10) / 10;
          this.config.onSeekPercent(percent);
        }
        break;
    }
  }

  private handleKeyUp(e: KeyboardEvent): void {
    if (this.isInputElement(e.target)) return;
    if (e.defaultPrevented) return;
    if (!this.shouldHandleKeyboard(e)) return;

    if (e.key === ' ' || e.key === 'Spacebar') {
      e.preventDefault();
      this.handleSpaceUp();
    }
  }

  private shouldHandleKeyboard(e: KeyboardEvent): boolean {
    if (this.spaceKeyDownTime > 0) return true;
    const target = e.target as HTMLElement | null;
    if (target && this.config.container.contains(target)) return true;
    const active = document.activeElement as HTMLElement | null;
    if (active && this.config.container.contains(active)) return true;
    try {
      if (this.config.container.matches(':focus-within')) return true;
      if (this.config.container.matches(':hover')) return true;
    } catch {}
    const now = Date.now();
    if (now - this.lastInteractionTime < DEFAULT_IDLE_TIMEOUT_MS) return true;
    return false;
  }

  private handleSpaceDown(): void {
    if (this.spaceKeyDownTime > 0) return; // Already tracking

    this.spaceKeyDownTime = Date.now();
    this.spaceIsHeld = false;

    // Only enable hold-for-speed on VOD/clips
    if (!this.config.isLive) {
      this.holdCheckTimeout = setTimeout(() => {
        this.spaceIsHeld = true;
        this.engageSpeedHold();
      }, HOLD_THRESHOLD_MS);
    }
  }

  private handleSpaceUp(): void {
    const downTime = this.spaceKeyDownTime;
    this.spaceKeyDownTime = 0;

    if (this.holdCheckTimeout) {
      clearTimeout(this.holdCheckTimeout);
      this.holdCheckTimeout = null;
    }

    if (this.spaceIsHeld) {
      // Was holding - release speed boost
      this.releaseSpeedHold();
      this.spaceIsHeld = false;
    } else {
      // Was a quick tap - toggle play/pause
      const elapsed = Date.now() - downTime;
      if (elapsed < HOLD_THRESHOLD_MS || this.config.isLive) {
        this.config.onPlayPause();
      }
    }
  }

  private handleDoubleClick(e: MouseEvent): void {
    if (this.isControlElement(e.target)) return;
    this.recordInteraction();
    e.preventDefault();
    this.config.onFullscreenToggle();
  }

  private stepFrame(direction: -1 | 1): void {
    const step = this.getFrameStepSeconds();
    if (!Number.isFinite(step) || step <= 0) return;
    if (this.config.onFrameStep?.(direction, step)) return;
    const video = this.config.videoElement;
    if (!video) return;

    const target = video.currentTime + (direction * step);
    if (!Number.isFinite(target)) return;

    // Only step within already-buffered ranges to avoid network seeks
    const buffered = video.buffered;
    if (buffered && buffered.length > 0) {
      for (let i = 0; i < buffered.length; i++) {
        const start = buffered.start(i);
        const end = buffered.end(i);
        if (target >= start && target <= end) {
          try { video.currentTime = target; } catch {}
          return;
        }
      }
    }
  }

  // ─────────────────────────────────────────────────────────────────
  // Pointer (Mouse/Touch) Handling
  // ─────────────────────────────────────────────────────────────────

  private handlePointerDown(e: PointerEvent): void {
    // Only handle primary button / single touch on the video area
    if (e.button !== 0) return;
    if (this.isControlElement(e.target)) return;

    // Record interaction for idle detection
    this.recordInteraction();

    // Ensure container has focus for keyboard events
    this.config.container.focus();

    const now = Date.now();
    const rect = this.config.container.getBoundingClientRect();
    const relativeX = (e.clientX - rect.left) / rect.width;
    const isMouse = e.pointerType === 'mouse';

    // Check for double-tap
    if (now - this.lastTapTime < DOUBLE_TAP_WINDOW_MS) {
      // Clear pending single-tap
      if (this.pendingTapTimeout) {
        clearTimeout(this.pendingTapTimeout);
        this.pendingTapTimeout = null;
      }

      // Mouse double-click handled via dblclick event (fullscreen)
      if (!isMouse) {
        // Handle double-tap to skip (mobile-style)
        if (!this.config.isLive) {
          if (relativeX < 0.33) {
            // Left third - skip back
            this.config.onSeek(-SKIP_AMOUNT_SECONDS);
          } else if (relativeX > 0.67) {
            // Right third - skip forward
            this.config.onSeek(SKIP_AMOUNT_SECONDS);
          } else {
            // Center - treat as play/pause
            this.config.onPlayPause();
          }
        }
      }

      this.lastTapTime = 0;
      return;
    }

    this.lastTapTime = now;
    this.lastTapX = relativeX;
    this.pointerDownTime = now;
    this.pointerIsHeld = false;

    // Start long-press detection for 2x speed (VOD only)
    if (!this.config.isLive) {
      this.pointerHoldTimeout = setTimeout(() => {
        this.pointerIsHeld = true;
        this.engageSpeedHold();
      }, LONG_PRESS_THRESHOLD_MS);
    }
  }

  private handlePointerUp(e: PointerEvent): void {
    if (e.button !== 0) return;

    const wasHeld = this.pointerIsHeld;
    this.cancelPointerHold();

    if (wasHeld) {
      // Was long-pressing - just release speed
      this.releaseSpeedHold();
    } else if (this.pointerDownTime > 0) {
      // Was a quick tap - delay to check for double-tap
      this.pendingTapTimeout = setTimeout(() => {
        this.pendingTapTimeout = null;
        this.config.onPlayPause();
      }, DOUBLE_TAP_WINDOW_MS);
    }

    this.pointerDownTime = 0;
  }

  private handlePointerCancel(_e: PointerEvent): void {
    if (this.pointerIsHeld) {
      this.releaseSpeedHold();
    }
    this.cancelPointerHold();
    this.pointerDownTime = 0;
  }

  private cancelPointerHold(): void {
    if (this.pointerHoldTimeout) {
      clearTimeout(this.pointerHoldTimeout);
      this.pointerHoldTimeout = null;
    }
    this.pointerIsHeld = false;
  }

  private handleContextMenu(e: Event): void {
    // Prevent context menu during long-press
    if (this.pointerIsHeld || this.pointerDownTime > 0) {
      e.preventDefault();
    }
  }

  // ─────────────────────────────────────────────────────────────────
  // Speed Hold Logic
  // ─────────────────────────────────────────────────────────────────

  private engageSpeedHold(): void {
    if (this.state.isHoldingSpeed) return;
    if (this.config.isLive) return;

    // Save current speed
    this.state.previousSpeed = this.config.videoElement.playbackRate;
    this.state.isHoldingSpeed = true;

    // Apply hold speed
    this.config.onSpeedChange(this.state.holdSpeed, true);
  }

  private releaseSpeedHold(): void {
    if (!this.state.isHoldingSpeed) return;

    this.state.isHoldingSpeed = false;

    // Restore previous speed
    this.config.onSpeedChange(this.state.previousSpeed, false);
  }

  private adjustPlaybackSpeed(delta: number): void {
    if (this.state.isHoldingSpeed) return;

    const currentSpeed = this.config.videoElement.playbackRate;
    const newSpeed = Math.max(0.25, Math.min(4, currentSpeed + delta));

    // Round to avoid floating point issues
    const roundedSpeed = Math.round(newSpeed * 100) / 100;

    this.config.onSpeedChange(roundedSpeed, false);
  }

  // ─────────────────────────────────────────────────────────────────
  // Idle Detection
  // ─────────────────────────────────────────────────────────────────

  private handleMouseMove(_e: MouseEvent): void {
    this.recordInteraction();
  }

  /**
   * Record that an interaction occurred and reset idle timer
   */
  recordInteraction(): void {
    this.lastInteractionTime = Date.now();

    // If was idle, become active
    if (this.state.isIdle) {
      this.state.isIdle = false;
      this.config.onActive?.();
    }

    // Reset idle timer
    this.resetIdleTimer();
  }

  /**
   * Reset the idle timer
   */
  private resetIdleTimer(): void {
    // Clear existing timer
    if (this.idleTimeout) {
      clearTimeout(this.idleTimeout);
      this.idleTimeout = null;
    }

    // Get timeout value (0 means disabled)
    const timeout = this.config.idleTimeout ?? DEFAULT_IDLE_TIMEOUT_MS;
    if (timeout <= 0) return;

    // Set new timer
    this.idleTimeout = setTimeout(() => {
      this.idleTimeout = null;
      if (!this.state.isIdle) {
        this.state.isIdle = true;
        this.config.onIdle?.();
      }
    }, timeout);
  }

  /**
   * Manually mark as active (e.g., when controls become visible)
   */
  markActive(): void {
    this.recordInteraction();
  }

  /**
   * Pause idle tracking (e.g., when controls are visible)
   */
  pauseIdleTracking(): void {
    if (this.idleTimeout) {
      clearTimeout(this.idleTimeout);
      this.idleTimeout = null;
    }
  }

  /**
   * Resume idle tracking
   */
  resumeIdleTracking(): void {
    if (this.isAttached) {
      this.resetIdleTimer();
    }
  }

  // ─────────────────────────────────────────────────────────────────
  // Utilities
  // ─────────────────────────────────────────────────────────────────

  private isInputElement(target: EventTarget | null): boolean {
    if (!target || !(target instanceof HTMLElement)) return false;
    const tagName = target.tagName.toLowerCase();
    return tagName === 'input' || tagName === 'textarea' || tagName === 'select' || target.isContentEditable;
  }

  private isControlElement(target: EventTarget | null): boolean {
    if (!target || !(target instanceof HTMLElement)) return false;

    // Check if clicking on player controls (buttons, sliders, etc.)
    const controlSelectors = [
      'button',
      '[role="button"]',
      '[role="slider"]',
      'input',
      'select',
      '.fw-player-controls',
      '[data-player-controls]',
      '.fw-controls-wrapper',
      '.fw-control-bar',
      '.fw-settings-menu',
      '.fw-context-menu',
      '.fw-stats-panel',
      '.fw-dev-panel',
      '.fw-error-overlay',
      '.fw-error-popup',
      '.fw-player-error',
    ];

    return controlSelectors.some(selector => {
      return target.matches(selector) || target.closest(selector) !== null;
    });
  }

  private getFrameStepSeconds(): number {
    const step = this.config.frameStepSeconds;
    if (Number.isFinite(step) && (step as number) > 0) return step as number;
    return 1 / 30;
  }
}

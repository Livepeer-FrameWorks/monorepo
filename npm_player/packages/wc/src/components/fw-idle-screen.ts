import { LitElement, html, css, nothing } from "lit";
import { customElement, property, query, state } from "lit/decorators.js";
import { sharedStyles } from "../styles/shared-styles.js";
import { utilityStyles } from "../styles/utility-styles.js";
import { LOGOMARK_DATA_URL } from "../constants/media-assets.js";
import { playHitmarkerSound } from "./shared/hitmarker-audio.js";
import { createTranslator, type TranslateFn } from "@livepeer-frameworks/player-core";
import "./fw-dvd-logo.js";

interface ParticleState {
  left: number;
  size: number;
  color: string;
  duration: number;
  delay: number;
}

interface BubbleState {
  top: number;
  left: number;
  size: number;
  opacity: number;
  color: string;
}

interface Hitmarker {
  id: number;
  x: number;
  y: number;
}

const BUBBLE_COLORS = [
  "rgba(122, 162, 247, 0.2)",
  "rgba(187, 154, 247, 0.2)",
  "rgba(158, 206, 106, 0.2)",
  "rgba(115, 218, 202, 0.2)",
  "rgba(125, 207, 255, 0.2)",
  "rgba(247, 118, 142, 0.2)",
  "rgba(224, 175, 104, 0.2)",
  "rgba(42, 195, 222, 0.2)",
];

const PARTICLE_COLORS = [
  "#7aa2f7",
  "#bb9af7",
  "#9ece6a",
  "#73daca",
  "#7dcfff",
  "#f7768e",
  "#e0af68",
  "#2ac3de",
];

@customElement("fw-idle-screen")
export class FwIdleScreen extends LitElement {
  @property({ type: String }) status?: string;
  @property({ type: String }) message?: string;
  @property({ type: Number }) percentage?: number;
  @property({ type: String }) error?: string;
  @property({ type: String, attribute: "logo-src" }) logoSrc?: string;
  @property({ type: Boolean, attribute: "retry-enabled" }) retryEnabled = false;
  @property({ attribute: false }) onRetry?: () => void;
  @property({ attribute: false }) translator?: TranslateFn;

  private _defaultTranslator: TranslateFn = createTranslator({ locale: "en" });

  private get _t(): TranslateFn {
    return this.translator ?? this._defaultTranslator;
  }
  @query(".idle-container") private _containerEl?: HTMLDivElement;

  @state() private _logoSize = 100;
  @state() private _logoOffset = { x: 0, y: 0 };
  @state() private _isLogoHovered = false;
  @state() private _bubbles: BubbleState[] = this._createBubbles();
  @state() private _hitmarkers: Hitmarker[] = [];

  private readonly _particles: ParticleState[] = this._createParticles();
  private _bubbleTimers = new Set<ReturnType<typeof setTimeout>>();
  private _resizeObserver?: ResizeObserver;

  static styles = [
    sharedStyles,
    utilityStyles,
    css`
      :host {
        display: contents;
      }
      .idle-container {
        position: absolute;
        inset: 0;
        z-index: 5;
        background: linear-gradient(
          135deg,
          hsl(var(--tn-bg-dark, 235 21% 11%)) 0%,
          hsl(var(--tn-bg, 233 23% 17%)) 25%,
          hsl(var(--tn-bg-dark, 235 21% 11%)) 50%,
          hsl(var(--tn-bg, 233 23% 17%)) 75%,
          hsl(var(--tn-bg-dark, 235 21% 11%)) 100%
        );
        background-size: 400% 400%;
        animation: _fw-gradient-shift 16s ease-in-out infinite;
        display: flex;
        flex-direction: column;
        align-items: center;
        justify-content: center;
        overflow: hidden;
        user-select: none;
        -webkit-user-select: none;
      }

      .particles,
      .bubbles {
        position: absolute;
        inset: 0;
        pointer-events: none;
      }

      .particle {
        position: absolute;
        border-radius: 50%;
        opacity: 0;
        animation: _fw-float-up linear infinite;
      }

      .bubble {
        position: absolute;
        border-radius: 50%;
        transition: opacity 1s ease-in-out;
      }

      .center-logo {
        position: absolute;
        top: 50%;
        left: 50%;
        display: flex;
        align-items: center;
        justify-content: center;
        z-index: 10;
        transition: transform 0.3s ease-out;
      }

      .logo-pulse {
        position: absolute;
        border-radius: 50%;
        background: rgba(122, 162, 247, 0.15);
        animation: _fw-logo-pulse 3s ease-in-out infinite;
        pointer-events: none;
        transition: transform 0.3s ease-out;
      }

      .logo-pulse.hovered {
        animation: _fw-logo-pulse 1s ease-in-out infinite;
        transform: scale(1.2);
      }

      .logo-button {
        all: unset;
        cursor: pointer;
        display: block;
      }

      .logo-image {
        position: relative;
        z-index: 1;
        display: block;
        filter: drop-shadow(0 4px 8px rgb(36 40 59 / 0.3));
        transition: all 0.3s ease-out;
        cursor: default;
        user-select: none;
        -webkit-user-drag: none;
      }

      .logo-image.hovered {
        transform: scale(1.1);
        filter: drop-shadow(0 6px 12px rgb(36 40 59 / 0.4)) brightness(1.1);
      }

      .status-overlay {
        position: absolute;
        bottom: 16px;
        left: 50%;
        transform: translateX(-50%);
        z-index: 20;
        display: flex;
        flex-direction: column;
        align-items: center;
        gap: 8px;
        max-width: 280px;
        text-align: center;
        font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
      }

      .status-indicator {
        display: flex;
        align-items: center;
        gap: 8px;
        color: #787c99;
        font-size: 13px;
      }

      .status-icon {
        width: 20px;
        height: 20px;
        flex: 0 0 auto;
      }

      .status-icon.spinning {
        animation: _fw-spin 1s linear infinite;
      }

      .progress-bar {
        width: 160px;
        height: 4px;
        background: rgb(65 72 104 / 0.4);
        border-radius: 2px;
        overflow: hidden;
      }

      .progress-fill {
        height: 100%;
        background: hsl(var(--tn-cyan, 193 100% 75%));
        transition: width 0.3s ease-out;
      }

      .retry-btn {
        padding: 6px 16px;
        background: transparent;
        border: 1px solid rgb(122 162 247 / 0.4);
        border-radius: 4px;
        color: #7aa2f7;
        font-size: 11px;
        font-weight: 500;
        cursor: pointer;
        transition: all 0.2s ease;
        font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
      }

      .retry-btn:hover {
        background: rgb(122 162 247 / 0.1);
      }

      .overlay-texture {
        position: absolute;
        inset: 0;
        background:
          radial-gradient(circle at 20% 80%, rgb(122 162 247 / 0.03) 0%, transparent 50%),
          radial-gradient(circle at 80% 20%, rgb(187 154 247 / 0.03) 0%, transparent 50%),
          radial-gradient(circle at 40% 40%, rgb(158 206 106 / 0.02) 0%, transparent 50%);
        pointer-events: none;
      }

      .hitmarker {
        position: absolute;
        transform: translate(-50%, -50%);
        pointer-events: none;
        z-index: 100;
        width: 40px;
        height: 40px;
      }

      .hitmarker-line {
        position: absolute;
        width: 12px;
        height: 3px;
        background-color: #fff;
        box-shadow: 0 0 8px rgb(255 255 255 / 0.8);
        border-radius: 1px;
      }

      .hitmarker-line.tl {
        top: 25%;
        left: 25%;
        animation: _fw-hitmarker-fade-45 0.6s ease-out forwards;
      }

      .hitmarker-line.tr {
        top: 25%;
        left: 75%;
        animation: _fw-hitmarker-fade-neg-45 0.6s ease-out forwards;
      }

      .hitmarker-line.bl {
        top: 75%;
        left: 25%;
        animation: _fw-hitmarker-fade-neg-45 0.6s ease-out forwards;
      }

      .hitmarker-line.br {
        top: 75%;
        left: 75%;
        animation: _fw-hitmarker-fade-45 0.6s ease-out forwards;
      }

      @keyframes _fw-spin {
        from {
          transform: rotate(0deg);
        }
        to {
          transform: rotate(360deg);
        }
      }

      @keyframes _fw-logo-pulse {
        0%,
        100% {
          opacity: 0.15;
          transform: scale(1);
        }
        50% {
          opacity: 0.25;
          transform: scale(1.05);
        }
      }

      @keyframes _fw-float-up {
        0% {
          transform: translateY(100vh) rotate(0deg);
          opacity: 0;
        }
        10% {
          opacity: 0.6;
        }
        90% {
          opacity: 0.6;
        }
        100% {
          transform: translateY(-100px) rotate(360deg);
          opacity: 0;
        }
      }

      @keyframes _fw-gradient-shift {
        0%,
        100% {
          background-position: 0% 50%;
        }
        50% {
          background-position: 100% 50%;
        }
      }

      @keyframes _fw-hitmarker-fade-45 {
        0% {
          opacity: 1;
          transform: translate(-50%, -50%) rotate(45deg) scale(0.5);
        }
        20% {
          opacity: 1;
          transform: translate(-50%, -50%) rotate(45deg) scale(1.2);
        }
        100% {
          opacity: 0;
          transform: translate(-50%, -50%) rotate(45deg) scale(1);
        }
      }

      @keyframes _fw-hitmarker-fade-neg-45 {
        0% {
          opacity: 1;
          transform: translate(-50%, -50%) rotate(-45deg) scale(0.5);
        }
        20% {
          opacity: 1;
          transform: translate(-50%, -50%) rotate(-45deg) scale(1.2);
        }
        100% {
          opacity: 0;
          transform: translate(-50%, -50%) rotate(-45deg) scale(1);
        }
      }
    `,
  ];

  connectedCallback() {
    super.connectedCallback();
    this._clearBubbleTimers();
    this._startBubbleAnimations();
  }

  disconnectedCallback() {
    super.disconnectedCallback();
    this._clearBubbleTimers();
    this._resizeObserver?.disconnect();
    this._resizeObserver = undefined;
  }

  protected firstUpdated() {
    this._updateLogoSize();
    if (typeof ResizeObserver !== "undefined") {
      this._resizeObserver = new ResizeObserver(() => {
        this._updateLogoSize();
      });
      if (this._containerEl) {
        this._resizeObserver.observe(this._containerEl);
      }
    }
  }

  private _createParticles(): ParticleState[] {
    return Array.from({ length: 12 }, (_, i) => ({
      left: Math.random() * 100,
      size: Math.random() * 4 + 2,
      color: PARTICLE_COLORS[i % PARTICLE_COLORS.length],
      duration: 8 + Math.random() * 4,
      delay: Math.random() * 8,
    }));
  }

  private _createBubbles(): BubbleState[] {
    return Array.from({ length: 8 }, (_, i) => ({
      top: Math.random() * 80 + 10,
      left: Math.random() * 80 + 10,
      size: Math.random() * 60 + 30,
      opacity: 0,
      color: BUBBLE_COLORS[i % BUBBLE_COLORS.length],
    }));
  }

  private _setManagedTimer(callback: () => void, delayMs: number) {
    const timer = setTimeout(() => {
      this._bubbleTimers.delete(timer);
      callback();
    }, delayMs);
    this._bubbleTimers.add(timer);
  }

  private _clearBubbleTimers() {
    this._bubbleTimers.forEach((timer) => clearTimeout(timer));
    this._bubbleTimers.clear();
  }

  private _updateBubble(index: number, nextState: Partial<BubbleState>) {
    if (index < 0 || index >= this._bubbles.length) {
      return;
    }
    const next = [...this._bubbles];
    next[index] = { ...next[index], ...nextState };
    this._bubbles = next;
  }

  private _animateBubble(index: number) {
    this._updateBubble(index, { opacity: 0.15 });

    const visibleDuration = 4000 + Math.random() * 3000;
    this._setManagedTimer(() => {
      this._updateBubble(index, { opacity: 0 });
      this._setManagedTimer(() => {
        this._updateBubble(index, {
          top: Math.random() * 80 + 10,
          left: Math.random() * 80 + 10,
          size: Math.random() * 60 + 30,
        });
        this._setManagedTimer(() => this._animateBubble(index), 200);
      }, 1500);
    }, visibleDuration);
  }

  private _startBubbleAnimations() {
    this._bubbles.forEach((_, index) => {
      this._setManagedTimer(() => this._animateBubble(index), index * 500);
    });
  }

  private _updateLogoSize() {
    const rect = this._containerEl?.getBoundingClientRect() ?? this.getBoundingClientRect();
    const minDimension = Math.min(rect.width, rect.height);
    if (!Number.isFinite(minDimension) || minDimension <= 0) {
      return;
    }
    this._logoSize = minDimension * 0.2;
  }

  private _handleMouseMove = (event: MouseEvent) => {
    const rect = this._containerEl?.getBoundingClientRect() ?? this.getBoundingClientRect();
    if (rect.width === 0 || rect.height === 0) {
      return;
    }

    const centerX = rect.left + rect.width / 2;
    const centerY = rect.top + rect.height / 2;
    const deltaX = event.clientX - centerX;
    const deltaY = event.clientY - centerY;
    const distance = Math.sqrt(deltaX * deltaX + deltaY * deltaY);
    const maxDistance = this._logoSize * 1.5;

    if (distance < maxDistance && distance > 0) {
      const pushStrength = (maxDistance - distance) / maxDistance;
      const pushDistance = 50 * pushStrength;
      this._logoOffset = {
        x: -(deltaX / distance) * pushDistance,
        y: -(deltaY / distance) * pushDistance,
      };
      this._isLogoHovered = true;
      return;
    }

    this._logoOffset = { x: 0, y: 0 };
    this._isLogoHovered = false;
  };

  private _handleMouseLeave = () => {
    this._logoOffset = { x: 0, y: 0 };
    this._isLogoHovered = false;
  };

  private _handleLogoClick = (event: MouseEvent) => {
    event.stopPropagation();

    const rect = this._containerEl?.getBoundingClientRect() ?? this.getBoundingClientRect();
    const hitmarker = {
      id: Date.now() + Math.random(),
      x: event.clientX - rect.left,
      y: event.clientY - rect.top,
    };
    this._hitmarkers = [...this._hitmarkers, hitmarker];
    playHitmarkerSound();

    this._setManagedTimer(() => {
      this._hitmarkers = this._hitmarkers.filter((h) => h.id !== hitmarker.id);
    }, 600);
  };

  private _handleRetry = () => {
    if (this.onRetry) {
      this.onRetry();
      return;
    }
    this.dispatchEvent(
      new CustomEvent("fw-retry", {
        bubbles: true,
        composed: true,
      })
    );
  };

  private get _isLoading() {
    return (
      this.status === "INITIALIZING" ||
      this.status === "BOOTING" ||
      this.status === "WAITING_FOR_DATA" ||
      !this.status
    );
  }

  private get _isOffline() {
    return this.status === "OFFLINE";
  }

  private get _isError() {
    return this.status === "ERROR" || this.status === "INVALID";
  }

  private get _showProgress() {
    return this.status === "INITIALIZING" && this.percentage != null;
  }

  private get _showRetry() {
    return this._isError && (this.retryEnabled || typeof this.onRetry === "function");
  }

  private get _displayMessage() {
    return this.error || this.message || this._t("waitingForStream");
  }

  private _renderStatusIcon() {
    if (this._isLoading) {
      return html`
        <svg
          class="status-icon spinning"
          fill="none"
          viewBox="0 0 24 24"
          style="color: hsl(var(--tn-yellow, 40 95% 64%));"
        >
          <circle
            style="opacity: 0.25;"
            cx="12"
            cy="12"
            r="10"
            stroke="currentColor"
            stroke-width="4"
          />
          <path
            style="opacity: 0.75;"
            fill="currentColor"
            d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"
          ></path>
        </svg>
      `;
    }

    if (this._isOffline) {
      return html`
        <svg
          class="status-icon"
          fill="none"
          viewBox="0 0 24 24"
          stroke="currentColor"
          style="color: hsl(var(--tn-red, 348 100% 72%));"
        >
          <path
            stroke-linecap="round"
            stroke-linejoin="round"
            stroke-width="2"
            d="M18.364 5.636a9 9 0 010 12.728m0 0l-2.829-2.829m2.829 2.829L21 21M15.536 8.464a5 5 0 010 7.072m0 0l-2.829-2.829m-4.243 2.829a4.978 4.978 0 01-1.414-2.83m-1.414 5.658a9 9 0 01-2.167-9.238m7.824 2.167a1 1 0 111.414 1.414m-1.414-1.414L3 3m8.293 8.293l1.414 1.414"
          ></path>
        </svg>
      `;
    }

    if (this._isError) {
      return html`
        <svg
          class="status-icon"
          fill="none"
          viewBox="0 0 24 24"
          stroke="currentColor"
          style="color: hsl(var(--tn-red, 348 100% 72%));"
        >
          <path
            stroke-linecap="round"
            stroke-linejoin="round"
            stroke-width="2"
            d="M12 9v2m0 4h.01m-6.938 4h13.856c1.54 0 2.502-1.667 1.732-3L13.732 4c-.77-1.333-2.694-1.333-3.464 0L3.34 16c-.77 1.333.192 3 1.732 3z"
          ></path>
        </svg>
      `;
    }

    return html`
      <svg
        class="status-icon spinning"
        fill="none"
        viewBox="0 0 24 24"
        style="color: hsl(var(--tn-cyan, 193 100% 75%));"
      >
        <circle
          style="opacity: 0.25;"
          cx="12"
          cy="12"
          r="10"
          stroke="currentColor"
          stroke-width="4"
        />
        <path
          style="opacity: 0.75;"
          fill="currentColor"
          d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"
        ></path>
      </svg>
    `;
  }

  protected render() {
    const progress = Math.min(100, Math.max(0, this.percentage ?? 0));
    const logoSrc = this.logoSrc || LOGOMARK_DATA_URL;

    return html`
      <div
        class="idle-container fw-player-root"
        role="status"
        aria-label="Stream status"
        @mousemove=${this._handleMouseMove}
        @mouseleave=${this._handleMouseLeave}
      >
        ${this._hitmarkers.map(
          (hitmarker) => html`
            <div class="hitmarker" style="left: ${hitmarker.x}px; top: ${hitmarker.y}px;">
              <div class="hitmarker-line tl"></div>
              <div class="hitmarker-line tr"></div>
              <div class="hitmarker-line bl"></div>
              <div class="hitmarker-line br"></div>
            </div>
          `
        )}

        <div class="particles">
          ${this._particles.map(
            (particle) => html`
              <div
                class="particle"
                style="
                  left: ${particle.left}%;
                  width: ${particle.size}px;
                  height: ${particle.size}px;
                  background: ${particle.color};
                  animation-duration: ${particle.duration}s;
                  animation-delay: ${particle.delay}s;
                "
              ></div>
            `
          )}
        </div>

        <div class="bubbles">
          ${this._bubbles.map(
            (bubble) => html`
              <div
                class="bubble"
                style="
                  top: ${bubble.top}%;
                  left: ${bubble.left}%;
                  width: ${bubble.size}px;
                  height: ${bubble.size}px;
                  background: ${bubble.color};
                  opacity: ${bubble.opacity};
                "
              ></div>
            `
          )}
        </div>

        <div
          class="center-logo"
          style="transform: translate(-50%, -50%) translate(${this._logoOffset.x}px, ${this
            ._logoOffset.y}px);"
        >
          <div
            class="logo-pulse ${this._isLogoHovered ? "hovered" : ""}"
            style="width: ${this._logoSize * 1.4}px; height: ${this._logoSize * 1.4}px;"
          ></div>
          <button
            type="button"
            class="logo-button"
            @click=${this._handleLogoClick}
            aria-label="FrameWorks logo"
          >
            <img
              src=${logoSrc}
              alt=""
              class="logo-image ${this._isLogoHovered ? "hovered" : ""}"
              style="width: ${this._logoSize}px; height: ${this._logoSize}px;"
              draggable="false"
            />
          </button>
        </div>

        <fw-dvd-logo .parentRef=${this._containerEl ?? null} .scale=${0.08}></fw-dvd-logo>

        <div class="status-overlay">
          <div class="status-indicator">
            ${this._renderStatusIcon()}
            <span>${this._displayMessage}</span>
          </div>

          ${this._showProgress
            ? html`
                <div class="progress-bar">
                  <div class="progress-fill" style="width: ${progress}%;"></div>
                </div>
              `
            : nothing}
          ${this._showRetry
            ? html`<button type="button" class="retry-btn" @click=${this._handleRetry}>
                ${this._t("retry")}
              </button>`
            : nothing}
        </div>

        <div class="overlay-texture"></div>
      </div>
    `;
  }
}

declare global {
  interface HTMLElementTagNameMap {
    "fw-idle-screen": FwIdleScreen;
  }
}

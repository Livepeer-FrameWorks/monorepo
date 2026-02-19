import { LitElement, css, html } from "lit";
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
  duration: number;
  delay: number;
}

interface BubbleState {
  top: number;
  left: number;
  size: number;
  opacity: number;
}

interface Hitmarker {
  id: number;
  x: number;
  y: number;
}

@customElement("fw-loading-screen")
export class FwLoadingScreen extends LitElement {
  @property({ type: String }) message?: string;
  @property({ type: String, attribute: "logo-src" }) logoSrc?: string;
  @property({ attribute: false }) translator?: TranslateFn;

  private _defaultTranslator: TranslateFn = createTranslator({ locale: "en" });
  @query(".loading-container") private _containerEl?: HTMLDivElement;

  @state() private _logoSize = 100;
  @state() private _logoOffset = { x: 0, y: 0 };
  @state() private _isLogoHovered = false;
  @state() private _bubbles: BubbleState[] = this._createBubbles();
  @state() private _hitmarkers: Hitmarker[] = [];

  private _bubbleTimers = new Set<ReturnType<typeof setTimeout>>();
  private _resizeObserver: ResizeObserver | null = null;
  private readonly _particles: ParticleState[] = this._createParticles();

  static styles = [
    sharedStyles,
    utilityStyles,
    css`
      :host {
        display: block;
        width: 100%;
        height: 100%;
      }

      .loading-container {
        position: relative;
        width: 100%;
        height: 100%;
        min-height: 300px;
        overflow: hidden;
        user-select: none;
        background: linear-gradient(
          135deg,
          hsl(var(--fw-surface-deep, 235 21% 11%)) 0%,
          hsl(var(--fw-surface, 233 23% 17%)) 25%,
          hsl(var(--fw-surface-deep, 235 21% 11%)) 50%,
          hsl(var(--fw-surface, 233 23% 17%)) 75%,
          hsl(var(--fw-surface-deep, 235 21% 11%)) 100%
        );
        background-size: 400% 400%;
        animation: _fw-gradient-shift 16s ease-in-out infinite;
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

      .particle:nth-child(8n + 1) {
        background: hsl(var(--fw-accent, 218 79% 73%));
      }
      .particle:nth-child(8n + 2) {
        background: hsl(var(--fw-accent-secondary, 178 64% 63%));
      }
      .particle:nth-child(8n + 3) {
        background: hsl(var(--fw-success, 95 53% 55%));
      }
      .particle:nth-child(8n + 4) {
        background: hsl(var(--fw-info, 178 64% 63%));
      }
      .particle:nth-child(8n + 5) {
        background: hsl(var(--fw-danger, 348 74% 64%));
      }
      .particle:nth-child(8n + 6) {
        background: hsl(var(--fw-warning, 35 79% 64%));
      }
      .particle:nth-child(8n + 7) {
        background: hsl(var(--fw-accent, 218 79% 73%) / 0.8);
      }
      .particle:nth-child(8n + 8) {
        background: hsl(var(--fw-accent-secondary, 178 64% 63%) / 0.8);
      }

      .bubble:nth-child(8n + 1) {
        background: hsl(var(--fw-accent, 218 79% 73%) / 0.2);
      }
      .bubble:nth-child(8n + 2) {
        background: hsl(var(--fw-accent-secondary, 178 64% 63%) / 0.2);
      }
      .bubble:nth-child(8n + 3) {
        background: hsl(var(--fw-success, 95 53% 55%) / 0.2);
      }
      .bubble:nth-child(8n + 4) {
        background: hsl(var(--fw-info, 178 64% 63%) / 0.2);
      }
      .bubble:nth-child(8n + 5) {
        background: hsl(var(--fw-danger, 348 74% 64%) / 0.2);
      }
      .bubble:nth-child(8n + 6) {
        background: hsl(var(--fw-warning, 35 79% 64%) / 0.2);
      }
      .bubble:nth-child(8n + 7) {
        background: hsl(var(--fw-accent, 218 79% 73%) / 0.15);
      }
      .bubble:nth-child(8n + 8) {
        background: hsl(var(--fw-accent-secondary, 178 64% 63%) / 0.15);
      }

      .center-logo {
        position: absolute;
        top: 50%;
        left: 50%;
        transform: translate(-50%, -50%);
        z-index: 10;
        transition: transform 0.3s ease-out;
      }

      .logo-pulse {
        position: absolute;
        border-radius: 50%;
        background: hsl(var(--fw-accent, 218 79% 73%) / 0.15);
        animation: _fw-logo-pulse 3s ease-in-out infinite;
        pointer-events: none;
      }

      .logo-pulse.hovered {
        animation: _fw-logo-pulse 1s ease-in-out infinite;
        transform: scale(1.2);
      }

      .logo-mark {
        position: relative;
        z-index: 1;
        transition: all 0.3s ease-out;
        border: none;
        background: transparent;
        padding: 0;
        margin: 0;
        cursor: pointer;
      }

      .logo-mark img {
        width: 100%;
        height: 100%;
        display: block;
      }

      .logo-mark.hovered {
        transform: scale(1.1);
        filter: drop-shadow(0 6px 12px hsl(var(--fw-surface-deep, 235 21% 11%) / 0.4))
          brightness(1.1);
      }

      .message {
        position: absolute;
        bottom: 20%;
        left: 50%;
        transform: translateX(-50%);
        z-index: 8;
        color: hsl(var(--fw-text-muted, 224 16% 53%));
        font-size: 16px;
        font-weight: 500;
        text-align: center;
        text-shadow: 0 2px 4px hsl(var(--fw-surface-deep, 235 21% 11%) / 0.5);
        animation: _fw-fade-in-out 2s ease-in-out infinite;
        pointer-events: none;
      }

      .overlay-texture {
        position: absolute;
        inset: 0;
        pointer-events: none;
        background:
          radial-gradient(
            circle at 20% 80%,
            hsl(var(--fw-accent, 218 79% 73%) / 0.03) 0%,
            transparent 50%
          ),
          radial-gradient(
            circle at 80% 20%,
            hsl(var(--fw-accent-secondary, 178 64% 63%) / 0.03) 0%,
            transparent 50%
          ),
          radial-gradient(
            circle at 40% 40%,
            hsl(var(--fw-success, 95 53% 55%) / 0.02) 0%,
            transparent 50%
          );
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
        background-color: hsl(var(--fw-text-bright, 0 0% 100%));
        box-shadow: 0 0 8px hsl(var(--fw-text-bright, 0 0% 100%) / 0.8);
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

      @keyframes _fw-fade-in-out {
        0%,
        100% {
          opacity: 0.6;
        }
        50% {
          opacity: 0.9;
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

  connectedCallback(): void {
    super.connectedCallback();
    this._clearBubbleTimers();
    this._startBubbleAnimations();
  }

  disconnectedCallback(): void {
    super.disconnectedCallback();
    this._clearBubbleTimers();
    this._resizeObserver?.disconnect();
    this._resizeObserver = null;
  }

  protected firstUpdated(): void {
    this._updateLogoSize();
    if (typeof ResizeObserver !== "undefined") {
      this._resizeObserver = new ResizeObserver(() => this._updateLogoSize());
      if (this._containerEl) {
        this._resizeObserver.observe(this._containerEl);
      }
    }
  }

  private _createParticles(): ParticleState[] {
    return Array.from({ length: 12 }, () => ({
      left: Math.random() * 100,
      size: Math.random() * 4 + 2,
      duration: 8 + Math.random() * 4,
      delay: Math.random() * 8,
    }));
  }

  private _createBubbles(): BubbleState[] {
    return Array.from({ length: 8 }, () => ({
      top: Math.random() * 80 + 10,
      left: Math.random() * 80 + 10,
      size: Math.random() * 60 + 30,
      opacity: 0,
    }));
  }

  private _setManagedTimer(callback: () => void, delayMs: number): void {
    const timer = setTimeout(() => {
      this._bubbleTimers.delete(timer);
      callback();
    }, delayMs);
    this._bubbleTimers.add(timer);
  }

  private _clearBubbleTimers(): void {
    this._bubbleTimers.forEach((timer) => clearTimeout(timer));
    this._bubbleTimers.clear();
  }

  private _updateBubble(index: number, nextState: Partial<BubbleState>): void {
    const next = [...this._bubbles];
    next[index] = { ...next[index], ...nextState };
    this._bubbles = next;
  }

  private _animateBubble(index: number): void {
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

  private _startBubbleAnimations(): void {
    this._bubbles.forEach((_, index) => {
      this._setManagedTimer(() => this._animateBubble(index), index * 500);
    });
  }

  private _updateLogoSize(): void {
    const rect = this._containerEl?.getBoundingClientRect() ?? this.getBoundingClientRect();
    const minDimension = Math.min(rect.width, rect.height);
    if (!Number.isFinite(minDimension) || minDimension <= 0) {
      return;
    }
    this._logoSize = minDimension * 0.2;
  }

  private _handleMouseMove = (event: MouseEvent): void => {
    const rect = this._containerEl?.getBoundingClientRect() ?? this.getBoundingClientRect();
    if (rect.width <= 0 || rect.height <= 0) {
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

  private _handleMouseLeave = (): void => {
    this._logoOffset = { x: 0, y: 0 };
    this._isLogoHovered = false;
  };

  private _handleLogoClick = (event: MouseEvent): void => {
    event.stopPropagation();
    const rect = this._containerEl?.getBoundingClientRect() ?? this.getBoundingClientRect();
    const hitmarker: Hitmarker = {
      id: Date.now() + Math.random(),
      x: event.clientX - rect.left,
      y: event.clientY - rect.top,
    };
    this._hitmarkers = [...this._hitmarkers, hitmarker];
    playHitmarkerSound();

    this._setManagedTimer(() => {
      this._hitmarkers = this._hitmarkers.filter((value) => value.id !== hitmarker.id);
    }, 600);
  };

  private get _t(): TranslateFn {
    return this.translator ?? this._defaultTranslator;
  }

  protected render() {
    const logoSrc = this.logoSrc || LOGOMARK_DATA_URL;
    const displayMessage = this.message ?? this._t("waitingForSource");
    return html`
      <div
        class="loading-container fw-player-root"
        @mousemove=${this._handleMouseMove}
        @mouseleave=${this._handleMouseLeave}
      >
        ${this._hitmarkers.map(
          (hitmarker) => html`
            <div class="hitmarker" style="left:${hitmarker.x}px;top:${hitmarker.y}px;">
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
                  left:${particle.left}%;
                  width:${particle.size}px;
                  height:${particle.size}px;
                  animation-duration:${particle.duration}s;
                  animation-delay:${particle.delay}s;
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
                  top:${bubble.top}%;
                  left:${bubble.left}%;
                  width:${bubble.size}px;
                  height:${bubble.size}px;
                  opacity:${bubble.opacity};
                "
              ></div>
            `
          )}
        </div>

        <div
          class="center-logo"
          style="transform:translate(-50%, -50%) translate(${this._logoOffset.x}px, ${this
            ._logoOffset.y}px);"
        >
          <div
            class="logo-pulse ${this._isLogoHovered ? "hovered" : ""}"
            style="width:${this._logoSize * 1.4}px;height:${this._logoSize * 1.4}px;"
          ></div>
          <button
            type="button"
            class="logo-mark ${this._isLogoHovered ? "hovered" : ""}"
            style="width:${this._logoSize}px;height:${this._logoSize}px;"
            @click=${this._handleLogoClick}
            aria-label="FrameWorks logo"
          >
            <img src=${logoSrc} alt="FrameWorks Logo" draggable="false" />
          </button>
        </div>

        <fw-dvd-logo .parentRef=${this._containerEl ?? null} .scale=${0.08}></fw-dvd-logo>

        <div class="message">${displayMessage}</div>
        <div class="overlay-texture"></div>
      </div>
    `;
  }
}

declare global {
  interface HTMLElementTagNameMap {
    "fw-loading-screen": FwLoadingScreen;
  }
}

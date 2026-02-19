/**
 * Default blueprint factories for the vanilla player.
 *
 * Each function receives a BlueprintContext and returns an HTMLElement (or null).
 * Reactivity is wired via ctx.subscribe.on() — each fires immediately with the
 * current value, then on every change.
 */

import type { BlueprintContext, BlueprintMap } from "./Blueprint";
import { formatTime } from "../core/TimeFormat";

function el(tag: string, className?: string): HTMLElement {
  const e = document.createElement(tag);
  if (className) e.className = className;
  return e;
}

function btn(className: string, label: string, onClick: () => void): HTMLButtonElement {
  const b = document.createElement("button");
  b.type = "button";
  b.className = className;
  b.setAttribute("aria-label", label);
  b.title = label;
  b.addEventListener("click", onClick);
  return b;
}

// SVG icon helpers
const ICONS = {
  play: `<svg viewBox="0 0 24 24" fill="currentColor"><polygon points="5,3 19,12 5,21"/></svg>`,
  pause: `<svg viewBox="0 0 24 24" fill="currentColor"><rect x="6" y="4" width="4" height="16"/><rect x="14" y="4" width="4" height="16"/></svg>`,
  volumeUp: `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polygon points="11 5 6 9 2 9 2 15 6 15 11 19 11 5"/><path d="M19.07 4.93a10 10 0 0 1 0 14.14"/><path d="M15.54 8.46a5 5 0 0 1 0 7.07"/></svg>`,
  volumeOff: `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polygon points="11 5 6 9 2 9 2 15 6 15 11 19 11 5"/><line x1="23" y1="9" x2="17" y2="15"/><line x1="17" y1="9" x2="23" y2="15"/></svg>`,
  skipBack: `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polygon points="19 20 9 12 19 4 19 20"/><line x1="5" y1="19" x2="5" y2="5"/></svg>`,
  skipForward: `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polygon points="5 4 15 12 5 20 5 4"/><line x1="19" y1="5" x2="19" y2="19"/></svg>`,
  fullscreen: `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="15 3 21 3 21 9"/><polyline points="9 21 3 21 3 15"/><line x1="21" y1="3" x2="14" y2="10"/><line x1="3" y1="21" x2="10" y2="14"/></svg>`,
  fullscreenExit: `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="4 14 10 14 10 20"/><polyline points="20 10 14 10 14 4"/><line x1="14" y1="10" x2="21" y2="3"/><line x1="3" y1="21" x2="10" y2="14"/></svg>`,
  pip: `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="2" y="3" width="20" height="14" rx="2"/><rect x="12" y="9" width="8" height="6" rx="1" fill="currentColor" opacity="0.3"/></svg>`,
  settings: `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 0 1 0 2.83 2 2 0 0 1-2.83 0l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-2 2 2 2 0 0 1-2-2v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 0 1-2.83 0 2 2 0 0 1 0-2.83l.06-.06A1.65 1.65 0 0 0 4.68 15a1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1-2-2 2 2 0 0 1 2-2h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 0 1 0-2.83 2 2 0 0 1 2.83 0l.06.06A1.65 1.65 0 0 0 9 4.68a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 2-2 2 2 0 0 1 2 2v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 0 1 2.83 0 2 2 0 0 1 0 2.83l-.06.06A1.65 1.65 0 0 0 19.4 9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 0 1 2 2 2 2 0 0 1-2 2h-.09a1.65 1.65 0 0 0-1.51 1z"/></svg>`,
};

function icon(name: keyof typeof ICONS, size = 16): HTMLElement {
  const span = document.createElement("span");
  span.className = "fw-bp-icon";
  span.style.width = `${size}px`;
  span.style.height = `${size}px`;
  span.style.display = "inline-flex";
  span.innerHTML = ICONS[name];
  const svg = span.querySelector("svg");
  if (svg) {
    svg.setAttribute("width", String(size));
    svg.setAttribute("height", String(size));
  }
  return span;
}

// ---- Blueprint factories ----

function container(ctx: BlueprintContext): HTMLElement {
  const root = el("div", "fw-player-surface fw-player-root fw-bp-container");
  root.setAttribute("role", "region");
  root.setAttribute("aria-label", ctx.translate("player", "Video player"));
  root.setAttribute("tabindex", "0");
  root.style.position = "relative";
  root.style.width = "100%";
  root.style.height = "100%";
  root.style.overflow = "hidden";
  root.style.backgroundColor = "black";
  return root;
}

function videocontainer(_ctx: BlueprintContext): HTMLElement {
  const wrap = el("div", "fw-bp-video-container");
  wrap.style.position = "absolute";
  wrap.style.inset = "0";
  return wrap;
}

function controls(ctx: BlueprintContext): HTMLElement {
  const bar = el("div", "fw-bp-controls");
  bar.style.position = "absolute";
  bar.style.bottom = "0";
  bar.style.left = "0";
  bar.style.right = "0";
  bar.style.zIndex = "10";
  bar.style.transition = "opacity 0.2s";
  bar.style.background = "linear-gradient(transparent, rgba(0,0,0,0.7))";
  bar.style.padding = "8px 12px 6px";

  ctx.subscribe.on("playing", () => {
    // Auto-hide controls after 3s of playback
    // (simplified — full implementation would use hover tracking)
  });

  return bar;
}

function controlbar(_ctx: BlueprintContext): HTMLElement {
  const bar = el("div", "fw-bp-controlbar");
  bar.style.display = "flex";
  bar.style.alignItems = "center";
  bar.style.gap = "6px";
  return bar;
}

function play(ctx: BlueprintContext): HTMLElement {
  const b = btn("fw-btn-flush fw-bp-play", ctx.translate("play", "Play"), () =>
    ctx.api.togglePlay()
  );
  const iconPlay = icon("play");
  const iconPause = icon("pause");
  b.appendChild(iconPlay);
  b.appendChild(iconPause);
  iconPause.style.display = "none";

  ctx.subscribe.on("playing", (val) => {
    const playing = val as boolean;
    iconPlay.style.display = playing ? "none" : "";
    iconPause.style.display = playing ? "" : "none";
    b.setAttribute(
      "aria-label",
      playing ? ctx.translate("pause", "Pause") : ctx.translate("play", "Play")
    );
    b.title = b.getAttribute("aria-label") ?? "";
  });

  return b;
}

function seekBackward(ctx: BlueprintContext): HTMLElement {
  return btn("fw-btn-flush fw-bp-seek-back", ctx.translate("skipBack", "Skip back 10s"), () =>
    ctx.api.skipBack(10)
  );
}

function seekForward(ctx: BlueprintContext): HTMLElement {
  return btn("fw-btn-flush fw-bp-seek-fwd", ctx.translate("skipForward", "Skip forward 10s"), () =>
    ctx.api.skipForward(10)
  );
}

function live(ctx: BlueprintContext): HTMLElement {
  const badge = el("div", "fw-bp-live");
  badge.style.display = "none";

  const dot = el("span", "fw-bp-live-dot");
  dot.style.width = "6px";
  dot.style.height = "6px";
  dot.style.borderRadius = "50%";
  dot.style.backgroundColor = "#ef4444";
  dot.style.marginRight = "4px";
  dot.style.display = "inline-block";

  const label = document.createElement("button");
  label.type = "button";
  label.className = "fw-btn-flush";
  label.textContent = "LIVE";
  label.style.fontSize = "0.625rem";
  label.style.fontWeight = "700";
  label.style.textTransform = "uppercase";
  label.style.letterSpacing = "0.05em";
  label.style.display = "inline-flex";
  label.style.alignItems = "center";
  label.style.cursor = "pointer";
  label.addEventListener("click", () => ctx.api.jumpToLive());
  label.prepend(dot);
  badge.appendChild(label);

  ctx.subscribe.on("playing", () => {
    badge.style.display = ctx.api.live ? "" : "none";
  });

  return badge;
}

function currentTimeBlueprint(ctx: BlueprintContext): HTMLElement {
  const span = el("span", "fw-bp-time");
  span.style.fontSize = "0.75rem";
  span.style.fontVariantNumeric = "tabular-nums";
  span.style.whiteSpace = "nowrap";
  span.textContent = "0:00";

  ctx.subscribe.on("currentTime", (val) => {
    span.textContent = formatTime(val as number);
  });

  return span;
}

function totalTime(ctx: BlueprintContext): HTMLElement {
  const span = el("span", "fw-bp-duration");
  span.style.fontSize = "0.75rem";
  span.style.fontVariantNumeric = "tabular-nums";
  span.style.whiteSpace = "nowrap";
  span.style.opacity = "0.7";
  span.textContent = "0:00";

  ctx.subscribe.on("duration", (val) => {
    const d = val as number;
    span.textContent = isNaN(d) || !isFinite(d) ? "" : formatTime(d);
  });

  return span;
}

function speaker(ctx: BlueprintContext): HTMLElement {
  const b = btn("fw-btn-flush fw-bp-speaker", ctx.translate("mute", "Mute"), () =>
    ctx.api.toggleMute()
  );
  const iconOn = icon("volumeUp");
  const iconOff = icon("volumeOff");
  b.appendChild(iconOn);
  b.appendChild(iconOff);
  iconOff.style.display = "none";

  ctx.subscribe.on("muted", (val) => {
    const muted = val as boolean;
    iconOn.style.display = muted ? "none" : "";
    iconOff.style.display = muted ? "" : "none";
    b.setAttribute(
      "aria-label",
      muted ? ctx.translate("unmute", "Unmute") : ctx.translate("mute", "Mute")
    );
    b.title = b.getAttribute("aria-label") ?? "";
  });

  return b;
}

function volumeBlueprint(ctx: BlueprintContext): HTMLElement {
  const wrap = el("div", "fw-bp-volume");
  wrap.style.display = "flex";
  wrap.style.alignItems = "center";
  wrap.style.width = "80px";

  const slider = document.createElement("input");
  slider.type = "range";
  slider.min = "0";
  slider.max = "1";
  slider.step = "0.01";
  slider.className = "fw-bp-volume-slider";
  slider.style.width = "100%";
  slider.style.cursor = "pointer";
  slider.setAttribute("aria-label", ctx.translate("volume", "Volume"));

  slider.addEventListener("input", () => {
    ctx.api.volume = parseFloat(slider.value);
  });

  ctx.subscribe.on("volume", (val) => {
    slider.value = String(val as number);
  });

  wrap.appendChild(slider);
  return wrap;
}

function fullscreenBlueprint(ctx: BlueprintContext): HTMLElement {
  const b = btn("fw-btn-flush fw-bp-fullscreen", ctx.translate("fullscreen", "Fullscreen"), () =>
    ctx.api.toggleFullscreen()
  );
  const iconEnter = icon("fullscreen");
  const iconExit = icon("fullscreenExit");
  b.appendChild(iconEnter);
  b.appendChild(iconExit);
  iconExit.style.display = "none";

  ctx.subscribe.on("fullscreen", (val) => {
    const fs = val as boolean;
    iconEnter.style.display = fs ? "none" : "";
    iconExit.style.display = fs ? "" : "none";
    b.setAttribute(
      "aria-label",
      fs
        ? ctx.translate("exitFullscreen", "Exit fullscreen")
        : ctx.translate("fullscreen", "Fullscreen")
    );
    b.title = b.getAttribute("aria-label") ?? "";
  });

  return b;
}

function pipBlueprint(ctx: BlueprintContext): HTMLElement {
  const b = btn("fw-btn-flush fw-bp-pip", ctx.translate("pip", "Picture-in-Picture"), () =>
    ctx.api.togglePiP()
  );
  b.appendChild(icon("pip"));
  return b;
}

function settingsBlueprint(ctx: BlueprintContext): HTMLElement {
  const b = btn("fw-btn-flush fw-bp-settings", ctx.translate("settings", "Settings"), () => {
    ctx.log(
      "Settings clicked (no built-in menu in blueprint mode — use a skin or custom blueprint)"
    );
  });
  b.appendChild(icon("settings"));
  return b;
}

function progress(ctx: BlueprintContext): HTMLElement {
  const wrap = el("div", "fw-bp-progress");
  wrap.style.position = "relative";
  wrap.style.height = "4px";
  wrap.style.backgroundColor = "rgba(255,255,255,0.2)";
  wrap.style.borderRadius = "2px";
  wrap.style.cursor = "pointer";
  wrap.style.marginBottom = "6px";

  const filled = el("div", "fw-bp-progress-filled");
  filled.style.height = "100%";
  filled.style.backgroundColor = "var(--fw-accent, #3b82f6)";
  filled.style.borderRadius = "2px";
  filled.style.width = "0%";
  filled.style.transition = "width 0.1s linear";
  wrap.appendChild(filled);

  ctx.subscribe.on("currentTime", () => {
    const t = ctx.api.currentTime;
    const d = ctx.api.duration;
    if (d && isFinite(d) && d > 0) {
      filled.style.width = `${Math.min(100, (t / d) * 100)}%`;
    }
  });

  wrap.addEventListener("click", (e) => {
    const rect = wrap.getBoundingClientRect();
    const pct = (e.clientX - rect.left) / rect.width;
    const d = ctx.api.duration;
    if (d && isFinite(d)) {
      ctx.api.seek(pct * d);
    }
  });

  return wrap;
}

function loading(ctx: BlueprintContext): HTMLElement {
  const overlay = el("div", "fw-bp-loading");
  overlay.style.position = "absolute";
  overlay.style.inset = "0";
  overlay.style.display = "none";
  overlay.style.alignItems = "center";
  overlay.style.justifyContent = "center";
  overlay.style.backgroundColor = "rgba(0,0,0,0.4)";
  overlay.style.zIndex = "20";

  const spinner = el("div", "fw-bp-spinner");
  spinner.style.width = "32px";
  spinner.style.height = "32px";
  spinner.style.border = "3px solid rgba(255,255,255,0.3)";
  spinner.style.borderTopColor = "white";
  spinner.style.borderRadius = "50%";
  spinner.style.animation = "fw-spin 0.8s linear infinite";
  overlay.appendChild(spinner);

  ctx.subscribe.on("buffering", (val) => {
    overlay.style.display = (val as boolean) ? "flex" : "none";
  });

  return overlay;
}

function errorBlueprint(ctx: BlueprintContext): HTMLElement {
  const overlay = el("div", "fw-bp-error");
  overlay.style.position = "absolute";
  overlay.style.inset = "0";
  overlay.style.display = "none";
  overlay.style.alignItems = "center";
  overlay.style.justifyContent = "center";
  overlay.style.backgroundColor = "rgba(0,0,0,0.7)";
  overlay.style.zIndex = "25";
  overlay.style.flexDirection = "column";
  overlay.style.gap = "12px";
  overlay.style.color = "white";

  const msg = el("p", "fw-bp-error-msg");
  msg.style.fontSize = "0.875rem";
  overlay.appendChild(msg);

  const retryBtn = btn("fw-btn-flush fw-bp-error-retry", ctx.translate("retry", "Retry"), () => {
    ctx.api.clearError();
    ctx.api.retry();
  });
  retryBtn.textContent = ctx.translate("retry", "Retry");
  retryBtn.style.padding = "6px 16px";
  retryBtn.style.borderRadius = "4px";
  retryBtn.style.backgroundColor = "rgba(255,255,255,0.15)";
  overlay.appendChild(retryBtn);

  ctx.subscribe.on("error", (val) => {
    const err = val as string | null;
    if (err) {
      msg.textContent = err;
      overlay.style.display = "flex";
    } else {
      overlay.style.display = "none";
    }
  });

  return overlay;
}

function spacer(): HTMLElement {
  const s = el("div", "fw-bp-spacer");
  s.style.flex = "1";
  return s;
}

/** All default blueprints keyed by type name */
export const DEFAULT_BLUEPRINTS: BlueprintMap = {
  container,
  videocontainer,
  controls,
  controlbar,
  play,
  seekBackward,
  seekForward,
  live,
  currentTime: currentTimeBlueprint,
  totalTime,
  speaker,
  volume: volumeBlueprint,
  fullscreen: fullscreenBlueprint,
  pip: pipBlueprint,
  settings: settingsBlueprint,
  progress,
  loading,
  error: errorBlueprint,
  spacer,
};

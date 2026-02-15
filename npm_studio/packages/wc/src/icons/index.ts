/**
 * SVG icons as Lit html template functions for StreamCrafter WC.
 * Ported from StreamCrafter.tsx and CompositorControls.tsx inline SVGs.
 */
import { html } from "lit";

export const cameraIcon = (size = 18) =>
  html` <svg
    width="${size}"
    height="${size}"
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    stroke-width="2"
    stroke-linecap="round"
    stroke-linejoin="round"
  >
    <path d="M23 7l-7 5 7 5V7z" />
    <rect x="1" y="5" width="15" height="14" rx="2" ry="2" />
  </svg>`;

export const monitorIcon = (size = 18) =>
  html` <svg
    width="${size}"
    height="${size}"
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    stroke-width="2"
    stroke-linecap="round"
    stroke-linejoin="round"
  >
    <rect x="2" y="3" width="20" height="14" rx="2" ry="2" />
    <line x1="8" y1="21" x2="16" y2="21" />
    <line x1="12" y1="17" x2="12" y2="21" />
  </svg>`;

export const micIcon = (size = 16) =>
  html` <svg
    width="${size}"
    height="${size}"
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    stroke-width="2"
    stroke-linecap="round"
    stroke-linejoin="round"
  >
    <path d="M12 1a3 3 0 0 0-3 3v8a3 3 0 0 0 6 0V4a3 3 0 0 0-3-3z" />
    <path d="M19 10v2a7 7 0 0 1-14 0v-2" />
    <line x1="12" y1="19" x2="12" y2="23" />
    <line x1="8" y1="23" x2="16" y2="23" />
  </svg>`;

export const micMutedIcon = (size = 16) =>
  html` <svg
    width="${size}"
    height="${size}"
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    stroke-width="2"
    stroke-linecap="round"
    stroke-linejoin="round"
  >
    <line x1="1" y1="1" x2="23" y2="23" />
    <path d="M9 9v3a3 3 0 0 0 5.12 2.12M15 9.34V4a3 3 0 0 0-5.94-.6" />
    <path d="M17 16.95A7 7 0 0 1 5 12v-2m14 0v2a7 7 0 0 1-.11 1.23" />
    <line x1="12" y1="19" x2="12" y2="23" />
    <line x1="8" y1="23" x2="16" y2="23" />
  </svg>`;

export const xIcon = (size = 14) =>
  html` <svg
    width="${size}"
    height="${size}"
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    stroke-width="2"
    stroke-linecap="round"
    stroke-linejoin="round"
  >
    <line x1="18" y1="6" x2="6" y2="18" />
    <line x1="6" y1="6" x2="18" y2="18" />
  </svg>`;

export const settingsIcon = (size = 16) =>
  html` <svg
    width="${size}"
    height="${size}"
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    stroke-width="2"
    stroke-linecap="round"
    stroke-linejoin="round"
  >
    <circle cx="12" cy="12" r="3" />
    <path
      d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 0 1 0 2.83 2 2 0 0 1-2.83 0l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-2 2 2 2 0 0 1-2-2v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 0 1-2.83 0 2 2 0 0 1 0-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1-2-2 2 2 0 0 1 2-2h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 0 1 0-2.83 2 2 0 0 1 2.83 0l.06.06a1.65 1.65 0 0 0 1.82.33H9a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 2-2 2 2 0 0 1 2 2v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 0 1 2.83 0 2 2 0 0 1 0 2.83l-.06.06a1.65 1.65 0 0 0-.33 1.82V9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 0 1 2 2 2 2 0 0 1-2 2h-.09a1.65 1.65 0 0 0-1.51 1z"
    />
  </svg>`;

export const chevronsRightIcon = (size = 14) =>
  html` <svg
    width="${size}"
    height="${size}"
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    stroke-width="2"
    stroke-linecap="round"
    stroke-linejoin="round"
  >
    <polyline points="13 17 18 12 13 7" />
    <polyline points="6 17 11 12 6 7" />
  </svg>`;

export const chevronsLeftIcon = (size = 14) =>
  html` <svg
    width="${size}"
    height="${size}"
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    stroke-width="2"
    stroke-linecap="round"
    stroke-linejoin="round"
  >
    <polyline points="11 17 6 12 11 7" />
    <polyline points="18 17 13 12 18 7" />
  </svg>`;

export const eyeIcon = (size = 14) =>
  html` <svg
    width="${size}"
    height="${size}"
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    stroke-width="2"
    stroke-linecap="round"
    stroke-linejoin="round"
  >
    <path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z" />
    <circle cx="12" cy="12" r="3" />
  </svg>`;

export const eyeOffIcon = (size = 14) =>
  html` <svg
    width="${size}"
    height="${size}"
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    stroke-width="2"
    stroke-linecap="round"
    stroke-linejoin="round"
  >
    <path
      d="M17.94 17.94A10.07 10.07 0 0 1 12 20c-7 0-11-8-11-8a18.45 18.45 0 0 1 5.06-5.94M9.9 4.24A9.12 9.12 0 0 1 12 4c7 0 11 8 11 8a18.5 18.5 0 0 1-2.16 3.19m-6.72-1.07a3 3 0 1 1-4.24-4.24"
    />
    <line x1="1" y1="1" x2="23" y2="23" />
  </svg>`;

export const videoIcon = (size = 14) =>
  html` <svg
    width="${size}"
    height="${size}"
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    stroke-width="2"
    stroke-linecap="round"
    stroke-linejoin="round"
  >
    <polygon points="23 7 16 12 23 17 23 7" />
    <rect x="1" y="5" width="15" height="14" rx="2" ry="2" />
  </svg>`;

// Layout icons (12x12 compositor presets)
export const soloIcon = () =>
  html`<svg width="12" height="12" viewBox="0 0 12 12" fill="currentColor">
    <rect x="1" y="1" width="10" height="10" rx="1" />
  </svg>`;
export const pipBRIcon = () =>
  html`<svg width="12" height="12" viewBox="0 0 12 12" fill="currentColor">
    <rect x="1" y="1" width="10" height="10" rx="1" fill-opacity="0.3" />
    <rect x="6.5" y="6.5" width="4" height="3" rx="0.5" />
  </svg>`;
export const pipBLIcon = () =>
  html`<svg width="12" height="12" viewBox="0 0 12 12" fill="currentColor">
    <rect x="1" y="1" width="10" height="10" rx="1" fill-opacity="0.3" />
    <rect x="1.5" y="6.5" width="4" height="3" rx="0.5" />
  </svg>`;
export const pipTRIcon = () =>
  html`<svg width="12" height="12" viewBox="0 0 12 12" fill="currentColor">
    <rect x="1" y="1" width="10" height="10" rx="1" fill-opacity="0.3" />
    <rect x="6.5" y="2.5" width="4" height="3" rx="0.5" />
  </svg>`;
export const pipTLIcon = () =>
  html`<svg width="12" height="12" viewBox="0 0 12 12" fill="currentColor">
    <rect x="1" y="1" width="10" height="10" rx="1" fill-opacity="0.3" />
    <rect x="1.5" y="2.5" width="4" height="3" rx="0.5" />
  </svg>`;
export const splitHIcon = () =>
  html`<svg width="12" height="12" viewBox="0 0 12 12" fill="currentColor">
    <rect x="1" y="1" width="4.5" height="10" rx="1" />
    <rect x="6.5" y="1" width="4.5" height="10" rx="1" />
  </svg>`;
export const splitVIcon = () =>
  html`<svg width="12" height="12" viewBox="0 0 12 12" fill="currentColor">
    <rect x="1" y="1" width="10" height="4.5" rx="1" />
    <rect x="1" y="6.5" width="10" height="4.5" rx="1" />
  </svg>`;
export const focusLIcon = () =>
  html`<svg width="12" height="12" viewBox="0 0 12 12" fill="currentColor">
    <rect x="1" y="1" width="7" height="10" rx="1" />
    <rect x="8.5" y="1" width="2.5" height="10" rx="1" fill-opacity="0.5" />
  </svg>`;
export const focusRIcon = () =>
  html`<svg width="12" height="12" viewBox="0 0 12 12" fill="currentColor">
    <rect x="1" y="1" width="2.5" height="10" rx="1" fill-opacity="0.5" />
    <rect x="4" y="1" width="7" height="10" rx="1" />
  </svg>`;
export const gridIcon = () =>
  html`<svg width="12" height="12" viewBox="0 0 12 12" fill="currentColor">
    <rect x="1" y="1" width="4.5" height="4.5" rx="1" />
    <rect x="6.5" y="1" width="4.5" height="4.5" rx="1" />
    <rect x="1" y="6.5" width="4.5" height="4.5" rx="1" />
    <rect x="6.5" y="6.5" width="4.5" height="4.5" rx="1" />
  </svg>`;
export const stackIcon = () =>
  html`<svg width="12" height="12" viewBox="0 0 12 12" fill="currentColor">
    <rect x="1" y="1" width="10" height="2.8" rx="0.5" />
    <rect x="1" y="4.6" width="10" height="2.8" rx="0.5" />
    <rect x="1" y="8.2" width="10" height="2.8" rx="0.5" />
  </svg>`;
export const dualPipIcon = () =>
  html`<svg width="12" height="12" viewBox="0 0 12 12" fill="currentColor">
    <rect x="1" y="1" width="10" height="10" rx="1" fill-opacity="0.3" />
    <rect x="7" y="4" width="3.5" height="2.5" rx="0.5" />
    <rect x="7" y="7" width="3.5" height="2.5" rx="0.5" />
  </svg>`;
export const splitPipIcon = () =>
  html`<svg width="12" height="12" viewBox="0 0 12 12" fill="currentColor">
    <rect x="1" y="1" width="4.5" height="10" rx="1" />
    <rect x="6.5" y="1" width="4.5" height="10" rx="1" fill-opacity="0.5" />
    <rect x="7.5" y="7" width="2.5" height="2.5" rx="0.5" />
  </svg>`;
export const featuredIcon = () =>
  html`<svg width="12" height="12" viewBox="0 0 12 12" fill="currentColor">
    <rect x="1" y="1" width="10" height="7.5" rx="1" />
    <rect x="1" y="9" width="3" height="2" rx="0.5" fill-opacity="0.5" />
    <rect x="4.5" y="9" width="3" height="2" rx="0.5" fill-opacity="0.5" />
    <rect x="8" y="9" width="3" height="2" rx="0.5" fill-opacity="0.5" />
  </svg>`;
export const featuredRIcon = () =>
  html`<svg width="12" height="12" viewBox="0 0 12 12" fill="currentColor">
    <rect x="1" y="1" width="8" height="10" rx="1" />
    <rect x="9.5" y="1" width="1.5" height="3" rx="0.5" fill-opacity="0.5" />
    <rect x="9.5" y="4.5" width="1.5" height="3" rx="0.5" fill-opacity="0.5" />
    <rect x="9.5" y="8" width="1.5" height="3" rx="0.5" fill-opacity="0.5" />
  </svg>`;

// Scaling mode icons
export const letterboxIcon = () =>
  html`<svg width="12" height="12" viewBox="0 0 12 12" fill="currentColor">
    <rect x="1" y="3" width="10" height="6" rx="1" />
    <rect x="0" y="1" width="12" height="1.5" fill-opacity="0.3" />
    <rect x="0" y="9.5" width="12" height="1.5" fill-opacity="0.3" />
  </svg>`;
export const cropIcon = () =>
  html`<svg width="12" height="12" viewBox="0 0 12 12" fill="currentColor">
    <rect x="0" y="0" width="12" height="12" rx="1" />
    <path
      d="M2 0v2H0v1h3V0H2zM10 0v3h2V2h-2V0H9v3h3V2h-2V0h1zM0 9v1h2v2h1V9H0zM12 9H9v3h1v-2h2v-1z"
      fill-opacity="0.5"
    />
  </svg>`;
export const stretchIcon = () =>
  html`<svg width="12" height="12" viewBox="0 0 12 12" fill="currentColor">
    <rect x="1" y="1" width="10" height="10" rx="1" fill-opacity="0.3" />
    <path
      d="M3 5.5h6M3 5l-1.5 1L3 7M9 5l1.5 1L9 7M5.5 3v6M5 3L6 1.5 7 3M5 9l1 1.5 1-1.5"
      stroke="currentColor"
      stroke-width="1"
      fill="none"
    />
  </svg>`;

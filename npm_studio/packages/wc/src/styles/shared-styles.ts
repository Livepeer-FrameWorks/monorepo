// AUTO-GENERATED — do not edit. Run `pnpm run build:css` to regenerate.
// Source: packages/core/src/styles/streamcrafter.css
import { css } from "lit";

export const sharedStyles = css`
  /**
 * StreamCrafter CSS
 * Wrapped in @layer fw-streamcrafter for cascade isolation.
 * Host app unlayered styles will always take precedence.
 *
 * Import this file in your application:
 * import '@livepeer-frameworks/streamcrafter-core/styles/streamcrafter.css';
 */

  /* Declare layer upfront for lowest priority */
  @layer fw-streamcrafter;

  @layer fw-streamcrafter {
    /* =============================================
     Root Container (with scoped CSS variables)
     ============================================= */
    .fw-sc-root {
      /* Semantic color tokens — defaults are Tokyo Night.
       Override via StudioThemeManager.applyStudioTheme() or data-theme attribute. */
      --fw-sc-surface-deep: 240 15% 10%;
      --fw-sc-surface: 235 19% 13%;
      --fw-sc-surface-raised: 229 24% 19%;
      --fw-sc-surface-active: 232 27% 25%;

      --fw-sc-text: 229 73% 86%;
      --fw-sc-text-bright: 220 13% 91%;
      --fw-sc-text-muted: 229 35% 75%;
      --fw-sc-text-faint: 229 23% 44%;
      --fw-sc-border: 229 24% 31%;

      --fw-sc-accent: 225 86% 70%;
      --fw-sc-accent-secondary: 264 85% 74%;
      --fw-sc-success: 89 51% 61%;
      --fw-sc-danger: 349 89% 72%;
      --fw-sc-warning: 36 66% 64%;
      --fw-sc-info: 197 95% 74%;
      --fw-sc-live: 89 51% 61%;
      --fw-sc-shadow-color: 235 30% 5%;
      --fw-sc-on-accent: 235 19% 13%;

      /* Component styles */
      background: hsl(var(--fw-sc-surface-deep));
      border: 1px solid hsl(var(--fw-sc-border) / 0.3);
      font-family:
        system-ui,
        -apple-system,
        BlinkMacSystemFont,
        "Segoe UI",
        Roboto,
        sans-serif;
      font-size: 14px;
      line-height: 1.5;
      color: hsl(var(--fw-sc-text));
      /* Allow root to take full height of parent and manage its children */
      height: 100%;
      min-height: 0;
      display: flex;
      flex-direction: column;

      overflow: hidden; /* Prevent content from overflowing the root container */

      /* Container query support for responsive layout */
      container-type: inline-size;
      container-name: streamcrafter;
    }

    /* Dev mode - flex layout for side panel */
    .fw-sc-root--devmode {
      display: flex;
      flex-direction: row;
    }

    /* Main content wrapper */
    .fw-sc-main {
      display: flex;
      flex-direction: column;
      min-width: 0;
      /* Occupy remaining vertical space in root */
      flex: 1;
      overflow: hidden; /* Manage internal overflow */
    }

    /* Content area (preview + mixer) - responsive layout */
    .fw-sc-content {
      display: flex;
      flex-direction: column;
      flex: 1; /* Occupy remaining vertical space in fw-sc-main */
      min-height: 0; /* Allow flex item to shrink below content size */
    }

    /* Preview wrapper for flex sizing */
    .fw-sc-preview-wrapper {
      display: flex;
      flex-direction: column;
    }

    /* Mixer panel class for responsive targeting */
    .fw-sc-mixer {
      /* Default: takes full width below preview */
    }

    /* =============================================
   Responsive Layout: Mixer on Right (Wide Screens)
   Uses container queries for container-aware layout
   ============================================= */
    @container streamcrafter (min-width: 600px) {
      .fw-sc-content {
        flex-direction: row;
        align-items: stretch; /* Ensure full height */
      }

      .fw-sc-preview-wrapper {
        flex: 1;
        min-width: 0;
        /* Center preview vertically if needed */
        display: flex;
        flex-direction: column;
        justify-content: center;
        background: black;
      }

      .fw-sc-mixer {
        width: 320px; /* Slightly wider for better ergonomics */
        flex-shrink: 0;
        min-height: 0;
        border-left: 1px solid hsl(var(--fw-sc-border) / 0.3);
        border-bottom: none;
        background: hsl(var(--fw-sc-surface));
        display: flex;
        flex-direction: column;
        max-height: none;
        overflow-y: auto;
        transition: width 0.2s ease-out; /* Smooth collapse transition */
      }

      /* Collapsed state for sidebar mixer */
      .fw-sc-mixer.fw-sc-section--collapsed {
        width: 48px; /* Collapsed width */
        overflow-x: hidden; /* Hide overflowing content */
      }

      .fw-sc-mixer.fw-sc-section--collapsed .fw-sc-section-header {
        justify-content: center; /* Center icon */
      }

      .fw-sc-mixer.fw-sc-section--collapsed .fw-sc-section-header span {
        display: none; /* Hide text */
      }

      .fw-sc-mixer.fw-sc-section--collapsed .fw-sc-section-header svg {
        transform: rotate(0deg) !important; /* Reset chevron rotation */
      }

      .fw-sc-mixer.fw-sc-section--collapsed .fw-sc-sources {
        display: none; /* Hide source list content */
      }

      /* Redesign Source Items for Sidebar Context */
      .fw-sc-mixer .fw-sc-source {
        display: flex;
        align-items: center;
        gap: 0.5rem;
        padding: 0.375rem 0.5rem;
        background: hsl(var(--fw-sc-surface-raised) / 0.1);
        border-bottom: 1px solid hsl(var(--fw-sc-border) / 0.3);
        transition: background 0.2s;
        height: 48px;
      }

      .fw-sc-mixer .fw-sc-source:hover {
        background: hsl(var(--fw-sc-surface-raised) / 0.3);
      }

      /* Icon */
      .fw-sc-mixer .fw-sc-source-icon {
        flex: 0 0 auto;
      }

      /* Name + Type */
      .fw-sc-mixer .fw-sc-source-info {
        flex: 1;
        min-width: 0; /* Enable truncation */
        display: flex;
        flex-direction: column;
        gap: 0;
      }

      .fw-sc-mixer .fw-sc-source-label {
        font-size: 0.8rem;
        font-weight: 600;
      }

      /* Hide the source type in sidebar to save space */
      .fw-sc-mixer .fw-sc-source-type {
        display: none;
      }

      /* Controls */
      .fw-sc-mixer .fw-sc-source-controls {
        display: flex;
        align-items: center;
        gap: 0.25rem;
        margin: 0;
        width: auto;
      }

      /* Volume Slider fixed width in compact mode */
      .fw-sc-mixer .fw-sc-volume-slider {
        width: 60px;
        flex: 0 0 auto;
        height: 6px;
      }

      /* Hide the percentage text in this compact view */
      .fw-sc-mixer .fw-sc-volume-label {
        display: none;
      }
    }

    /* Advanced Panel styling - matches Player DevModePanel */
    .fw-sc-advanced-panel {
      background: hsl(var(--fw-sc-surface));
      border-left: 1px solid hsl(var(--fw-sc-border) / 0.5);
      width: 280px;
      flex-shrink: 0;
      display: flex;
      flex-direction: column;
      height: 100%;
      overflow: hidden;
      font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
      font-size: 12px;
      z-index: 40;
    }

    /* =============================================
   Header Zone
   ============================================= */
    .fw-sc-header {
      display: flex;
      align-items: center;
      justify-content: space-between;
      padding: 0.5rem 1rem;
      border-bottom: 1px solid hsl(var(--fw-sc-border) / 0.3);
      background: hsl(var(--fw-sc-surface-raised) / 0.5);
    }

    .fw-sc-header-title {
      font-weight: 600;
      text-transform: uppercase;
      letter-spacing: 0.05em;
      font-size: 0.75rem;
      color: hsl(var(--fw-sc-text-muted));
    }

    .fw-sc-header-status {
      display: flex;
      align-items: center;
      gap: 0.5rem;
    }

    .fw-sc-header-actions {
      display: flex;
      align-items: center;
      gap: 0.25rem;
    }

    /* Header Button - visible gear/wrench like Player */
    .fw-sc-header-btn {
      display: flex;
      align-items: center;
      justify-content: center;
      width: 28px;
      height: 28px;
      padding: 0;
      border: none;
      border-radius: 4px;
      background: transparent;
      color: hsl(var(--fw-sc-text-faint));
      cursor: pointer;
      transition: all 0.15s ease;
    }

    .fw-sc-header-btn:hover {
      background: hsl(var(--fw-sc-surface-raised));
      color: hsl(var(--fw-sc-text));
    }

    .fw-sc-header-btn--active {
      background: hsl(var(--fw-sc-accent) / 0.2);
      color: hsl(var(--fw-sc-accent));
    }

    .fw-sc-header-btn--active:hover {
      background: hsl(var(--fw-sc-accent) / 0.3);
      color: hsl(var(--fw-sc-accent));
    }

    /* =============================================
   Video Preview (Flush - No Padding)
   ============================================= */
    .fw-sc-preview {
      position: relative;
      /* Use flex-grow instead of aspect-ratio to fill available height */
      flex: 1;
      background: black;
      overflow: hidden;
      display: flex; /* Make it a flex container for the video */
      align-items: center;
      justify-content: center;
    }

    .fw-sc-preview video {
      width: 100%;
      height: 100%;
      object-fit: contain; /* Ensure video fits within the preview area */
    }

    .fw-sc-preview-placeholder {
      position: absolute;
      inset: 0;
      display: flex;
      flex-direction: column;
      align-items: center;
      justify-content: center;
      color: hsl(var(--fw-sc-text-faint));
      gap: 0.5rem;
    }

    .fw-sc-preview-placeholder svg {
      width: 48px;
      height: 48px;
      opacity: 0.5;
    }

    /* =============================================
   Live Badge Overlay
   ============================================= */
    .fw-sc-live-badge {
      position: absolute;
      top: 1rem;
      right: 1rem;
      display: flex;
      align-items: center;
      gap: 0.375rem;
      padding: 0.25rem 0.5rem;
      background: hsl(var(--fw-sc-danger));
      color: white;
      font-size: 0.7rem;
      font-weight: 700;
      text-transform: uppercase;
      letter-spacing: 0.05em;
    }

    .fw-sc-live-badge::before {
      content: "";
      width: 0.5rem;
      height: 0.5rem;
      background: white;
      border-radius: 50%;
      animation: fw-sc-pulse 1.5s infinite;
    }

    @keyframes fw-sc-pulse {
      0%,
      100% {
        opacity: 1;
      }
      50% {
        opacity: 0.5;
      }
    }

    /* =============================================
   Status Overlay
   ============================================= */
    .fw-sc-status-overlay {
      position: absolute;
      inset: 0;
      display: flex;
      flex-direction: column;
      align-items: center;
      justify-content: center;
      background: hsl(var(--fw-sc-surface-deep) / 0.9);
      gap: 1rem;
    }

    .fw-sc-status-spinner {
      width: 32px;
      height: 32px;
      border: 3px solid hsl(var(--fw-sc-border) / 0.3);
      border-top-color: hsl(var(--fw-sc-accent));
      border-radius: 50%;
      animation: fw-sc-spin 1s linear infinite;
    }

    @keyframes fw-sc-spin {
      to {
        transform: rotate(360deg);
      }
    }

    .fw-sc-status-text {
      font-size: 0.875rem;
      color: hsl(var(--fw-sc-text-muted));
    }

    /* =============================================
   VU Meter
   ============================================= */
    .fw-sc-vu-meter {
      height: 8px;
      background: hsl(var(--fw-sc-surface-raised));
      position: relative;
      overflow: hidden;
    }

    .fw-sc-vu-meter-fill {
      height: 100%;
      background: linear-gradient(
        to right,
        hsl(var(--fw-sc-success)) 0%,
        hsl(var(--fw-sc-success)) 60%,
        hsl(var(--fw-sc-warning)) 80%,
        hsl(var(--fw-sc-danger)) 100%
      );
      transition: width 50ms ease-out;
    }

    .fw-sc-vu-meter-peak {
      position: absolute;
      top: 0;
      height: 100%;
      width: 2px;
      background: hsl(var(--fw-sc-text));
      transition: left 50ms ease-out;
    }

    /* Vertical VU Meter variant */
    .fw-sc-vu-meter--vertical {
      width: 4px;
      height: 100%;
      background: hsl(var(--fw-sc-surface-raised));
    }

    .fw-sc-vu-meter--vertical .fw-sc-vu-meter-fill {
      width: 100%;
      background: linear-gradient(
        to top,
        hsl(var(--fw-sc-success)) 0%,
        hsl(var(--fw-sc-success)) 60%,
        hsl(var(--fw-sc-warning)) 80%,
        hsl(var(--fw-sc-danger)) 100%
      );
      transition: height 50ms ease-out;
    }

    /* =============================================
   Section (Collapsible Areas)
   ============================================= */
    .fw-sc-section {
      border-bottom: 1px solid hsl(var(--fw-sc-border) / 0.3);
    }

    .fw-sc-section:last-child {
      border-bottom: none;
    }

    .fw-sc-section-header {
      display: flex;
      align-items: center;
      justify-content: space-between;
      padding: 0.5rem 1rem;
      font-size: 0.7rem;
      font-weight: 600;
      text-transform: uppercase;
      letter-spacing: 0.05em;
      color: hsl(var(--fw-sc-text-faint));
      cursor: pointer;
      user-select: none;
      background: hsl(var(--fw-sc-surface-raised) / 0.3);
    }

    .fw-sc-section-header:hover {
      background: hsl(var(--fw-sc-surface-raised) / 0.5);
    }

    .fw-sc-section-header svg {
      width: 14px;
      height: 14px;
      transition: transform 0.2s;
    }

    .fw-sc-section--collapsed .fw-sc-section-header svg {
      transform: rotate(-90deg);
    }

    .fw-sc-section-body {
      padding: 0.75rem 1rem;
    }

    .fw-sc-section-body--flush {
      padding: 0;
    }

    /* =============================================
   Source List
   ============================================= */
    .fw-sc-sources {
      display: flex;
      flex-direction: column;
    }

    .fw-sc-source {
      display: flex;
      align-items: center;
      padding: 0.5rem 1rem;
      gap: 0.5rem;
      border-bottom: 1px solid hsl(var(--fw-sc-border) / 0.2);
    }

    .fw-sc-source:last-child {
      border-bottom: none;
    }

    .fw-sc-source--hidden {
      opacity: 0.5;
      background: hsl(var(--fw-sc-surface-deep) / 0.3);
    }

    .fw-sc-source-icon {
      width: 24px;
      height: 24px;
      display: flex;
      align-items: center;
      justify-content: center;
      color: hsl(var(--fw-sc-text-faint));
    }

    .fw-sc-source-info {
      flex: 1;
      min-width: 0;
    }

    .fw-sc-source-label {
      font-size: 0.875rem;
      font-weight: 500;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
      display: flex;
      align-items: center;
      gap: 0.5rem;
    }

    .fw-sc-primary-badge {
      font-size: 0.55rem;
      font-weight: 700;
      text-transform: uppercase;
      letter-spacing: 0.05em;
      padding: 0.125rem 0.375rem;
      background: hsl(var(--fw-sc-success) / 0.2);
      color: hsl(var(--fw-sc-success));
      border-radius: 2px;
    }

    .fw-sc-source-type {
      font-size: 0.7rem;
      color: hsl(var(--fw-sc-text-faint));
      text-transform: uppercase;
    }

    .fw-sc-source-controls {
      display: flex;
      align-items: center;
      gap: 0.25rem;
      flex-wrap: wrap; /* Allow items to wrap */
      justify-content: flex-end; /* Align to the right when wrapped */
    }

    /* =============================================
   Icon Buttons (Small Controls)
   ============================================= */
    .fw-sc-icon-btn {
      width: 28px;
      height: 28px;
      display: flex;
      align-items: center;
      justify-content: center;
      padding: 0;
      border: none;
      border-radius: 4px;
      background: transparent;
      color: hsl(var(--fw-sc-text-faint));
      cursor: pointer;
      transition:
        background 0.15s,
        color 0.15s;
    }

    .fw-sc-icon-btn:hover {
      background: hsl(var(--fw-sc-surface-raised) / 0.5);
      color: hsl(var(--fw-sc-text));
    }

    .fw-sc-icon-btn:disabled {
      opacity: 0.5;
      cursor: not-allowed;
    }

    .fw-sc-icon-btn--active {
      color: hsl(var(--fw-sc-accent));
    }

    .fw-sc-icon-btn--inactive {
      opacity: 0.45;
      color: hsl(var(--fw-sc-text-faint));
    }

    .fw-sc-icon-btn--primary {
      color: hsl(var(--fw-sc-success));
    }

    .fw-sc-icon-btn--primary:disabled {
      color: hsl(var(--fw-sc-success));
      opacity: 1;
    }

    .fw-sc-icon-btn--destructive:hover {
      color: hsl(var(--fw-sc-danger));
    }

    .fw-sc-icon-btn--muted {
      color: hsl(var(--fw-sc-text-faint) / 0.5);
    }

    .fw-sc-icon-btn svg {
      width: 16px;
      height: 16px;
    }

    /* =============================================
   Settings Panel
   ============================================= */
    .fw-sc-settings {
      display: flex;
      flex-direction: column;
      gap: 0.5rem;
    }

    .fw-sc-setting-row {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 1rem;
    }

    .fw-sc-setting-label {
      font-size: 0.875rem;
      color: hsl(var(--fw-sc-text-muted));
    }

    .fw-sc-select {
      padding: 0.25rem 0.5rem;
      border: 1px solid hsl(var(--fw-sc-border) / 0.3);
      border-radius: 4px;
      background: hsl(var(--fw-sc-surface-raised));
      color: hsl(var(--fw-sc-text));
      font-size: 0.875rem;
      cursor: pointer;
    }

    .fw-sc-select:focus {
      outline: none;
      border-color: hsl(var(--fw-sc-accent));
    }

    /* =============================================
   Action Bar (Bottom Controls)
   ============================================= */
    .fw-sc-actions {
      display: flex;
      border-top: 1px solid hsl(var(--fw-sc-border) / 0.3);
    }

    .fw-sc-actions button {
      padding: 1rem;
      border: none;
      border-radius: 0;
      background: transparent;
      color: hsl(var(--fw-sc-text));
      font-size: 0.875rem;
      font-weight: 500;
      cursor: pointer;
      transition: background 0.15s;
      border-right: 1px solid hsl(var(--fw-sc-border) / 0.3);
      display: flex;
      align-items: center;
      justify-content: center;
      gap: 0.25rem;
    }

    .fw-sc-actions button:last-child {
      border-right: none;
    }

    .fw-sc-actions button:hover:not(:disabled) {
      background: hsl(var(--fw-sc-surface-raised) / 0.5);
    }

    .fw-sc-actions button:disabled {
      color: hsl(var(--fw-sc-text-faint));
      cursor: not-allowed;
    }

    .fw-sc-actions button svg {
      width: 18px;
      height: 18px;
    }

    /* Secondary actions (Camera, Screen, Settings) - smaller */
    .fw-sc-action-secondary {
      flex: 0 0 auto;
    }

    .fw-sc-action-secondary--active {
      background: hsl(var(--fw-sc-accent) / 0.2) !important;
      color: hsl(var(--fw-sc-accent)) !important;
    }

    /* Settings icon rotation on hover */
    .fw-sc-action-secondary .settings-icon-wrapper {
      display: inline-flex;
      transition: transform 0.2s ease;
    }

    .fw-sc-action-secondary:hover .settings-icon-wrapper {
      transform: rotate(90deg);
    }

    /* Primary action (Go Live) - takes remaining space */
    .fw-sc-action-primary {
      flex: 1;
      font-weight: 600 !important;
      background: hsl(var(--fw-sc-danger)) !important;
      color: white !important;
    }

    .fw-sc-action-primary:hover:not(:disabled) {
      background: hsl(var(--fw-sc-danger) / 0.8) !important;
    }

    .fw-sc-action-primary:disabled {
      background: hsl(var(--fw-sc-surface-raised)) !important;
      color: hsl(var(--fw-sc-text-faint)) !important;
    }

    /* Stop action (when streaming) */
    .fw-sc-action-stop {
      background: hsl(var(--fw-sc-surface-raised)) !important;
    }

    .fw-sc-action-stop:hover:not(:disabled) {
      background: hsl(var(--fw-sc-danger) / 0.3) !important;
    }

    /* =============================================
   Status Badge
   ============================================= */
    .fw-sc-badge {
      display: inline-flex;
      align-items: center;
      gap: 0.25rem;
      padding: 0.125rem 0.5rem;
      font-size: 0.7rem;
      font-weight: 600;
      text-transform: uppercase;
      letter-spacing: 0.025em;
      border-radius: 2px;
    }

    .fw-sc-badge--idle {
      background: hsl(var(--fw-sc-surface-raised));
      color: hsl(var(--fw-sc-text-faint));
    }

    .fw-sc-badge--ready {
      background: hsl(var(--fw-sc-accent) / 0.2);
      color: hsl(var(--fw-sc-accent));
    }

    .fw-sc-badge--live {
      background: hsl(var(--fw-sc-danger));
      color: white;
    }

    .fw-sc-badge--connecting {
      background: hsl(var(--fw-sc-warning) / 0.2);
      color: hsl(var(--fw-sc-warning));
    }

    .fw-sc-badge--error {
      background: hsl(var(--fw-sc-danger) / 0.2);
      color: hsl(var(--fw-sc-danger));
    }

    /* =============================================
   Volume Slider
   ============================================= */
    .fw-sc-volume-label {
      font-size: 0.65rem;
      font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
      color: hsl(var(--fw-sc-text-muted));
      min-width: 32px;
      text-align: right;
      font-variant-numeric: tabular-nums;
    }

    .fw-sc-volume-slider {
      width: 80px; /* Slightly wider by default */
      height: 6px;
      -webkit-appearance: none;
      appearance: none;
      background: hsl(var(--fw-sc-surface-raised));
      border-radius: 3px;
      cursor: pointer;
      position: relative;
      transition: opacity 0.2s;
    }

    .fw-sc-volume-slider:hover {
      opacity: 0.9;
    }

    /* Track fill simulation (Note: WebKit requires JS to update background-size for true fill before thumb, 
   but we can use a gradient trick if value is known, or just keep the clean track look) */

    .fw-sc-volume-slider::-webkit-slider-thumb {
      -webkit-appearance: none;
      width: 14px;
      height: 14px;
      border-radius: 50%;
      background: hsl(var(--fw-sc-text));
      border: 2px solid hsl(var(--fw-sc-surface)); /* Ring effect */
      box-shadow: 0 1px 3px rgba(0, 0, 0, 0.3);
      cursor: pointer;
      margin-top: -4px; /* Center thumb on 6px track */
      transition: transform 0.1s;
    }

    .fw-sc-volume-slider:hover::-webkit-slider-thumb {
      transform: scale(1.1);
      background: white;
    }

    .fw-sc-volume-slider::-moz-range-thumb {
      width: 14px;
      height: 14px;
      border-radius: 50%;
      background: hsl(var(--fw-sc-text));
      border: 2px solid hsl(var(--fw-sc-surface));
      box-shadow: 0 1px 3px rgba(0, 0, 0, 0.3);
      cursor: pointer;
      transition: transform 0.1s;
    }

    .fw-sc-volume-slider:hover::-moz-range-thumb {
      transform: scale(1.1);
      background: white;
    }

    /* Boosted state (>100%) - Add a glow */
    .fw-sc-volume-slider--boosted::-webkit-slider-thumb {
      background: hsl(var(--fw-sc-warning));
      box-shadow: 0 0 5px hsl(var(--fw-sc-warning) / 0.5);
    }

    .fw-sc-volume-slider--boosted::-moz-range-thumb {
      background: hsl(var(--fw-sc-warning));
      box-shadow: 0 0 5px hsl(var(--fw-sc-warning) / 0.5);
    }

    /* =============================================
   Error State
   ============================================= */
    .fw-sc-error {
      padding: 1rem;
      background: hsl(var(--fw-sc-danger) / 0.1);
      border-left: 3px solid hsl(var(--fw-sc-danger));
    }

    .fw-sc-error-title {
      font-weight: 600;
      color: hsl(var(--fw-sc-danger));
      margin-bottom: 0.25rem;
    }

    .fw-sc-error-message {
      font-size: 0.875rem;
      color: hsl(var(--fw-sc-text-muted));
    }

    /* =============================================
   Utility Classes
   ============================================= */
    .fw-sc-hidden {
      display: none !important;
    }

    .fw-sc-sr-only {
      position: absolute;
      width: 1px;
      height: 1px;
      padding: 0;
      margin: -1px;
      overflow: hidden;
      clip: rect(0, 0, 0, 0);
      white-space: nowrap;
      border: 0;
    }

    /* =============================================
   Settings Dropdown
   ============================================= */
    .fw-sc-settings-dropdown {
      position: absolute;
      top: calc(100% + 4px);
      right: 0;
      min-width: 200px;
      background: hsl(var(--fw-sc-surface-deep));
      border: 1px solid hsl(var(--fw-sc-border) / 0.3);
      box-shadow: 0 4px 12px rgba(0, 0, 0, 0.4);
      z-index: 50;
      overflow: hidden;
    }

    .fw-sc-dropdown-section {
      border-bottom: 1px solid hsl(var(--fw-sc-border) / 0.2);
    }

    .fw-sc-dropdown-section:last-child {
      border-bottom: none;
    }

    .fw-sc-dropdown-label {
      padding: 0.5rem 0.75rem 0.25rem;
      font-size: 0.65rem;
      font-weight: 600;
      text-transform: uppercase;
      letter-spacing: 0.05em;
      color: hsl(var(--fw-sc-text-faint));
    }

    .fw-sc-dropdown-options {
      display: flex;
      flex-direction: column;
      padding: 0 0.25rem 0.5rem;
    }

    .fw-sc-dropdown-option {
      display: flex;
      flex-direction: column;
      align-items: flex-start;
      gap: 0;
      padding: 0.375rem 0.5rem;
      margin: 0;
      border: none;
      border-radius: 4px;
      background: transparent;
      color: hsl(var(--fw-sc-text));
      cursor: pointer;
      text-align: left;
      width: 100%;
      transition: background 0.15s;
    }

    .fw-sc-dropdown-option:hover:not(:disabled) {
      background: hsl(var(--fw-sc-surface-raised) / 0.5);
    }

    .fw-sc-dropdown-option:disabled {
      opacity: 0.5;
      cursor: not-allowed;
    }

    .fw-sc-dropdown-option--active {
      background: hsl(var(--fw-sc-accent) / 0.2);
    }

    .fw-sc-dropdown-option--active:hover:not(:disabled) {
      background: hsl(var(--fw-sc-accent) / 0.3);
    }

    .fw-sc-dropdown-option-label {
      font-size: 0.875rem;
      font-weight: 500;
    }

    .fw-sc-dropdown-option-desc {
      font-size: 0.7rem;
      color: hsl(var(--fw-sc-text-faint));
    }

    .fw-sc-dropdown-info {
      padding: 0.25rem 0.75rem 0.5rem;
    }

    .fw-sc-dropdown-info-row {
      display: flex;
      justify-content: space-between;
      align-items: center;
      padding: 0.25rem 0;
      font-size: 0.75rem;
      color: hsl(var(--fw-sc-text-muted));
    }

    .fw-sc-dropdown-info-value {
      font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
      color: hsl(var(--fw-sc-text));
    }

    /* =============================================
   Context Menu
   ============================================= */
    .fw-sc-context-menu {
      position: fixed;
      min-width: 160px;
      background: hsl(var(--fw-sc-surface-deep));
      border: 1px solid hsl(var(--fw-sc-border) / 0.3);
      box-shadow: 0 4px 12px rgba(0, 0, 0, 0.4);
      z-index: 100;
      overflow: hidden;
    }

    .fw-sc-context-menu-item {
      display: flex;
      align-items: center;
      gap: 0.5rem;
      padding: 0.5rem 0.75rem;
      font-size: 0.875rem;
      color: hsl(var(--fw-sc-text));
      cursor: pointer;
      background: transparent;
      border: none;
      width: 100%;
      text-align: left;
      transition:
        background 0.15s,
        color 0.15s;
    }

    .fw-sc-context-menu-item:hover {
      background: hsl(var(--fw-sc-surface-raised) / 0.7);
      color: hsl(var(--fw-sc-text));
    }

    .fw-sc-context-menu-item--destructive {
      color: hsl(var(--fw-sc-danger));
    }

    .fw-sc-context-menu-item--destructive:hover {
      background: hsl(var(--fw-sc-danger) / 0.15);
      color: hsl(var(--fw-sc-danger));
    }

    .fw-sc-context-menu-separator {
      height: 1px;
      background: hsl(var(--fw-sc-border) / 0.3);
      margin: 0.25rem 0;
    }

    .fw-sc-context-menu-label {
      padding: 0.375rem 0.75rem;
      font-size: 0.65rem;
      font-weight: 600;
      text-transform: uppercase;
      letter-spacing: 0.05em;
      color: hsl(var(--fw-sc-text-faint));
    }

    /* =============================================
   Compositor Controls (Phase 3)
   ============================================= */
    .fw-sc-compositor-controls {
      display: flex;
      flex-direction: column;
      border-bottom: 1px solid hsl(var(--fw-sc-border) / 0.3);
    }

    .fw-sc-compositor-controls--disabled,
    .fw-sc-compositor-controls--loading {
      padding: 1rem;
    }

    .fw-sc-compositor-enable {
      text-align: center;
    }

    .fw-sc-compositor-enable p {
      font-size: 0.875rem;
      color: hsl(var(--fw-sc-text-muted));
      margin-bottom: 0.75rem;
    }

    .fw-sc-compositor-enable-btn {
      padding: 0.5rem 1rem;
      border: 1px solid hsl(var(--fw-sc-accent));
      border-radius: 4px;
      background: hsl(var(--fw-sc-accent) / 0.1);
      color: hsl(var(--fw-sc-accent));
      font-size: 0.875rem;
      font-weight: 500;
      cursor: pointer;
      transition: background 0.15s;
    }

    .fw-sc-compositor-enable-btn:hover {
      background: hsl(var(--fw-sc-accent) / 0.2);
    }

    .fw-sc-compositor-loading {
      text-align: center;
      color: hsl(var(--fw-sc-text-faint));
      font-size: 0.875rem;
    }

    .fw-sc-compositor-header {
      display: flex;
      align-items: center;
      justify-content: space-between;
      padding: 0.5rem 1rem;
      border-bottom: 1px solid hsl(var(--fw-sc-border) / 0.2);
      background: hsl(var(--fw-sc-surface-raised) / 0.3);
    }

    .fw-sc-compositor-info {
      display: flex;
      align-items: center;
      gap: 0.75rem;
    }

    .fw-sc-compositor-title {
      font-size: 0.7rem;
      font-weight: 600;
      text-transform: uppercase;
      letter-spacing: 0.05em;
      color: hsl(var(--fw-sc-text-faint));
    }

    .fw-sc-compositor-renderer {
      font-size: 0.7rem;
      color: hsl(var(--fw-sc-text-muted));
    }

    .fw-sc-compositor-stats-inline {
      font-size: 0.65rem;
      color: hsl(var(--fw-sc-border));
      font-family:
        ui-monospace,
        SFMono-Regular,
        SF Mono,
        Menlo,
        Consolas,
        monospace;
      margin-left: 0.5rem;
    }

    .fw-sc-compositor-disable-btn {
      width: 24px;
      height: 24px;
      display: flex;
      align-items: center;
      justify-content: center;
      padding: 0;
      border: none;
      border-radius: 4px;
      background: transparent;
      color: hsl(var(--fw-sc-text-faint));
      cursor: pointer;
      transition: all 0.15s;
    }

    .fw-sc-compositor-disable-btn:hover {
      background: hsl(var(--fw-sc-danger) / 0.2);
      color: hsl(var(--fw-sc-danger));
    }

    .fw-sc-compositor-stats {
      display: flex;
      align-items: center;
      gap: 1rem;
      padding: 0.25rem 1rem;
      font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
      font-size: 0.7rem;
      color: hsl(var(--fw-sc-text-muted));
      background: hsl(var(--fw-sc-surface-deep) / 0.5);
    }

    .fw-sc-stat {
      color: hsl(var(--fw-sc-info));
    }

    /* =============================================
   Compositor Actions (Header Right Side)
   ============================================= */
    .fw-sc-compositor-actions {
      display: flex;
      align-items: center;
      gap: 0.5rem;
    }

    .fw-sc-scaling-mode-select {
      padding: 0.25rem 0.5rem;
      font-size: 0.7rem;
      background: hsl(var(--fw-sc-surface-deep));
      border: 1px solid hsl(var(--fw-sc-border) / 0.3);
      border-radius: 4px;
      color: hsl(var(--fw-sc-text));
      cursor: pointer;
    }

    .fw-sc-scaling-mode-select:hover {
      border-color: hsl(var(--fw-sc-border) / 0.5);
    }

    /* =============================================
   Compositor Sources (Simplified)
   ============================================= */
    .fw-sc-compositor-sources {
      padding: 0.5rem;
      border-bottom: 1px solid hsl(var(--fw-sc-border) / 0.2);
    }

    .fw-sc-section-label {
      font-size: 0.65rem;
      font-weight: 600;
      text-transform: uppercase;
      letter-spacing: 0.05em;
      color: hsl(var(--fw-sc-text-faint));
      margin-bottom: 0.5rem;
      padding-left: 0.5rem;
    }

    .fw-sc-source-list {
      display: flex;
      flex-direction: column;
      gap: 0.25rem;
    }

    .fw-sc-source-row {
      display: flex;
      align-items: center;
      gap: 0.5rem;
      padding: 0.375rem 0.5rem;
      background: hsl(var(--fw-sc-surface-raised) / 0.3);
      border-radius: 4px;
      transition: opacity 0.15s;
    }

    .fw-sc-source-row--hidden {
      opacity: 0.4;
    }

    .fw-sc-visibility-btn {
      width: 24px;
      height: 24px;
      display: flex;
      align-items: center;
      justify-content: center;
      padding: 0;
      border: none;
      border-radius: 4px;
      background: transparent;
      color: hsl(var(--fw-sc-text-muted));
      cursor: pointer;
      transition: all 0.15s;
    }

    .fw-sc-visibility-btn:hover {
      background: hsl(var(--fw-sc-surface-raised));
      color: hsl(var(--fw-sc-text));
    }

    .fw-sc-source-icon {
      display: flex;
      align-items: center;
      color: hsl(var(--fw-sc-text-faint));
    }

    .fw-sc-source-label {
      flex: 1;
      font-size: 0.8rem;
      color: hsl(var(--fw-sc-text));
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    /* =============================================
   Layout Presets (Grid)
   ============================================= */
    .fw-sc-layout-presets {
      padding: 0.5rem;
      border-bottom: 1px solid hsl(var(--fw-sc-border) / 0.2);
    }

    .fw-sc-layout-grid {
      display: grid;
      grid-template-columns: repeat(6, 1fr);
      gap: 0.25rem;
    }

    .fw-sc-layout-btn {
      aspect-ratio: 1;
      display: flex;
      align-items: center;
      justify-content: center;
      padding: 0;
      border: 1px solid hsl(var(--fw-sc-border) / 0.3);
      border-radius: 4px;
      background: hsl(var(--fw-sc-surface-raised) / 0.3);
      color: hsl(var(--fw-sc-text-muted));
      cursor: pointer;
      transition: all 0.15s;
    }

    .fw-sc-layout-btn:hover {
      background: hsl(var(--fw-sc-surface-raised));
      border-color: hsl(var(--fw-sc-border) / 0.5);
    }

    .fw-sc-layout-btn--active {
      background: hsl(var(--fw-sc-accent) / 0.2);
      border-color: hsl(var(--fw-sc-accent) / 0.5);
      color: hsl(var(--fw-sc-accent));
    }

    .fw-sc-layout-btn--disabled,
    .fw-sc-layout-btn:disabled {
      opacity: 0.3;
      cursor: not-allowed;
    }

    .fw-sc-layout-btn--disabled:hover,
    .fw-sc-layout-btn:disabled:hover {
      background: hsl(var(--fw-sc-surface-raised) / 0.3);
      border-color: hsl(var(--fw-sc-border) / 0.3);
    }

    /* Legacy layout preset buttons (for backwards compat) */
    .fw-sc-layout-presets-label {
      font-size: 0.7rem;
      font-weight: 600;
      text-transform: uppercase;
      letter-spacing: 0.05em;
      color: hsl(var(--fw-sc-text-faint));
    }

    .fw-sc-layout-preset-buttons {
      display: flex;
      gap: 0.25rem;
    }

    .fw-sc-layout-preset-btn {
      width: 32px;
      height: 28px;
      display: flex;
      align-items: center;
      justify-content: center;
      padding: 0;
      border: 1px solid hsl(var(--fw-sc-border) / 0.3);
      border-radius: 4px;
      background: hsl(var(--fw-sc-surface-raised) / 0.3);
      color: hsl(var(--fw-sc-text-muted));
      font-size: 0.75rem;
      cursor: pointer;
      transition: all 0.15s;
    }

    .fw-sc-layout-preset-btn:hover {
      background: hsl(var(--fw-sc-surface-raised));
      border-color: hsl(var(--fw-sc-border) / 0.5);
    }

    .fw-sc-layout-preset-btn--active {
      background: hsl(var(--fw-sc-accent) / 0.2);
      border-color: hsl(var(--fw-sc-accent) / 0.5);
      color: hsl(var(--fw-sc-accent));
    }

    .fw-sc-layout-preset-btn--disabled,
    .fw-sc-layout-preset-btn:disabled {
      opacity: 0.3;
      cursor: not-allowed;
    }

    .fw-sc-layout-preset-btn--disabled:hover,
    .fw-sc-layout-preset-btn:disabled:hover {
      background: transparent;
      border-color: hsl(var(--fw-sc-border) / 0.3);
    }

    /* =============================================
   Scene Switcher
   ============================================= */
    .fw-sc-scene-switcher {
      border-bottom: 1px solid hsl(var(--fw-sc-border) / 0.2);
    }

    .fw-sc-scene-switcher-header {
      display: flex;
      align-items: center;
      justify-content: space-between;
      padding: 0.5rem 1rem;
    }

    .fw-sc-scene-switcher-title {
      font-size: 0.7rem;
      font-weight: 600;
      text-transform: uppercase;
      letter-spacing: 0.05em;
      color: hsl(var(--fw-sc-text-faint));
    }

    .fw-sc-transition-controls {
      display: flex;
      align-items: center;
      gap: 0.5rem;
    }

    .fw-sc-transition-select {
      padding: 0.25rem 0.5rem;
      border: 1px solid hsl(var(--fw-sc-border) / 0.3);
      border-radius: 4px;
      background: hsl(var(--fw-sc-surface-raised));
      color: hsl(var(--fw-sc-text));
      font-size: 0.75rem;
      cursor: pointer;
    }

    .fw-sc-transition-select:focus {
      outline: none;
      border-color: hsl(var(--fw-sc-accent));
    }

    .fw-sc-transition-duration {
      width: 60px;
      padding: 0.25rem 0.5rem;
      border: 1px solid hsl(var(--fw-sc-border) / 0.3);
      border-radius: 4px;
      background: hsl(var(--fw-sc-surface-raised));
      color: hsl(var(--fw-sc-text));
      font-size: 0.75rem;
      font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
    }

    .fw-sc-transition-duration:focus {
      outline: none;
      border-color: hsl(var(--fw-sc-accent));
    }

    .fw-sc-transition-unit {
      font-size: 0.7rem;
      color: hsl(var(--fw-sc-text-faint));
    }

    .fw-sc-scene-list {
      display: flex;
      gap: 0.5rem;
      padding: 0 1rem 0.75rem;
      overflow-x: auto;
    }

    .fw-sc-scene-item {
      position: relative;
      display: flex;
      flex-direction: column;
      align-items: flex-start;
      padding: 0.5rem 0.75rem;
      min-width: 100px;
      border: 1px solid hsl(var(--fw-sc-border) / 0.3);
      border-radius: 4px;
      background: hsl(var(--fw-sc-surface-raised));
      color: hsl(var(--fw-sc-text));
      cursor: pointer;
      transition: all 0.15s;
    }

    .fw-sc-scene-item:hover {
      border-color: hsl(var(--fw-sc-border) / 0.5);
    }

    .fw-sc-scene-item--active {
      border-color: hsl(var(--fw-sc-success));
      box-shadow: 0 0 0 1px hsl(var(--fw-sc-success) / 0.3);
    }

    .fw-sc-scene-item--transitioning {
      opacity: 0.7;
      pointer-events: none;
    }

    .fw-sc-scene-name {
      font-size: 0.875rem;
      font-weight: 500;
    }

    .fw-sc-scene-layer-count {
      font-size: 0.7rem;
      color: hsl(var(--fw-sc-text-faint));
    }

    .fw-sc-scene-delete {
      position: absolute;
      top: 2px;
      right: 2px;
      width: 18px;
      height: 18px;
      display: flex;
      align-items: center;
      justify-content: center;
      padding: 0;
      border: none;
      border-radius: 2px;
      background: transparent;
      color: hsl(var(--fw-sc-text-faint));
      font-size: 14px;
      cursor: pointer;
      opacity: 0;
      transition: all 0.15s;
    }

    .fw-sc-scene-item:hover .fw-sc-scene-delete {
      opacity: 1;
    }

    .fw-sc-scene-delete:hover {
      background: hsl(var(--fw-sc-danger) / 0.2);
      color: hsl(var(--fw-sc-danger));
    }

    .fw-sc-scene-add {
      display: flex;
      align-items: center;
      justify-content: center;
      min-width: 40px;
      padding: 0.5rem;
      border: 1px dashed hsl(var(--fw-sc-border) / 0.5);
      border-radius: 4px;
      background: transparent;
      color: hsl(var(--fw-sc-text-faint));
      font-size: 1.25rem;
      cursor: pointer;
      transition: all 0.15s;
    }

    .fw-sc-scene-add:hover {
      border-color: hsl(var(--fw-sc-accent));
      color: hsl(var(--fw-sc-accent));
      background: hsl(var(--fw-sc-accent) / 0.1);
    }

    /* New Scene Input */
    .fw-sc-new-scene-input {
      display: flex;
      gap: 0.5rem;
      padding: 0.5rem 1rem;
      background: hsl(var(--fw-sc-surface-deep) / 0.5);
    }

    .fw-sc-new-scene-input input {
      flex: 1;
      padding: 0.375rem 0.5rem;
      border: 1px solid hsl(var(--fw-sc-border) / 0.3);
      border-radius: 4px;
      background: hsl(var(--fw-sc-surface-raised));
      color: hsl(var(--fw-sc-text));
      font-size: 0.875rem;
    }

    .fw-sc-new-scene-input input:focus {
      outline: none;
      border-color: hsl(var(--fw-sc-accent));
    }

    .fw-sc-new-scene-input button {
      padding: 0.375rem 0.75rem;
      border: none;
      border-radius: 4px;
      background: hsl(var(--fw-sc-accent));
      color: white;
      font-size: 0.75rem;
      font-weight: 500;
      cursor: pointer;
      transition: background 0.15s;
    }

    .fw-sc-new-scene-input button:hover {
      background: hsl(var(--fw-sc-accent) / 0.8);
    }

    .fw-sc-new-scene-input button:last-child {
      background: hsl(var(--fw-sc-surface-raised));
      color: hsl(var(--fw-sc-text-muted));
    }

    .fw-sc-new-scene-input button:last-child:hover {
      background: hsl(var(--fw-sc-surface-raised) / 0.8);
    }

    /* =============================================
   Layer List
   ============================================= */
    .fw-sc-layer-list {
      border-bottom: 1px solid hsl(var(--fw-sc-border) / 0.2);
    }

    .fw-sc-layer-list-header {
      display: flex;
      align-items: center;
      justify-content: space-between;
      padding: 0.5rem 1rem;
      background: hsl(var(--fw-sc-surface-raised) / 0.3);
    }

    .fw-sc-layer-list-title {
      font-size: 0.7rem;
      font-weight: 600;
      text-transform: uppercase;
      letter-spacing: 0.05em;
      color: hsl(var(--fw-sc-text-faint));
    }

    .fw-sc-layer-count {
      font-size: 0.7rem;
      color: hsl(var(--fw-sc-text-muted));
      background: hsl(var(--fw-sc-surface-raised));
      padding: 0.125rem 0.375rem;
      border-radius: 8px;
    }

    .fw-sc-layer-items {
      display: flex;
      flex-direction: column;
    }

    .fw-sc-layer-empty {
      padding: 1rem;
      text-align: center;
      color: hsl(var(--fw-sc-text-faint));
      font-size: 0.875rem;
    }

    .fw-sc-layer-item {
      display: flex;
      align-items: center;
      gap: 0.5rem;
      padding: 0.5rem 1rem;
      border-bottom: 1px solid hsl(var(--fw-sc-border) / 0.15);
      cursor: pointer;
      transition: background 0.15s;
    }

    .fw-sc-layer-item:last-child {
      border-bottom: none;
    }

    .fw-sc-layer-item:hover {
      background: hsl(var(--fw-sc-surface-raised) / 0.3);
    }

    .fw-sc-layer-item--selected {
      background: hsl(var(--fw-sc-accent) / 0.1);
    }

    .fw-sc-layer-item--selected:hover {
      background: hsl(var(--fw-sc-accent) / 0.15);
    }

    .fw-sc-layer-item--dragging {
      opacity: 0.5;
    }

    .fw-sc-layer-item--drag-over {
      border-top: 2px solid hsl(var(--fw-sc-accent));
    }

    .fw-sc-layer-item--hidden {
      opacity: 0.5;
    }

    .fw-sc-layer-visibility {
      width: 24px;
      height: 24px;
      display: flex;
      align-items: center;
      justify-content: center;
      padding: 0;
      border: none;
      border-radius: 4px;
      background: transparent;
      color: hsl(var(--fw-sc-text-faint));
      cursor: pointer;
      transition: all 0.15s;
    }

    .fw-sc-layer-visibility:hover {
      background: hsl(var(--fw-sc-surface-raised));
    }

    .fw-sc-layer-visibility--visible {
      color: hsl(var(--fw-sc-text));
    }

    .fw-sc-layer-icon {
      font-size: 1rem;
    }

    .fw-sc-layer-name {
      flex: 1;
      font-size: 0.875rem;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .fw-sc-layer-opacity {
      display: flex;
      align-items: center;
      gap: 0.5rem;
      margin-right: 0.5rem;
    }

    .fw-sc-layer-opacity input[type="range"] {
      width: 60px;
      height: 4px;
      -webkit-appearance: none;
      appearance: none;
      background: hsl(var(--fw-sc-surface-raised));
      border-radius: 2px;
      cursor: pointer;
    }

    .fw-sc-layer-opacity input[type="range"]::-webkit-slider-thumb {
      -webkit-appearance: none;
      width: 12px;
      height: 12px;
      border-radius: 50%;
      background: hsl(var(--fw-sc-text));
      cursor: pointer;
    }

    .fw-sc-layer-opacity input[type="range"]::-moz-range-thumb {
      width: 12px;
      height: 12px;
      border-radius: 50%;
      background: hsl(var(--fw-sc-text));
      border: none;
      cursor: pointer;
    }

    .fw-sc-layer-opacity span {
      font-size: 0.7rem;
      color: hsl(var(--fw-sc-text-muted));
      font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
      min-width: 32px;
    }

    .fw-sc-layer-controls {
      display: flex;
      align-items: center;
      gap: 0.125rem;
    }

    .fw-sc-layer-btn {
      width: 24px;
      height: 24px;
      display: flex;
      align-items: center;
      justify-content: center;
      padding: 0;
      border: none;
      border-radius: 4px;
      background: transparent;
      color: hsl(var(--fw-sc-text-faint));
      font-size: 0.875rem;
      cursor: pointer;
      transition: all 0.15s;
    }

    .fw-sc-layer-btn:hover:not(:disabled) {
      background: hsl(var(--fw-sc-surface-raised));
      color: hsl(var(--fw-sc-text));
    }

    .fw-sc-layer-btn:disabled {
      opacity: 0.3;
      cursor: not-allowed;
    }

    .fw-sc-layer-btn--active {
      background: hsl(var(--fw-sc-accent) / 0.2);
      color: hsl(var(--fw-sc-accent));
    }

    .fw-sc-layer-btn--danger:hover:not(:disabled) {
      background: hsl(var(--fw-sc-danger) / 0.2);
      color: hsl(var(--fw-sc-danger));
    }

    /* ============================================================================
 * Compact Layout Overlay
 * ============================================================================ */

    .fw-sc-layout-overlay {
      position: absolute;
      bottom: 8px;
      left: 50%;
      transform: translateX(-50%);
      z-index: 20;
      display: flex;
      flex-direction: column;
      align-items: center;
      gap: 4px;
      opacity: 0.7;
      transition: opacity 0.2s ease;
    }

    .fw-sc-layout-overlay:hover,
    .fw-sc-layout-overlay--expanded {
      opacity: 1;
    }

    .fw-sc-layout-bar {
      display: flex;
      align-items: center;
      gap: 2px;
      padding: 4px 6px;
      background: hsl(var(--fw-sc-surface-deep) / 0.9);
      backdrop-filter: blur(8px);
      border-radius: 6px;
      border: 1px solid hsl(var(--fw-sc-border) / 0.3);
      box-shadow: 0 2px 8px rgba(0, 0, 0, 0.3);
    }

    .fw-sc-layout-icons,
    .fw-sc-scaling-icons {
      display: flex;
      align-items: center;
      gap: 1px;
    }

    .fw-sc-layout-icon {
      width: 22px;
      height: 22px;
      display: flex;
      align-items: center;
      justify-content: center;
      padding: 0;
      border: none;
      border-radius: 4px;
      background: transparent;
      color: hsl(var(--fw-sc-text-muted));
      cursor: pointer;
      transition: all 0.15s ease;
    }

    .fw-sc-layout-icon:hover {
      background: hsl(var(--fw-sc-surface-raised));
      color: hsl(var(--fw-sc-text));
    }

    .fw-sc-layout-icon--active {
      background: hsl(var(--fw-sc-accent) / 0.2);
      color: hsl(var(--fw-sc-accent));
    }

    .fw-sc-layout-icon--active:hover {
      background: hsl(var(--fw-sc-accent) / 0.3);
    }

    .fw-sc-layout-separator {
      width: 1px;
      height: 14px;
      background: hsl(var(--fw-sc-border) / 0.5);
      margin: 0 4px;
    }

    .fw-sc-layout-stats {
      font-size: 0.65rem;
      font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
      color: hsl(var(--fw-sc-text-muted));
      white-space: nowrap;
      padding: 0 2px;
    }

    /* Source chips (expanded on hover) */
    .fw-sc-layout-sources {
      display: flex;
      align-items: center;
      gap: 4px;
      padding: 4px 6px;
      background: hsl(var(--fw-sc-surface-deep) / 0.9);
      backdrop-filter: blur(8px);
      border-radius: 6px;
      border: 1px solid hsl(var(--fw-sc-border) / 0.3);
      box-shadow: 0 2px 8px rgba(0, 0, 0, 0.3);
    }

    .fw-sc-source-chip {
      display: flex;
      align-items: center;
      gap: 4px;
      padding: 3px 8px;
      border: none;
      border-radius: 4px;
      background: hsl(var(--fw-sc-surface-raised));
      color: hsl(var(--fw-sc-text));
      font-size: 0.7rem;
      cursor: pointer;
      transition: all 0.15s ease;
    }

    .fw-sc-source-chip:hover {
      background: hsl(var(--fw-sc-surface-raised) / 1.5);
    }

    .fw-sc-source-chip--hidden {
      background: hsl(var(--fw-sc-surface-deep));
      color: hsl(var(--fw-sc-text-faint));
      opacity: 0.6;
    }

    .fw-sc-source-chip--hidden:hover {
      opacity: 0.9;
    }

    .fw-sc-source-chip-icon {
      font-size: 0.75rem;
    }

    .fw-sc-source-chip-label {
      max-width: 80px;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    /* ============================================================================
 * Layout Bar Sections & Labels
 * ============================================================================ */

    .fw-sc-layout-section {
      display: flex;
      align-items: center;
      gap: 4px;
    }

    .fw-sc-layout-label {
      font-size: 0.6rem;
      font-weight: 500;
      text-transform: uppercase;
      letter-spacing: 0.05em;
      color: hsl(var(--fw-sc-text-faint));
      padding: 0 4px;
    }

    /* ============================================================================
 * Custom Instant Tooltips
 * ============================================================================ */

    .fw-sc-tooltip-wrapper {
      position: relative;
      display: inline-flex;
    }

    .fw-sc-tooltip {
      position: absolute;
      bottom: calc(100% + 8px);
      left: 50%;
      transform: translateX(-50%);
      padding: 4px 8px;
      background: hsl(var(--fw-sc-surface-deep));
      border: 1px solid hsl(var(--fw-sc-border) / 0.5);
      border-radius: 4px;
      font-size: 0.7rem;
      font-weight: 500;
      color: hsl(var(--fw-sc-text));
      white-space: nowrap;
      z-index: 100;
      pointer-events: none;
      box-shadow: 0 2px 8px rgba(0, 0, 0, 0.3);
    }

    .fw-sc-tooltip::after {
      content: "";
      position: absolute;
      top: 100%;
      left: 50%;
      transform: translateX(-50%);
      border: 5px solid transparent;
      border-top-color: hsl(var(--fw-sc-border) / 0.5);
    }

    .fw-sc-tooltip::before {
      content: "";
      position: absolute;
      top: 100%;
      left: 50%;
      transform: translateX(-50%);
      border: 4px solid transparent;
      border-top-color: hsl(var(--fw-sc-surface-deep));
      margin-top: -1px;
    }
  } /* End @layer fw-streamcrafter */
`;

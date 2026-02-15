/**
 * Side-effect import that registers all custom elements.
 * Usage: import '@livepeer-frameworks/player-wc/define';
 */
import { FwPlayer } from "./components/fw-player.js";
import { FwPlayerControls } from "./components/fw-player-controls.js";
import { FwSeekBar } from "./components/fw-seek-bar.js";
import { FwVolumeControl } from "./components/fw-volume-control.js";
import { FwSettingsMenu } from "./components/fw-settings-menu.js";
import { FwIdleScreen } from "./components/fw-idle-screen.js";
import { FwLoadingSpinner } from "./components/fw-loading-spinner.js";
import { FwLoadingScreen } from "./components/fw-loading-screen.js";
import { FwStreamStateOverlay } from "./components/fw-stream-state-overlay.js";
import { FwThumbnailOverlay } from "./components/fw-thumbnail-overlay.js";
import { FwDvdLogo } from "./components/fw-dvd-logo.js";
import { FwTitleOverlay } from "./components/fw-title-overlay.js";
import { FwErrorOverlay } from "./components/fw-error-overlay.js";
import { FwToast } from "./components/fw-toast.js";
import { FwStatsPanel } from "./components/fw-stats-panel.js";
import { FwDevModePanel } from "./components/fw-dev-mode-panel.js";
import { FwSubtitleRenderer } from "./components/fw-subtitle-renderer.js";
import { FwSkipIndicator } from "./components/fw-skip-indicator.js";
import { FwSpeedIndicator } from "./components/fw-speed-indicator.js";
import { FwContextMenu } from "./components/fw-context-menu.js";

function safeDefine(name: string, ctor: CustomElementConstructor) {
  if (!customElements.get(name)) {
    customElements.define(name, ctor);
  }
}

safeDefine("fw-player", FwPlayer);
safeDefine("fw-player-controls", FwPlayerControls);
safeDefine("fw-seek-bar", FwSeekBar);
safeDefine("fw-volume-control", FwVolumeControl);
safeDefine("fw-settings-menu", FwSettingsMenu);
safeDefine("fw-idle-screen", FwIdleScreen);
safeDefine("fw-loading-spinner", FwLoadingSpinner);
safeDefine("fw-loading-screen", FwLoadingScreen);
safeDefine("fw-stream-state-overlay", FwStreamStateOverlay);
safeDefine("fw-thumbnail-overlay", FwThumbnailOverlay);
safeDefine("fw-dvd-logo", FwDvdLogo);
safeDefine("fw-title-overlay", FwTitleOverlay);
safeDefine("fw-error-overlay", FwErrorOverlay);
safeDefine("fw-toast", FwToast);
safeDefine("fw-stats-panel", FwStatsPanel);
safeDefine("fw-dev-mode-panel", FwDevModePanel);
safeDefine("fw-subtitle-renderer", FwSubtitleRenderer);
safeDefine("fw-skip-indicator", FwSkipIndicator);
safeDefine("fw-speed-indicator", FwSpeedIndicator);
safeDefine("fw-context-menu", FwContextMenu);

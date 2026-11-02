/**
 * Side-effect import that registers all custom elements.
 * Usage: import '@livepeer-frameworks/streamcrafter-wc/define';
 */
import { FwStreamCrafter } from "./components/fw-streamcrafter.js";
import { FwScCompositor } from "./components/fw-sc-compositor.js";
import { FwScSceneSwitcher } from "./components/fw-sc-scene-switcher.js";
import { FwScLayerList } from "./components/fw-sc-layer-list.js";
import { FwScVolume } from "./components/fw-sc-volume.js";
import { FwScAdvanced } from "./components/fw-sc-advanced.js";
import { FwScProducerPanel } from "./components/fw-sc-producer-panel.js";
import { FwScCameraPicker } from "./components/fw-sc-camera-picker.js";
import { FwScOverlayControls } from "./components/fw-sc-overlay-controls.js";
import { FwScMediaControls } from "./components/fw-sc-media-controls.js";
import { FwScMediaButton } from "./components/fw-sc-media-button.js";
import { FwScMeter } from "./components/fw-sc-meter.js";

function safeDefine(name: string, ctor: CustomElementConstructor) {
  if (!customElements.get(name)) {
    customElements.define(name, ctor);
  }
}

safeDefine("fw-streamcrafter", FwStreamCrafter);
safeDefine("fw-sc-compositor", FwScCompositor);
safeDefine("fw-sc-scene-switcher", FwScSceneSwitcher);
safeDefine("fw-sc-layer-list", FwScLayerList);
safeDefine("fw-sc-volume", FwScVolume);
safeDefine("fw-sc-advanced", FwScAdvanced);
safeDefine("fw-sc-producer-panel", FwScProducerPanel);
safeDefine("fw-sc-camera-picker", FwScCameraPicker);
safeDefine("fw-sc-overlay-controls", FwScOverlayControls);
safeDefine("fw-sc-media-controls", FwScMediaControls);
safeDefine("fw-sc-media-button", FwScMediaButton);
safeDefine("fw-sc-meter", FwScMeter);

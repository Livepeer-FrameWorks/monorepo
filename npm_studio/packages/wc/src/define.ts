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

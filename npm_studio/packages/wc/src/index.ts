/**
 * ESM entry — exports all classes and types but does NOT auto-register custom elements.
 * Use `import '@livepeer-frameworks/streamcrafter-wc/define'` to register elements.
 */
export { FwStreamCrafter } from "./components/fw-streamcrafter.js";
export { FwScCompositor } from "./components/fw-sc-compositor.js";
export { FwScSceneSwitcher } from "./components/fw-sc-scene-switcher.js";
export { FwScLayerList } from "./components/fw-sc-layer-list.js";
export { FwScVolume } from "./components/fw-sc-volume.js";
export { FwScAdvanced } from "./components/fw-sc-advanced.js";
export { IngestControllerHost } from "./controllers/ingest-controller-host.js";
export type {
  IngestControllerHostState,
  EncoderStats,
} from "./controllers/ingest-controller-host.js";

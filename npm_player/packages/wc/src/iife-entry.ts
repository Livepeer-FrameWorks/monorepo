/**
 * IIFE entry point for CDN usage.
 * Bundles Lit + player-core + all components into a single self-registering script.
 *
 * Usage:
 *   <script src="https://cdn.example.com/@livepeer-frameworks/player-wc/dist/fw-player.iife.js"></script>
 *   <fw-player content-id="my-stream" gateway-url="https://..." muted autoplay></fw-player>
 */
import "./define.js";
export { FwPlayer } from "./components/fw-player.js";
export { FwLoadingScreen } from "./components/fw-loading-screen.js";
export { FwIdleScreen } from "./components/fw-idle-screen.js";
export { FwStreamStateOverlay } from "./components/fw-stream-state-overlay.js";
export { FwThumbnailOverlay } from "./components/fw-thumbnail-overlay.js";
export { PlayerControllerHost } from "./controllers/player-controller-host.js";

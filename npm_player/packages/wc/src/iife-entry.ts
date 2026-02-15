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
export { PlayerControllerHost } from "./controllers/player-controller-host.js";

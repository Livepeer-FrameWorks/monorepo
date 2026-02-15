/**
 * IIFE entry — bundles everything and auto-registers custom elements.
 * For CDN <script> tag consumers.
 *
 * Usage:
 *   <script src="fw-streamcrafter.iife.js"></script>
 *   <fw-streamcrafter whip-url="..."></fw-streamcrafter>
 */
import "./define.js";
export { FwStreamCrafter } from "./components/fw-streamcrafter.js";
export { IngestControllerHost } from "./controllers/ingest-controller-host.js";

/**
 * Code examples for the StreamCrafter card on the SDKs page. They are written
 * against the published API of the streamcrafter packages, and
 * studioSnippets.test.ts type-checks them against the package sources.
 */

// Split so Vite's dependency scanner does not read the example imports as
// imports of this module (the packages are not dependencies of the dashboard).
const _imp = "import";

export type StudioFramework = "react" | "svelte" | "wc" | "vanilla";

/** `wcVersion` pins the CDN script, since the SDK is beta and changes between versions. */
export function studioCodeExamples(
  gatewayUrl: string,
  wcVersion: string
): Record<StudioFramework, string> {
  return {
    react: `${_imp} { StreamCrafter } from '@livepeer-frameworks/streamcrafter-react';
${_imp} '@livepeer-frameworks/streamcrafter-react/streamcrafter.css';

export function Studio() {
  return (
    <StreamCrafter
      gatewayUrl="${gatewayUrl}"
      streamKey="your-stream-key"
      initialProfile="broadcast"
    />
  );
}`,
    svelte: `<script>
  ${_imp} { StreamCrafter } from '@livepeer-frameworks/streamcrafter-svelte';
  ${_imp} '@livepeer-frameworks/streamcrafter-svelte/streamcrafter.css';
<\/script>

<StreamCrafter
  gatewayUrl="${gatewayUrl}"
  streamKey="your-stream-key"
  initialProfile="broadcast"
/>`,
    wc: `<!-- IIFE via npm CDN — no bundler needed -->
<!-- unpkg -->
<script src="https://unpkg.com/@livepeer-frameworks/streamcrafter-wc@${wcVersion}/dist/fw-streamcrafter.iife.js"><\/script>
<!-- or jsdelivr -->
<script src="https://cdn.jsdelivr.net/npm/@livepeer-frameworks/streamcrafter-wc@${wcVersion}/dist/fw-streamcrafter.iife.js"><\/script>

<fw-streamcrafter
  gateway-url="${gatewayUrl}"
  stream-key="your-stream-key"
  initial-profile="broadcast"
></fw-streamcrafter>`,
    vanilla: `${_imp} { createStreamCrafter, IngestClient } from '@livepeer-frameworks/streamcrafter-core';

// Resolve the nearest ingest node from the publisher's browser
const client = new IngestClient({
  gatewayUrl: '${gatewayUrl}',
  streamKey: 'your-stream-key',
});
const { primary } = await client.resolve();
if (!primary.whipUrl) throw new Error('No WHIP endpoint');

const studio = createStreamCrafter({ whipUrl: primary.whipUrl, profile: 'broadcast' });

await studio.startCamera();
await studio.goLive();

// When done:
await studio.stop();
studio.destroy();`,
  };
}

export const studioFeatures = [
  "Cameras, screen share and media files in one mix",
  "Scenes with cut, fade and slide transitions",
  "Layouts, layers, and logo and text overlays",
  "Noise gate, de-esser, EQ and ducking",
  "WebCodecs encoding over WHIP",
  "Connection health and auto-reconnect",
];

// StreamCrafter code samples shown in the SDK preview. studioSnippets.test.js
// checks every prop, attribute, import and method they use against the SDK
// sources.

export const studioSnippets = (gatewayUrl) => ({
  react: `import { StreamCrafter } from "@livepeer-frameworks/streamcrafter-react";
import "@livepeer-frameworks/streamcrafter-react/streamcrafter.css";

export function BroadcastPanel() {
  return (
    <StreamCrafter
      gatewayUrl="${gatewayUrl}"
      streamKey="sk_live_..."
      initialProfile="broadcast"
    />
  );
}`,

  svelte: `<script lang="ts">
  import { StreamCrafter } from '@livepeer-frameworks/streamcrafter-svelte'
  import '@livepeer-frameworks/streamcrafter-svelte/streamcrafter.css'
</script>

<StreamCrafter
  gatewayUrl="${gatewayUrl}"
  streamKey="sk_live_..."
  initialProfile="broadcast"
/>`,

  vanilla: `import { createStreamCrafter } from '@livepeer-frameworks/streamcrafter-core'

const studio = createStreamCrafter({
  target: '#studio',
  whipUrl: 'https://edge-ingest.eu.example.com/webrtc/stream-key',
  profile: 'broadcast',
  theme: 'dracula',
  locale: 'en',
})

studio.on('stateChange', ({ state }) => {
  console.log(state === 'streaming' ? 'LIVE' : state)
})

await studio.startCamera()
await studio.goLive()`,

  wc: `<!-- IIFE via npm CDN, no bundler needed -->
<!-- unpkg -->
<script src="https://unpkg.com/@livepeer-frameworks/streamcrafter-wc/dist/fw-streamcrafter.iife.js"></script>
<!-- or jsdelivr -->
<script src="https://cdn.jsdelivr.net/npm/@livepeer-frameworks/streamcrafter-wc/dist/fw-streamcrafter.iife.js"></script>

<fw-streamcrafter
  gateway-url="${gatewayUrl}"
  stream-key="sk_live_..."
  initial-profile="broadcast"
></fw-streamcrafter>`,
});

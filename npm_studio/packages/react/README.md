# @livepeer-frameworks/streamcrafter-react

React wrapper for StreamCrafter: drop-in UI component plus hooks for custom layouts.

## Documentation

Docs: https://logbook.frameworks.network

## Install

```bash
npm install @livepeer-frameworks/streamcrafter-react
```

## Usage (Component)

```tsx
import { StreamCrafter } from "@livepeer-frameworks/streamcrafter-react";
import "@livepeer-frameworks/streamcrafter-react/streamcrafter.css";

export function BroadcastPage() {
  return (
    <StreamCrafter
      whipUrl="https://edge-ingest.example.com/webrtc/your-stream-key"
      initialProfile="broadcast"
    />
  );
}
```

### Gateway Mode (Stream Key + Gateway URL)

```tsx
<StreamCrafter
  gatewayUrl="https://bridge.example.com/graphql"
  streamKey="sk_live_..."
  initialProfile="broadcast"
/>
```

Notes:

- There is **no default gateway**; pass either `whipUrl` or (`gatewayUrl` + `streamKey`).
- If both are provided, `whipUrl` takes priority.

## Usage (Hook)

```tsx
import { useStreamCrafterV2 } from "@livepeer-frameworks/streamcrafter-react";

export function CustomBroadcaster() {
  const { mediaStream, startCamera, startStreaming, stopStreaming } = useStreamCrafterV2({
    whipUrl: "https://edge-ingest.example.com/webrtc/your-stream-key",
    profile: "broadcast",
  });

  return (
    <div>
      <video ref={(el) => el && (el.srcObject = mediaStream)} autoPlay muted />
      <button onClick={() => startCamera()}>Camera</button>
      <button onClick={() => startStreaming()}>Go Live</button>
      <button onClick={() => stopStreaming()}>Stop</button>
    </div>
  );
}
```

## Notes

- CSS export: `@livepeer-frameworks/streamcrafter-react/streamcrafter.css`
- Peer deps: `react`, `react-dom`

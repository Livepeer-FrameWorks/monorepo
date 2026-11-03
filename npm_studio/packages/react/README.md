# @livepeer-frameworks/streamcrafter-react

React wrapper for StreamCrafter: drop-in UI component plus hooks for custom layouts.

> **Beta.** StreamCrafter is on 0.x and its API will change a lot while cloud production and guests are built. Expect breaking changes in minor releases: pin an exact version and read the changelog before you upgrade.

The drop-in `<StreamCrafter>` includes the producer panel (scenes, transitions, layers, logo and text overlays, media files) whenever the compositor runs; turn it off with `showProducerPanel={false}`. It also has Add Media File in the action bar, an Audio section (noise gate, de-esser, EQ, ducking) and a connection health pill while live. Composable children get the compositor from context, so `<StudioProducerPanel />`, `<StudioOverlayControls />`, `<StudioMediaControls />` and `<StudioAudioControls />` need no props.

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

### Keeping the layout per stream

`onLayoutChange` reports the scenes, overlays and layout preset as JSON; give it back as `initialLayout`. Turning `enableCompositor` off and on, or a new `compositorConfig.renderer`, needs no remount.

The studio keeps its current layout when it builds a new controller for the same stream (a new `whipUrl` from the gateway, or a new `initialProfile` or `debug`). It starts from `initialLayout` again only when you pass a different `initialLayout` object, which marks another stream, so keep the object the same between renders with `useMemo`.

```tsx
function readLayout(streamId: string): StudioLayout | null {
  try {
    return JSON.parse(localStorage.getItem(`layout:${streamId}`) ?? "null");
  } catch {
    return null;
  }
}

function saveLayout(streamId: string, layout: StudioLayout) {
  try {
    localStorage.setItem(`layout:${streamId}`, JSON.stringify(layout));
  } catch {
    // Storage is blocked, or full: a picked image is carried as a data URL.
  }
}

const initialLayout = useMemo(() => readLayout(streamId), [streamId]);

<StreamCrafter
  whipUrl={whipUrl}
  initialLayout={initialLayout}
  onLayoutChange={(layout) => saveLayout(streamId, layout)}
/>;
```

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

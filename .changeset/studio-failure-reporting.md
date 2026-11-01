---
"@livepeer-frameworks/streamcrafter-core": patch
"@livepeer-frameworks/streamcrafter-react": patch
"@livepeer-frameworks/streamcrafter-svelte": patch
"@livepeer-frameworks/streamcrafter-wc": patch
---

A failed Go Live or an exhausted reconnect now reports its reason through the `error` event and the banner, with `recoverable` set and the server's status kept (403, 503). Ending a session releases the encoder and connection, the same way `stopStreaming()` does.

Reconnecting stays `reconnecting` until the attempts succeed or run out, so a late attempt can no longer run into a new session. Stop Streaming replaces Go Live during that time and ends the retries, and `stopStreaming()` no longer waits for a lost server before the state changes. A camera that fails with no source running leaves the studio ready with a recoverable error, in `idle` rather than `error`, so a desktop with no webcam can still share a screen, and `autoStartCamera` asks once. Add Camera and Share Screen stay available in the `error` state, so a session that ended with nothing to publish can be recovered.

A failed camera or screen share emits one `error` event, with the device's message (`getUserMedia failed: ...`, `Screen capture failed: ...`), which is also what the state context carries.

The first camera opens the microphone even when a media file or screen share already has audio, and the microphone moves to a remaining camera when the camera holding it is removed, staying muted if it was. A track replaced during a session reaches its sender even after that sender lost its track. A scene transition no longer brings a running layout animation's scene back. An SVG with only a `viewBox` can be used as an overlay. The noise gate, de-esser and ducker hear every channel of a stereo source.

The drop-in components say when the browser cannot capture, publish or run the compositor, turn off Share Screen where `getDisplayMedia` is missing, and replace Add Media File with a reason where `captureStream` is missing. Add Camera says so when the cameras cannot be listed. Showing a source without a layer in `fw-streamcrafter` adds the layer. New exports: `detectStudioSupport`, `FULL_STUDIO_SUPPORT`, `isMediaFileCaptureSupported` and `StudioSupport` in core, `useStudioSupport` and `useCompositor().error` in React, and `error` on the Svelte compositor state.

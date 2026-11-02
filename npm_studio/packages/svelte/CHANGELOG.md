# @livepeer-frameworks/streamcrafter-svelte

## 0.5.0

### Minor Changes

- 1ec53be: The producer panel adds logo and text overlays and local media files, and Add Camera lists your cameras so a second one can join the mix.

  Overlays are on `SceneManager` and on the Svelte store and React hook: `addImageOverlay()` takes a `File` or `Blob`, which stays in the browser, and `addTextOverlay()`, `updateTextOverlay()`, `setOverlayPlacement()`, `removeOverlay()` and `getOverlays()` manage them. Both add methods now take a placement (`x`, `y`, `size`) rather than a layer transform. Text bitmaps are as large as the text, so a lower third covers its band and not the frame.

  `addMediaFile()` plays a video or audio file through the compositor and mixer, with `playMediaSource()`, `pauseMediaSource()` and `setMediaSourceLoop()`. `addMediaFileSource()` now keeps a file that runs out in the mix instead of removing it. `getCameraChoices()` lists the video inputs. `startCamera()` opens no second microphone once one is in the mix, unless you pass `audio: true`.

- eae0c26: StreamCrafter has a producer panel for scenes, transitions and layers: `showProducerPanel` on Svelte and React, `show-producer-panel` on the web component, off by default. Svelte children get the compositor from the StreamCrafter context.

  Fades and slides now play on the WebGL and WebGPU renderers, and no longer flash the target scene when they start. `transitionTo()` resolves when the transition has finished, and a cut can interrupt a running transition. A layer no longer disappears when it is shown again in the solo layout, keeps its place in the stack when hidden and shown, and a removed source leaves every scene. `reorderLayers()` lays the active scene out again.

- 0621375: StreamCrafter no longer records in the browser; FrameWorks records on the server. Removed from core: `RecordingManager`, `WebMWriter` and their option types; on the controller and `createStreamCrafter`, `startRecording()`, `stopRecording()`, `pauseRecording()`, `resumeRecording()`, the `recording`, `recordingDuration` and `recordingFileSize` getters and the controller's `isRecordingActive()`, `getRecordingDuration()` and `getRecordingFileSize()`; the `recordingStarted`, `recordingStopped`, `recordingPaused`, `recordingResumed` and `recordingProgress` events; and `recording` from the reactive state. The React, Svelte and Web Component wrappers expose the controller through `getController()`, so the same members are gone there. `IngestMetadata.recordingEnabled` stays: it reports the stream's server-side recording setting.

### Patch Changes

- 0a0e26d: A failed Go Live or an exhausted reconnect now reports its reason through the `error` event and the banner, with `recoverable` set and the server's status kept (403, 503). Ending a session releases the encoder and connection, the same way `stopStreaming()` does.

  Reconnecting stays `reconnecting` until the attempts succeed or run out, so a late attempt can no longer run into a new session. Stop Streaming replaces Go Live during that time and ends the retries, and `stopStreaming()` no longer waits for a lost server before the state changes. A camera that fails with no source running leaves the studio ready with a recoverable error, in `idle` rather than `error`, so a desktop with no webcam can still share a screen, and `autoStartCamera` asks once. Add Camera and Share Screen stay available in the `error` state, so a session that ended with nothing to publish can be recovered.

  A failed camera or screen share emits one `error` event, with the device's message (`getUserMedia failed: ...`, `Screen capture failed: ...`), which is also what the state context carries.

  The first camera opens the microphone even when a media file or screen share already has audio, and the microphone moves to a remaining camera when the camera holding it is removed, staying muted if it was. A track replaced during a session reaches its sender even after that sender lost its track. A scene transition no longer brings a running layout animation's scene back. An SVG with only a `viewBox` can be used as an overlay. The noise gate, de-esser and ducker hear every channel of a stereo source.

  The drop-in components say when the browser cannot capture, publish or run the compositor, turn off Share Screen where `getDisplayMedia` is missing, and replace Add Media File with a reason where `captureStream` is missing. Add Camera says so when the cameras cannot be listed. Showing a source without a layer in `fw-streamcrafter` adds the layer. New exports: `detectStudioSupport`, `FULL_STUDIO_SUPPORT`, `isMediaFileCaptureSupported` and `StudioSupport` in core, `useStudioSupport` and `useCompositor().error` in React, and `error` on the Svelte compositor state.

- a0c2a03: `LayerList` now calls `onReorder` with layer IDs bottom to top, the order `reorderLayers` takes. Moving a layer used to reverse the whole stack. If you reversed the IDs in your own handler, remove that.
- 61902b6: A failed connection now returns to `capturing` when sources are still live, with the reason in the state context, so a broadcaster can go live again without re-adding sources. The state stays `error` when there is nothing to publish. The advanced panel masks the stream key in the WHIP URL it displays; copying still gives the full URL.
- Updated dependencies [ffba554]
- Updated dependencies [1ec53be]
- Updated dependencies [0a0e26d]
- Updated dependencies [13bf3ae]
- Updated dependencies [1ec53be]
- Updated dependencies [eae0c26]
- Updated dependencies [0621375]
- Updated dependencies [2d35259]
- Updated dependencies [61902b6]
  - @livepeer-frameworks/streamcrafter-core@0.5.0

## 0.4.3

### Patch Changes

- @livepeer-frameworks/streamcrafter-core@0.4.3

## 0.4.2

### Patch Changes

- @livepeer-frameworks/streamcrafter-core@0.4.2

## 0.4.1

### Patch Changes

- @livepeer-frameworks/streamcrafter-core@0.4.1

## 0.4.0

### Minor Changes

- Viewer and ingest resolution send the typed `ResolveViewerEndpoint` and `ResolveIngestEndpoint` documents from
  `@livepeer-frameworks/api`, now a dependency of the core packages. Each gateway's `serverInfo` is probed once per
  page: a gateway older than FrameWorks v0.3.11, or one without `serverInfo`, is refused with `ServerTooOldError`
  (`GatewayClient.resolve()` and `IngestClient.resolve()` reject with it, and the status event carries its message).
  Development builds and release candidates are not checked, and a probe that cannot reach the gateway does not block
  resolution. The `GatewayClient` and `IngestClient` APIs are unchanged.

### Patch Changes

- Updated dependencies
  - @livepeer-frameworks/streamcrafter-core@0.4.0

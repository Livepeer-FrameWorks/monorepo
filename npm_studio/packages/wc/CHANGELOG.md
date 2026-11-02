# @livepeer-frameworks/streamcrafter-wc

## 0.6.0

### Minor Changes

- 278b951: The echo cancellation, noise suppression and auto gain toggles live only in the Audio section; the dev-mode advanced panel no longer repeats them and shows the capture format instead. `AdvancedPanel` (Svelte) drops its `audioProcessing` and `onAudioProcessingChange` props, and `<fw-sc-advanced>` drops its `audioProcessing` property and no longer fires `fw-audio-processing-change`. `AudioProcessingSettings` is now a core type; the Svelte package still re-exports it, and `<fw-sc-audio-controls>` takes it for `browserProcessing` in place of `BrowserAudioProcessing`.
- ff9532e: The studio has an Audio section while capturing, without dev mode: echo cancellation, noise suppression and auto gain for the microphones, and a noise gate with its threshold, a de-esser, a three-band EQ and ducking of other sources under the camera microphone. While live, a pill next to the status badge grades the connection as good, unstable or poor, with the send rate, packet loss and round-trip time as its title. New `StudioAudioControls` and `StudioConnectionHealth` on React and Svelte, and `<fw-sc-audio-controls>` and `<fw-sc-connection-health>` on the web component.
- c626985: The bindings match on three APIs. React `StreamCrafter` and the `<fw-streamcrafter>` `resolveWhipUrl` property take a resolver that returns the destination right before each connection, as the Svelte component already did. The `<fw-streamcrafter>` `theme` property takes an object of token overrides as well as a preset name. The Svelte compositor store has `setRenderer()`, like the React `useCompositor`.
- 94464dd: `]` and `[` switch to the next and previous scene in every drop-in studio, as they already did in the vanilla studio; a key typed into a field is left alone. Scene tiles take keyboard focus and switch with Enter or Space. The layer list in the producer panel removes a layer and edits its opacity. Core exports `handleStudioSceneHotkey()`, `adjacentSceneId()` and `isTypingTarget()`.
- 462f661: Add Media File sits in the action bar next to Add Camera and Share Screen, in the drop-in UI and in `StudioActionBar`. A browser that cannot play a file into the stream gets the button turned off with the reason as its title. The media section of the producer panel lists the files with their playback controls. New `MediaFileButton` on React and Svelte, and `<fw-sc-media-button>` on the web component. `StudioMediaControls` no longer takes `addMediaFile`.
- 7593ca0: The producer panel is on by default whenever the compositor runs. Turn it off with `showProducerPanel={false}` on React and Svelte, or `show-producer-panel="false"` on the web component. On the web component, `enable-compositor="false"` now turns the compositor off; before, any value of the attribute left it on.
- 531d6bb: `<fw-sc-volume>`, `<fw-sc-compositor>`, `<fw-sc-scene-switcher>`, `<fw-sc-layer-list>` and `<fw-sc-advanced>` no longer declare a `for` attribute, which did nothing; `FwScVolume.resolveStudio()` is gone with it. These elements are presentational: set their data through JS properties. For a studio-bound scene switcher and layer list, use `<fw-sc-producer-panel>`.
- 6ee0ca7: New elements `<fw-sc-producer-panel>`, `<fw-sc-camera-picker>`, `<fw-sc-overlay-controls>`, `<fw-sc-media-controls>` and `<fw-sc-meter>`, which `<fw-streamcrafter>` also uses itself. Each binds to the studio whose id is in `for`, or to the `<fw-streamcrafter>` it sits in, and finds a studio that mounts after it. With `controls="false"` they build a custom layout around the studio.

  `<fw-streamcrafter>` renders again when scenes, layers or transitions change through its scene manager, and no longer renders at the audio meter's rate while capturing. It exposes `t`, `support` and `sceneManager` for the sub-elements.

### Patch Changes

- 5971599: Copy Stream Info masks the stream key in the WHIP URL it copies, and the menu item that copies the full URL says that it contains the stream key. The context menu and the quality popup take their colours from the studio theme tokens, so a themed studio no longer shows Tokyo Night colours there. Core exports `formatStreamInfo()`, and the `.fw-sc-portal` class gives the theme tokens to popups rendered outside the studio root. `StudioSettings` takes `defaultOpen` and extra sections as children.
- aad2032: The Add Media File button has an accessible name, and its error note closes on a click elsewhere or Escape, like the camera menu.
- 88694e8: The READMEs say that StreamCrafter is in beta and will change a lot, and describe the producer panel, the audio section and the bound web component sub-elements.
- 17e7159: A scene tile selects its scene with a native button (`.fw-sc-scene-select`, `aria-pressed` on the active scene) and keeps the delete button beside it instead of inside it, so screen readers announce both. The delete button has an accessible name and shows when it has keyboard focus.
- 75299ba: A studio-bound sub-element binds to the new studio when its `for` attribute or `studio` property changes after it is connected.
- Updated dependencies [278b951]
- Updated dependencies [267f1d1]
- Updated dependencies [177c851]
- Updated dependencies [ff9532e]
- Updated dependencies [6381331]
- Updated dependencies [72334c5]
- Updated dependencies [94464dd]
- Updated dependencies [5971599]
- Updated dependencies [88694e8]
- Updated dependencies [17e7159]
- Updated dependencies [7fb5951]
  - @livepeer-frameworks/streamcrafter-core@0.6.0

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

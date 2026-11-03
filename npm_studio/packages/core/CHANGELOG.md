# @livepeer-frameworks/streamcrafter-core

## 0.8.0

### Minor Changes

- d05e03e: A studio can switch streams without losing its sources or carrying one stream's scenes to another. The drop-ins take a new `layoutKey` prop (`layout-key` attribute on `<fw-streamcrafter>`) that names the stream the layout belongs to. When it changes, the running studio shows that key's `initialLayout`, or the default layout when there is none or it does not apply. Before, a switch between two streams that both had no saved layout kept the previous stream's scenes. `onLayoutChange` now receives `(layout, layoutKey)`, and `fw-sc-layout-change` carries `{ layout, layoutKey }`. A report exported for the previous key is dropped, including one whose export was still running, so it cannot be saved under the next stream's key. A new `whipUrl` now goes to the running controller instead of rebuilding it, so camera, screen and media sources keep running; a broadcast that is live when `whipUrl` changes is stopped, and the next one goes to the new endpoint. Without `layoutKey`, a new `initialLayout` object no longer counts as another stream.

  Core adds `IngestControllerV2.setWhipUrl()` (also on the vanilla studio and `StreamCrafterV2`), `createDefaultStudioLayout()`, and `StudioLayoutKeeper`, which the drop-ins share for these rules. React's `useCompositor` and Svelte's `createCompositorStore` take a `layoutKeeper` in place of `initialLayout`, and return `followLayoutKey()` to apply a key change to the running compositor.

### Patch Changes

- e7c3d61: The advanced panel's Encoder block now shows the effective encoder config read-only: video codec preference, resolution, frame rate, bitrates and audio codec, with each value that differs from the quality profile marked as overridden and a pointer to Settings. The Encoder section in `StudioSettings` is the one place to change these. The Svelte `AdvancedPanel` drops its `onEncoderOverridesChange` prop and `<fw-sc-advanced>` no longer fires `fw-encoder-overrides-change`. Core exports `encoderSummaryRows()` for the read-only rows. The Spanish, French, German and Dutch packs now translate the encoder labels, including the codec's Auto choice. The web component README's `setEncoderOverrides` example now uses the `{ video: { bitrate } }` shape that `EncoderOverrides` takes.

## 0.7.1

### Patch Changes

- 10ed5f0: The drop-in studios keep the current layout when they build a new controller for the same stream, such as after a profile, `debug` or endpoint change, instead of going back to `initialLayout`; they start from `initialLayout` again only when it changes to another object, which marks another stream. The controller's `exportLayout()` now returns the layout the compositor had when it was last turned off or destroyed, and the layout is read at the time of the call. A picked image larger than the output frame is stored at frame size and encoded once, so reporting a layout after every change stays cheap. `importLayout()` refuses an unknown layout mode or text overlay field before it changes anything. A failing `onLayoutChange` callback is now logged apart from a failed export. In React, the Encoder section follows the `qualityProfile` given to `StudioSettings`. The web component also starts the compositor on a controller built while the previous one was still starting. The toggle knobs in the advanced panel use the foreground token of their track, so they show in the light themes.

## 0.7.0

### Minor Changes

- 2e96c5a: The settings popup has an Encoder section in every binding, outside dev mode: resolution, frame rate, video and audio bitrate, and the video codec when the browser can send more than one. A choice equal to the quality profile clears the override, Reset to Profile clears them all, and the settings lock while streaming. `EncoderOverrides.video.codec` offers the chosen codec to the server first and takes the place of the `videoCodec` option. Core exports the choices (`encoderSettingRows`, `withEncoderSetting`, `sendableVideoCodecs`) so custom UIs offer the same ones, and React `useStreamCrafterV2` now returns `encoderOverrides`.
- 2e96c5a: The studio layout can be saved and restored. `exportLayout()` returns the scenes, their image and text overlays and the layout preset as plain data (a picked image travels as a data URL), and `importLayout()` applies it; both are on `SceneManager`, the controller, the vanilla studio, React `useCompositor`, the Svelte compositor store and `<fw-streamcrafter>`. The drop-in components take `initialLayout` and report changes through `onLayoutChange` (and the `fw-sc-layout-change` event on the web component), so an app can keep a layout per stream. Turning `enableCompositor` off and on no longer needs a remount and keeps the layout, and a new `compositorConfig.renderer` switches the running compositor. In React the compositor also moves to the new controller when the endpoint changes.

### Patch Changes

- 2e96c5a: The advanced panel toggles and the warning and connecting notices take their colours from the theme tokens, so they follow custom themes. The React layer list shows the same camera icon as Svelte and the web component, React removes an overlay with the same cross icon, the web component's advanced panel closes with the same cross as the others, and the connecting notice has the same title in every binding.
- Updated dependencies [30d2f3e]
  - @livepeer-frameworks/api@0.5.0

## 0.6.0

### Minor Changes

- 278b951: The echo cancellation, noise suppression and auto gain toggles live only in the Audio section; the dev-mode advanced panel no longer repeats them and shows the capture format instead. `AdvancedPanel` (Svelte) drops its `audioProcessing` and `onAudioProcessingChange` props, and `<fw-sc-advanced>` drops its `audioProcessing` property and no longer fires `fw-audio-processing-change`. `AudioProcessingSettings` is now a core type; the Svelte package still re-exports it, and `<fw-sc-audio-controls>` takes it for `browserProcessing` in place of `BrowserAudioProcessing`.
- 267f1d1: `audioMixing` defaults to true for `IngestControllerV2` and `createStreamCrafter()`, as it already was in the React, Svelte and web component studios. Master volume, levels, media file audio and the audio processing chain work without setting it. Pass `audioMixing: false` to skip the Web Audio mixer.
- 177c851: `IngestControllerV2.setAudioProcessing()` sets the noise gate, EQ and de-esser on the mixed audio; settings made before capture are kept and applied once the mixer runs. `setAudioDucking(true)` lowers every other source while the first camera's microphone has signal, and follows sources as they come and go. Ducking no longer overwrites a source's volume, and a source ducked when ducking is turned off returns to full level. `assessConnectionHealth()` grades the publish connection from its stats.

  `IngestStats.connection.rtt` is now in milliseconds, as the advanced panels already displayed it; it was in seconds.

- 94464dd: `]` and `[` switch to the next and previous scene in every drop-in studio, as they already did in the vanilla studio; a key typed into a field is left alone. Scene tiles take keyboard focus and switch with Enter or Space. The layer list in the producer panel removes a layer and edits its opacity. Core exports `handleStudioSceneHotkey()`, `adjacentSceneId()` and `isTypingTarget()`.
- 5971599: Copy Stream Info masks the stream key in the WHIP URL it copies, and the menu item that copies the full URL says that it contains the stream key. The context menu and the quality popup take their colours from the studio theme tokens, so a themed studio no longer shows Tokyo Night colours there. Core exports `formatStreamInfo()`, and the `.fw-sc-portal` class gives the theme tokens to popups rendered outside the studio root. `StudioSettings` takes `defaultOpen` and extra sections as children.

### Patch Changes

- ff9532e: The studio has an Audio section while capturing, without dev mode: echo cancellation, noise suppression and auto gain for the microphones, and a noise gate with its threshold, a de-esser, a three-band EQ and ducking of other sources under the camera microphone. While live, a pill next to the status badge grades the connection as good, unstable or poor, with the send rate, packet loss and round-trip time as its title. New `StudioAudioControls` and `StudioConnectionHealth` on React and Svelte, and `<fw-sc-audio-controls>` and `<fw-sc-connection-health>` on the web component.
- 6381331: `IngestStats` reports packet loss from the receiver's reports (`remote-inbound-rtp`), which it read as 0 before, and adds `connection.packetLossPct`, the loss over the last stats interval. The connection health pill grades on that interval loss, so it follows the link as it is now, and falls back to the receiver's round-trip time when the candidate pair has none.
- 72334c5: Audio ducking stays on when the microphone moves to another camera after its camera is removed, and a media file that loops or is captured again stays ducked.
- 88694e8: The READMEs say that StreamCrafter is in beta and will change a lot, and describe the producer panel, the audio section and the bound web component sub-elements.
- 17e7159: A scene tile selects its scene with a native button (`.fw-sc-scene-select`, `aria-pressed` on the active scene) and keeps the delete button beside it instead of inside it, so screen readers announce both. The delete button has an accessible name and shows when it has keyboard focus.
- 7fb5951: `createStreamCrafter()` instances have `setAudioProcessing()` and `setAudioDucking()`, the same as the controller.
- Updated dependencies [30d2f3e]
  - @livepeer-frameworks/api@0.5.0

## 0.5.0

### Minor Changes

- 13bf3ae: `addLayer()` accepts `{ kind: "overlay" }`. Overlay layers keep their own position above the layout, and layout changes and source cycling leave them alone.
- 1ec53be: The producer panel adds logo and text overlays and local media files, and Add Camera lists your cameras so a second one can join the mix.

  Overlays are on `SceneManager` and on the Svelte store and React hook: `addImageOverlay()` takes a `File` or `Blob`, which stays in the browser, and `addTextOverlay()`, `updateTextOverlay()`, `setOverlayPlacement()`, `removeOverlay()` and `getOverlays()` manage them. Both add methods now take a placement (`x`, `y`, `size`) rather than a layer transform. Text bitmaps are as large as the text, so a lower third covers its band and not the frame.

  `addMediaFile()` plays a video or audio file through the compositor and mixer, with `playMediaSource()`, `pauseMediaSource()` and `setMediaSourceLoop()`. `addMediaFileSource()` now keeps a file that runs out in the mix instead of removing it. `getCameraChoices()` lists the video inputs. `startCamera()` opens no second microphone once one is in the mix, unless you pass `audio: true`.

- eae0c26: StreamCrafter has a producer panel for scenes, transitions and layers: `showProducerPanel` on Svelte and React, `show-producer-panel` on the web component, off by default. Svelte children get the compositor from the StreamCrafter context.

  Fades and slides now play on the WebGL and WebGPU renderers, and no longer flash the target scene when they start. `transitionTo()` resolves when the transition has finished, and a cut can interrupt a running transition. A layer no longer disappears when it is shown again in the solo layout, keeps its place in the stack when hidden and shown, and a removed source leaves every scene. `reorderLayers()` lays the active scene out again.

- 0621375: StreamCrafter no longer records in the browser; FrameWorks records on the server. Removed from core: `RecordingManager`, `WebMWriter` and their option types; on the controller and `createStreamCrafter`, `startRecording()`, `stopRecording()`, `pauseRecording()`, `resumeRecording()`, the `recording`, `recordingDuration` and `recordingFileSize` getters and the controller's `isRecordingActive()`, `getRecordingDuration()` and `getRecordingFileSize()`; the `recordingStarted`, `recordingStopped`, `recordingPaused`, `recordingResumed` and `recordingProgress` events; and `recording` from the reactive state. The React, Svelte and Web Component wrappers expose the controller through `getController()`, so the same members are gone there. `IngestMetadata.recordingEnabled` stays: it reports the stream's server-side recording setting.

### Patch Changes

- ffba554: The noise gate, de-esser, and sidechain ducker now measure levels on the audio thread, so they keep working while the tab is in the background. They used to stop whenever the browser paused animation frames. If AudioWorklet is unavailable, they fall back to a timer.
- 1ec53be: Half-transparent pixels in an image no longer turn dark on the WebGL renderer. A source added after an overlay stays under it, and a scene update during a layout animation is kept.

  `startCamera()` opens the picked camera when encoder overrides are set, and no longer swaps in another camera when the picked one cannot meet the constraints. A camera that fails to open leaves the sources already running ready to go live. `switchVideoDevice()` takes the source to switch, and with the compositor on it updates that source and leaves the composited output in place.

  An audio-only source no longer throws with the compositor on, and shows as visible in the mixer. Removing a source frees its last frame in the compositor worker and its renderer texture.

  The audio meter starts when the audio does, whichever comes first. In React, the compositor and producer panel come up without waiting for the first source.

- 0a0e26d: A failed Go Live or an exhausted reconnect now reports its reason through the `error` event and the banner, with `recoverable` set and the server's status kept (403, 503). Ending a session releases the encoder and connection, the same way `stopStreaming()` does.

  Reconnecting stays `reconnecting` until the attempts succeed or run out, so a late attempt can no longer run into a new session. Stop Streaming replaces Go Live during that time and ends the retries, and `stopStreaming()` no longer waits for a lost server before the state changes. A camera that fails with no source running leaves the studio ready with a recoverable error, in `idle` rather than `error`, so a desktop with no webcam can still share a screen, and `autoStartCamera` asks once. Add Camera and Share Screen stay available in the `error` state, so a session that ended with nothing to publish can be recovered.

  A failed camera or screen share emits one `error` event, with the device's message (`getUserMedia failed: ...`, `Screen capture failed: ...`), which is also what the state context carries.

  The first camera opens the microphone even when a media file or screen share already has audio, and the microphone moves to a remaining camera when the camera holding it is removed, staying muted if it was. A track replaced during a session reaches its sender even after that sender lost its track. A scene transition no longer brings a running layout animation's scene back. An SVG with only a `viewBox` can be used as an overlay. The noise gate, de-esser and ducker hear every channel of a stereo source.

  The drop-in components say when the browser cannot capture, publish or run the compositor, turn off Share Screen where `getDisplayMedia` is missing, and replace Add Media File with a reason where `captureStream` is missing. Add Camera says so when the cameras cannot be listed. Showing a source without a layer in `fw-streamcrafter` adds the layer. New exports: `detectStudioSupport`, `FULL_STUDIO_SUPPORT`, `isMediaFileCaptureSupported` and `StudioSupport` in core, `useStudioSupport` and `useCompositor().error` in React, and `error` on the Svelte compositor state.

- 2d35259: `transitionTo()` now plays the requested transition. It used to cut to the new scene whichever type you passed.
- 61902b6: A failed connection now returns to `capturing` when sources are still live, with the reason in the state context, so a broadcaster can go live again without re-adding sources. The state stays `error` when there is nothing to publish. The advanced panel masks the stream key in the WHIP URL it displays; copying still gives the full URL.

## 0.4.3

### Patch Changes

- Updated dependencies [f22d2b9]
  - @livepeer-frameworks/api@0.4.0

## 0.4.2

### Patch Changes

- Updated dependencies
  - @livepeer-frameworks/api@0.3.0

## 0.4.1

### Patch Changes

- Updated dependencies
  - @livepeer-frameworks/api@0.2.0

## 0.4.0

### Minor Changes

- Viewer and ingest resolution send the typed `ResolveViewerEndpoint` and `ResolveIngestEndpoint` documents from
  `@livepeer-frameworks/api`, now a dependency of the core packages. Each gateway's `serverInfo` is probed once per
  page: a gateway older than FrameWorks v0.3.11, or one without `serverInfo`, is refused with `ServerTooOldError`
  (`GatewayClient.resolve()` and `IngestClient.resolve()` reject with it, and the status event carries its message).
  Development builds and release candidates are not checked, and a probe that cannot reach the gateway does not block
  resolution. The `GatewayClient` and `IngestClient` APIs are unchanged.

---
"@livepeer-frameworks/streamcrafter-core": minor
"@livepeer-frameworks/streamcrafter-svelte": minor
"@livepeer-frameworks/streamcrafter-react": minor
"@livepeer-frameworks/streamcrafter-wc": minor
---

The producer panel adds logo and text overlays and local media files, and Add Camera lists your cameras so a second one can join the mix.

Overlays are on `SceneManager` and on the Svelte store and React hook: `addImageOverlay()` takes a `File` or `Blob`, which stays in the browser, and `addTextOverlay()`, `updateTextOverlay()`, `setOverlayPlacement()`, `removeOverlay()` and `getOverlays()` manage them. Both add methods now take a placement (`x`, `y`, `size`) rather than a layer transform. Text bitmaps are as large as the text, so a lower third covers its band and not the frame.

`addMediaFile()` plays a video or audio file through the compositor and mixer, with `playMediaSource()`, `pauseMediaSource()` and `setMediaSourceLoop()`. `addMediaFileSource()` now keeps a file that runs out in the mix instead of removing it. `getCameraChoices()` lists the video inputs. `startCamera()` opens no second microphone once one is in the mix, unless you pass `audio: true`.

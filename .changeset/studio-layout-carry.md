---
"@livepeer-frameworks/streamcrafter-core": patch
"@livepeer-frameworks/streamcrafter-react": patch
"@livepeer-frameworks/streamcrafter-svelte": patch
"@livepeer-frameworks/streamcrafter-wc": patch
---

The drop-in studios keep the current layout when they build a new controller for the same stream, such as after a profile, `debug` or endpoint change, instead of going back to `initialLayout`; they start from `initialLayout` again only when it changes to another object, which marks another stream. The controller's `exportLayout()` now returns the layout the compositor had when it was last turned off or destroyed, and the layout is read at the time of the call. A picked image larger than the output frame is stored at frame size and encoded once, so reporting a layout after every change stays cheap. `importLayout()` refuses an unknown layout mode or text overlay field before it changes anything. A failing `onLayoutChange` callback is now logged apart from a failed export. In React, the Encoder section follows the `qualityProfile` given to `StudioSettings`. The web component also starts the compositor on a controller built while the previous one was still starting. The toggle knobs in the advanced panel use the foreground token of their track, so they show in the light themes.

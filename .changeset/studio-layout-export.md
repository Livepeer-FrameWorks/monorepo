---
"@livepeer-frameworks/streamcrafter-core": minor
"@livepeer-frameworks/streamcrafter-react": minor
"@livepeer-frameworks/streamcrafter-svelte": minor
"@livepeer-frameworks/streamcrafter-wc": minor
---

The studio layout can be saved and restored. `exportLayout()` returns the scenes, their image and text overlays and the layout preset as plain data (a picked image travels as a data URL), and `importLayout()` applies it; both are on `SceneManager`, the controller, the vanilla studio, React `useCompositor`, the Svelte compositor store and `<fw-streamcrafter>`. The drop-in components take `initialLayout` and report changes through `onLayoutChange` (and the `fw-sc-layout-change` event on the web component), so an app can keep a layout per stream. Turning `enableCompositor` off and on no longer needs a remount and keeps the layout, and a new `compositorConfig.renderer` switches the running compositor. In React the compositor also moves to the new controller when the endpoint changes.

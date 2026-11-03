---
"@livepeer-frameworks/streamcrafter-core": minor
"@livepeer-frameworks/streamcrafter-react": minor
"@livepeer-frameworks/streamcrafter-svelte": minor
"@livepeer-frameworks/streamcrafter-wc": minor
---

A studio can switch streams without losing its sources or carrying one stream's scenes to another. The drop-ins take a new `layoutKey` prop (`layout-key` attribute on `<fw-streamcrafter>`) that names the stream the layout belongs to. When it changes, the running studio shows that key's `initialLayout`, or the default layout when there is none or it does not apply. Before, a switch between two streams that both had no saved layout kept the previous stream's scenes. `onLayoutChange` now receives `(layout, layoutKey)`, and `fw-sc-layout-change` carries `{ layout, layoutKey }`. A report exported for the previous key is dropped, including one whose export was still running, so it cannot be saved under the next stream's key. A new `whipUrl` now goes to the running controller instead of rebuilding it, so camera, screen and media sources keep running; a broadcast that is live when `whipUrl` changes is stopped, and the next one goes to the new endpoint. Without `layoutKey`, a new `initialLayout` object no longer counts as another stream.

Core adds `IngestControllerV2.setWhipUrl()` (also on the vanilla studio and `StreamCrafterV2`), `createDefaultStudioLayout()`, and `StudioLayoutKeeper`, which the drop-ins share for these rules. React's `useCompositor` and Svelte's `createCompositorStore` take a `layoutKeeper` in place of `initialLayout`, and return `followLayoutKey()` to apply a key change to the running compositor.

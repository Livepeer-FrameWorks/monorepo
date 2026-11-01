---
"@livepeer-frameworks/streamcrafter-react": patch
"@livepeer-frameworks/streamcrafter-svelte": patch
"@livepeer-frameworks/streamcrafter-wc": patch
---

`LayerList` now calls `onReorder` with layer IDs bottom to top, the order `reorderLayers` takes. Moving a layer used to reverse the whole stack. If you reversed the IDs in your own handler, remove that.

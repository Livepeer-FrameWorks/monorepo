---
"@livepeer-frameworks/streamcrafter-core": minor
"@livepeer-frameworks/streamcrafter-svelte": minor
"@livepeer-frameworks/streamcrafter-react": minor
"@livepeer-frameworks/streamcrafter-wc": minor
---

StreamCrafter has a producer panel for scenes, transitions and layers: `showProducerPanel` on Svelte and React, `show-producer-panel` on the web component, off by default. Svelte children get the compositor from the StreamCrafter context.

Fades and slides now play on the WebGL and WebGPU renderers, and no longer flash the target scene when they start. `transitionTo()` resolves when the transition has finished, and a cut can interrupt a running transition. A layer no longer disappears when it is shown again in the solo layout, keeps its place in the stack when hidden and shown, and a removed source leaves every scene. `reorderLayers()` lays the active scene out again.

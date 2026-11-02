---
"@livepeer-frameworks/streamcrafter-core": minor
"@livepeer-frameworks/streamcrafter-react": minor
"@livepeer-frameworks/streamcrafter-svelte": minor
"@livepeer-frameworks/streamcrafter-wc": minor
---

`]` and `[` switch to the next and previous scene in every drop-in studio, as they already did in the vanilla studio; a key typed into a field is left alone. Scene tiles take keyboard focus and switch with Enter or Space. The layer list in the producer panel removes a layer and edits its opacity. Core exports `handleStudioSceneHotkey()`, `adjacentSceneId()` and `isTypingTarget()`.

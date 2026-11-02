---
"@livepeer-frameworks/streamcrafter-core": minor
---

`audioMixing` defaults to true for `IngestControllerV2` and `createStreamCrafter()`, as it already was in the React, Svelte and web component studios. Master volume, levels, media file audio and the audio processing chain work without setting it. Pass `audioMixing: false` to skip the Web Audio mixer.

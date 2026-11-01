---
"@livepeer-frameworks/streamcrafter-core": patch
---

The noise gate, de-esser, and sidechain ducker now measure levels on the audio thread, so they keep working while the tab is in the background. They used to stop whenever the browser paused animation frames. If AudioWorklet is unavailable, they fall back to a timer.

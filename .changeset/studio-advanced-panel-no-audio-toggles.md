---
"@livepeer-frameworks/streamcrafter-core": minor
"@livepeer-frameworks/streamcrafter-react": minor
"@livepeer-frameworks/streamcrafter-svelte": minor
"@livepeer-frameworks/streamcrafter-wc": minor
---

The echo cancellation, noise suppression and auto gain toggles live only in the Audio section; the dev-mode advanced panel no longer repeats them and shows the capture format instead. `AdvancedPanel` (Svelte) drops its `audioProcessing` and `onAudioProcessingChange` props, and `<fw-sc-advanced>` drops its `audioProcessing` property and no longer fires `fw-audio-processing-change`. `AudioProcessingSettings` is now a core type; the Svelte package still re-exports it, and `<fw-sc-audio-controls>` takes it for `browserProcessing` in place of `BrowserAudioProcessing`.

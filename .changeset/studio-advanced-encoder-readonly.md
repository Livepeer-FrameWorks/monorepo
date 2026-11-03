---
"@livepeer-frameworks/streamcrafter-core": patch
"@livepeer-frameworks/streamcrafter-react": patch
"@livepeer-frameworks/streamcrafter-svelte": patch
"@livepeer-frameworks/streamcrafter-wc": patch
---

The advanced panel's Encoder block now shows the effective encoder config read-only: video codec preference, resolution, frame rate, bitrates and audio codec, with each value that differs from the quality profile marked as overridden and a pointer to Settings. The Encoder section in `StudioSettings` is the one place to change these. The Svelte `AdvancedPanel` drops its `onEncoderOverridesChange` prop and `<fw-sc-advanced>` no longer fires `fw-encoder-overrides-change`. Core exports `encoderSummaryRows()` for the read-only rows. The Spanish, French, German and Dutch packs now translate the encoder labels, including the codec's Auto choice. The web component README's `setEncoderOverrides` example now uses the `{ video: { bitrate } }` shape that `EncoderOverrides` takes.

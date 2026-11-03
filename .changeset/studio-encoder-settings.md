---
"@livepeer-frameworks/streamcrafter-core": minor
"@livepeer-frameworks/streamcrafter-react": minor
"@livepeer-frameworks/streamcrafter-svelte": minor
"@livepeer-frameworks/streamcrafter-wc": minor
---

The settings popup has an Encoder section in every binding, outside dev mode: resolution, frame rate, video and audio bitrate, and the video codec when the browser can send more than one. A choice equal to the quality profile clears the override, Reset to Profile clears them all, and the settings lock while streaming. `EncoderOverrides.video.codec` offers the chosen codec to the server first and takes the place of the `videoCodec` option. Core exports the choices (`encoderSettingRows`, `withEncoderSetting`, `sendableVideoCodecs`) so custom UIs offer the same ones, and React `useStreamCrafterV2` now returns `encoderOverrides`.

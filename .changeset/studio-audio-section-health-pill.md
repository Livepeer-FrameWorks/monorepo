---
"@livepeer-frameworks/streamcrafter-core": patch
"@livepeer-frameworks/streamcrafter-react": minor
"@livepeer-frameworks/streamcrafter-svelte": minor
"@livepeer-frameworks/streamcrafter-wc": minor
---

The studio has an Audio section while capturing, without dev mode: echo cancellation, noise suppression and auto gain for the microphones, and a noise gate with its threshold, a de-esser, a three-band EQ and ducking of other sources under the camera microphone. While live, a pill next to the status badge grades the connection as good, unstable or poor, with the send rate, packet loss and round-trip time as its title. New `StudioAudioControls` and `StudioConnectionHealth` on React and Svelte, and `<fw-sc-audio-controls>` and `<fw-sc-connection-health>` on the web component.

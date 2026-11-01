---
"@livepeer-frameworks/streamcrafter-core": patch
"@livepeer-frameworks/streamcrafter-react": patch
---

Half-transparent pixels in an image no longer turn dark on the WebGL renderer. A source added after an overlay stays under it, and a scene update during a layout animation is kept.

`startCamera()` opens the picked camera when encoder overrides are set, and no longer swaps in another camera when the picked one cannot meet the constraints. A camera that fails to open leaves the sources already running ready to go live. `switchVideoDevice()` takes the source to switch, and with the compositor on it updates that source and leaves the composited output in place.

An audio-only source no longer throws with the compositor on, and shows as visible in the mixer. Removing a source frees its last frame in the compositor worker and its renderer texture.

The audio meter starts when the audio does, whichever comes first. In React, the compositor and producer panel come up without waiting for the first source.

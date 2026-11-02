---
"@livepeer-frameworks/streamcrafter-wc": minor
---

New elements `<fw-sc-producer-panel>`, `<fw-sc-camera-picker>`, `<fw-sc-overlay-controls>`, `<fw-sc-media-controls>` and `<fw-sc-meter>`, which `<fw-streamcrafter>` also uses itself. Each binds to the studio whose id is in `for`, or to the `<fw-streamcrafter>` it sits in, and finds a studio that mounts after it. With `controls="false"` they build a custom layout around the studio.

`<fw-streamcrafter>` renders again when scenes, layers or transitions change through its scene manager, and no longer renders at the audio meter's rate while capturing. It exposes `t`, `support` and `sceneManager` for the sub-elements.

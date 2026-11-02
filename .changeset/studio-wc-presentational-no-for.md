---
"@livepeer-frameworks/streamcrafter-wc": minor
---

`<fw-sc-volume>`, `<fw-sc-compositor>`, `<fw-sc-scene-switcher>`, `<fw-sc-layer-list>` and `<fw-sc-advanced>` no longer declare a `for` attribute, which did nothing; `FwScVolume.resolveStudio()` is gone with it. These elements are presentational: set their data through JS properties. For a studio-bound scene switcher and layer list, use `<fw-sc-producer-panel>`.

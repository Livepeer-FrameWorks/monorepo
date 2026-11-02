---
"@livepeer-frameworks/streamcrafter-core": patch
"@livepeer-frameworks/streamcrafter-react": patch
"@livepeer-frameworks/streamcrafter-svelte": patch
"@livepeer-frameworks/streamcrafter-wc": patch
---

A scene tile selects its scene with a native button (`.fw-sc-scene-select`, `aria-pressed` on the active scene) and keeps the delete button beside it instead of inside it, so screen readers announce both. The delete button has an accessible name and shows when it has keyboard focus.

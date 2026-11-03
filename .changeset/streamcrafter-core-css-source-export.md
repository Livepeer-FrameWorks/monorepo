---
"@livepeer-frameworks/streamcrafter-core": patch
---

The `./streamcrafter.css`, `./styles.css` and `./styles/streamcrafter.css` exports and the `style` field point at the shipped `src/styles/streamcrafter.css`, which is byte-identical to the `dist/` copy, so a CSS `@import` of it resolves from a workspace checkout that has not been built.

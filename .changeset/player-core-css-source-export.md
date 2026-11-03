---
"@livepeer-frameworks/player-core": patch
---

The `./player.css`, `./styles.css` and `./themes/*.css` exports and the `style` field point at the shipped `src/styles/` stylesheets, which are byte-identical to the `dist/` copies, so a CSS `@import` of them resolves from a workspace checkout that has not been built.

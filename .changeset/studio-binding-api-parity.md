---
"@livepeer-frameworks/streamcrafter-react": minor
"@livepeer-frameworks/streamcrafter-svelte": minor
"@livepeer-frameworks/streamcrafter-wc": minor
---

The bindings match on three APIs. React `StreamCrafter` and the `<fw-streamcrafter>` `resolveWhipUrl` property take a resolver that returns the destination right before each connection, as the Svelte component already did. The `<fw-streamcrafter>` `theme` property takes an object of token overrides as well as a preset name. The Svelte compositor store has `setRenderer()`, like the React `useCompositor`.

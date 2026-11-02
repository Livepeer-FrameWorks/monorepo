---
"@livepeer-frameworks/streamcrafter-core": minor
"@livepeer-frameworks/streamcrafter-react": patch
"@livepeer-frameworks/streamcrafter-svelte": patch
"@livepeer-frameworks/streamcrafter-wc": patch
---

Copy Stream Info masks the stream key in the WHIP URL it copies, and the menu item that copies the full URL says that it contains the stream key. The context menu and the quality popup take their colours from the studio theme tokens, so a themed studio no longer shows Tokyo Night colours there. Core exports `formatStreamInfo()`, and the `.fw-sc-portal` class gives the theme tokens to popups rendered outside the studio root. `StudioSettings` takes `defaultOpen` and extra sections as children.

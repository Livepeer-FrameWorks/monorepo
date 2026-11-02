---
"@livepeer-frameworks/streamcrafter-react": minor
"@livepeer-frameworks/streamcrafter-svelte": minor
"@livepeer-frameworks/streamcrafter-wc": minor
---

The producer panel is on by default whenever the compositor runs. Turn it off with `showProducerPanel={false}` on React and Svelte, or `show-producer-panel="false"` on the web component. On the web component, `enable-compositor="false"` now turns the compositor off; before, any value of the attribute left it on.

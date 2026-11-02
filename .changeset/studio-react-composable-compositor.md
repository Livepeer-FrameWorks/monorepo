---
"@livepeer-frameworks/streamcrafter-react": minor
---

Composable children of `<StreamCrafter>` get its compositor from the context, so `<StudioProducerPanel />` works without props. `StudioOverlayControls` and `StudioMediaControls` are exported, like on Svelte. `useStreamCrafterContext()` returns a `StreamCrafterContextValue` with a `compositor` field.

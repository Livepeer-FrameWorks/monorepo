---
"@livepeer-frameworks/streamcrafter-react": minor
"@livepeer-frameworks/streamcrafter-svelte": minor
"@livepeer-frameworks/streamcrafter-wc": minor
---

Add Media File sits in the action bar next to Add Camera and Share Screen, in the drop-in UI and in `StudioActionBar`. A browser that cannot play a file into the stream gets the button turned off with the reason as its title. The media section of the producer panel lists the files with their playback controls. New `MediaFileButton` on React and Svelte, and `<fw-sc-media-button>` on the web component. `StudioMediaControls` no longer takes `addMediaFile`.

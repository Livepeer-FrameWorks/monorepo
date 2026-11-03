---
"@livepeer-frameworks/player-svelte": minor
---

`<Player bind:controller>` and `getController()` on the component instance give the `PlayerController` of the current content, as a ref does on the React `Player`. The bound value changes when the content changes and is null once the player is torn down.

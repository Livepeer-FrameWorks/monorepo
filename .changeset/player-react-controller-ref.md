---
"@livepeer-frameworks/player-react": minor
---

`<Player>` forwards a `ref` to its `PlayerController`. The ref receives the controller once it exists and `null` after it is destroyed, so a page can read stats, tracks and events from the same controller the player uses, as `controller` already allows on `<fw-player>`.

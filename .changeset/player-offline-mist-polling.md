---
"@livepeer-frameworks/player-core": patch
---

A pre-resolved endpoint on a Mist edge keeps polling stream state when Mist answers with an error such as "Stream is offline", so the player picks the stream up when it comes online.

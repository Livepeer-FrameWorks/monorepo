---
"@livepeer-frameworks/player-core": patch
---

With pre-resolved `endpoints`, the player rules out Mist only when the edge answers `json_<name>.js` with HTTP 404 or a reply that is not Mist JSON. A network error or another HTTP error repeats the lookup with backoff; once Mist answers, the player takes that edge's sources and tracks and starts stream-state polling. A Mist `Could not retrieve stream` reply, which Mist sends for a name it does not know, plays the URL without opening stream-state or metadata sockets for that name.

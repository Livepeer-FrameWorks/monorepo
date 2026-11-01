---
"@livepeer-frameworks/player-core": patch
---

Pre-resolved endpoints on a host without MistServer no longer open Mist stream-state or metadata sockets. Set `baseUrl` to keep both on a known Mist edge.

---
"@livepeer-frameworks/player-core": patch
---

`PlayerController.attach()` and `registerPlayer()` no longer start a background load of every built-in player module into the global player manager. Players load only for the stream being initialized, on the controller's own manager, inside `initializePlayer`, so a failed player chunk no longer surfaces as an unhandled promise rejection and no load keeps running after the controller is destroyed.

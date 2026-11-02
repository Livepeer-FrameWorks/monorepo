---
"@livepeer-frameworks/streamcrafter-core": minor
---

`IngestControllerV2.setAudioProcessing()` sets the noise gate, EQ and de-esser on the mixed audio; settings made before capture are kept and applied once the mixer runs. `setAudioDucking(true)` lowers every other source while the first camera's microphone has signal, and follows sources as they come and go. Ducking no longer overwrites a source's volume, and a source ducked when ducking is turned off returns to full level. `assessConnectionHealth()` grades the publish connection from its stats.

`IngestStats.connection.rtt` is now in milliseconds, as the advanced panels already displayed it; it was in seconds.

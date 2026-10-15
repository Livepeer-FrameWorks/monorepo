---
"@livepeer-frameworks/api": patch
---

`BufferState` gains `UNKNOWN`. `RebufferingEvent.previousState` is `UNKNOWN` for a stream's first buffer transition on a node, which has no earlier transition; it used to read `RECOVER`. A stream that ends while rebuffering now reports a `DRY` to `EMPTY` transition with `rebufferEnd: true`, so every rebuffer start has an end.

---
"@livepeer-frameworks/streamcrafter-core": patch
---

`IngestStats` reports packet loss from the receiver's reports (`remote-inbound-rtp`), which it read as 0 before, and adds `connection.packetLossPct`, the loss over the last stats interval. The connection health pill grades on that interval loss, so it follows the link as it is now, and falls back to the receiver's round-trip time when the candidate pair has none.
